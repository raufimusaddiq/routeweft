// Package app wires the Routeweft process together.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"sync/atomic"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/ingress"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

type Config struct {
	Listen       string
	DataDir      string
	MaxBodyBytes int64
	CORSOrigins  []string
}

type App struct {
	cfg     Config
	log     *slog.Logger
	store   *sqlite.Store
	runtime *runtime.Manager
	ingress *ingress.Handler
	ready   atomic.Bool
}

func New(cfg Config, log *slog.Logger) *App {
	if log == nil {
		log = slog.Default()
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = ingress.DefaultMaxBodyBytes
	}
	return &App{cfg: cfg, log: log}
}

// Initialize creates the data directory, migrates durable state and compiles the
// initial request-serving snapshot before the listener is started.
func (a *App) Initialize(ctx context.Context) error {
	if a.cfg.DataDir == "" {
		return errors.New("data directory is required")
	}
	store, err := sqlite.Open(ctx, filepath.Join(a.cfg.DataDir, "routeweft.sqlite"))
	if err != nil {
		return err
	}
	if err := migrations.NewRunner(store.DB()).Apply(ctx); err != nil {
		_ = store.Close()
		return fmt.Errorf("migrate database: %w", err)
	}
	if err := store.IntegrityCheck(ctx); err != nil {
		_ = store.Close()
		return fmt.Errorf("validate database integrity: %w", err)
	}
	manager, err := runtime.NewManager(ctx, store.DB())
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("initialize runtime: %w", err)
	}
	a.store, a.runtime = store, manager
	a.ingress = ingress.New(manager, ingress.Options{MaxBodyBytes: a.cfg.MaxBodyBytes, CORSOrigins: a.cfg.CORSOrigins})
	a.ready.Store(true)
	return nil
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	if a.ingress != nil {
		a.ingress.Attach(mux)
	}
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, _ *http.Request) {
		if !a.ready.Load() {
			http.Error(w, "not ready", http.StatusServiceUnavailable)
			return
		}
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("ok\n"))
	})
	return withRequestID(withBodyLimit(a.cfg.MaxBodyBytes, mux))
}

func (a *App) Serve(ctx context.Context) error {
	if !a.ready.Load() {
		if err := a.Initialize(ctx); err != nil {
			return err
		}
	}
	srv := &http.Server{
		Addr:              a.cfg.Listen,
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	errCh := make(chan error, 1)
	go func() {
		a.log.Info("listening", "addr", a.cfg.Listen)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	select {
	case err := <-errCh:
		return fmt.Errorf("serve: %w", err)
	case <-ctx.Done():
		a.ready.Store(false)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return err
		}
		if a.store != nil {
			return a.store.Close()
		}
		return nil
	}
}
