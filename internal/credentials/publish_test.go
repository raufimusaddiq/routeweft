package credentials

import (
	"context"
	"testing"
	"time"
)

// TestImportDuringRefreshCommitKeepsMemoryAndStorageConsistent races an import
// against a refresh whose access token never expires. Before the fix the refresh
// published to memory outside the install lock, so an import landing in that gap
// left the served credential pointing at the stale rotated secret forever. The
// served and durable credentials must agree.
func TestImportDuringRefreshCommitKeepsMemoryAndStorageConsistent(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "codex")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "stale", RefreshToken: "r0", Expiry: time.Now().Add(-time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(store, nil)
	registry.Register(connection, func(context.Context, Secret) (Secret, error) {
		return Secret{AccessToken: "refreshed", RefreshToken: "r1"}, nil
	})

	// Race the import against the refresh commit window.
	done := make(chan struct{})
	go func() { defer close(done); _, _ = registry.Credential(ctx, connection.ID) }()
	if err := registry.Import(ctx, connection.ID, "acct", Secret{AccessToken: "imported", RefreshToken: "r-imported"}); err != nil {
		t.Fatal(err)
	}
	<-done
	served, err := registry.Credential(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Secret.AccessToken != served {
		t.Fatalf("memory and storage diverged: durable=%q served=%q", loaded.Secret.AccessToken, served)
	}
}

// TestMemoryPublishStaysWithLatestGeneration drives a plain refresh and then an
// import and asserts the served credential equals the durable one, which is the
// invariant the review is protecting for zero/far-future expiry credentials.
func TestMemoryPublishStaysWithLatestGeneration(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "github")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Enabled: true, Secret: Secret{RefreshToken: "r0", Expiry: time.Now().Add(-time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(store, nil)
	registry.Register(connection, func(context.Context, Secret) (Secret, error) {
		// Zero expiry: the served credential would otherwise stay valid forever if
		// memory diverged from storage.
		return Secret{AccessToken: "refreshed", RefreshToken: "r1"}, nil
	})
	first, err := registry.Credential(ctx, connection.ID)
	if err != nil || first != "refreshed" {
		t.Fatalf("credential=%q err=%v", first, err)
	}
	if err := registry.Import(ctx, connection.ID, "acct", Secret{AccessToken: "imported", RefreshToken: "r-imported"}); err != nil {
		t.Fatal(err)
	}
	served, err := registry.Credential(ctx, connection.ID)
	if err != nil || served != "imported" {
		t.Fatalf("served=%q err=%v", served, err)
	}
	loaded, err := store.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Secret.AccessToken != served {
		t.Fatalf("diverged: durable=%q served=%q", loaded.Secret.AccessToken, served)
	}
}
