package migrations

var v7 = Migration{
	Version: 7,
	Name:    "model_service_kind",
	SQL:     `ALTER TABLE provider_models ADD COLUMN kind TEXT NOT NULL DEFAULT '' CHECK(kind IN ('','llm','systemone'));`,
}
