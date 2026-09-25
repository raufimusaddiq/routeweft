package migrations

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

func TestFreshMigrationAndRestart(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := NewRunner(store.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := NewRunner(store.DB()).Apply(ctx); err != nil {
		t.Fatalf("migration not idempotent: %v", err)
	}
	version, err := NewRunner(store.DB()).Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != LatestVersion() {
		t.Fatalf("version %d, want %d", version, LatestVersion())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := sqlite.Open(ctx, store.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := NewRunner(reopened.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	required := []string{"meta", "settings", "provider_nodes", "provider_connections", "combos", "usage_events", "request_details", "provider_models"}
	for _, name := range required {
		var count int
		if err := reopened.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("table %q missing after restart", name)
		}
	}
}

func TestV1DatabaseUpgradesWithoutLosingCatalogRecords(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := NewRunner(store.DB(), v1).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO settings(key,value) VALUES('requireApiKey','true');
INSERT INTO api_keys(id,name,key_hash,key_prefix) VALUES('k1','operator key','hash-only','rw_demo');
INSERT INTO custom_models(id,provider_id,model_id,display_name) VALUES('m1','provider','model','Model');`); err != nil {
		t.Fatal(err)
	}
	if err := NewRunner(store.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	version, err := NewRunner(store.DB()).Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != LatestVersion() {
		t.Fatalf("version %d, want %d", version, LatestVersion())
	}
	var keyName, modelName string
	if err := store.DB().QueryRowContext(ctx, "SELECT name FROM api_keys WHERE id='k1'").Scan(&keyName); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, "SELECT display_name FROM custom_models WHERE id='m1'").Scan(&modelName); err != nil {
		t.Fatal(err)
	}
	if keyName != "operator key" || modelName != "Model" {
		t.Fatalf("upgraded records key=%q model=%q", keyName, modelName)
	}
}

func TestMigrationFailureRollsBackSchemaAndVersion(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	broken := NewRunner(store.DB(), Migration{Version: 1, Name: "bad", SQL: "CREATE TABLE partial (id INTEGER); INVALID SQL"})
	if err := broken.Apply(ctx); err == nil {
		t.Fatal("expected migration failure")
	}
	var count int
	if err := store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='partial'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed migration left partial schema")
	}
	version, err := broken.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != 0 {
		t.Fatalf("failed migration recorded version %d", version)
	}
}

func TestMigrationRunnerRejectsMissingVersion(t *testing.T) {
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runner := NewRunner(store.DB(), Migration{Version: 2, Name: "gap", SQL: "CREATE TABLE should_not_exist (id INTEGER)"})
	if err := runner.Apply(context.Background()); err == nil {
		t.Fatal("expected non-contiguous migration list to fail")
	}
	var count int
	if err := store.DB().QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='should_not_exist'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("invalid migration list changed schema")
	}
}

func TestVersionTableCheckDoesNotCreateTable(t *testing.T) {
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	exists, err := NewRunner(store.DB()).HasVersionTable(context.Background())
	if err != nil || exists {
		t.Fatalf("exists=%v err=%v, want false without error", exists, err)
	}
	var count int
	if err := store.DB().QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("read-only version check created the migration table")
	}
}
