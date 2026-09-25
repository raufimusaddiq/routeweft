// Package migrations owns versioned durable schema changes.
package migrations

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// Migration is one forward-only schema step applied exactly once.
type Migration struct {
	Version int
	Name    string
	SQL     string
}

// All returns the ordered migration set owned by Routeweft.
func All() []Migration { return []Migration{v1, v2, v3, v4} }

// LatestVersion is the schema version Routeweft compiles against.
func LatestVersion() int {
	all := All()
	return all[len(all)-1].Version
}

// Runner applies migrations to one SQLite database.
type Runner struct {
	db         *sql.DB
	migrations []Migration
}

// NewRunner builds a runner; with no explicit migrations it uses All().
func NewRunner(db *sql.DB, list ...Migration) *Runner {
	if len(list) == 0 {
		list = All()
	}
	sorted := make([]Migration, len(list))
	copy(sorted, list)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Version < sorted[j].Version })
	return &Runner{db: db, migrations: sorted}
}

// Apply runs pending migrations, one transaction each, recording each version.
func (r *Runner) Apply(ctx context.Context) error {
	if err := validateMigrations(r.migrations); err != nil {
		return err
	}
	if err := r.ensureMeta(ctx); err != nil {
		return err
	}
	current, err := r.currentVersion(ctx)
	if err != nil {
		return err
	}
	latest := 0
	for _, m := range r.migrations {
		if m.Version > latest {
			latest = m.Version
		}
	}
	if current > latest {
		return fmt.Errorf("database schema version %d is newer than supported %d", current, latest)
	}
	for _, m := range r.migrations {
		if m.Version <= current {
			continue
		}
		if err := r.apply(ctx, m); err != nil {
			return fmt.Errorf("apply migration %d (%s): %w", m.Version, m.Name, err)
		}
	}
	return nil
}

func validateMigrations(list []Migration) error {
	for i, migration := range list {
		if migration.Version != i+1 {
			return fmt.Errorf("migration versions must be contiguous from 1: index %d has version %d", i, migration.Version)
		}
		if migration.Name == "" || migration.SQL == "" {
			return fmt.Errorf("migration %d must have a name and SQL", migration.Version)
		}
	}
	return nil
}

// Version reports the applied schema version.
func (r *Runner) Version(ctx context.Context) (int, error) {
	if err := r.ensureMeta(ctx); err != nil {
		return 0, err
	}
	return r.currentVersion(ctx)
}

// HasVersionTable distinguishes a migrated Routeweft DB from an arbitrary
// SQLite file without mutating the candidate.
func (r *Runner) HasVersionTable(ctx context.Context) (bool, error) {
	var count int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`).Scan(&count)
	return count == 1, err
}

func (r *Runner) ensureMeta(ctx context.Context) error {
	_, err := r.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version     INTEGER PRIMARY KEY,
		name        TEXT NOT NULL,
		applied_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
	)`)
	if err != nil {
		return fmt.Errorf("create schema_migrations: %w", err)
	}
	return nil
}

func (r *Runner) currentVersion(ctx context.Context) (int, error) {
	var version sql.NullInt64
	if err := r.db.QueryRowContext(ctx, `SELECT MAX(version) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	if !version.Valid {
		return 0, nil
	}
	return int(version.Int64), nil
}

func (r *Runner) apply(ctx context.Context, m Migration) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, m.SQL); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations (version, name) VALUES (?, ?)`, m.Version, m.Name); err != nil {
		return err
	}
	return tx.Commit()
}
