package proxy

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/sou7/loopguard/internal/detect"
)

// generateCounterPattern returns a `length`-byte string with a minimal
// period equal to `length` itself (i.e., no shorter period exists), so that
// when the string is repeated, KMP computes period = length. This is achieved
// by filling the string with a sequential byte counter that produces no
// self-overlapping prefix-suffix structure.
func generateCounterPattern(length int) string {
	buf := make([]byte, length)
	for i := 0; i < length; i++ {
		buf[i] = byte('A' + (i % 26))
	}
	return string(buf)
}

// parsePort extracts the numeric port from an httptest server URL.
func parsePort(rawurl string) int {
	u, err := url.Parse(rawurl)
	if err != nil {
		return 0
	}
	host := u.Host
	if idx := strings.LastIndex(host, ":"); idx >= 0 {
		host = host[idx+1:]
	}
	var n int
	for _, c := range host {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}

// startMockSSE spawns a mock SSE child that emits `pattern` repeated `repeats`
// times as delta content on chat-completions, then [DONE].
func startMockSSE(t *testing.T, pattern string, repeats int) (int, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming not supported", http.StatusInternalServerError)
			return
		}
		flusher.Flush()
		for i := 0; i < repeats; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}]}\n\n", pattern)
			flusher.Flush()
		}
		fmt.Fprintf(w, "data: [DONE]\n\n")
	}))
	return parsePort(server.URL), server
}

// TestLoopDetectionEndToEnd verifies the proxy detects a loop from a mock
// child and emits the spec §6 response (finish_reason: "length").
func TestLoopDetectionEndToEnd(t *testing.T) {
	// 160-byte counter pattern (minimal period = 160). Repeated 4 times
	// in the mock server. The proxy should detect at the 3rd repetition
	// (window = 480 bytes, period = 160, redundant bytes = 160 * 2 = 320 > 250).
	pattern := generateCounterPattern(160)
	childPort, childServer := startMockSSE(t, pattern, 4)
	defer childServer.Close()

	p := New(childPort, 1, 4000, 250)

	reqBody := `{"model":"test","messages":[],"stream":true}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(reqBody)))
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, req)

	result := rr.Body.String()
	if !strings.Contains(result, `"finish_reason":"length"`) {
		t.Errorf("expected finish_reason:length in response, got: %s", result)
	}
}

// TestLoopDetectionNonStreamingE2E verifies non-streaming clients get a
// complete JSON response (spec §6.2) on loop detection.
func TestLoopDetectionNonStreamingE2E(t *testing.T) {
	pattern := generateCounterPattern(160)
	childPort, childServer := startMockSSE(t, pattern, 4)
	defer childServer.Close()

	p := New(childPort, 1, 4000, 250)

	// stream omitted (defaults false).
	reqBody := `{"model":"test","messages":[]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(reqBody)))
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, req)

	result := rr.Body.String()
	if !strings.Contains(result, `"finish_reason":"length"`) {
		t.Errorf("expected finish_reason:length, got: %s", result)
	}
}

// TestPassThroughNonGeneration verifies non-generation paths are proxied
// verbatim (spec §1 - transparency).
func TestPassThroughNonGeneration(t *testing.T) {
	childServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprintf(w, "passthrough-body for %s", r.URL.Path)
	}))
	defer childServer.Close()

	port := parsePort(childServer.URL)
	p := New(port, 1, 4000, 250)

	for _, path := range []string{"/", "/props", "/slots", "/metrics"} {
		req := httptest.NewRequest("GET", path, nil)
		rr := httptest.NewRecorder()
		p.ServeHTTP(rr, req)
		if rr.Code != http.StatusOK {
			t.Errorf("path %s: got status %d, want 200", path, rr.Code)
		}
		if !strings.Contains(rr.Body.String(), "passthrough-body") {
			t.Errorf("path %s: unexpected body: %s", path, rr.Body.String())
		}
	}
}

// TestLoopDetectionNoFalsePositive verifies non-repeating streams don't
// trigger loop detection.
func TestLoopDetectionNoFalsePositive(t *testing.T) {
	childServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flush", http.StatusInternalServerError)
			return
		}
		flusher.Flush()
		// 5 unique chunks — no repetition.
		for i := 0; i < 5; i++ {
			fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":\"chunk%d\"}}]}\n\n", i)
			flusher.Flush()
		}
		fmt.Fprintf(w, "data: [DONE]\n\n")
	}))
	defer childServer.Close()

	port := parsePort(childServer.URL)
	p := New(port, 1, 4000, 250)

	reqBody := `{"model":"test","stream":true}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(reqBody)))
	rr := httptest.NewRecorder()
	p.ServeHTTP(rr, req)

	result := rr.Body.String()
	if strings.Contains(result, `"finish_reason":"length"`) {
		t.Errorf("should not detect loop on non-repeating content, got: %s", result)
	}
}

// TestDetectLoop_RealisticScenario directly tests DetectLoop with delta text
// (not full JSON), matching spec §4.1 ("累積デルタテキスト(バイト列)").
func TestDetectLoop_RealisticScenario(t *testing.T) {
	pattern := generateCounterPattern(160)
	window := []byte{}
	for i := 0; i < 3; i++ {
		window = append(window, []byte(pattern)...)
	}
	// min=1, max=4000, threshold=250. Period=160, reps=3.
	// Redundant bytes: 160 * 2 = 320 > 250. Should detect.
	if !detect.DetectLoop(window, 1, 4000, 250) {
		t.Fatal("expected loop detected with 3 repetitions of 160-byte chunk")
	}
}
