package migrations

// v9 lets a usage record say what is known and what is not. termination is the
// empty string for a request that ran to its natural end, 'client_abandoned' when the client
// went away mid-flight and 'upstream_truncated' when the upstream stream ended
// without its terminal event. usage_observed=0 means the upstream never
// reported usage for the request, so the token columns hold no measurement.
// Existing rows default to a completed request with observed usage, which is
// what every row written before this version claimed. The token columns stay
// NOT NULL (SQLite cannot relax a column without rebuilding the table), so
// readers must consult usage_observed instead of trusting a zero.
// Forward-only; rollback requires the pre-upgrade backup.
var v9 = Migration{
	Version: 9,
	Name:    "usage_event_termination",
	SQL: `ALTER TABLE usage_events ADD COLUMN termination TEXT NOT NULL DEFAULT '';
ALTER TABLE usage_events ADD COLUMN usage_observed INTEGER NOT NULL DEFAULT 1;`,
}
