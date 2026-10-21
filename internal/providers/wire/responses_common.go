package wire

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// The Codex and Grok CLI executors share one contract with their endpoints: a
// Responses request that is always streamed, never stored, with an input array,
// flat function tools and an allowlist of fields (executors/codex.js:417-519,
// executors/grok-cli.js:419-526). The two differ in details, so each owns its
// rewrite; this file holds what they do identically.

// decodeAny reads raw with numbers kept as written, so a rewrite that never
// touches a number does not reformat it.
func decodeAny(raw json.RawMessage) (any, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, false
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return nil, false
	}
	return value, true
}

// encodeAny writes value without escaping <, > and &, which the endpoints read as
// the text it is.
func encodeAny(value any) (json.RawMessage, bool) {
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if encoder.Encode(value) != nil {
		return nil, false
	}
	return json.RawMessage(bytes.TrimRight(buffer.Bytes(), "\n")), true
}

// set stores value under key and reports whether the body changed.
func (b Body) set(key string, value any) bool {
	encoded, ok := encodeAny(value)
	if !ok {
		return false
	}
	if previous, present := b[key]; present && bytes.Equal(previous, encoded) {
		return false
	}
	b[key] = encoded
	return true
}

// absent is JS `!value` for the fields these rewrites default: missing, null,
// or an empty string.
func (b Body) absent(key string) bool {
	raw, ok := b[key]
	if !ok {
		return true
	}
	value, decoded := decodeAny(raw)
	if !decoded || value == nil {
		return true
	}
	text, isText := value.(string)
	return isText && text == ""
}

func (b Body) text(key string) (string, bool) {
	value, ok := decodeAny(b[key])
	if !ok {
		return "", false
	}
	text, isText := value.(string)
	return text, isText
}

func (b Body) object(key string) (map[string]any, bool) {
	value, ok := decodeAny(b[key])
	if !ok {
		return nil, false
	}
	object, isObject := value.(map[string]any)
	return object, isObject && object != nil
}

func (b Body) array(key string) ([]any, bool) {
	value, ok := decodeAny(b[key])
	if !ok {
		return nil, false
	}
	array, isArray := value.([]any)
	return array, isArray
}

// dropKeys removes keys and reports whether any was present.
func (b Body) dropKeys(keys ...string) bool {
	changed := false
	for _, key := range keys {
		if b.delete(key) {
			changed = true
		}
	}
	return changed
}

// keepOnly deletes every key that is not in allowed: an unknown field is how an
// endpoint answers "unsupported" for the whole request.
func (b Body) keepOnly(allowed map[string]bool) bool {
	changed := false
	for key := range b {
		if !allowed[key] {
			delete(b, key)
			changed = true
		}
	}
	return changed
}

// responsesPlaceholder is the one user turn standing in for an input the
// endpoint would reject as empty (translator/formats/responsesApi.js:11-24).
func responsesPlaceholder(text string) []any {
	return []any{map[string]any{
		"type": "message", "role": "user",
		"content": []any{map[string]any{"type": "input_text", "text": text}},
	}}
}

// serverItemID is a server-generated item id the endpoint cannot resolve when
// nothing was stored (codex.js:30, grok-cli.js:20).
var serverItemID = regexp.MustCompile(`^(rs|fc|resp|msg)_`)

// stripStoredItemReferences removes what refers to a stored item: a bare id
// string, an item_reference, and the id of an item that carries one. keep names
// an id the endpoint issued itself and resolves (grok-cli.js:223-237).
func stripStoredItemReferences(input []any, keep func(string) bool) []any {
	kept := make([]any, 0, len(input))
	for _, item := range input {
		switch typed := item.(type) {
		case string:
			if serverItemID.MatchString(typed) {
				continue
			}
		case map[string]any:
			if typed["type"] == "item_reference" {
				continue
			}
			if id, ok := typed["id"].(string); ok && serverItemID.MatchString(id) && (keep == nil || !keep(id)) {
				delete(typed, "id")
			}
		}
		kept = append(kept, item)
	}
	return kept
}

// truncate is JS slice(0, n) for the 128-character tool-name cap, by code point.
func truncate(text string, limit int) string {
	runes := []rune(text)
	if len(runes) <= limit {
		return text
	}
	return string(runes[:limit])
}

// functionToolParts reads a Chat-shaped or flat function tool: the name, the
// description and the parameters schema, wherever the tool put them.
func functionToolParts(tool map[string]any) (name, description string, parameters any, ok bool) {
	function, _ := tool["function"].(map[string]any)
	name, named := tool["name"].(string)
	if !named {
		name, _ = function["name"].(string)
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "", "", nil, false
	}
	if text, isText := tool["description"].(string); isText {
		description = text
	} else if text, isText := function["description"].(string); isText {
		description = text
	}
	if schema, isObject := tool["parameters"].(map[string]any); isObject {
		parameters = schema
	} else if schema, isObject := function["parameters"].(map[string]any); isObject {
		parameters = schema
	} else {
		parameters = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return name, description, parameters, true
}

// truthy is JS truthiness for a decoded JSON value.
func truthy(value any) bool {
	switch typed := value.(type) {
	case nil:
		return false
	case bool:
		return typed
	case string:
		return typed != ""
	case json.Number:
		parsed, err := typed.Float64()
		return err != nil || parsed != 0
	}
	return true
}

// toolChoiceFunctionName is the function a tool_choice names, trimmed and cut to
// the 128 characters a tool name may have; a choice that names a function
// nothing declares is the caller's to drop, because the endpoint answers 400 to it.
func toolChoiceFunctionName(choice map[string]any) string {
	name, _ := choice["name"].(string)
	return truncate(strings.TrimSpace(name), 128)
}
