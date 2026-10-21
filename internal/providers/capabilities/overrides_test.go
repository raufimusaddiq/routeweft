package capabilities

import "testing"

func TestOverrideFrozenAndWins(t *testing.T) {
	old := New()
	correction := defaults
	correction.MaxOutput = 123
	overrides["codex"] = map[string]Capabilities{"gpt-6-astra": correction}
	defer delete(overrides, "codex")
	current := New()
	delete(overrides, "codex")
	if current.Resolve("codex", "vendor/gpt-6-astra").MaxOutput != 123 {
		t.Fatal("correction lost")
	}
	if old.Resolve("codex", "gpt-6-astra").MaxOutput != 128000 {
		t.Fatal("existing catalog mutated")
	}
}
