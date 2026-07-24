// Package sse provides a minimal Server-Sent Events (SSE) line parser.
// It extracts "data:" payloads from a byte stream, per specification section 5.3.
package sse

import "strings"

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

// ExtractDelta extracts the delta text from a parsed JSON chunk for the
// supported endpoints, per specification section 5.196-203.
//
// It returns the raw string (unescaped JSON value) of the delta content, or
// empty string if the chunk does not contain usable delta text (e.g. a
// finish_reason-only chunk) or cannot be parsed.
//
// Supported shapes:
//
//	{"choices":[{"text":"..."}]}            // /completion, /v1/completions
//	{"choices":[{"delta":{"content":"..."}}]} // /v1/chat/completions
//
// This function intentionally avoids importing a JSON library at this layer
// and performs a lightweight substring search so it remains dependency-free.
// It first looks for "content" (chat completions), then falls back to "text"
// (completion/completions endpoints).
func ExtractDelta(data string) string {
	// Reject the terminal marker.
	if IsDoneMarker(data) {
		return ""
	}
	if len(data) == 0 || data[0] != '{' {
		return ""
	}
	// Try "content" first (covers /completion native and /v1/chat/completions delta shape).
	if delta := extractAfterField(data, "content"); delta != "" {
		return delta
	}
	// Fall back to "text" (covers /v1/completions and native /completion when shaped as text).
	return extractAfterField(data, "text")
}

// extractAfterField finds the first occurrence of `"field"` in data and
// returns the unescaped string value of the immediately following quoted
// JSON string. Returns "" if the field is absent or the value is not a
// string (e.g. a number or object — as is the case for finish_reason).
func extractAfterField(data, field string) string {
	needle := field + `":`
	idx := strings.Index(data, needle)
	if idx < 0 {
		return ""
	}
	// Skip past the field name AND its colon.
	rest := data[idx+len(needle):]
	return extractFirstQuotedString(rest)
}

// extractFirstQuotedString returns the content of the first "..." string
// in the input, with JSON escapes processed minimally (\n, \t, \", \\, \/).
// Returns empty string if no quoted string is found.
func extractFirstQuotedString(s string) string {
	// Find first quote.
	start := strings.IndexByte(s, '"')
	if start < 0 {
		return ""
	}
	buf := strings.Builder{}
	i := start + 1
	for i < len(s) {
		c := s[i]
		if c == '\\' && i+1 < len(s) {
			next := s[i+1]
			switch next {
			case 'n':
				buf.WriteByte('\n')
			case 't':
				buf.WriteByte('\t')
			case 'r':
				buf.WriteByte('\r')
			case '"':
				buf.WriteByte('"')
			case '\\':
				buf.WriteByte('\\')
			case '/':
				buf.WriteByte('/')
			default:
				buf.WriteByte(c)
				buf.WriteByte(next)
			}
			i += 2
			continue
		}
		if c == '"' {
			break
		}
		buf.WriteByte(c)
		i++
	}
	return buf.String()
}
