package migrations

// v13 persists each connection's validation lifecycle beside the connection,
// outside the sealed secret, so the dashboard can show it without opening a
// credential (#220). The reference keeps testStatus, lastTested, lastError and
// lastErrorAt on the connection row (connectionsRepo.js:8-9) and writes them
// from a connection test (testUtils.js:892-899) and from every sign-in
// ("active", [provider]/[action]/route.js:69). Routeweft records three states:
// untested (stored, never tested since its credential last changed), active
// (the provider accepted the credential) and failed (the last test did not
// pass). auth_origin names how the credential arrived (manual, oauth, device,
// import) and credential_expires_at mirrors the sealed secret's expiry so a
// refresh rotation's new expiry is readable without the key. None of these
// columns hold credential material and none enter the runtime snapshot.
// Existing rows start untested. Forward-only: the added columns are ignored by
// an older binary, but rollback still requires the pre-upgrade backup.
var v13 = Migration{
	Version: 13,
	Name:    "connection_validation_status",
	SQL: `
ALTER TABLE provider_connections ADD COLUMN test_status TEXT NOT NULL DEFAULT 'untested' CHECK (test_status IN ('untested','active','failed'));
ALTER TABLE provider_connections ADD COLUMN last_tested_at TEXT;
ALTER TABLE provider_connections ADD COLUMN last_error TEXT;
ALTER TABLE provider_connections ADD COLUMN last_error_at TEXT;
ALTER TABLE provider_connections ADD COLUMN models_available INTEGER;
ALTER TABLE provider_connections ADD COLUMN auth_origin TEXT NOT NULL DEFAULT '';
ALTER TABLE provider_connections ADD COLUMN credential_expires_at TEXT;
`,
}
