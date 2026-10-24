package wire

import (
	"encoding/json"
	"regexp"
	"strings"

	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// openCodeGoTransport lists the pin's Responses-only models (registry/opencode-go.js:41,72-74).
func openCodeGoTransport(model string) string {
	switch model {
	case "gpt-5.6-luna", "grok-4.6", "muse-spark-1.2-contributor", "muse-spark-1.3-contributor":
		return openaiadapter.ResponsesProtocol
	}
	return ""
}

func openCodeGoStream(provider routing.ProviderRef, body Body) bool {
	if !openCodeGoForcesStream(provider) {
		return false
	}
	return body.set("stream", true)
}

func openCodeGoForcesStream(provider routing.ProviderRef) bool {
	return provider.Protocol == openaiadapter.ResponsesProtocol && openCodeGoTransport(provider.UpstreamModel) == provider.Protocol
}

// openCodeGoLuna is the one Responses model with extra request omissions
// (executors/opencode-go.js:20,202-206).
const openCodeGoLuna = "gpt-5.6-luna"

// openCodeGoLunaOmit are the fields the Luna relay rejects (:21-27).
var openCodeGoLunaOmit = []string{
	"temperature", "top_p", "frequency_penalty", "presence_penalty",
	"logprobs", "top_logprobs", "n", "seed", "max_tokens",
	"max_completion_tokens", "max_output_tokens", "user",
	"prompt_cache_retention", "metadata", "stream_options",
	"safety_identifier", "previous_response_id",
}

// openCodeGoServerID is a server-generated item id (executors/codex.js:30).
var openCodeGoServerID = regexp.MustCompile(`^(rs|fc|resp|msg)_`)

// openCodeGoFinalize is transformRequest (executors/opencode-go.js:173-209) for
// the fields it names, then the forced stream. Everything it removes or renames is
// removed or renamed by key; fields it does not know travel as received.
func openCodeGoFinalize(provider routing.ProviderRef, body Body) bool {
	// A client-scoped prior response id never resolves upstream (store=false), so
	// the relay answers 400; it goes for every model and wire (:174-178).
	changed := body.delete("previous_response_id")
	if openCodeGoNormalize(provider, body) {
		changed = true
	}
	if openCodeGoStream(provider, body) {
		changed = true
	}
	return changed
}

// openCodeGoNormalize is the Responses-only models' half of transformRequest
// (:179-207). The pin's remaining input work (the empty-input placeholder,
// function_call/function_call_output coercion, tool flattening) is not here.
func openCodeGoNormalize(provider routing.ProviderRef, body Body) bool {
	if !openCodeGoForcesStream(provider) {
		return false
	}
	changed := false
	// Responses names the output cap max_output_tokens (:185-191).
	if _, has := body["max_output_tokens"]; !has {
		if raw, ok := body["max_completion_tokens"]; ok {
			body["max_output_tokens"], changed = raw, true
		} else if raw, ok := body["max_tokens"]; ok {
			body["max_output_tokens"], changed = raw, true
		}
	}
	if body.dropKeys("max_tokens", "max_completion_tokens") {
		changed = true
	}
	// reasoning_effort becomes reasoning.effort, and a reasoning object always
	// asks for a summary (:192-198).
	if effort, ok := body["reasoning_effort"]; ok {
		if _, has := body["reasoning"]; !has {
			if encoded, err := json.Marshal(map[string]json.RawMessage{"effort": effort, "summary": json.RawMessage(`"auto"`)}); err == nil {
				body["reasoning"], changed = encoded, true
			}
		}
		body.delete("reasoning_effort")
		changed = true
	}
	var reasoning map[string]json.RawMessage
	if raw := body["reasoning"]; isObjectRaw(raw) && json.Unmarshal(raw, &reasoning) == nil && reasoning != nil && !jsTruthy(reasoning["summary"]) {
		reasoning["summary"] = json.RawMessage(`"auto"`)
		if encoded, err := json.Marshal(reasoning); err == nil {
			body["reasoning"], changed = encoded, true
		}
	}
	if body.set("store", false) {
		changed = true
	}
	luna := provider.UpstreamModel == openCodeGoLuna
	if luna && body.dropKeys(openCodeGoLunaOmit...) {
		changed = true
	}
	if openCodeGoInput(body, luna) {
		changed = true
	}
	return changed
}

// openCodeGoInput rewrites only the input items the pin touches. Every item a
// rewrite leaves alone keeps its bytes. For all Responses models a prior turn's
// reasoning item is dropped and encrypted_content removed from the rest, because
// an encrypted payload cannot be validated across rotated accounts (:102-111); for
// Luna system messages become developer messages and server item references go
// (codex.js:50-70).
func openCodeGoInput(body Body, luna bool) bool {
	var items []json.RawMessage
	if json.Unmarshal(body["input"], &items) != nil {
		return false
	}
	kept := make([]json.RawMessage, 0, len(items))
	changed := false
	for _, raw := range items {
		item, drop, edited := openCodeGoItem(raw, luna)
		if drop {
			changed = true
			continue
		}
		if edited {
			changed = true
		}
		kept = append(kept, item)
	}
	if !changed {
		return false
	}
	return setJSON(body, "input", kept)
}

func openCodeGoItem(raw json.RawMessage, luna bool) (item json.RawMessage, drop, edited bool) {
	if !isObjectRaw(raw) {
		var id string
		return raw, luna && json.Unmarshal(raw, &id) == nil && openCodeGoServerID.MatchString(id), false
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return raw, false, false
	}
	var kind, role, id string
	_ = json.Unmarshal(fields["type"], &kind)
	_ = json.Unmarshal(fields["role"], &role)
	_ = json.Unmarshal(fields["id"], &id)
	if kind == "reasoning" || (luna && kind == "item_reference") {
		return raw, true, false
	}
	if luna && role == "system" && jsFalsyType(fields["type"], kind) {
		fields["role"], edited = json.RawMessage(`"developer"`), true
	}
	if luna && openCodeGoServerID.MatchString(id) && fields["id"][0] == '"' {
		delete(fields, "id")
		edited = true
	}
	if _, ok := fields["encrypted_content"]; ok {
		delete(fields, "encrypted_content")
		edited = true
	}
	if _, ok := fields["reasoning_encrypted_content"]; ok {
		delete(fields, "reasoning_encrypted_content")
		edited = true
	}
	if !edited {
		return raw, false, false
	}
	encoded, err := json.Marshal(fields)
	if err != nil {
		return raw, false, false
	}
	return encoded, false, true
}

// jsFalsyType is `!item.type || item.type === "message"` (codex.js:54).
func jsFalsyType(raw json.RawMessage, kind string) bool {
	return !jsTruthy(raw) || kind == "message"
}

func isObjectRaw(raw json.RawMessage) bool {
	return strings.HasPrefix(strings.TrimSpace(string(raw)), "{")
}
