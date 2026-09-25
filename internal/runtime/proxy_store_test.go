package runtime

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

// TestSnapshotCompilesConnectionPoolBindings proves per-connection pool bindings
// reach the immutable snapshot so the request path never queries SQLite for a
// connection's proxy (SPEC §5).
func TestSnapshotCompilesConnectionPoolBindings(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := migrations.NewRunner(store.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	seedProxyBinding(t, store.DB(), "conn-1", "pool-1")
	manager, err := NewManager(ctx, store.DB())
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	if got := snapshot.ConnectionPool("conn-1"); got != "pool-1" {
		t.Fatalf("binding=%q", got)
	}
	if got := snapshot.ConnectionPool("conn-2"); got != "" {
		t.Fatalf("unbound connection=%q", got)
	}
}

func seedProxyBinding(t *testing.T, db *sql.DB, connectionID, poolID string) {
	t.Helper()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, "INSERT INTO provider_nodes(id,kind,provider_id,name) VALUES('node-1','builtin','codex','Codex')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO proxy_pools(id,name,enabled,strategy) VALUES('pool-1','egress',1,'round_robin')"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, "INSERT INTO provider_connections(id,node_id,name,auth_kind,identity,enabled,proxy_pool_id) VALUES(?,'node-1','primary','api-key','acct',1,?)", connectionID, poolID); err != nil {
		t.Fatal(err)
	}
}
