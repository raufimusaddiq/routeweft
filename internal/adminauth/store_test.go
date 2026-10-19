package adminauth

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

func newAuthStore(t *testing.T) (*Store, *sqlite.Store) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "routeweft.sqlite"))
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
