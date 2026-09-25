package runtime

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/auth"
	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

func newTestManager(t *testing.T) (*Manager, *sqlite.Store) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.NewRunner(store.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(ctx, store.DB())
	if err != nil {
		t.Fatal(err)
	}
	return manager, store
}

func TestUpdatePublishesAfterCommitAndSurvivesRestart(t *testing.T) {
	ctx := context.Background()
	manager, store := newTestManager(t)
	defer store.Close()
	before, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	if before.ConfigRevision() != 0 {
		t.Fatalf("initial revision %d, want 0", before.ConfigRevision())
	}
	updated, err := manager.Update(ctx, func(c *Candidate) error {
		c.Set("requireApiKey", "true")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.Version() != before.Version()+1 || updated.ConfigRevision() != 1 {
		t.Fatalf("version=%d revision=%d", updated.Version(), updated.ConfigRevision())
	}
	if updated.Settings()["requireApiKey"] != "true" {
		t.Fatalf("settings %v", updated.Settings())
	}

	storePath := store.Path()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := sqlite.Open(ctx, storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted, err := NewManager(ctx, reopened.DB())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := restarted.Load()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ConfigRevision() != 1 || snapshot.Settings()["requireApiKey"] != "true" {
		t.Fatalf("restart revision=%d settings=%v", snapshot.ConfigRevision(), snapshot.Settings())
	}
}

func TestFailedCompileKeepsActiveSnapshotAndDoesNotCommit(t *testing.T) {
	ctx := context.Background()
	manager, store := newTestManager(t)
	defer store.Close()
	if _, err := manager.Update(ctx, func(c *Candidate) error {
		c.Set(" ", "invalid")
		return nil
	}); err == nil {
		t.Fatal("expected compile failure")
	}
	snapshot, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ConfigRevision() != 0 {
		t.Fatalf("active snapshot revision changed to %d", snapshot.ConfigRevision())
	}
	if snapshot.Settings()["requireApiKey"] != "true" || snapshot.Settings()[" "] != "" {
		t.Fatalf("rejected candidate leaked into defaults: %v", snapshot.Settings())
	}
	var revisions int
	if err := store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM meta WHERE key='config_revision'").Scan(&revisions); err != nil {
		t.Fatal(err)
	}
	if revisions != 0 {
		t.Fatal("failed compile committed a config revision")
	}
}

func TestFailedMutationLeavesSnapshotIntact(t *testing.T) {
	ctx := context.Background()
	manager, store := newTestManager(t)
	defer store.Close()
	sentinel := errors.New("rejected")
	if _, err := manager.Update(ctx, func(c *Candidate) error {
		c.Set("queued", "value")
		return sentinel
	}); !errors.Is(err, sentinel) {
		t.Fatalf("got %v", err)
	}
	snapshot, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snapshot.Settings()["queued"]; ok {
		t.Fatal("rejected candidate leaked into active snapshot")
	}
}

func TestCommitFailureLeavesSnapshotAndDurableConfigIntact(t *testing.T) {
	ctx := context.Background()
	manager, store := newTestManager(t)
	defer store.Close()
	if _, err := store.DB().ExecContext(ctx, `CREATE TRIGGER reject_settings BEFORE INSERT ON settings BEGIN SELECT RAISE(ABORT, 'injected commit failure'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Update(ctx, func(c *Candidate) error {
		c.Set("commitFailure", "must-not-publish")
		return nil
	}); err == nil {
		t.Fatal("expected transaction failure")
	}
	snapshot, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ConfigRevision() != 0 {
		t.Fatalf("failed update published revision %d", snapshot.ConfigRevision())
	}
	var count int
	if err := store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM settings WHERE key='commitFailure'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed update persisted candidate setting")
	}
}

func TestDefaultsMatchProductContract(t *testing.T) {
	manager, store := newTestManager(t)
	defer store.Close()
	snapshot, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"requireLogin":                 "true",
		"requireApiKey":                "true",
		"stickyRoundRobinLimit":        "3",
		"providerStrategies":           "{}",
		"quotaVisibility":              "{}",
		"comboStrategy":                "fallback",
		"comboStickyRoundRobinLimit":   "1",
		"comboStrategies":              "{}",
		"capacityAdapterVision":        "{\"enabled\":false,\"pool\":[]}",
		"capacityAdapterPDF":           "{\"enabled\":false,\"pool\":[]}",
		"capacityAdapterAudioInput":    "{\"enabled\":false,\"pool\":[]}",
		"capacityAdapterVideoInput":    "{\"enabled\":false,\"pool\":[]}",
		"enableObservability":          "false",
		"observabilityMaxRecords":      "1000",
		"observabilityBatchSize":       "20",
		"observabilityFlushIntervalMs": "5000",
		"observabilityMaxJsonSize":     "5242880",
		"outboundProxyEnabled":         "false",
		"outboundProxyUrl":             "",
		"noProxy":                      "[]",
		"dnsToolEnabled":               "false",
		"providerCompatibility":        "{}",
		"rtkEnabled":                   "true",
		"headroomEnabled":              "false",
		"headroomUrl":                  "http://localhost:8787",
		"headroomCompressUserMessages": "false",
		"headroomTimeoutMs":            "3000",
		"cavemanEnabled":               "false",
		"cavemanLevel":                 "full",
		"ponytailEnabled":              "false",
		"ponytailLevel":                "full",
		"pxpipeEnabled":                "false",
		"pxpipeAutoInstall":            "true",
		"pxpipeMinChars":               "25000",
		"pxpipeTimeoutMs":              "15000",
	}
	got := snapshot.Settings()
	for key, value := range want {
		if got[key] != value {
			t.Errorf("setting %q = %q, want %q", key, got[key], value)
		}
	}
}

func TestConcurrentReadersSeeCoherentSnapshots(t *testing.T) {
	ctx := context.Background()
	manager, store := newTestManager(t)
	defer store.Close()
	var wg sync.WaitGroup
	for reader := 0; reader < 8; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				snapshot, err := manager.Load()
				if err != nil {
					t.Error(err)
					return
				}
				settings := snapshot.Settings()
				if value, ok := settings["generation"]; ok && snapshot.ConfigRevision() < 1 {
					t.Errorf("snapshot revision %d exposes generation %q", snapshot.ConfigRevision(), value)
					return
				}
			}
		}()
	}
	for update := 0; update < 20; update++ {
		if _, err := manager.Update(ctx, func(c *Candidate) error {
			c.Set("generation", "value")
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
	snapshot, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ConfigRevision() != 20 {
		t.Fatalf("revision %d, want 20", snapshot.ConfigRevision())
	}
}

func TestCatalogMutationPersistsAndPublishesAtomically(t *testing.T) {
	ctx := context.Background()
	manager, store := newTestManager(t)
	defer store.Close()
	entry, plaintext, err := manager.CreateAPIKey(ctx, "key A")
	if err != nil {
		t.Fatal(err)
	}
	if plaintext == "" || entry.Hash != "" {
		t.Fatal("key creation must return plaintext once and hide hash")
	}
	var storedHash string
	if err := store.DB().QueryRowContext(ctx, "SELECT key_hash FROM api_keys WHERE id=?", entry.ID).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash != auth.Hash(plaintext) || storedHash == plaintext {
		t.Fatal("database must contain only the one-way key digest")
	}
	active, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := active.APIKeys().Lookup(plaintext); !ok {
		t.Fatal("key absent from published snapshot")
	}
	if err := manager.PutModel(ctx, Model{ProviderID: "openai", ID: "model-a", Name: "A", ContextWindow: 8192, Capabilities: []string{"tools"}}); err != nil {
		t.Fatal(err)
	}
	if err := manager.PutAlias(ctx, "fast", ModelRef{ProviderID: "openai", ModelID: "model-a"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetModelDisabled(ctx, "openai", "model-a", true); err != nil {
		t.Fatal(err)
	}
	if models := manager.active.Load().Models(); len(models) != 0 {
		t.Fatalf("disabled target/alias visible: %+v", models)
	}
	if err := manager.SetModelDisabled(ctx, "openai", "model-a", false); err != nil {
		t.Fatal(err)
	}
	if models := manager.active.Load().Models(); len(models) != 2 {
		t.Fatalf("models=%+v, want model and alias", models)
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
	snapshot, err := restarted.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := snapshot.APIKeys().Lookup(plaintext); !ok {
		t.Fatal("key missing after restart")
	}
	if model, ok := snapshot.ResolveModel("", "fast"); !ok || model.ID != "model-a" {
		t.Fatalf("alias resolution = %+v %v", model, ok)
	}
}

func TestCatalogConcurrentReadsAndUpdatesRaceSafe(t *testing.T) {
	ctx := context.Background()
	manager, store := newTestManager(t)
	defer store.Close()
	_, plaintext, err := manager.CreateAPIKey(ctx, "race key")
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for reader := 0; reader < 8; reader++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				snapshot, err := manager.Load()
				if err != nil {
					t.Error(err)
					return
				}
				_, _ = snapshot.APIKeys().Lookup(plaintext)
				_ = snapshot.Models()
			}
		}()
	}
	for i := 0; i < 10; i++ {
		if err := manager.PutModel(ctx, Model{ProviderID: "provider", ID: fmt.Sprintf("model-%d", i)}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}
