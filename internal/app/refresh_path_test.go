package app

import (
	"context"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

// TestProductionRefreshPathUsesARealClient exercises the wiring the app installs,
// not a test-supplied resolver: a nil client there failed every refresh at request
// time while the resolver still looked healthy.
func TestProductionRefreshPathUsesARealClient(t *testing.T) {
	a := New(Config{DataDir: t.TempDir(), CredentialKey: []byte("01234567890123456789012345678901")}, nil)
	if err := a.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer a.store.Close()
	ctx := context.Background()
	node, err := a.creds.PutNode(ctx, credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: "claude", Name: "claude"})
	if err != nil {
		t.Fatal(err)
	}
	// An expired rotating credential: the next read must attempt a refresh.
	connection, err := a.creds.PutConnection(ctx, credentials.Connection{
		NodeID: node.ID, Name: "expired", Identity: "expired",
		AuthKind: credentials.AuthOAuth, Enabled: true,
		Secret: credentials.Secret{RefreshToken: "fixture-refresh", Expiry: time.Now().Add(-time.Hour)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.credReg.Load(ctx, "claude"); err != nil {
		t.Fatal(err)
	}
	_, err = a.credReg.Secret(ctx, connection.ID)
	// The network is unavailable here, so the refresh cannot succeed. What matters is
	// that it failed for a network reason, never "requires an HTTP client".
	if err != nil && err.Error() == "token exchange requires an HTTP client" {
		t.Fatalf("production refresh path has no HTTP client: %v", err)
	}
	if err == nil {
		t.Fatal("expected the unreachable upstream to fail the refresh")
	}
	t.Logf("refresh attempt failed as expected, for a real reason: %v", err)
}
