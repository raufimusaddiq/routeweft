package openai

import (
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

var validChatContentTypes = map[string]bool{"text": true, "image_url": true, "image": true, "input_audio": true, "audio_url": true, "file": true}

// FilterChatBody normalises a body that was converted into Chat so a strict
// OpenAI-compatible upstream accepts it
// (translator/formats/openai.js:10-133, applied by translator/index.js:150-154
// whenever the target is Chat). It drops thinking and unknown blocks, signature
// and cache_control (kept when preserveCacheControl), empty messages and an
// empty tools array, and rewrites Claude and Gemini tool shapes and tool_choice.
func FilterChatBody(body map[string]any, preserveCacheControl bool) {
	messages := concerns.Items(body["messages"])
	if messages == nil {
		return
	}
	strip := func(block map[string]any) map[string]any {
		cleaned := map[string]any{}
		for key, value := range block {
			if key != "signature" && key != "cache_control" {
				cleaned[key] = value
			}
		}
		if preserveCacheControl && concerns.Truthy(block["cache_control"]) {
			cleaned["cache_control"] = block["cache_control"]
		}
		return cleaned
	}
	kept := make([]any, 0, len(messages))
	for _, raw := range messages {
		message := concerns.Object(raw)
		if message == nil {
			kept = append(kept, raw)
			continue
		}
		if concerns.Text(message["role"]) == "developer" {
			copied := make(map[string]any, len(message))
			for key, value := range message {
				copied[key] = value
			}
			copied["role"] = "system"
			message = copied
		}
		role := concerns.Text(message["role"])
		switch {
		case role == "tool", role == "assistant" && concerns.Truthy(message["tool_calls"]):
		case isString(message["content"]):
		case concerns.Items(message["content"]) != nil:
			filtered := []any{}
			for _, rawBlock := range concerns.Items(message["content"]) {
				block := concerns.Object(rawBlock)
				switch kind := concerns.Text(block["type"]); {
				case kind == "thinking", kind == "redacted_thinking", kind == "tool_use":
				case validChatContentTypes[kind], kind == "tool_result":
					filtered = append(filtered, strip(block))
				}
			}
			if len(filtered) == 0 {
				filtered = append(filtered, map[string]any{"type": "text", "text": ""})
			}
			copied := make(map[string]any, len(message))
			for key, value := range message {
				copied[key] = value
			}
			copied["content"] = filtered
			message = copied
		}
		kept = append(kept, message)
	}
	nonEmpty := make([]any, 0, len(kept))
	for _, raw := range kept {
		if keepMessage(concerns.Object(raw)) {
			nonEmpty = append(nonEmpty, raw)
		}
	}
	body["messages"] = nonEmpty
	if tools := concerns.Items(body["tools"]); tools != nil {
		if len(tools) == 0 {
			delete(body, "tools")
		} else {
			body["tools"] = chatTools(tools)
		}
	}
	if choice := concerns.Object(body["tool_choice"]); choice != nil {
		switch concerns.Text(choice["type"]) {
		case "auto":
			body["tool_choice"] = "auto"
		case "any":
			body["tool_choice"] = "required"
		case "tool":
			if concerns.Truthy(choice["name"]) {
				body["tool_choice"] = map[string]any{"type": "function", "function": map[string]any{"name": choice["name"]}}
			}
		}
	}
}

func isString(value any) bool { _, ok := value.(string); return ok }

// keepMessage drops a message that carries only empty text. Tool messages and
// assistant messages with tool_calls are never dropped.
func keepMessage(message map[string]any) bool {
	if message == nil {
		return true
	}
	role := concerns.Text(message["role"])
	if role == "tool" || (role == "assistant" && concerns.Truthy(message["tool_calls"])) {
		return true
	}
	if text, ok := message["content"].(string); ok {
		return strings.TrimSpace(text) != ""
	}
	if blocks := concerns.Items(message["content"]); blocks != nil {
		for _, raw := range blocks {
			block := concerns.Object(raw)
			if concerns.Text(block["type"]) != "text" || strings.TrimSpace(concerns.Text(block["text"])) != "" {
				return true
			}
		}
		return false
	}
	return true
}

func chatTools(tools []any) []any {
	var result []any
	for _, raw := range tools {
		tool := concerns.Object(raw)
		switch {
		case concerns.Text(tool["type"]) == "function" && concerns.Truthy(tool["function"]):
			result = append(result, raw)
		case concerns.Truthy(tool["name"]) && (concerns.Truthy(tool["input_schema"]) || concerns.Truthy(tool["description"])):
			schema := tool["input_schema"]
			if !concerns.Truthy(schema) {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			description := ""
			if text, ok := tool["description"].(string); ok {
				description = text
			}
			result = append(result, map[string]any{"type": "function", "function": map[string]any{
				"name": tool["name"], "description": description, "parameters": schema}})
		case concerns.Items(tool["functionDeclarations"]) != nil:
			for _, declaration := range concerns.Items(tool["functionDeclarations"]) {
				fn := concerns.Object(declaration)
				schema := fn["parameters"]
				if !concerns.Truthy(schema) {
					schema = map[string]any{"type": "object", "properties": map[string]any{}}
				}
				result = append(result, map[string]any{"type": "function", "function": map[string]any{
					"name": fn["name"], "description": concerns.Text(fn["description"]), "parameters": schema}})
			}
		default:
			result = append(result, raw)
		}
	}
	return result
}
