package promptcache

import (
	"encoding/json"
	"errors"
	"fmt"
)

// DefaultBudget is Anthropic's supported cache breakpoint count. Markers beyond
// the budget are rejected by the platform, so Routeweft keeps at most this many
// Routeweft-added anchors per request.
const DefaultBudget = 4

// Anchor adds cache-control breakpoints to a final Anthropic Messages body.
// Client-supplied markers are never removed. Existing breakpoints
// count against the budget; only the remaining budget is spent on Routeweft
// anchors. Priority follows the stable prefix: system, tools, history.
func Anchor(body []byte, budget int) ([]byte, error) {
	if budget <= 0 {
		budget = DefaultBudget
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return nil, errors.New("prompt cache: request body must be one JSON object")
	}
	used, err := countClientMarkers(fields)
	if err != nil {
		return nil, err
	}
	remaining := budget - used
	if remaining <= 0 {
		return append([]byte(nil), body...), nil
	}
	changed, err := anchorSystem(fields, &remaining)
	if err != nil {
		return nil, err
	}
	if anchorTools(fields, &remaining) {
		changed = true
	}
	if anchorHistory(fields, &remaining) {
		changed = true
	}
	if !changed {
		return append([]byte(nil), body...), nil
	}
	return json.Marshal(fields)
}

func countClientMarkers(fields map[string]json.RawMessage) (int, error) {
	total := 0
	var system []json.RawMessage
	if raw, ok := fields["system"]; ok {
		if len(raw) > 0 && raw[0] == '"' {
			// A string system prompt cannot carry an inline Anthropic marker.
		} else if err := json.Unmarshal(raw, &system); err != nil {
			return 0, fmt.Errorf("prompt cache: system must be an array of blocks: %w", err)
		} else {
			total += countBlocks(system)
		}
	}
	var messages []json.RawMessage
	if raw, ok := fields["messages"]; ok {
		if err := json.Unmarshal(raw, &messages); err != nil {
			return 0, fmt.Errorf("prompt cache: messages must be an array: %w", err)
		}
		for _, message := range messages {
			total += countMessageBlocks(message)
		}
	}
	var tools []json.RawMessage
	if raw, ok := fields["tools"]; ok {
		if err := json.Unmarshal(raw, &tools); err != nil {
			return 0, fmt.Errorf("prompt cache: tools must be an array: %w", err)
		}
		for _, tool := range tools {
			if hasMarker(tool) {
				total++
			}
		}
	}
	return total, nil
}

func hasMarker(item []byte) bool {
	var object map[string]json.RawMessage
	return json.Unmarshal(item, &object) == nil && object["cache_control"] != nil
}

func countBlocks(blocks []json.RawMessage) int {
	total := 0
	for _, item := range blocks {
		if hasMarker(item) {
			total++
		}
	}
	return total
}

func countMessageBlocks(message json.RawMessage) int {
	var envelope struct {
		Content json.RawMessage `json:"content"`
	}
	if err := json.Unmarshal(message, &envelope); err != nil || len(envelope.Content) == 0 || envelope.Content[0] != '[' {
		return 0
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(envelope.Content, &blocks); err != nil {
		return 0
	}
	return countBlocks(blocks)
}

func anchorSystem(fields map[string]json.RawMessage, remaining *int) (bool, error) {
	if *remaining == 0 {
		return false, nil
	}
	raw, ok := fields["system"]
	if !ok || len(raw) == 0 || raw[0] == '"' {
		return false, nil
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(raw, &blocks); err != nil {
		return false, nil
	}
	if len(blocks) == 0 || hasMarker(blocks[len(blocks)-1]) {
		return false, nil
	}
	updated, err := addMarker(blocks[len(blocks)-1])
	if err != nil {
		return false, err
	}
	blocks[len(blocks)-1] = updated
	encoded, err := json.Marshal(blocks)
	if err != nil {
		return false, err
	}
	fields["system"] = encoded
	*remaining--
	return true, nil
}

func anchorTools(fields map[string]json.RawMessage, remaining *int) bool {
	if *remaining == 0 {
		return false
	}
	raw, ok := fields["tools"]
	if !ok {
		return false
	}
	var tools []json.RawMessage
	if json.Unmarshal(raw, &tools) != nil || len(tools) == 0 || hasMarker(tools[len(tools)-1]) {
		return false
	}
	updated, err := addMarker(tools[len(tools)-1])
	if err != nil {
		return false
	}
	tools[len(tools)-1] = updated
	encoded, err := json.Marshal(tools)
	if err != nil {
		return false
	}
	fields["tools"] = encoded
	*remaining--
	return true
}

func anchorHistory(fields map[string]json.RawMessage, remaining *int) bool {
	if *remaining == 0 {
		return false
	}
	raw, ok := fields["messages"]
	if !ok {
		return false
	}
	var messages []json.RawMessage
	if json.Unmarshal(raw, &messages) != nil || len(messages) < 2 {
		return false
	}
	// Keep a final user turn outside the anchor; if this request ends on an
	// assistant/tool turn, anchor that completed turn, so the N and N+1 prefix
	// boundary stays stable as a new user turn is appended.
	index := len(messages) - 1
	var final struct {
		Role string `json:"role"`
	}
	if json.Unmarshal(messages[index], &final) != nil {
		return false
	}
	if final.Role == "user" {
		index--
	}
	if index < 0 {
		return false
	}
	var envelope map[string]json.RawMessage
	if json.Unmarshal(messages[index], &envelope) != nil {
		return false
	}
	content, ok := envelope["content"]
	if !ok || len(content) == 0 || content[0] != '[' {
		return false
	}
	var blocks []json.RawMessage
	if json.Unmarshal(content, &blocks) != nil || len(blocks) == 0 || hasMarker(blocks[len(blocks)-1]) {
		return false
	}
	var finalBlock struct {
		Type string `json:"type"`
	}
	if json.Unmarshal(blocks[len(blocks)-1], &finalBlock) != nil || finalBlock.Type == "thinking" || finalBlock.Type == "redacted_thinking" {
		return false
	}
	updated, err := addMarker(blocks[len(blocks)-1])
	if err != nil {
		return false
	}
	blocks[len(blocks)-1] = updated
	envelope["content"], err = json.Marshal(blocks)
	if err != nil {
		return false
	}
	messages[index], err = json.Marshal(envelope)
	if err != nil {
		return false
	}
	encoded, err := json.Marshal(messages)
	if err != nil {
		return false
	}
	fields["messages"] = encoded
	*remaining--
	return true
}

func addMarker(raw json.RawMessage) (json.RawMessage, error) {
	var block map[string]json.RawMessage
	if err := json.Unmarshal(raw, &block); err != nil || block == nil {
		return nil, errors.New("prompt cache: cache anchor target must be an object")
	}
	block["cache_control"] = json.RawMessage(`{"type":"ephemeral"}`)
	return json.Marshal(block)
}
