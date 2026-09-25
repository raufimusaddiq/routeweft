package proxy

import (
	"context"
	"testing"
)

type staticBindings map[string]string

func (s staticBindings) ConnectionPool(_ context.Context, connectionID string) (string, error) {
	return s[connectionID], nil
}

// TestBinderCompilesConnectionClientFromBoundPool proves the production wiring:
// a connection bound to an enabled pool gets a pooled client compiled from that
// pool, while an unbound connection falls back to the global setting.
func TestBinderCompilesConnectionClientFromBoundPool(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	pool, err := store.PutPool(ctx, Pool{Name: "egress", Enabled: true, Strategy: StrategyRoundRobin, Members: []Member{
		{URL: "http://pool-a.example:3128", Enabled: true},
		{URL: "http://pool-b.example:3128", Enabled: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	settings := map[string]string{"outboundProxyEnabled": "false", "outboundProxyUrl": "", "noProxy": "[]"}
	binder := NewBinder(store, func() map[string]string { return settings }, staticBindings{"conn-bound": pool.ID})
	bound := binder.ClientFor(ctx, "conn-bound")
	if bound == nil {
		t.Fatal("bound pool did not compile a client")
	}
	// Round-robin advances per call, and the same material config is cached.
	if second := binder.ClientFor(ctx, "conn-bound"); second == nil {
		t.Fatal("bound pool stopped compiling a client")
	}
	// With no global proxy configured, an unbound connection has no proxy at all
	// and must fall back to the handler's default client (nil here).
	if unbound := binder.ClientFor(ctx, "conn-unbound"); unbound != nil {
		t.Fatal("unbound connection without a global proxy must use the default client")
	}
}

func TestBinderDisabledPoolFallsBackToGlobal(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	pool, err := store.PutPool(ctx, Pool{Name: "off", Enabled: false, Members: []Member{{URL: "http://pool.example:3128", Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	settings := map[string]string{"outboundProxyEnabled": "true", "outboundProxyUrl": "http://global.example:3128"}
	binder := NewBinder(store, func() map[string]string { return settings }, staticBindings{"conn": pool.ID})
	if binder.ClientFor(ctx, "conn") == nil {
		t.Fatal("disabled pool must fall back to the enabled global proxy")
	}
}

func TestBinderNilWhenNothingConfigured(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	binder := NewBinder(store, func() map[string]string { return map[string]string{} }, staticBindings{})
	if binder.ClientFor(ctx, "conn") != nil {
		t.Fatal("no proxy configured should return the default client (nil)")
	}
}

func TestBinderMissingPoolFallsBackToGlobal(t *testing.T) {
	ctx := context.Background()
	store := testStore(t)
	settings := map[string]string{"outboundProxyEnabled": "true", "outboundProxyUrl": "http://global.example:3128"}
	binder := NewBinder(store, func() map[string]string { return settings }, staticBindings{"conn": "does-not-exist"})
	if binder.ClientFor(ctx, "conn") == nil {
		t.Fatal("missing pool must fall back to the global proxy")
	}
}

func TestParseNoProxy(t *testing.T) {
	if got := parseNoProxy(`["localhost",".internal.example"]`); len(got) != 2 || got[0] != "localhost" {
		t.Fatalf("noProxy=%v", got)
	}
	if parseNoProxy("not-json") != nil || parseNoProxy("") != nil {
		t.Fatal("invalid/blank no-proxy should decode to nil")
	}
}
