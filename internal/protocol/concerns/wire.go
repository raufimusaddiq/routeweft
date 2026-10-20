package concerns

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
)

// WireObject decodes exactly one JSON object, keeping numbers exact.
func WireObject(body []byte) (map[string]any, error) {
	var value map[string]any
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil || value == nil {
		return nil, errors.New("expected a JSON object")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, errors.New("unexpected data after JSON object")
	}
	return value, nil
}

func Object(value any) map[string]any { result, _ := value.(map[string]any); return result }
func Items(value any) []any           { result, _ := value.([]any); return result }
func Text(value any) string           { result, _ := value.(string); return result }

// Int reads a JSON number as an integer; anything else is 0, like the oracle's
// `typeof v === "number" ? v : 0`.
func Int(value any) int64 {
	switch number := value.(type) {
	case json.Number:
		if parsed, err := number.Int64(); err == nil {
			return parsed
		}
		if parsed, err := number.Float64(); err == nil {
			return int64(parsed)
		}
	case float64:
		return int64(number)
	case int64:
		return number
	case int:
		return int64(number)
	}
	return 0
}

// IsNumber reports whether the value is a JSON number.
func IsNumber(value any) bool {
	switch value.(type) {
	case json.Number, float64, int64, int:
		return true
	}
	return false
}

// Truthy follows JavaScript truthiness for decoded JSON.
func Truthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case json.Number:
		parsed, err := v.Float64()
		return err != nil || parsed != 0
	case float64:
		return v != 0
	case int64:
		return v != 0
	case int:
		return v != 0
	}
	return true
}

// JSONText is JSON.stringify for a decoded value; nil encodes as "null".
func JSONText(value any) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	return string(encoded)
}

// ExtractText joins the text parts of a Chat content value
// (translator/formats/gemini.js:113-119).
func ExtractText(content any, separator string) string {
	if text, ok := content.(string); ok {
		return text
	}
	var texts []string
	for _, raw := range Items(content) {
		if part := Object(raw); Text(part["type"]) == "text" {
			texts = append(texts, Text(part["text"]))
		}
	}
	return joinStrings(texts, separator)
}

func joinStrings(texts []string, separator string) string {
	result := ""
	for i, text := range texts {
		if i > 0 {
			result += separator
		}
		result += text
	}
	return result
}

var dataURIPattern = regexp.MustCompile(`(?s)^data:([^;]+);base64,(.+)$`)

// ParseDataURI splits a base64 data URI (concerns/image.js:6-13).
func ParseDataURI(url any) (mediaType, data string, ok bool) {
	text, isText := url.(string)
	if !isText {
		return "", "", false
	}
	match := dataURIPattern.FindStringSubmatch(text)
	if match == nil {
		return "", "", false
	}
	return match[1], match[2], true
}

// ToolInput is the `input` of a tool_use block: always a JSON object. A Chat
// call's arguments arrive as a string that may be empty (a no-argument tool),
// absent, malformed, or valid JSON that is not an object; none of those can be
// sent as `input`, so each is `{}` (nonstream-to-claude.js:5-13 for the first
// three; the last follows from the same rule).
func ToolInput(value any) map[string]any {
	switch v := value.(type) {
	case map[string]any:
		return v
	case string:
		if v == "" {
			return map[string]any{}
		}
		if parsed, err := WireObject([]byte(v)); err == nil {
			return parsed
		}
	}
	return map[string]any{}
}

// ToolArguments is the Chat `arguments` string for a tool_use input: the input
// as a JSON object, `{}` when it is none.
func ToolArguments(input any) string { return JSONText(ToolInput(input)) }

// Present reports whether a field asks for anything, whatever shape it has: an
// empty object, list or string, null and false ask for nothing. A number always
// asks for something, zero included.
func Present(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case map[string]any:
		return len(v) > 0
	case []any:
		return len(v) > 0
	case string:
		return v != ""
	case bool:
		return v
	}
	return true
}
