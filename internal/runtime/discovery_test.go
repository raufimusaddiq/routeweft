package runtime

import (
	"context"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

func TestReplaceDiscoveredModelsKeepsCustomAndOtherProviders(t *testing.T) {
	manager, store := newTestManager(t)
	defer store.Close()
	ctx := context.Background()

	if err := manager.PutModel(ctx, Model{ProviderID: "openrouter", ID: "custom/manual", Name: "Manual", ContextWindow: 8192}); err != nil {
		t.Fatal(err)
	}
	if err := manager.PutModel(ctx, Model{ProviderID: "other", ID: "other-model", Name: "Other"}); err != nil {
		t.Fatal(err)
	}
	// Simulate a seeded/static model already present for the target provider.
	if err := manager.UpdateCatalog(ctx, func(candidate *Candidate) error {
		candidate.AddModel(Model{ProviderID: "openrouter", ID: "seed/model", Name: "Seed", Source: "static"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := manager.ReplaceDiscoveredModels(ctx, "openrouter", []Model{{ID: "z-ai/glm"}, {ID: "openai/gpt"}}); err != nil {
		t.Fatal(err)
	}
	if err := manager.ReplaceDiscoveredModels(ctx, "openrouter", []Model{{ID: "z-ai/glm"}, {ID: "openai/gpt"}, {ID: "new/model"}}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]Model{}
	for _, model := range snapshot.Models() {
		byID[model.ProviderID+"/"+model.ID] = model
	}
	// Custom model and other provider survive discovery replacement.
	if _, ok := byID["openrouter/custom/manual"]; !ok {
		t.Fatalf("custom model dropped: %+v", byID)
	}
	if _, ok := byID["other/other-model"]; !ok {
		t.Fatalf("other provider model dropped: %+v", byID)
	}
	// Discovered set is replaced, not appended forever.
	if _, ok := byID["openrouter/new/model"]; !ok {
		t.Fatalf("newly discovered model missing: %+v", byID)
	}
	discovered := 0
	for key, model := range byID {
		if model.ProviderID == "openrouter" && model.Source == "discovered" {
			discovered++
			_ = key
		}
	}
	if discovered != 3 {
		t.Fatalf("discovered count=%d want 3 (%+v)", discovered, byID)
	}
	if byID["openrouter/z-ai/glm"].Source != "discovered" || byID["openrouter/custom/manual"].Source != "custom" {
		t.Fatalf("source attribution wrong: %+v", byID)
	}

	// Empty discovery is advisory and must not delete the last known catalog.
	if err := manager.ReplaceDiscoveredModels(ctx, "openrouter", nil); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = manager.Load()
	remaining := 0
	for _, model := range snapshot.Models() {
		if model.ProviderID == "openrouter" {
			remaining++
		}
	}
	if remaining != 5 {
		t.Fatalf("empty discovery deleted models: remaining=%d want 5 (3 discovered + 1 custom + 1 seed)", remaining)
	}
	if _, ok := byIDSnapshot(snapshot)["openrouter/z-ai/glm"]; !ok {
		t.Fatal("empty discovery erased an existing discovered model")
	}
	// Seeded/static and custom entries survive discovery refreshes.
	if _, ok := byID["openrouter/seed/model"]; !ok {
		t.Fatal("seed model dropped by discovery refresh")
	}

	if err := manager.ReplaceDiscoveredModels(ctx, "  ", nil); err == nil {
		t.Fatal("accepted blank provider id")
	}
}

func byIDSnapshot(snapshot *RuntimeSnapshot) map[string]Model {
	result := make(map[string]Model)
	for _, model := range snapshot.Models() {
		result[model.ProviderID+"/"+model.ID] = model
	}
	return result
}

func TestReplaceDiscoveredModelsPersistsAcrossReload(t *testing.T) {
	manager, store := newTestManager(t)
	ctx := context.Background()
	if err := manager.ReplaceDiscoveredModels(ctx, "kilo", []Model{{ID: "anthropic/claude-sonnet-4"}}); err != nil {
		t.Fatal(err)
	}
	path := store.Path()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := sqlite.Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	reopened, err := NewManager(ctx, store.DB())
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, model := range reopened.active.Load().Models() {
		if model.ProviderID == "kilo" && model.ID == "anthropic/claude-sonnet-4" && model.Source == "discovered" {
			found = true
		}
	}
	if !found {
		t.Fatal("discovered model did not survive reload")
	}
}
