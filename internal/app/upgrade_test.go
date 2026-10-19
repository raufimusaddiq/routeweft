package app

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/backup"
	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

// TestUpgradeRollbackRehearsal proves the RUNBOOK §13-14 upgrade path: back up,
// upgrade the schema, verify persisted records survive, reject an older binary
// against the newer schema, then roll the data back to the pre-upgrade backup.
func TestUpgradeRollbackRehearsal(t *testing.T) {
	ctx := context.Background()
	dataDir := t.TempDir()
	database := filepath.Join(dataDir, "routeweft.sqlite")
	store, err := sqlite.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.NewRunner(store.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO settings(key,value) VALUES('rtkEnabled','true')`); err != nil {
		t.Fatal(err)
	}
	before, err := backup.Create(ctx, store, filepath.Join(dataDir, "routeweft-pre-upgrade.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if before.SchemaVersion != migrations.LatestVersion() {
		t.Fatalf("pre-upgrade backup schema=%d, want %d", before.SchemaVersion, migrations.LatestVersion())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// Simulate the next release applying an additive migration, then recompiling
	// state from the upgraded database.
	upgraded, err := sqlite.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := upgraded.DB().ExecContext(ctx, `ALTER TABLE settings ADD COLUMN upgrade_marker TEXT`); err != nil {
		t.Fatal(err)
	}
	if err := migrations.NewRunner(upgraded.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	var persisted string
	if err := upgraded.DB().QueryRowContext(ctx, `SELECT value FROM settings WHERE key='rtkEnabled'`).Scan(&persisted); err != nil {
		t.Fatal(err)
	}
	if persisted != "true" {
		t.Fatalf("setting after upgrade=%q, want true", persisted)
	}
	if err := upgraded.Close(); err != nil {
		t.Fatal(err)
	}

	// A previous release must refuse to open a schema newer than it supports.
	previous, err := sqlite.Open(ctx, database)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.NewRunner(previous.DB(), migrations.All()[:len(migrations.All())-1]...).Apply(ctx); err == nil {
		t.Fatal("older binary accepted a newer schema")
	}
	if err := previous.Close(); err != nil {
		t.Fatal(err)
	}

	// Rollback restores the pre-upgrade backup into the live path.
	candidate, err := backup.StageRestore(ctx, filepath.Join(dataDir, "routeweft-pre-upgrade.sqlite"), dataDir)
	if err != nil {
		t.Fatal(err)
	}
	defer candidate.Discard()
	application := New(Config{Listen: ":0", DataDir: dataDir}, nil)
	if err := application.initialize(ctx); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if application.store != nil {
			_ = application.store.Close()
		}
	}()
	if err := application.activateRestore(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	if application.runtime.Settings()["rtkEnabled"] != "true" {
		t.Fatal("rolled-back database lost its setting")
	}
	var archived int
	if err := application.store.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info('settings') WHERE name='upgrade_marker'`).Scan(&archived); err != nil {
		t.Fatal(err)
	}
	if archived != 0 {
		t.Fatal("rollback retained the newer schema column")
	}
	if application.store.IntegrityCheck(ctx) != nil {
		t.Fatal("rolled-back database failed integrity check")
	}
}
