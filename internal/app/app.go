// Package app wires the Routeweft process together.
package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
	"github.com/raufimusaddiq/routeweft/internal/adminauth"
	"github.com/raufimusaddiq/routeweft/internal/backup"
	"github.com/raufimusaddiq/routeweft/internal/buildinfo"
	controlapi "github.com/raufimusaddiq/routeweft/internal/control/api"
	controlevents "github.com/raufimusaddiq/routeweft/internal/control/events"
	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/ingress"
	claudeprovider "github.com/raufimusaddiq/routeweft/internal/providers/claude"
	"github.com/raufimusaddiq/routeweft/internal/providers/discovery"
	"github.com/raufimusaddiq/routeweft/internal/providers/oauthrefresh"
	"github.com/raufimusaddiq/routeweft/internal/providers/onboarding"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	providerusage "github.com/raufimusaddiq/routeweft/internal/providers/usage"
	"github.com/raufimusaddiq/routeweft/internal/proxy"
	quota "github.com/raufimusaddiq/routeweft/internal/quota"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
	"github.com/raufimusaddiq/routeweft/internal/telemetry"
	"github.com/raufimusaddiq/routeweft/internal/transforms/pxpipe"
	"github.com/raufimusaddiq/routeweft/internal/transport"
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
	// AdminBootstrapUsername/Password provision the first dashboard admin when the
	// database has none (RUNBOOK §bootstrap). Both are ignored once an admin
	// exists, so a stale environment value cannot re-provision or overwrite one.
	AdminBootstrapUsername string
	AdminBootstrapPassword string
	// AdminSessionTTL overrides the dashboard session lifetime (zero uses default).
	AdminSessionTTL time.Duration
	// UIDir is the built Vite UI directory served by the Go binary (BDR-003).
	// Empty disables static serving so a reverse proxy can own the assets.
	UIDir string
	// PublicOrigin is the operator-facing origin used as a provider sign-in
	// callback. It is configured, never derived from a request, so a caller cannot
	// steer a provider redirect. Providers whose client registration pins a
	// loopback port (Codex, xAI) do not need it.
	PublicOrigin string
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

type App struct {
	cfg          Config
	log          *slog.Logger
	store        *sqlite.Store
	runtime      *runtime.Manager
	pxpipe       *pxpipe.Service
	ingress      *ingress.Handler
	quota        *quota.Service
	usage        *telemetry.Service
	usageStarted bool
	details      *telemetry.SQLiteDetailStore
	admin        *adminauth.Store
	creds        *credentials.Store
	credReg      *credentials.Registry
	control      *controlapi.Handler
	events       *controlevents.Bus
	feed         *controlevents.UsageFeed
	logs         *controlapi.LogBuffer
	active       atomic.Int64
	ready        atomic.Bool
	// mu serializes Initialize and restore activation. Quiescent is set while a
	// restore reinitializes the process so new requests are refused instead of
	// racing the database swap (SPEC §25).
	mu        sync.Mutex
	quiescent atomic.Bool
	serving   atomic.Bool
	restoreCh chan *backup.Candidate
	// drainGrace overrides the SIGTERM drain deadline (default 10s); tests only.
	drainGrace time.Duration
}

func New(cfg Config, log *slog.Logger) *App {
	if log == nil {
		log = slog.Default()
	}
	if cfg.MaxBodyBytes <= 0 {
		cfg.MaxBodyBytes = ingress.DefaultMaxBodyBytes
	}
	logs := controlapi.NewLogBuffer(0)
	return &App{cfg: cfg, log: slog.New(controlapi.LogHandler(log.Handler(), logs)), logs: logs, restoreCh: make(chan *backup.Candidate, 1)}
}

// Initialize creates the data directory, migrates durable state and compiles the
// initial request-serving snapshot before the listener is started.
func (a *App) Initialize(ctx context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.initialize(ctx)
}

// initialize performs the startup sequence. Callers must hold a.mu.
func (a *App) initialize(ctx context.Context) error {
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
	// PXPIPE diagnostics outlive snapshots, so the sink is process-level and
	// handed to every compiled pipeline.
	a.pxpipe = pxpipe.NewService(pxpipe.Transform{})
	var sealer *credentials.Sealer
	if len(a.cfg.CredentialKey) != 0 {
		sealer, err = credentials.NewSealer(a.cfg.CredentialKey)
		if err != nil {
			_ = store.Close()
			return fmt.Errorf("initialize credential sealer: %w", err)
		}
	}
	manager, err := runtime.NewManagerObserved(ctx, store.DB(), a.pxpipe.Observe, sealer)
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
	usage := telemetry.New(telemetry.NewSQLiteSink(store.DB()), snapshot.TelemetryOptions())
	usage.OptionsSource = func() telemetry.Options {
		current, _ := manager.Load()
		return current.TelemetryOptions()
	}
	a.details = telemetry.NewSQLiteDetailStore(store.DB(), snapshot.DetailSizeLimit())
	a.details.SizeLimit = func() int {
		current, _ := manager.Load()
		return current.DetailSizeLimit()
	}
	usage.WithDetails(a.details)
	a.usage = usage
	a.usageStarted = false
	a.events = controlevents.New()
	a.feed = controlevents.NewUsageFeed()
	binder := proxy.NewBinder(loadSnapshot(manager))
	binder.AllowLocal = a.cfg.AllowPrivateUpstreams
	specs := registry.Builtins()
	if err := a.initializeQuota(ctx, store, manager, specs, sealer); err != nil {
		_ = store.Close()
		return err
	}
	a.ingress = ingress.New(manager, ingress.Options{
		TransformFinalBody: func(ctx context.Context, snapshot *runtime.RuntimeSnapshot, protocol string, body []byte) ([]byte, error) {
			return snapshot.TransformBody(ctx, protocol, body, a.cfg.AllowPrivateUpstreams)
		},
		TranslateChat:      ingress.TranslateChat,
		TranslateResponses: ingress.TranslateResponses,
		TranslateMessages:  ingress.TranslateMessages,
		Candidates: func(snapshot *runtime.RuntimeSnapshot, model string) ([]routing.Account, bool) {
			return snapshot.ProviderCandidates(model)
		},
		AccountProvider: func(snapshot *runtime.RuntimeSnapshot, model, account, source string) (routing.ProviderRef, bool) {
			return snapshot.AccountProvider(model, account, source, ingress.CanTranslate)
		},
		CredentialFor: func(ctx context.Context, account string) (string, error) {
			if a.credReg == nil {
				return "", credentials.ErrKeyRequired
			}
			return a.credReg.Credential(ctx, account)
		},
		MaxBodyBytes:          a.cfg.MaxBodyBytes,
		CORSOrigins:           a.cfg.CORSOrigins,
		State:                 manager.State(),
		AllowPrivateUpstreams: a.cfg.AllowPrivateUpstreams,
		ClientFor: func(connectionID string) *http.Client {
			return binder.ClientFor(context.Background(), connectionID)
		},
		OnRequestComplete: func(outcome ingress.RequestOutcome) {
			// An unanswered outcome is accounting only: the client left before the
			// response was delivered, so no request completed. It is gated on
			// the cause, not the status number: an upstream may answer 499 itself.
			// Neither the live event bus nor the "request.completed" usage feed
			// carries it; usage persistence below still does.
			if !outcome.Unanswered {
				a.events.Publish("request.completed", map[string]any{
					"requestId":    outcome.RequestID,
					"providerId":   outcome.ProviderID,
					"modelId":      outcome.ModelID,
					"connectionId": outcome.ConnectionID,
					"status":       outcome.Status,
					"durationMs":   outcome.Duration.Milliseconds(),
				})
				a.feed.Publish(controlevents.UsageEvent{
					RequestID: outcome.RequestID, ProviderID: outcome.ProviderID, ModelID: outcome.ModelID,
					ConnectionID: outcome.ConnectionID, APIKeyID: outcome.APIKeyID, Status: outcome.Status,
					DurationMS: outcome.Duration.Milliseconds(), InputTokens: outcome.InputTokens,
					OutputTokens: outcome.OutputTokens, Termination: outcome.Termination, UsageUnobserved: outcome.UsageUnobserved,
					CreatedAt: time.Now().UTC(),
				})
			}
			usage.Record(telemetry.Event{
				Class:        telemetry.Critical,
				RequestID:    outcome.RequestID,
				ProviderID:   outcome.ProviderID,
				ModelID:      outcome.ModelID,
				ConnectionID: outcome.ConnectionID,
				APIKeyID:     outcome.APIKeyID,
				Status:       outcome.Status,
				InputTokens:  outcome.InputTokens,
				OutputTokens: outcome.OutputTokens,
				CacheRead:    outcome.CacheRead,
				CacheWrite:   outcome.CacheWrite,
				DurationMS:   outcome.Duration.Milliseconds(),
				Termination:  outcome.Termination, UsageUnobserved: outcome.UsageUnobserved,
			})
		},
	})
	if err := a.initializeAdmin(ctx, store, manager, specs); err != nil {
		_ = store.Close()
		return err
	}
	a.ready.Store(true)
	return nil
}

// initializeAdmin provisions the first dashboard admin when none exists and
// builds the session-gated control API. A missing bootstrap credential is not an
// error while no admin exists: the control API stays mounted but returns 409
// until an operator provisions one (RUNBOOK §bootstrap).
func (a *App) initializeAdmin(ctx context.Context, store *sqlite.Store, manager *runtime.Manager, specs []registry.Spec) error {
	a.admin = adminauth.NewStore(store.DB())
	if (a.cfg.AdminBootstrapUsername == "") != (a.cfg.AdminBootstrapPassword == "") {
		return errors.New("initial admin bootstrap username and password must be configured together")
	}
	if a.cfg.AdminBootstrapUsername != "" && a.cfg.AdminBootstrapPassword != "" {
		has, err := a.admin.HasAdmin(ctx)
		if err != nil {
			return fmt.Errorf("check admin account: %w", err)
		}
		if !has {
			if _, err := a.admin.Bootstrap(ctx, newAdminID(), a.cfg.AdminBootstrapUsername, a.cfg.AdminBootstrapPassword); err != nil {
				return fmt.Errorf("bootstrap admin account: %w", err)
			}
			a.log.Info("provisioned initial admin account", "username", a.cfg.AdminBootstrapUsername)
		}
	}
	sessions := adminauth.NewSessionManager(a.cfg.AdminSessionTTL)
	a.control = controlapi.New(controlapi.Options{
		Accounts:              a.admin,
		Sessions:              sessions,
		Settings:              manager,
		Keys:                  manager,
		DB:                    store.DB(),
		Runtime:               manager,
		Providers:             specs,
		Credentials:           a.creds,
		CredentialRegistry:    a.credReg,
		ProviderCatalog:       manager,
		PoolBindings:          manager,
		Combos:                manager,
		QuotaRefresher:        a.RefreshQuota,
		AllowPrivateUpstreams: a.cfg.AllowPrivateUpstreams,
		PublicOrigin:          a.cfg.PublicOrigin,
		PXPIPE:                a.pxpipe,
		DiscoveryClient:       &discovery.Client{},
		Telemetry:             a.usage,
		Events:                a.events,
		UsageFeed:             a.feed,
		Logs:                  a.logs,
		ActiveRequests:        func() int64 { return a.active.Load() },
		Ready:                 a.ready.Load,
		Build:                 buildinfo.Current(),
		DataDir:               a.cfg.DataDir,
		Backup:                a.CreateBackup,
		Restore:               a.QueueRestore,
	})
	return nil
}

// newAdminID returns a stable, non-secret admin identifier.
func newAdminID() string { return "admin_" + uuid.NewString() }

// initializeQuota wires the production quota path: provider usage reads are
// normalized and published to RuntimeState, reusing the same durable,
// singleflight-refreshed credentials inference uses. It is skipped when no
// credential master key is configured, in which case there are no stored
// provider connections to read quota for.
func (a *App) initializeQuota(ctx context.Context, store *sqlite.Store, manager *runtime.Manager, specs []registry.Spec, sealer *credentials.Sealer) error {
	if sealer == nil {
		return nil
	}
	credentialStore, err := credentials.NewStore(store.DB(), sealer)
	if err != nil {
		return fmt.Errorf("initialize credential store: %w", err)
	}
	credentialPolicies := make(map[string]registry.Spec)
	for _, spec := range specs {
		credentialPolicies[spec.ID] = spec
	}
	credentialRegistry := credentials.NewRegistry(credentialStore, nil)
	credentialRegistry.SetCredentialNameResolver(func(connection credentials.Connection) (string, string) {
		policy := credentialPolicies[connection.ProviderID]
		return policy.CredentialName, policy.CredentialExpiryName
	})
	// Refreshes are network calls: they need the same pooled, SSRF-guarded client
	// inference uses, never nil. A nil client here failed every refresh at request
	// time, which surfaced as a 5xx on inference once a token crossed its lead.
	refreshClient := transport.NewSSRFProtectedClient()
	if a.cfg.AllowPrivateUpstreams {
		refreshClient = transport.NewTrustedLocalSSRFProtectedClient()
	}
	resolver, err := oauthrefresh.Resolver(refreshClient)
	if err != nil {
		return fmt.Errorf("install credential refresh: %w", err)
	}
	credentialRegistry.SetExchangerResolver(resolver)
	// The control plane shares this store/registry so provider CRUD publishes
	// credentials to the same memory-first state inference reads (SPEC §19).
	a.creds, a.credReg = credentialStore, credentialRegistry
	credentialStore.SetPublisher(manager.UpdateProviders)
	// Seed the memory-first credential registry so quota reads do not fail with
	// ErrNotFound before the first refresh.
	nodes, err := credentialStore.ListNodes(ctx)
	if err != nil {
		return err
	}
	seeded := make(map[string]bool)
	for _, node := range nodes {
		if seeded[node.ProviderID] {
			continue
		}
		seeded[node.ProviderID] = true
		// A Google-family provider cannot refresh or sign in without the
		// operator-supplied OAuth client (docs/SPEC.md 19.1). Say so once at
		// startup instead of failing the first request that needs it.
		if node.ProviderID == "gemini-cli" || node.ProviderID == "antigravity" {
			if _, _, err := onboarding.GoogleClientFromEnvironment(); err != nil {
				a.log.Warn("google-family provider enabled without an OAuth client; sign-in and refresh will fail until it is configured",
					"provider", node.ProviderID, "error", err)
			}
		}
		if _, err := credentialRegistry.Load(ctx, node.ProviderID); err != nil {
			a.log.Warn("provider credentials unavailable; skipping startup seed", "provider", node.ProviderID, "error", err)
		}
	}
	// Codex and Claude keep their dedicated engines; every other provider whose
	// quota LiteRouter reads goes through the shared usage package, so the reader
	// set is the reference dispatcher's table rather than a second hand-kept list.
	usageClient := transport.NewSSRFProtectedClient()
	if a.cfg.AllowPrivateUpstreams {
		usageClient = transport.NewTrustedLocalSSRFProtectedClient()
	}
	clients, err := providerusage.Readers(usageClient, nil)
	if err != nil {
		return fmt.Errorf("install quota readers: %w", err)
	}
	clients["codex"] = quota.CodexClient{}
	clients["claude"] = quota.ClaudeClient{Client: &claudeprovider.UsageClient{}}
	observer := quota.Observer{
		Clients:   clients,
		Publisher: manager.State(),
	}
	a.quota = quota.NewService(credentialStore, credentialRegistry, observer).WithSeeder(credentialRegistry)
	return nil
}

// pruneDetails deletes request details older than the retention window at
// startup and hourly thereafter (PRD-OBS-002). Errors are logged, never fatal.
func (a *App) pruneDetails(ctx context.Context) {
	store := a.details
	if store == nil {
		return
	}
	prune := func() {
		if _, err := store.PruneNow(ctx); err != nil && ctx.Err() == nil {
			a.log.Warn("prune request details", "error", err)
		}
	}
	prune()
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			prune()
		}
	}
}

func (a *App) Handler() http.Handler {
	mux := http.NewServeMux()
	inferenceMux := http.NewServeMux()
	if a.ingress != nil {
		a.ingress.Attach(inferenceMux)
	}
	if a.control != nil {
		a.control.Attach(mux)
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
	inference := withBodyLimit(a.cfg.MaxBodyBytes, a.trackInference(inferenceMux))
	ui, err := newStaticUI(a.cfg.UIDir)
	if err != nil {
		a.log.Error("static ui disabled", "dir", a.cfg.UIDir, "error", err)
	}
	if ui == nil {
		mux.Handle("/", inference)
	} else {
		// Inference routes win; only GET/HEAD requests no inference route claims
		// reach the static UI, so API paths never receive index.html.
		mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet || r.Method == http.MethodHead {
				if _, pattern := inferenceMux.Handler(r); pattern == "" && !reservedPath(r.URL.Path) {
					ui.ServeHTTP(w, r)
					return
				}
			}
			inference.ServeHTTP(w, r)
		})
	}
	return withRequestID(a.quiescenceGate(mux))
}

func (a *App) quiescenceGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.quiescent.Load() && r.URL.Path != "/health/live" {
			http.Error(w, "restore in progress", http.StatusServiceUnavailable)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// DatabasePath returns the live database path.
func (a *App) DatabasePath() string {
	if a.store == nil {
		return filepath.Join(a.cfg.DataDir, "routeweft.sqlite")
	}
	return a.store.Path()
}

// CreateBackup writes a validated online backup artifact (SPEC §25).
func (a *App) CreateBackup(ctx context.Context, output string) (backup.Metadata, error) {
	if a.store == nil {
		return backup.Metadata{}, errors.New("database is not initialized")
	}
	return backup.Create(ctx, a.store, output)
}

// StageRestore validates and stages a restore candidate without touching live
// state. The returned candidate must be activated with ActivateRestore or
// discarded.
func (a *App) StageRestore(ctx context.Context, input string) (*backup.Candidate, error) {
	if a.store == nil {
		return nil, errors.New("database is not initialized")
	}
	return backup.StageRestore(ctx, input, a.cfg.DataDir)
}

// QueueRestore validates the uploaded file and schedules activation after the
// current HTTP request returns. The server then drains requests and telemetry
// before replacing any live SQLite file (SPEC §25).
func (a *App) QueueRestore(ctx context.Context, input string) (backup.Metadata, error) {
	if !a.serving.Load() {
		return backup.Metadata{}, errors.New("restore activation requires a running server")
	}
	candidate, err := a.StageRestore(ctx, input)
	if err != nil {
		return backup.Metadata{}, err
	}
	if !a.quiescent.CompareAndSwap(false, true) {
		candidate.Discard()
		return backup.Metadata{}, errors.New("another restore is already in progress")
	}
	a.ready.Store(false)
	select {
	case a.restoreCh <- candidate:
		return candidate.Metadata, nil
	default:
		a.ready.Store(true)
		a.quiescent.Store(false)
		candidate.Discard()
		return backup.Metadata{}, errors.New("restore queue is busy")
	}
}

func (a *App) activateRestore(ctx context.Context, candidate *backup.Candidate) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	database := a.DatabasePath()
	rollback := database + ".pre-restore-" + time.Now().UTC().Format("20060102T150405.000000000Z")

	// The server and its request handlers have drained. Finish accepted Usage,
	// then close the only live SQLite writer before file replacement.
	a.flushTelemetry()
	if a.usage != nil && a.usageStarted {
		a.usage.Close()
		a.usage.Wait()
	}
	if err := sqlite.BackupTo(ctx, a.store, rollback); err != nil {
		return fmt.Errorf("preserve rollback database: %w", err)
	}
	if err := syncDir(filepath.Dir(database)); err != nil {
		return err
	}
	if err := a.store.Close(); err != nil {
		return fmt.Errorf("close live database: %w", err)
	}
	if err := replaceDatabase(candidate.Path, database); err != nil {
		if rollbackErr := restoreRollback(rollback, database); rollbackErr != nil {
			return &restoreRecoveryError{err: fmt.Errorf("activate candidate: %v; restore rollback: %w", err, rollbackErr)}
		}
		return err
	}
	candidate.Discard()
	if err := a.initialize(ctx); err != nil {
		// A candidate that passed validation can still fail to initialize due to
		// an environmental error. Restore the preserved DB before reopening.
		if rollbackErr := restoreRollback(rollback, database); rollbackErr != nil {
			return &restoreRecoveryError{err: fmt.Errorf("reinitialize restored database: %v; rollback failed: %w", err, rollbackErr)}
		}
		if reopenErr := a.initialize(ctx); reopenErr != nil {
			return &restoreRecoveryError{err: fmt.Errorf("reinitialize restored database: %v; reopen rollback database: %w", err, reopenErr)}
		}
		return fmt.Errorf("restore activation rejected; previous database restored: %w", err)
	}
	return nil
}

// replaceDatabase moves the validated candidate over database, removing stale
// WAL sidecars so no pre-restore pages survive the swap.
func replaceDatabase(candidatePath, database string) error {
	sidecars := []string{database + "-wal", database + "-shm"}
	for _, path := range sidecars {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("clear stale database sidecar %s: %w", path, err)
		}
	}
	if err := os.Rename(candidatePath, database); err != nil {
		return fmt.Errorf("activate restore candidate: %w", err)
	}
	return syncDir(filepath.Dir(database))
}

func restoreRollback(rollback, database string) error {
	source, err := os.Open(rollback)
	if err != nil {
		return err
	}
	defer source.Close()
	target, err := os.CreateTemp(filepath.Dir(database), ".routeweft-rollback-*.sqlite")
	if err != nil {
		return err
	}
	stage := target.Name()
	if err := target.Chmod(0o600); err != nil {
		_ = target.Close()
		_ = os.Remove(stage)
		return err
	}
	_, copyErr := io.Copy(target, source)
	if copyErr == nil {
		copyErr = target.Sync()
	}
	closeErr := target.Close()
	if copyErr != nil {
		_ = os.Remove(stage)
		return copyErr
	}
	if closeErr != nil {
		_ = os.Remove(stage)
		return closeErr
	}
	for _, sidecar := range []string{database + "-wal", database + "-shm"} {
		if err := os.Remove(sidecar); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(stage)
			return err
		}
	}
	if err := os.Rename(stage, database); err != nil {
		_ = os.Remove(stage)
		return err
	}
	return syncDir(filepath.Dir(database))
}

func (a *App) flushTelemetry() {
	if a.usage == nil || !a.usageStarted {
		return
	}
	flushCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_ = a.usage.FlushNow(flushCtx)
}

func syncDir(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	err = dir.Sync()
	_ = dir.Close()
	if err != nil {
		return fmt.Errorf("sync database directory: %w", err)
	}
	return nil
}

func (a *App) trackInference(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if isInferencePath(r.URL.Path) {
			a.active.Add(1)
			defer a.active.Add(-1)
		}
		next.ServeHTTP(w, r)
	})
}

// waitInferenceIdle waits up to d for in-flight inference handlers to return.
func (a *App) waitInferenceIdle(d time.Duration) {
	deadline := time.Now().Add(d)
	for a.active.Load() > 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
}

func isInferencePath(path string) bool {
	return path == "/v1" || strings.HasPrefix(path, "/v1/") ||
		path == "/v1beta" || strings.HasPrefix(path, "/v1beta/") ||
		path == "/responses" || strings.HasPrefix(path, "/responses/") ||
		path == "/messages" || strings.HasPrefix(path, "/messages/") ||
		path == "/codex" || strings.HasPrefix(path, "/codex/")
}

var errRestoreApplied = errors.New("restore activated")

// restoreRecoveryError means activation failed after the live database was
// already replaced or closed, so automatic resume is unsafe; the process should
// stop and the preserved rollback database should be inspected.
type restoreRecoveryError struct{ err error }

func (e *restoreRecoveryError) Error() string { return e.err.Error() }
func (e *restoreRecoveryError) Unwrap() error { return e.err }

func (a *App) Serve(ctx context.Context) error {
	for {
		if !a.ready.Load() {
			if err := a.Initialize(ctx); err != nil {
				return err
			}
		}
		err := a.serveOnce(ctx)
		if errors.Is(err, errRestoreApplied) {
			continue
		}
		return err
	}
}

func (a *App) serveOnce(ctx context.Context) error {
	srv := &http.Server{
		Addr:              a.cfg.Listen,
		Handler:           a.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	errCh := make(chan error, 1)
	// Background services must outlive the signal context: SIGTERM cancels ctx
	// first, and the batcher's final drain has to run after in-flight requests
	// finish (SPEC §24 steps 3-5), or their usage records are enqueued onto a
	// batcher that already exited and are lost. Every exit path below calls
	// stopServices after the HTTP drain.
	serviceCtx, stopServices := context.WithCancel(context.WithoutCancel(ctx))
	a.serving.Store(true)
	if a.usage != nil {
		// SPEC §21: the batcher is the only usage writer; the request path only
		// enqueues, so a normal success never synchronously writes SQLite.
		a.usageStarted = true
		go a.usage.Run(serviceCtx)
		// PRD-OBS-002 bounded retention: prune expired request details hourly.
		go a.pruneDetails(serviceCtx)
	}
	if a.quota != nil {
		// PRD-QUOTA-001: provider quota refresh runs in the background and must not
		// block normal inference. A refresh error is recorded, never fatal.
		go a.quota.Run(serviceCtx, a.cfg.QuotaRefreshInterval)
	}
	go func() {
		a.log.Info("listening", "addr", a.cfg.Listen)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	select {
	case err := <-errCh:
		a.serving.Store(false)
		stopServices()
		return fmt.Errorf("serve: %w", err)
	case candidate := <-a.restoreCh:
		a.ready.Store(false)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		shutdownErr := srv.Shutdown(shutdownCtx)
		cancel()
		if shutdownErr != nil {
			_ = srv.Close()
		}
		stopServices()
		if a.usage != nil {
			a.usage.Close()
			a.usage.Wait()
		}
		a.serving.Store(false)
		if err := a.activateRestore(context.Background(), candidate); err != nil {
			candidate.Discard()
			a.log.Error("restore activation failed", "error", err)
			var recoveryErr *restoreRecoveryError
			if errors.As(err, &recoveryErr) {
				return err
			}
			if a.store != nil {
				_ = a.store.Close()
			}
			if reopenErr := a.initialize(context.Background()); reopenErr != nil {
				return fmt.Errorf("restore failed: %v; resume service: %w", err, reopenErr)
			}
		}
		a.quiescent.Store(false)
		if shutdownErr != nil {
			a.log.Warn("restore shutdown exceeded drain deadline; active requests were closed", "error", shutdownErr)
		}
		return errRestoreApplied
	case <-ctx.Done():
		a.ready.Store(false)
		grace := 10 * time.Second
		if a.drainGrace > 0 {
			grace = a.drainGrace
		}
		shutdownCtx, cancel := context.WithTimeout(context.Background(), grace)
		err := srv.Shutdown(shutdownCtx)
		cancel()
		if err != nil {
			// The drain deadline passed: cut the connections, which cancels each
			// request context, and give the handlers a bounded moment to publish
			// their (client_abandoned) records before the batcher takes its final
			// drain. Records published after that point are not accepted.
			_ = srv.Close()
			a.waitInferenceIdle(5 * time.Second)
		}
		stopServices()
		if a.usage != nil {
			a.usage.Close()
			a.usage.Wait()
		}
		a.serving.Store(false)
		if err != nil {
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

// reservedPath marks API namespaces that must 404 as JSON-API paths instead of
// falling through to the static UI.
func reservedPath(p string) bool {
	for _, prefix := range []string{"/admin/", "/v1/", "/v1beta/", "/api/"} {
		if strings.HasPrefix(p, prefix) {
			return true
		}
	}
	return false
}
