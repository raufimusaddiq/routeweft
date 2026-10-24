package wire

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"strings"

	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/providers/capabilities"
	codexprovider "github.com/raufimusaddiq/routeweft/internal/providers/codex"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// Codex is served by a stream-only Responses endpoint (registry/codex.js:37-40)
// that rejects what it does not know. The pin's executor rewrites every request to
// it (executors/codex.js:417-519) and a compact request to a smaller document
// (:420-427). The rewrite runs on the final outbound body (AGENTS §6 step 8), so
// nothing after it can put a field back.
//
// What this does not do, by decision:
//   - it does not download remote images to inline them (codex.js:263-278): that
//     is a server-side fetch of a client-supplied URL, and Routeweft will not make
//     one outside its SSRF policy. A remote image URL reaches the endpoint as an
//     input_image URL; the endpoint's refusal is the client's to see;
//   - it takes no session from the client's headers or from a hash of the
//     assistant's text (codex.js:129-138): the prompt cache key is the client's
//     own, else one derived from the connection.

// codexAllowlist is RESPONSES_API_ALLOWLIST (codex.js:43-47).
var codexAllowlist = map[string]bool{
	"model": true, "input": true, "instructions": true, "tools": true, "tool_choice": true,
	"stream": true, "store": true, "reasoning": true, "service_tier": true, "include": true,
	"prompt_cache_key": true, "client_metadata": true, "text": true,
}

// codexHostedTools execute on the endpoint's side (codex.js:33-37).
var codexHostedTools = map[string]bool{
	"image_generation": true, "web_search": true, "web_search_preview": true, "file_search": true,
	"computer": true, "computer_use_preview": true, "code_interpreter": true, "mcp": true,
	"local_shell": true, "tool_search": true,
}

// codexEffortSuffixes are the thinking levels a model id may end in
// (codex.js:465-474), in the order the pin tries them.
var codexEffortSuffixes = []string{"none", "minimal", "low", "medium", "high", "xhigh"}

var codexHook = Hook{
	OpenAIHeaders:   codexHeaders,
	ClassifyError:   codexReset,
	ForceStream:     true,
	Finalize:        codexBody,
	FinalizeAccount: codexCacheKey,
	Compact:         codexCompactBody,
	ClientHeaders:   []string{"x-codex-beta-features"},
	RequestHeaders:  codexSession,
	Do:              accountless(CodexDo),
}

// codexBody is transformRequest (codex.js:417-519) for a streaming request.
func codexBody(protocol string, body Body) bool {
	if protocol != openaiadapter.ResponsesProtocol {
		return false
	}
	changed := false
	if codexInput(body) {
		changed = true
	}
	if codexTools(body) {
		changed = true
	}
	if body.set("stream", true) {
		changed = true
	}
	if instructions, isText := body.text("instructions"); body.absent("instructions") || (isText && strings.TrimSpace(instructions) == "") {
		if body.set("instructions", codexprovider.DefaultInstructions) {
			changed = true
		}
	}
	if body.set("store", false) {
		changed = true
	}
	effort := ""
	if model, ok := body.text("model"); ok {
		// A review twin is the base model on the review quota (:461).
		if mapped := codexprovider.UpstreamModel(model); mapped != model {
			model = mapped
			if body.set("model", model) {
				changed = true
			}
		}
		for _, level := range codexEffortSuffixes {
			if strings.HasSuffix(model, "-"+level) {
				effort = level
				if body.set("model", strings.TrimSuffix(model, "-"+level)) {
					changed = true
				}
				break
			}
		}
	}
	if codexReasoning(body, effort) {
		changed = true
	}
	// fast is the endpoint's priority tier; any other tier it does not serve is
	// dropped (:510-511). A blank tier is no tier and stays as it was.
	if tier, ok := body.text("service_tier"); ok && tier == "fast" {
		if body.set("service_tier", "priority") {
			changed = true
		}
	}
	if tier, ok := body.text("service_tier"); !body.absent("service_tier") && (!ok || tier != "priority") {
		if body.delete("service_tier") {
			changed = true
		}
	}
	// The allowlist subsumes the pin's explicit deletes (codex.js:491-508): a field
	// the endpoint does not list is a field it would reject.
	if body.keepOnly(codexAllowlist) {
		changed = true
	}
	return changed
}

// codexInput is the input half of transformRequest: a string or empty input
// becomes a user turn (:429-435), system turns become developer turns so they
// stay in the cacheable prefix (:438), stored-item references go (:440), and an
// image_url part becomes the input_image the endpoint takes.
func codexInput(body Body) bool {
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
		items = responsesPlaceholder("...")
	default:
		return false
	}
	if items == nil {
		return false
	}
	for _, item := range items {
		object, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if object["role"] == "system" {
			if kind, typed := object["type"]; !typed || kind == "" || kind == "message" {
				object["role"] = "developer"
			}
		}
		parts, ok := object["content"].([]any)
		if !ok {
			continue
		}
		for index, part := range parts {
			image, ok := part.(map[string]any)
			if !ok || image["type"] != "image_url" {
				continue
			}
			url, detail := "", "auto"
			switch reference := image["image_url"].(type) {
			case string:
				url = reference
			case map[string]any:
				url, _ = reference["url"].(string)
				if text, ok := reference["detail"].(string); ok && text != "" {
					detail = text
				}
			}
			if url != "" {
				parts[index] = map[string]any{"type": "input_image", "image_url": url, "detail": detail}
			}
		}
	}
	items = stripStoredItemReferences(items, nil)
	return body.set("input", items)
}

// codexTools is normalizeCodexTools (codex.js:73-127): Chat-shaped function tools
// become the flat Responses shape, tools the endpoint cannot run are dropped, a
// schema pattern its validator cannot parse is removed, and a tool_choice naming
// a function that is no longer declared goes.
func codexTools(body Body) bool {
	tools, ok := body.array("tools")
	if !ok {
		return false
	}
	valid := map[string]bool{}
	kept := make([]any, 0, len(tools))
	for _, entry := range tools {
		tool, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := tool["type"].(string)
		if kind == "namespace" {
			if nested, ok := tool["tools"].([]any); ok {
				for _, inner := range nested {
					function, ok := inner.(map[string]any)
					if !ok {
						continue
					}
					if name, ok := function["name"].(string); ok {
						if name = truncate(strings.TrimSpace(name), 128); name != "" {
							valid[name] = true
						}
					}
					if schema, ok := function["parameters"].(map[string]any); ok {
						function["parameters"] = stripUnicodePatterns(schema)
					}
				}
			}
			kept = append(kept, tool)
			continue
		}
		if kind != "function" {
			if kind == "custom" {
				kept = append(kept, tool)
				continue
			}
			_, named := tool["name"].(string)
			if kind == "" || truthy(tool["function"]) || named {
				continue
			}
			if codexHostedTools[kind] {
				kept = append(kept, tool)
			}
			continue
		}
		name, description, parameters, ok := functionToolParts(tool)
		if !ok {
			continue
		}
		function, _ := tool["function"].(map[string]any)
		strict, hasStrict := tool["strict"].(bool)
		if !hasStrict {
			strict, hasStrict = function["strict"].(bool)
		}
		flat := map[string]any{"type": "function", "name": truncate(name, 128), "parameters": stripUnicodePatterns(parameters)}
		if description != "" {
			flat["description"] = description
		}
		if hasStrict {
			flat["strict"] = strict
		}
		valid[name] = true
		kept = append(kept, flat)
	}
	changed := body.set("tools", kept)
	if choice, ok := body.object("tool_choice"); ok && choice["type"] == "function" {
		if name := toolChoiceFunctionName(choice); name == "" || !valid[name] {
			if body.delete("tool_choice") {
				changed = true
			}
		}
	}
	return changed
}

// unicodePropertyEscape matches \p{...} or \P{...} behind an odd number of
// backslashes (utils/codexToolSchema.js:26): an even count means the backslash
// is itself escaped and the p is a literal.
var unicodePropertyEscape = regexp.MustCompile(`(^|[^\\])(\\\\)*\\[pP]\{`)

// stripUnicodePatterns removes the schema `pattern` keywords the endpoint's
// validator rejects (it implements no Unicode property escapes, so every
// account fails the same request, codexToolSchema.js:1-22). Nothing else in the
// schema changes. `properties` holds property names, not keywords, so its keys
// are never read as `pattern`.
func stripUnicodePatterns(node any) any {
	switch typed := node.(type) {
	case []any:
		for index, item := range typed {
			typed[index] = stripUnicodePatterns(item)
		}
		return typed
	case map[string]any:
		for key, value := range typed {
			switch {
			case key == "pattern":
				if text, ok := value.(string); ok && unicodePropertyEscape.MatchString(text) {
					delete(typed, key)
				}
			case key == "properties":
				if properties, ok := value.(map[string]any); ok {
					for name, schema := range properties {
						properties[name] = stripUnicodePatterns(schema)
					}
					continue
				}
				typed[key] = stripUnicodePatterns(value)
			default:
				typed[key] = stripUnicodePatterns(value)
			}
		}
		return typed
	}
	return node
}

// codexReasoning sets the reasoning block (codex.js:476-489). The order of
// preference is an explicit reasoning.effort, then reasoning_effort, then the
// level the model id ended in, then low. reasoning_effort is consumed.
func codexReasoning(body Body, suffix string) bool {
	changed := false
	model, _ := body.text("model")
	reasoning, hasObject := body.object("reasoning")
	if body.absent("reasoning") {
		requested, _ := body.text("reasoning_effort")
		if requested == "" {
			requested = suffix
		}
		if requested == "" {
			requested = "low"
		}
		reasoning = map[string]any{"effort": normalizeCodexEffort(model, requested), "summary": "auto"}
		hasObject = true
		if body.set("reasoning", reasoning) {
			changed = true
		}
	} else if hasObject {
		if effort, ok := reasoning["effort"].(string); ok {
			reasoning["effort"] = normalizeCodexEffort(model, effort)
		}
		if summary, ok := reasoning["summary"]; !ok || summary == nil || summary == "" {
			reasoning["summary"] = "auto"
		}
		if body.set("reasoning", reasoning) {
			changed = true
		}
	}
	if body.delete("reasoning_effort") {
		changed = true
	}
	if effort, ok := reasoning["effort"].(string); hasObject && ok && effort != "" && effort != "none" {
		// Merged, as Grok CLI's is: a client's own include entries stay.
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

// normalizeCodexEffort is normalizeReasoningEffort (codex.js:140-151): a level
// the model supports stays; none, which a model that cannot disable thinking
// rejects, becomes its lowest real level; max and ultra fall to xhigh where the
// model has no such level.
func normalizeCodexEffort(model, value string) string {
	levels := codexThinkingLevels(model)
	has := func(level string) bool {
		for _, candidate := range levels {
			if candidate == level {
				return true
			}
		}
		return false
	}
	if has(value) {
		return value
	}
	if value == "none" && len(levels) > 0 {
		for _, level := range levels {
			if level != "none" {
				return level
			}
		}
	}
	if value == "ultra" && has("max") {
		return "max"
	}
	if value == "max" || value == "ultra" {
		return "xhigh"
	}
	return value
}

var (
	codexGPT56Levels = []string{"none", "minimal", "low", "medium", "high", "xhigh", "max"}
	// codexThinkingPatterns are the codex rows of PATTERN_THINKING
	// (providers/thinkingLevels.js:42-52), first match wins; matching is a
	// case-insensitive glob on the model id.
	codexThinkingPatterns = []struct {
		pattern *regexp.Regexp
		levels  []string
	}{
		{thinkingGlob("*gpt-6.1-sol*"), []string{"low", "medium", "high", "xhigh", "max"}},
		{thinkingGlob("*gpt-6*"), codexGPT56Levels},
		{thinkingGlob("*gpt-5.6-sol*"), append(append([]string{}, codexGPT56Levels...), "ultra")},
		{thinkingGlob("*gpt-5.6-terra*"), append(append([]string{}, codexGPT56Levels...), "ultra")},
		{thinkingGlob("*gpt-5.6-luna*"), codexGPT56Levels},
		{thinkingGlob("*codex*"), []string{"low", "medium", "high", "xhigh"}},
	}
)

func thinkingGlob(pattern string) *regexp.Regexp {
	return regexp.MustCompile("(?i)^" + strings.ReplaceAll(regexp.QuoteMeta(pattern), `\*`, ".*") + "$")
}

// codexThinkingLevels is getThinkingLevels("codex", model) (thinkingLevels.js:77-87):
// nil when the model has no reasoning.
func codexThinkingLevels(model string) []string {
	caps := capabilities.Resolve("codex", model)
	if !caps.Reasoning {
		return nil
	}
	var levels []string
	for _, entry := range codexThinkingPatterns {
		if entry.pattern.MatchString(model) {
			levels = entry.levels
			break
		}
	}
	if levels == nil {
		switch caps.ThinkingFormat {
		case "openai":
			levels = []string{"none", "minimal", "low", "medium", "high", "xhigh"}
		case "openai-responses":
			levels = []string{"none", "low", "medium", "high", "xhigh", "max"}
		default:
			levels = []string{"none", "low", "medium", "high"}
		}
	}
	if !caps.ThinkingCanDisable {
		filtered := make([]string, 0, len(levels))
		for _, level := range levels {
			if level != "none" {
				filtered = append(filtered, level)
			}
		}
		levels = filtered
	}
	return levels
}

// codexCompactBody is the compact document (codex.js:420-427): the model, the
// input and the instructions, and nothing else. The compact endpoint answers
// JSON, so it is not forced to stream (:281).
func codexCompactBody(protocol string, body Body) bool {
	if protocol != openaiadapter.ResponsesProtocol {
		return false
	}
	changed := body.keepOnly(map[string]bool{"model": true, "input": true, "instructions": true})
	if model, ok := body.text("model"); ok {
		if mapped := codexprovider.UpstreamModel(model); mapped != model && body.set("model", mapped) {
			changed = true
		}
	}
	return changed
}

// codexCacheKey names the prompt cache when the client named none (codex.js:455-458).
// The pin falls back to the connection's own id; Routeweft sends a digest of it,
// so a connection id is not an identifier the endpoint sees.
func codexCacheKey(provider routing.ProviderRef, body Body) bool {
	if provider.Protocol != openaiadapter.ResponsesProtocol || !body.absent("prompt_cache_key") {
		return false
	}
	key := connectionSession(provider)
	if key == "" {
		return false
	}
	return body.set("prompt_cache_key", key)
}

// connectionSession is a stable, opaque session id for a connection, empty when
// the request has none.
func connectionSession(provider routing.ProviderRef) string {
	if provider.ConnectionID == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(provider.ProviderID + "\x00" + provider.ConnectionID))
	return hex.EncodeToString(sum[:16])
}

// codexSession is the request-side identity: the session_id header (codex.js:226),
// which is the prompt cache key the request carries, else the connection's
// session, else "default" (it reads the final body, so it names the same session
// the cache key does), and, on the streaming endpoint, the event-stream Accept the
// forced stream needs whatever the client asked for (:76 via BaseExecutor).
func codexSession(provider routing.ProviderRef, body []byte, target string) (map[string]string, error) {
	session := ""
	var parsed Body
	if json.Unmarshal(body, &parsed) == nil {
		session, _ = parsed.text("prompt_cache_key")
	}
	if session == "" {
		session = connectionSession(provider)
	}
	if session == "" {
		session = "default"
	}
	headers := map[string]string{"session_id": session}
	if !isCompactEndpoint(strings.SplitN(target, "?", 2)[0]) {
		headers["Accept"] = "text/event-stream"
	}
	return headers, nil
}
