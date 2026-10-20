package migrations

// v10 stores a Combo's optional Fusion panel timing (C06): the pinned v1
// comboStrategies[name].fusionTuning keys minPanel, stragglerGraceMs and
// panelHardTimeoutMs. NULL means "use the default" for that key, so every
// existing Combo keeps resolving to the default timing. Forward-only;
// rollback requires the pre-upgrade backup.
var v10 = Migration{
	Version: 10,
	Name:    "combo_fusion_tuning",
	SQL: `
ALTER TABLE combos ADD COLUMN fusion_min_panel INTEGER CHECK (fusion_min_panel IS NULL OR fusion_min_panel >= 0);
ALTER TABLE combos ADD COLUMN fusion_grace_ms INTEGER CHECK (fusion_grace_ms IS NULL OR fusion_grace_ms >= 0);
ALTER TABLE combos ADD COLUMN fusion_hard_timeout_ms INTEGER CHECK (fusion_hard_timeout_ms IS NULL OR fusion_hard_timeout_ms > 0);
`,
}
