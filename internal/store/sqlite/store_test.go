package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenWALAndPrivatePermissions(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "routeweft.sqlite")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.IntegrityCheck(ctx); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Fatalf("mode %o, want 600", got)
	}
	var mode string
	if err := s.DB().QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&mode); err != nil {
		t.Fatal(err)
	}
	if mode != "wal" {
		t.Fatalf("journal mode %q", mode)
	}
}

func TestBackupIsOnlineConsistentAndNeverOverwrites(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, filepath.Join(dir, "source.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB().ExecContext(ctx, "CREATE TABLE sample (value TEXT); INSERT INTO sample VALUES ('preserved')"); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "backup.sqlite")
	if err := s.Backup(ctx, destination); err != nil {
		t.Fatal(err)
	}
	copy, err := Open(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	defer copy.Close()
	var value string
	if err := copy.DB().QueryRowContext(ctx, "SELECT value FROM sample").Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != "preserved" {
		t.Fatalf("backup contains %q", value)
	}
	if err := os.WriteFile(destination, []byte("must remain"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := s.Backup(ctx, destination); err == nil {
		t.Fatal("expected destination collision to be rejected")
	}
	data, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "must remain" {
		t.Fatalf("existing destination overwritten: %q", data)
	}
}

func TestOpenRejectsInMemoryDatabase(t *testing.T) {
	if _, err := Open(context.Background(), ":memory:"); err == nil {
		t.Fatal("expected persistent database requirement")
	}
}

func TestConfigureBackupRemovesWALDependency(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	s, err := Open(ctx, filepath.Join(dir, "source.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if _, err := s.DB().ExecContext(ctx, "CREATE TABLE sample (value TEXT); INSERT INTO sample VALUES ('x')"); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(dir, "backup.sqlite")
	if err := s.Backup(ctx, destination); err != nil {
		t.Fatal(err)
	}
	backup, err := Open(ctx, destination)
	if err != nil {
		t.Fatal(err)
	}
	if err := backup.CloseBackup(ctx); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		if _, err := os.Stat(destination + suffix); !os.IsNotExist(err) {
			t.Fatalf("backup sidecar %s remains: %v", suffix, err)
		}
	}
}
