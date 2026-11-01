package registry

import "testing"

func TestUpstreamModelIDRewritesOnlyTheTabledPairs(t *testing.T) {
	for _, tc := range []struct{ provider, model, want string }{
		{"blackbox", "gpt-5.5", "blackboxai/openai/gpt-5.5"},
		{"deepseek", "deepseek-v4.1-flash", "deepseek-flash"},
		{"deepseek", "deepseek-flash", "deepseek-flash"},
		{"openrouter", "gpt-5.5", "gpt-5.5"},
		{"", "gpt-5.5", "gpt-5.5"},
	} {
		if got := UpstreamModelID(tc.provider, tc.model); got != tc.want {
			t.Errorf("%s/%s = %q, want %q", tc.provider, tc.model, got, tc.want)
		}
	}
}

func TestUpstreamModelIDsAreSeededModelsOfBuiltins(t *testing.T) {
	builtin := map[string]Spec{}
	for _, spec := range Builtins() {
		builtin[spec.ID] = spec
	}
	for provider, models := range UpstreamModelIDs() {
		spec, ok := builtin[provider]
		if !ok {
			t.Fatalf("%s is not a built-in provider", provider)
		}
		seeded := map[string]bool{}
		for _, id := range spec.StaticModels() {
			seeded[id] = true
		}
		for model, upstream := range models {
			if !seeded[model] || upstream == "" || upstream == model {
				t.Errorf("%s/%s -> %q is not a rewrite of a seeded model", provider, model, upstream)
			}
		}
	}
}
