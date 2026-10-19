package adminauth

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
)

// ErrNoAdmin is returned when the database has no admin account yet, which is
// the signal to run the one-time bootstrap (RUNBOOK §bootstrap).
var ErrNoAdmin = errors.New("no admin account exists")

// Account is one dashboard admin identity. PasswordHash is never exposed.
type Account struct {
	ID       string
	Username string
	hash     string
}

// Store persists admin accounts and verifies credentials.
type Store struct {
	db *sql.DB
}

// NewStore wraps the durable store.
func NewStore(db *sql.DB) *Store { return &Store{db: db} }

// HasAdmin reports whether any admin account exists.
func (s *Store) HasAdmin(ctx context.Context) (bool, error) {
	var count int
	if err := s.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM admin_users").Scan(&count); err != nil {
		return false, fmt.Errorf("count admin users: %w", err)
	}
	return count > 0, nil
}

// Bootstrap creates the first admin account. It fails when an account already
// exists so a stale bootstrap password cannot re-provision or overwrite one.
func (s *Store) Bootstrap(ctx context.Context, id, username, password string) (Account, error) {
	username = strings.TrimSpace(username)
	if strings.TrimSpace(id) == "" || username == "" {
		return Account{}, errors.New("admin ID and username are required")
	}
	hash, err := HashPassword(password)
	if err != nil {
		return Account{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Account{}, fmt.Errorf("begin admin bootstrap: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	var count int
	if err := tx.QueryRowContext(ctx, "SELECT COUNT(*) FROM admin_users").Scan(&count); err != nil {
		return Account{}, fmt.Errorf("check admin bootstrap state: %w", err)
	}
	if count != 0 {
		return Account{}, errors.New("an admin account already exists")
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO admin_users (id,username,password_hash) VALUES (?,?,?)", id, username, hash); err != nil {
		return Account{}, fmt.Errorf("create admin user: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return Account{}, fmt.Errorf("commit admin bootstrap: %w", err)
	}
	return Account{ID: id, Username: username, hash: hash}, nil
}

// Authenticate verifies a username/password pair. It returns ErrNoAdmin only
// when there is no admin at all; an unknown user or bad password is a plain
// false so callers cannot enumerate accounts.
func (s *Store) Authenticate(ctx context.Context, username, password string) (Account, bool, error) {
	var account Account
	var hash string
	err := s.db.QueryRowContext(ctx, "SELECT id,username,password_hash FROM admin_users WHERE username=?", strings.TrimSpace(username)).Scan(&account.ID, &account.Username, &hash)
	switch {
	case errors.Is(err, sql.ErrNoRows):
		has, hasErr := s.HasAdmin(ctx)
		if hasErr != nil {
			return Account{}, false, hasErr
		}
		if !has {
			return Account{}, false, ErrNoAdmin
		}
		return Account{}, false, nil
	case err != nil:
		return Account{}, false, fmt.Errorf("load admin user: %w", err)
	}
	if !VerifyPassword(hash, password) {
		return Account{}, false, nil
	}
	account.hash = hash
	return account, true, nil
}

// ChangePassword replaces one admin's password hash.
func (s *Store) ChangePassword(ctx context.Context, id, password string) error {
	hash, err := HashPassword(password)
	if err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, "UPDATE admin_users SET password_hash=?,updated_at=strftime('%Y-%m-%dT%H:%M:%fZ','now') WHERE id=?", hash, id)
	if err != nil {
		return fmt.Errorf("update admin password: %w", err)
	}
	if affected, err := result.RowsAffected(); err == nil && affected == 0 {
		return errors.New("admin account not found")
	}
	return nil
}
