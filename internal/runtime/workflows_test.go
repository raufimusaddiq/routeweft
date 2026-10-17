package runtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

func TestCustomModelWorkflowRestartAndReferences(t *testing.T) {
	m, store := newTestManager(t)
	defer store.Close()
	ctx := context.Background()
	custom := Model{ProviderID: "custom", ID: "vendor/model:tag", Name: "Typed", Kind: "systemone", Capabilities: []string{"reasoning"}, ContextWindow: 8192}
	if err := m.PutModel(ctx, custom); err != nil {
		t.Fatal(err)
	}
	if err := m.ReplaceDiscoveredModels(ctx, "custom", []Model{{ID: custom.ID}, {ID: "discovered"}}); err != nil {
		t.Fatal(err)
	}
	if err := m.SetModelDisabled(ctx, "custom", "discovered", true); err != nil {
		t.Fatal(err)
	}
	if err := m.MergeDiscoveredModels(ctx, "custom", []Model{{ID: custom.ID}, {ID: "new"}, {ID: "new"}}); err != nil {
		t.Fatal(err)
	}
	if err := m.MergeDiscoveredModels(ctx, "custom", []Model{{ID: "new", Name: "New", ContextWindow: 4096, Capabilities: []string{"vision"}}}); err != nil {
		t.Fatal(err)
	}
	if model, ok := mustLoad(t, m).ResolveModel("custom", "new"); !ok || model.Name != "New" || model.ContextWindow != 4096 || len(model.Capabilities) != 1 {
		t.Fatalf("catalog refresh metadata lost: %+v", model)
	}
	if _, ok := mustLoad(t, m).ResolveModel("custom", "discovered"); ok {
		t.Fatal("sync enabled a disabled model")
	}
	m, err := NewManager(ctx, store.DB())
	if err != nil {
		t.Fatal(err)
	}
	snapshot := mustLoad(t, m)
	got, ok := snapshot.ResolveModel("custom", custom.ID)
	if !ok || got.Source != "custom" || got.Kind != "systemone" || got.Name != "Typed" || got.ContextWindow != 8192 || len(got.Capabilities) != 1 || got.Capabilities[0] != "reasoning" || snapshot.ModelKind(got) != "systemone" {
		t.Fatalf("metadata lost: %+v", got)
	}
	if err := m.PutModel(ctx, Model{ProviderID: "custom", ID: custom.ID, Kind: "llm"}); err != nil {
		t.Fatal(err)
	}
	m, err = NewManager(ctx, store.DB())
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := mustLoad(t, m).ResolveModel("custom", custom.ID); got.Kind != "llm" {
		t.Fatalf("kind update lost: %+v", got)
	}
	if err := m.PutModel(ctx, custom); err != nil {
		t.Fatal(err)
	}
	if err := m.PutAlias(ctx, "typed", ModelRef{ProviderID: "custom", ModelID: custom.ID}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.PutCombo(ctx, Combo{Name: "typed-combo", Members: []ComboMember{{ProviderID: "custom", ModelID: custom.ID, Selected: true}}}); err != nil {
		t.Fatal(err)
	}
	revision := mustLoad(t, m).ConfigRevision()
	if err := m.DeleteModel(ctx, "custom", custom.ID); err == nil {
		t.Fatal("deleted a Combo-referenced model")
	}
	if mustLoad(t, m).ConfigRevision() != revision {
		t.Fatal("failed deletion changed revision")
	}
	if _, err := m.DeleteCombo(ctx, "typed-combo"); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteModel(ctx, "custom", custom.ID); err != nil {
		t.Fatal(err)
	}
	m, err = NewManager(ctx, store.DB())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := mustLoad(t, m).ResolveModel("custom", custom.ID); ok {
		t.Fatal("deleted model revived on restart")
	}
	if err := m.SetModelDisabled(ctx, "custom", "discovered", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := mustLoad(t, m).ResolveModel("custom", "discovered"); !ok {
		t.Fatal("sync erased a disabled model")
	}
	if err := m.DeleteModel(ctx, "custom", "discovered"); !errors.Is(err, ErrModelNotCustom) {
		t.Fatalf("non-custom deletion: %v", err)
	}
	if err := m.PutModel(ctx, Model{ProviderID: "custom", ID: "image", Kind: "image"}); err == nil {
		t.Fatal("standalone media kind accepted")
	}
}

func TestCooldownResetConcurrent(t *testing.T) {
	state := NewState()
	state.Cooldown("other", time.Now().Add(time.Hour))
	state.ObserveQuota("account", QuotaObservation{Err: "quota error"})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				state.Cooldown("account", time.Now().Add(time.Hour))
				state.ClearCooldown("account")
				state.CooldownUntil("account", time.Now())
			}
		}()
	}
	wg.Wait()
	state.ClearCooldown("account")
	if _, active := state.CooldownUntil("account", time.Now()); active {
		t.Fatal("cooldown not cleared")
	}
	if _, active := state.CooldownUntil("other", time.Now()); !active {
		t.Fatal("unrelated cooldown cleared")
	}
	if q, ok := state.Quota("account"); !ok || q.Err != "quota error" {
		t.Fatal("reset erased quota")
	}
}

func TestCustomSeedOverrideDelete(t *testing.T) {
	m, creds := providerStore(t)
	ctx := context.Background()
	if _, err := creds.PutNode(ctx, credentials.Node{ID: "builtin", Kind: credentials.NodeBuiltin, ProviderID: "openai", Name: "OpenAI"}); err != nil {
		t.Fatal(err)
	}
	if err := m.PutModel(ctx, Model{ProviderID: "openai", ID: "gpt-4o", Name: "Override", Kind: "llm"}); err != nil {
		t.Fatal(err)
	}
	if err := m.PutAlias(ctx, "override-alias", ModelRef{ProviderID: "openai", ModelID: "gpt-4o"}); err != nil {
		t.Fatal(err)
	}
	if err := m.DeleteModel(ctx, "openai", "gpt-4o"); err != nil {
		t.Fatal(err)
	}
	restarted, err := NewManager(ctx, m.db)
	if err != nil {
		t.Fatal(err)
	}
	for _, manager := range []*Manager{m, restarted} {
		model, ok := mustLoad(t, manager).ServingModel("openai/gpt-4o")
		if !ok || model.Source != "seed" || model.Name == "Override" {
			t.Fatalf("seed not restored: %+v", model)
		}
	}
}
