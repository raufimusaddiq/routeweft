package registry

// upstreamModelIDs maps a catalog model id to the id the provider's API accepts,
// for the providers whose registry rows carry `upstreamModelId` and whose
// rewrite is a plain replacement of the whole id (open-sse/config/providerModels.js:
// getModelUpstreamId, called for every request at open-sse/handlers/chatCore.js:126).
//
// The catalog id stays what the operator selects, aliases and bills; only the
// outbound `model` changes. Rewrites that are not a plain replacement are not here
// because a hook owns them: Codex review twins, Grok CLI effort suffixes, the
// DeepSeek `-max`/`-none` thinking aliases, Kiro `-thinking`/`-agentic`
// variants and MiMo Preview account ids read the suffix, so replacing the id
// before the hook would erase what it reads. The oracle-gated test
// parity.TestEveryOracleUpstreamModelIDIsAccountedFor holds this table, the hook
// set and the still-open set against the pinned registry.
var upstreamModelIDs = map[string]map[string]string{
	"blackbox": {
		"claude-fable-5":    "blackboxai/anthropic/claude-fable-5",
		"claude-opus-4.8":   "blackboxai/anthropic/claude-opus-4.8",
		"claude-sonnet-4.6": "blackboxai/anthropic/claude-sonnet-4.6",
		"gpt-5.5":           "blackboxai/openai/gpt-5.5",
		"gpt-5.4-pro":       "blackboxai/openai/gpt-5.4-pro",
		"gpt-5.4":           "blackboxai/openai/gpt-5.4",
		"gpt-5.3-codex":     "blackboxai/openai/gpt-5.3-codex",
		"gpt-5.4-nano":      "blackboxai/openai/gpt-5.4-nano",
		"deepseek-v4-flash": "blackboxai/deepseek/deepseek-v4-flash",
		"grok-4.3":          "blackboxai/x-ai/grok-4.3",
	},
	// DeepSeek serves the id only as "deepseek-flash" and answers 400 to the
	// catalog's "deepseek-v4.1-flash" (registry/deepseek.js, pin note #4940).
	"deepseek": {
		"deepseek-v4.1-flash": "deepseek-flash",
	},
}

// UpstreamModelID returns the id to send upstream for a built-in provider's
// catalog model, or the model itself when the provider has no rewrite for it.
// An empty provider (a custom node) never rewrites.
func UpstreamModelID(providerID, model string) string {
	if upstream, ok := upstreamModelIDs[providerID][model]; ok {
		return upstream
	}
	return model
}

// UpstreamModelIDs returns a copy of the rewrite table for tests and tooling.
func UpstreamModelIDs() map[string]map[string]string {
	out := make(map[string]map[string]string, len(upstreamModelIDs))
	for provider, models := range upstreamModelIDs {
		copied := make(map[string]string, len(models))
		for model, upstream := range models {
			copied[model] = upstream
		}
		out[provider] = copied
	}
	return out
}
