package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
)

var ErrUninitialized = errors.New("runtime snapshot is not initialized")

// Manager publishes immutable snapshots. Writes serialize; readers do atomic loads.
type Manager struct {
	db      *sql.DB
	mu      sync.Mutex
	active  atomic.Pointer[RuntimeSnapshot]
	version atomic.Uint64
	state   *RuntimeState
}

// NewManager loads durable configuration and compiles the initial snapshot.
func NewManager(ctx context.Context, db *sql.DB) (*Manager, error) {
	m := &Manager{db: db, state: NewState()}
	config, err := loadConfig(ctx, db)
	if err != nil {
		return nil, err
	}
	snapshot, err := (Compiler{}).Compile(config, 1)
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
	m.mu.Lock()
	defer m.mu.Unlock()

	current, err := m.Load()
	if err != nil {
		return nil, err
	}
	candidate := &Candidate{Settings: current.Settings()}
	if err := mutate(candidate); err != nil {
		return nil, err
	}
	config := Config{Revision: current.ConfigRevision() + 1, Settings: cloneSettings(candidate.Settings)}
	nextVersion := m.version.Load() + 1
	compiled, err := (Compiler{}).Compile(config, nextVersion)
	if err != nil {
		return nil, err
	}
	tx, err := m.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin config transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
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

func loadConfig(ctx context.Context, db *sql.DB) (Config, error) {
	config := Config{Settings: defaultSettingsCopy()}
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
	return config, rows.Err()
}
