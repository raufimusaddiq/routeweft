package ingress

import (
	"context"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func comboSnapshot(t *testing.T) *runtime.RuntimeSnapshot {
	t.Helper()
	manager := newManager(t)
	ctx := context.Background()
	for _, model := range []runtime.Model{
		{ProviderID: "openai", ID: "text"},
		{ProviderID: "openai", ID: "vision", Capabilities: []string{"vision"}},
		{ProviderID: "anthropic", ID: "claude"},
	} {
		if err := manager.PutModel(ctx, model); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := manager.SetCombos(ctx, []runtime.Combo{{ID: "c1", Name: "auto", Strategy: "sticky-round-robin", StickyLimit: 2, Members: []runtime.ComboMember{
		{ProviderID: "openai", ModelID: "text", Position: 0, Selected: true},
		{ProviderID: "anthropic", ModelID: "claude", Position: 1, Selected: true},
		{ProviderID: "openai", ModelID: "vision", Position: 2, Selected: true},
	}}}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestPlanComboReorderAndDeselect(t *testing.T) {
	snapshot := comboSnapshot(t)
	handler := New(nil, Options{State: runtime.NewState()})
	ordered, ok := handler.PlanCombo(snapshot, "auto", []routing.CapabilityRequirement{{Name: "vision"}})
	if !ok || len(ordered) != 3 {
		t.Fatalf("ordered=%+v ok=%v", ordered, ok)
	}
	if ordered[0].ModelID != "vision" || ordered[1].ModelID != "text" || ordered[2].ModelID != "claude" {
		t.Fatalf("capability reorder failed: %+v", ordered)
	}
}

func TestPlanComboAdapterPrependWhenNoCandidateCapable(t *testing.T) {
	snapshot := comboSnapshot(t)
	handler := New(nil, Options{State: runtime.NewState()})
	ordered, ok := handler.PlanCombo(snapshot, "auto", []routing.CapabilityRequirement{{Name: "audio-input", AdapterEnabled: true, AdapterPool: []routing.Member{{ProviderID: "openai", ModelID: "vision", Capabilities: []string{"audio-input"}}}}})
	if !ok || len(ordered) != 4 {
		t.Fatalf("ordered=%+v", ordered)
	}
	if ordered[0].ModelID != "vision" {
		t.Fatalf("adapter not prepended: %+v", ordered)
	}
	empty, ok := handler.PlanCombo(snapshot, "auto", []routing.CapabilityRequirement{{Name: "audio-input", AdapterEnabled: true}})
	if !ok || len(empty) != 3 {
		t.Fatalf("empty pool must be no-op: %+v", empty)
	}
}

func TestPlanComboStickyRoundRobinRotates(t *testing.T) {
	snapshot := comboSnapshot(t)
	handler := New(nil, Options{State: runtime.NewState()})
	first, _ := handler.PlanCombo(snapshot, "auto", nil)
	second, _ := handler.PlanCombo(snapshot, "auto", nil)
	third, _ := handler.PlanCombo(snapshot, "auto", nil)
	if first[0].ModelID != "text" || second[0].ModelID != "text" || third[0].ModelID != "claude" {
		t.Fatalf("sticky rotation wrong first=%+v second=%+v third=%+v", first, second, third)
	}
	if _, ok := handler.PlanCombo(snapshot, "missing", nil); ok {
		t.Fatal("unknown combo resolved")
	}
}
