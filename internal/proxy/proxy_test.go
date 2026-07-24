package proxy

import (
	"bytes"
	"net/http/httptest"
	"strings"
	"testing"
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
	resp := buildNonStreamingResponse(req, "hello world", "length")
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
	resp := buildNonStreamingResponse(req, "abc", "length")
	if !strings.Contains(resp, `"text":"abc"`) {
		t.Errorf("missing text: %s", resp)
	}
}
