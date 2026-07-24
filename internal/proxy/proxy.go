// Package proxy implements the reverse-proxy layer of loopguard.
//
// Non-generation paths are proxied verbatim via httputil.ReverseProxy
// (specification §1, "完全に透過する"). Generation paths (see constants.go)
// are intercepted so that their SSE stream can be inspected for loops in
// real time (specification §4, §5).
package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/sou7/loopguard/internal/detect"
	"github.com/sou7/loopguard/internal/sse"
)

const (
	childScheme = "http"
	childHost   = "127.0.0.1"

	// finishReasonForced is the finish_reason value sent to clients when a
	// loop is detected (specification §6, "コード内定数、フラグ化しない").
	finishReasonForced = "length"

	// windowBufSize is the maximum accumulated delta bytes retained per
	// request. The effective upper bound on detectLoop's working window is
	// max-period-bytes * 2 (enough to catch a long period after 2-3 repeats,
	// since redundant-volume detection needs period + at least one full repeat).
	windowBufSize = 1 << 15 // 32 KiB; ample headroom over the worst case.
)

// Proxy is the central loopguard HTTP handler. It wraps both the pass-through
// ReverseProxy and the interception logic for generation endpoints.
type Proxy struct {
	childURL *url.URL
	// ReverseProxy for pass-through (non-generation) requests
	rp *httputil.ReverseProxy

	// Loop-detection configuration.
	minPeriod   int
	maxPeriod   int
	threshold   int // redundant repetitions bytes before cut-off
}

// New creates a Proxy that forwards to the child server at the given port.
func New(childPort int, minPeriod, maxPeriod, threshold int) *Proxy {
	u := &url.URL{
		Scheme: childScheme,
		Host:   fmt.Sprintf("%s:%d", childHost, childPort),
	}
	p := &Proxy{
		childURL:  u,
		minPeriod: minPeriod,
		maxPeriod: maxPeriod,
		threshold: threshold,
	}
	// Standard reverse proxy for transparent pass-through.
	// WebSocket upgrades are handled automatically by Go's ReverseProxy
	// (specification §1, footnote on Web UI).
	// We use the classic ReverseProxy with a Director instead of the
	// (Go 1.24+) NewSingleTargetProxy constructor, since the older API is
	// universally available.
	rp := &httputil.ReverseProxy{
		Director:  singleTargetHTTP(u),
		Transport: defaultTransport(),
	}
	p.rp = rp
	return p
}

// singleTargetHTTP returns a Director that rewrites the request to the
// given *url.URL, preserving path and query like the modern
// NewSingleTargetProxy constructor.
func singleTargetHTTP(u *url.URL) func(*http.Request) {
	return func(r *http.Request) {
		r.URL.Scheme = u.Scheme
		r.URL.Host = u.Host
		// Path and RawQuery are left intact: Go's ReverseProxy (unlike the
		// new single-target proxy) does NOT preserve them by default.
	}
}

// defaultTransport returns an HTTP transport suitable for reaching the
// local child server. It is a normal transport with connection reuse and
// reasonable timeouts.
func defaultTransport() *http.Transport {
	t := http.DefaultTransport.(*http.Transport).Clone()
	t.DisableKeepAlives = false
	return t
}

// ServeHTTP dispatches to the appropriate handler. Generation endpoints
// (IsGenerated) are intercepted; all others are passed through transparently.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if IsGenerated(r.Method, r.URL.Path) {
		p.handleGeneration(w, r)
		return
	}
	p.rp.ServeHTTP(w, r)
}

// handleGeneration implements specification §5, §4, §6 for the four
// generation endpoints.
func (p *Proxy) handleGeneration(w http.ResponseWriter, r *http.Request) {
	// 5.1: Parse request body as JSON, capture the original stream flag.
	body, clientWantsStream, err := rewriteBodyForChild(r)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusBadRequest)
		return
	}

	// Build a request to the child with stream forced to true.
	childReq, err := p.buildChildRequest(r, body)
	if err != nil {
		http.Error(w, fmt.Sprintf(`{"error":"%s"}`, err.Error()), http.StatusInternalServerError)
		return
	}

	// 5.4: Use a cancellable context for the upstream request so that loop
	// detection can abort the upstream connection immediately.
	reqCtx, cancel := context.WithCancel(r.Context())
	defer cancel()
	childReq = childReq.WithContext(reqCtx)

	// 5.2/5.3: Stream the child's SSE response.
	resp, err := childHTTPClient.Do(childReq)
	if err != nil {
		// Spec §1: during child startup, connection failure yields 502
		// automatically (ReverseProxy default). We replicate this for
		// generation paths too.
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		// If for some reason the child did not stream, just copy the body.
		// Flush after each chunk to minimize latency.
		flusher, _ := w.(http.Flusher)
		buf := make([]byte, 4096)
		for {
			n, err := resp.Body.Read(buf)
			if n > 0 {
				w.Write(buf[:n])
				if flusher != nil {
					flusher.Flush()
				}
			}
			if err != nil {
				break
			}
		}
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	// Prevent intermediate proxies/servers from buffering the stream.
	w.Header().Set("X-Accel-Buffering", "no")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)

	p.streamWithLoopDetection(w, r, resp.Body, clientWantsStream, cancel)
}

// streamWithLoopDetection reads SSE chunks from the child, appends delta
// bytes to the window, runs detectLoop on each append, and relays or
// accumulates according to clientWantsStream. On loop detection it cancels
// the upstream request and emits the spec §6 response.
func (p *Proxy) streamWithLoopDetection(
	w http.ResponseWriter,
	r *http.Request,
	reader io.Reader,
	clientWantsStream bool,
	cancel context.CancelFunc,
) {
	// maxWindowBytes caps the window used by detectLoop.
	maxWindowBytes := p.maxWindow()
	window := make([]byte, 0, windowBufSize)

	// bufio.Reader is used to scan the SSE line stream efficiently.
	br := bufio.NewReader(reader)

	// For non-streaming clients, we accumulate the full text and assemble a
	// complete JSON response at the end. We track the "delta content" via
	// the SSE data payloads (specification §5.196-203).
	var accumulatedText strings.Builder

	for {
		line, err := br.ReadString('\n')
		if len(line) > 0 {
			line = strings.TrimRight(line, "\r\n")
		} else if err == io.EOF {
			break
		}
		if err != nil && err != io.EOF {
			break
		}

		ok, data := sse.ParseLine(line)
		if !ok {
			continue
		}

		// Per spec §5.196-203: skip the terminal [DONE] marker; it is handled
		// by the end-of-stream logic below.
		if sse.IsDoneMarker(data) {
			break
		}

		delta := sse.ExtractDelta(data)

		// 4.2/3: Append raw delta text to the window and trim to W bytes.
		window = append(window, []byte(delta)...)
		if len(window) > maxWindowBytes {
			keep := maxWindowBytes
			copy(window, window[len(window)-keep:])
			window = window[:keep]
		}

		// 4.2: Detect loop on each new chunk.
		if detect.DetectLoop(window, p.minPeriod, p.maxPeriod, p.threshold) {
			log.Println("loopguard: loop detected, terminating stream")
			cancel() // 5.4: abort the upstream request.
			p.emitLoopTermination(w, r, accumulatedText, clientWantsStream)
			return
		}

		if clientWantsStream {
			// 5.3: Relay chunk verbatim as SSE.
			fmt.Fprintf(w, "data: %s\n\n", data)
			// Flush immediately so clients receive tokens without delay.
			if f, ok := w.(http.Flusher); ok {
				f.Flush()
			}
		} else {
			// 5.3: Accumulate internally only.
			accumulatedText.WriteString(delta)
		}
	}

	// 5.5/6: Normal end-of-stream.
	if clientWantsStream {
		// Relay the final [DONE].
		fmt.Fprintf(w, "data: [DONE]\n\n")
		// Flush the final marker immediately.
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	} else {
		// 5.5/6.2: Assemble a non-streaming JSON response from accumulated text.
		assemble := buildNonStreamingResponse(r, accumulatedText.String(), finishReasonForced)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, assemble)
	}
}

// emitLoopTermination sends the spec §6 response to the client given the
// text accumulated so far.
func (p *Proxy) emitLoopTermination(w http.ResponseWriter, r *http.Request,
	accumulated strings.Builder, clientWants bool) {
	if clientWants {
		// 6.2: Send final chunk with finish_reason: "length", then [DONE].
		fmt.Fprintf(w, "data: {\"choices\":[{\"finish_reason\":%q}]}, \n\n", finishReasonForced)
		fmt.Fprintf(w, "data: [DONE]\n\n")
		// Flush the termination chunks immediately.
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
	} else {
		// 6.2: Assemble non-streaming JSON shape.
		resp := buildNonStreamingResponse(r, accumulated.String(), finishReasonForced)
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, resp)
	}
}

// maxWindow returns the effective byte window size used for loop detection.
// The window must be large enough to hold a full period plus enough redundant
// repetitions to exceed the threshold. We use maxPeriod + threshold as a safe
// upper bound.
func (p *Proxy) maxWindow() int {
	return p.maxPeriod + p.threshold
}

// childHTTPClient is a shared *http.Client for reaching the child process.
// It avoids per-request overhead and reuses connections.
var childHTTPClient = &http.Client{
	Transport: &http.Transport{
		// Reuse connections where possible.
		DisableKeepAlives: false,
	},
}

// buildChildRequest constructs the outbound request to the child, forcing
// stream=true regardless of the original client's request, per spec §5.2.
func (p *Proxy) buildChildRequest(r *http.Request, body []byte) (*http.Request, error) {
	u := *p.childURL
	u.Path = r.URL.Path
	u.RawQuery = r.URL.Query().Encode()

	req := &http.Request{
		Method: r.Method,
		URL:    &u,
		Header: r.Header,
		Body:   io.NopCloser(bytes.NewReader(body)),
		Host:   u.Host,
	}
	req.Header.Set("Content-Type", "application/json")
	return req, nil
}

// rewriteBodyForChild unmarshals the incoming JSON body, forces stream=true,
// and re-serializes. Returns the raw bytes and the (original) client stream flag.
func rewriteBodyForChild(r *http.Request) ([]byte, bool, error) {
	raw, err := io.ReadAll(r.Body)
	if err != nil {
		return nil, false, err
	}
	var body map[string]json.RawMessage
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &body); err != nil {
			// Non-JSON body: pass through unchanged.
			return raw, false, nil
		}
	}
	// Determine original stream value.
	clientWantsStream := false
	if rawStream, ok := body["stream"]; ok {
		var v bool
		if err := json.Unmarshal(rawStream, &v); err == nil {
			clientWantsStream = v
		}
	}

	// Force stream=true.
	body["stream"] = json.RawMessage("true")

	out, err := json.Marshal(body)
	if err != nil {
		return nil, clientWantsStream, err
	}
	return out, clientWantsStream, nil
}

// buildNonStreamingResponse assembles a complete JSON response for a
// non-streaming client that was cut short by loop detection.
func buildNonStreamingResponse(r *http.Request, text string, finishReason string) string {
	// Escape text as a JSON string.
	escaped := jsonStringEscape(text)
	switch r.URL.Path {
	case "/completion", "/infill":
		return fmt.Sprintf(`{"choices":[{"text":%s}],"stop":"%s"}`, escaped, finishReason)
	case "/v1/completions":
		return fmt.Sprintf(`{"id":"chatcmocore-123","object":"text_completion","choices":[{"text":%s,"index":0,"finish_reason":"%s"}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`,
			escaped, finishReason)
	case "/v1/chat/completions":
		return fmt.Sprintf(`{"id":"chatcmocore-123","object":"chat.completion","choices":[{"index":0,"finish_reason":"%s","message":{"role":"assistant","content":%s}}],"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0}}`,
			finishReason, escaped)
	default:
		// Fallback: return a generic shape.
		return fmt.Sprintf(`{"choices":[{"finish_reason":"%s","text":%s}]}`, finishReason, escaped)
	}
}

// jsonStringEscape returns a JSON-encoded string literal for s (the value,
// including the surrounding quotes).
func jsonStringEscape(s string) string {
	b, err := json.Marshal(s)
	if err != nil {
		// Fallback: empty string.
		return `""`
	}
	return string(b)
}
