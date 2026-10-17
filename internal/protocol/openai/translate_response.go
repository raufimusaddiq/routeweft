package openai

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func integer(value any) int64 {
	number, _ := value.(json.Number)
	result, _ := number.Int64()
	return result
}

func translatedUsage(usage map[string]any, responses bool) map[string]any {
	if usage == nil {
		return nil
	}
	input, output := "input_tokens", "output_tokens"
	inputDetails, outputDetails := "input_tokens_details", "output_tokens_details"
	if responses {
		input, output = "prompt_tokens", "completion_tokens"
		inputDetails, outputDetails = "prompt_tokens_details", "completion_tokens_details"
	}
	result := map[string]any{"total_tokens": integer(usage[input]) + integer(usage[output])}
	toInput, toOutput := "prompt_tokens", "completion_tokens"
	toInputDetails, toOutputDetails := "prompt_tokens_details", "completion_tokens_details"
	if responses {
		toInput, toOutput = "input_tokens", "output_tokens"
		toInputDetails, toOutputDetails = "input_tokens_details", "output_tokens_details"
	}
	result[toInput], result[toOutput] = usage[input], usage[output]
	if usage[inputDetails] != nil {
		result[toInputDetails] = usage[inputDetails]
	}
	if usage[outputDetails] != nil {
		result[toOutputDetails] = usage[outputDetails]
	}
	if usage["total_tokens"] != nil {
		result["total_tokens"] = usage["total_tokens"]
	}
	return result
}

// CustomToolNames retains the request-local names needed to undo the function
// wrapper used for Responses custom tools on a Chat-only upstream.
func CustomToolNames(request *ResponsesRequest) map[string]bool {
	body, _ := wireObject(request.Raw)
	result := map[string]bool{}
	tools := append([]any(nil), items(body["tools"])...)
	for _, raw := range items(body["input"]) {
		item := object(raw)
		if item["type"] == "additional_tools" {
			tools = append(tools, items(item["tools"])...)
		}
		if item["type"] == "custom_tool_call" {
			result[text(item["name"])] = true
		}
	}
	for _, raw := range tools {
		tool := object(raw)
		if tool["type"] == "custom" {
			result[text(tool["name"])] = true
		}
	}
	return result
}

func customInput(arguments string) string {
	value, err := wireObject([]byte(arguments))
	if err == nil {
		if input, ok := value["input"].(string); ok {
			return input
		}
	}
	return arguments
}

func responseOutput(message map[string]any, id string, customTools map[string]bool) ([]any, error) {
	output, _, err := chatInput([]any{message})
	if err != nil {
		return nil, err
	}
	if refusal := text(message["refusal"]); refusal != "" {
		output = append(output, map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "refusal", "refusal": refusal}}})
	}
	for index, raw := range output {
		item := object(raw)
		item["id"] = fmt.Sprintf("item_%s_%d", id, index)
		switch item["type"] {
		case "message", "function_call":
			item["status"] = "completed"
		}
		if item["type"] == "function_call" && customTools[text(item["name"])] {
			item["type"], item["input"] = "custom_tool_call", customInput(text(item["arguments"]))
			delete(item, "arguments")
		}
	}
	return output, nil
}

// ChatResponseToResponses converts a completed Chat response, including tools,
// refusal, reasoning and cache/reasoning token details.
func ChatResponseToResponses(body []byte, customTools map[string]bool) ([]byte, error) {
	chat, err := wireObject(body)
	if err != nil {
		return nil, err
	}
	if chat["error"] != nil {
		return json.Marshal(map[string]any{"error": chat["error"]})
	}
	choices := items(chat["choices"])
	if len(choices) == 0 || object(object(choices[0])["message"]) == nil {
		return nil, errors.New("upstream Chat response has no message")
	}
	choice := object(choices[0])
	id := "resp_" + text(chat["id"])
	message := object(choice["message"])
	message["role"] = "assistant"
	output, err := responseOutput(message, id, customTools)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"id": id, "object": "response", "created_at": chat["created"], "model": chat["model"], "status": "completed", "output": output, "error": nil}
	if choice["finish_reason"] == "length" || choice["finish_reason"] == "content_filter" {
		result["status"] = "incomplete"
		reason := "max_output_tokens"
		if choice["finish_reason"] == "content_filter" {
			reason = "content_filter"
		}
		result["incomplete_details"] = map[string]any{"reason": reason}
	}
	if usage := translatedUsage(object(chat["usage"]), true); usage != nil {
		result["usage"] = usage
	}
	return json.Marshal(result)
}

// ResponsesResponseToChat folds output items into one assistant turn; parallel
// calls keep their own call_id and arguments instead of sharing a stream index.
func ResponsesResponseToChat(body []byte) ([]byte, error) {
	response, err := wireObject(body)
	if err != nil {
		return nil, err
	}
	if response["error"] != nil {
		return json.Marshal(map[string]any{"error": response["error"]})
	}
	output, ok := response["output"].([]any)
	if !ok {
		return nil, errors.New("upstream Responses response has no output array")
	}
	message := map[string]any{"role": "assistant", "content": ""}
	var content, reasoning, refusal strings.Builder
	var calls []any
	for _, raw := range output {
		item := object(raw)
		switch item["type"] {
		case "message":
			for _, rawBlock := range items(item["content"]) {
				block := object(rawBlock)
				content.WriteString(text(block["text"]))
				refusal.WriteString(text(block["refusal"]))
			}
		case "reasoning":
			reasoning.WriteString(instructionText(item["summary"]))
			copyFields(message, item, "encrypted_content")
		case "function_call", "custom_tool_call":
			arguments := jsonText(item["arguments"])
			if item["type"] == "custom_tool_call" {
				arguments = jsonText(map[string]any{"input": jsonText(item["input"])})
			}
			calls = append(calls, map[string]any{"id": item["call_id"], "type": "function", "function": map[string]any{"name": item["name"], "arguments": arguments}})
		}
	}
	message["content"] = content.String()
	finish := "stop"
	if len(calls) > 0 {
		message["tool_calls"] = calls
		finish = "tool_calls"
		if content.Len() == 0 {
			message["content"] = nil
		}
	}
	if reasoning.Len() > 0 {
		message["reasoning_content"] = reasoning.String()
	}
	if refusal.Len() > 0 {
		message["refusal"] = refusal.String()
	}
	if response["status"] == "incomplete" {
		finish = "length"
		if object(response["incomplete_details"])["reason"] == "content_filter" {
			finish = "content_filter"
		}
	}
	result := map[string]any{"id": "chatcmpl_" + text(response["id"]), "object": "chat.completion", "created": response["created_at"], "model": response["model"], "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}}
	if usage := translatedUsage(object(response["usage"]), false); usage != nil {
		result["usage"] = usage
	}
	return json.Marshal(result)
}
