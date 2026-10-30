package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/transport"
)

// WritableSettings is the allowlist of settings the control API may mutate.
// Settings not listed here are owned by dedicated primitives (keys, models,
// combos, proxy pools) and must not be set through the generic settings route.
var writableSettings = map[string]struct{}{
	"providerStrategy":              {},
	"requireApiKey":                 {},
	"stickyRoundRobinLimit":         {},
	"providerStrategies":            {},
	"comboStrategy":                 {},
	"comboStickyRoundRobinLimit":    {},
	"comboStrategies":               {},
	"capacityAdapterVision":         {},
	"capacityAdapterPDF":            {},
	"capacityAdapterAudioInput":     {},
	"capacityAdapterVideoInput":     {},
	"quotaVisibility":               {},
	"claudeAutoPing":                {},
	"codexAutoPing":                 {},
	"enableObservability":           {},
	"observabilityMaxRecords":       {},
	"observabilityBatchSize":        {},
	"observabilityFlushIntervalMs":  {},
	"observabilityMaxJsonSize":      {},
	"observabilityMaxDetailRecords": {},
	"outboundProxyEnabled":          {},
	"outboundProxyUrl":              {},
	"noProxy":                       {},
	"dnsToolEnabled":                {},
	"providerCompatibility":         {},
	"rtkEnabled":                    {},
	"headroomEnabled":               {},
	"headroomUrl":                   {},
	"headroomCompressUserMessages":  {},
	"headroomTimeoutMs":             {},
	"cavemanEnabled":                {},
	"cavemanLevel":                  {},
	"ponytailEnabled":               {},
	"ponytailLevel":                 {},
	"pxpipeEnabled":                 {},
	"pxpipeUrl":                     {},
	"pxpipeAutoInstall":             {},
	"pxpipeMinChars":                {},
	"pxpipeTimeoutMs":               {},
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
		if err := validateSettingValue(key, value); err != nil {
			return 0, err
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

func validateSettingValue(key, value string) error {
	invalid := func() error { return fmt.Errorf("setting %q has an invalid value", key) }
	switch key {
	case "requireApiKey", "enableObservability", "outboundProxyEnabled", "rtkEnabled", "headroomEnabled", "headroomCompressUserMessages", "cavemanEnabled", "ponytailEnabled", "pxpipeEnabled", "pxpipeAutoInstall":
		if value != "true" && value != "false" {
			return invalid()
		}
	case "providerStrategy":
		if value != "" && value != "fill-first" && value != "round-robin" && value != "sticky-round-robin" {
			return invalid()
		}
	case "comboStrategy":
		if value != "" && value != "fallback" && value != "round-robin" && value != "sticky-round-robin" {
			return invalid()
		}
	case "cavemanLevel", "ponytailLevel":
		if value != "lite" && value != "full" && value != "ultra" {
			return invalid()
		}
	case "headroomTimeoutMs", "pxpipeTimeoutMs", "observabilityFlushIntervalMs":
		n, err := strconv.ParseInt(value, 10, 64)
		if err != nil || n <= 0 || n > int64((1<<63-1)/time.Millisecond) {
			return invalid()
		}
	case "pxpipeMinChars":
		n, err := strconv.Atoi(value)
		if err != nil || n <= 0 {
			return invalid()
		}
	case "headroomUrl", "pxpipeUrl":
		if value == "" && key == "pxpipeUrl" {
			return nil
		}
		if _, err := transport.ValidateTrustedLocalURL(value); err != nil {
			return invalid()
		}
	case "stickyRoundRobinLimit", "comboStickyRoundRobinLimit", "observabilityMaxRecords", "observabilityBatchSize", "observabilityMaxJsonSize", "observabilityMaxDetailRecords":
		number, err := strconv.Atoi(value)
		minimum := 1
		if key == "stickyRoundRobinLimit" || key == "comboStickyRoundRobinLimit" {
			minimum = 0
		}
		if err != nil || number < minimum {
			return invalid()
		}
	case "providerStrategies", "comboStrategies":
		if _, err := parseStrategyOverrides(value, key == "comboStrategies"); err != nil {
			return fmt.Errorf("setting %q has an invalid value: %w", key, err)
		}
	case "dnsToolEnabled":
		if _, err := parseDNSToolEnabled(value); err != nil {
			return invalid()
		}
	case "capacityAdapterVision", "capacityAdapterPDF", "capacityAdapterAudioInput", "capacityAdapterVideoInput":
		if _, err := parseCapacityAdapter(value); err != nil {
			return invalid()
		}
	case "claudeAutoPing", "codexAutoPing":
		var config map[string]json.RawMessage
		if json.Unmarshal([]byte(value), &config) != nil || config == nil || len(config) != 1 {
			return invalid()
		}
		var connections map[string]json.RawMessage
		if json.Unmarshal(config["connections"], &connections) != nil || connections == nil {
			return invalid()
		}
		for id, enabled := range connections {
			if strings.TrimSpace(id) == "" || string(enabled) != "true" && string(enabled) != "false" {
				return invalid()
			}
		}
	case "quotaVisibility", "providerCompatibility":
		var entries map[string]json.RawMessage
		if json.Unmarshal([]byte(value), &entries) != nil || entries == nil {
			return invalid()
		}
	case "noProxy":
		if _, err := parseNoProxy(value); err != nil {
			return invalid()
		}
	case "outboundProxyUrl":
		if value == "" {
			return nil
		}
		parsed, err := url.Parse(value)
		if err != nil || parsed.Host == "" || parsed.Scheme != "http" && parsed.Scheme != "https" {
			return invalid()
		}
		if port := parsed.Port(); port != "" {
			portNumber, err := strconv.Atoi(port)
			if err != nil || portNumber < 1 || portNumber > 65535 {
				return invalid()
			}
		}
	}
	return nil
}
