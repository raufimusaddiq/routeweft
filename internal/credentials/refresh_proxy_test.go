package credentials

import (
	"context"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/transport"
)

// A refresh leaves through the connection's proxy, as the pin passes
// proxyOptions to every refresh (chatCore.js:473-474): the exchange runs on
// a context that names the connection for the connection-routed client.
func TestRefreshCarriesTheConnection(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "codex")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "stale", RefreshToken: "r0", Expiry: time.Now().Add(-time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	var seen string
	registry := NewRegistry(store, nil)
	registry.Register(connection, func(ctx context.Context, _ Secret) (Secret, error) {
		seen = transport.ConnectionFrom(ctx)
		return Secret{AccessToken: "fresh", Expiry: time.Now().Add(time.Hour)}, nil
	})
	if _, err := registry.Credential(ctx, connection.ID); err != nil {
		t.Fatal(err)
	}
	if seen != connection.ID {
		t.Fatalf("refresh went out for %q, want %q", seen, connection.ID)
	}
}
