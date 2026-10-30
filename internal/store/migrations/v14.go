package migrations

// v14 lets the inference path say what it knows about a request (#273).
// usage_events gains the model the client asked for and the provider/model the
// router selected (model_id already holds the model the provider was called
// with), and the cost the request was priced at, in dollars. usage_daily gains
// the same cost, summed. cost is computed when the event is written, from the
// operator's price override or else the default catalog, so a later price change
// does not rewrite history. request_details gains the attribution columns the
// reference filters its detail list by (provider, model, account, status) and an
// index on the time column the retention and listing queries order by. None of
// the new columns hold a credential. Existing rows keep NULL attribution and a
// cost of 0, which is what they were recorded with. Forward-only: an older
// binary ignores the added columns, but rollback still requires the pre-upgrade
// backup.
var v14 = Migration{
	Version: 14,
	Name:    "inference_observability",
	SQL: `
ALTER TABLE usage_events ADD COLUMN requested_model TEXT;
ALTER TABLE usage_events ADD COLUMN selected_model TEXT;
ALTER TABLE usage_events ADD COLUMN cost REAL NOT NULL DEFAULT 0;
ALTER TABLE usage_daily ADD COLUMN cost REAL NOT NULL DEFAULT 0;
ALTER TABLE request_details ADD COLUMN provider_id TEXT;
ALTER TABLE request_details ADD COLUMN model_id TEXT;
ALTER TABLE request_details ADD COLUMN connection_id TEXT;
ALTER TABLE request_details ADD COLUMN status TEXT;
CREATE INDEX idx_request_details_created ON request_details(created_at);
`,
}
