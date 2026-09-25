package credentials

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

func testStore(t *testing.T) (*Store, *Sealer) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := migrations.NewRunner(store.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	sealer := testSealer(t)
	credentials, err := NewStore(store.DB(), sealer)
	if err != nil {
		t.Fatal(err)
	}
	return credentials, sealer
}

func TestPutConnectionSealsSecretAtRest(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node, err := store.PutNode(ctx, Node{Kind: NodeBuiltin, ProviderID: "codex", Name: "Codex"})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct-1", Enabled: true, Priority: 1, Secret: Secret{AccessToken: "fixture-access", RefreshToken: "fixture-rotating-credential"}})
	if err != nil {
		t.Fatal(err)
	}
	var blob string
	if err := store.db.QueryRowContext(ctx, "SELECT secret_blob FROM provider_connections WHERE id=?", connection.ID).Scan(&blob); err != nil {
		t.Fatal(err)
	}
	if blob == "" || blob[:1] != "r" {
		t.Fatalf("stored blob is not a sealed envelope: %q", blob)
	}
	if strings.Contains(blob, "fixture-rotating-credential") {
		t.Fatal("plaintext credential reached SQLite")
	}
	loaded, err := store.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Secret.RefreshToken != "fixture-rotating-credential" || loaded.Secret.AccessToken != "fixture-access" || loaded.AuthKind != AuthOAuth || loaded.Priority != 1 || !loaded.Enabled {
		t.Fatalf("connection=%+v", loaded)
	}
}

func TestPutConnectionPreservesStoredSecretOnEdit(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node, err := store.PutNode(ctx, Node{Kind: NodeBuiltin, ProviderID: "claude", Name: "Claude"})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "work", AuthKind: AuthOAuth, Identity: "acct-2", Enabled: true, Secret: Secret{AccessToken: "a", RefreshToken: "r"}})
	if err != nil {
		t.Fatal(err)
	}
	// An edit that omits the secret (rename/disable) must not erase it.
	if _, err := store.PutConnection(ctx, Connection{ID: connection.ID, NodeID: node.ID, Name: "work-renamed", AuthKind: AuthOAuth, Identity: "acct-2", Enabled: false, Secret: Secret{}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != "work-renamed" || loaded.Enabled || loaded.Secret.RefreshToken != "r" {
		t.Fatalf("connection=%+v", loaded)
	}
}

func TestRotateSecretRefusesEmptyAndMissing(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node, err := store.PutNode(ctx, Node{Kind: NodeBuiltin, ProviderID: "codex", Name: "Codex"})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Secret: Secret{RefreshToken: "r"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RotateSecret(ctx, connection.ID, Secret{}); err == nil {
		t.Fatal("empty rotation was accepted")
	}
	if err := store.RotateSecret(ctx, "missing", Secret{RefreshToken: "x"}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing connection err=%v", err)
	}
	if err := store.RotateSecret(ctx, connection.ID, Secret{AccessToken: "new", RefreshToken: "rotated"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Secret.RefreshToken != "rotated" || loaded.Secret.AccessToken != "new" {
		t.Fatalf("secret=%+v", loaded.Secret)
	}
}

func TestListConnectionsOrdersByPriorityThenName(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node, err := store.PutNode(ctx, Node{Kind: NodeBuiltin, ProviderID: "kimi", Name: "Kimi"})
	if err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []Connection{
		{NodeID: node.ID, Name: "b", AuthKind: AuthAPIKey, Identity: "b", Priority: 5, Secret: Secret{AccessToken: "k"}},
		{NodeID: node.ID, Name: "a", AuthKind: AuthAPIKey, Identity: "a", Priority: 1, Secret: Secret{AccessToken: "k"}},
		{NodeID: node.ID, Name: "c", AuthKind: AuthAPIKey, Identity: "c", Priority: 1, Secret: Secret{AccessToken: "k"}},
	} {
		if _, err := store.PutConnection(ctx, candidate); err != nil {
			t.Fatal(err)
		}
	}
	connections, err := store.ListConnections(ctx, "kimi")
	if err != nil {
		t.Fatal(err)
	}
	if len(connections) != 3 || connections[0].Name != "a" || connections[1].Name != "c" || connections[2].Name != "b" {
		t.Fatalf("order=%v", connectionNames(connections))
	}
}

func TestSetEnabledAndDelete(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node, err := store.PutNode(ctx, Node{Kind: NodeBuiltin, ProviderID: "xai", Name: "xAI"})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthAPIKey, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "k"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SetConnectionEnabled(ctx, connection.ID, false); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetConnection(ctx, connection.ID)
	if err != nil || loaded.Enabled {
		t.Fatalf("loaded=%+v err=%v", loaded, err)
	}
	if err := store.SetConnectionEnabled(ctx, "missing", true); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing enable err=%v", err)
	}
	if err := store.DeleteConnection(ctx, connection.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetConnection(ctx, connection.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted lookup err=%v", err)
	}
	if err := store.DeleteConnection(ctx, connection.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err=%v", err)
	}
}

func TestRecordCredentialEventIsAuditOnly(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	if err := store.RecordCredentialEvent(ctx, "conn-1", "import", "identity=acct"); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordCredentialEvent(ctx, "conn-1", "", ""); err == nil {
		t.Fatal("empty event accepted")
	}
	var count int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM credential_events").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("credential event count=%d", count)
	}
}

func TestStoreRejectsMissingSealerOrDB(t *testing.T) {
	if _, err := NewStore(nil, testSealer(t)); err == nil {
		t.Fatal("nil db accepted")
	}
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := NewStore(store.DB(), nil); !errors.Is(err, ErrKeyRequired) {
		t.Fatalf("nil sealer err=%v", err)
	}
}

func connectionNames(connections []Connection) []string {
	names := make([]string, 0, len(connections))
	for _, connection := range connections {
		names = append(names, connection.Name)
	}
	return names
}
