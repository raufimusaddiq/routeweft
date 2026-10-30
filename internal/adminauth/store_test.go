package adminauth

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
	"github.com/raufimusaddiq/routeweft/internal/store/storetest"
)

func newAuthStore(t *testing.T) (*Store, *sqlite.Store) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "routeweft.sqlite")
	storetest.Seed(t, dbPath)
	store, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.NewRunner(store.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	return NewStore(store.DB()), store
}

func TestBootstrapAndAuthenticate(t *testing.T) {
	ctx := context.Background()
	store, sqlStore := newAuthStore(t)
	defer sqlStore.Close()
	if has, err := store.HasAdmin(ctx); err != nil || has {
		t.Fatalf("has=%v err=%v", has, err)
	}
	if _, _, err := store.Authenticate(ctx, "anyone", "x"); !errors.Is(err, ErrNoAdmin) {
		t.Fatalf("expected ErrNoAdmin, got %v", err)
	}
	if _, err := store.Bootstrap(ctx, "a1", "operator", "s3cret"); err != nil {
		t.Fatal(err)
	}
	account, ok, err := store.Authenticate(ctx, "operator", "s3cret")
	if err != nil || !ok || account.Username != "operator" {
		t.Fatalf("account=%+v ok=%v err=%v", account, ok, err)
	}
	if _, ok, _ := store.Authenticate(ctx, "operator", "wrong"); ok {
		t.Fatal("wrong password accepted")
	}
	if _, _, err := store.Authenticate(ctx, "ghost", "x"); err != nil {
		t.Fatalf("unknown user should be a plain miss, got %v", err)
	}
	// Second bootstrap must be refused.
	if _, err := store.Bootstrap(ctx, "a2", "other", "pw"); err == nil {
		t.Fatal("second bootstrap allowed")
	}
}

func TestChangePassword(t *testing.T) {
	ctx := context.Background()
	store, sqlStore := newAuthStore(t)
	defer sqlStore.Close()
	account, err := store.Bootstrap(ctx, "a1", "operator", "first")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ChangePassword(ctx, account.ID, "second"); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := store.Authenticate(ctx, "operator", "first"); ok {
		t.Fatal("old password still valid")
	}
	if _, ok, _ := store.Authenticate(ctx, "operator", "second"); !ok {
		t.Fatal("new password rejected")
	}
	if err := store.ChangePassword(ctx, "missing", "x"); err == nil {
		t.Fatal("password change for missing account allowed")
	}
}

func TestAdminPasswordIsStoredHashed(t *testing.T) {
	ctx := context.Background()
	store, sqlStore := newAuthStore(t)
	defer sqlStore.Close()
	if _, err := store.Bootstrap(ctx, "a1", "operator", "plaintext-secret"); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := sqlStore.DB().QueryRowContext(ctx, "SELECT password_hash FROM admin_users WHERE username=?", "operator").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if stored == "plaintext-secret" || !strings.HasPrefix(stored, "pbkdf2-sha256$") {
		t.Fatalf("password not hashed: %s", stored)
	}
}

// C09 bootstrap proof: the "act only while admin_users is empty" decision and the
// INSERT happen in one transaction, so concurrent first boots (two replicas on
// one data directory, or a retry racing a slow start) provision exactly one admin.
func TestConcurrentBootstrapProvisionsExactlyOneAdmin(t *testing.T) {
	ctx := context.Background()
	store, sqlStore := newAuthStore(t)
	defer sqlStore.Close()
	const contenders = 6
	var wg sync.WaitGroup
	var created atomic.Int32
	start := make(chan struct{})
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := store.Bootstrap(ctx, fmt.Sprintf("id-%d", i), fmt.Sprintf("operator-%d", i), fmt.Sprintf("password-%d", i)); err == nil {
				created.Add(1)
			}
		}()
	}
	close(start)
	wg.Wait()
	var rows int
	if err := sqlStore.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM admin_users").Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if created.Load() != 1 || rows != 1 {
		t.Fatalf("successful bootstraps=%d admin rows=%d, want exactly one of each", created.Load(), rows)
	}
}

func TestBootstrapTrimsTheUsernameAndRefusesAWhitespaceOnlyOne(t *testing.T) {
	ctx := context.Background()
	store, sqlStore := newAuthStore(t)
	defer sqlStore.Close()
	if _, err := store.Bootstrap(ctx, "a1", " \t ", "s3cret"); err == nil {
		t.Fatal("whitespace-only username was accepted")
	}
	if has, err := store.HasAdmin(ctx); err != nil || has {
		t.Fatalf("a refused bootstrap left an admin behind: has=%v err=%v", has, err)
	}
	account, err := store.Bootstrap(ctx, "a1", "  operator  ", "s3cret")
	if err != nil || account.Username != "operator" {
		t.Fatalf("account=%+v err=%v", account, err)
	}
	if _, ok, err := store.Authenticate(ctx, "operator", "s3cret"); err != nil || !ok {
		t.Fatalf("trimmed username cannot log in: ok=%v err=%v", ok, err)
	}
}

func TestBootstrapStoresOnlyASaltedSlowHash(t *testing.T) {
	ctx := context.Background()
	store, sqlStore := newAuthStore(t)
	const password = "synthetic-bootstrap-password"
	if _, err := store.Bootstrap(ctx, "a1", "operator", password); err != nil {
		t.Fatal(err)
	}
	var stored string
	if err := sqlStore.DB().QueryRowContext(ctx, "SELECT password_hash FROM admin_users").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(stored, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2-sha256" || parts[1] != "600000" || strings.Contains(stored, password) {
		t.Fatalf("stored credential is not a salted PBKDF2 hash with the documented work factor: %q", stored)
	}
	if err := sqlStore.Close(); err != nil { // checkpoints the WAL into the main file
		t.Fatal(err)
	}
}
