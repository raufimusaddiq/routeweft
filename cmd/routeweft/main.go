// Command routeweft is the Routeweft server and operator CLI.
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime"
	"syscall"

	"github.com/raufimusaddiq/routeweft/internal/app"
	"github.com/raufimusaddiq/routeweft/internal/buildinfo"
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
	case "version":
		fmt.Printf("routeweft %s (commit %s, built %s, %s)\n", buildinfo.Version, buildinfo.Commit, buildinfo.Date, runtime.Version())
		return nil
	case "help", "-h", "--help":
		fmt.Fprintln(os.Stdout, "usage: routeweft [serve|version|help]")
		return nil
	default:
		return fmt.Errorf("unknown command %q", command)
	}
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	listen := flags.String("listen", envOr("ROUTEWEFT_LISTEN", ":21128"), "HTTP listen address")
	dataDir := flags.String("data-dir", envOr("ROUTEWEFT_DATA_DIR", "/var/lib/routeweft"), "Routeweft data directory")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected arguments: %v", flags.Args())
	}
	log := slog.New(slog.NewJSONHandler(os.Stderr, nil))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	return app.New(app.Config{Listen: *listen, DataDir: *dataDir}, log).Serve(ctx)
}

func envOr(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
