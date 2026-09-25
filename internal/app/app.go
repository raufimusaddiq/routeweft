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

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/ingress"
	claudeprovider "github.com/raufimusaddiq/routeweft/internal/providers/claude"
	quota "github.com/raufimusaddiq/routeweft/internal/quota"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

type Config struct {
	Listen       string
	DataDir      string
	MaxBodyBytes int64
	CORSOrigins  []string
	// CredentialKey seals provider credentials at rest (internal/credentials).
	// It is optional so the process still boots for a deployment that only serves
	// the inference surface with no stored provider connections yet.
	CredentialKey []byte
	// QuotaRefreshInterval controls the periodic provider quota refresh. Zero uses
	// the default interval.
	QuotaRefreshInterval time.Duration
}

type App struct {
	cfg     Config
	log     *slog.Logger
	store   *sqlite.Store
	runtime *runtime.Manager
	ingress *ingress.Handler
	quota   *quota.Service
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
	a.ingress = ingress.New(manager, ingress.Options{MaxBodyBytes: a.cfg.MaxBodyBytes, CORSOrigins: a.cfg.CORSOrigins, State: manager.State()})
	if err := a.initializeQuota(ctx, store, manager); err != nil {
		_ = store.Close()
		return err
	}
	a.ready.Store(true)
	return nil
}

// initializeQuota wires the production quota path: provider usage reads are
// normalized and published to RuntimeState, reusing the same durable,
// singleflight-refreshed credentials inference uses. It is skipped when no
// credential master key is configured, in which case there are no stored
// provider connections to read quota for.
func (a *App) initializeQuota(ctx context.Context, store *sqlite.Store, manager *runtime.Manager) error {
	if len(a.cfg.CredentialKey) == 0 {
		return nil
	}
	sealer, err := credentials.NewSealer(a.cfg.CredentialKey)
	if err != nil {
		return fmt.Errorf("initialize credential sealer: %w", err)
	}
	credentialStore, err := credentials.NewStore(store.DB(), sealer)
	if err != nil {
		return fmt.Errorf("initialize credential store: %w", err)
	}
	registry := credentials.NewRegistry(credentialStore, nil)
	observer := quota.Observer{
		Clients: map[string]quota.UsageClient{
			"codex":  quota.CodexClient{},
			"claude": quota.ClaudeClient{Client: &claudeprovider.UsageClient{}},
		},
		Publisher: manager.State(),
	}
	a.quota = quota.NewService(credentialStore, registry, observer)
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
	if a.quota != nil {
		// PRD-QUOTA-001: provider quota refresh runs in the background and must not
		// block normal inference. A refresh error is recorded, never fatal.
		go a.quota.Run(ctx, a.cfg.QuotaRefreshInterval)
	}
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

// RefreshQuota refreshes provider quota for every configured usage client. The
// control plane calls it for an operator-initiated refresh; it never blocks the
// inference path.
func (a *App) RefreshQuota(ctx context.Context) error {
	if a.quota == nil {
		return nil
	}
	return a.quota.RefreshAll(ctx)
}
