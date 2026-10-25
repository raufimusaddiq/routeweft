package wire

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"

	"github.com/google/uuid"

	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// Grok CLI (Grok Build) is served by the cli-chat-proxy Responses endpoint
// (registry/grok-cli.js:43-46). Like Codex's it only answers streams and rejects
// what it does not list, and it wants the CLI's identity: an older one is
// answered HTTP 426 (config/grokCli.js:1-3). The pin's executor
// (executors/grok-cli.js:349-526) supplies both.
//
// What this does not do, by decision:
//   - a connection with no deviceId or agentId gets no x-grok-agent-id. The pin's
//     derivation from the host's machine id (:528-550) is unreachable:
//     execute() sets _agentId, then super.execute calls transformRequest, which
//     resets it to deviceId || agentId || null (:424-427) before buildHeaders reads
//     it (:384). Run offline, the pin sends no such header for such a connection
//     (TestGrokCLIAgentIDIsOnlyTheConnectionsOwn). The turn index is
//     grokcli_turns.go, the retry and the 402 grokcli_retry.go.

// grokCLIAllowlist is RESPONSES_API_ALLOWLIST (grok-cli.js:35-52): Codex's list
// plus the sampling fields this endpoint takes.
var grokCLIAllowlist = map[string]bool{
	"model": true, "input": true, "instructions": true, "tools": true, "tool_choice": true,
	"stream": true, "store": true, "reasoning": true, "include": true, "temperature": true,
	"top_p": true, "max_output_tokens": true, "parallel_tool_calls": true, "text": true,
	"metadata": true, "prompt_cache_key": true,
}

// grokCLIHostedTools execute on the endpoint's side (grok-cli.js:23-32).
var grokCLIHostedTools = map[string]bool{
	"web_search": true, "x_search": true, "web_search_preview": true, "file_search": true,
	"image_generation": true, "code_interpreter": true, "mcp": true, "local_shell": true,
}

var (
	grokCLIEffortLevels = []string{"low", "medium", "high", "xhigh"}
	// grokCLIItemID is an item id the endpoint issued itself (grok-cli.js:56).
	grokCLIItemID = regexp.MustCompile(`(?i)^(?:rs|msg|fc)_[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
	// grokCLIEffortModel is supportsGrokCliReasoningEffort (config/grokCli.js:10-13).
	grokCLIEffortModel = regexp.MustCompile(`^grok-4\.5(?:$|-)`)
	// grokCLIFreeform is the schema a freeform (custom) tool is flattened to (:57-61).
	grokCLIFreeform = map[string]any{
		"type":       "object",
		"properties": map[string]any{"input": map[string]any{"type": "string"}},
		"required":   []any{"input"},
	}
)

var grokCLIHook = Hook{
	ForceStream:    true,
	Finalize:       grokCLIBody,
	RequestHeaders: grokCLIHeaders,
	Do:             accountless(GrokCLIDo),
	ClassifyError:  grokCLIPaymentRequired,
}

// grokCLIBody is transformRequest (grok-cli.js:419-525).
func grokCLIBody(protocol string, body Body) bool {
	if protocol != openaiadapter.ResponsesProtocol {
		return false
	}
	changed := false
	if grokCLIInput(body) {
		changed = true
	}
	if grokCLITools(body) {
		changed = true
	}
	if body.set("stream", true) {
		changed = true
	}
	if body.set("store", false) {
		changed = true
	}
	suffix := ""
	model, _ := body.text("model")
	for _, level := range grokCLIEffortLevels {
		if strings.HasSuffix(model, "-"+level) {
			suffix = level
			model = strings.TrimSuffix(model, "-"+level)
			if body.set("model", model) {
				changed = true
			}
			break
		}
	}
	if grokCLIReasoning(body, model, suffix) {
		changed = true
	}
	if body.keepOnly(grokCLIAllowlist) {
		changed = true
	}
	return changed
}

// grokCLIInput normalizes the input (:429-453): a string or empty input becomes
// a user turn, a Chat body that arrived with messages becomes input turns,
// custom tool calls become function calls, a reasoning item the endpoint did not
// issue is dropped, an output with no call is dropped, and stored-item
// references go while the endpoint's own ids stay.
func grokCLIInput(body Body) bool {
	changed := false
	value, _ := decodeAny(body["input"])
	var items []any
	switch typed := value.(type) {
	case string:
		text := typed
		if strings.TrimSpace(text) == "" {
			text = "..."
		}
		items = responsesPlaceholder(text)
	case []any:
		items = typed
		if len(items) == 0 {
			items = responsesPlaceholder("...")
		}
	case nil:
		if messages, ok := body.array("messages"); ok && len(messages) > 0 {
			items = make([]any, 0, len(messages))
			for _, entry := range messages {
				message, _ := entry.(map[string]any)
				role, _ := message["role"].(string)
				if role == "" {
					role = "user"
				}
				content, isText := message["content"].(string)
				if !isText {
					encoded, _ := encodeAny(message["content"])
					content = string(encoded)
					if message["content"] == nil {
						content = `""`
					}
				}
				items = append(items, map[string]any{"type": "message", "role": role, "content": content})
			}
			if body.delete("messages") {
				changed = true
			}
		} else {
			items = []any{map[string]any{"type": "message", "role": "user", "content": "..."}}
		}
	default:
		return false
	}
	items = grokCLINormalizeItems(items)
	items = stripStoredItemReferences(items, grokCLIItemID.MatchString)
	if body.set("input", items) {
		changed = true
	}
	return changed
}

// grokCLINormalizeItems is normalizeGrokCliInput (:161-221).
func grokCLINormalizeItems(input []any) []any {
	normalized := make([]any, 0, len(input))
	calls := map[string]bool{}
	for _, entry := range input {
		item, ok := entry.(map[string]any)
		if !ok {
			normalized = append(normalized, entry)
			continue
		}
		delete(item, "internal_chat_message_metadata_passthrough")
		id, _ := item["id"].(string)
		callID := firstText(item["call_id"], item["id"])
		switch item["type"] {
		case "reasoning":
			if _, encrypted := item["encrypted_content"].(string); !grokCLIItemID.MatchString(id) || !encrypted {
				continue
			}
		case "custom_tool_call":
			name, _ := item["name"].(string)
			if name = strings.TrimSpace(name); callID == "" || name == "" {
				continue
			}
			payload := item["input"]
			if payload == nil {
				payload = item["arguments"]
			}
			arguments, _ := encodeAny(map[string]any{"input": grokCLIText(payload)})
			item = map[string]any{"type": "function_call", "call_id": callID, "name": name, "arguments": string(arguments)}
			calls[callID] = true
		case "custom_tool_call_output", "function_call_output":
			if callID == "" {
				continue
			}
			item = map[string]any{"type": "function_call_output", "call_id": callID, "output": grokCLIText(item["output"])}
		case "function_call":
			name, _ := item["name"].(string)
			if name = strings.TrimSpace(name); callID == "" || name == "" {
				continue
			}
			rebuilt := map[string]any{"type": "function_call", "call_id": callID, "name": name}
			if grokCLIItemID.MatchString(id) {
				rebuilt["id"] = id
			}
			if arguments, isText := item["arguments"].(string); isText {
				rebuilt["arguments"] = arguments
			} else {
				arguments := item["arguments"]
				if arguments == nil {
					arguments = map[string]any{}
				}
				encoded, _ := encodeAny(arguments)
				rebuilt["arguments"] = string(encoded)
			}
			if status, isText := item["status"].(string); isText {
				rebuilt["status"] = status
			}
			item = rebuilt
			calls[callID] = true
		}
		normalized = append(normalized, item)
	}
	kept := make([]any, 0, len(normalized))
	for _, entry := range normalized {
		if item, ok := entry.(map[string]any); ok && item["type"] == "function_call_output" {
			if callID, _ := item["call_id"].(string); !calls[callID] {
				continue
			}
		}
		kept = append(kept, entry)
	}
	return kept
}

// grokCLIText is stringifyGrokCliToolOutput (:151-155): a string as it is,
// nothing as the empty string, anything else as JSON.
func grokCLIText(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case nil:
		return ""
	}
	encoded, _ := encodeAny(value)
	return string(encoded)
}

// firstString is JS `a || b` over two candidate ids.
func firstText(values ...any) string {
	for _, value := range values {
		if text, ok := value.(string); ok && text != "" {
			return text
		}
	}
	return ""
}

// grokCLITools is normalizeGrokCliTools (:243-326).
func grokCLITools(body Body) bool {
	tools, ok := body.array("tools")
	if !ok || len(tools) == 0 {
		changed := body.delete("tools")
		if body.delete("tool_choice") {
			changed = true
		}
		return changed
	}
	valid := map[string]bool{}
	hosted := map[string]bool{}
	kept := make([]any, 0, len(tools))
	for _, entry := range tools {
		tool, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := tool["type"].(string)
		_, named := tool["name"].(string)
		if kind != "function" {
			if grokCLIHostedTools[kind] {
				hosted[kind] = true
				kept = append(kept, tool)
				continue
			}
			// A typeless tool, or a typed one that carries a name, is a function in
			// another shape (:261-269).
			if kind != "" && !named {
				continue
			}
		}
		name, description, parameters, ok := functionToolParts(tool)
		if !ok {
			continue
		}
		if kind == "custom" {
			parameters = grokCLIFreeform
		}
		flat := map[string]any{"type": "function", "name": truncate(name, 128), "parameters": parameters}
		if description != "" {
			flat["description"] = description
		}
		valid[truncate(name, 128)] = true
		kept = append(kept, flat)
	}
	if len(kept) == 0 {
		changed := body.delete("tools")
		if body.delete("tool_choice") {
			changed = true
		}
		return changed
	}
	changed := body.set("tools", kept)
	if choice, ok := body.object("tool_choice"); ok {
		kind, _ := choice["type"].(string)
		if kind == "function" || kind == "custom" {
			name, _ := choice["name"].(string)
			if choice["name"] == nil {
				function, _ := choice["function"].(map[string]any)
				name, _ = function["name"].(string)
			}
			name = truncate(strings.TrimSpace(name), 128)
			if name == "" || !valid[name] {
				if body.delete("tool_choice") {
					changed = true
				}
			} else if body.set("tool_choice", map[string]any{"type": "function", "name": name}) {
				changed = true
			}
		} else if !hosted[kind] && body.delete("tool_choice") {
			changed = true
		}
	}
	return changed
}

// grokCLIReasoning is the reasoning block (:475-502). Effort is sent only for the
// models that take it; summary and the encrypted continuity item always are.
func grokCLIReasoning(body Body, model, suffix string) bool {
	changed := false
	supports := grokCLIEffortModel.MatchString(model)
	requested, _ := body.text("reasoning_effort")
	reasoning, isObject := body.object("reasoning")
	if !isObject {
		reasoning = map[string]any{"summary": "concise"}
		if supports {
			reasoning["effort"] = normalizeGrokCLIEffort(firstText(requested, suffix))
		}
	} else {
		if supports {
			explicit, _ := reasoning["effort"].(string)
			reasoning["effort"] = normalizeGrokCLIEffort(firstText(explicit, requested, suffix))
		} else {
			delete(reasoning, "effort")
		}
		if summary, ok := reasoning["summary"]; !ok || !truthy(summary) {
			reasoning["summary"] = "concise"
		}
	}
	if body.set("reasoning", reasoning) {
		changed = true
	}
	if body.delete("reasoning_effort") {
		changed = true
	}
	if reasoning["effort"] != "none" {
		include, _ := body.array("include")
		if include == nil {
			include = []any{}
		}
		present := false
		for _, entry := range include {
			present = present || entry == "reasoning.encrypted_content"
		}
		if !present {
			include = append(include, "reasoning.encrypted_content")
		}
		if body.set("include", include) {
			changed = true
		}
	}
	return changed
}

// normalizeGrokCLIEffort is normalizeGrokCliEffort (:124-129).
func normalizeGrokCLIEffort(value string) string {
	effort := strings.ToLower(strings.TrimSpace(value))
	if effort == "max" {
		return "xhigh"
	}
	for _, level := range grokCLIEffortLevels {
		if effort == level {
			return effort
		}
	}
	return "high"
}

// grokCLIHeaders is buildHeaders (:362-398): the CLI's identity, the session the
// request belongs to, and the account it speaks for. It reads the final body, so
// the model and turn it names are the ones sent.
func grokCLIHeaders(provider routing.ProviderRef, raw []byte, _ string) (map[string]string, error) {
	var body Body
	_ = json.Unmarshal(raw, &body)
	session, _ := body.text("prompt_cache_key")
	if session == "" {
		session = connectionSession(provider)
	}
	if session == "" {
		session = uuid.NewString()
	}
	headers := map[string]string{
		"User-Agent":               "grok-shell/" + grokCLIVersion + " (linux; x86_64)",
		"Accept":                   "text/event-stream",
		"x-grok-client-identifier": "grok-shell",
		"x-grok-client-version":    grokCLIVersion,
		"x-grok-session-id":        session,
		"x-grok-conv-id":           session,
		"x-grok-req-id":            uuid.NewString(),
		"x-grok-turn-idx":          strconv.Itoa(grokCLITurnStore.resolve(session, grokCLITurns(body), raw)),
	}
	settings := provider.Connection.Settings
	if agent := firstNonEmpty(settings.DeviceID, settings.AgentID); agent != "" {
		headers["x-grok-agent-id"] = agent
	}
	if model, ok := body.text("model"); ok && model != "" {
		headers["x-grok-model-override"] = model
	}
	if settings.Email != "" {
		headers["x-email"] = settings.Email
	}
	if settings.UserID != "" {
		headers["x-userid"] = settings.UserID
	}
	return headers, nil
}

// grokCLITurns is countGrokCliUserTurns (:72-82): user turns in the input, at
// least one.
func grokCLITurns(body Body) int {
	input, _ := body.array("input")
	turns := 0
	for _, entry := range input {
		item, ok := entry.(map[string]any)
		if !ok || item["role"] != "user" {
			continue
		}
		if kind, typed := item["type"]; !typed || kind == "" || kind == "message" {
			turns++
		}
	}
	if turns < 1 {
		return 1
	}
	return turns
}
