// Package runtime owns immutable request configuration and mutable live state.
package runtime

import "github.com/raufimusaddiq/routeweft/internal/auth"

// RuntimeSnapshot is immutable after publication. Accessors return copies of
// mutable values so consumers cannot modify the active configuration.
type RuntimeSnapshot struct {
	version        uint64
	configRevision uint64
	settings       map[string]string
	keys           *auth.KeyIndex
	models         map[string]Model
	aliases        map[string]ModelRef
	disabledModels map[string]struct{}
	combos         map[string]Combo
	poolBindings   map[string]string
	poolPools      map[string]proxyPoolEntry
}

type proxyPoolEntry struct {
	enabled  bool
	strategy string
	members  []ProxyMember
}

// ConnectionPool returns the proxy pool bound to one connection, or "" when the
// connection uses the global proxy setting (PRD-ROUTE-005).
func (s *RuntimeSnapshot) ConnectionPool(connectionID string) string {
	if s == nil {
		return ""
	}
	return s.poolBindings[connectionID]
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
		pools = append(pools, ProxyPool{ID: id, Enabled: entry.enabled, Strategy: entry.strategy, Members: append([]ProxyMember(nil), entry.members...)})
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

// ResolveModel resolves a direct provider/model pair or a model alias.
func (s *RuntimeSnapshot) ResolveModel(provider, model string) (Model, bool) {
	if target, ok := s.aliases[model]; ok && (provider == "" || provider == target.ProviderID) {
		provider, model = target.ProviderID, target.ModelID
	}
	key := modelKey(provider, model)
	if _, disabled := s.disabledModels[key]; disabled {
		return Model{}, false
	}
	entry, ok := s.models[key]
	entry.Capabilities = append([]string(nil), entry.Capabilities...)
	return entry, ok
}
