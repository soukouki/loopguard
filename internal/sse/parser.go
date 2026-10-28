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

// Chunk is one parsed SSE data payload. It carries the delta text plus the
// fields that a non-streaming client would otherwise lose when the stream is
// reassembled (specification §5.5, §6.2).
type Chunk struct {
	// Delta is the delta text carried by the chunk. It is empty when the
	// chunk carries no delta, for example a finish_reason-only chunk.
	Delta string

	// FinishReason is the finish_reason reported by the chunk, or "" when it
	// is absent or null. For the native shapes, a chunk with "stop": true is
	// reported as "stop".
	FinishReason string

	// Usage and Timings are the raw JSON objects reported by the chunk, or
	// nil when the chunk does not carry them. Upstream servers put them on
	// the final chunk, so callers must keep the last non-empty value seen.
	Usage   json.RawMessage
	Timings json.RawMessage
}

// chunkShape mirrors the union of the response shapes listed in the
// specification §5 mapping table. Fields from shapes that do not apply stay
// zero.
type chunkShape struct {
	Choices []struct {
		Text  string `json:"text"`
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
		FinishReason *string `json:"finish_reason"`
	} `json:"choices"`
	Content string          `json:"content"`
	Text    string          `json:"text"`
	Stop    bool            `json:"stop"`
	Usage   json.RawMessage `json:"usage"`
	Timings json.RawMessage `json:"timings"`
}

// ParseChunk parses one data payload into a Chunk. It returns an error when
// the payload is not a JSON object.
//
// Parsing goes through encoding/json rather than a substring search. A
// substring search cannot tell a field name from a field value, so the
// role-only first chunk of /v1/chat/completions, where delta.content is null,
// returned the following field name as delta text.
func ParseChunk(data string) (Chunk, error) {
	if IsDoneMarker(data) {
		return Chunk{}, nil
	}
	var shape chunkShape
	if err := json.Unmarshal([]byte(data), &shape); err != nil {
		return Chunk{}, err
	}

	c := Chunk{Usage: shape.Usage, Timings: shape.Timings}

	if len(shape.Choices) > 0 {
		choice := shape.Choices[0]
		// /v1/chat/completions puts the delta under choices[0].delta, while
		// /v1/completions puts it at choices[0].text.
		c.Delta = choice.Delta.Content
		if c.Delta == "" {
			c.Delta = choice.Text
		}
		if choice.FinishReason != nil {
			c.FinishReason = *choice.FinishReason
		}
	}

	// Native /completion and /infill carry the delta at top level.
	if c.Delta == "" {
		c.Delta = shape.Content
		if c.Delta == "" {
			c.Delta = shape.Text
		}
	}

	if c.FinishReason == "" && shape.Stop {
		c.FinishReason = "stop"
	}

	return c, nil
}

// ExtractDelta extracts the delta text from a parsed JSON chunk for the
// supported endpoints, per specification section 5.196-203.
//
// It returns the unescaped delta text, or empty string if the chunk does not
// contain usable delta text (e.g. a finish_reason-only chunk) or cannot be
// parsed.
func ExtractDelta(data string) string {
	c, err := ParseChunk(data)
	if err != nil {
		return ""
	}
	return c.Delta
}
