package runtime

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// loadProxyPools reads every durable proxy pool and its members into compiled
// snapshot input so proxy selection stays database-free at request time.
func loadProxyPools(ctx context.Context, db *sql.DB) ([]ProxyPool, error) {
	rows, err := db.QueryContext(ctx, "SELECT id,enabled,strategy FROM proxy_pools ORDER BY id")
	if err != nil {
		return nil, fmt.Errorf("load proxy pools: %w", err)
	}
	defer rows.Close()
	var pools []ProxyPool
	index := make(map[string]int)
	for rows.Next() {
		var pool ProxyPool
		var enabled int
		if err := rows.Scan(&pool.ID, &enabled, &pool.Strategy); err != nil {
			return nil, err
		}
		pool.Enabled = enabled != 0
		index[pool.ID] = len(pools)
		pools = append(pools, pool)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	members, err := db.QueryContext(ctx, "SELECT pool_id,proxy_url,enabled FROM proxy_pool_members ORDER BY pool_id,position,id")
	if err != nil {
		return nil, fmt.Errorf("load proxy pool members: %w", err)
	}
	defer members.Close()
	for members.Next() {
		var poolID, url string
		var enabled int
		if err := members.Scan(&poolID, &url, &enabled); err != nil {
			return nil, err
		}
		if position, ok := index[poolID]; ok {
			pools[position].Members = append(pools[position].Members, ProxyMember{URL: url, Enabled: enabled != 0})
		}
	}
	if err := members.Err(); err != nil {
		return nil, err
	}
	return pools, nil
}

// loadPoolBindings reads connection -> proxy pool bindings into the compiled
// snapshot so the request path never opens SQLite to find a connection's proxy
// (SPEC §5). provider_connections is owned by the credentials store; runtime
// reads it read-only during snapshot compilation.
func loadPoolBindings(ctx context.Context, db *sql.DB) (map[string]string, error) {
	return loadPoolBindingsDB(ctx, db)
}

func loadPoolBindingsDB(ctx context.Context, db *sql.DB) (map[string]string, error) {
	rows, err := db.QueryContext(ctx, "SELECT id,proxy_pool_id FROM provider_connections WHERE proxy_pool_id IS NOT NULL AND proxy_pool_id<>''")
	if err != nil {
		return nil, fmt.Errorf("load proxy pool bindings: %w", err)
	}
	defer rows.Close()
	bindings := make(map[string]string)
	for rows.Next() {
		var connectionID, poolID string
		if err := rows.Scan(&connectionID, &poolID); err != nil {
			return nil, err
		}
		if strings.TrimSpace(connectionID) == "" || strings.TrimSpace(poolID) == "" {
			continue
		}
		bindings[connectionID] = poolID
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(bindings) == 0 {
		return nil, nil
	}
	return bindings, nil
}
