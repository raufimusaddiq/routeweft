package migrations

// v6 stores per-key model/Combo access policy (C02/U02). Existing keys stay
// unrestricted so an upgrade never narrows a working integration.
var v6 = Migration{
	Version: 6,
	Name:    "api_key_access_policy",
	SQL: `
ALTER TABLE api_keys ADD COLUMN access_restricted INTEGER NOT NULL DEFAULT 0 CHECK (access_restricted IN (0,1));
ALTER TABLE api_keys ADD COLUMN access_allow TEXT NOT NULL DEFAULT '[]';
`,
}
