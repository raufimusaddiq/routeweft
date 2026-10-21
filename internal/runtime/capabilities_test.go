package runtime

import "testing"

func TestSnapshotCapabilitiesResolveAlias(t *testing.T) {
	s, err := (Compiler{}).Compile(Config{
		Models:  []Model{{ProviderID: "anthropic", ID: "claude-opus-4.8"}},
		Aliases: map[string]ModelRef{"opus": {ProviderID: "anthropic", ModelID: "claude-opus-4.8"}},
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
}
