package openai

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
)

func wireObject(body []byte) (map[string]any, error) {
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

func object(value any) map[string]any { result, _ := value.(map[string]any); return result }
func items(value any) []any           { result, _ := value.([]any); return result }
func text(value any) string           { result, _ := value.(string); return result }

func jsonText(value any) string {
	if value == nil {
		return ""
	}
	if result, ok := value.(string); ok {
		return result
	}
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func copyFields(dst, src map[string]any, names ...string) {
	for _, name := range names {
		if value, ok := src[name]; ok {
			dst[name] = value
		}
	}
}

func contentBlocks(content any, responses bool, role string) (any, error) {
	if content == nil {
		return nil, nil
	}
	if value, ok := content.(string); ok {
		if !responses {
			return value, nil
		}
		content = []any{map[string]any{"type": "text", "text": value}}
	}
	blocks, ok := content.([]any)
	if !ok {
		if block := object(content); block != nil {
			blocks = []any{block}
		} else {
			return nil, errors.New("message content must be text or content blocks")
		}
	}
	result := make([]any, 0, len(blocks))
	for _, raw := range blocks {
		block := object(raw)
		if block == nil {
			return nil, errors.New("content block must be an object")
		}
		converted := make(map[string]any, len(block))
		for name, value := range block {
			converted[name] = value
		}
		switch text(block["type"]) {
		case "text", "input_text", "output_text":
			converted["type"] = "text"
			if responses {
				converted["type"] = "input_text"
				if role == "assistant" {
					converted["type"] = "output_text"
				}
			}
		case "image_url":
			if responses {
				image := object(block["image_url"])
				url := text(block["image_url"])
				if image != nil {
					url = text(image["url"])
				}
				converted["type"], converted["image_url"] = "input_image", url
				copyFields(converted, image, "detail")
			}
		case "input_image":
			if !responses {
				url := block["image_url"]
				if url == nil {
					url = block["file_id"]
				}
				image := map[string]any{"url": url}
				copyFields(image, block, "detail")
				converted = map[string]any{"type": "image_url", "image_url": image}
			}
		case "file":
			if responses {
				converted = map[string]any{"type": "input_file"}
				copyFields(converted, object(block["file"]), "file_id", "file_data", "filename")
			}
		case "input_file":
			if !responses {
				file := map[string]any{}
				copyFields(file, block, "file_id", "file_data", "filename")
				converted = map[string]any{"type": "file", "file": file}
			}
		}
		result = append(result, converted)
	}
	return result, nil
}

func instructionText(content any) string {
	if value, ok := content.(string); ok {
		return value
	}
	var result []string
	for _, block := range items(content) {
		value := text(object(block)["text"])
		if value == "" {
			value = text(object(block)["content"])
		}
		if value != "" {
			result = append(result, value)
		}
	}
	return strings.Join(result, "\n")
}

func reasoningText(message map[string]any) string {
	for _, name := range []string{"reasoning_content", "reasoning", "reasoning_details"} {
		if value := instructionText(message[name]); value != "" {
			return value
		}
	}
	return ""
}

func chatInput(messages []any) ([]any, string, error) {
	input := make([]any, 0, len(messages))
	var instructions []string
	for _, raw := range messages {
		message := object(raw)
		role := text(message["role"])
		switch role {
		case "system", "developer":
			instructions = append(instructions, instructionText(message["content"]))
			continue
		case "tool":
			input = append(input, map[string]any{"type": "function_call_output", "call_id": message["tool_call_id"], "output": jsonText(message["content"])})
			continue
		case "user", "assistant":
		default:
			return nil, "", fmt.Errorf("unsupported message role %q", role)
		}
		if role == "assistant" {
			reasoning := map[string]any{"type": "reasoning", "summary": []any{}}
			value := reasoningText(message)
			if value != "" {
				reasoning["summary"] = []any{map[string]any{"type": "summary_text", "text": value}}
			}
			copyFields(reasoning, message, "encrypted_content")
			if reasoning["encrypted_content"] == nil {
				if value := message["reasoning_encrypted_content"]; value != nil {
					reasoning["encrypted_content"] = value
				} else if value := object(message["reasoning"])["encrypted_content"]; value != nil {
					reasoning["encrypted_content"] = value
				}
			}
			if value != "" || reasoning["encrypted_content"] != nil {
				input = append(input, reasoning)
			}
		}
		content, err := contentBlocks(message["content"], true, role)
		if err != nil {
			return nil, "", err
		}
		if len(items(content)) > 0 {
			input = append(input, map[string]any{"type": "message", "role": role, "content": content})
		}
		for _, rawCall := range items(message["tool_calls"]) {
			call := object(rawCall)
			function := object(call["function"])
			if text(function["name"]) == "" {
				continue
			}
			arguments := jsonText(function["arguments"])
			if !json.Valid([]byte(arguments)) {
				arguments = "{}"
			}
			input = append(input, map[string]any{"type": "function_call", "call_id": call["id"], "name": function["name"], "arguments": arguments})
		}
	}
	return input, strings.Join(instructions, "\n"), nil
}

func responsesMessages(input any) ([]any, error) {
	if value, ok := input.(string); ok {
		if strings.TrimSpace(value) == "" {
			value = "..."
		}
		input = []any{map[string]any{"role": "user", "content": value}}
	}
	list, ok := input.([]any)
	if !ok {
		return nil, errors.New("Responses input must be a string or array")
	}
	if len(list) == 0 {
		list = []any{map[string]any{"role": "user", "content": "..."}}
	}
	messages := make([]any, 0, len(list))
	var pendingReasoning, pendingEncrypted string
	for _, raw := range list {
		item := object(raw)
		kind := text(item["type"])
		if kind == "" && item["role"] != nil {
			kind = "message"
		}
		switch kind {
		case "reasoning":
			pendingReasoning = instructionText(item["summary"])
			pendingEncrypted = text(item["encrypted_content"])
		case "message":
			role := text(item["role"])
			if role != "user" && role != "assistant" && role != "system" && role != "developer" {
				return nil, fmt.Errorf("unsupported Responses role %q", role)
			}
			content, err := contentBlocks(item["content"], false, role)
			if err != nil {
				return nil, err
			}
			message := map[string]any{"role": role, "content": content}
			if role == "assistant" {
				if pendingReasoning != "" {
					message["reasoning_content"] = pendingReasoning
				}
				if pendingEncrypted != "" {
					message["encrypted_content"] = pendingEncrypted
				}
			}
			pendingReasoning, pendingEncrypted = "", ""
			messages = append(messages, message)
		case "function_call", "custom_tool_call":
			if text(item["name"]) == "" {
				continue
			}
			arguments := jsonText(item["arguments"])
			if kind == "custom_tool_call" {
				arguments = jsonText(map[string]any{"input": jsonText(item["input"])})
			}
			call := map[string]any{"id": item["call_id"], "type": "function", "function": map[string]any{"name": item["name"], "arguments": arguments}}
			var message map[string]any
			if len(messages) > 0 {
				last := object(messages[len(messages)-1])
				if last["role"] == "assistant" {
					message = last
				}
			}
			if message == nil {
				message = map[string]any{"role": "assistant", "content": nil}
				messages = append(messages, message)
			}
			message["tool_calls"] = append(items(message["tool_calls"]), call)
			if pendingReasoning != "" {
				message["reasoning_content"] = pendingReasoning
			}
			if pendingEncrypted != "" {
				message["encrypted_content"] = pendingEncrypted
			}
			pendingReasoning, pendingEncrypted = "", ""
		case "function_call_output", "custom_tool_call_output":
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": item["call_id"], "content": jsonText(item["output"])})
		case "additional_tools":
			// Tool declarations are collected separately from conversation history.
		default:
			return nil, fmt.Errorf("unsupported Responses input item %q", kind)
		}
	}
	return messages, nil
}

// ChatToResponses translates only differing wire protocols; native requests
// continue to use MarshalBody, including their unknown forward-compatible fields.
func ChatToResponses(request *ChatRequest) ([]byte, error) {
	body, err := wireObject(request.Raw)
	if err != nil {
		return nil, err
	}
	input, instructions, err := chatInput(items(body["messages"]))
	if err != nil {
		return nil, err
	}
	result := map[string]any{"model": request.Model, "stream": request.Stream, "store": false, "input": input, "instructions": instructions}
	copyFields(result, body, "temperature", "top_p", "parallel_tool_calls", "metadata", "service_tier", "prompt_cache_key", "reasoning")
	for _, name := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
		if value, ok := body[name]; ok {
			result["max_output_tokens"] = value
		}
	}
	if value, ok := body["reasoning_effort"]; ok {
		result["reasoning"] = map[string]any{"effort": value, "summary": "auto"}
	}
	if format := object(body["response_format"]); format != nil {
		value := format
		if format["type"] == "json_schema" {
			value = object(format["json_schema"])
			if value == nil {
				return nil, errors.New("json_schema response format requires an object")
			}
			value["type"] = "json_schema"
		}
		result["text"] = map[string]any{"format": value}
	}
	if tools, ok := body["tools"]; ok {
		converted := make([]any, 0, len(items(tools)))
		for _, raw := range items(tools) {
			tool := object(raw)
			if tool["type"] == "function" {
				function := object(tool["function"])
				if text(function["name"]) == "" {
					continue
				}
				tool = map[string]any{"type": "function"}
				copyFields(tool, function, "name", "description", "parameters", "strict")
			}
			converted = append(converted, tool)
		}
		result["tools"] = converted
	}
	if choice, ok := body["tool_choice"]; ok {
		if value := object(choice); value["type"] == "function" {
			choice = map[string]any{"type": "function", "name": object(value["function"])["name"]}
		}
		result["tool_choice"] = choice
	}
	return json.Marshal(result)
}

// ResponsesToChat preserves tool correlation and reasoning continuity while
// removing Responses-only options that Chat endpoints reject.
func ResponsesToChat(request *ResponsesRequest) ([]byte, error) {
	body, err := wireObject(request.Raw)
	if err != nil {
		return nil, err
	}
	messages, err := responsesMessages(body["input"])
	if err != nil {
		return nil, err
	}
	if instructions := text(body["instructions"]); instructions != "" {
		messages = append([]any{map[string]any{"role": "system", "content": instructions}}, messages...)
	}
	result := map[string]any{"model": request.Model, "stream": request.Stream, "messages": messages}
	copyFields(result, body, "temperature", "top_p", "parallel_tool_calls", "metadata", "service_tier")
	if request.Stream {
		result["stream_options"] = map[string]any{"include_usage": true}
	}
	if value, ok := body["max_output_tokens"]; ok {
		result["max_tokens"] = value
	}
	if value := object(body["reasoning"])["effort"]; value != nil {
		result["reasoning_effort"] = value
	}
	if format := object(object(body["text"])["format"]); format != nil {
		result["response_format"] = format
		if format["type"] == "json_schema" {
			delete(format, "type")
			result["response_format"] = map[string]any{"type": "json_schema", "json_schema": format}
		}
	}
	tools := append([]any(nil), items(body["tools"])...)
	for _, raw := range items(body["input"]) {
		item := object(raw)
		if item["type"] == "additional_tools" {
			tools = append(tools, items(item["tools"])...)
		}
	}
	if len(tools) > 0 {
		converted := make([]any, 0, len(tools))
		for _, raw := range tools {
			tool := object(raw)
			if tool["function"] != nil {
				converted = append(converted, tool)
				continue
			}
			if text(tool["name"]) == "" {
				continue
			}
			function := map[string]any{}
			copyFields(function, tool, "name", "description", "parameters", "strict")
			if tool["type"] == "custom" {
				function["parameters"] = map[string]any{"type": "object", "properties": map[string]any{"input": map[string]any{"type": "string"}}, "required": []any{"input"}, "additionalProperties": false}
			}
			converted = append(converted, map[string]any{"type": "function", "function": function})
		}
		result["tools"] = converted
	}
	if choice, ok := body["tool_choice"]; ok {
		if value := object(choice); value["type"] == "function" || value["type"] == "custom" {
			choice = map[string]any{"type": "function", "function": map[string]any{"name": value["name"]}}
		}
		result["tool_choice"] = choice
	}
	return json.Marshal(result)
}
