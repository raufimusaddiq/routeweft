package wire

import (
	"bytes"
	"encoding/json"
	"strings"

	anthropicadapter "github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// Body is a request body parsed one level deep. Values stay raw, so every field
// keeps its value as received, including ones this package does not know. When a
// quirk fires the body is re-marshalled, so its top-level keys come out in
// alphabetical order and insignificant whitespace is gone; a body no quirk
// changes is never re-encoded.
type Body map[string]json.RawMessage

// Finalize is a provider's last rewrite of the outbound body, given the wire
// protocol it is about to be sent on. It reports whether it changed the body.
type Finalize func(protocol string, body Body) bool

// FinalizeBody applies the provider's registry quirks to the final outbound body.
// It is provider normalization, AGENTS §6 step 8: the caller must run it on the
// body the token savers and prompt-cache anchoring (steps 2-7) produced, as the
// last rewrite before dispatch (internal/ingress newUpstreamRequest). It is
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
// (open-sse/executors/default.js:103). The pin removes it from any body, so this
// quirk is deliberately not scoped to a wire.
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
		if json.Unmarshal(raw, &tool) != nil || tool == nil || hasType(tool["type"]) {
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
// rest (open-sse/translator/formats/claude.js:643-668).
//
// tools and tool_choice are a pair; a quirk that removes one half leaves a
// request the gateway rejects. Emptied tools take tool_choice with them (pin
// :681-684). Beyond the pin, a tool_choice that names a tool this filter removed
// is dropped too, even when other tools survive; "auto", "any" and "none" name
// nothing and stay, and so does a tool_choice naming a surviving tool.
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
		keptNames := map[string]bool{}
		for _, raw := range tools {
			var tool map[string]json.RawMessage
			if json.Unmarshal(raw, &tool) != nil || tool == nil {
				kept = append(kept, raw)
				continue
			}
			var kind, name string
			_ = json.Unmarshal(tool["type"], &kind)
			_ = json.Unmarshal(tool["name"], &name)
			if kind != "" && kind != "function" && !contains(allowed, kind) {
				continue
			}
			kept = append(kept, raw)
			keptNames[name] = true
		}
		if len(kept) == len(tools) {
			return false
		}
		if len(kept) == 0 {
			body.delete("tools")
			body.delete("tool_choice")
			return true
		}
		var choice struct {
			Type string `json:"type"`
			Name string `json:"name"`
		}
		if json.Unmarshal(body["tool_choice"], &choice) == nil && choice.Type == "tool" && !keptNames[choice.Name] {
			body.delete("tool_choice")
		}
		return setJSON(body, "tools", kept)
	}
}

// foldResponsesSystem: quirks.responsesSystemRoleUnsupported (kenari). Kenari's
// Responses endpoint rejects role "system": system messages and `instructions`
// are folded into the first user turn (open-sse/executors/default.js:21-42,97).
//
// Every legal `input` shape either folds or is left byte-identical:
//   - an array: system items are removed and their text, with `instructions`,
//     goes ahead of the first user message;
//   - a plain string (openai/responses.go accepts it): it is one user turn, so with
//     `instructions` it becomes a one-message array carrying the folded text;
//   - an empty array, no system text, no `instructions`: unchanged;
//   - system text or `instructions` but no user turn to fold into: unchanged;
//   - anything else (an object, a number): unchanged, the gateway decides.
//
// Divergences from the pin, all so no instruction is silently lost or ignored:
// the pin leaves a string `input` alone, so `instructions` still reached kenari;
// the pin drops system messages when there is no user turn, losing their text;
// and an item with no `type` but role "system" (a legal EasyInputMessage) is
// folded, where the pin looks only at type "message". The body is never emitted
// with an empty `input`, and it is left as received whenever the fold would have
// to discard something it cannot carry: non-string `instructions`, a system
// message whose content is an object, a number or has a non-text part, or a first
// user message whose content is an object or a number.
func foldResponsesSystem(protocol string, body Body) bool {
	if protocol != openaiadapter.ResponsesProtocol {
		return false
	}
	var instructions string
	if raw := body["instructions"]; len(raw) > 0 && string(raw) != "null" && json.Unmarshal(raw, &instructions) != nil {
		return false // not a string: it cannot be folded, and it must not be deleted
	}
	instructions = strings.TrimSpace(instructions)

	var input []json.RawMessage
	if err := json.Unmarshal(body["input"], &input); err != nil {
		var text string
		if json.Unmarshal(body["input"], &text) != nil || instructions == "" {
			return false
		}
		turn, err := json.Marshal(map[string]string{"type": "message", "role": "user", "content": text})
		if err != nil {
			return false
		}
		input = []json.RawMessage{turn}
	}

	var texts []string
	if instructions != "" {
		texts = append(texts, instructions)
	}
	rest := make([]json.RawMessage, 0, len(input))
	firstUser := -1
	for _, raw := range input {
		var item struct {
			Type    string          `json:"type"`
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		}
		isMessage := json.Unmarshal(raw, &item) == nil && (item.Type == "message" || item.Type == "")
		if isMessage && item.Role == "system" {
			text, ok := systemItemText(item.Content)
			if !ok {
				return false // content this fold cannot carry as text: nothing is dropped
			}
			if text != "" {
				texts = append(texts, text)
			}
			continue
		}
		if isMessage && item.Role == "user" && firstUser < 0 {
			firstUser = len(rest)
			if !foldableContent(item.Content) {
				return false // an object or number would lose the user's payload
			}
		}
		rest = append(rest, raw)
	}
	switch {
	case len(rest) == 0:
		// Nothing would be left to send; the gateway should judge the original.
		return false
	case len(texts) > 0 && firstUser < 0:
		return false
	case len(texts) == 0 && len(rest) == len(input):
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

// foldableContent reports whether a user message's content is a shape the fold
// can prepend to: a string, an array of parts, or no content at all.
func foldableContent(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return true
	}
	return trimmed[0] == '"' || trimmed[0] == '['
}

// systemItemText is the text of a system message: a string, or the text of its
// input_text/text parts joined by newlines (default.js:26-29). ok is false for
// content that cannot be carried as text (an object, a number, a part of another
// type such as an image, a text-less part), so the caller leaves the body alone
// rather than drop what the operator put in a system message.
func systemItemText(content json.RawMessage) (text string, ok bool) {
	trimmed := bytes.TrimSpace(content)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return "", true
	}
	if json.Unmarshal(trimmed, &text) == nil {
		return text, true
	}
	var parts []struct {
		Type string  `json:"type"`
		Text *string `json:"text"`
	}
	if json.Unmarshal(trimmed, &parts) != nil {
		return "", false
	}
	var texts []string
	for _, part := range parts {
		if (part.Type != "input_text" && part.Type != "text") || part.Text == nil {
			return "", false
		}
		if *part.Text != "" {
			texts = append(texts, *part.Text)
		}
	}
	return strings.Join(texts, "\n"), true
}

// hasType reports whether a tool's `type` is present: any value but a missing
// key, null or an empty string. A non-string type is the client's to answer for.
func hasType(raw json.RawMessage) bool {
	if len(raw) == 0 || string(raw) == "null" {
		return false
	}
	var text string
	if json.Unmarshal(raw, &text) == nil {
		return text != ""
	}
	return true
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
