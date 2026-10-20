package anthropic

import (
	"encoding/json"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// ClaudeSystemPrompt is injected as the first system block of every request the
// leg produces (config/appConstants.js:172; translator/request/openai-to-claude.js:136).
const ClaudeSystemPrompt = "You are Claude Code, Anthropic's official CLI for Claude."

// TrailingUserPlaceholder ends a conversation that would otherwise end on an
// assistant turn (translator/formats/claude.js:343).
const TrailingUserPlaceholder = "Continue."

var ephemeral1h = func() map[string]any { return map[string]any{"type": "ephemeral", "ttl": "1h"} }

// ChatToMessages is the request leg openai-chat>anthropic-messages
// (translator/request/openai-to-claude.js:17-201). The returned ToolNames maps
// the names sent upstream back to the ones the client declared; the response
// legs need it.
func ChatToMessages(body map[string]any, model string, stream bool) (map[string]any, *concerns.ToolNames) {
	names := concerns.NewToolNames(concerns.ChatToolNames(body))
	result := map[string]any{"model": model, "max_tokens": AdjustMaxTokens(body, 0), "stream": stream}
	if temperature, ok := body["temperature"]; ok {
		result["temperature"] = temperature
	}
	messages := []any{}
	var systemParts []string
	if source := concerns.Items(body["messages"]); source != nil {
		var others []map[string]any
		for _, raw := range source {
			message := concerns.Object(raw)
			if concerns.Text(message["role"]) == "system" {
				systemParts = append(systemParts, concerns.ExtractText(message["content"], "\n"))
			} else {
				others = append(others, message)
			}
		}
		var role string
		var current []any
		flush := func() {
			if role != "" && len(current) > 0 {
				messages = append(messages, map[string]any{"role": role, "content": current})
				current = nil
			}
		}
		for _, message := range others {
			next := "assistant"
			if r := concerns.Text(message["role"]); r == "user" || r == "tool" {
				next = "user"
			}
			blocks := contentBlocks(message, names)
			var results, rest []any
			hasToolUse := false
			for _, raw := range blocks {
				switch concerns.Text(concerns.Object(raw)["type"]) {
				case "tool_result":
					results = append(results, raw)
				case "tool_use":
					hasToolUse = true
					rest = append(rest, raw)
				default:
					rest = append(rest, raw)
				}
			}
			if len(results) > 0 {
				flush()
				messages = append(messages, map[string]any{"role": "user", "content": results})
				if len(rest) > 0 {
					role = next
					current = append(current, rest...)
				}
				continue
			}
			if role != next {
				flush()
				role = next
			}
			current = append(current, blocks...)
			if hasToolUse {
				flush()
			}
		}
		flush()
		markLastAssistant(messages)
	}
	if format := concerns.Object(body["response_format"]); format != nil {
		schema := concerns.Object(format["json_schema"])["schema"]
		switch {
		case concerns.Text(format["type"]) == "json_schema" && concerns.Truthy(schema):
			encoded, _ := json.MarshalIndent(schema, "", "  ")
			systemParts = append(systemParts, "You must respond with valid JSON that strictly follows this JSON schema:\n```json\n"+string(encoded)+"\n```\nRespond ONLY with the JSON object, no other text.")
		case concerns.Text(format["type"]) == "json_object":
			systemParts = append(systemParts, "You must respond with valid JSON. Respond ONLY with a JSON object, no other text.")
		}
	}
	result["messages"] = messages
	system := []any{map[string]any{"type": "text", "text": ClaudeSystemPrompt}}
	if len(systemParts) > 0 {
		system = append(system, map[string]any{"type": "text", "text": strings.Join(systemParts, "\n"), "cache_control": ephemeral1h()})
	}
	result["system"] = system
	if tools := concerns.Items(body["tools"]); tools != nil {
		converted := []any{}
		for _, raw := range tools {
			tool := concerns.Object(raw)
			if kind := concerns.Text(tool["type"]); kind != "" && kind != "function" {
				converted = append(converted, raw)
				continue
			}
			data := concerns.Object(tool["function"])
			if data == nil {
				data = tool
			}
			name := data["name"]
			if text, ok := name.(string); ok {
				name = names.Safe(strings.TrimSpace(text))
			}
			schema := data["parameters"]
			if !concerns.Truthy(schema) {
				schema = data["input_schema"]
			}
			if !concerns.Truthy(schema) {
				schema = map[string]any{"type": "object", "properties": map[string]any{}, "required": []any{}}
			}
			description := data["description"]
			if !concerns.Truthy(description) {
				description = ""
			}
			converted = append(converted, map[string]any{"name": name, "description": description, "input_schema": schema})
		}
		if len(converted) > 0 {
			if last := concerns.Object(converted[len(converted)-1]); last != nil {
				last["cache_control"] = ephemeral1h()
			}
		}
		result["tools"] = converted
	}
	if concerns.Truthy(body["tool_choice"]) {
		result["tool_choice"] = toolChoiceFromChat(body["tool_choice"], names)
	}
	return result, names
}

// markLastAssistant puts a cache breakpoint on the last cacheable block of the
// last assistant message (openai-to-claude.js:102-117).
func markLastAssistant(messages []any) {
	for i := len(messages) - 1; i >= 0; i-- {
		message := concerns.Object(messages[i])
		blocks := concerns.Items(message["content"])
		if concerns.Text(message["role"]) != "assistant" || len(blocks) == 0 {
			continue
		}
		for j := len(blocks) - 1; j >= 0; j-- {
			block := concerns.Object(blocks[j])
			switch concerns.Text(block["type"]) {
			case "text", "tool_use", "tool_result", "image":
				block["cache_control"] = map[string]any{"type": "ephemeral"}
				return
			}
		}
		return
	}
}

// contentBlocks is openai-to-claude.js:204-302.
func contentBlocks(message map[string]any, names *concerns.ToolNames) []any {
	var blocks []any
	switch concerns.Text(message["role"]) {
	case "tool":
		blocks = append(blocks, map[string]any{"type": "tool_result", "tool_use_id": message["tool_call_id"], "content": message["content"]})
	case "user":
		if text, ok := message["content"].(string); ok {
			if text != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": text})
			}
			break
		}
		for _, raw := range concerns.Items(message["content"]) {
			part := concerns.Object(raw)
			switch concerns.Text(part["type"]) {
			case "text":
				if concerns.Truthy(part["text"]) {
					blocks = append(blocks, map[string]any{"type": "text", "text": part["text"]})
				}
			case "tool_result":
				block := map[string]any{"type": "tool_result", "tool_use_id": part["tool_use_id"], "content": part["content"]}
				if concerns.Truthy(part["is_error"]) {
					block["is_error"] = part["is_error"]
				}
				blocks = append(blocks, block)
			case "image_url":
				url := concerns.Text(concerns.Object(part["image_url"])["url"])
				if mediaType, data, ok := concerns.ParseDataURI(url); ok {
					blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": mediaType, "data": data}})
				} else if strings.HasPrefix(url, "http://") || strings.HasPrefix(url, "https://") {
					blocks = append(blocks, map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": url}})
				}
			case "image":
				if concerns.Truthy(part["source"]) {
					blocks = append(blocks, map[string]any{"type": "image", "source": part["source"]})
				}
			case "file":
				if file := concerns.Object(part["file"]); file != nil {
					if mediaType, data, ok := concerns.ParseDataURI(file["file_data"]); ok && mediaType == "application/pdf" {
						blocks = append(blocks, map[string]any{"type": "document", "source": map[string]any{"type": "base64", "media_type": mediaType, "data": data}})
					}
				}
			}
		}
	case "assistant":
		if reasoning := concerns.Text(message["reasoning_content"]); reasoning != "" {
			blocks = append(blocks, map[string]any{"type": "thinking", "thinking": reasoning})
		}
		if parts := concerns.Items(message["content"]); parts != nil {
			for _, raw := range parts {
				part := concerns.Object(raw)
				switch concerns.Text(part["type"]) {
				case "text":
					if concerns.Truthy(part["text"]) {
						blocks = append(blocks, map[string]any{"type": "text", "text": part["text"]})
					}
				case "tool_use":
					blocks = append(blocks, map[string]any{"type": "tool_use", "id": part["id"], "name": part["name"], "input": part["input"]})
				case "thinking":
					thinking := map[string]any{}
					for key, value := range part {
						if key != "cache_control" {
							thinking[key] = value
						}
					}
					blocks = append(blocks, thinking)
				}
			}
		} else if concerns.Truthy(message["content"]) {
			if text := concerns.ExtractText(message["content"], "\n"); text != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": text})
			}
		}
		for _, raw := range concerns.Items(message["tool_calls"]) {
			call := concerns.Object(raw)
			if concerns.Text(call["type"]) != "function" {
				continue
			}
			function := concerns.Object(call["function"])
			input := function["arguments"]
			if text, ok := input.(string); ok {
				var parsed any
				if err := json.Unmarshal([]byte(text), &parsed); err == nil {
					input = parsed
				}
			}
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": call["id"], "name": names.Safe(concerns.Text(function["name"])), "input": input})
		}
	}
	return blocks
}

// toolChoiceFromChat is openai-to-claude.js:310-338. Anthropic only accepts
// auto, any, tool and none; the OpenAI "function" type is never passed through.
func toolChoiceFromChat(choice any, names *concerns.ToolNames) any {
	if !concerns.Truthy(choice) {
		return map[string]any{"type": "auto"}
	}
	if text, ok := choice.(string); ok {
		switch text {
		case "required":
			return map[string]any{"type": "any"}
		case "none":
			return map[string]any{"type": "none"}
		}
		return map[string]any{"type": "auto"}
	}
	object := concerns.Object(choice)
	if name := concerns.Text(concerns.Object(object["function"])["name"]); name != "" {
		return map[string]any{"type": "tool", "name": names.Safe(strings.TrimSpace(name))}
	}
	switch concerns.Text(object["type"]) {
	case "auto", "any", "tool", "none":
		if concerns.Text(object["type"]) == "tool" {
			if name, ok := object["name"].(string); ok {
				copied := map[string]any{}
				for key, value := range object {
					copied[key] = value
				}
				copied["name"] = names.Safe(strings.TrimSpace(name))
				return copied
			}
		}
		return object
	}
	return map[string]any{"type": "auto"}
}

// EnsureTrailingUserTurn restores a user turn when translation emptied the
// client's last one (translator/formats/claude.js:345-349). A client that
// itself ended on an assistant turn is prefilling, which is its choice.
func EnsureTrailingUserTurn(messages []any, clientEndsAssistant bool) []any {
	if clientEndsAssistant || len(messages) == 0 {
		return messages
	}
	if concerns.Text(concerns.Object(messages[len(messages)-1])["role"]) != "assistant" {
		return messages
	}
	return append(messages, map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": TrailingUserPlaceholder}}})
}
