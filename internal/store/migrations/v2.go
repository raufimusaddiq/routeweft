package migrations

// v2 adds the inference credential and model-catalog columns that the PR 6
// compile primitives persist transactionally with the config revision.
var v2 = Migration{
	Version: 2,
	Name:    "client_keys_and_model_catalog",
	SQL: `
CREATE TABLE provider_models (
    provider_id    TEXT NOT NULL,
    model_id       TEXT NOT NULL,
    display_name   TEXT,
    context_window INTEGER,
    capabilities   TEXT NOT NULL DEFAULT '[]',
    updated_at     TEXT NOT NULL DEFAULT (strftime('%Y-%m-%dT%H:%M:%fZ','now')),
    PRIMARY KEY (provider_id, model_id)
);
CREATE INDEX idx_provider_models_provider ON provider_models(provider_id, model_id);
`,
}
