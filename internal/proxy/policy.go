package proxy

import (
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/transport"
)

// GlobalConfig is the compiled global outbound proxy setting (PRD-ROUTE-005,
// settings keys outboundProxyEnabled/outboundProxyUrl/noProxy).
type GlobalConfig struct {
	Enabled    bool
	ProxyURL   string
	NoProxy    []string
	AllowLocal bool
}

// Binding describes one connection's compiled proxy decision: its optional
// bound pool and the selected member URL for this attempt.
type Binding struct {
	PoolID       string
	MemberURL    string
	RotationUsed bool
}

// Resolver compiles transport.ProxyPolicy values. Pools are durable; the
// rotation cursor is supplied by the caller from RuntimeState so selection stays
// memory-first and never persists per request (SPEC §13).
type Resolver struct {
	Global GlobalConfig
	Store  *Store
}

// Policy compiles the effective proxy policy for one connection. A bound, enabled
// pool member takes precedence over the global proxy; an empty or disabled pool
// falls back to the global setting (an empty pool is not a hard failure, matching
// the capacity-adapter deselect semantics for optional infrastructure).
func (r Resolver) Policy(binding Binding) transport.ProxyPolicy {
	proxyURL := strings.TrimSpace(binding.MemberURL)
	if proxyURL == "" {
		proxyURL = strings.TrimSpace(r.Global.ProxyURL)
	}
	return transport.ProxyPolicy{
		Enabled:            r.Global.Enabled || proxyURL != "",
		GlobalProxyURL:     strings.TrimSpace(r.Global.ProxyURL),
		ConnectionProxyURL: strings.TrimSpace(binding.MemberURL),
		NoProxy:            append([]string(nil), r.Global.NoProxy...),
		AllowPrivate:       r.Global.AllowLocal,
	}
}

// SelectMember chooses a pool member for one attempt. strategy/cursor come from
// compiled config and RuntimeState; fill-first and round-robin are both stable
// and skip disabled or blank members.
func SelectMember(pool Pool, cursor uint64, strategy Strategy) (Member, bool) {
	eligible := make([]Member, 0, len(pool.Members))
	for _, member := range pool.Members {
		if member.Enabled && strings.TrimSpace(member.URL) != "" {
			eligible = append(eligible, member)
		}
	}
	if len(eligible) == 0 {
		return Member{}, false
	}
	if strategy == StrategyFillFirst {
		return eligible[0], true
	}
	return eligible[cursor%uint64(len(eligible))], true
}
