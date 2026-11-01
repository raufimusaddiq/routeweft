package runtime

import (
	"context"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/auth"
)

// A restriction the store cannot be trusted to express must stop the catalog
// loading rather than widen: nothing here ever falls through to unrestricted.
func TestTamperedAccessRowsFailClosedOnLoad(t *testing.T) {
	for name, allow := range map[string]string{
		"not json":      `{{`,
		"wrong shape":   `{"a":1}`,
		"empty entry":   `["x",""]`,
		"non string":    `[1]`,
		"oversize item": `["` + strings.Repeat("a", 300) + `"]`,
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			manager, store := newTestManager(t)
			if _, _, err := manager.CreateAPIKeyWithAccess(ctx, "k", true, []string{"x"}); err != nil {
				t.Fatal(err)
			}
			if _, err := store.DB().ExecContext(ctx, "UPDATE api_keys SET access_allow=?", allow); err != nil {
				t.Fatal(err)
			}
			if _, err := NewManagerObserved(ctx, store.DB(), nil, nil); err == nil {
				t.Fatalf("allowlist %s loaded; want a refusal", allow)
			}
		})
	}
}

func TestMissingAccessColumnsMeanUnrestricted(t *testing.T) {
	ctx := context.Background()
	manager, store := newTestManager(t)
	entry, secret, err := manager.CreateAPIKey(ctx, "legacy")
	if err != nil {
		t.Fatal(err)
	}
	// A key written before the access columns existed carries their defaults.
	if _, err := store.DB().ExecContext(ctx, "UPDATE api_keys SET access_restricted=DEFAULT, access_allow=DEFAULT WHERE id=?", entry.ID); err != nil {
		// SQLite has no UPDATE ... DEFAULT; set the migration defaults explicitly.
		if _, err := store.DB().ExecContext(ctx, "UPDATE api_keys SET access_restricted=0, access_allow='[]' WHERE id=?", entry.ID); err != nil {
			t.Fatal(err)
		}
	}
	reloaded, err := NewManagerObserved(ctx, store.DB(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reloaded.Load()
	if err != nil {
		t.Fatal(err)
	}
	key, ok := snapshot.APIKeys().Lookup(secret)
	if !ok || key.Restricted || !snapshot.KeyAllows(key, "anything/at-all") {
		t.Fatalf("legacy key = %+v ok=%v; want unrestricted (pin KEY_ACCESS_UNRESTRICTED)", key, ok)
	}
}

// Grants match resolved identities, so a restricted key granted the canonical
// provider/model also reaches the registry alias prefix and the other way round.
func TestKeyGrantsSeeThroughProviderAliases(t *testing.T) {
	models, aliases, disabled := compileCatalog([]Model{{ProviderID: "kimi", ID: "kimi-k2"}}, nil, nil)
	s := &RuntimeSnapshot{models: models, aliases: aliases, disabledModels: disabled}
	for _, allow := range []string{"kimi/kimi-k2", "kmc/kimi-k2", "kimi-coding/kimi-k2"} {
		key := auth.Entry{Restricted: true, Allow: []string{allow}}
		if !s.KeyAllows(key, "kmc/kimi-k2") || !s.KeyAllows(key, "kimi-coding/kimi-k2") || !s.KeyAllows(key, "kimi/kimi-k2") {
			t.Errorf("grant %q did not reach every spelling of the same model", allow)
		}
		if s.KeyAllows(key, "antigravity/kimi-k2") {
			t.Errorf("grant %q reached another provider", allow)
		}
	}
}
