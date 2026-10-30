// Package storetest seeds test databases with an already-migrated schema.
//
// Applying every migration to a fresh SQLite file is the dominant cost of most
// tests under -race (the driver's pointer instrumentation makes each schema
// reload slow), so a test that does not exercise migration itself copies a
// template migrated once per test binary. Only test code may import it.
package storetest

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

const fileName = "routeweft.sqlite"

var (
	once     sync.Once
	template []byte
	err      error
)

func build() ([]byte, error) {
	once.Do(func() {
		dir, mkErr := os.MkdirTemp("", "routeweft-storetest-")
		if mkErr != nil {
			err = mkErr
			return
		}
		defer os.RemoveAll(dir)
		path := filepath.Join(dir, fileName)
		ctx := context.Background()
		store, openErr := sqlite.Open(ctx, path)
		if openErr != nil {
			err = openErr
			return
		}
		if applyErr := migrations.NewRunner(store.DB()).Apply(ctx); applyErr != nil {
			_ = store.Close()
			err = applyErr
			return
		}
		// Closing the last connection checkpoints the WAL into the main file.
		if closeErr := store.Close(); closeErr != nil {
			err = closeErr
			return
		}
		template, err = os.ReadFile(path)
	})
	return template, err
}

// Seed writes a fully migrated database to path. The caller still opens it with
// sqlite.Open and may still run the migration runner, which finds nothing to do.
func Seed(t testing.TB, path string) {
	t.Helper()
	data, buildErr := build()
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	if writeErr := os.WriteFile(path, data, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
}

// DataDir returns a fresh temporary data directory whose routeweft.sqlite is
// already migrated.
func DataDir(t testing.TB) string {
	t.Helper()
	dir := t.TempDir()
	Seed(t, filepath.Join(dir, fileName))
	return dir
}

// DataDirWithAdmin is DataDir plus an admin account that already exists, so a
// fixture that only needs to log in does not pay for the production PBKDF2 work
// factor (600000 iterations, 5-7 s under the race detector) at startup. The
// hash is a valid self-describing pbkdf2-sha256 hash at 1000 iterations, which
// VerifyPassword accepts; production code never produces one. Config bootstrap
// values passed alongside it are ignored, exactly as on any later boot. Tests
// that exercise bootstrap itself must not use this.
func DataDirWithAdmin(t testing.TB, username, password string) string {
	t.Helper()
	dir := DataDir(t)
	ctx := context.Background()
	store, openErr := sqlite.Open(ctx, filepath.Join(dir, fileName))
	if openErr != nil {
		t.Fatal(openErr)
	}
	hash := AdminHash(password)
	if _, execErr := store.DB().ExecContext(ctx, "INSERT INTO admin_users (id,username,password_hash) VALUES (?,?,?)", "storetest-admin", username, hash); execErr != nil {
		_ = store.Close()
		t.Fatal(execErr)
	}
	if closeErr := store.Close(); closeErr != nil {
		t.Fatal(closeErr)
	}
	return dir
}

// AdminHash returns the fixture password hash DataDirWithAdmin stores, for a test
// that inserts an admin into a database it already has open.
func AdminHash(password string) string {
	salt := []byte("routeweft-storetest")
	return fmt.Sprintf("pbkdf2-sha256$%d$%s$%s", fixtureIterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(pbkdf2([]byte(password), salt, fixtureIterations, 32)))
}

const fixtureIterations = 1000

// pbkdf2 is PBKDF2-HMAC-SHA256 (RFC 8018) for one 32-byte block.
func pbkdf2(password, salt []byte, iterations, keyLen int) []byte {
	mac := hmac.New(sha256.New, password)
	mac.Write(salt)
	mac.Write([]byte{0, 0, 0, 1})
	u := mac.Sum(nil)
	derived := append([]byte(nil), u...)
	for i := 1; i < iterations; i++ {
		mac.Reset()
		mac.Write(u)
		u = mac.Sum(u[:0])
		for j := range derived {
			derived[j] ^= u[j]
		}
	}
	return derived[:keyLen]
}
