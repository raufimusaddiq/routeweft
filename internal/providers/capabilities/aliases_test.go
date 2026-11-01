package capabilities

import "testing"

// ProviderAlias is single-hop by construction; a chained entry would resolve a
// prefix to another alias and silently stop there. Keep the generated table free
// of chains and of targets that are not a provider identity.
func TestAliasTableIsSingleHopAndComplete(t *testing.T) {
	for alias, id := range aliases {
		if canonical, chained := aliases[id]; chained && canonical != id {
			t.Fatalf("alias %q -> %q -> %q is chained", alias, id, canonical)
		}
		if _, identity := aliases[id]; !identity {
			t.Fatalf("alias %q resolves to %q, which is not a provider identity", alias, id)
		}
	}
}
