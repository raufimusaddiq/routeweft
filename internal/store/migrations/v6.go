package migrations

var v6 = Migration{
	Version: 6,
	Name:    "model_service_kind",
	SQL:     `ALTER TABLE provider_models ADD COLUMN kind TEXT NOT NULL DEFAULT '' CHECK(kind IN ('','llm','systemone'));`,
}
