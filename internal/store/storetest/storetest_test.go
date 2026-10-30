package storetest

import (
	"context"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/adminauth"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
	"path/filepath"
)

// The fixture hash must be one production accepts, or every login in a fixture
// would silently fail for the wrong reason.
func TestFixtureAdminHashIsAcceptedByProductionVerification(t *testing.T) {
	dir := DataDirWithAdmin(t, "operator", "fixture-pass")
	store, err := sqlite.Open(context.Background(), filepath.Join(dir, fileName))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	accounts := adminauth.NewStore(store.DB())
	if _, ok, err := accounts.Authenticate(context.Background(), "operator", "fixture-pass"); err != nil || !ok {
		t.Fatalf("fixture admin cannot log in: ok=%v err=%v", ok, err)
	}
	if _, ok, _ := accounts.Authenticate(context.Background(), "operator", "wrong"); ok {
		t.Fatal("fixture admin accepted a wrong password")
	}
}
