package wire

import (
	"bytes"
	"encoding/json"
	"strings"

	anthropicadapter "github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// Body is a request body parsed one level deep. Values stay raw, so only a field
// a quirk touches is re-encoded and everything else survives as received.
type Body map[string]json.RawMessage

// Finalize is a provider's last rewrite of the outbound body, given the wire
// protocol it is about to be sent on. It reports whether it changed the body.
type Finalize func(protocol string, body Body) bool

// FinalizeBody applies the provider's registry quirks to the final outbound body,
// after every transform and prompt-cache anchoring (AGENTS §6 step 8). It is
// fail-open: a body that is not a JSON object, or a quirk that cannot be
// applied, leaves the request exactly as it was. A body no quirk changes is
// returned untouched, byte for byte.
func FinalizeBody(provider routing.ProviderRef, body []byte) []byte {
	finalize := For(provider.ProviderID).Finalize
	if finalize == nil {
		return body
	}
	var parsed Body
	if err := json.Unmarshal(body, &parsed); err != nil || parsed == nil {
		return body
	}
	if !finalize(provider.Protocol, parsed) {
		return body
	}
	out, err := json.Marshal(parsed)
	if err != nil {
		return body
	}
	return out
}

func (b Body) delete(key string) bool {
	if _, ok := b[key]; !ok {
		return false
	}
	delete(b, key)
	return true
}

// dropClientMetadata: registry quirks.dropClientMetadata (cerebras, mistral).
// These gateways reject Anthropic's client_metadata field
// (open-sse/executors/default.js:103).
func dropClientMetadata(_ string, body Body) bool { return body.delete("client_metadata") }

// dropOutputConfig: quirks.dropOutputConfig (minimax, minimax-cn). The
// Claude-compatible endpoint rejects output_config with 400
// (open-sse/translator/formats/claude.js:487).
func dropOutputConfig(protocol string, body Body) bool {
	return protocol == anthropicadapter.MessagesProtocol && body.delete("output_config")
}

// requireClaudeToolType: quirks.requireClaudeToolType (minimax, minimax-cn). A
// Messages tool with no `type` gets "custom"; the gateway rejects the legacy
// typeless shape (open-sse/translator/concerns/toolCall.js:165-181, applied at
// open-sse/handlers/chatCore.js:291). Scoped to the provider: other endpoints
// reject "custom".
func requireClaudeToolType(protocol string, body Body) bool {
	if protocol != anthropicadapter.MessagesProtocol {
		return false
	}
	var tools []json.RawMessage
	if json.Unmarshal(body["tools"], &tools) != nil {
		return false
	}
	changed := false
	for i, raw := range tools {
		var tool map[string]json.RawMessage
		if json.Unmarshal(raw, &tool) != nil || tool == nil || truthy(tool["type"]) {
			continue
		}
		tool["type"] = json.RawMessage(`"custom"`)
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

// supportedToolTypes: quirks.claudeSupportedToolTypes (deepseek). DeepSeek's
// Anthropic endpoint accepts only the built-in web_search_* tools and answers
// 400 "unknown variant `custom`" to any other typed tool, so a Messages request
// keeps function-style and typeless tools plus the listed types and drops the
// rest (open-sse/translator/formats/claude.js:643-668). Emptied tools take
// tool_choice with them (:681-684).
func supportedToolTypes(allowed ...string) Finalize {
	return func(protocol string, body Body) bool {
		if protocol != anthropicadapter.MessagesProtocol {
			return false
		}
		var tools []json.RawMessage
		if json.Unmarshal(body["tools"], &tools) != nil {
			return false
		}
		kept := make([]json.RawMessage, 0, len(tools))
		for _, raw := range tools {
			var tool map[string]json.RawMessage
			if json.Unmarshal(raw, &tool) != nil || tool == nil {
				kept = append(kept, raw)
				continue
			}
			var kind string
			_ = json.Unmarshal(tool["type"], &kind)
			if kind != "" && kind != "function" && !contains(allowed, kind) {
				continue
			}
			kept = append(kept, raw)
		}
		if len(kept) == len(tools) {
			return false
		}
		if len(kept) == 0 {
			body.delete("tools")
			body.delete("tool_choice")
			return true
		}
		return setJSON(body, "tools", kept)
	}
}

// foldResponsesSystem: quirks.responsesSystemRoleUnsupported (kenari). Kenari's
// Responses endpoint rejects role "system": system messages and `instructions`
// are folded into the first user turn (open-sse/executors/default.js:21-42,97).
//
// Divergence from the pin: when there is no user turn to fold into, the pin drops
// the system messages and keeps the rest, silently losing their text. Here the
// body is left as received so the gateway's own error reaches the client instead
// of a request that quietly lost its instructions.
func foldResponsesSystem(protocol string, body Body) bool {
	if protocol != openaiadapter.ResponsesProtocol {
		return false
	}
	var input []json.RawMessage
	if json.Unmarshal(body["input"], &input) != nil {
		return false
	}
	var texts []string
	var rest []json.RawMessage
	firstUser := -1
	for _, raw := range input {
		var item struct {
			Type    string          `json:"type"`
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		if json.Unmarshal(raw, &item) == nil && item.Type == "message" && item.Role == "system" {
			if text := systemItemText(item.Content); text != "" {
				texts = append(texts, text)
			}
			continue
		}
		if firstUser < 0 && item.Type == "message" && item.Role == "user" {
			firstUser = len(rest)
		}
		rest = append(rest, raw)
	}
	var instructions string
	_ = json.Unmarshal(body["instructions"], &instructions)
	if trimmed := strings.TrimSpace(instructions); trimmed != "" {
		texts = append([]string{trimmed}, texts...)
	}
	switch {
	case len(rest) == len(input) && len(texts) == 0:
		return false
	case len(texts) > 0 && firstUser < 0:
		return false
	}
	if len(texts) > 0 {
		prefix := strings.Join(texts, "\n\n")
		var user map[string]json.RawMessage
		if json.Unmarshal(rest[firstUser], &user) != nil {
			return false
		}
		var parts []json.RawMessage
		if json.Unmarshal(user["content"], &parts) == nil && bytes.HasPrefix(bytes.TrimSpace(user["content"]), []byte("[")) {
			lead, _ := json.Marshal(map[string]string{"type": "input_text", "text": prefix + "\n\n"})
			user["content"], _ = json.Marshal(append([]json.RawMessage{lead}, parts...))
		} else {
			var current string
			_ = json.Unmarshal(user["content"], &current)
			user["content"], _ = json.Marshal(prefix + "\n\n" + current)
		}
		encoded, err := json.Marshal(user)
		if err != nil {
			return false
		}
		rest[firstUser] = encoded
		body.delete("instructions")
	}
	return setJSON(body, "input", rest)
}

// systemItemText is the text of a system message: a string, or the text of its
// input_text/text parts joined by newlines (default.js:26-29).
func systemItemText(content json.RawMessage) string {
	var text string
	if json.Unmarshal(content, &text) == nil {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(content, &parts) != nil {
		return ""
	}
	var texts []string
	for _, part := range parts {
		if (part.Type == "input_text" || part.Type == "text") && part.Text != "" {
			texts = append(texts, part.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func truthy(raw json.RawMessage) bool {
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text != ""
	}
	return len(raw) > 0 && string(raw) != "null" && string(raw) != "false" && string(raw) != "0"
}

func contains(values []string, want string) bool {
	for _, v := range values {
		if v == want {
			return true
		}
	}
	return false
}

func setJSON(body Body, key string, value any) bool {
	encoded, err := json.Marshal(value)
	if err != nil {
		return false
	}
	body[key] = encoded
	return true
}

func chain(finalizers ...Finalize) Finalize {
	return func(protocol string, body Body) bool {
		changed := false
		for _, finalize := range finalizers {
			if finalize(protocol, body) {
				changed = true
			}
		}
		return changed
	}
}
