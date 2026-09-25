package backup

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

func TestCreateIsValidatedMetadataAndNeverOverwrites(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := sqlite.Open(ctx, filepath.Join(dir, "live.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := migrations.NewRunner(store.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, "INSERT INTO settings(key,value) VALUES('requireApiKey','false')"); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "artifacts", "backup.sqlite")
	meta, err := Create(ctx, store, output)
	if err != nil {
		t.Fatal(err)
	}
	if meta.SchemaVersion != migrations.LatestVersion() || meta.RouteweftVersion == "" || meta.RouteweftCommit == "" {
		t.Fatalf("unexpected metadata %+v", meta)
	}
	if _, err := os.Stat(output + ".meta.json"); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(output); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("backup mode %v err=%v", info.Mode(), err)
	}
	if _, err := Create(ctx, store, output); err == nil {
		t.Fatal("expected second backup to refuse overwriting the artifact")
	}
}

func TestStageRestoreValidatesAndDiscards(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	store, err := sqlite.Open(ctx, filepath.Join(dir, "live.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.NewRunner(store.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(dir, "artifact.sqlite")
	want, err := Create(ctx, store, artifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	candidate, err := StageRestore(ctx, artifact, dir)
	if err != nil {
		t.Fatal(err)
	}
	if candidate.Metadata.ConfigRevision != want.ConfigRevision || candidate.Metadata.SchemaVersion != want.SchemaVersion {
		t.Fatalf("candidate metadata %+v, want %+v", candidate.Metadata, want)
	}
	if _, err := os.Stat(candidate.Path); err != nil {
		t.Fatal(err)
	}
	candidate.Discard()
	if _, err := os.Stat(candidate.Dir); !os.IsNotExist(err) {
		t.Fatalf("staging directory survived Discard: %v", err)
	}
}

func TestStageRestoreRejectsForeignAndInconsistentCandidates(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	foreign := filepath.Join(dir, "foreign.sqlite")
	foreignStore, err := sqlite.Open(ctx, foreign)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := foreignStore.DB().ExecContext(ctx, "CREATE TABLE sample(value TEXT)"); err != nil {
		t.Fatal(err)
	}
	if err := foreignStore.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := StageRestore(ctx, foreign, dir); err == nil {
		t.Fatal("expected non-Routeweft database to be rejected")
	}

	store, err := sqlite.Open(ctx, filepath.Join(dir, "live.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := migrations.NewRunner(store.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(dir, "artifact.sqlite")
	if _, err := Create(ctx, store, artifact); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(artifact+".meta.json", []byte(`{"schemaVersion":99,"routeweftVersion":"dev","routeweftCommit":"test","createdAt":"2026-09-25T00:00:00Z","configRevision":0}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := StageRestore(ctx, artifact, dir); err == nil {
		t.Fatal("expected mismatched metadata to be rejected")
	}
}
