package runtime

import (
	"context"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

func comboModels(t *testing.T, manager *Manager) {
	t.Helper()
	ctx := context.Background()
	for _, model := range []Model{
		{ProviderID: "openai", ID: "gpt-vision", Capabilities: []string{"vision"}},
		{ProviderID: "openai", ID: "gpt-text"},
		{ProviderID: "anthropic", ID: "claude-text"},
	} {
		if err := manager.PutModel(ctx, model); err != nil {
			t.Fatal(err)
		}
	}
}

func TestComboCRUDPersistsOrderAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	manager, store := newTestManager(t)
	comboModels(t, manager)
	if _, err := manager.SetCombos(ctx, []Combo{{ID: "combo-1", Name: "fast", Strategy: "sticky-round-robin", StickyLimit: 4, Members: []ComboMember{
		{ProviderID: "openai", ModelID: "gpt-vision", Position: 1, Selected: true},
		{ProviderID: "anthropic", ModelID: "claude-text", Position: 0, Selected: true},
		{ProviderID: "openai", ModelID: "gpt-text", Position: 2},
	}}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	combo, ok := loaded.ComboByName("fast")
	if !ok || len(combo.Members) != 3 {
		t.Fatalf("combo=%+v ok=%v", combo, ok)
	}
	if resolved := combo.Resolve(); len(resolved) != 2 || resolved[0].Position != 1 {
		t.Fatalf("resolve=%+v", resolved)
	}
	if got := loaded.ComboStrategy("fast"); got != "sticky-round-robin" {
		t.Fatalf("strategy=%q", got)
	}
	if got := loaded.ComboStickyLimit("fast"); got != 4 {
		t.Fatalf("sticky limit=%d", got)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(ctx, store.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted, err := NewManagerObserved(ctx, reopened.DB(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	after, err := restarted.Load()
	if err != nil {
		t.Fatal(err)
	}
	restored, ok := after.ComboByName("fast")
	if !ok {
		t.Fatal("combo missing after restart")
	}
	if got := restored.Members; len(got) != 3 || got[0].ModelID != "claude-text" || got[2].ProviderID != "openai" {
		t.Fatalf("member order not preserved: %+v", got)
	}
}

func TestComboPutDeleteAndStrategyOverride(t *testing.T) {
	ctx := context.Background()
	manager, store := newTestManager(t)
	defer store.Close()
	comboModels(t, manager)
	if _, err := manager.PutCombo(ctx, Combo{Name: "auto", Members: []ComboMember{{ProviderID: "openai", ModelID: "gpt-text"}}}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	combo, ok := snapshot.ComboByName("auto")
	if !ok || combo.ID == "" {
		t.Fatalf("combo id not generated: %+v", combo)
	}
	if got := snapshot.ComboStrategy("auto"); got != "fallback" {
		t.Fatalf("default strategy=%q", got)
	}
	if _, err := manager.Update(ctx, func(c *Candidate) error {
		c.Set("comboStrategies", `{"auto":"round-robin"}`)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err = manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.ComboStrategy("auto"); got != "round-robin" {
		t.Fatalf("override strategy=%q", got)
	}
	if _, err := manager.DeleteCombo(ctx, "auto"); err != nil {
		t.Fatal(err)
	}
	snapshot, err = manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snapshot.ComboByName("auto"); ok {
		t.Fatal("combo not deleted")
	}
	var count int
	if err := store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM combo_models WHERE combo_id=?", combo.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("orphaned members=%d", count)
	}
}

func TestCompileRejectsUnknownComboMember(t *testing.T) {
	manager, store := newTestManager(t)
	defer store.Close()
	comboModels(t, manager)
	if _, err := manager.SetCombos(context.Background(), []Combo{{ID: "c", Name: "bad", Members: []ComboMember{{ProviderID: "openai", ModelID: "missing"}}}}); err == nil {
		t.Fatal("unknown member compiled")
	}
}

// Aliases are usable as Combo members and resolve to their target provider/model
// so capability lookups and dispatch work uniformly (PRD-COMBO-001).
func TestCompileResolvesAliasComboMember(t *testing.T) {
	manager, store := newTestManager(t)
	defer store.Close()
	ctx := context.Background()
	comboModels(t, manager)
	if err := manager.PutAlias(ctx, "smart", ModelRef{ProviderID: "openai", ModelID: "gpt-vision"}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.SetCombos(ctx, []Combo{{ID: "c", Name: "alias-combo", Members: []ComboMember{{ProviderID: "openai", ModelID: "smart", Selected: true}}}})
	if err != nil {
		t.Fatalf("alias member rejected: %v", err)
	}
	combo, ok := snapshot.ComboByName("alias-combo")
	if !ok || len(combo.Members) != 1 {
		t.Fatalf("combo=%+v ok=%v", combo, ok)
	}
	if combo.Members[0].ProviderID != "openai" || combo.Members[0].ModelID != "gpt-vision" {
		t.Fatalf("alias not canonicalized: %+v", combo.Members[0])
	}
	capabilities, _, ok := snapshot.ModelCapabilities("openai", "gpt-vision")
	if !ok || len(capabilities) == 0 || capabilities[0] != "vision" {
		t.Fatalf("alias capability lookup failed: %v ok=%v", capabilities, ok)
	}
}

// An update matches by ID only, so renaming cannot clash with another Combo;
// creating a second Combo with an existing name is rejected.
func TestPutComboRenameAndDuplicateName(t *testing.T) {
	manager, store := newTestManager(t)
	defer store.Close()
	ctx := context.Background()
	comboModels(t, manager)
	first, err := manager.PutCombo(ctx, Combo{ID: "c1", Name: "alpha", Members: []ComboMember{{ProviderID: "openai", ModelID: "gpt-text", Selected: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.PutCombo(ctx, Combo{ID: "c2", Name: "beta", Members: []ComboMember{{ProviderID: "openai", ModelID: "gpt-text", Selected: true}}}); err != nil {
		t.Fatal(err)
	}
	_ = first
	// Renaming by ID updates that record in place (no new entry created).
	renamed, err := manager.PutCombo(ctx, Combo{ID: "c1", Name: "gamma", Members: []ComboMember{{ProviderID: "openai", ModelID: "gpt-text", Selected: true}}})
	if err != nil {
		t.Fatalf("rename failed: %v", err)
	}
	if len(renamed.Combos()) != 2 {
		t.Fatalf("combos=%d want 2", len(renamed.Combos()))
	}
	// A fresh create reusing a taken name is rejected.
	if _, err := manager.PutCombo(ctx, Combo{Name: "beta", Members: []ComboMember{{ProviderID: "openai", ModelID: "gpt-text", Selected: true}}}); err == nil {
		t.Fatal("duplicate name accepted on create")
	}
	// Renaming onto a name another Combo already owns is rejected rather than
	// silently creating two entries with one name.
	if _, err := manager.PutCombo(ctx, Combo{ID: "c1", Name: "beta", Members: []ComboMember{{ProviderID: "openai", ModelID: "gpt-text", Selected: true}}}); err == nil {
		t.Fatal("rename onto an existing name accepted")
	}
}

// A Combo may name another Combo as a member (open-sse chat.js:177-229). The
// member has no provider; it survives storage and restart and Resolve keeps it.
func TestNestedComboMembersPersistAndResolve(t *testing.T) {
	ctx := context.Background()
	manager, store := newTestManager(t)
	defer store.Close()
	comboModels(t, manager)
	if _, err := manager.SetCombos(ctx, []Combo{
		{ID: "outer", Name: "outer", Members: []ComboMember{{ModelID: "inner", Position: 0, Selected: true}, {ProviderID: "openai", ModelID: "gpt-text", Position: 1, Selected: true}}},
		{ID: "inner", Name: "inner", Members: []ComboMember{{ProviderID: "anthropic", ModelID: "claude-text", Position: 0, Selected: true}}},
	}); err != nil {
		t.Fatal(err)
	}
	loaded, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	outer, _ := loaded.ComboByName("outer")
	members := outer.Resolve()
	if len(members) != 2 || !members[0].IsCombo() || members[0].ModelID != "inner" || members[1].IsCombo() {
		t.Fatalf("members %+v", members)
	}
	// An alias wins over a Combo of the same name, as getModelInfo precedes the
	// Combo lookup in the oracle.
	if err := manager.PutAlias(ctx, "inner2", ModelRef{ProviderID: "openai", ModelID: "gpt-text"}); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetCombos(ctx, []Combo{{ID: "a", Name: "a", Members: []ComboMember{{ModelID: "inner2", Position: 0, Selected: true}}}, {ID: "inner2", Name: "inner2", Members: []ComboMember{{ProviderID: "anthropic", ModelID: "claude-text", Position: 0, Selected: true}}}}); err != nil {
		t.Fatal(err)
	}
	loaded, _ = manager.Load()
	aliased, _ := loaded.ComboByName("a")
	if member := aliased.Members[0]; member.IsCombo() || member.ProviderID != "openai" {
		t.Fatalf("an alias must resolve before a Combo name: %+v", member)
	}
}

// The v1 importer turns a bare name into a provider-less member; a Combo that
// names another Combo is stored once that one is, whatever order they came in.
func TestV1ImportStoresNestedCombosInAnyOrder(t *testing.T) {
	ctx := context.Background()
	manager, store := newTestManager(t)
	defer store.Close()
	comboModels(t, manager)
	plan := &V1Plan{Combos: []Combo{
		{ID: "v1-outer", Name: "outer", Members: []ComboMember{{ModelID: "inner", Position: 0, Selected: true}}},
		{ID: "v1-inner", Name: "inner", Members: []ComboMember{{ProviderID: "openai", ModelID: "gpt-text", Position: 0, Selected: true}}},
	}}
	if failed := plan.ApplyCatalog(ctx, manager); len(failed) != 0 {
		t.Fatalf("failed=%+v", failed)
	}
	snapshot, _ := manager.Load()
	if outer, ok := snapshot.ComboByName("outer"); !ok || !outer.Members[0].IsCombo() {
		t.Fatalf("outer=%+v", outer)
	}
	bad := &V1Plan{Combos: []Combo{{ID: "v1-x", Name: "x", Members: []ComboMember{{ModelID: "nobody", Position: 0, Selected: true}}}}}
	if failed := bad.ApplyCatalog(ctx, manager); len(failed) != 1 {
		t.Fatalf("a name that is neither an alias nor a Combo must fail: %+v", failed)
	}
}
