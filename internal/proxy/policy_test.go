package proxy

import (
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/transport"
)

// TestCompiledPolicyPrefersBoundMemberOnTheWire asserts precedence through the
// actual transport selector, not just the struct fields: with both a global and
// a bound pool proxy, the bound member must be the one that dials.
func TestCompiledPolicyPrefersBoundMemberOnTheWire(t *testing.T) {
	resolver := Resolver{Global: GlobalConfig{Enabled: true, ProxyURL: "http://global.example:3128"}}
	policy := resolver.Policy(Binding{PoolID: "pool-1", MemberURL: "http://pool.example:1080"})
	selected, err := policy.ProxyForHost("api.example")
	if err != nil || selected == nil || selected.Host != "pool.example:1080" {
		t.Fatalf("selected=%v err=%v", selected, err)
	}
	// With no bound member the global proxy applies.
	fallback, err := resolver.Policy(Binding{}).ProxyForHost("api.example")
	if err != nil || fallback == nil || fallback.Host != "global.example:3128" {
		t.Fatalf("fallback=%v err=%v", fallback, err)
	}
	// A no-proxy match wins over both.
	resolver.Global.NoProxy = []string{".example"}
	bypass, err := resolver.Policy(Binding{MemberURL: "http://pool.example:1080"}).ProxyForHost("api.example")
	if err != nil || bypass != nil {
		t.Fatalf("no-proxy bypass=%v err=%v", bypass, err)
	}
}

func TestPolicyPrefersBoundPoolMemberOverGlobal(t *testing.T) {
	resolver := Resolver{Global: GlobalConfig{Enabled: true, ProxyURL: "http://global.example:3128", NoProxy: []string{".internal.example", "localhost"}}}
	policy := resolver.Policy(Binding{PoolID: "pool-1", MemberURL: "http://pool.example:1080"})
	want := transport.ProxyPolicy{Enabled: true, GlobalProxyURL: "http://global.example:3128", ConnectionProxyURL: "http://pool.example:1080", NoProxy: []string{".internal.example", "localhost"}}
	if policy.Enabled != want.Enabled || policy.GlobalProxyURL != want.GlobalProxyURL || policy.ConnectionProxyURL != want.ConnectionProxyURL || len(policy.NoProxy) != len(want.NoProxy) {
		t.Fatalf("policy=%+v want=%+v", policy, want)
	}
}

func TestPolicyFallsBackToGlobalWhenPoolEmpty(t *testing.T) {
	resolver := Resolver{Global: GlobalConfig{Enabled: true, ProxyURL: "http://global.example:3128"}}
	policy := resolver.Policy(Binding{})
	if policy.ConnectionProxyURL != "" || policy.GlobalProxyURL != "http://global.example:3128" {
		t.Fatalf("policy=%+v", policy)
	}
}

func TestPolicyDisabledWhenNoProxyConfigured(t *testing.T) {
	policy := (Resolver{}).Policy(Binding{})
	if policy.Enabled {
		t.Fatalf("policy=%+v", policy)
	}
}

func TestPolicyExplicitPoolURLEnablesProxyWithoutGlobalFlag(t *testing.T) {
	policy := (Resolver{}).Policy(Binding{MemberURL: "http://pool.example:1080"})
	if !policy.Enabled || policy.ConnectionProxyURL != "http://pool.example:1080" {
		t.Fatalf("policy=%+v", policy)
	}
}

func TestSelectMemberStrategies(t *testing.T) {
	pool := Pool{Members: []Member{
		{Position: 0, URL: "http://a.example:3128", Enabled: true},
		{Position: 1, URL: "", Enabled: true},
		{Position: 2, URL: "http://b.example:3128", Enabled: false},
		{Position: 3, URL: "http://c.example:3128", Enabled: true},
	}}
	if member, ok := SelectMember(pool, 99, StrategyFillFirst); !ok || member.URL != "http://a.example:3128" {
		t.Fatalf("fill-first member=%+v ok=%v", member, ok)
	}
	for cursor, want := range map[uint64]string{0: "http://a.example:3128", 1: "http://c.example:3128", 2: "http://a.example:3128"} {
		member, ok := SelectMember(pool, cursor, StrategyRoundRobin)
		if !ok || member.URL != want {
			t.Fatalf("rr cursor %d member=%+v ok=%v", cursor, member, ok)
		}
	}
	if _, ok := SelectMember(Pool{}, 0, StrategyRoundRobin); ok {
		t.Fatal("empty pool selected a member")
	}
	if _, ok := SelectMember(Pool{Members: []Member{{URL: "http://x.example", Enabled: false}}}, 0, StrategyRoundRobin); ok {
		t.Fatal("disabled-only pool selected a member")
	}
}
