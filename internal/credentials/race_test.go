package credentials

import (
	"context"
	"testing"
	"time"
)

// TestImportWinsOverInFlightRefresh proves the interleaving reported in review:
// a refresh that starts first must not overwrite an import that lands while the
// exchange is still running, neither in memory nor durably.
func TestImportWinsOverInFlightRefresh(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "codex")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "stale", RefreshToken: "r0", Expiry: time.Now().Add(-time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	exchangeStarted := make(chan struct{})
	releaseExchange := make(chan struct{})
	registry := NewRegistry(store, nil)
	registry.Register(connection, func(ctx context.Context, stored Secret) (Secret, error) {
		close(exchangeStarted)
		select {
		case <-releaseExchange:
		case <-ctx.Done():
			return Secret{}, ctx.Err()
		}
		// A slow upstream refresh that finishes after the import landed.
		return Secret{AccessToken: "refreshed-late", RefreshToken: "r1", Expiry: time.Now().Add(time.Hour)}, nil
	})

	refreshDone := make(chan error, 1)
	go func() {
		_, err := registry.Credential(ctx, connection.ID)
		refreshDone <- err
	}()
	<-exchangeStarted

	if err := registry.Import(ctx, connection.ID, "acct", Secret{AccessToken: "imported", RefreshToken: "r-imported"}); err != nil {
		t.Fatal(err)
	}
	close(releaseExchange)
	if err := <-refreshDone; err != nil {
		t.Fatalf("superseded refresh surfaced an error: %v", err)
	}

	// Durable state must be the import, not the late refresh.
	loaded, err := store.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Secret.AccessToken != "imported" || loaded.Secret.RefreshToken != "r-imported" {
		t.Fatalf("late refresh overwrote the durable import: %+v", loaded.Secret)
	}
	// And the in-memory credential served afterwards is the import.
	credential, err := registry.Credential(ctx, connection.ID)
	if err != nil || credential != "imported" {
		t.Fatalf("credential=%q err=%v", credential, err)
	}
}

// TestConcurrentImportsKeepLastInstall is a lighter invariant check that a
// refresh never resurrects a credential generation that an install replaced.
func TestRefreshAfterImportServesImport(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "github")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "old", RefreshToken: "r0", Expiry: time.Now().Add(-time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(store, nil)
	calls := 0
	registry.Register(connection, func(context.Context, Secret) (Secret, error) {
		calls++
		return Secret{AccessToken: "refreshed", Expiry: time.Now().Add(time.Hour)}, nil
	})
	if err := registry.Import(ctx, connection.ID, "acct", Secret{AccessToken: "imported", RefreshToken: "r-imported", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	credential, err := registry.Credential(ctx, connection.ID)
	if err != nil || credential != "imported" {
		t.Fatalf("credential=%q calls=%d err=%v", credential, calls, err)
	}
	if calls != 0 {
		t.Fatalf("import did not satisfy the credential request: calls=%d", calls)
	}
}
