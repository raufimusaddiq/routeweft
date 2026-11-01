package parity

import (
	"regexp"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

// A registry row's `upstreamModelId` is the id the provider's API accepts; the
// reference sends it for every request (providerModels.js getModelUpstreamId,
// chatCore.js:126). Every such row of the pinned registry must be accounted for
// here: rewritten by registry.UpstreamModelID, owned by a wire hook that reads
// the id's suffix (so the id must reach the hook whole), or tracked as open.
// Nothing is allowed to be silently sent under the catalog id.
var hookOwnedUpstreamIDs = map[string]func(model string) bool{
	// wire/codex_body.go: the review twin is sent as its base model (TestCodexModelIsTheUpstreamOne).
	"codex": func(m string) bool { return strings.HasSuffix(m, "-review") },
	// wire/grokcli_body.go: the effort suffix becomes reasoning.effort (TestGrokCLIReasoning).
	"grok-cli": func(m string) bool { return strings.HasPrefix(m, "grok-4.5-") },
	// wire/sanitize.go: -max and -none are thinking settings on deepseek-v4-pro.
	"deepseek": func(m string) bool { return m == "deepseek-v4-pro-max" || m == "deepseek-v4-pro-none" },
	// wire/mimo_preview.go: Preview ids take the account route.
	"xiaomi-mimo": func(m string) bool { return m == "mimo-x-pro-preview" || m == "mimo-x-flash-preview" },
	// protocol/kiro/request.go: -thinking and -agentic are read from the routed id.
	"kiro": func(m string) bool {
		return strings.HasSuffix(m, "-thinking") || strings.HasSuffix(m, "-agentic") || strings.HasSuffix(m, "-thinking-agentic")
	},
}

// trackedUpstreamIDGaps are rewrites Routeweft does not perform. They only shrink.
var trackedUpstreamIDGaps = map[string]string{
	// The pin appends the thinking level to a tiered upstream id ("gemini-3.7-flash-tiered(high)")
	// which applyThinking then reads; Routeweft sends the catalog id. Needs the Antigravity
	// thinking path, not a plain rewrite (#76 P03, reported open).
	"antigravity/gemini-3.8-flash-high": "thinking-level upstream id", "antigravity/gemini-3.8-flash-medium": "thinking-level upstream id",
	"antigravity/gemini-3.8-flash-low": "thinking-level upstream id", "antigravity/gemini-3.8-flash": "thinking-level upstream id",
	"antigravity/gemini-3.7-flash-high": "thinking-level upstream id", "antigravity/gemini-3.7-flash-medium": "thinking-level upstream id",
	"antigravity/gemini-3.7-flash-low": "thinking-level upstream id", "antigravity/gemini-3.6-flash-high": "thinking-level upstream id",
	"antigravity/gemini-3.6-flash-medium": "thinking-level upstream id", "antigravity/gemini-3.6-flash-low": "thinking-level upstream id",
	// The row also carries targetFormat "claude" (a per-model transport), which Routeweft
	// has no field for; rewriting the id alone would send a Claude-format model on the Chat wire.
	"xiaomi-tokenplan/mimo-v2.5-pro-claude": "per-model targetFormat",
}

func TestEveryOracleUpstreamModelIDIsAccountedFor(t *testing.T) {
	o := pinnedOracle(t)
	active, _ := oracleProviders(t, o)
	index := o.file(t, registryDir+"/index.js")
	files := map[string]string{}
	for _, m := range regexp.MustCompile(`(?m)^import \w+ from "\./(.+)\.js";`).FindAllStringSubmatch(index, -1) {
		source := o.file(t, registryDir+"/"+m[1]+".js")
		if id := regexp.MustCompile(`(?m)^  id: "([^"]+)"`).FindStringSubmatch(source); id != nil {
			files[id[1]] = source
		}
	}
	row := regexp.MustCompile(`\{\s*id:\s*"([^"]+)"[^\n]*?upstreamModelId:\s*"([^"]+)"`)
	table := registry.UpstreamModelIDs()
	found := map[string]string{}
	seen := 0
	for provider := range active {
		for _, m := range row.FindAllStringSubmatch(files[provider], -1) {
			model, upstream := m[1], m[2]
			if model == upstream {
				continue
			}
			seen++
			switch {
			case table[provider][model] == upstream:
			case table[provider][model] != "":
				t.Errorf("%s/%s: Routeweft sends %q, the pin sends %q", provider, model, table[provider][model], upstream)
			case hookOwnedUpstreamIDs[provider] != nil && hookOwnedUpstreamIDs[provider](model):
			default:
				found[provider+"/"+model] = upstream
			}
		}
	}
	if seen < 30 {
		t.Fatalf("parsed only %d upstreamModelId rows; the registry parse is broken", seen)
	}
	ratchet(t, "upstream model ids", found, trackedUpstreamIDGaps)
	for provider, models := range table {
		for model := range models {
			if !row.MatchString(files[provider]) || !strings.Contains(files[provider], `id: "`+model+`"`) {
				t.Errorf("%s/%s is in the rewrite table but not in the pinned registry", provider, model)
			}
		}
	}
}
