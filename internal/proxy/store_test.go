package proxy

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

func testStore(t *testing.T) *Store {
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
	proxies, err := NewStore(store.DB())
	if err != nil {
		t.Fatal(err)
	}
	return proxies
}

func TestPutPoolValidatesAndOrdersMembers(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	pool, err := store.PutPool(ctx, Pool{Name: "egress", Enabled: true, Members: []Member{
		{Position: 2, URL: "http://proxy-b.example:3128", Enabled: true},
		{Position: 1, URL: "http://proxy-a.example:3128", Enabled: true},
		{Position: 3, URL: "http://proxy-a.example:3128", Enabled: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if pool.Strategy != StrategyRoundRobin {
		t.Fatalf("default strategy=%q", pool.Strategy)
	}
	loaded, err := store.GetPool(ctx, pool.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Members) != 2 {
		t.Fatalf("dedup failed: %+v", loaded.Members)
	}
	if loaded.Members[0].URL != "http://proxy-a.example:3128" || loaded.Members[1].URL != "http://proxy-b.example:3128" {
		t.Fatalf("order=%+v", loaded.Members)
	}
}

func TestPutPoolRejectsInvalidProxyURLs(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	for _, raw := range []string{"", "ftp://proxy.example", "not-a-url", "http://"} {
		if _, err := store.PutPool(ctx, Pool{Name: "bad", Members: []Member{{URL: raw, Enabled: true}}}); err == nil {
			t.Errorf("proxy URL %q was accepted", raw)
		}
	}
	if _, err := store.PutPool(ctx, Pool{Name: "", Members: []Member{{URL: "http://p.example"}}}); err == nil {
		t.Fatal("blank pool name accepted")
	}
}

func TestPutPoolIsReplaceNotAppend(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	pool, err := store.PutPool(ctx, Pool{Name: "egress", Members: []Member{{URL: "http://a.example:3128", Enabled: true}, {URL: "http://b.example:3128", Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutPool(ctx, Pool{ID: pool.ID, Name: "egress", Members: []Member{{URL: "http://c.example:3128", Enabled: true}}}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetPool(ctx, pool.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Members) != 1 || loaded.Members[0].URL != "http://c.example:3128" {
		t.Fatalf("members=%+v", loaded.Members)
	}
}

func TestDeletePoolAndNotFound(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	pool, err := store.PutPool(ctx, Pool{Name: "egress"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.GetPool(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing err=%v", err)
	}
	if err := store.DeletePool(ctx, pool.ID); err != nil {
		t.Fatal(err)
	}
	if err := store.DeletePool(ctx, pool.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("second delete err=%v", err)
	}
}

func TestEmptyPoolIsListedWithoutMembers(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	if _, err := store.PutPool(ctx, Pool{Name: "empty", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	pools, err := store.ListPools(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(pools) != 1 || len(pools[0].Members) != 0 {
		t.Fatalf("pools=%+v", pools)
	}
}
