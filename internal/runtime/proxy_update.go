package runtime

import (
	"context"
)

// RefreshPoolBindings re-reads connection -> proxy pool bindings from durable
// state and republishes the snapshot through the standard compile-before-commit
// protocol (BDR-007). The credentials store calls it after a connection's
// proxy_pool_id changes, so the request path sees the new proxy without ever
// querying SQLite itself (SPEC §5).
func (m *Manager) RefreshPoolBindings(ctx context.Context) (*RuntimeSnapshot, error) {
	bindings, err := loadPoolBindingsDB(ctx, m.db)
	if err != nil {
		return nil, err
	}
	pools, err := loadProxyPools(ctx, m.db)
	if err != nil {
		return nil, err
	}
	return m.Update(ctx, func(candidate *Candidate) error {
		candidate.PoolBindings = bindings
		candidate.ProxyPools = pools
		return nil
	})
}

// PoolBinding reports the pool id currently compiled for one connection.
func (m *Manager) PoolBinding(connectionID string) (string, error) {
	snapshot, err := m.Load()
	if err != nil {
		return "", err
	}
	return snapshot.ConnectionPool(connectionID), nil
}
