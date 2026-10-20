package routing

import (
	"errors"
	"fmt"
	"time"
)

// MaxFusionTuningMs caps a per-Combo grace or hard timeout. v1 accepts any
// value (open-sse/services/combo.js:561); SPEC §16 requires the panel timeout
// to be capped, so Routeweft rejects anything above ten minutes.
const MaxFusionTuningMs = 10 * 60 * 1000

// FusionTuning is one Combo's optional panel-timing override: the pinned
// comboStrategies[name].fusionTuning object (src/sse/handlers/chat.js:132,212;
// keys minPanel / stragglerGraceMs / panelHardTimeoutMs, combo.js:473-475). A
// nil field keeps the default, exactly as an absent key does in v1's
// `{...FUSION_DEFAULTS, ...tuning}` (combo.js:561).
type FusionTuning struct {
	MinPanel           *int
	StragglerGraceMs   *int64
	PanelHardTimeoutMs *int64
}

// IsZero reports whether the Combo overrides nothing.
func (t FusionTuning) IsZero() bool {
	return t.MinPanel == nil && t.StragglerGraceMs == nil && t.PanelHardTimeoutMs == nil
}

// Validate rejects values v1 would run but cannot mean: a negative number, a
// hard timeout of zero (v1 finishes the panel before any answer arrives), and
// anything beyond MaxFusionTuningMs.
func (t FusionTuning) Validate() error {
	if t.MinPanel != nil && *t.MinPanel < 0 {
		return errors.New("fusion tuning: minPanel must not be negative")
	}
	if t.StragglerGraceMs != nil && (*t.StragglerGraceMs < 0 || *t.StragglerGraceMs > MaxFusionTuningMs) {
		return fmt.Errorf("fusion tuning: stragglerGraceMs must be 0..%d", MaxFusionTuningMs)
	}
	if t.PanelHardTimeoutMs != nil && (*t.PanelHardTimeoutMs <= 0 || *t.PanelHardTimeoutMs > MaxFusionTuningMs) {
		return fmt.Errorf("fusion tuning: panelHardTimeoutMs must be 1..%d", MaxFusionTuningMs)
	}
	return nil
}

// Clone returns a copy that shares no pointee with t, so a published Combo
// cannot be mutated through a caller's copy (AGENTS §3).
func (t FusionTuning) Clone() FusionTuning {
	if t.MinPanel != nil {
		v := *t.MinPanel
		t.MinPanel = &v
	}
	if t.StragglerGraceMs != nil {
		v := *t.StragglerGraceMs
		t.StragglerGraceMs = &v
	}
	if t.PanelHardTimeoutMs != nil {
		v := *t.PanelHardTimeoutMs
		t.PanelHardTimeoutMs = &v
	}
	return t
}

// Normalize returns the tuning as it resolves: a minPanel below 2 is stored as
// 2 (combo.js:562), so what is stored is what runs.
func (t FusionTuning) Normalize() FusionTuning {
	t = t.Clone()
	if t.MinPanel != nil && *t.MinPanel < 2 {
		two := 2
		t.MinPanel = &two
	}
	return t
}

// Apply returns base with the Combo's overrides resolved the way v1 resolves
// them for a panel of the given size: the quorum gate is clamped into
// [2, panels] (combo.js:562), so minPanel 0 or 1 still means two.
func (t FusionTuning) Apply(base FusionConfig, panels int) (FusionConfig, error) {
	if err := t.Validate(); err != nil {
		return FusionConfig{}, err
	}
	config := base
	if t.MinPanel != nil {
		config.MinPanelQuorum = *t.MinPanel
	}
	if config.MinPanelQuorum < 2 {
		config.MinPanelQuorum = 2
	}
	if panels > 0 && config.MinPanelQuorum > panels {
		config.MinPanelQuorum = panels
	}
	if t.StragglerGraceMs != nil {
		config.Grace = time.Duration(*t.StragglerGraceMs) * time.Millisecond
	}
	if t.PanelHardTimeoutMs != nil {
		config.HardTimeout = time.Duration(*t.PanelHardTimeoutMs) * time.Millisecond
	}
	return config, nil
}
