package runtime

import (
	"sort"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

// compileNoAuthProxies reads the proxyPoolId and rotateStrategy a providerStrategies
// entry sets for a no-auth provider (src/sse/services/auth.js:78-104) and lists the
// pools a rotation may pick from. The reference gives every no-auth provider one
// virtual account that carries the choice; Routeweft has real connections with no
// credential, so the choice is attached to each of them.
func (s *RuntimeSnapshot) compileNoAuthProxies(connections []credentials.Connection) {
	s.rotationPools = rotationPools(s.poolPools)
	overrides := strategyOverridesTolerant(s.settings["providerStrategies"], false)
	for _, connection := range connections {
		if connection.AuthKind != credentials.AuthNone {
			continue
		}
		override, ok := overrides[connection.ProviderID]
		if !ok {
			continue
		}
		rotate := strings.TrimSpace(override.RotateStrategy)
		poolID := strings.TrimSpace(override.ProxyPoolID)
		if (rotate == "" || rotate == "none") && poolID == "" {
			continue
		}
		if s.noAuthProxies == nil {
			s.noAuthProxies = make(map[string]NoAuthProxy)
		}
		s.noAuthProxies[connection.ID] = NoAuthProxy{ProviderID: connection.ProviderID, PoolID: poolID, Rotate: rotate}
	}
}

// rotationPools returns the ids of the enabled pools that have a usable member,
// most recently updated first (the reference sorts its pool list by updatedAt,
// newest first, then keeps the active ones that have a URL).
func rotationPools(pools map[string]proxyPoolEntry) []string {
	type entry struct{ id, updated string }
	var usable []entry
	for id, pool := range pools {
		if !pool.enabled {
			continue
		}
		for _, member := range pool.members {
			if member.Enabled && strings.TrimSpace(member.URL) != "" {
				usable = append(usable, entry{id, pool.updatedAt})
				break
			}
		}
	}
	sort.Slice(usable, func(i, j int) bool {
		if usable[i].updated != usable[j].updated {
			return usable[i].updated > usable[j].updated
		}
		return usable[i].id < usable[j].id
	})
	ids := make([]string, len(usable))
	for i, item := range usable {
		ids[i] = item.id
	}
	return ids
}
