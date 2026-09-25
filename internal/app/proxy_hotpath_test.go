package app

import (
	"context"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/proxy"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// TestProxySelectionIsDatabaseFreeOnTheHotPath proves the review fix: after the
// durable store is closed, proxy selection for a bound connection still resolves
// from the compiled snapshot. If ClientFor touched SQLite it would fail here.
func TestProxySelectionIsDatabaseFreeOnTheHotPath(t *testing.T) {
	ctx := context.Background()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 3)
	}
	application := New(Config{Listen: ":0", DataDir: t.TempDir(), CredentialKey: key, AllowPrivateUpstreams: true}, nil)
	if err := application.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	proxies, err := proxy.NewStore(application.store.DB())
	if err != nil {
		t.Fatal(err)
	}
	pool, err := proxies.PutPool(ctx, proxy.Pool{Name: "egress", Enabled: true, Members: []proxy.Member{{URL: "http://127.0.0.1:3128", Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := application.store.DB().ExecContext(ctx, "INSERT INTO provider_nodes(id,kind,provider_id,name) VALUES('n1','builtin','codex','Codex')"); err != nil {
		t.Fatal(err)
	}
	if _, err := application.store.DB().ExecContext(ctx, "INSERT INTO provider_connections(id,node_id,name,auth_kind,identity,enabled,proxy_pool_id) VALUES('c1','n1','primary','api-key','acct',1,?)", pool.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := application.runtime.RefreshPoolBindings(ctx); err != nil {
		t.Fatal(err)
	}
	// Close durable storage; a database-free hot path must still resolve.
	if err := application.store.Close(); err != nil {
		t.Fatal(err)
	}
	snapshot, err := application.runtime.Load()
	if err != nil {
		t.Fatal(err)
	}
	strategy, members, ok := snapshot.ProxyPool(pool.ID)
	if !ok || strategy == "" || len(members) != 1 || members[0].URL != "http://127.0.0.1:3128" {
		t.Fatalf("compiled pool=%q members=%+v ok=%v", strategy, members, ok)
	}
	binder := proxy.NewBinder(func() *runtime.RuntimeSnapshot { return snapshot })
	if binder.ClientFor(ctx, "c1") == nil {
		t.Fatal("bound connection did not resolve a proxy client after the store closed")
	}
}
