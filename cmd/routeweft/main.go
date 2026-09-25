// Command routeweft is the Routeweft server and operator CLI.
package main

import (
	"context"
	"encoding/base64"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"

	"github.com/raufimusaddiq/routeweft/internal/app"
	"github.com/raufimusaddiq/routeweft/internal/backup"
	"github.com/raufimusaddiq/routeweft/internal/buildinfo"
	"github.com/raufimusaddiq/routeweft/internal/ingress"
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
		return backupCommand(rest)
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

func backupCommand(args []string) error {
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
	metadata, err := backup.Create(ctx, store, *output)
	if err != nil {
		return err
	}
	fmt.Printf("routeweft: backup %s (schema %d, config revision %d)\n", *output, metadata.SchemaVersion, metadata.ConfigRevision)
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
	candidate, err := backup.StageRestore(ctx, *input, *dataDir)
	if err != nil {
		return err
	}
	defer candidate.Discard()
	fmt.Printf("routeweft: restore --check %s ok (schema %d, config revision %d)\n", *input, candidate.Metadata.SchemaVersion, candidate.Metadata.ConfigRevision)
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
	credentialKey := flags.String("credential-key", envOr("ROUTEWEFT_CREDENTIAL_KEY", ""), "base64 credential master key sealing provider secrets at rest")
	allowPrivate := flags.Bool("allow-private-upstreams", envBool("ROUTEWEFT_ALLOW_PRIVATE_UPSTREAMS", false), "allow trusted-local/private upstream and proxy destinations")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	var key []byte
	if trimmed := strings.TrimSpace(*credentialKey); trimmed != "" {
		decoded, err := base64.StdEncoding.DecodeString(trimmed)
		if err != nil {
			return fmt.Errorf("decode credential key: %w", err)
		}
		key = decoded
	}
	adminPassword := os.Getenv("ROUTEWEFT_BOOTSTRAP_ADMIN_PASSWORD")
	adminUsername := strings.TrimSpace(os.Getenv("ROUTEWEFT_BOOTSTRAP_ADMIN_USERNAME"))
	if adminPassword != "" && adminUsername == "" {
		adminUsername = "admin"
	}
	return app.New(app.Config{Listen: *listen, DataDir: *dataDir, MaxBodyBytes: *maxBody, CORSOrigins: splitList(*corsOrigins), CredentialKey: key, AllowPrivateUpstreams: *allowPrivate, AdminBootstrapUsername: adminUsername, AdminBootstrapPassword: adminPassword}, log).Serve(ctx)
}

func envBool(key string, fallback bool) bool {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		if parsed, err := strconv.ParseBool(value); err == nil {
			return parsed
		}
	}
	return fallback
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
