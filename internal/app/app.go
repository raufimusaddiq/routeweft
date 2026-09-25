// Package app wires the Routeweft process together.
package app

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/ingress"
	claudeprovider "github.com/raufimusaddiq/routeweft/internal/providers/claude"
	"github.com/raufimusaddiq/routeweft/internal/proxy"
	quota "github.com/raufimusaddiq/routeweft/internal/quota"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
	"github.com/raufimusaddiq/routeweft/internal/telemetry"
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
	// AllowPrivateUpstreams mirrors the trusted-local operator policy so outbound
	// proxy validation and inference may reach LAN/self-hosted destinations.
	AllowPrivateUpstreams bool
}

// loadSnapshot exposes the active snapshot to the proxy binder's binding source.
func loadSnapshot(manager *runtime.Manager) func() *runtime.RuntimeSnapshot {
	return func() *runtime.RuntimeSnapshot {
		snapshot, err := manager.Load()
		if err != nil {
			return nil
		}
		return snapshot
	}
}

// telemetryOptions maps the compiled observability settings onto the bounded
// queue configuration (SPEC §21). Invalid values fall back to the defaults.
func telemetryOptions(settings map[string]string) telemetry.Options {
	opts := telemetry.DefaultOptions()
	opts.Enabled = settings["enableObservability"] == "true"
	if value, err := strconv.Atoi(settings["observabilityMaxRecords"]); err == nil && value > 0 {
		opts.MaxRecords = value
	}
	if value, err := strconv.Atoi(settings["observabilityBatchSize"]); err == nil && value > 0 {
		opts.BatchSize = value
	}
	if value, err := strconv.Atoi(settings["observabilityFlushIntervalMs"]); err == nil && value > 0 {
		opts.FlushInterval = time.Duration(value) * time.Millisecond
	}
	return opts
}

type App struct {
	cfg     Config
	log     *slog.Logger
	store   *sqlite.Store
	runtime *runtime.Manager
	ingress *ingress.Handler
	quota   *quota.Service
	usage   *telemetry.Service
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
	snapshot, err := manager.Load()
	if err != nil {
		_ = store.Close()
		return fmt.Errorf("load initial snapshot: %w", err)
	}
	usage := telemetry.New(telemetry.NewSQLiteSink(store.DB()), telemetryOptions(snapshot.Settings()))
	a.usage = usage
	binder := proxy.NewBinder(loadSnapshot(manager))
	binder.AllowLocal = a.cfg.AllowPrivateUpstreams
	a.ingress = ingress.New(manager, ingress.Options{
		MaxBodyBytes:          a.cfg.MaxBodyBytes,
		CORSOrigins:           a.cfg.CORSOrigins,
		State:                 manager.State(),
		AllowPrivateUpstreams: a.cfg.AllowPrivateUpstreams,
		ClientFor: func(connectionID string) *http.Client {
			return binder.ClientFor(context.Background(), connectionID)
		},
		OnRequestComplete: func(outcome ingress.RequestOutcome) {
			usage.Record(telemetry.Event{
				Class:        telemetry.Critical,
				RequestID:    outcome.RequestID,
				ProviderID:   outcome.ProviderID,
				ModelID:      outcome.ModelID,
				ConnectionID: outcome.ConnectionID,
				Status:       outcome.Status,
				InputTokens:  outcome.InputTokens,
				OutputTokens: outcome.OutputTokens,
				CacheRead:    outcome.CacheRead,
				CacheWrite:   outcome.CacheWrite,
				DurationMS:   outcome.Duration.Milliseconds(),
			})
		},
	})
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
	// Seed the memory-first credential registry so quota reads do not fail with
	// ErrNotFound before the first refresh.
	for _, providerID := range []string{"codex", "claude"} {
		if _, err := registry.Load(ctx, providerID); err != nil {
			return fmt.Errorf("seed %s credentials: %w", providerID, err)
		}
	}
	observer := quota.Observer{
		Clients: map[string]quota.UsageClient{
			"codex":  quota.CodexClient{},
			"claude": quota.ClaudeClient{Client: &claudeprovider.UsageClient{}},
		},
		Publisher: manager.State(),
	}
	a.quota = quota.NewService(credentialStore, registry, observer).WithSeeder(registry)
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
	if a.usage != nil && a.usage.Enabled() {
		// SPEC §21: the batcher is the only usage writer; the request path only
		// enqueues, so a normal success never synchronously writes SQLite.
		go a.usage.Run(ctx)
	}
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
		if a.usage != nil {
			a.usage.Close()
			// ctx is already cancelled, so Run performs its final flush now; join it
			// before the store is closed (BDR-013 shutdown).
			a.usage.Wait()
		}
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
