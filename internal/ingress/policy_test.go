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
		{ProviderID: "adapter", ID: "audio", Capabilities: []string{"audio-input"}},
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
	ordered, ok := handler.PlanCombo(snapshot, "auto", []routing.CapabilityRequirement{{Name: "audio-input", AdapterEnabled: true, AdapterPool: []routing.Member{{ProviderID: "adapter", ModelID: "audio", Capabilities: []string{"audio-input"}}}}})
	if !ok || len(ordered) != 4 {
		t.Fatalf("ordered=%+v", ordered)
	}
	if ordered[0].ModelID != "audio" {
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

func TestPlanComboAdaptersStayAheadAndRotateIndependently(t *testing.T) {
	snapshot := comboSnapshot(t)
	ctx := context.Background()
	manager := newManager(t)
	for _, model := range []runtime.Model{
		{ProviderID: "openai", ID: "text"},
		{ProviderID: "openai", ID: "vision", Capabilities: []string{"vision"}},
		{ProviderID: "adapter", ID: "vision-audio", Capabilities: []string{"audio-input"}},
		{ProviderID: "adapter", ID: "text-audio", Capabilities: []string{"audio-input"}},
	} {
		if err := manager.PutModel(ctx, model); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := manager.SetCombos(ctx, []runtime.Combo{{ID: "c2", Name: "adapter", Members: []runtime.ComboMember{
		{ProviderID: "openai", ModelID: "vision", Position: 0, Selected: true},
		{ProviderID: "openai", ModelID: "text", Position: 1, Selected: true},
	}}}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	state := runtime.NewState()
	handler := New(nil, Options{State: state})
	requirement := routing.CapabilityRequirement{Name: "audio-input", Strategy: routing.StrategyRoundRobin, AdapterEnabled: true, AdapterPool: []routing.Member{
		{ProviderID: "adapter", ModelID: "vision-audio", Position: 0},
		{ProviderID: "adapter", ModelID: "text-audio", Position: 1},
	}}
	// Adapters lack the capability so the pool stays a no-op until members carry it.
	requirement.AdapterPool[0].Capabilities = []string{"audio-input"}
	requirement.AdapterPool[1].Capabilities = []string{"audio-input"}
	first, ok := handler.PlanCombo(snapshot, "adapter", []routing.CapabilityRequirement{requirement})
	if !ok || len(first) != 4 {
		t.Fatalf("first=%+v", first)
	}
	if first[0].ModelID != "vision-audio" || first[1].ModelID != "text-audio" {
		t.Fatalf("adapter tier not first or misordered: %+v", first)
	}
	second, _ := handler.PlanCombo(snapshot, "adapter", []routing.CapabilityRequirement{requirement})
	if second[0].ModelID != "text-audio" || second[1].ModelID != "vision-audio" {
		t.Fatalf("adapter rotation wrong: %+v", second)
	}
	if second[2].ModelID != "vision" || second[3].ModelID != "text" {
		t.Fatalf("original tier changed: %+v", second)
	}
}

func TestTrimComboHistoryForTargetUsesSmallerAdapterContext(t *testing.T) {
	handler := New(nil, Options{})
	messages := []string{"sys", "old1", "old2", "old3", "user"}
	trimmed := handler.TrimComboHistoryForTarget(messages, routing.ContextBudget{Head: 1, Tail: 1, Limit: len(messages)}, 3)
	if len(trimmed) != 3 || trimmed[0] != "sys" || trimmed[len(trimmed)-1] != "user" {
		t.Fatalf("trim=%v", trimmed)
	}
	// A larger adapter context than the budget does not expand history.
	kept := handler.TrimComboHistoryForTarget(messages, routing.ContextBudget{Head: 1, Tail: 1, Limit: 3}, 100)
	if len(kept) != 3 {
		t.Fatalf("kept=%v", kept)
	}
}

func TestPlanComboRequestTrimsHistoryForSmallerAdapter(t *testing.T) {
	snapshot := comboSnapshot(t)
	state := runtime.NewState()
	handler := New(nil, Options{State: state})
	messages := []string{"sys", "old1", "old2", "old3", "user"}
	requirement := routing.CapabilityRequirement{
		Name:                 "audio-input",
		AdapterEnabled:       true,
		AdapterContextWindow: 3,
		AdapterPool:          []routing.Member{{ProviderID: "adapter", ModelID: "audio", Capabilities: []string{"audio-input"}}},
	}
	ordered, trimmed, ok := handler.PlanComboRequest(snapshot, "auto", []routing.CapabilityRequirement{requirement}, messages, routing.ContextBudget{Head: 1, Tail: 1})
	if !ok || len(ordered) != 4 {
		t.Fatalf("ordered=%+v ok=%v", ordered, ok)
	}
	if len(trimmed) != 3 || trimmed[0] != "sys" || trimmed[len(trimmed)-1] != "user" {
		t.Fatalf("trimmed=%v", trimmed)
	}
	// Without an adapter requirement the original history is preserved.
	_, untouched, _ := handler.PlanComboRequest(snapshot, "auto", nil, messages, routing.ContextBudget{Head: 1, Tail: 1})
	if len(untouched) != len(messages) {
		t.Fatalf("untouched=%v", untouched)
	}
}

func TestPlanComboDedupesAdapterAndSelectsPerCapability(t *testing.T) {
	manager := newManager(t)
	ctx := context.Background()
	for _, model := range []runtime.Model{
		{ProviderID: "openai", ID: "text"},
		{ProviderID: "openai", ID: "vision", Capabilities: []string{"vision"}},
		{ProviderID: "adapter", ID: "audio", Capabilities: []string{"audio-input"}},
	} {
		if err := manager.PutModel(ctx, model); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := manager.SetCombos(ctx, []runtime.Combo{{ID: "c3", Name: "multi", Members: []runtime.ComboMember{
		{ProviderID: "openai", ModelID: "vision", Position: 0, Selected: true},
		{ProviderID: "openai", ModelID: "text", Position: 1, Selected: true},
	}}}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	handler := New(nil, Options{State: runtime.NewState()})
	// vision is already a capable Combo member, so no adapter is needed for it;
	// audio-input is unmet and pulls exactly one adapter candidate.
	ordered, ok := handler.PlanCombo(snapshot, "multi", []routing.CapabilityRequirement{
		{Name: "vision"},
		{Name: "audio-input", AdapterEnabled: true, AdapterPool: []routing.Member{{ProviderID: "adapter", ModelID: "audio", Capabilities: []string{"audio-input"}}}},
	})
	if !ok || len(ordered) != 3 {
		t.Fatalf("ordered=%+v", ordered)
	}
	seen := map[string]int{}
	for _, member := range ordered {
		seen[member.ProviderID+"/"+member.ModelID]++
	}
	for key, count := range seen {
		if count > 1 {
			t.Fatalf("duplicate candidate %s in %+v", key, ordered)
		}
	}
	if ordered[0].ModelID != "audio" {
		t.Fatalf("adapter for the unmet capability should lead: %+v", ordered)
	}
}
