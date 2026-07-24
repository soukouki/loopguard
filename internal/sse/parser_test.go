package sse

import "testing"

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

func TestExtractDelta_FinishReasonOnly(t *testing.T) {
	// A chunk that only has finish_reason (no content): should return ""
	// so window is not polluted and no false loop is triggered.
	data := `{"choices":[{"finish_reason":"stop"}]}`
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
