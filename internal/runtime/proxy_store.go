package runtime

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

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
