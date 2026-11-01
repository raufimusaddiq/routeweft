package runtime

import "testing"

func TestSnapshotCapabilitiesResolveAlias(t *testing.T) {
	s, err := (Compiler{}).Compile(Config{
		Models: []Model{{ProviderID: "anthropic", ID: "claude-opus-4.8"}, {ProviderID: "kimi", ID: "kimi-k3"}},
		Aliases: map[string]ModelRef{
			"opus":      {ProviderID: "anthropic", ModelID: "claude-opus-4.8"},
			"fast-kimi": {ProviderID: "kmc", ModelID: "kimi-k3"},
		},
	}, 1)
	if err != nil {
		t.Fatal(err)
	}
	caps := s.ModelCapabilityDetails("", "opus")
	if caps.MaxOutput != 128000 {
		t.Fatalf("alias caps: %+v", caps)
	}
	caps.MaxOutput = 1
	if s.ModelCapabilityDetails("", "opus").MaxOutput != 128000 {
		t.Fatal("snapshot mutated")
	}
	// The capability pair resolves alias prefixes the same way serving does.
	if got, want := s.ModelCapabilityDetails("", "fast-kimi"), s.ModelCapabilityDetails("kimi", "kimi-k3"); got != want {
		t.Fatalf("alias target caps = %+v, want %+v", got, want)
	}
}
