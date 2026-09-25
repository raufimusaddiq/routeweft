package migrations

// v4 adds proxy-pool members. Pool membership is normalized (one row per proxy
// endpoint) rather than an opaque JSON blob so rotation, per-connection binding
// and connectivity testing operate on real records (PRD-ROUTE-005, SPEC §4).
var v4 = Migration{
	Version: 4,
	Name:    "proxy_pool_members",
	SQL: `
CREATE TABLE proxy_pool_members (
    id            TEXT PRIMARY KEY,
    pool_id       TEXT NOT NULL REFERENCES proxy_pools(id) ON DELETE CASCADE,
    position      INTEGER NOT NULL DEFAULT 0,
    proxy_url     TEXT NOT NULL,
    enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (pool_id, proxy_url)
);

CREATE INDEX idx_proxy_pool_members_pool ON proxy_pool_members(pool_id, position, id);
`,
}
