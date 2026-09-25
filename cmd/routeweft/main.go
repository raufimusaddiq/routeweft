// Command routeweft is the Routeweft server and operator CLI.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/app"
	"github.com/raufimusaddiq/routeweft/internal/buildinfo"
	"github.com/raufimusaddiq/routeweft/internal/ingress"
	appruntime "github.com/raufimusaddiq/routeweft/internal/runtime"
	storemigrations "github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "routeweft:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	command, rest := "serve", args
	if len(args) > 0 {
		command, rest = args[0], args[1:]
	}
	switch command {
	case "serve":
		return serve(rest)
	case "migrate":
		return migrate(rest)
	case "backup":
		return backup(rest)
	case "restore":
		return restore(rest)
	case "version":
		fmt.Printf("routeweft %s (commit %s, built %s, %s)\n", buildinfo.Version, buildinfo.Commit, buildinfo.Date, runtime.Version())
		return nil
	case "help", "-h", "--help":
		fmt.Fprintln(os.Stdout, "usage: routeweft [serve|migrate|backup|restore|version|help]")
		return nil
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func migrate(args []string) error {
	flags := flag.NewFlagSet("migrate", flag.ContinueOnError)
	dataDir := flags.String("data-dir", envOr("ROUTEWEFT_DATA_DIR", "/var/lib/routeweft"), "Routeweft data directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := openStore(ctx, *dataDir)
	if err != nil {
		return err
	}
	defer store.Close()
	version, err := storemigrations.NewRunner(store.DB()).Version(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("routeweft: schema version %d at %s\n", version, store.Path())
	return nil
}

func backup(args []string) error {
	flags := flag.NewFlagSet("backup", flag.ContinueOnError)
	dataDir := flags.String("data-dir", envOr("ROUTEWEFT_DATA_DIR", "/var/lib/routeweft"), "Routeweft data directory")
	output := flags.String("output", "", "backup destination file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *output == "" {
		return errors.New("backup requires --output")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	store, err := openStore(ctx, *dataDir)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.IntegrityCheck(ctx); err != nil {
		return err
	}
	version, err := storemigrations.NewRunner(store.DB()).Version(ctx)
	if err != nil {
		return err
	}
	manager, err := appruntime.NewManager(ctx, store.DB())
	if err != nil {
		return err
	}
	snapshot, err := manager.Load()
	if err != nil {
		return err
	}
	durable := snapshot.ConfigRevision()
	info := buildinfo.Current()
	metadata := map[string]any{
		"schemaVersion":    version,
		"routeweftVersion": info.Version,
		"routeweftCommit":  info.Commit,
		"createdAt":        time.Now().UTC().Format(time.RFC3339Nano),
		"configRevision":   durable,
	}
	blob, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return err
	}
	if err := publishBackup(ctx, store, *output, append(blob, '\n')); err != nil {
		return err
	}
	fmt.Printf("routeweft: backup %s (schema %d, config revision %d)\n", *output, version, durable)
	return nil
}

func restore(args []string) error {
	flags := flag.NewFlagSet("restore", flag.ContinueOnError)
	input := flags.String("input", "", "candidate backup file")
	dataDir := flags.String("data-dir", envOr("ROUTEWEFT_DATA_DIR", "/var/lib/routeweft"), "Routeweft staging directory")
	check := flags.Bool("check", false, "validate the candidate without activating it")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *input == "" {
		return errors.New("restore requires --input")
	}
	if !*check {
		return errors.New("activation restore is not implemented yet; run with --check")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	version, err := validateRestoreCandidate(ctx, *input, *dataDir)
	if err != nil {
		return err
	}
	fmt.Printf("routeweft: restore --check %s ok (schema %d)\n", *input, version)
	return nil
}

func validateRestoreCandidate(ctx context.Context, input, dataDir string) (int, error) {
	file, err := os.Open(input)
	if err != nil {
		return 0, fmt.Errorf("open restore candidate: %w", err)
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return 0, err
	}
	if !info.Mode().IsRegular() {
		return 0, errors.New("restore candidate must be a regular file")
	}
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return 0, fmt.Errorf("create restore staging directory: %w", err)
	}
	stageDir, err := os.MkdirTemp(dataDir, ".routeweft-restore-*")
	if err != nil {
		return 0, err
	}
	defer os.RemoveAll(stageDir)
	stagePath := filepath.Join(stageDir, "candidate.sqlite")
	stagedFile, err := os.OpenFile(stagePath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return 0, err
	}
	if _, err := io.Copy(stagedFile, file); err == nil {
		err = stagedFile.Sync()
	}
	closeErr := stagedFile.Close()
	if err != nil {
		return 0, fmt.Errorf("stage restore candidate: %w", err)
	}
	if closeErr != nil {
		return 0, closeErr
	}
	candidate, err := sqlite.Open(ctx, stagePath)
	if err != nil {
		return 0, err
	}
	defer candidate.Close()
	if err := candidate.IntegrityCheck(ctx); err != nil {
		return 0, err
	}
	runner := storemigrations.NewRunner(candidate.DB())
	hasVersions, err := runner.HasVersionTable(ctx)
	if err != nil {
		return 0, err
	}
	if !hasVersions {
		return 0, fmt.Errorf("candidate %s is not a Routeweft database", input)
	}
	version, err := runner.Version(ctx)
	if err != nil {
		return 0, err
	}
	if version > storemigrations.LatestVersion() {
		return 0, fmt.Errorf("candidate schema version %d is newer than supported %d", version, storemigrations.LatestVersion())
	}
	if err := runner.Apply(ctx); err != nil {
		return 0, fmt.Errorf("migrate candidate: %w", err)
	}
	manager, err := appruntime.NewManager(ctx, candidate.DB())
	if err != nil {
		return 0, fmt.Errorf("compile candidate snapshot: %w", err)
	}
	snapshot, err := manager.Load()
	if err != nil {
		return 0, err
	}
	if err := validateBackupMetadata(input, version, snapshot.ConfigRevision()); err != nil {
		return 0, err
	}
	if err := candidate.IntegrityCheck(ctx); err != nil {
		return 0, err
	}
	return version, nil
}

type backupMetadata struct {
	SchemaVersion    int    `json:"schemaVersion"`
	RouteweftVersion string `json:"routeweftVersion"`
	RouteweftCommit  string `json:"routeweftCommit"`
	CreatedAt        string `json:"createdAt"`
	ConfigRevision   uint64 `json:"configRevision"`
}

func validateBackupMetadata(input string, schemaVersion int, configRevision uint64) error {
	metadataFile, err := os.Open(input + ".meta.json")
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open backup metadata: %w", err)
	}
	defer metadataFile.Close()
	var metadata backupMetadata
	if err := json.NewDecoder(metadataFile).Decode(&metadata); err != nil {
		return fmt.Errorf("decode backup metadata: %w", err)
	}
	if metadata.SchemaVersion != schemaVersion || metadata.RouteweftVersion == "" || metadata.RouteweftCommit == "" || metadata.ConfigRevision != configRevision {
		return errors.New("backup metadata does not match the database schema/configuration")
	}
	if _, err := time.Parse(time.RFC3339Nano, metadata.CreatedAt); err != nil {
		return fmt.Errorf("invalid backup metadata timestamp: %w", err)
	}
	return nil
}

func publishBackup(ctx context.Context, source *sqlite.Store, output string, metadata []byte) error {
	absolute, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	directory := filepath.Dir(absolute)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	metadataPath := absolute + ".meta.json"
	for _, path := range []string{absolute, metadataPath} {
		if _, err := os.Lstat(path); err == nil {
			return fmt.Errorf("backup destination already exists: %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("check backup destination: %w", err)
		}
	}
	stageDir, err := os.MkdirTemp(directory, ".routeweft-backup-*")
	if err != nil {
		return fmt.Errorf("stage backup: %w", err)
	}
	defer os.RemoveAll(stageDir)
	stageDB := filepath.Join(stageDir, "routeweft.sqlite")
	if err := source.Backup(ctx, stageDB); err != nil {
		return err
	}
	staged, err := sqlite.Open(ctx, stageDB)
	if err != nil {
		return err
	}
	if err := staged.IntegrityCheck(ctx); err != nil {
		_ = staged.Close()
		return err
	}
	if err := staged.CloseBackup(ctx); err != nil {
		return err
	}
	stageMetadata := filepath.Join(stageDir, "routeweft.sqlite.meta.json")
	file, err := os.OpenFile(stageMetadata, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	if _, err = file.Write(metadata); err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil {
		return fmt.Errorf("write backup metadata: %w", err)
	}
	if closeErr != nil {
		return closeErr
	}
	if err := os.Link(stageDB, absolute); err != nil {
		return fmt.Errorf("activate backup file: %w", err)
	}
	if err := os.Link(stageMetadata, metadataPath); err != nil {
		_ = os.Remove(absolute)
		return fmt.Errorf("activate backup metadata: %w", err)
	}
	dir, err := os.Open(directory)
	if err != nil {
		_ = os.Remove(metadataPath)
		_ = os.Remove(absolute)
		return err
	}
	err = dir.Sync()
	_ = dir.Close()
	if err != nil {
		_ = os.Remove(metadataPath)
		_ = os.Remove(absolute)
		return fmt.Errorf("sync backup directory: %w", err)
	}
	return nil
}

func openStore(ctx context.Context, dataDir string) (*sqlite.Store, error) {
	if dataDir == "" {
		return nil, errors.New("data directory is required")
	}
	store, err := sqlite.Open(ctx, filepath.Join(dataDir, "routeweft.sqlite"))
	if err != nil {
		return nil, err
	}
	if err := storemigrations.NewRunner(store.DB()).Apply(ctx); err != nil {
		_ = store.Close()
		return nil, err
	}
	return store, nil
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := flags.String("listen", envOr("ROUTEWEFT_LISTEN", ":21128"), "HTTP listen address")
	dataDir := flags.String("data-dir", envOr("ROUTEWEFT_DATA_DIR", "/var/lib/routeweft"), "Routeweft data directory")
	maxBody := flags.Int64("max-body-bytes", envInt64("ROUTEWEFT_MAX_BODY_BYTES", ingress.DefaultMaxBodyBytes), "maximum request body size in bytes")
	corsOrigins := flags.String("cors-origins", envOr("ROUTEWEFT_CORS_ORIGINS", ""), "comma-separated allowed browser origins")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return app.New(app.Config{Listen: *listen, DataDir: *dataDir, MaxBodyBytes: *maxBody, CORSOrigins: splitList(*corsOrigins)}, log).Serve(ctx)
}

func splitList(value string) []string {
	var out []string
	for _, item := range strings.Split(value, ",") {
		if trimmed := strings.TrimSpace(item); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}

func envInt64(key string, fallback int64) int64 {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
			return parsed
		}
	}
	return fallback
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
