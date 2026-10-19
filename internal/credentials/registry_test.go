package credentials

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func seedNode(t *testing.T, store *Store, providerID string) Node {
	t.Helper()
	node, err := store.PutNode(context.Background(), Node{Kind: NodeBuiltin, ProviderID: providerID, Name: providerID})
	if err != nil {
		t.Fatal(err)
	}
	return node
}

func TestSecretReturnsFreshWithoutRefresh(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "codex")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "static", Expiry: time.Now().Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(store, nil)
	registry.Register(connection, func(context.Context, Secret) (Secret, error) {
		t.Fatal("refresh should not run for a fresh token")
		return Secret{}, nil
	})
	credential, err := registry.Credential(ctx, connection.ID)
	if err != nil || credential != "static" {
		t.Fatalf("credential=%q err=%v", credential, err)
	}
}

func TestExpiredTokenRefreshesSingleflightAndPersists(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "claude")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "stale", RefreshToken: "r0", Expiry: time.Now().Add(-time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	registry := NewRegistry(store, nil)
	registry.Register(connection, func(_ context.Context, current Secret) (Secret, error) {
		calls.Add(1)
		if current.RefreshToken != "r0" {
			t.Errorf("refresh token=%q", current.RefreshToken)
		}
		time.Sleep(20 * time.Millisecond)
		return Secret{AccessToken: "fresh", Expiry: time.Now().Add(time.Hour)}, nil
	})
	var wg sync.WaitGroup
	results := make([]string, 8)
	errs := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errs[index] = registry.Credential(ctx, connection.ID)
		}(i)
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("refresh calls=%d, want 1", calls.Load())
	}
	for i := range results {
		if errs[i] != nil || results[i] != "fresh" {
			t.Fatalf("call %d credential=%q err=%v", i, results[i], errs[i])
		}
	}
	// The rotated token must be durable so a restart keeps working.
	loaded, err := store.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Secret.AccessToken != "fresh" || loaded.Secret.RefreshToken != "r0" {
		t.Fatalf("durable secret=%+v", loaded.Secret)
	}
}

func TestRefreshKeepsRotatedTokenAndRejectsEmptyResult(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "github")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Secret: Secret{AccessToken: "stale", RefreshToken: "r0", Expiry: time.Now().Add(-time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(store, nil)
	registry.Register(connection, func(context.Context, Secret) (Secret, error) {
		return Secret{AccessToken: "only-access", Expiry: time.Now().Add(time.Hour)}, nil
	})
	secret, err := registry.Secret(ctx, connection.ID)
	if err != nil || secret.RefreshToken != "r0" || secret.AccessToken != "only-access" {
		t.Fatalf("secret=%+v err=%v", secret, err)
	}
	// An empty refresh payload must fail without destroying the durable secret.
	expired := connection
	expired.Secret = Secret{AccessToken: "stale", RefreshToken: "r0", Expiry: time.Now().Add(-time.Minute)}
	registry.Register(expired, func(context.Context, Secret) (Secret, error) { return Secret{}, nil })
	if _, err := registry.Secret(ctx, connection.ID); err == nil {
		t.Fatal("empty refresh payload was accepted")
	}
	loaded, err := store.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Secret.AccessToken != "only-access" {
		t.Fatalf("failed refresh destroyed the durable credential: %+v", loaded.Secret)
	}
}

func TestProviderErrorLeavesDurableCredentialIntact(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "xai")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Secret: Secret{AccessToken: "stale", RefreshToken: "r0", Expiry: time.Now().Add(-time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(store, nil)
	wantErr := errors.New("upstream refresh failed")
	registry.Register(connection, func(context.Context, Secret) (Secret, error) { return Secret{}, wantErr })
	if _, err := registry.Credential(ctx, connection.ID); !errors.Is(err, wantErr) {
		t.Fatalf("err=%v", err)
	}
	loaded, err := store.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Secret.RefreshToken != "r0" || loaded.Secret.AccessToken != "stale" {
		t.Fatalf("failed refresh mutated durable state: %+v", loaded.Secret)
	}
}

func TestImportValidatesIdentityAndUsesDurablePath(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "kimi")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthAPIKey, Identity: "acct-a", Secret: Secret{AccessToken: "old"}})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(store, nil)
	registry.Register(connection, nil)
	if err := registry.Import(ctx, connection.ID, "acct-b", Secret{AccessToken: "new"}); err == nil {
		t.Fatal("mismatched identity import was accepted")
	}
	if err := registry.Import(ctx, connection.ID, "acct-a", Secret{}); err == nil {
		t.Fatal("empty import was accepted")
	}
	if err := registry.Import(ctx, connection.ID, "acct-a", Secret{AccessToken: "imported", RefreshToken: "ir"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Secret.AccessToken != "imported" || loaded.Secret.RefreshToken != "ir" {
		t.Fatalf("secret=%+v", loaded.Secret)
	}
	credential, err := registry.Credential(ctx, connection.ID)
	if err != nil || credential != "imported" {
		t.Fatalf("credential=%q err=%v", credential, err)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM credential_events WHERE connection_id=? AND event='import'", connection.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("import audit rows=%d", count)
	}
}

func TestUnknownConnectionAndForget(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	registry := NewRegistry(store, nil)
	if _, err := registry.Credential(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing err=%v", err)
	}
	node := seedNode(t, store, "zed")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthCookie, Identity: "acct", Secret: Secret{Cookie: "session=fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	registry.Register(connection, nil)
	credential, err := registry.Credential(ctx, connection.ID)
	if err != nil || !strings.Contains(credential, "session=") {
		t.Fatalf("cookie credential=%q err=%v", credential, err)
	}
	registry.Forget(connection.ID)
	if _, err := registry.Credential(ctx, connection.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after forget err=%v", err)
	}
}

func TestLoadSeedsProviderConnections(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "groq")
	for _, name := range []string{"a", "b"} {
		if _, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: name, AuthKind: AuthAPIKey, Identity: name, Enabled: true, Secret: Secret{AccessToken: "k-" + name}}); err != nil {
			t.Fatal(err)
		}
	}
	registry := NewRegistry(store, nil)
	connections, err := registry.Load(ctx, "groq")
	if err != nil {
		t.Fatal(err)
	}
	if len(connections) != 2 {
		t.Fatalf("loaded %d connections", len(connections))
	}
	credential, err := registry.Credential(ctx, connections[0].ID)
	if err != nil || !strings.HasPrefix(credential, "k-") {
		t.Fatalf("credential=%q err=%v", credential, err)
	}
}
