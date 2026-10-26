package migrations

// v12 lets the built-in Codex node hold several connections with one identity
// (#241). The reference keeps a second Codex row for the same email whenever the
// ChatGPT account ids differ or either is missing, and never dedups a token import
// (connectionsRepo.js:239-242,264), so UNIQUE(node_id, identity) cannot stay on
// that node. A partial index cannot name the provider (node ids are opaque and an
// index predicate cannot read provider_nodes), so the table-level UNIQUE is
// dropped and the same rule is kept for every other node by triggers that raise
// SQLite's own UNIQUE message. NULL identities never collide, as in v11. SQLite
// cannot drop a table constraint in place, so the table is rebuilt and every row
// and column is copied unchanged. Forward-only: once Codex duplicates exist, the
// pre-v12 schema cannot hold them, so rollback requires the pre-upgrade backup.
var v12 = Migration{
	Version: 12,
	Name:    "codex_connection_identity_duplicates",
	SQL: `
CREATE TABLE provider_connections_v12 (
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
    account_id    TEXT NOT NULL DEFAULT ''
);
INSERT INTO provider_connections_v12(id,node_id,name,auth_kind,identity,secret_blob,enabled,priority,proxy_pool_id,created_at,updated_at,account_id)
SELECT id,node_id,name,auth_kind,identity,secret_blob,enabled,priority,proxy_pool_id,created_at,updated_at,account_id FROM provider_connections;
DROP TABLE provider_connections;
ALTER TABLE provider_connections_v12 RENAME TO provider_connections;
CREATE INDEX idx_provider_connections_node ON provider_connections(node_id);
CREATE INDEX idx_provider_connections_identity ON provider_connections(node_id, identity);
CREATE TRIGGER provider_connections_identity_unique_insert
BEFORE INSERT ON provider_connections
WHEN NEW.identity IS NOT NULL
 AND NOT EXISTS (SELECT 1 FROM provider_nodes WHERE id=NEW.node_id AND kind='builtin' AND provider_id='codex')
 AND EXISTS (SELECT 1 FROM provider_connections WHERE node_id=NEW.node_id AND identity=NEW.identity AND id<>NEW.id)
BEGIN
    SELECT RAISE(ABORT, 'UNIQUE constraint failed: provider_connections.node_id, provider_connections.identity');
END;
CREATE TRIGGER provider_connections_identity_unique_update
BEFORE UPDATE OF node_id, identity ON provider_connections
WHEN NEW.identity IS NOT NULL
 AND NOT EXISTS (SELECT 1 FROM provider_nodes WHERE id=NEW.node_id AND kind='builtin' AND provider_id='codex')
 AND EXISTS (SELECT 1 FROM provider_connections WHERE node_id=NEW.node_id AND identity=NEW.identity AND id<>NEW.id)
BEGIN
    SELECT RAISE(ABORT, 'UNIQUE constraint failed: provider_connections.node_id, provider_connections.identity');
END;
`,
}
