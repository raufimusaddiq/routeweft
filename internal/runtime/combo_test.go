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
	restarted, err := NewManager(ctx, reopened.DB())
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
