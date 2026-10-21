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

// AdjustMaxTokens applies output/tool/thinking bounds; ceiling <= 0 uses the default.
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
func MessagesToChat(body map[string]any, model string, stream bool) (map[string]any, error) {
	for _, field := range []string{"mcp_servers", "container", "context_management"} {
		// These change what the server does with the request; a Chat upstream
		// cannot honour them, so serving the rest would be a confident wrong answer.
		if concerns.Truthy(body[field]) {
			return nil, unsupported("request field %q has no Chat equivalent", field)
		}
	}
	result := map[string]any{"model": model, "messages": []any{}, "stream": stream}
	if concerns.Truthy(body["max_tokens"]) {
		result["max_tokens"] = AdjustMaxTokens(body, 0)
	}
	if temperature, ok := body["temperature"]; ok {
		result["temperature"] = temperature
	}
	// Carried although the pin drops them (PARITY V12): sampling and identity
	// controls a Chat upstream understands.
	if topP, ok := body["top_p"]; ok {
		result["top_p"] = topP
	}
	if stop := concerns.Items(body["stop_sequences"]); len(stop) > 0 {
		result["stop"] = stop
	}
	if user := concerns.Text(concerns.Object(body["metadata"])["user_id"]); user != "" {
		result["user"] = user
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
		converted, err := convertMessage(concerns.Object(raw))
		if err != nil {
			return nil, err
		}
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
	// A tool_choice only means something next to tools; Chat takes neither an
	// empty tools array nor a choice with nothing to choose from.
	if concerns.Truthy(body["tool_choice"]) && len(concerns.Items(result["tools"])) > 0 {
		choice, err := toolChoiceToChat(body["tool_choice"])
		if err != nil {
			return nil, err
		}
		result["tool_choice"] = choice
		if concerns.Truthy(concerns.Object(body["tool_choice"])["disable_parallel_tool_use"]) {
			result["parallel_tool_calls"] = false
		}
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
	return result, nil
}

// unsupported is a request the leg refuses rather than serve with content or
// meaning silently removed. The dispatcher treats it as a translation failure
// and tries the next candidate.
func unsupported(format string, args ...any) error {
	return fmt.Errorf("unsupported: "+format, args...)
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
	return concerns.JSONText(value)
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
// messages for one Messages message. A block the leg cannot carry is an error,
// never a silent drop; the few blocks that are deliberately not carried are
// named below and recorded in PARITY 6.3.
func convertMessage(message map[string]any) ([]any, error) {
	if message == nil {
		return nil, nil
	}
	if content := message["content"]; content != nil {
		if object := concerns.Object(content); object != nil {
			message["content"] = []any{object}
		}
	}
	role := concerns.Text(message["role"])
	if role == "system" {
		if text := systemReminderText(message["content"]); text != "" {
			return []any{map[string]any{"role": "user", "content": text}}, nil
		}
		return nil, nil
	}
	chatRole := "assistant"
	switch role {
	case "user", "tool":
		chatRole = "user"
	case "assistant":
	default:
		// The pin treats every other role as the assistant, putting words in its
		// mouth that nobody said.
		return nil, unsupported("message role %q", role)
	}
	if text, ok := message["content"].(string); ok {
		return []any{map[string]any{"role": chatRole, "content": text}}, nil
	}
	blocks := concerns.Items(message["content"])
	if blocks == nil {
		return nil, nil
	}
	var parts, toolCalls, toolResults []any
	for _, raw := range blocks {
		block := concerns.Object(raw)
		switch kind := concerns.Text(block["type"]); kind {
		case "text":
			part := map[string]any{"type": "text"}
			if text, ok := block["text"]; ok {
				part["text"] = text
			}
			parts = append(parts, part)
		case "image":
			part := imagePart(concerns.Object(block["source"]))
			if part == nil {
				return nil, unsupported("image source %q", concerns.Text(concerns.Object(block["source"])["type"]))
			}
			parts = append(parts, part)
		case "document":
			part, err := documentPart(block)
			if err != nil {
				return nil, err
			}
			parts = append(parts, part)
		case "tool_use":
			function := map[string]any{"arguments": concerns.ToolArguments(block["input"])}
			if name, ok := block["name"]; ok {
				function["name"] = name
			}
			call := map[string]any{"type": "function", "function": function}
			if id, ok := block["id"]; ok {
				call["id"] = id
			}
			toolCalls = append(toolCalls, call)
		case "tool_result":
			content, images, err := toolResultContent(block["content"])
			if err != nil {
				return nil, err
			}
			result := map[string]any{"role": "tool", "content": content}
			if id, ok := block["tool_use_id"]; ok {
				result["tool_call_id"] = id
			}
			toolResults = append(toolResults, result)
			if len(images) > 0 {
				parts = append(parts, map[string]any{"type": "text", "text": "[Image from tool result " + jsText(block["tool_use_id"]) + "]"})
				parts = append(parts, images...)
			}
		case "thinking", "redacted_thinking":
			// Signed by Anthropic and meaningless to another provider; the pin drops
			// them too (claude-to-openai.js has no case).
		case "server_tool_use", "web_search_tool_result":
			// Server-executed intermediate blocks of an earlier turn. The model's
			// own text already reflects them and no Chat tool can replay them.
		default:
			return nil, unsupported("content block type %q", kind)
		}
	}
	if len(toolResults) > 0 {
		if len(toolCalls) > 0 {
			return nil, unsupported("a message that holds both tool_use and tool_result")
		}
		if len(parts) > 0 {
			return append(toolResults, map[string]any{"role": "user", "content": collapseTextParts(parts)}), nil
		}
		return toolResults, nil
	}
	if len(toolCalls) > 0 {
		assistant := map[string]any{"role": "assistant", "tool_calls": toolCalls}
		if len(parts) > 0 {
			assistant["content"] = collapseTextParts(parts)
		}
		return []any{assistant}, nil
	}
	if len(parts) > 0 {
		return []any{map[string]any{"role": chatRole, "content": collapseTextParts(parts)}}, nil
	}
	if len(blocks) == 0 {
		return []any{map[string]any{"role": chatRole, "content": ""}}, nil
	}
	return nil, nil
}

// documentPart maps an Anthropic document to a Chat file part, so a PDF reaches
// a model that can read one instead of vanishing (the pin has no case). A source
// that cannot be carried is an error: answering about a document the model never
// saw is worse than failing.
func documentPart(block map[string]any) (map[string]any, error) {
	source := concerns.Object(block["source"])
	switch concerns.Text(source["type"]) {
	case "base64":
		file := map[string]any{"file_data": dataURI(source["media_type"], source["data"])}
		if title := concerns.Text(block["title"]); title != "" {
			file["filename"] = title
		}
		return map[string]any{"type": "file", "file": file}, nil
	case "text":
		return map[string]any{"type": "text", "text": concerns.Text(source["data"])}, nil
	}
	return nil, unsupported("document source %q", concerns.Text(source["type"]))
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
// is_error has no Chat equivalent and is not carried; the error text itself is.
func toolResultContent(content any) (string, []any, error) {
	if text, ok := content.(string); ok {
		return text, nil, nil
	}
	if blocks := concerns.Items(content); blocks != nil {
		var images []any
		var texts []string
		for _, raw := range blocks {
			block := concerns.Object(raw)
			switch kind := concerns.Text(block["type"]); kind {
			case "image":
				part := imagePart(concerns.Object(block["source"]))
				if part == nil {
					return "", nil, unsupported("tool_result image source %q", concerns.Text(concerns.Object(block["source"])["type"]))
				}
				images = append(images, part)
			case "text":
				texts = append(texts, jsText(block["text"]))
			default:
				return "", nil, unsupported("tool_result content block type %q", kind)
			}
		}
		if result := strings.Join(texts, "\n"); result != "" {
			return result, images, nil
		}
		if len(images) > 0 {
			return "", images, nil
		}
		return concerns.JSONText(content), images, nil
	}
	if concerns.Truthy(content) {
		return concerns.JSONText(content), nil, nil
	}
	return "", nil, nil
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

// toolChoiceToChat is claude-to-openai.js:273-283, minus its permissive
// default: the pin turns every type it does not know, "none" included, into
// "auto", re-enabling tools the client forbade. A choice with no answer is
// refused instead (PARITY V19).
func toolChoiceToChat(choice any) (any, error) {
	if text, ok := choice.(string); ok {
		switch text {
		case "auto", "none", "required":
			return text, nil
		}
		return nil, unsupported("tool_choice %q", text)
	}
	object := concerns.Object(choice)
	switch kind := concerns.Text(object["type"]); kind {
	case "auto":
		return "auto", nil
	case "none":
		return "none", nil
	case "any":
		return "required", nil
	case "tool":
		if concerns.Text(object["name"]) == "" {
			return nil, unsupported("tool_choice of type tool with no name")
		}
		return map[string]any{"type": "function", "function": map[string]any{"name": object["name"]}}, nil
	default:
		return nil, unsupported("tool_choice type %q", kind)
	}
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
