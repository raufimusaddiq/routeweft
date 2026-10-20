package wire

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf16"

	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
)

// jsTruthy mirrors JavaScript truthiness for a raw JSON value: missing, null,
// false, 0 and "" are falsy; everything else (including [] and {}) is truthy.
func jsTruthy(raw json.RawMessage) bool {
	s := strings.TrimSpace(string(raw))
	switch s {
	case "", "null", "false", `""`:
		return false
	}
	if number, err := strconv.ParseFloat(s, 64); err == nil && number == 0 {
		return false // 0, 0.0, -0, 0e5
	}
	return true
}

func isArrayRaw(raw json.RawMessage) bool {
	return strings.HasPrefix(strings.TrimSpace(string(raw)), "[")
}

// ---- GitHub Copilot Chat Completions (open-sse/executors/github.js) ----

// GitHub's newer OpenAI models require max_completion_tokens (github.js:94-96).
var githubCompletionTokensModel = regexp.MustCompile(`(?i)gpt-5|o[134]-`)

// githubChatBody applies the Copilot /chat/completions quirks to a Chat body:
// max_tokens becomes max_completion_tokens for gpt-5 and o-series models
// (github.js:98-103), reasoning_effort "none" is removed because models that do
// not know "none" answer 400 (:104-107), and message content parts the endpoint
// does not accept are serialized as text (:56-91, applied at :144-147).
//
// Divergence from the pin: when a client sent both max_tokens and
// max_completion_tokens the pin overwrites its explicit max_completion_tokens
// with max_tokens; Routeweft keeps the explicit one and drops max_tokens.
func githubChatBody(protocol string, body Body) bool {
	if protocol != openaiadapter.ChatProtocol {
		return false
	}
	changed := false
	var model string
	_ = json.Unmarshal(body["model"], &model)
	if githubCompletionTokensModel.MatchString(model) {
		if value, ok := body["max_tokens"]; ok {
			if _, has := body["max_completion_tokens"]; !has {
				body["max_completion_tokens"] = value
			}
			delete(body, "max_tokens")
			changed = true
		}
	}
	var effort string
	if json.Unmarshal(body["reasoning_effort"], &effort) == nil && effort == "none" {
		delete(body, "reasoning_effort")
		changed = true
	}
	if sanitizeCopilotMessages(body) {
		changed = true
	}
	return changed
}

// sanitizeCopilotMessages: the endpoint accepts only `text` and `image_url`
// content parts, so any other part becomes a text part carrying its text, its
// content, or its JSON, and a message left with no parts gets null content.
func sanitizeCopilotMessages(body Body) bool {
	var messages []json.RawMessage
	if json.Unmarshal(body["messages"], &messages) != nil {
		return false
	}
	changed := false
	for i, raw := range messages {
		var message map[string]json.RawMessage
		if json.Unmarshal(raw, &message) != nil || message == nil || !isArrayRaw(message["content"]) {
			continue
		}
		var parts []json.RawMessage
		if json.Unmarshal(message["content"], &parts) != nil {
			continue
		}
		clean := make([]json.RawMessage, 0, len(parts))
		touched := false
		for _, part := range parts {
			converted, keep, partChanged := copilotPart(part)
			touched = touched || partChanged
			if keep {
				clean = append(clean, converted)
			}
		}
		if !touched {
			continue
		}
		if len(clean) > 0 {
			encoded, err := json.Marshal(clean)
			if err != nil {
				return false
			}
			message["content"] = encoded
		} else {
			message["content"] = json.RawMessage("null")
		}
		encoded, err := json.Marshal(message)
		if err != nil {
			return false
		}
		messages[i], changed = encoded, true
	}
	if !changed {
		return false
	}
	return setJSON(body, "messages", messages)
}

// copilotPart returns the part to send, whether to keep it, and whether it
// differs from what the client sent.
func copilotPart(raw json.RawMessage) (json.RawMessage, bool, bool) {
	var part map[string]json.RawMessage
	if json.Unmarshal(raw, &part) != nil || part == nil {
		part = nil
	}
	var kind string
	if part != nil {
		_ = json.Unmarshal(part["type"], &kind)
	}
	switch kind {
	case "image_url":
		return raw, true, false
	case "text":
		var text string
		if json.Unmarshal(part["text"], &text) == nil && part["text"] != nil && text == "" {
			return nil, false, true // empty text parts are removed
		}
		return raw, true, false
	}
	var text string
	switch {
	case part != nil && jsTruthy(part["text"]):
		text = jsString(part["text"])
	case part != nil && jsTruthy(part["content"]):
		text = jsString(part["content"])
	default:
		text = string(raw)
	}
	if text == "" {
		return nil, false, true
	}
	converted, err := json.Marshal(map[string]string{"type": "text", "text": text})
	if err != nil {
		return raw, true, false
	}
	return converted, true, true
}

// jsString is the string if raw is one, otherwise its JSON (JSON.stringify).
func jsString(raw json.RawMessage) string {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text
	}
	return string(raw)
}

// ---- Kimchi gateway (open-sse/executors/kimchi.js) ----

// Fields the Kimchi OpenAI gateway rejects at the top level (kimchi.js:4-12).
var kimchiGatewayDrops = []string{"anthropic_version", "anthropic_beta", "client_metadata", "mcp_servers", "stop_sequences", "thinking", "top_k"}

// A real thinking block is longer than the one-character placeholder some paths
// inject; the pin strips only longer ones (kimchi.js:70-87).
const kimchiReasoningPlaceholderMax = 8

var kimchiAnthropicModel = regexp.MustCompile(`(?i)(^|[-_/])(?:claude|anthropic)([-_/]|$)`)

// kimchiBody sanitizes a Chat body for the Kimchi gateway (kimchi.js:100-120):
// a top-level Anthropic `system` is merged into the messages, gateway-rejected
// fields are dropped, Anthropic-backed models lose their reasoning fields,
// cache_control and signature artifacts are stripped from messages and tools, and
// echoed reasoning_content is removed.
//
// Divergences from the pin, all so nothing the client sent is silently lost: the
// pin's live Kimchi model metadata (services/kimchiModels.js) is not consulted, so
// Anthropic-backed is decided by the model name alone (kimchi.js:89-93); and when a
// top-level `system` cannot be merged (no messages array, a first system message
// whose content is neither a string nor an array, or a `system` that is not a
// string or an array of text) the `system` field is left in place, where the pin
// deletes its text anyway (kimchi.js:104-108); every other sanitation still runs.
func kimchiBody(protocol string, body Body) bool {
	if protocol != openaiadapter.ChatProtocol {
		return false
	}
	changed := false
	if raw, present := body["system"]; present {
		// A `system` that cannot be carried stays where it is; that failure is
		// local. The field drops, reasoning strip and artifact stripping below
		// are independent of it and always run.
		if _, ok := mergeKimchiSystem(body, raw); ok {
			delete(body, "system")
			changed = true
		}
	}
	for _, key := range kimchiGatewayDrops {
		if _, ok := body[key]; ok {
			delete(body, key)
			changed = true
		}
	}
	var model string
	_ = json.Unmarshal(body["model"], &model)
	if kimchiAnthropicModel.MatchString(model) {
		for _, key := range []string{"reasoning_effort", "reasoning", "thinking"} {
			if _, ok := body[key]; ok {
				delete(body, key)
				changed = true
			}
		}
	}
	if stripKimchiMessages(body) {
		changed = true
	}
	if stripToolCacheControl(body) {
		changed = true
	}
	return changed
}

// mergeKimchiSystem folds a top-level Anthropic `system` into messages. ok is
// false when it cannot be carried, so the caller leaves the body alone.
func mergeKimchiSystem(body Body, raw json.RawMessage) (merged, ok bool) {
	if !jsTruthy(raw) {
		return false, true
	}
	text, carried := systemTopLevelText(raw)
	if !carried {
		return false, false
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return false, true
	}
	var messages []json.RawMessage
	if json.Unmarshal(body["messages"], &messages) != nil || !isArrayRaw(body["messages"]) {
		return false, false
	}
	for i, rawMessage := range messages {
		var message map[string]json.RawMessage
		var role string
		if json.Unmarshal(rawMessage, &message) != nil || message == nil {
			continue
		}
		_ = json.Unmarshal(message["role"], &role)
		if role != "system" {
			continue
		}
		var content string
		switch {
		case json.Unmarshal(message["content"], &content) == nil && message["content"] != nil && !isNullRaw(message["content"]):
			message["content"], _ = json.Marshal(text + "\n\n" + content)
		case isArrayRaw(message["content"]):
			var parts []json.RawMessage
			if json.Unmarshal(message["content"], &parts) != nil {
				return false, false
			}
			lead, _ := json.Marshal(map[string]string{"type": "text", "text": text})
			message["content"], _ = json.Marshal(append([]json.RawMessage{lead}, parts...))
		default:
			return false, false
		}
		encoded, err := json.Marshal(message)
		if err != nil {
			return false, false
		}
		messages[i] = encoded
		return true, setJSON(body, "messages", messages)
	}
	lead, err := json.Marshal(map[string]string{"role": "system", "content": text})
	if err != nil {
		return false, false
	}
	return true, setJSON(body, "messages", append([]json.RawMessage{lead}, messages...))
}

func isNullRaw(raw json.RawMessage) bool { return strings.TrimSpace(string(raw)) == "null" }

// systemTopLevelText is the text of an Anthropic `system`: a string, or the
// strings and text fields of an array's parts joined by newlines
// (kimchi.js:14-27). carried is false for a shape that has text this cannot read.
func systemTopLevelText(raw json.RawMessage) (string, bool) {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text, true
	}
	var parts []json.RawMessage
	if json.Unmarshal(raw, &parts) != nil {
		return "", false
	}
	var texts []string
	for _, part := range parts {
		var s string
		if json.Unmarshal(part, &s) == nil {
			if s != "" {
				texts = append(texts, s)
			}
			continue
		}
		var object struct {
			Text *string `json:"text"`
		}
		if json.Unmarshal(part, &object) != nil || object.Text == nil {
			return "", false
		}
		if *object.Text != "" {
			texts = append(texts, *object.Text)
		}
	}
	return strings.Join(texts, "\n"), true
}

// stripKimchiMessages removes cache_control from messages and from their content
// parts, signature from parts, and echoed reasoning_content (kimchi.js:47-87).
func stripKimchiMessages(body Body) bool {
	var messages []json.RawMessage
	if json.Unmarshal(body["messages"], &messages) != nil {
		return false
	}
	changed := false
	for i, raw := range messages {
		var message map[string]json.RawMessage
		if json.Unmarshal(raw, &message) != nil || message == nil {
			continue
		}
		touched := false
		if _, ok := message["cache_control"]; ok {
			delete(message, "cache_control")
			touched = true
		}
		var role, reasoning string
		_ = json.Unmarshal(message["role"], &role)
		if role == "assistant" && json.Unmarshal(message["reasoning_content"], &reasoning) == nil && len(utf16.Encode([]rune(reasoning))) > kimchiReasoningPlaceholderMax {
			delete(message, "reasoning_content")
			touched = true
		}
		if isArrayRaw(message["content"]) {
			var parts []json.RawMessage
			if json.Unmarshal(message["content"], &parts) == nil {
				partsChanged := false
				for j, rawPart := range parts {
					var part map[string]json.RawMessage
					if json.Unmarshal(rawPart, &part) != nil || part == nil {
						continue
					}
					_, hasCache := part["cache_control"]
					_, hasSignature := part["signature"]
					if !hasCache && !hasSignature {
						continue
					}
					delete(part, "cache_control")
					delete(part, "signature")
					encoded, err := json.Marshal(part)
					if err != nil {
						return false
					}
					parts[j], partsChanged = encoded, true
				}
				if partsChanged {
					message["content"], _ = json.Marshal(parts)
					touched = true
				}
			}
		}
		if !touched {
			continue
		}
		encoded, err := json.Marshal(message)
		if err != nil {
			return false
		}
		messages[i], changed = encoded, true
	}
	if !changed {
		return false
	}
	return setJSON(body, "messages", messages)
}

// stripToolCacheControl removes cache_control from each tool (kimchi.js:61-68).
func stripToolCacheControl(body Body) bool {
	var tools []json.RawMessage
	if json.Unmarshal(body["tools"], &tools) != nil {
		return false
	}
	changed := false
	for i, raw := range tools {
		var tool map[string]json.RawMessage
		if json.Unmarshal(raw, &tool) != nil || tool == nil {
			continue
		}
		if _, ok := tool["cache_control"]; !ok {
			continue
		}
		delete(tool, "cache_control")
		encoded, err := json.Marshal(tool)
		if err != nil {
			return false
		}
		tools[i], changed = encoded, true
	}
	if !changed {
		return false
	}
	return setJSON(body, "tools", tools)
}
