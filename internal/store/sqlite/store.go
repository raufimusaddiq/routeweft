// Package sqlite implements Routeweft's single-process SQLite store.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	moderncsqlite "modernc.org/sqlite"
)

const (
	driverName = "sqlite"
	busyMS     = 5000
)

// Store owns one SQLite connection pool and the Routeweft database path.
type Store struct {
	db   *sql.DB
	path string
}

// Open opens a Routeweft-owned DB and enforces WAL, foreign keys and busy timeout.
func Open(ctx context.Context, path string) (*Store, error) {
	if path == "" {
		return nil, errors.New("sqlite path is required")
	}
	if path == ":memory:" || strings.HasPrefix(path, "file::memory:") {
		return nil, errors.New("Routeweft requires a persistent SQLite database")
	}
	if !strings.HasPrefix(path, "file:") {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return nil, fmt.Errorf("create data directory: %w", err)
		}
	}
	db, err := sql.Open(driverName, makeDSN(path))
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	s := &Store{db: db, path: path}
	if err := s.configure(ctx); err != nil {
		_ = db.Close()
		return nil, err
	}
	if !strings.HasPrefix(path, "file:") {
		if err := os.Chmod(path, 0o600); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("restrict database permissions: %w", err)
		}
	}
	return s, nil
}

func makeDSN(path string) string {
	base := path
	if !strings.HasPrefix(path, "file:") {
		base = (&url.URL{Scheme: "file", Path: path}).String()
	}
	separator := "?"
	if strings.Contains(base, "?") {
		separator = "&"
	}
	return base + separator + url.Values{
		"_pragma": {fmt.Sprintf("busy_timeout(%d)", busyMS), "foreign_keys(1)", "journal_mode(WAL)", "synchronous(NORMAL)"},
	}.Encode()
}

func (s *Store) configure(ctx context.Context) error {
	if err := s.db.PingContext(ctx); err != nil {
		return fmt.Errorf("ping sqlite: %w", err)
	}
	var journal string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
		return fmt.Errorf("read journal mode: %w", err)
	}
	if !strings.EqualFold(journal, "wal") {
		return fmt.Errorf("sqlite journal mode is %q, want WAL", journal)
	}
	var foreignKeys int
	if err := s.db.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&foreignKeys); err != nil {
		return fmt.Errorf("read foreign key setting: %w", err)
	}
	if foreignKeys != 1 {
		return errors.New("sqlite foreign key enforcement is disabled")
	}
	var busy int
	if err := s.db.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil {
		return fmt.Errorf("read busy timeout: %w", err)
	}
	if busy < busyMS {
		return fmt.Errorf("sqlite busy timeout %dms is below required %dms", busy, busyMS)
	}
	return nil
}

// DB exposes the standard-library handle to repository-owned store modules.
func (s *Store) DB() *sql.DB { return s.db }

// Path returns the configured Routeweft database path.
func (s *Store) Path() string { return s.path }

// ConfigureBackup prepares an online backup destination's journal mode for
// safe reopening without a destination WAL dependency.
func (s *Store) ConfigureBackup(ctx context.Context) error {
	var mode string
	if err := s.db.QueryRowContext(ctx, "PRAGMA journal_mode=DELETE").Scan(&mode); err != nil {
		return fmt.Errorf("configure backup journal mode: %w", err)
	}
	if !strings.EqualFold(mode, "delete") {
		return fmt.Errorf("backup journal mode is %q, want DELETE", mode)
	}
	return nil
}

// CloseBackup switches a staged artifact to a self-contained rollback-journal
// database before closing it for publication.
func (s *Store) CloseBackup(ctx context.Context) error {
	if err := s.ConfigureBackup(ctx); err != nil {
		_ = s.db.Close()
		return err
	}
	return s.db.Close()
}

// Close checkpoints WAL and closes the connection pool.
func (s *Store) Close() error {
	if _, err := s.db.Exec("PRAGMA wal_checkpoint(TRUNCATE)"); err != nil {
		_ = s.db.Close()
		return fmt.Errorf("checkpoint sqlite: %w", err)
	}
	return s.db.Close()
}

// IntegrityCheck runs SQLite integrity_check and requires the single result "ok".
func (s *Store) IntegrityCheck(ctx context.Context) error {
	rows, err := s.db.QueryContext(ctx, "PRAGMA integrity_check")
	if err != nil {
		return fmt.Errorf("integrity check: %w", err)
	}
	defer rows.Close()
	count := 0
	for rows.Next() {
		var result string
		if err := rows.Scan(&result); err != nil {
			return fmt.Errorf("read integrity check: %w", err)
		}
		count++
		if result != "ok" {
			return fmt.Errorf("sqlite integrity check failed: %s", result)
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("sqlite integrity check returned %d results", count)
	}
	return nil
}

// Backup writes an online-consistent SQLite backup without replacing an
// existing destination.
func (s *Store) Backup(ctx context.Context, destination string) error {
	return BackupTo(ctx, s, destination)
}

// BackupTo writes an online-consistent copy of source into destination. The
// destination must not already exist, so a rollback copy can be produced
// without risking an existing artifact.
func BackupTo(ctx context.Context, source *Store, destination string) error {
	if source == nil {
		return errors.New("backup source is required")
	}
	s := source
	if destination == "" || destination == s.path {
		return errors.New("backup destination must be non-empty and differ from live database")
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		return fmt.Errorf("create backup directory: %w", err)
	}
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("backup destination already exists: %s", destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("check backup destination: %w", err)
	}
	backup, err := os.CreateTemp(filepath.Dir(destination), ".routeweft-backup-*.sqlite")
	if err != nil {
		return fmt.Errorf("stage backup: %w", err)
	}
	stage := backup.Name()
	if err := backup.Close(); err != nil {
		_ = os.Remove(stage)
		return fmt.Errorf("close staged backup: %w", err)
	}
	defer os.Remove(stage)
	conn, err := s.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire sqlite connection: %w", err)
	}
	defer conn.Close()
	err = conn.Raw(func(driverConn any) error {
		backuper, ok := driverConn.(interface {
			NewBackup(string) (*moderncsqlite.Backup, error)
		})
		if !ok {
			return errors.New("sqlite driver does not support online backup")
		}
		uri := (&url.URL{Scheme: "file", Path: stage}).String()
		b, err := backuper.NewBackup(uri)
		if err != nil {
			return err
		}
		for more := true; more; {
			if err := ctx.Err(); err != nil {
				_ = b.Finish()
				return err
			}
			more, err = b.Step(-1)
			if err != nil {
				_ = b.Finish()
				return err
			}
		}
		return b.Finish()
	})
	if err != nil {
		return fmt.Errorf("backup database: %w", err)
	}
	if err := os.Chmod(stage, 0o600); err != nil {
		return fmt.Errorf("restrict backup permissions: %w", err)
	}
	if err := os.Link(stage, destination); err != nil {
		return fmt.Errorf("activate backup file (destination must not exist): %w", err)
	}
	return nil
}
