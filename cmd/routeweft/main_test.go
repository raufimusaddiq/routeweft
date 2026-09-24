package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

func TestBackupMetadataAndRestoreCheckLeaveInputUntouched(t *testing.T) {
	dir := t.TempDir()
	dbDir := filepath.Join(dir, "data")
	if err := run([]string{"migrate", "--data-dir", dbDir}); err != nil {
		t.Fatal(err)
	}
	backupPath := filepath.Join(dir, "backup.sqlite")
	if err := run([]string{"backup", "--data-dir", dbDir, "--output", backupPath}); err != nil {
		t.Fatal(err)
	}
	metadata, err := os.ReadFile(backupPath + ".meta.json")
	if err != nil || len(metadata) == 0 {
		t.Fatalf("metadata length=%d err=%v", len(metadata), err)
	}
	before, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"restore", "--input", backupPath, "--data-dir", dbDir, "--check"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatal("restore --check modified its input database")
	}
}

func TestRestoreCheckRejectsMismatchedMetadata(t *testing.T) {
	dir := t.TempDir()
	store, err := sqlite.Open(context.Background(), filepath.Join(dir, "candidate.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.NewRunner(store.DB()).Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "candidate.sqlite")
	if err := os.WriteFile(path+".meta.json", []byte(`{"schemaVersion":99,"routeweftVersion":"dev","routeweftCommit":"test","createdAt":"2026-09-24T00:00:00Z","configRevision":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"restore", "--input", path, "--data-dir", dir, "--check"}); err == nil {
		t.Fatal("expected mismatched metadata to be rejected")
	}
}
