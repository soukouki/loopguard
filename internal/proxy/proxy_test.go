package proxy

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sou7/loopguard/internal/sse"
)

// TestRewriteBodyForChild_ForcesStream verifies that rewriteBodyForChild
// forces stream=true regardless of the original value (specification §5.2).
func TestRewriteBodyForChild_ForcesStream(t *testing.T) {
	// Streaming request body.
	body := `{"model":"x","stream":false,"messages":[]}`
	req := httptest.NewRequest("POST", "/v1/chat/completions", bytes.NewReader([]byte(body)))
	out, clientWantsStream, err := rewriteBodyForChild(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if clientWantsStream != false {
		t.Errorf("clientWantsStream = %v, want false", clientWantsStream)
	}
	// The output must contain "stream":true.
	if !strings.Contains(string(out), `"stream":true`) {
		t.Errorf("output %q does not contain stream:true", out)
	}
}

// TestRewriteBodyForChild_NonJSONPassthrough verifies that a non-JSON body
// is passed through unchanged.
func TestRewriteBodyForChild_NonJSONPassthrough(t *testing.T) {
	body := "plain text"
	req := httptest.NewRequest("POST", "/completion", bytes.NewReader([]byte(body)))
	out, _, err := rewriteBodyForChild(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != body {
		t.Errorf("output %q != input %q", out, body)
	}
}

// TestIsGenerated_EndpointMatching verifies the generation endpoint whitelist.
func TestIsGenerated_EndpointMatching(t *testing.T) {
	cases := []struct {
		method string
		path   string
		want   bool
	}{
		{"POST", "/completion", true},
		{"POST", "/v1/completions", true},
		{"POST", "/v1/chat/completions", true},
		{"POST", "/infill", true},
		{"POST", "/props", false},
		{"GET", "/v1/completions", false},
		{"GET", "/completion", false},
	}
	for _, c := range cases {
		got := IsGenerated(c.method, c.path)
		if got != c.want {
			t.Errorf("IsGenerated(%q,%q) = %v, want %v", c.method, c.path, got, c.want)
		}
	}
}

// TestBuildNonStreamingResponse_ChatCompletions verifies the assembled
// non-streaming response shape for /v1/chat/completions (specification §6.2).
func TestBuildNonStreamingResponse_ChatCompletions(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	resp := buildNonStreamingResponse(req, "hello world", "length", &streamMeta{})
	if !strings.Contains(resp, `"finish_reason":"length"`) {
		t.Errorf("missing finish_reason: %s", resp)
	}
	if !strings.Contains(resp, `"content":"hello world"`) {
		t.Errorf("missing content or wrong escape: %s", resp)
	}
}

// TestBuildNonStreamingResponse_Completion verifies the native /completion shape.
func TestBuildNonStreamingResponse_Completion(t *testing.T) {
	req := httptest.NewRequest("POST", "/completion", nil)
	resp := buildNonStreamingResponse(req, "abc", "length", &streamMeta{})
	if !strings.Contains(resp, `"text":"abc"`) {
		t.Errorf("missing text: %s", resp)
	}
}

// TestBuildNonStreamingResponse_CarriesUsageAndTimings verifies that the usage
// and timings reported by the stream survive reassembly. Without them a
// non-streaming client, and llama-swap's activity log, record zeros for a
// request that really consumed tokens.
func TestBuildNonStreamingResponse_CarriesUsageAndTimings(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	meta := streamMeta{
		usage:        json.RawMessage(`{"prompt_tokens":17,"completion_tokens":12,"total_tokens":29}`),
		timings:      json.RawMessage(`{"prompt_n":5,"predicted_n":12}`),
		finishReason: "stop",
	}
	resp := buildNonStreamingResponse(req, "hello", "stop", &meta)
	if !strings.Contains(resp, `"usage":{"prompt_tokens":17`) {
		t.Errorf("usage not carried: %s", resp)
	}
	if !strings.Contains(resp, `"timings":{"prompt_n":5`) {
		t.Errorf("timings not carried: %s", resp)
	}
	if !strings.Contains(resp, `"finish_reason":"stop"`) {
		t.Errorf("finish_reason should be the value the stream reported: %s", resp)
	}
	if !json.Valid([]byte(resp)) {
		t.Errorf("assembled response is not valid JSON: %s", resp)
	}
}

// TestBuildNonStreamingResponse_OmitsAbsentUsage verifies that a stream which
// reported no usage does not produce a null or zero usage field, since a client
// would read that as a measurement of zero tokens.
func TestBuildNonStreamingResponse_OmitsAbsentUsage(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	resp := buildNonStreamingResponse(req, "hello", "stop", &streamMeta{})
	if strings.Contains(resp, "usage") {
		t.Errorf("usage should be absent when the stream reported none: %s", resp)
	}
}

// TestBuildNonStreamingResponse_CarriesIdentityFields verifies that the id,
// created and model reported by the stream survive reassembly, and that the
// placeholder id is kept when the stream reported none.
func TestBuildNonStreamingResponse_CarriesIdentityFields(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	meta := streamMeta{
		id:      "chatcmpl-abc",
		created: json.Number("1700000000"),
		model:   "gemma-4-e2b",
	}
	resp := buildNonStreamingResponse(req, "hello", "stop", &meta)
	if !strings.Contains(resp, `"id":"chatcmpl-abc"`) {
		t.Errorf("id not carried: %s", resp)
	}
	if !strings.Contains(resp, `"created":1700000000`) {
		t.Errorf("created not carried: %s", resp)
	}
	if !strings.Contains(resp, `"model":"gemma-4-e2b"`) {
		t.Errorf("model not carried: %s", resp)
	}

	resp = buildNonStreamingResponse(req, "hello", "stop", &streamMeta{})
	if !strings.Contains(resp, `"id":"chatcmocore-123"`) {
		t.Errorf("placeholder id lost: %s", resp)
	}
	if strings.Contains(resp, "created") || strings.Contains(resp, "model") {
		t.Errorf("fields the stream did not report should be absent: %s", resp)
	}
}

// TestBuildNonStreamingResponse_CarriesReasoning verifies that reasoning text
// accumulated from the stream is written to the message. Without it a
// non-streaming client gets an empty message for a model that only produced
// reasoning.
func TestBuildNonStreamingResponse_CarriesReasoning(t *testing.T) {
	req := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	var meta streamMeta
	meta.observe(sse.Chunk{ReasoningContent: "thinking"})
	meta.observe(sse.Chunk{ReasoningContent: " harder"})
	resp := buildNonStreamingResponse(req, "answer", "stop", &meta)
	if !strings.Contains(resp, `"reasoning_content":"thinking harder"`) {
		t.Errorf("reasoning not carried: %s", resp)
	}

	// A response with no reasoning must not gain an empty reasoning field.
	resp = buildNonStreamingResponse(req, "answer", "stop", &streamMeta{})
	if strings.Contains(resp, "reasoning_content") {
		t.Errorf("empty reasoning field written: %s", resp)
	}
}

// TestStreamMeta_FinishForAssembly verifies the finish_reason used for a
// reassembled response: the value the stream reported when there was one,
// otherwise "stop".
func TestStreamMeta_FinishForAssembly(t *testing.T) {
	if got := (&streamMeta{finishReason: "stop"}).finishForAssembly(); got != "stop" {
		t.Errorf("got %q, want stop", got)
	}
	if got := (&streamMeta{}).finishForAssembly(); got != "stop" {
		t.Errorf("got %q, want stop", got)
	}
}

// TestStreamMeta_ObservKeepsLastValue verifies that a chunk reporting no usage
// does not clear usage observed on an earlier chunk.
func TestStreamMeta_ObservKeepsLastValue(t *testing.T) {
	var meta streamMeta
	meta.observe(sse.Chunk{Usage: json.RawMessage(`{"prompt_tokens":10}`)})
	meta.observe(sse.Chunk{Delta: "x"})
	if len(meta.usage) == 0 {
		t.Fatal("usage was cleared by a chunk that reported none")
	}
}
