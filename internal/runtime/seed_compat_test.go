package runtime

import (
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

// TestUpgradeKeepsPreviouslyShippedSeedModels proves an existing install still
// boots: an alias or Combo member that pointed at a seed ID the previous release
// shipped must resolve, even if the reference no longer lists it. Regenerating
// seeds from the oracle alone dropped six such IDs and made the config fail to
// compile, which is a startup failure, not a routing gap.
func TestUpgradeKeepsPreviouslyShippedSeedModels(t *testing.T) {
	// The IDs main shipped that the oracle no longer does.
	previouslyShipped := map[string]string{
		"codex":       "gpt-5.5",
		"antigravity": "gemini-3.7-flash",
		"kiro":        "claude-sonnet-4.6",
	}
	for providerID, modelID := range previouslyShipped {
		spec, ok := lookupBuiltin(providerID)
		if !ok {
			t.Fatalf("%s missing from the catalog", providerID)
		}
		found := false
		for _, seed := range spec.SeedModels {
			if seed.ID == modelID {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("%s/%s was routable before and is no longer seeded", providerID, modelID)
		}
	}

	// A config that references them must compile, not error.
	config := Config{
		// Both providers are configured, as they would be on the install that has
		// the alias and the Combo.
		Providers: credentials.Configuration{Nodes: []credentials.Node{
			{ID: "n1", Kind: credentials.NodeBuiltin, ProviderID: "codex", Name: "codex"},
			{ID: "n2", Kind: credentials.NodeBuiltin, ProviderID: "antigravity", Name: "antigravity"},
		}},
		Models:  []Model{{ProviderID: "codex", ID: "gpt-5.5"}},
		Aliases: map[string]ModelRef{"sol": {ProviderID: "codex", ModelID: "gpt-5.5"}},
		Combos: []Combo{{
			ID: "c1", Name: "c1", Strategy: "fallback",
			Members: []ComboMember{{ProviderID: "antigravity", ModelID: "gemini-3.7-flash"}},
		}},
	}
	if _, err := (Compiler{}).Compile(config, 1); err != nil {
		t.Fatalf("an existing install would not boot: %v", err)
	}
}

// lookupBuiltin finds one built-in provider identity.
func lookupBuiltin(id string) (registry.Spec, bool) {
	for _, spec := range registry.Builtins() {
		if spec.ID == id {
			return spec, true
		}
	}
	return registry.Spec{}, false
}
