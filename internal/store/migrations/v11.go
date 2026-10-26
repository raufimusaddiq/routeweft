package migrations

// v11 lets a provider connection have no identity (#239). The reference stores
// a Codex connection with no email when the credential names none and never
// dedups it (connectionsRepo.js:228-264), so identity becomes nullable; SQLite's
// UNIQUE treats NULLs as distinct, so such rows never collide while every
// non-NULL identity keeps its (node_id, identity) uniqueness. SQLite cannot drop
// NOT NULL in place, so the table is rebuilt with every row and column copied as
// is: a connection stored with any identity, including a ChatGPT account id from
// the earlier fallback, keeps it. Only an empty identity, which the store never
// accepted, becomes NULL, so "no identity" has one spelling. Nothing references
// provider_connections by foreign key. Forward-only; rollback requires the
// pre-upgrade backup.
var v11 = Migration{
	Version: 11,
	Name:    "connection_identity_optional",
	SQL: `
CREATE TABLE provider_connections_v11 (
    id            TEXT PRIMARY KEY,
    node_id       TEXT NOT NULL REFERENCES provider_nodes(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    auth_kind     TEXT NOT NULL,
    identity      TEXT CHECK (identity IS NULL OR identity <> ''),
    secret_blob   TEXT,
    enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    priority      INTEGER NOT NULL DEFAULT 0,
    proxy_pool_id TEXT REFERENCES proxy_pools(id) ON DELETE SET NULL,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    account_id    TEXT NOT NULL DEFAULT '',
    UNIQUE (node_id, identity)
);
INSERT INTO provider_connections_v11(id,node_id,name,auth_kind,identity,secret_blob,enabled,priority,proxy_pool_id,created_at,updated_at,account_id)
SELECT id,node_id,name,auth_kind,NULLIF(identity,''),secret_blob,enabled,priority,proxy_pool_id,created_at,updated_at,account_id FROM provider_connections;
DROP TABLE provider_connections;
ALTER TABLE provider_connections_v11 RENAME TO provider_connections;
CREATE INDEX idx_provider_connections_node ON provider_connections(node_id);
`,
}
