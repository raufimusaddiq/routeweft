package wire

import (
	"encoding/json"
	"math"
	"regexp"
	"strings"

	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/providers/capabilities"
)

// Body sanitation: the pin's DefaultExecutor.transformRequest runs
// stripUnsupportedParams and injectReasoningContent on the body it is about to
// send (open-sse/executors/default.js:106,109). That is the finalize step, not
// the translator (translator/index.js builds the body and only applies
// preserveCacheControl, :152), so the rules live here and run on every body
// FinalizeBody sees, whichever leg built it: a native passthrough, a Chat
// conversion, or a probe. A translator-time rule (cache_control stripping,
// openai.FilterChatBody) is not repeated here.
//
// The rules run before the provider's own Finalize hook, as the pin's
// subclasses call super.transformRequest first (kimchi.js:101, codebuddy-cn.js:19).
// Every rule is idempotent, so a body finalized twice (serving, then a probe
// that replays it) comes out the same.

// sanitizeScope says which of the two rule sets the pin reaches for a provider.
type sanitizeScope struct{ strip, inject bool }

// noSanitize: providers whose pin executor does not extend DefaultExecutor's
// transformRequest (BaseExecutor subclasses, and azure, whose transformRequest
// returns the body as is: azure.js:54), so neither rule set reaches them.
var noSanitize = map[string]bool{
	"antigravity": true, "azure": true, "codex": true, "commandcode": true,
	"cursor": true, "devin-cli": true, "gemini-cli": true, "grok-cli": true,
	"grok-web": true, "iflow": true, "kiro": true, "mimo-free": true,
	"perplexity-web": true, "qoder": true, "trae": true, "vertex": true,
	"vertex-partner": true, "windsurf": true, "zed": true,
}

func sanitizeScopeFor(providerID string) sanitizeScope {
	switch {
	case providerID == "github":
		return sanitizeScope{strip: true} // github.js:109 strips, never injects
	case providerID == "opencode":
		return sanitizeScope{inject: true} // opencode.js:464 injects, never strips
	case noSanitize[providerID]:
		return sanitizeScope{}
	}
	return sanitizeScope{strip: true, inject: true}
}

// sanitizeBody applies the strip rules and reasoning injection to body.
func sanitizeBody(providerID, protocol string, body Body) bool {
	scope := sanitizeScopeFor(providerID)
	changed := false
	if scope.strip && stripUnsupportedParams(providerID, protocol, body) {
		changed = true
	}
	if scope.inject && injectReasoningContent(providerID, protocol, body) {
		changed = true
	}
	return changed
}

// stripRule is one entry of the pin's STRIP_RULES (paramSupport.js:8-45).
type stripRule struct {
	provider string
	match    func(model string) bool // nil matches every model
	drop     []string
	// dropMessageFields removes fields from assistant messages (:69-76).
	dropMessageFields []string
	// flattenContent joins array content to a plain string (:78-86).
	flattenContent bool
	// maxOutputCap clamps the output budget fields (:87-102).
	maxOutputCap float64
	// clampToModelMaxOutput also clamps to the capability catalog's ceiling for
	// the model (:87-92). A model the catalog does not resolve adds no ceiling.
	clampToModelMaxOutput bool
}

func modelMatches(pattern string) func(string) bool {
	re := regexp.MustCompile(pattern)
	return re.MatchString
}

var (
	reasoningFields   = []string{"reasoning_content", "reasoning", "reasoning_details"}
	githubClaudeModel = regexp.MustCompile(`(?i)claude`)
	// github's Claude 4.6 opus/sonnet accept thinking (paramSupport.js:14).
	githubClaude46 = regexp.MustCompile(`(?i)claude.*(opus|sonnet).*4\.6`)
)

var stripRules = []stripRule{
	{match: modelMatches(`(?i)claude`), drop: []string{"temperature"}},
	{provider: "github", match: modelMatches(`(?i)gpt-5\.4`), drop: []string{"temperature"}},
	{provider: "github", match: func(m string) bool { return githubClaudeModel.MatchString(m) && !githubClaude46.MatchString(m) }, drop: []string{"thinking", "reasoning_effort"}},
	{provider: "cloudflare-ai", flattenContent: true},
	{provider: "xiaomi-mimo", match: modelMatches(`(?i)preview`), flattenContent: true},
	{provider: "volcengine-ark", match: modelMatches(`(?i)glm-5`), clampToModelMaxOutput: true},
	{provider: "volcengine-ark", match: modelMatches(`(?i)kimi`), maxOutputCap: 32768},
	{provider: "groq", dropMessageFields: reasoningFields},
	{provider: "mistral", dropMessageFields: reasoningFields},
	{provider: "cerebras", dropMessageFields: reasoningFields},
	{provider: "opencode-go", match: modelMatches(`(?i)^deepseek`), drop: []string{"response_format"}},
}

// stripUnsupportedParams: paramSupport.js:60-105. A body without a string model
// is left alone (:61). Rules that rewrite messages apply to Chat bodies only:
// the pin runs them on whatever body.messages holds, but their shapes (an OpenAI
// content-part array, assistant reasoning fields) exist only on Chat, and on a
// Messages body flattening content would destroy tool_use and image blocks.
func stripUnsupportedParams(providerID, protocol string, body Body) bool {
	var model string
	if json.Unmarshal(body["model"], &model) != nil || model == "" {
		return false
	}
	changed := false
	for _, rule := range stripRules {
		if rule.provider != "" && rule.provider != providerID {
			continue
		}
		if rule.match != nil && !rule.match(model) {
			continue
		}
		for _, key := range rule.drop {
			if body.delete(key) {
				changed = true
			}
		}
		if protocol == openaiadapter.ChatProtocol {
			if len(rule.dropMessageFields) > 0 && editMessages(body, func(message map[string]json.RawMessage) bool {
				if !messageRole(message, "assistant") {
					return false
				}
				touched := false
				for _, key := range rule.dropMessageFields {
					if _, ok := message[key]; ok {
						delete(message, key)
						touched = true
					}
				}
				return touched
			}) {
				changed = true
			}
			if rule.flattenContent && editMessages(body, flattenMessageContent) {
				changed = true
			}
		}
		ceiling := rule.maxOutputCap
		if rule.clampToModelMaxOutput {
			if modelCeiling, ok := capabilities.OutputCeiling(providerID, model); ok && (ceiling == 0 || float64(modelCeiling) < ceiling) {
				ceiling = float64(modelCeiling)
			}
		}
		if ceiling > 0 {
			for _, key := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens"} {
				if clampNumber(body, key, ceiling) {
					changed = true
				}
			}
		}
	}
	return changed
}

// flattenMessageContent joins an array content to the concatenation of its text
// parts; every other part contributes "" (paramSupport.js:80-83).
func flattenMessageContent(message map[string]json.RawMessage) bool {
	if !isArrayRaw(message["content"]) {
		return false
	}
	var parts []json.RawMessage
	if json.Unmarshal(message["content"], &parts) != nil {
		return false
	}
	var text strings.Builder
	for _, raw := range parts {
		var part struct {
			Type string  `json:"type"`
			Text *string `json:"text"`
		}
		if json.Unmarshal(raw, &part) == nil && part.Type == "text" && part.Text != nil {
			text.WriteString(*part.Text)
		}
	}
	encoded, err := json.Marshal(text.String())
	if err != nil {
		return false
	}
	message["content"] = encoded
	return true
}

// clampNumber lowers a finite number above ceiling to it (paramSupport.js:53-57).
func clampNumber(body Body, key string, ceiling float64) bool {
	var value float64
	if json.Unmarshal(body[key], &value) != nil || math.IsNaN(value) || math.IsInf(value, 0) || value <= ceiling {
		return false
	}
	return setJSON(body, key, int64(ceiling))
}

// ---- reasoning_content injection (reasoningContentInjector.js) ----

var (
	reasoningInjectKimi     = regexp.MustCompile(`(?i)^kimi-`)
	reasoningInjectDeepseek = regexp.MustCompile(`(?i)deepseek`)
)

// reasoningProviders: registry transport.reasoningInject, scope "all"
// (registry/deepseek.js:25, minimax.js:27, minimax-cn.js:27, opencode.js:26).
var reasoningProviders = map[string]bool{"deepseek": true, "minimax": true, "minimax-cn": true, "opencode": true}

// deepseekV4ProAliases: the -max and -none ids are Routeweft-side names for one
// upstream model with a thinking setting (reasoningContentInjector.js:17-27).
var deepseekV4ProAliases = map[string]struct {
	thinking string
	effort   string // "" removes reasoning_effort
}{
	"deepseek-v4-pro-max":  {"enabled", "max"},
	"deepseek-v4-pro-none": {"disabled", ""},
}

// injectReasoningContent: reasoningContentInjector.js:70-76. Thinking-mode
// models require reasoning_content echoed on assistant messages and
// OpenAI-format clients do not send it, so an assistant message without one gets
// a single space (:6,29-43). A registry provider rule wins over the model rule
// (:73); kimi-* models need it only on a message carrying tool_calls, deepseek
// models on every assistant message.
//
// The space is a structural placeholder, not invented content. Chat bodies only:
// reasoning_content is a Chat message field, and the pin's other targets either
// have no `messages` (Responses) or would be sent a field their schema rejects
// (Messages). A reasoning_content of another type than a string is the client's
// to answer for and is left for the gateway to judge, where the pin overwrites it.
func injectReasoningContent(providerID, protocol string, body Body) bool {
	if protocol != openaiadapter.ChatProtocol {
		return false
	}
	var model string
	_ = json.Unmarshal(body["model"], &model)
	changed := applyDeepSeekV4ProAlias(providerID, model, body)
	var toolCallsOnly bool
	switch {
	case reasoningProviders[providerID]:
	case reasoningInjectKimi.MatchString(model):
		toolCallsOnly = true
	case reasoningInjectDeepseek.MatchString(model):
	default:
		return changed
	}
	if editMessages(body, func(message map[string]json.RawMessage) bool {
		if !messageRole(message, "assistant") {
			return false
		}
		if existing, present := message["reasoning_content"]; present && !isNullRaw(existing) {
			var text string
			if json.Unmarshal(existing, &text) != nil || text != "" {
				return false // non-empty text, or a type the gateway should judge
			}
		}
		if toolCallsOnly {
			var calls []json.RawMessage
			if json.Unmarshal(message["tool_calls"], &calls) != nil || len(calls) == 0 {
				return false
			}
		}
		message["reasoning_content"] = json.RawMessage(`" "`)
		return true
	}) {
		changed = true
	}
	return changed
}

// applyDeepSeekV4ProAlias: reasoningContentInjector.js:45-68. A body whose
// extra_body or extra_body.thinking is present but not an object is left as
// received: the pin spreads it into a nonsense object.
func applyDeepSeekV4ProAlias(providerID, model string, body Body) bool {
	alias, ok := deepseekV4ProAliases[model]
	if providerID != "deepseek" || !ok {
		return false
	}
	extra := map[string]json.RawMessage{}
	if raw := body["extra_body"]; len(raw) > 0 && !isNullRaw(raw) && json.Unmarshal(raw, &extra) != nil {
		return false
	}
	thinking := map[string]json.RawMessage{}
	if raw := extra["thinking"]; len(raw) > 0 && !isNullRaw(raw) && json.Unmarshal(raw, &thinking) != nil {
		return false
	}
	thinkingType, err := json.Marshal(alias.thinking)
	if err != nil {
		return false
	}
	thinking["type"] = thinkingType
	if !setJSON(Body(extra), "thinking", thinking) || !setJSON(body, "extra_body", extra) {
		return false
	}
	if alias.effort != "" {
		setJSON(body, "reasoning_effort", alias.effort)
	} else {
		body.delete("reasoning_effort")
	}
	return setJSON(body, "model", "deepseek-v4-pro")
}

func messageRole(message map[string]json.RawMessage, want string) bool {
	var role string
	return json.Unmarshal(message["role"], &role) == nil && role == want
}

// editMessages runs edit on each object in body.messages and re-encodes only the
// messages it changed. It reports whether any changed; a body whose messages is
// not an array is left alone.
func editMessages(body Body, edit func(message map[string]json.RawMessage) bool) bool {
	var messages []json.RawMessage
	if json.Unmarshal(body["messages"], &messages) != nil {
		return false
	}
	changed := false
	for i, raw := range messages {
		var message map[string]json.RawMessage
		if json.Unmarshal(raw, &message) != nil || message == nil || !edit(message) {
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
