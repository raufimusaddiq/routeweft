package ingress

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// The pure text and body helpers of Fusion, each a port of open-sse/services/combo.js
// (LiteRouter main fc8dd1fc), so the panel and judge requests carry what the oracle
// carries. Bodies are decoded with UseNumber so a number is written back as it came.

const (
	toolCallPrefix   = "[Called tools: "
	toolResultPrefix = "[Tool result: "
)

func decodeObject(raw []byte) (map[string]any, bool) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var object map[string]any
	if decoder.Decode(&object) != nil || object == nil {
		return nil, false
	}
	return object, true
}

// jsTruthy is JavaScript truthiness for a decoded JSON value.
func jsTruthy(value any) bool {
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

// jsToString is String(value) for a decoded JSON value.
func jsToString(value any) string {
	switch typed := value.(type) {
	case nil:
		return "null"
	case string:
		return typed
	case bool:
		return strconv.FormatBool(typed)
	case json.Number:
		return typed.String()
	case []any:
		parts := make([]string, 0, len(typed))
		for _, item := range typed {
			if item == nil {
				parts = append(parts, "")
			} else {
				parts = append(parts, jsToString(item))
			}
		}
		return strings.Join(parts, ",")
	}
	return "[object Object]"
}

// extractTextContent is translator/formats/gemini.js:113-119: a string as it is,
// an array's text blocks joined, anything else empty.
func extractTextContent(content any) string {
	switch typed := content.(type) {
	case string:
		return typed
	case []any:
		var text strings.Builder
		for _, item := range typed {
			block, _ := item.(map[string]any)
			if block["type"] != "text" {
				continue
			}
			if value, ok := block["text"]; ok && value != nil {
				text.WriteString(jsToString(value))
			}
		}
		return text.String()
	}
	return ""
}

func objectOf(value any) map[string]any {
	object, _ := value.(map[string]any)
	return object
}

// flattenToolHistory is combo.js:21-59: tool turns become prose, so a panel model
// keeps the context but cannot loop on tools.
func flattenToolHistory(messages []any) []any {
	out := make([]any, 0, len(messages))
	for _, item := range messages {
		message := objectOf(item)
		if message == nil {
			if jsTruthy(item) {
				out = append(out, item)
			}
			continue
		}
		role, _ := message["role"].(string)
		if role == "tool" || role == "function" {
			text := extractTextContent(message["content"])
			if text == "" {
				text = jsToStringOrEmpty(message["content"])
			}
			out = append(out, map[string]any{"role": "assistant", "content": toolResultPrefix + text + "]"})
			continue
		}
		if calls, ok := message["tool_calls"].([]any); ok && role == "assistant" {
			names := make([]string, 0, len(calls))
			for _, call := range calls {
				names = append(names, toolCallName(call))
			}
			base := extractTextContent(message["content"])
			if base == "" {
				if text, ok := message["content"].(string); ok {
					base = text
				}
			}
			rest := make(map[string]any, len(message))
			for key, value := range message {
				if key != "tool_calls" {
					rest[key] = value
				}
			}
			separator := ""
			if base != "" {
				separator = "\n"
			}
			rest["content"] = base + separator + toolCallPrefix + strings.Join(names, ", ") + "]"
			out = append(out, rest)
			continue
		}
		if blocks, ok := message["content"].([]any); ok {
			if flat, changed := flattenToolBlocks(message, blocks); changed {
				out = append(out, flat)
				continue
			}
		}
		out = append(out, item)
	}
	return out
}

func jsToStringOrEmpty(value any) string {
	if value == nil {
		return ""
	}
	return jsToString(value)
}

// toolCallName is c?.function?.name || c?.name || "tool".
func toolCallName(call any) string {
	object := objectOf(call)
	if name, _ := objectOf(object["function"])["name"].(string); name != "" {
		return name
	}
	if name, _ := object["name"].(string); name != "" {
		return name
	}
	return "tool"
}

// flattenToolBlocks handles content arrays holding tool_use / tool_result blocks
// (combo.js:34-56); changed is false when there are none.
func flattenToolBlocks(message map[string]any, blocks []any) (map[string]any, bool) {
	var hasUse, hasResult bool
	for _, item := range blocks {
		switch objectOf(item)["type"] {
		case "tool_use":
			hasUse = true
		case "tool_result":
			hasResult = true
		}
	}
	if !hasUse && !hasResult {
		return nil, false
	}
	var texts, names, results []string
	for _, item := range blocks {
		block := objectOf(item)
		switch block["type"] {
		case "text":
			if text, _ := block["text"].(string); text != "" {
				texts = append(texts, text)
			}
		case "tool_use":
			name, _ := block["name"].(string)
			if name == "" {
				name = "tool"
			}
			names = append(names, name)
		case "tool_result":
			text := extractTextContent(block["content"])
			if text == "" {
				text = jsToStringOrEmpty(block["content"])
			}
			results = append(results, text)
		}
	}
	content := strings.Join(texts, "\n")
	if len(names) > 0 {
		if content != "" {
			content += "\n"
		}
		content += toolCallPrefix + strings.Join(names, ", ") + "]"
	}
	if len(results) > 0 {
		if content != "" {
			content += "\n"
		}
		content += toolResultPrefix + strings.Join(results, "\n") + "]"
	}
	flat := make(map[string]any, len(message))
	for key, value := range message {
		flat[key] = value
	}
	flat["content"] = content
	return flat, true
}

// extractPanelText is combo.js:390-422: the assistant text of a non-stream answer
// in any of the four shapes the oracle reads, plus Ollama's message.content, which
// the oracle never sees because its Ollama route converts after the Combo ran.
func extractPanelText(answer map[string]any) string {
	if choices, ok := answer["choices"].([]any); ok && len(choices) > 0 && jsTruthy(choices[0]) {
		choice := objectOf(choices[0])
		message := objectOf(choice["message"])
		if choice["message"] == nil {
			message = objectOf(choice["delta"])
		}
		if text := extractTextContent(message["content"]); strings.TrimSpace(text) != "" {
			return text
		}
		if text, ok := choice["text"].(string); ok && strings.TrimSpace(text) != "" {
			return text
		}
	}
	if text := extractTextContent(answer["content"]); strings.TrimSpace(text) != "" {
		return text
	}
	if candidates, ok := answer["candidates"].([]any); ok && len(candidates) > 0 {
		if parts, ok := objectOf(objectOf(objectOf(candidates[0])["content"]))["parts"].([]any); ok {
			var text strings.Builder
			for _, part := range parts {
				if value, ok := objectOf(part)["text"]; ok && jsTruthy(value) {
					text.WriteString(jsToString(value))
				}
			}
			if strings.TrimSpace(text.String()) != "" {
				return text.String()
			}
		}
	}
	if output, ok := answer["output"].([]any); ok {
		var text strings.Builder
		for _, item := range output {
			content, ok := objectOf(item)["content"].([]any)
			if !ok {
				continue
			}
			for _, block := range content {
				if value, ok := objectOf(block)["text"]; ok && jsTruthy(value) {
					text.WriteString(jsToString(value))
				}
			}
		}
		if strings.TrimSpace(text.String()) != "" {
			return text.String()
		}
	}
	if text, ok := objectOf(answer["message"])["content"].(string); ok && strings.TrimSpace(text) != "" {
		return text
	}
	return ""
}

// fusionAnswer is one panel answer that made it to the judge.
type fusionAnswer struct {
	model string
	text  string
}

// buildJudgePrompt is combo.js:451-469. The judge analyses and then writes one
// answer; the sources are anonymous so it weighs substance, not a model's name.
func buildJudgePrompt(answers []fusionAnswer) string {
	var panel strings.Builder
	for index, answer := range answers {
		if index > 0 {
			panel.WriteString("\n\n")
		}
		fmt.Fprintf(&panel, "[Source %d]\n%s", index+1, answer.text)
	}
	return strings.Join([]string{
		fmt.Sprintf("You are the JUDGE in a model-fusion panel. %d expert models independently answered the user's most recent request. Their responses are below, anonymized by source.", len(answers)),
		"",
		"Do NOT mention that multiple models were used, and do NOT refer to the sources. Produce ONE authoritative final answer addressed directly to the user.",
		"",
		"First, internally analyze the panel along these dimensions: consensus (points most sources agree on — treat as higher-confidence), contradictions (where they disagree — resolve with your own judgment), partial coverage, unique insights only one source surfaced, and blind spots every source missed. Then write the best possible final answer grounded in that analysis — more complete and correct than any single response, with no filler.",
		"",
		"=== PANEL RESPONSES ===",
		panel.String(),
		"=== END PANEL RESPONSES ===",
		"",
		"Now write the final answer to the user's original request.",
	}, "\n")
}

// panelBody is combo.js:566-578: the request every panel model gets. Tools, the
// tool choice and stream options go (a provider such as DeepSeek refuses
// stream_options without streaming), the call is not streamed, and prior tool
// turns become prose. native names the keys that carry the tools in a protocol
// whose body has no stream field (Gemini streams by URL).
func panelBody(body []byte, streamField bool) ([]byte, bool) {
	root, ok := decodeObject(body)
	if !ok {
		return nil, false
	}
	for _, key := range []string{"tools", "tool_choice", "stream_options", "toolConfig"} {
		delete(root, key)
	}
	if streamField {
		root["stream"] = false
	}
	if messages, ok := root["messages"].([]any); ok {
		root["messages"] = flattenToolHistory(messages)
	} else if input, ok := root["input"].([]any); ok {
		root["input"] = flattenToolHistory(input)
	}
	encoded, err := json.Marshal(root)
	return encoded, err == nil
}

// appendUserTurn is combo.js:428-440: the judge sees the whole conversation and
// then a user turn carrying the panel, in whichever array the request format uses.
func appendUserTurn(body []byte, text string) ([]byte, bool) {
	root, ok := decodeObject(body)
	if !ok {
		return nil, false
	}
	switch {
	case isArray(root["messages"]):
		root["messages"] = append(root["messages"].([]any), map[string]any{"role": "user", "content": text})
	case isArray(root["input"]):
		root["input"] = append(root["input"].([]any), map[string]any{"role": "user", "content": text})
	case isArray(root["contents"]):
		root["contents"] = append(root["contents"].([]any), map[string]any{"role": "user", "parts": []any{map[string]any{"text": text}}})
	default:
		root["messages"] = []any{map[string]any{"role": "user", "content": text}}
	}
	encoded, err := json.Marshal(root)
	return encoded, err == nil
}

func isArray(value any) bool {
	_, ok := value.([]any)
	return ok
}
