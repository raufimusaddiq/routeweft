package migrations

// v1 is the initial Routeweft schema: the normalized durable surface named in
// SPEC section 4. Columns are minimally complete; feature PRs extend the
// schema with their own migrations rather than rewriting v1.
var v1 = Migration{
	Version: 1,
	Name:    "initial_schema",
	SQL: `
CREATE TABLE meta (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL,
    updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE settings (
    key         TEXT PRIMARY KEY,
    value       TEXT NOT NULL,
    updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE admin_users (
    id            TEXT PRIMARY KEY,
    username      TEXT NOT NULL UNIQUE,
    password_hash TEXT NOT NULL,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE proxy_pools (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL UNIQUE,
    enabled     INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    strategy    TEXT NOT NULL DEFAULT 'round_robin',
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE provider_nodes (
    id          TEXT PRIMARY KEY,
    kind        TEXT NOT NULL CHECK (kind IN ('builtin','generic')),
    provider_id TEXT NOT NULL,
    name        TEXT NOT NULL,
    prefix      TEXT UNIQUE,
    base_url    TEXT,
    transports  TEXT,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE provider_connections (
    id            TEXT PRIMARY KEY,
    node_id       TEXT NOT NULL REFERENCES provider_nodes(id) ON DELETE CASCADE,
    name          TEXT NOT NULL,
    auth_kind     TEXT NOT NULL,
    identity      TEXT NOT NULL,
    secret_blob   TEXT,
    enabled       INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    priority      INTEGER NOT NULL DEFAULT 0,
    proxy_pool_id TEXT REFERENCES proxy_pools(id) ON DELETE SET NULL,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (node_id, identity)
);

CREATE TABLE combos (
    id             TEXT PRIMARY KEY,
    name           TEXT NOT NULL UNIQUE,
    strategy       TEXT NOT NULL DEFAULT 'fallback',
    sticky_limit   INTEGER NOT NULL DEFAULT 1,
    fusion_enabled INTEGER NOT NULL DEFAULT 0 CHECK (fusion_enabled IN (0,1)),
    judge_model    TEXT,
    created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    updated_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE combo_models (
    id          TEXT PRIMARY KEY,
    combo_id    TEXT NOT NULL REFERENCES combos(id) ON DELETE CASCADE,
    provider_id TEXT NOT NULL,
    model_id    TEXT NOT NULL,
    position    INTEGER NOT NULL,
    selected    INTEGER NOT NULL DEFAULT 1 CHECK (selected IN (0,1)),
    UNIQUE (combo_id, position)
);

CREATE TABLE model_aliases (
    alias       TEXT PRIMARY KEY,
    provider_id TEXT NOT NULL,
    model_id    TEXT NOT NULL,
    updated_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE custom_models (
    id             TEXT PRIMARY KEY,
    provider_id    TEXT NOT NULL,
    model_id       TEXT NOT NULL,
    display_name   TEXT,
    context_window INTEGER,
    created_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    UNIQUE (provider_id, model_id)
);

CREATE TABLE disabled_models (
    provider_id TEXT NOT NULL,
    model_id    TEXT NOT NULL,
    created_at  TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (provider_id, model_id)
);

CREATE TABLE pricing_overrides (
    provider_id          TEXT NOT NULL,
    model_id             TEXT NOT NULL,
    input_per_mtok       REAL,
    output_per_mtok      REAL,
    cache_read_per_mtok  REAL,
    cache_write_per_mtok REAL,
    updated_at           TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (provider_id, model_id)
);

CREATE TABLE api_keys (
    id           TEXT PRIMARY KEY,
    name         TEXT NOT NULL,
    key_hash     TEXT NOT NULL UNIQUE,
    key_prefix   TEXT NOT NULL,
    enabled      INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0,1)),
    paused       INTEGER NOT NULL DEFAULT 0 CHECK (paused IN (0,1)),
    created_at   TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    last_used_at TEXT
);

CREATE TABLE usage_events (
    id                 INTEGER PRIMARY KEY AUTOINCREMENT,
    request_id         TEXT NOT NULL,
    provider_id        TEXT,
    model_id           TEXT,
    connection_id      TEXT,
    api_key_id         TEXT,
    status             INTEGER NOT NULL,
    input_tokens       INTEGER NOT NULL DEFAULT 0,
    output_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    duration_ms        INTEGER,
    ttft_ms            INTEGER,
    created_at         TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE usage_daily (
    day                TEXT NOT NULL,
    provider_id        TEXT NOT NULL,
    model_id           TEXT NOT NULL,
    requests           INTEGER NOT NULL DEFAULT 0,
    input_tokens       INTEGER NOT NULL DEFAULT 0,
    output_tokens      INTEGER NOT NULL DEFAULT 0,
    cache_read_tokens  INTEGER NOT NULL DEFAULT 0,
    cache_write_tokens INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (day, provider_id, model_id)
);

CREATE TABLE request_details (
    request_id TEXT PRIMARY KEY,
    route_mode TEXT,
    detail     TEXT,
    created_at TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE TABLE credential_events (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    connection_id TEXT,
    event         TEXT NOT NULL,
    detail        TEXT,
    created_at    TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now'))
);

CREATE INDEX idx_provider_connections_node ON provider_connections(node_id);
CREATE INDEX idx_combo_models_combo ON combo_models(combo_id, position);
CREATE INDEX idx_usage_events_created ON usage_events(created_at);
CREATE INDEX idx_usage_events_provider ON usage_events(provider_id, model_id);
`,
}
