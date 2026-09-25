package runtime

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
)

// WritableSettings is the allowlist of settings the control API may mutate.
// Settings not listed here are owned by dedicated primitives (keys, models,
// combos, proxy pools) and must not be set through the generic settings route.
var writableSettings = map[string]struct{}{
	"providerStrategy":             {},
	"requireApiKey":                {},
	"stickyRoundRobinLimit":        {},
	"providerStrategies":           {},
	"comboStrategy":                {},
	"comboStickyRoundRobinLimit":   {},
	"comboStrategies":              {},
	"capacityAdapterVision":        {},
	"capacityAdapterPDF":           {},
	"capacityAdapterAudioInput":    {},
	"capacityAdapterVideoInput":    {},
	"quotaVisibility":              {},
	"enableObservability":          {},
	"observabilityMaxRecords":      {},
	"observabilityBatchSize":       {},
	"observabilityFlushIntervalMs": {},
	"observabilityMaxJsonSize":     {},
	"outboundProxyEnabled":         {},
	"outboundProxyUrl":             {},
	"noProxy":                      {},
	"dnsToolEnabled":               {},
	"providerCompatibility":        {},
	"rtkEnabled":                   {},
	"headroomEnabled":              {},
	"headroomUrl":                  {},
	"headroomCompressUserMessages": {},
	"headroomTimeoutMs":            {},
	"cavemanEnabled":               {},
	"cavemanLevel":                 {},
	"ponytailEnabled":              {},
	"ponytailLevel":                {},
	"pxpipeEnabled":                {},
	"pxpipeAutoInstall":            {},
	"pxpipeMinChars":               {},
	"pxpipeTimeoutMs":              {},
}

// IsWritableSetting reports whether the generic settings route may mutate key.
func IsWritableSetting(key string) bool {
	_, ok := writableSettings[key]
	return ok
}

// WritableSettingNames returns the stable allowlist used by the control API.
func WritableSettingNames() []string {
	keys := make([]string, 0, len(writableSettings))
	for key := range writableSettings {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// Settings returns the active compiled settings as a defensive copy.
func (m *Manager) Settings() map[string]string {
	snapshot, err := m.Load()
	if err != nil {
		return nil
	}
	return snapshot.Settings()
}

// SetSettings applies an allowlisted settings patch through the standard
// compile-before-commit protocol (BDR-007) and returns the new config revision.
// Unknown or non-writable keys are rejected before any state changes.
func (m *Manager) SetSettings(ctx context.Context, values map[string]string, remove []string) (uint64, error) {
	for key := range values {
		if !IsWritableSetting(key) {
			return 0, fmt.Errorf("setting %q is not writable through this route", key)
		}
	}
	for _, key := range remove {
		if !IsWritableSetting(key) {
			return 0, fmt.Errorf("setting %q is not writable through this route", key)
		}
	}
	for key, value := range values {
		if strings.ContainsRune(key, 0) || len(value) > 1<<20 {
			return 0, fmt.Errorf("invalid setting %q", key)
		}
		if key == "requireApiKey" && value != "true" && value != "false" {
			return 0, errors.New("setting \"requireApiKey\" must be \"true\" or \"false\"")
		}
	}
	snapshot, err := m.Update(ctx, func(candidate *Candidate) error {
		for key, value := range values {
			candidate.Set(key, value)
		}
		for _, key := range remove {
			candidate.Delete(key)
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return snapshot.ConfigRevision(), nil
}
