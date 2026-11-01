// Package runtime owns immutable request configuration and mutable live state.
package runtime

import (
	"context"

	"github.com/raufimusaddiq/routeweft/internal/auth"
	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/capabilities"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/telemetry"
	transformbody "github.com/raufimusaddiq/routeweft/internal/transforms/body"
)

// RuntimeSnapshot is immutable after publication. Accessors return copies of
// mutable values so consumers cannot modify the active configuration.
type RuntimeSnapshot struct {
	modelCapabilities   *capabilities.Catalog
	telemetryOptions    telemetry.Options
	detailSizeLimit     int
	detailMaxRecords    int
	savers              *transformbody.Pipeline
	providerConfig      credentials.Configuration
	providers           map[string]servingProvider
	providerAccounts    map[string][]routing.Account
	bareModels          map[string]ModelRef
	providerStrategy    routing.Strategy
	providerStrategies  map[string]routing.Strategy
	providerStickyLimit uint64
	providerSticky      map[string]uint64
	comboOverrides      map[string]string
	version             uint64
	configRevision      uint64
	settings            map[string]string
	keys                *auth.KeyIndex
	models              map[string]Model
	aliases             map[string]ModelRef
	keyAccess           map[string]keyGrants
	disabledModels      map[string]struct{}
	combos              map[string]Combo
	capacityAdapters    map[string]CapacityAdapterConfig
	adaptersActive      bool
	fusion              map[string]Combo
	nested              map[string]bool
	poolBindings        map[string]string
	poolPools           map[string]proxyPoolEntry
	connectionProxies   map[string]ConnectionProxy
	proxyEnabled        bool
	proxyURL            string
	noProxy             []string
	// rotationPools and noAuthProxies serve no-auth proxy rotation (PARITY 6.38).
	rotationPools []string
	noAuthProxies map[string]NoAuthProxy
}

func (s *RuntimeSnapshot) TransformBody(ctx context.Context, protocol string, body []byte, allowPrivate bool) ([]byte, error) {
	return s.savers.Run(ctx, protocol, body, allowPrivate)
}

func (s *RuntimeSnapshot) OutboundProxy() (bool, string, []string) {
	return s.proxyEnabled, s.proxyURL, append([]string(nil), s.noProxy...)
}

type proxyPoolEntry struct {
	enabled   bool
	strategy  string
	members   []ProxyMember
	kind      string
	strict    bool
	noProxy   string
	updatedAt string
}

// PoolView is a compiled pool as proxy selection reads it, disabled pools
// included: a strict pool that is switched off still has to refuse a connection
// that names it (src/lib/network/connectionProxy.js:80-85).
type PoolView struct {
	Enabled  bool
	Strategy string
	Kind     string
	Strict   bool
	NoProxy  string
	Members  []ProxyMember
}

// PoolView returns the compiled pool for an id, whether or not it is enabled.
func (s *RuntimeSnapshot) PoolView(poolID string) (PoolView, bool) {
	if s == nil {
		return PoolView{}, false
	}
	entry, ok := s.poolPools[poolID]
	if !ok {
		return PoolView{}, false
	}
	return PoolView{Enabled: entry.enabled, Strategy: entry.strategy, Kind: entry.kind, Strict: entry.strict, NoProxy: entry.noProxy, Members: append([]ProxyMember(nil), entry.members...)}, true
}

// RotationPools lists the pools a no-auth provider may rotate over: enabled and
// holding a usable member, newest first (src/sse/services/auth.js:84-87 lists the
// active pools that have a URL, in the order the pool repository returns them).
func (s *RuntimeSnapshot) RotationPools() []string {
	if s == nil {
		return nil
	}
	return append([]string(nil), s.rotationPools...)
}

// NoAuthProxy is the proxy choice a providerStrategies entry makes for a no-auth
// provider: a fixed pool, or a rotation over every active pool.
type NoAuthProxy struct {
	ProviderID string
	PoolID     string
	Rotate     string
}

// NoAuthProxy returns the choice that applies to one connection, if its provider
// is a no-auth provider with a proxyPoolId or rotateStrategy set.
func (s *RuntimeSnapshot) NoAuthProxy(connectionID string) (NoAuthProxy, bool) {
	if s == nil {
		return NoAuthProxy{}, false
	}
	choice, ok := s.noAuthProxies[connectionID]
	return choice, ok
}

// ConnectionPool returns the proxy pool bound to one connection, or "" when the
// connection uses the global proxy setting (PRD-ROUTE-005).
func (s *RuntimeSnapshot) ConnectionPool(connectionID string) string {
	if s == nil {
		return ""
	}
	return s.poolBindings[connectionID]
}

// ConnectionProxy is one connection's compiled LiteRouter per-connection proxy
// (providerSpecificData.connectionProxyUrl/connectionNoProxy). It is compiled
// only when connectionProxyEnabled is true and the URL is not blank, the
// pin's own condition (src/lib/network/connectionProxy.js:39-56,151-154).
type ConnectionProxy struct {
	URL     string
	NoProxy []string
}

// LegacyConnectionProxy returns the per-connection proxy imported or set for
// one connection. A usable bound pool takes precedence; the proxy binder
// decides that.
func (s *RuntimeSnapshot) LegacyConnectionProxy(connectionID string) (ConnectionProxy, bool) {
	if s == nil {
		return ConnectionProxy{}, false
	}
	proxy, ok := s.connectionProxies[connectionID]
	proxy.NoProxy = append([]string(nil), proxy.NoProxy...)
	return proxy, ok
}

// ProxyMember is one compiled proxy pool endpoint. The pool's ordered members
// and enabled flags are snapshotted so proxy selection never reads SQLite on the
// request path (SPEC §5, §20).
type ProxyMember struct {
	URL     string
	Enabled bool
}

// ProxyPool returns the compiled, ordered members for a pool id. The second
// result is false when the pool is absent or disabled.
func (s *RuntimeSnapshot) ProxyPool(poolID string) (strategy string, members []ProxyMember, ok bool) {
	if s == nil {
		return "", nil, false
	}
	entry, ok := s.poolPools[poolID]
	if !ok || !entry.enabled {
		return "", nil, false
	}
	return entry.strategy, append([]ProxyMember(nil), entry.members...), true
}

// ProxyPools returns a defensive copy of every compiled pool for snapshot
// recompilation. Disabled pools are included so their state survives updates.
func (s *RuntimeSnapshot) ProxyPools() []ProxyPool {
	if s == nil || len(s.poolPools) == 0 {
		return nil
	}
	pools := make([]ProxyPool, 0, len(s.poolPools))
	for id, entry := range s.poolPools {
		pools = append(pools, ProxyPool{ID: id, Enabled: entry.enabled, Strategy: entry.strategy, Members: append([]ProxyMember(nil), entry.members...),
			Kind: entry.kind, Strict: entry.strict, NoProxy: entry.noProxy, UpdatedAt: entry.updatedAt})
	}
	return pools
}

func (s *RuntimeSnapshot) Version() uint64        { return s.version }
func (s *RuntimeSnapshot) ConfigRevision() uint64 { return s.configRevision }
func (s *RuntimeSnapshot) Settings() map[string]string {
	return cloneSettings(s.settings)
}
func (s *RuntimeSnapshot) APIKeys() *auth.KeyIndex { return s.keys }

// Models returns the enabled catalog and compiled aliases as a defensive copy.
func (s *RuntimeSnapshot) Models() []Model { return listCatalog(s.models, s.aliases, s.disabledModels) }

// ResolveModel resolves a direct provider/model pair or a model alias. The
// provider may be a registry alias (kmc, kimi-coding, ag): parseModel resolves
// that prefix before lookup (services/model.js:18-37).
func (s *RuntimeSnapshot) ResolveModel(provider, model string) (Model, bool) {
	if target, ok := s.aliases[model]; ok && (provider == "" || canonicalProvider(provider) == canonicalProvider(target.ProviderID)) {
		provider, model = target.ProviderID, target.ModelID
	}
	key := modelKey(provider, model)
	if _, disabled := s.disabledModels[key]; disabled {
		return Model{}, false
	}
	entry, ok := s.models[key]
	if !ok {
		// A registry alias names the same provider (kmc, kimi-coding, ag), but a
		// configured provider whose id really is the alias keeps precedence.
		if canonical, aliased := capabilities.ProviderAlias(provider); aliased {
			key = modelKey(canonical, model)
			if _, disabled := s.disabledModels[key]; disabled {
				return Model{}, false
			}
			entry, ok = s.models[key]
		}
	}
	entry.Capabilities = append([]string(nil), entry.Capabilities...)
	return entry, ok
}

func (s *RuntimeSnapshot) ModelCapabilityDetails(provider, model string) capabilities.Capabilities {
	if target, ok := s.aliases[model]; ok && (provider == "" || provider == target.ProviderID) {
		provider, model = target.ProviderID, target.ModelID
	}
	return s.modelCapabilities.Resolve(provider, model)
}
