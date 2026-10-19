package credentials

import (
	"context"
	"strings"
	"testing"
)

// TestImportPublishesBeforeAuditFailure proves an audit-write failure after a
// successful durable secret write cannot leave the registry serving the previous
// credential. The import remains effective; only the audit call reports trouble.
func TestImportPublishesBeforeAuditFailure(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "kimi")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthAPIKey, Identity: "acct", Secret: Secret{AccessToken: "old"}})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(store, nil)
	registry.Register(connection, nil)

	// Force the credential_events insert to fail by dropping the table.
	if _, err := store.db.ExecContext(ctx, "DROP TABLE credential_events"); err != nil {
		t.Fatal(err)
	}
	err = registry.Import(ctx, connection.ID, "acct", Secret{AccessToken: "imported", RefreshToken: "r-imported"})
	if err == nil || !strings.Contains(err.Error(), "audit") {
		t.Fatalf("err=%v", err)
	}
	// The durable import must have landed.
	loaded, err := store.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Secret.AccessToken != "imported" {
		t.Fatalf("durable=%q", loaded.Secret.AccessToken)
	}
	// And the served credential must match durable state, not the previous key.
	served, err := registry.Credential(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if served != "imported" {
		t.Fatalf("served=%q, want imported (memory diverged from durable state)", served)
	}
}
