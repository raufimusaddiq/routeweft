package anthropic

import (
	"encoding/json"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// TrailingUserPlaceholder ends a conversation that would otherwise end on an
// assistant turn (translator/formats/claude.js:343).
const TrailingUserPlaceholder = "Continue."

// ephemeral is the plain 5-minute cache breakpoint. The pin asks for ttl "1h"
// (openai-to-claude.js:142,184), which Anthropic gates behind the
// extended-cache-ttl beta header; the gateway sends no such header, so a body
// asking for it would be rejected or silently downgraded (PARITY V13).
var ephemeral = func() map[string]any { return map[string]any{"type": "ephemeral"} }

// ChatToMessages is the request leg openai-chat>anthropic-messages
// (translator/request/openai-to-claude.js:17-201). The returned ToolNames maps
// the names sent upstream back to the ones the client declared; the response
// legs need it.
func ChatToMessages(body map[string]any, model string, stream bool) (map[string]any, *concerns.ToolNames, error) {
	if err := rejectUncarriable(body); err != nil {
		return nil, nil, err
	}
	names := concerns.NewToolNames(concerns.ChatToolNames(body))
	limits := body
	if !concerns.Truthy(body["max_tokens"]) && concerns.Truthy(body["max_completion_tokens"]) {
		// The budget the caller asked for, under its current name. The pin reads
		// only max_tokens and would send the default 64000 (PARITY V12).
		limits = map[string]any{"max_tokens": body["max_completion_tokens"], "tools": body["tools"]}
	}
	result := map[string]any{"model": model, "max_tokens": AdjustMaxTokens(limits, 0), "stream": stream}
	if temperature, ok := body["temperature"]; ok {
		result["temperature"] = temperature
	}
	if topP, ok := body["top_p"]; ok {
		// Current Claude models reject a request that sets both; temperature,
		// the older and more common control, wins (PARITY V15).
		if _, both := result["temperature"]; !both {
			result["top_p"] = topP
		}
	}
	if stop := stopSequences(body["stop"]); len(stop) > 0 {
		result["stop_sequences"] = stop
	}
	if user := concerns.Text(body["user"]); user != "" {
		result["metadata"] = map[string]any{"user_id": user}
	}
	messages := []any{}
	var systemParts []string
	if source := concerns.Items(body["messages"]); source != nil {
		var others []map[string]any
		for _, raw := range source {
			message := concerns.Object(raw)
			switch role := concerns.Text(message["role"]); role {
			case "system", "developer":
				// The pin drops a developer message (openai-to-claude.js:204-299);
				// Chat treats it as a system message, so does this (PARITY V12).
				systemParts = append(systemParts, concerns.ExtractText(message["content"], "\n"))
			case "user", "assistant", "tool":
				others = append(others, message)
			default:
				return nil, nil, unsupported("message role %q", role)
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
			blocks, err := contentBlocks(message, names)
			if err != nil {
				return nil, nil, err
			}
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
	// The pin prepends "You are Claude Code, Anthropic's official CLI for Claude."
	// (openai-to-claude.js:136). A translation leg has no business telling a
	// third-party model who it is or spending tokens on every request, so only
	// the caller's own system text is sent (PARITY V14).
	if len(systemParts) > 0 {
		result["system"] = []any{map[string]any{"type": "text", "text": strings.Join(systemParts, "\n"), "cache_control": ephemeral()}}
	}
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
				last["cache_control"] = ephemeral()
			}
		}
		if len(converted) > 0 {
			// Anthropic rejects an empty tools array, and a tool_choice with no tools.
			result["tools"] = converted
		}
	}
	if result["tools"] != nil {
		if concerns.Truthy(body["tool_choice"]) {
			result["tool_choice"] = toolChoiceFromChat(body["tool_choice"], names)
		}
		if parallel, ok := body["parallel_tool_calls"].(bool); ok && !parallel {
			choice, _ := result["tool_choice"].(map[string]any)
			if choice == nil {
				choice = map[string]any{"type": "auto"}
			}
			if concerns.Text(choice["type"]) != "none" {
				choice["disable_parallel_tool_use"] = true
			}
			result["tool_choice"] = choice
		}
	}
	return result, names, nil
}

// rejectUncarriable refuses a Chat request that asks for something Anthropic
// cannot do, instead of answering without it. Dropped hints that only shape
// sampling (seed, penalties, logit_bias, top_logprobs) are recorded in PARITY 6.3.
func rejectUncarriable(body map[string]any) error {
	if n := concerns.Int(body["n"]); n > 1 {
		return unsupported("n=%d: Anthropic returns one completion", n)
	}
	if concerns.Truthy(body["logprobs"]) {
		return unsupported("logprobs: Anthropic reports none")
	}
	for _, modality := range concerns.Items(body["modalities"]) {
		if concerns.Text(modality) == "audio" {
			return unsupported("audio output")
		}
	}
	for _, field := range []string{"audio", "functions", "function_call"} {
		if concerns.Truthy(body[field]) {
			return unsupported("request field %q (use tools)", field)
		}
	}
	return nil
}

// stopSequences accepts a Chat stop value, a string or a list of strings.
func stopSequences(stop any) []any {
	if text, ok := stop.(string); ok && text != "" {
		return []any{text}
	}
	var sequences []any
	for _, raw := range concerns.Items(stop) {
		if text, ok := raw.(string); ok && text != "" {
			sequences = append(sequences, text)
		}
	}
	return sequences
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

// contentBlocks is openai-to-claude.js:204-302. A part Anthropic cannot take is
// an error: dropping it would answer a question about content the model never saw.
func contentBlocks(message map[string]any, names *concerns.ToolNames) ([]any, error) {
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
			switch kind := concerns.Text(part["type"]); kind {
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
				} else {
					return nil, unsupported("image_url that is neither a data URI nor an http(s) URL")
				}
			case "image":
				if !concerns.Truthy(part["source"]) {
					return nil, unsupported("image part with no source")
				}
				blocks = append(blocks, map[string]any{"type": "image", "source": part["source"]})
			case "file":
				// OpenAI file part to Claude document (PDF only; Claude rejects
				// other types). The reverse is documentPart.
				file := concerns.Object(part["file"])
				mediaType, data, ok := concerns.ParseDataURI(file["file_data"])
				if !ok || mediaType != "application/pdf" {
					return nil, unsupported("file part that is not an inline PDF (file_id and other types cannot be carried)")
				}
				document := map[string]any{"type": "document", "source": map[string]any{"type": "base64", "media_type": mediaType, "data": data}}
				if name := concerns.Text(file["filename"]); name != "" {
					document["title"] = name
				}
				blocks = append(blocks, document)
			default:
				return nil, unsupported("user content part %q", kind)
			}
		}
	case "assistant":
		// reasoning_content (and any thinking part without a signature) is not sent:
		// Anthropic only accepts a thinking block it signed, so an unsigned one is
		// a request it rejects (PARITY V15). Signed parts below are kept.
		if parts := concerns.Items(message["content"]); parts != nil {
			for _, raw := range parts {
				part := concerns.Object(raw)
				switch kind := concerns.Text(part["type"]); kind {
				case "text":
					if concerns.Truthy(part["text"]) {
						blocks = append(blocks, map[string]any{"type": "text", "text": part["text"]})
					}
				case "refusal":
					if text := concerns.Text(part["refusal"]); text != "" {
						blocks = append(blocks, map[string]any{"type": "text", "text": text})
					}
				case "tool_use":
					blocks = append(blocks, map[string]any{"type": "tool_use", "id": part["id"], "name": part["name"], "input": concerns.ToolInput(part["input"])})
				case "thinking":
					if concerns.Text(part["signature"]) == "" {
						continue
					}
					thinking := map[string]any{}
					for key, value := range part {
						if key != "cache_control" {
							thinking[key] = value
						}
					}
					blocks = append(blocks, thinking)
				default:
					return nil, unsupported("assistant content part %q", kind)
				}
			}
		} else if concerns.Truthy(message["content"]) {
			if text := concerns.ExtractText(message["content"], "\n"); text != "" {
				blocks = append(blocks, map[string]any{"type": "text", "text": text})
			}
		}
		for _, raw := range concerns.Items(message["tool_calls"]) {
			call := concerns.Object(raw)
			if kind := concerns.Text(call["type"]); kind != "function" {
				return nil, unsupported("tool call type %q", kind)
			}
			function := concerns.Object(call["function"])
			input := concerns.ToolInput(function["arguments"])
			blocks = append(blocks, map[string]any{"type": "tool_use", "id": call["id"], "name": names.Safe(concerns.Text(function["name"])), "input": input})
		}
	}
	return blocks, nil
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
