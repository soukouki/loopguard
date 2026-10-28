package sse

import (
	"strings"
	"testing"
)

func TestParseLine_DataPrefix(t *testing.T) {
	ok, data := ParseLine("data: hello")
	if !ok || data != "hello" {
		t.Errorf("got ok=%v data=%q", ok, data)
	}
}

func TestParseLine_NonDataLine(t *testing.T) {
	ok, _ := ParseLine(":ping")
	if ok {
		t.Fatal("expected ok=false for non-data line")
	}
}

func TestIsDoneMarker(t *testing.T) {
	if !IsDoneMarker("[DONE]") {
		t.Fatal("expected [DONE] to be recognized")
	}
	if IsDoneMarker("data: [DONE]") {
		t.Fatal("data-prefixed marker should not match")
	}
}

func TestExtractDelta_ChatCompletions(t *testing.T) {
	// /v1/chat/completions shape.
	data := `{"choices":[{"delta":{"content":"hello"}}]}`
	if got := ExtractDelta(data); got != "hello" {
		t.Errorf("got %q, want %q", got, "hello")
	}
}

func TestExtractDelta_TextCompletions(t *testing.T) {
	// /completion and /v1/completions shape.
	data := `{"choices":[{"text":"world"}]}`
	if got := ExtractDelta(data); got != "world" {
		t.Errorf("got %q, want %q", got, "world")
	}
}

func TestExtractDelta_NativeCompletionTopLevel(t *testing.T) {
	// Native /completion shape carries the delta at top level.
	data := `{"content":"done","stop":true}`
	if got := ExtractDelta(data); got != "done" {
		t.Errorf("got %q, want %q", got, "done")
	}
}

func TestExtractDelta_FinishReasonOnly(t *testing.T) {
	// A chunk that only has finish_reason (no content): should return ""
	// so window is not polluted and no false loop is triggered.
	data := `{"choices":[{"finish_reason":"stop"}]}`
	if got := ExtractDelta(data); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestExtractDelta_NullContent(t *testing.T) {
	// The role-only first chunk of /v1/chat/completions has delta.content null.
	// A substring parser reads the next quoted token after "content":, which is
	// a field name, so the window and the assembled text got polluted with the
	// word "created".
	data := `{"choices":[{"index":0,"delta":{"role":"assistant","content":null}}],"created":1700000000,"id":"chatcmpl-1"}`
	if got := ExtractDelta(data); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestExtractDelta_Escapes(t *testing.T) {
	// JSON-escaped characters should be unescaped.
	data := `{"choices":[{"delta":{"content":"a\nb\tc"}}]}`
	if got := ExtractDelta(data); got != "a\nb\tc" {
		t.Errorf("got %q, want %q", got, "a\nb\tc")
	}
}

func TestExtractDelta_NotJSON(t *testing.T) {
	if got := ExtractDelta("not json"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestParseChunk_UsageAndTimingsOnFinalChunk(t *testing.T) {
	// Upstream servers report usage and timings only on the final chunk.
	data := `{"choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":17,"completion_tokens":5},"timings":{"prompt_n":17}}`
	c, err := ParseChunk(data)
	if err != nil {
		t.Fatalf("ParseChunk: %v", err)
	}
	if c.FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want stop", c.FinishReason)
	}
	if len(c.Usage) == 0 || !strings.Contains(string(c.Usage), `"prompt_tokens":17`) {
		t.Errorf("usage = %q, want the raw usage object", c.Usage)
	}
	if len(c.Timings) == 0 || !strings.Contains(string(c.Timings), `"prompt_n":17`) {
		t.Errorf("timings = %q, want the raw timings object", c.Timings)
	}
}

func TestParseChunk_NativeCompletionStopTrue(t *testing.T) {
	// Native /completion shape: delta at top level, terminal condition is stop true.
	data := `{"content":"done","stop":true,"timings":{"predicted_n":4}}`
	c, err := ParseChunk(data)
	if err != nil {
		t.Fatalf("ParseChunk: %v", err)
	}
	if c.Delta != "done" {
		t.Errorf("delta = %q, want done", c.Delta)
	}
	if c.FinishReason != "stop" {
		t.Errorf("finish_reason = %q, want stop", c.FinishReason)
	}
}

func TestParseChunk_InvalidJSON(t *testing.T) {
	if _, err := ParseChunk("not json"); err == nil {
		t.Fatal("expected an error for a non-JSON payload")
	}
}
