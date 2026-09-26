package security

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/adminauth"
	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

func newAdminStore(t *testing.T) *adminauth.Store {
	t.Helper()
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "security.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := migrations.NewRunner(store.DB()).Apply(context.Background()); err != nil {
		t.Fatal(err)
	}
	accounts := adminauth.NewStore(store.DB())
	if _, err := accounts.Bootstrap(context.Background(), "a1", "operator", "s3cret"); err != nil {
		t.Fatal(err)
	}
	return accounts
}
