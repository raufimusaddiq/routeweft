package ingress

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/auth"

	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func comboSnapshot(t *testing.T) *runtime.RuntimeSnapshot {
	t.Helper()
	return mustLoad(t, comboManager(t))
}

func comboManager(t *testing.T) *runtime.Manager {
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
	return manager
}

func TestPlanComboReorderAndDeselect(t *testing.T) {
	snapshot := comboSnapshot(t)
	handler := New(nil, Options{State: runtime.NewState()})
	ordered, ok := handler.PlanCombo(snapshot, auth.Entry{}, "auto", []string{"vision"})
	if !ok || len(ordered) != 3 {
		t.Fatalf("ordered=%+v ok=%v", ordered, ok)
	}
	// The list is rotated first (text, claude, vision), then the models that
	// satisfy the request float stably. Capabilities come from the oracle's own
	// table (capabilities.js), which marks Claude models vision-capable, plus the
	// vision a catalog entry declares; nothing is dropped.
	if ordered[0].ModelID != "claude" || ordered[1].ModelID != "vision" || ordered[2].ModelID != "text" {
		t.Fatalf("capability reorder failed: %+v", ordered)
	}
}

func TestPlanComboAdapterPrependWhenNoCandidateCapable(t *testing.T) {
	snapshot := withAdapterPools(t, comboManager(t), map[string]string{"capacityAdapterAudioInput": adapterPool(true, false, "adapter/audio")})
	handler := New(nil, Options{State: runtime.NewState()})
	ordered, ok := handler.PlanCombo(snapshot, auth.Entry{}, "auto", []string{"audio-input"})
	if !ok || len(ordered) != 4 {
		t.Fatalf("ordered=%+v", ordered)
	}
	if ordered[0].ModelID != "audio" {
		t.Fatalf("adapter not prepended: %+v", ordered)
	}
	// An enabled empty pool is a deliberate no-op (capacityAdapter.js:32-34).
	empty := withAdapterPools(t, comboManager(t), map[string]string{"capacityAdapterAudioInput": adapterPool(true, false)})
	noop, ok := handler.PlanCombo(empty, auth.Entry{}, "auto", []string{"audio-input"})
	if !ok || len(noop) != 3 {
		t.Fatalf("empty pool must be no-op: %+v", noop)
	}
	// A disabled pool is not consulted either.
	off := withAdapterPools(t, comboManager(t), map[string]string{"capacityAdapterAudioInput": adapterPool(false, false, "adapter/audio")})
	if disabled, _ := handler.PlanCombo(off, auth.Entry{}, "auto", []string{"audio-input"}); len(disabled) != 3 {
		t.Fatalf("disabled pool must be no-op: %+v", disabled)
	}
}

func TestPlanComboStickyRoundRobinRotates(t *testing.T) {
	snapshot := comboSnapshot(t)
	handler := New(nil, Options{State: runtime.NewState()})
	first, _ := handler.PlanCombo(snapshot, auth.Entry{}, "auto", nil)
	second, _ := handler.PlanCombo(snapshot, auth.Entry{}, "auto", nil)
	third, _ := handler.PlanCombo(snapshot, auth.Entry{}, "auto", nil)
	if first[0].ModelID != "text" || second[0].ModelID != "text" || third[0].ModelID != "claude" {
		t.Fatalf("sticky rotation wrong first=%+v second=%+v third=%+v", first, second, third)
	}
	if _, ok := handler.PlanCombo(snapshot, auth.Entry{}, "missing", nil); ok {
		t.Fatal("unknown combo resolved")
	}
}

// The oracle rotates the whole list, adapter models included, with the Combo's
// own strategy; a pool's roundRobin flag only matters for a single-model target
// (chat.js:113,138-149 vs 154-168; capacityAdapter.js:64-72).
func TestPlanComboAdapterModelsFollowTheComboStrategy(t *testing.T) {
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
	if _, err := manager.SetCombos(ctx, []runtime.Combo{
		{ID: "c2", Name: "adapter", Members: []runtime.ComboMember{
			{ProviderID: "openai", ModelID: "vision", Position: 0, Selected: true},
			{ProviderID: "openai", ModelID: "text", Position: 1, Selected: true},
		}},
		{ID: "c3", Name: "rotating", Strategy: "round-robin", Members: []runtime.ComboMember{
			{ProviderID: "openai", ModelID: "vision", Position: 0, Selected: true},
			{ProviderID: "openai", ModelID: "text", Position: 1, Selected: true},
		}},
	}); err != nil {
		t.Fatal(err)
	}
	snapshot := withAdapterPools(t, manager, map[string]string{"capacityAdapterAudioInput": adapterPool(true, true, "adapter/vision-audio", "adapter/text-audio")})
	handler := New(nil, Options{State: runtime.NewState()})
	ids := func(members []runtime.ComboMember) []string {
		out := make([]string, 0, len(members))
		for _, member := range members {
			out = append(out, member.ModelID)
		}
		return out
	}
	// Fill-first Combo: the pool's roundRobin flag does not rotate the adapters.
	for i := 0; i < 2; i++ {
		got, _ := handler.PlanCombo(snapshot, auth.Entry{}, "adapter", []string{"audio-input"})
		if want := "vision-audio text-audio vision text"; strings.Join(ids(got), " ") != want {
			t.Fatalf("call %d: %v, want %s", i, ids(got), want)
		}
	}
	// Round-robin Combo: rotation covers the adapter models, then the capable ones float.
	first, _ := handler.PlanCombo(snapshot, auth.Entry{}, "rotating", []string{"audio-input"})
	second, _ := handler.PlanCombo(snapshot, auth.Entry{}, "rotating", []string{"audio-input"})
	if strings.Join(ids(first), " ") != "vision-audio text-audio vision text" || strings.Join(ids(second), " ") != "text-audio vision-audio vision text" {
		t.Fatalf("first=%v second=%v", ids(first), ids(second))
	}
	// A single-model target uses the first required capability's pool strategy.
	soloFirst, ok := handler.PlanSolo(snapshot, auth.Entry{}, "openai", "text", []string{"audio-input"})
	soloSecond, _ := handler.PlanSolo(snapshot, auth.Entry{}, "openai", "text", []string{"audio-input"})
	if !ok || strings.Join(ids(soloFirst), " ") != "vision-audio text-audio text" || strings.Join(ids(soloSecond), " ") != "text-audio vision-audio text" {
		t.Fatalf("solo first=%v second=%v", ids(soloFirst), ids(soloSecond))
	}
	if _, ok := handler.PlanSolo(snapshot, auth.Entry{}, "openai", "text", nil); ok {
		t.Fatal("a request that needs nothing must not be rescued")
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

func TestPlanComboRequestTrimsHistoryToTheBudget(t *testing.T) {
	snapshot := withAdapterPools(t, comboManager(t), map[string]string{"capacityAdapterAudioInput": adapterPool(true, false, "adapter/audio")})
	handler := New(nil, Options{State: runtime.NewState()})
	messages := []string{"sys", "old1", "old2", "old3", "user"}
	ordered, trimmed, ok := handler.PlanComboRequest(snapshot, auth.Entry{}, "auto", []string{"audio-input"}, messages, routing.ContextBudget{Head: 1, Tail: 1, Limit: 3})
	if !ok || len(ordered) != 4 {
		t.Fatalf("ordered=%+v ok=%v", ordered, ok)
	}
	if len(trimmed) != 3 || trimmed[0] != "sys" || trimmed[len(trimmed)-1] != "user" {
		t.Fatalf("trimmed=%v", trimmed)
	}
	// Without a requirement the original history is preserved.
	_, untouched, _ := handler.PlanComboRequest(snapshot, auth.Entry{}, "auto", nil, messages, routing.ContextBudget{Head: 1, Tail: 1})
	if len(untouched) != len(messages) {
		t.Fatalf("untouched=%v", untouched)
	}
}

// The adapter test is "does any original model satisfy EVERY required hard
// capability", and a pool model must satisfy every one too
// (capacityAdapter.js:88-96); a model already in the Combo is not added twice.
func TestPlanComboAdapterNeedsEveryRequiredCapability(t *testing.T) {
	manager := newManager(t)
	ctx := context.Background()
	for _, model := range []runtime.Model{
		{ProviderID: "openai", ID: "text"},
		{ProviderID: "openai", ID: "vision", Capabilities: []string{"vision"}},
		{ProviderID: "adapter", ID: "audio", Capabilities: []string{"audio-input"}},
		{ProviderID: "adapter", ID: "both", Capabilities: []string{"vision", "audio-input"}},
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
	snapshot := withAdapterPools(t, manager, map[string]string{
		"capacityAdapterVision":     adapterPool(true, false, "openai/vision", "adapter/both"),
		"capacityAdapterAudioInput": adapterPool(true, false, "adapter/audio", "adapter/both"),
	})
	handler := New(nil, Options{State: runtime.NewState()})
	// vision alone: a member has it, so no adapter is consulted.
	only, _ := handler.PlanCombo(snapshot, auth.Entry{}, "multi", []string{"vision"})
	if len(only) != 2 || only[0].ModelID != "vision" {
		t.Fatalf("vision alone: %+v", only)
	}
	// vision + audio-input: no member has both, and only adapter/both does; the
	// audio-only pool model cannot satisfy the request and is left out; the
	// Combo's own vision model is not duplicated by the vision pool.
	both, _ := handler.PlanCombo(snapshot, auth.Entry{}, "multi", []string{"vision", "audio-input"})
	got := make([]string, 0, len(both))
	for _, member := range both {
		got = append(got, member.ProviderID+"/"+member.ModelID)
	}
	if strings.Join(got, " ") != "adapter/both openai/vision openai/text" {
		t.Fatalf("multi-capability order: %v", got)
	}
}

func TestPlanComboRRNeverPutsNonCapableFallbackFirst(t *testing.T) {
	manager := newManager(t)
	ctx := context.Background()
	for _, model := range []runtime.Model{
		{ProviderID: "p", ID: "v1", Capabilities: []string{"vision"}},
		{ProviderID: "p", ID: "text"},
		{ProviderID: "p", ID: "v2", Capabilities: []string{"vision"}},
	} {
		if err := manager.PutModel(ctx, model); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := manager.SetCombos(ctx, []runtime.Combo{{ID: "rr", Name: "rr", Strategy: "round-robin", Members: []runtime.ComboMember{
		{ProviderID: "p", ModelID: "v1", Position: 0, Selected: true},
		{ProviderID: "p", ModelID: "text", Position: 1, Selected: true},
		{ProviderID: "p", ModelID: "v2", Position: 2, Selected: true},
	}}}); err != nil {
		t.Fatal(err)
	}
	snapshot := mustLoad(t, manager)
	state := runtime.NewState()
	state.ComboRotation("combo|rr", 3, 1) // start from the non-capable member before capability reorder.
	handler := New(nil, Options{State: state})
	ordered, ok := handler.PlanCombo(snapshot, auth.Entry{}, "rr", []string{"vision"})
	if !ok || ordered[0].ModelID != "v2" || ordered[1].ModelID != "v1" || ordered[2].ModelID != "text" {
		t.Fatalf("RR capability order=%+v", ordered)
	}
}

// #78/C04: a provider's stickyRoundRobinLimit override wins over the global
// limit for that provider only (pin auth.js:186); an Options override still
// beats both, as before.
func TestStickyLimitForAppliesProviderOverride(t *testing.T) {
	manager := newManager(t)
	if _, err := manager.SetSettings(context.Background(), map[string]string{
		"stickyRoundRobinLimit": "4",
		"providerStrategies":    `{"openai":{"fallbackStrategy":"sticky-round-robin","stickyRoundRobinLimit":9}}`,
	}, nil); err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	handler := New(nil, Options{})
	if got := handler.StickyLimitFor(snapshot, "openai"); got != 9 {
		t.Fatalf("openai=%d want 9", got)
	}
	if got := handler.StickyLimitFor(snapshot, "anthropic"); got != 4 {
		t.Fatalf("anthropic=%d want global 4", got)
	}
	if got := handler.StrategyFor(snapshot, "openai"); got != routing.StrategyStickyRR {
		t.Fatalf("strategy=%v", got)
	}
	forced := New(nil, Options{StickyLimit: 2})
	if got := forced.StickyLimitFor(snapshot, "openai"); got != 2 {
		t.Fatalf("explicit option=%d want 2", got)
	}
}

func mustLoad(t *testing.T, manager *runtime.Manager) *runtime.RuntimeSnapshot {
	t.Helper()
	snapshot, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// adapterPool is a stored capacity-adapter value for provider/model strings.
func adapterPool(enabled, roundRobin bool, models ...string) string {
	pool := make([]map[string]string, 0, len(models))
	for _, model := range models {
		provider, id, _ := strings.Cut(model, "/")
		pool = append(pool, map[string]string{"providerId": provider, "modelId": id})
	}
	encoded, _ := json.Marshal(map[string]any{"enabled": enabled, "roundRobin": roundRobin, "pool": pool})
	return string(encoded)
}

// withAdapterPools stores adapter settings through the real manager and returns
// the republished snapshot.
func withAdapterPools(t *testing.T, manager *runtime.Manager, settings map[string]string) *runtime.RuntimeSnapshot {
	t.Helper()
	if _, err := manager.SetSettings(context.Background(), settings, nil); err != nil {
		t.Fatal(err)
	}
	return mustLoad(t, manager)
}
