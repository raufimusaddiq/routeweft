package credentials

import (
	"context"
	"errors"
	"testing"
	"time"
)

func forceRig(t *testing.T, refreshToken string) (*Registry, string, *int) {
	t.Helper()
	store, _ := testStore(t)
	node := seedNode(t, store, "codex")
	ctx := context.Background()
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "old", RefreshToken: refreshToken, Expiry: time.Now().Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(store, nil)
	exchanges := 0
	registry.Register(connection, func(context.Context, Secret) (Secret, error) {
		exchanges++
		return Secret{AccessToken: "new", RefreshToken: "r2", Expiry: time.Now().Add(time.Hour)}, nil
	})
	return registry, connection.ID, &exchanges
}

// A token the provider refused is replaced although its expiry has not passed.
func TestForceRefreshReplacesARefusedUnexpiredToken(t *testing.T) {
	registry, id, exchanges := forceRig(t, "r1")
	secret, err := registry.ForceRefresh(context.Background(), id, "old")
	if err != nil || secret.AccessToken != "new" || *exchanges != 1 {
		t.Fatalf("%+v %v exchanges %d", secret, err, *exchanges)
	}
}

// Once another caller has rotated past the refused token, the newer credential
// is used and the refresh token is not spent a second time.
func TestForceRefreshDoesNotRotateTwice(t *testing.T) {
	registry, id, exchanges := forceRig(t, "r1")
	if _, err := registry.ForceRefresh(context.Background(), id, "old"); err != nil {
		t.Fatal(err)
	}
	secret, err := registry.ForceRefresh(context.Background(), id, "old")
	if err != nil || secret.AccessToken != "new" || *exchanges != 1 {
		t.Fatalf("%+v %v exchanges %d", secret, err, *exchanges)
	}
}

func TestForceRefreshWithoutRefreshTokenFails(t *testing.T) {
	registry, id, exchanges := forceRig(t, "")
	if _, err := registry.ForceRefresh(context.Background(), id, "old"); !errors.Is(err, ErrNoRefreshToken) || *exchanges != 0 {
		t.Fatalf("%v exchanges %d", err, *exchanges)
	}
}
