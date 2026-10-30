package main

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
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

func TestInvalidLogLevelFailsBeforeOpeningData(t *testing.T) {
	t.Setenv("ROUTEWEFT_LOG_LEVEL", "verbose")
	dir := filepath.Join(t.TempDir(), "unopened")
	if err := serve([]string{"--data-dir", dir}); err == nil || err.Error() != "invalid ROUTEWEFT_LOG_LEVEL" {
		t.Fatalf("invalid level: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("invalid configuration touched data: %v", err)
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

// #283 / SPEC 12.3: ROUTEWEFT_TRUSTED_PROXIES and --trusted-proxies, and the
// ROUTEWEFT_LOG_LEVEL / --log-level pair, map to the same settings; a flag wins
// over the environment; nothing is accepted that would silently change trust.
func TestServeSettingsMapEnvironmentAndFlags(t *testing.T) {
	t.Setenv("ROUTEWEFT_TRUSTED_PROXIES", "10.0.0.0/8, ::1")
	t.Setenv("ROUTEWEFT_LOG_LEVEL", "warn")
	fromEnv, err := parseServe([]string{"--data-dir", t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	if got := len(fromEnv.config.TrustedProxies); got != 2 || fromEnv.level != slog.LevelWarn {
		t.Fatalf("environment: %d trusted proxies, level %v", got, fromEnv.level)
	}
	fromFlags, err := parseServe([]string{"--data-dir", t.TempDir(), "--trusted-proxies", "192.0.2.1", "--log-level", "DEBUG"})
	if err != nil {
		t.Fatal(err)
	}
	if got := fromFlags.config.TrustedProxies; len(got) != 1 || got[0].String() != "192.0.2.1/32" || fromFlags.level != slog.LevelDebug {
		t.Fatalf("flags did not override the environment: %v level %v", got, fromFlags.level)
	}
	t.Setenv("ROUTEWEFT_TRUSTED_PROXIES", "")
	t.Setenv("ROUTEWEFT_LOG_LEVEL", "")
	defaults, err := parseServe([]string{"--data-dir", t.TempDir()})
	if err != nil || len(defaults.config.TrustedProxies) != 0 || defaults.level != slog.LevelInfo {
		t.Fatalf("defaults: %v %v level %v", err, defaults.config.TrustedProxies, defaults.level)
	}
}

func TestInvalidTrustedProxiesFailBeforeOpeningData(t *testing.T) {
	for _, bad := range []string{"0.0.0.0/0", "::/0", "proxy.example", "10.0.0.0/33", "true"} {
		for name, run := range map[string]func(dir string) error{
			"environment": func(dir string) error {
				t.Setenv("ROUTEWEFT_TRUSTED_PROXIES", bad)
				return serve([]string{"--data-dir", dir})
			},
			"flag": func(dir string) error {
				t.Setenv("ROUTEWEFT_TRUSTED_PROXIES", "")
				return serve([]string{"--data-dir", dir, "--trusted-proxies", bad})
			},
		} {
			dir := filepath.Join(t.TempDir(), "unopened")
			err := run(dir)
			want := "invalid ROUTEWEFT_TRUSTED_PROXIES"
			if name == "flag" {
				want = "invalid --trusted-proxies"
			}
			if err == nil || !strings.HasPrefix(err.Error(), want+": invalid trusted proxy") {
				t.Fatalf("%s %q: %v", name, bad, err)
			}
			if _, statErr := os.Stat(dir); !os.IsNotExist(statErr) {
				t.Fatalf("%s %q touched data: %v", name, bad, statErr)
			}
		}
	}
	t.Setenv("ROUTEWEFT_LOG_LEVEL", "")
	if err := serve([]string{"--data-dir", filepath.Join(t.TempDir(), "unopened"), "--log-level", "verbose"}); err == nil || err.Error() != "invalid --log-level" {
		t.Fatalf("invalid flag level: %v", err)
	}
}
