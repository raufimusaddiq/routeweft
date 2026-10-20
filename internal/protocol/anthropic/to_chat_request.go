package anthropic

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// Output-token bounds the oracle applies to every translated request
// (config/runtimeConfig.js:64-65).
const (
	DefaultMaxTokens int64 = 64000
	DefaultMinTokens int64 = 32000
)

var billingHeader = regexp.MustCompile(`(?i)^x-anthropic-billing-header:[^\n]*(?:\r?\n)?`)

// AdjustMaxTokens is translator/formats/maxTokens.js:12-33. ceiling <= 0 means
// the default; a model-aware ceiling needs a capability table Routeweft does
// not have yet, so every caller passes 0 (the pin does the same for a model it
// has no capabilities for).
func AdjustMaxTokens(body map[string]any, ceiling int64) int64 {
	if ceiling <= 0 {
		ceiling = DefaultMaxTokens
	}
	maxTokens := concerns.Int(body["max_tokens"])
	if maxTokens == 0 {
		maxTokens = DefaultMaxTokens
	}
	if tools := concerns.Items(body["tools"]); len(tools) > 0 && maxTokens < DefaultMinTokens {
		maxTokens = DefaultMinTokens
	}
	if budget := concerns.Int(concerns.Object(body["thinking"])["budget_tokens"]); budget != 0 && maxTokens <= budget {
		maxTokens = budget + 1024
	}
	if maxTokens > ceiling {
		maxTokens = ceiling
	}
	return maxTokens
}

// MessagesToChat is the request leg anthropic-messages>openai-chat
// (translator/request/claude-to-openai.js:14-94). body is the client's Messages
// request, model the upstream model. The leg mutates body's message content the
// way the pin does (a bare content object becomes a one-element array).
func MessagesToChat(body map[string]any, model string, stream bool) map[string]any {
	result := map[string]any{"model": model, "messages": []any{}, "stream": stream}
	if concerns.Truthy(body["max_tokens"]) {
		result["max_tokens"] = AdjustMaxTokens(body, 0)
	}
	if temperature, ok := body["temperature"]; ok {
		result["temperature"] = temperature
	}
	messages := []any{}
	if concerns.Truthy(body["system"]) {
		var system string
		if parts := concerns.Items(body["system"]); parts != nil {
			var texts []string
			for _, part := range parts {
				if text := stripBillingHeader(concerns.Text(concerns.Object(part)["text"])); text != "" {
					texts = append(texts, text)
				}
			}
			system = strings.Join(texts, "\n")
		} else {
			system = stripBillingHeader(concerns.Text(body["system"]))
		}
		if system != "" {
			messages = append(messages, map[string]any{"role": "system", "content": system})
		}
	}
	for _, raw := range concerns.Items(body["messages"]) {
		converted := convertMessage(concerns.Object(raw))
		messages = append(messages, converted...)
	}
	fixMissingToolResponses(&messages)
	result["messages"] = messages
	if tools := concerns.Items(body["tools"]); tools != nil {
		converted := make([]any, 0, len(tools))
		for _, raw := range tools {
			tool := concerns.Object(raw)
			function := map[string]any{"description": jsText(tool["description"]), "parameters": inputSchema(tool["input_schema"])}
			if name, ok := tool["name"]; ok {
				function["name"] = name
			}
			converted = append(converted, map[string]any{"type": "function", "function": function})
		}
		result["tools"] = converted
	}
	if concerns.Truthy(body["tool_choice"]) {
		result["tool_choice"] = toolChoiceToChat(body["tool_choice"])
	}
	reasoning := concerns.Object(body["reasoning"])
	if effort, ok := body["reasoning_effort"]; ok {
		result["reasoning_effort"] = effort
	} else if effort, ok := reasoning["effort"]; ok {
		result["reasoning_effort"] = effort
	}
	if value, ok := body["reasoning"]; ok {
		result["reasoning"] = value
	}
	return result
}

func stripBillingHeader(text string) string { return billingHeader.ReplaceAllString(text, "") }

// jsText is String(value || "") for a description.
func jsText(value any) string {
	if !concerns.Truthy(value) {
		return ""
	}
	if text, ok := value.(string); ok {
		return text
	}
	if number, ok := value.(json.Number); ok {
		return number.String()
	}
	return fmt.Sprint(value)
}

func inputSchema(value any) any {
	if concerns.Truthy(value) {
		return value
	}
	return map[string]any{"type": "object", "properties": map[string]any{}}
}

// collapseTextParts: a lone text part becomes a plain string
// (concerns/message.js:5-7). A text part without text yields no content.
func collapseTextParts(parts []any) any {
	if len(parts) == 1 {
		if part := concerns.Object(parts[0]); concerns.Text(part["type"]) == "text" {
			return part["text"]
		}
	}
	return parts
}

func dataURI(mediaType, data any) string {
	return fmt.Sprintf("data:%s;base64,%s", jsText(mediaType), jsText(data))
}

// convertMessage is claude-to-openai.js:144-270: zero, one or several Chat
// messages for one Messages message.
func convertMessage(message map[string]any) []any {
	if message == nil {
		return nil
	}
	if content := message["content"]; content != nil {
		if object := concerns.Object(content); object != nil {
			message["content"] = []any{object}
		}
	}
	role := concerns.Text(message["role"])
	if role == "system" {
		if text := systemReminderText(message["content"]); text != "" {
			return []any{map[string]any{"role": "user", "content": text}}
		}
		return nil
	}
	chatRole := "assistant"
	if role == "user" || role == "tool" {
		chatRole = "user"
	}
	if text, ok := message["content"].(string); ok {
		return []any{map[string]any{"role": chatRole, "content": text}}
	}
	blocks := concerns.Items(message["content"])
	if blocks == nil {
		return nil
	}
	var parts, toolCalls, toolResults []any
	for _, raw := range blocks {
		block := concerns.Object(raw)
		switch concerns.Text(block["type"]) {
		case "text":
			part := map[string]any{"type": "text"}
			if text, ok := block["text"]; ok {
				part["text"] = text
			}
			parts = append(parts, part)
		case "image":
			if part := imagePart(concerns.Object(block["source"])); part != nil {
				parts = append(parts, part)
			}
		case "tool_use":
			input := block["input"]
			if !concerns.Truthy(input) {
				input = map[string]any{}
			}
			function := map[string]any{"arguments": concerns.JSONText(input)}
			if name, ok := block["name"]; ok {
				function["name"] = name
			}
			call := map[string]any{"type": "function", "function": function}
			if id, ok := block["id"]; ok {
				call["id"] = id
			}
			toolCalls = append(toolCalls, call)
		case "tool_result":
			content, images := toolResultContent(block["content"])
			result := map[string]any{"role": "tool", "content": content}
			if id, ok := block["tool_use_id"]; ok {
				result["tool_call_id"] = id
			}
			toolResults = append(toolResults, result)
			if len(images) > 0 {
				parts = append(parts, map[string]any{"type": "text", "text": "[Image from tool result " + jsText(block["tool_use_id"]) + "]"})
				parts = append(parts, images...)
			}
		}
	}
	if len(toolResults) > 0 {
		if len(parts) > 0 {
			return append(toolResults, map[string]any{"role": "user", "content": collapseTextParts(parts)})
		}
		return toolResults
	}
	if len(toolCalls) > 0 {
		assistant := map[string]any{"role": "assistant", "tool_calls": toolCalls}
		if len(parts) > 0 {
			assistant["content"] = collapseTextParts(parts)
		}
		return []any{assistant}
	}
	if len(parts) > 0 {
		return []any{map[string]any{"role": chatRole, "content": collapseTextParts(parts)}}
	}
	if len(blocks) == 0 {
		return []any{map[string]any{"role": chatRole, "content": ""}}
	}
	return nil
}

// imagePart converts an Anthropic image source. A url source is forwarded as is,
// because Chat takes remote URLs natively; the pin drops it
// (claude-to-openai.js:177-186), which can leave a request with no content.
func imagePart(source map[string]any) map[string]any {
	switch concerns.Text(source["type"]) {
	case "base64":
		return map[string]any{"type": "image_url", "image_url": map[string]any{"url": dataURI(source["media_type"], source["data"])}}
	case "url":
		if url := concerns.Text(source["url"]); url != "" {
			return map[string]any{"type": "image_url", "image_url": map[string]any{"url": url}}
		}
	}
	return nil
}

// toolResultContent is claude-to-openai.js:200-218. The Chat `tool` role is
// text-only, so images a tool returned come back separately for the user turn.
func toolResultContent(content any) (string, []any) {
	if text, ok := content.(string); ok {
		return text, nil
	}
	if blocks := concerns.Items(content); blocks != nil {
		var images, texts []any
		for _, raw := range blocks {
			block := concerns.Object(raw)
			switch concerns.Text(block["type"]) {
			case "image":
				if part := imagePart(concerns.Object(block["source"])); part != nil {
					images = append(images, part)
				}
			case "text":
				texts = append(texts, block["text"])
			}
		}
		var joined []string
		for _, text := range texts {
			joined = append(joined, jsText(text))
		}
		if result := strings.Join(joined, "\n"); result != "" {
			return result, images
		}
		if len(images) > 0 {
			return "", images
		}
		return concerns.JSONText(content), images
	}
	if concerns.Truthy(content) {
		return concerns.JSONText(content), nil
	}
	return "", nil
}

// systemReminderText wraps a mid-conversation system message so it ends as a
// user turn (claude-to-openai.js:134-141).
func systemReminderText(content any) string {
	var parts []string
	if blocks := concerns.Items(content); blocks != nil {
		for _, raw := range blocks {
			if block := concerns.Object(raw); concerns.Text(block["type"]) == "text" {
				parts = append(parts, concerns.Text(block["text"]))
			}
		}
	} else {
		parts = []string{concerns.Text(content)}
	}
	var texts []string
	for _, part := range parts {
		if part != "" {
			texts = append(texts, part)
		}
	}
	text := strings.Join(texts, "\n")
	if strings.TrimSpace(text) == "" {
		return ""
	}
	return "<instructions>\n" + text + "\n</instructions>"
}

// toolChoiceToChat is claude-to-openai.js:273-283.
func toolChoiceToChat(choice any) any {
	if text, ok := choice.(string); ok {
		return text
	}
	object := concerns.Object(choice)
	switch concerns.Text(object["type"]) {
	case "any":
		return "required"
	case "tool":
		return map[string]any{"type": "function", "function": map[string]any{"name": object["name"]}}
	}
	return "auto"
}

// fixMissingToolResponses is claude-to-openai.js:97-130: every Chat tool call
// needs a reply, so a call with none among the `tool` messages that immediately
// follow it gets "[No response received]".
func fixMissingToolResponses(messages *[]any) {
	list := *messages
	for i := 0; i < len(list); i++ {
		message := concerns.Object(list[i])
		calls := concerns.Items(message["tool_calls"])
		if concerns.Text(message["role"]) != "assistant" || len(calls) == 0 {
			continue
		}
		responded := map[string]bool{}
		insertAt := i + 1
		for j := i + 1; j < len(list); j++ {
			next := concerns.Object(list[j])
			if concerns.Text(next["role"]) != "tool" || !concerns.Truthy(next["tool_call_id"]) {
				break
			}
			responded[concerns.Text(next["tool_call_id"])] = true
			insertAt = j + 1
		}
		var missing []any
		for _, raw := range calls {
			id := concerns.Text(concerns.Object(raw)["id"])
			if !responded[id] {
				missing = append(missing, map[string]any{"role": "tool", "tool_call_id": id, "content": "[No response received]"})
			}
		}
		if len(missing) > 0 {
			rest := append([]any{}, list[insertAt:]...)
			list = append(append(list[:insertAt:insertAt], missing...), rest...)
			i = insertAt + len(missing) - 1
		}
	}
	*messages = list
}
