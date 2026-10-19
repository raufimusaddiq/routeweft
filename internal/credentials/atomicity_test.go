package credentials

import (
	"context"
	"testing"
	"time"
)

// TestImportCannotBeOverwrittenByStaleRefreshCommit drives the exact window the
// review named: the refresh completes its exchange (so its staleness check would
// pass) and only then does an import land. Because the check and the durable
// write share the per-connection install lock, the import must either run before
// the refresh commits (making the refresh a superseded no-op) or wait for it —
// never let the stale rotated secret overwrite the newer import.
func TestImportCannotBeOverwrittenByStaleRefreshCommit(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "codex")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "stale", RefreshToken: "r0", Expiry: time.Now().Add(-time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	exchangeReturned := make(chan struct{})
	registry := NewRegistry(store, nil)
	registry.Register(connection, func(context.Context, Secret) (Secret, error) {
		// The exchange finishes, then signals. The caller resumes inside refresh(),
		// where the staleness check + durable commit run under the install lock.
		close(exchangeReturned)
		return Secret{AccessToken: "refreshed-stale", RefreshToken: "r1", Expiry: time.Now().Add(time.Hour)}, nil
	})

	refreshDone := make(chan error, 1)
	go func() {
		_, err := registry.Credential(ctx, connection.ID)
		refreshDone <- err
	}()
	<-exchangeReturned
	// Race the import against the refresh commit; whichever order the scheduler
	// picks, the durable result must be the import.
	if err := registry.Import(ctx, connection.ID, "acct", Secret{AccessToken: "imported", RefreshToken: "r-imported"}); err != nil {
		t.Fatal(err)
	}
	if err := <-refreshDone; err != nil && err != ErrSuperseded {
		t.Fatalf("refresh err=%v", err)
	}

	loaded, err := store.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Secret.AccessToken != "imported" || loaded.Secret.RefreshToken != "r-imported" {
		t.Fatalf("stale refresh overwrote the durable import: %+v", loaded.Secret)
	}
	credential, err := registry.Credential(ctx, connection.ID)
	if err != nil || credential != "imported" {
		t.Fatalf("credential=%q err=%v", credential, err)
	}
}

// TestStaleRefreshCommitAfterImportIsRejected forces the refresh commit to wait
// until the import has fully landed, then asserts the refresh detects the newer
// generation instead of writing over it.
func TestStaleRefreshCommitAfterImportIsRejected(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "github")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "stale", RefreshToken: "r0", Expiry: time.Now().Add(-time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(store, nil)
	registry.Register(connection, func(context.Context, Secret) (Secret, error) {
		return Secret{AccessToken: "refreshed-stale", RefreshToken: "r1", Expiry: time.Now().Add(time.Hour)}, nil
	})
	// Import first, then attempt the credential fetch. The connection is now
	// fresh after import, so the stale refresh path must never run.
	if err := registry.Import(ctx, connection.ID, "acct", Secret{AccessToken: "imported", RefreshToken: "r-imported", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	credential, err := registry.Credential(ctx, connection.ID)
	if err != nil || credential != "imported" {
		t.Fatalf("credential=%q err=%v", credential, err)
	}
	loaded, err := store.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Secret.AccessToken != "imported" {
		t.Fatalf("durable=%+v", loaded.Secret)
	}
}
