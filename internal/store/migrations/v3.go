package migrations

// v3 indexes the normalized ordered Combo members already present in v1.
var v3 = Migration{
	Version: 3,
	Name:    "combo_member_order_index",
	SQL:     `CREATE INDEX idx_combo_models_ordered ON combo_models(combo_id, position, id);`,
}
