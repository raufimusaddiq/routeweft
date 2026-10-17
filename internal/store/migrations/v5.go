package migrations

var v5 = Migration{
	Version: 5,
	Name:    "connection_account_id",
	SQL:     `ALTER TABLE provider_connections ADD COLUMN account_id TEXT NOT NULL DEFAULT '';`,
}
