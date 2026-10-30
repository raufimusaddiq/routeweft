package proxy

import (
	"strings"
	"sync"
)

// PoolInfo is what resolving a connection's proxy needs to know about the pool the
// connection names: whether it exists, whether it is usable, what it is, and the
// URL the strategy selected for this attempt ("" when it has no usable member).
type PoolInfo struct {
	Found   bool
	Enabled bool
	Kind    Kind
	Strict  bool
	URL     string
	NoProxy string
}

// ConnectionInput is the proxy configuration of one connection: the pool it names
// (providerSpecificData.proxyPoolId, "__none__" for "none on purpose") and its own
// proxy (connectionProxyEnabled/Url/NoProxy).
type ConnectionInput struct {
	PoolID        string
	Pool          PoolInfo
	LegacyEnabled bool
	LegacyURL     string
	LegacyNoProxy string
}

// NoPool is the pool id that means "no pool, deliberately".
const NoPool = "__none__"

// Resolution is the proxy configuration a connection ends up with, in the shape of
// the reference's resolveConnectionProxyConfig result.
type Resolution struct {
	// Source is "pool", "legacy", "none", "missing-pool", or the relay kind.
	Source string
	PoolID string
	// ProxyEnabled, ProxyURL and NoProxy are the forward proxy to use.
	ProxyEnabled bool
	ProxyURL     string
	NoProxy      string
	// Strict says the connection must not leave over the direct address when no
	// proxy applies, even though its pool could not supply one.
	Strict bool
	// RelayURL is set for a relay pool: the request goes there, carrying its real
	// destination in x-relay-target and x-relay-path.
	RelayURL string
}

// Resolve picks a connection's proxy: its pool when that is usable, else its own
// proxy, else none (src/lib/network/connectionProxy.js:66-181). A pool that is
// strict keeps that guarantee when it is unusable (inactive, or without a URL),
// and a pool that no longer exists counts as strict, so neither silently sends a
// connection direct (connectionProxy.js:80-85,103-104; #4333).
func Resolve(in ConnectionInput) Resolution {
	poolID := strings.TrimSpace(in.PoolID)
	if poolID == NoPool {
		poolID = ""
	}
	legacyURL := strings.TrimSpace(in.LegacyURL)
	legacyNoProxy := strings.TrimSpace(in.LegacyNoProxy)
	var strict, missing bool
	if poolID != "" {
		pool := in.Pool
		missing = !pool.Found
		strict = missing || pool.Strict
		url := strings.TrimSpace(pool.URL)
		if pool.Found && pool.Enabled && url != "" {
			noProxy := strings.TrimSpace(pool.NoProxy)
			if pool.Kind.Relay() {
				return Resolution{Source: string(pool.Kind), PoolID: poolID, NoProxy: noProxy, Strict: pool.Strict, RelayURL: url}
			}
			return Resolution{Source: "pool", PoolID: poolID, ProxyEnabled: true, ProxyURL: url, NoProxy: noProxy, Strict: pool.Strict}
		}
	}
	if in.LegacyEnabled && legacyURL != "" {
		return Resolution{Source: "legacy", PoolID: poolID, ProxyEnabled: true, ProxyURL: legacyURL, NoProxy: legacyNoProxy, Strict: strict}
	}
	source := "none"
	if missing {
		source = "missing-pool"
	}
	return Resolution{Source: source, PoolID: poolID, ProxyEnabled: in.LegacyEnabled, ProxyURL: legacyURL, NoProxy: legacyNoProxy, Strict: strict}
}

// Rotation picks among several pools for a no-auth provider
// (src/lib/network/connectionProxy.js:18-34). Round-robin positions are kept in
// memory per provider and start over on restart; any strategy but "round-robin"
// and "random" takes the first.
type Rotation struct {
	mu     sync.Mutex
	cursor map[string]int
}

// Pick returns one of ids, or "" when there are none. random draws an index in
// [0, n).
func (r *Rotation) Pick(ids []string, strategy, provider string, random func(n int) int) string {
	switch {
	case len(ids) == 0:
		return ""
	case len(ids) == 1:
		return ids[0]
	}
	switch strategy {
	case "round-robin":
		r.mu.Lock()
		defer r.mu.Unlock()
		if r.cursor == nil {
			r.cursor = make(map[string]int)
		}
		next, seen := r.cursor[provider]
		if !seen {
			next = -1
		}
		next = (next + 1) % len(ids)
		r.cursor[provider] = next
		return ids[next]
	case "random":
		if random != nil {
			return ids[random(len(ids))]
		}
	}
	return ids[0]
}
