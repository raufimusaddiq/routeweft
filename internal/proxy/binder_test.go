package proxy

import (
	"context"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// testSnapshot compiles an immutable snapshot carrying settings, connection
// bindings and proxy pools, so the binder can be exercised without any SQLite
// read on the request path.
func testSnapshot(t *testing.T, settings map[string]string, bindings map[string]string, pools []runtime.ProxyPool) *runtime.RuntimeSnapshot {
	t.Helper()
	snapshot, err := (runtime.Compiler{}).Compile(runtime.Config{Settings: settings, PoolBindings: bindings, ProxyPools: pools}, 1)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// TestBinderCompilesConnectionClientFromBoundPool proves the production wiring:
// a connection bound to an enabled pool gets a pooled client compiled from the
// snapshot alone (no SQLite), while an unbound connection without a global proxy
// falls back to the default client.
func TestBinderCompilesConnectionClientFromBoundPool(t *testing.T) {
	ctx := context.Background()
	pool := runtime.ProxyPool{ID: "pool-1", Enabled: true, Strategy: "round_robin", Members: []runtime.ProxyMember{
		{URL: "http://pool-a.example:3128", Enabled: true},
		{URL: "http://pool-b.example:3128", Enabled: true},
	}}
	snapshot := testSnapshot(t, map[string]string{"outboundProxyUrl": ""}, map[string]string{"conn-bound": "pool-1"}, []runtime.ProxyPool{pool})
	binder := NewBinder(func() *runtime.RuntimeSnapshot { return snapshot })
	if binder.ClientFor(ctx, "conn-bound") == nil {
		t.Fatal("bound pool did not compile a client")
	}
	if binder.ClientFor(ctx, "conn-bound") == nil {
		t.Fatal("bound pool stopped compiling a client")
	}
	if unbound := binder.ClientFor(ctx, "conn-unbound"); unbound != nil {
		t.Fatal("unbound connection without a global proxy must use the default client")
	}
}

func TestBinderDisabledPoolFallsBackToGlobal(t *testing.T) {
	ctx := context.Background()
	pool := runtime.ProxyPool{ID: "off", Enabled: false, Members: []runtime.ProxyMember{{URL: "http://pool.example:3128", Enabled: true}}}
	snapshot := testSnapshot(t, map[string]string{"outboundProxyEnabled": "true", "outboundProxyUrl": "http://global.example:3128"}, map[string]string{"conn": "off"}, []runtime.ProxyPool{pool})
	binder := NewBinder(func() *runtime.RuntimeSnapshot { return snapshot })
	if binder.ClientFor(ctx, "conn") == nil {
		t.Fatal("disabled pool must fall back to the enabled global proxy")
	}
}

func TestBinderMissingPoolFallsBackToGlobal(t *testing.T) {
	ctx := context.Background()
	snapshot := testSnapshot(t, map[string]string{"outboundProxyEnabled": "true", "outboundProxyUrl": "http://global.example:3128"}, map[string]string{"conn": "does-not-exist"}, nil)
	binder := NewBinder(func() *runtime.RuntimeSnapshot { return snapshot })
	if binder.ClientFor(ctx, "conn") == nil {
		t.Fatal("missing pool must fall back to the global proxy")
	}
}

func TestBinderNilWhenNothingConfigured(t *testing.T) {
	ctx := context.Background()
	snapshot := testSnapshot(t, map[string]string{}, nil, nil)
	binder := NewBinder(func() *runtime.RuntimeSnapshot { return snapshot })
	if binder.ClientFor(ctx, "conn") != nil {
		t.Fatal("no proxy configured should return the default client (nil)")
	}
	// A nil snapshot is also safe: no proxy resolution happens.
	if NewBinder(func() *runtime.RuntimeSnapshot { return nil }).ClientFor(ctx, "conn") != nil {
		t.Fatal("nil snapshot should return the default client (nil)")
	}
}

func TestCompiledNoProxy(t *testing.T) {
	snapshot := testSnapshot(t, map[string]string{"noProxy": `["localhost",".internal.example"]`}, nil, nil)
	_, _, got := snapshot.OutboundProxy()
	if len(got) != 2 || got[0] != "localhost" {
		t.Fatalf("noProxy=%v", got)
	}
	got[0] = "mutated"
	_, _, fresh := snapshot.OutboundProxy()
	if fresh[0] != "localhost" {
		t.Fatal("caller mutated compiled proxy config")
	}
	for _, raw := range []string{"not-json", "", `["localhost",1]`} {
		view := testSnapshot(t, map[string]string{"noProxy": raw}, nil, nil)
		_, _, got := view.OutboundProxy()
		if got != nil {
			t.Fatal("invalid/blank no-proxy should decode to nil")
		}
	}
}
