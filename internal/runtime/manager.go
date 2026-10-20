package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/raufimusaddiq/routeweft/internal/auth"
	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/transforms/pxpipe"
)

var ErrUninitialized = errors.New("runtime snapshot is not initialized")

// Manager publishes immutable snapshots. Writes serialize; readers do atomic loads.
type Manager struct {
	db       *sql.DB
	mutation chan struct{}
	active   atomic.Pointer[RuntimeSnapshot]
	version  atomic.Uint64
	state    *RuntimeState
	// compiler carries process-level hooks shared by every snapshot.
	compiler Compiler
}

// NewManager loads keyless configuration; sealed rows require NewManagerObserved.
func NewManager(ctx context.Context, db *sql.DB) (*Manager, error) {
	return NewManagerObserved(ctx, db, nil, nil)
}

// NewManagerObserved is NewManager with a PXPIPE diagnostics sink that is part
// of the initial snapshot and of every later one. The sealer is used only during startup.
func NewManagerObserved(ctx context.Context, db *sql.DB, observePXPIPE func(pxpipe.Diagnostic), sealer *credentials.Sealer) (*Manager, error) {
	m := &Manager{mutation: make(chan struct{}, 1), db: db, state: NewState(), compiler: Compiler{PXPIPEObserver: observePXPIPE}}
	config, err := loadConfig(ctx, db, sealer)
	if err != nil {
		return nil, err
	}
	snapshot, err := m.compiler.Compile(config, 1)
	if err != nil {
		return nil, fmt.Errorf("compile initial runtime snapshot: %w", err)
	}
	m.version.Store(1)
	m.active.Store(snapshot)
	return m, nil
}

func (m *Manager) Load() (*RuntimeSnapshot, error) {
	snapshot := m.active.Load()
	if snapshot == nil {
		return nil, ErrUninitialized
	}
	return snapshot, nil
}

func (m *Manager) State() *RuntimeState { return m.state }

// Update compiles before commit and publishes only after durable commit succeeds.
func (m *Manager) Update(ctx context.Context, mutate func(*Candidate) error) (*RuntimeSnapshot, error) {
	return m.update(ctx, mutate, nil)
}

func (m *Manager) update(ctx context.Context, mutate func(*Candidate) error, persist func(*sql.Tx) error) (*RuntimeSnapshot, error) {
	if err := m.lockMutation(ctx); err != nil {
		return nil, err
	}
	defer m.unlockMutation()
	return m.updateLocked(ctx, mutate, persist)
}

func (m *Manager) updateLocked(ctx context.Context, mutate func(*Candidate) error, persist func(*sql.Tx) error) (*RuntimeSnapshot, error) {
	current, err := m.Load()
	if err != nil {
		return nil, err
	}
	candidate := &Candidate{Settings: current.Settings(), APIKeys: current.APIKeys().Records(), Models: cloneModelsForConfig(current.models), Aliases: cloneAliases(current.aliases), DisabledModels: cloneDisabled(current.disabledModels), Combos: current.Combos(), PoolBindings: cloneStringMap(current.poolBindings), ProxyPools: current.ProxyPools()}
	candidate.Providers = current.providerConfig.Clone()
	if err := mutate(candidate); err != nil {
		return nil, err
	}
	config := Config{Revision: current.ConfigRevision() + 1, Settings: cloneSettings(candidate.Settings), APIKeys: append([]auth.Entry(nil), candidate.APIKeys...), Models: append([]Model(nil), candidate.Models...), Aliases: cloneAliases(candidate.Aliases), DisabledModels: cloneDisabled(candidate.DisabledModels), Combos: append([]Combo(nil), candidate.Combos...), PoolBindings: cloneStringMap(candidate.PoolBindings), ProxyPools: append([]ProxyPool(nil), candidate.ProxyPools...)}
	config.Providers = candidate.Providers.Clone()
	nextVersion := m.version.Load() + 1
	compiled, err := m.compiler.Compile(config, nextVersion)
	if err != nil {
		return nil, err
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin config transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if persist != nil {
		if err := persist(tx); err != nil {
			return nil, err
		}
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM settings"); err != nil {
		return nil, fmt.Errorf("replace settings: %w", err)
	}
	for key, value := range config.Settings {
		const upsert = "INSERT INTO settings (key,value) VALUES (?,?) " +
			"ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')"
		if _, err := tx.ExecContext(ctx, upsert, key, value); err != nil {
			return nil, fmt.Errorf("persist setting %q: %w", key, err)
		}
	}
	if err := persistCatalog(ctx, tx, config, removedModels(current.models, config.Models)); err != nil {
		return nil, err
	}
	if err := persistCombos(ctx, tx, config.Combos); err != nil {
		return nil, err
	}
	const revisionUpsert = "INSERT INTO meta (key,value) VALUES ('config_revision',?) " +
		"ON CONFLICT(key) DO UPDATE SET value=excluded.value,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now')"
	if _, err := tx.ExecContext(ctx, revisionUpsert, fmt.Sprint(config.Revision)); err != nil {
		return nil, fmt.Errorf("persist config revision: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit configuration: %w", err)
	}
	m.version.Store(nextVersion)
	m.active.Store(compiled)
	return compiled, nil
}

// Version reports how many times the snapshot has been recompiled. It exists so a
// caller can observe how much work a mutation caused: every Update recompiles the
// whole candidate, so a batch that publishes per item shows up here.
func (m *Manager) Version() uint64 {
	return m.version.Load()
}

func loadConfig(ctx context.Context, db *sql.DB, sealer *credentials.Sealer) (Config, error) {
	return loadConfigUsing(ctx, db, func(ctx context.Context, db *sql.DB) (credentials.Configuration, error) {
		return credentials.LoadConfiguration(ctx, db, sealer)
	})
}

func loadConfigUsing(ctx context.Context, db *sql.DB, providers func(context.Context, *sql.DB) (credentials.Configuration, error)) (Config, error) {
	config := Config{Settings: defaultSettingsCopy()}
	config.Aliases = make(map[string]ModelRef)
	config.DisabledModels = make(map[string]struct{})
	var raw string
	err := db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='config_revision'").Scan(&raw)
	switch {
	case err == nil:
		var revision uint64
		if _, err := fmt.Sscan(raw, &revision); err != nil {
			return Config{}, fmt.Errorf("parse config revision: %w", err)
		}
		config.Revision = revision
	case errors.Is(err, sql.ErrNoRows):
	default:
		return Config{}, fmt.Errorf("load config revision: %w", err)
	}
	rows, err := db.QueryContext(ctx, "SELECT key,value FROM settings ORDER BY key")
	if err != nil {
		return Config{}, fmt.Errorf("load settings: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return Config{}, err
		}
		config.Settings[key] = value
	}
	if err := rows.Err(); err != nil {
		return Config{}, err
	}
	if err := loadCatalog(ctx, db, &config); err != nil {
		return Config{}, err
	}
	bindings, err := loadPoolBindings(ctx, db)
	if err != nil {
		return Config{}, err
	}
	config.PoolBindings = bindings
	pools, err := loadProxyPools(ctx, db)
	if err != nil {
		return Config{}, err
	}
	config.ProxyPools = pools
	combos, err := loadCombos(ctx, db)
	if err != nil {
		return Config{}, err
	}
	config.Combos = combos
	config.Providers, err = providers(ctx, db)
	if err != nil {
		return Config{}, err
	}
	return config, nil
}

func (m *Manager) lockMutation(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case m.mutation <- struct{}{}:
		if err := ctx.Err(); err != nil {
			m.unlockMutation()
			return err
		}
		return nil
	}
}

func (m *Manager) unlockMutation() { <-m.mutation }

// ValidateStoredConfiguration checks backup structure without publishing a serving snapshot.
// Sealed settings are authenticated later by application startup with its credential key.
func ValidateStoredConfiguration(ctx context.Context, db *sql.DB) (uint64, error) {
	config, err := loadConfigUsing(ctx, db, credentials.LoadMetadata)
	if err != nil {
		return 0, err
	}
	if _, err := (Compiler{}).Compile(config, 1); err != nil {
		return 0, err
	}
	return config.Revision, nil
}
