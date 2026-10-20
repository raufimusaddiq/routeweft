package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

// TestComboFusionTuningPersistsAndResolves covers the C06 Fusion mapping end to
// end: stored per Combo, survives restart, a Combo without tuning keeps the
// pinned v1 defaults, and a tuned Combo resolves over them.
func TestComboFusionTuningPersistsAndResolves(t *testing.T) {
	ctx := context.Background()
	manager, store := newTestManager(t)
	comboModels(t, manager)
	minPanel, grace, hard := 3, int64(1500), int64(20000)
	members := []ComboMember{
		{ProviderID: "openai", ModelID: "gpt-vision", Position: 0, Selected: true},
		{ProviderID: "openai", ModelID: "gpt-text", Position: 1, Selected: true},
		{ProviderID: "anthropic", ModelID: "claude-text", Position: 2, Selected: true},
	}
	if _, err := manager.SetCombos(ctx, []Combo{
		{ID: "tuned", Name: "tuned", FusionEnabled: true, FusionTuning: routing.FusionTuning{MinPanel: &minPanel, StragglerGraceMs: &grace, PanelHardTimeoutMs: &hard}, Members: members},
		{ID: "plain", Name: "plain", FusionEnabled: true, Members: members},
	}); err != nil {
		t.Fatal(err)
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
	snapshot, err := restarted.Load()
	if err != nil {
		t.Fatal(err)
	}
	tuned, _ := snapshot.ComboByName("tuned")
	config, err := tuned.FusionConfig(routing.DefaultFusionConfig())
	if err != nil || config.MinPanelQuorum != 3 || config.Grace != 1500*time.Millisecond || config.HardTimeout != 20*time.Second {
		t.Fatalf("tuned config %+v err=%v", config, err)
	}
	plain, _ := snapshot.ComboByName("plain")
	if !plain.FusionTuning.IsZero() {
		t.Fatalf("untuned Combo grew tuning: %+v", plain.FusionTuning)
	}
	config, err = plain.FusionConfig(routing.DefaultFusionConfig())
	if err != nil || config.MinPanelQuorum != 2 || config.Grace != 8*time.Second || config.HardTimeout != 90*time.Second {
		t.Fatalf("plain config %+v err=%v", config, err)
	}
}

func TestComboFusionTuningInvalidIsRefusedBeforeCommit(t *testing.T) {
	ctx := context.Background()
	manager, store := newTestManager(t)
	defer store.Close()
	comboModels(t, manager)
	zero := int64(0)
	_, err := manager.SetCombos(ctx, []Combo{{ID: "bad", Name: "bad", FusionTuning: routing.FusionTuning{PanelHardTimeoutMs: &zero}, Members: []ComboMember{{ProviderID: "openai", ModelID: "gpt-text", Selected: true}}}})
	if err == nil {
		t.Fatal("zero hard timeout accepted")
	}
	snapshot, loadErr := manager.Load()
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if _, ok := snapshot.ComboByName("bad"); ok {
		t.Fatal("rejected Combo was published")
	}
}
