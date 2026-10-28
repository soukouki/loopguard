// Package sse provides a minimal Server-Sent Events (SSE) line parser.
// It extracts "data:" payloads from a byte stream, per specification section 5.3.
package sse

import (
	"encoding/json"
	"strings"
)

// ParseLine parses a single line of an SSE stream.
// It returns:
//   - ok:   true if the line starts with "data:".
//   - data: the payload following "data:" (stripped of leading whitespace).
//
// Lines that do not start with "data:", the "[DONE]" marker, or an empty
// line reset, are reported via ok=false so callers can decide how to handle them.
func ParseLine(line string) (ok bool, data string) {
	// A full SSE event block ends with an empty line. We only care about
	// "data:" fields.
	if !strings.HasPrefix(line, "data:") {
		return false, ""
	}
	// Strip a single optional leading space after "data:" (rfc-style), then trim.
	payload := strings.TrimSpace(line[len("data:"):])
	return true, payload
}

// IsDoneMarker reports whether the data payload is the textual end-of-stream
// marker ("[DONE]") used by OpenAI-compatible streaming endpoints.
func IsDoneMarker(data string) bool {
	return data == "[DONE]"
}

// deltaShape mirrors the union of the response shapes listed in the
// specification §5 mapping table. Fields from shapes that do not apply stay
// zero.
type deltaShape struct {
	Choices []struct {
		Text  string `json:"text"`
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
	Content string `json:"content"`
	Text    string `json:"text"`
}

// ExtractDelta extracts the delta text from a parsed JSON chunk for the
// supported endpoints, per specification section 5.196-203.
//
// It returns the unescaped delta text, or empty string if the chunk does not
// contain usable delta text (e.g. a finish_reason-only chunk) or cannot be
// parsed.
//
// Parsing goes through encoding/json rather than a substring search. A
// substring search cannot tell a field name from a field value, so the
// role-only first chunk of /v1/chat/completions, where delta.content is null,
// returned the following field name as delta text.
func ExtractDelta(data string) string {
	// Reject the terminal marker.
	if IsDoneMarker(data) {
		return ""
	}
	var shape deltaShape
	if err := json.Unmarshal([]byte(data), &shape); err != nil {
		return ""
	}

	if len(shape.Choices) > 0 {
		choice := shape.Choices[0]
		// /v1/chat/completions puts the delta under choices[0].delta, while
		// /v1/completions puts it at choices[0].text.
		if choice.Delta.Content != "" {
			return choice.Delta.Content
		}
		if choice.Text != "" {
			return choice.Text
		}
	}

	// Native /completion and /infill carry the delta at top level.
	if shape.Content != "" {
		return shape.Content
	}
	return shape.Text
}
