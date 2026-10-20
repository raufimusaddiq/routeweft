package runtime

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// parseNoProxy decodes the noProxy setting: a JSON array of host patterns.
// The pin stores one comma-separated string, outboundNoProxy
// (src/lib/db/repos/settingsRepo.js:39), and splits it per request
// (open-sse/utils/proxyFetch.js:17). Entries are trimmed and lowercased and
// blanks dropped, matching that split, so the compiled list is canonical.
func parseNoProxy(raw string) ([]string, error) {
	var entries []string
	if err := json.Unmarshal([]byte(raw), &entries); err != nil || entries == nil {
		return nil, errors.New("noProxy must be a JSON array of strings")
	}
	return canonicalNoProxy(entries), nil
}

func canonicalNoProxy(entries []string) []string {
	out := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry = trimmedLower(entry); entry != "" {
			out = append(out, entry)
		}
	}
	return out
}

// parseDNSToolEnabled decodes dnsToolEnabled: a provider-keyed boolean map, as
// in the pin (settingsRepo.js:40, default {}).
func parseDNSToolEnabled(raw string) (map[string]bool, error) {
	var entries map[string]bool
	if err := json.Unmarshal([]byte(raw), &entries); err != nil || entries == nil {
		return nil, errors.New("dnsToolEnabled must be a JSON object of provider to boolean")
	}
	return entries, nil
}

// normalizeLegacySettings rewrites the one earlier Routeweft shape that is no
// longer valid: dnsToolEnabled used to be a bare boolean. A boolean cannot name
// providers, so it compiles to the empty map (nothing consumed it). Any other
// unparseable value is left as stored rather than discarded as if it were the
// legacy boolean; nothing reads the compiled value, and SetSettings rejects it
// on the next write.
func normalizeLegacySettings(settings map[string]string) {
	if raw := strings.TrimSpace(settings["dnsToolEnabled"]); raw == "true" || raw == "false" {
		settings["dnsToolEnabled"] = "{}"
	}
}

// CapacityAdapterConfig is one capability's compiled capacity-adapter pool.
// The pin nests {enabled, roundRobin, models} under settings.capacityAdapter
// (settingsRepo.js:24-29, open-sse/services/capacityAdapter.js:18-30).
type CapacityAdapterConfig struct {
	Enabled    bool
	RoundRobin bool
	Pool       []ModelRef
}

// Strategy is the pool's selection policy: round-robin only when the pool is
// enabled and roundRobin is set (capacityAdapter.js:57-60).
func (c CapacityAdapterConfig) Strategy() routing.Strategy {
	if c.Enabled && c.RoundRobin {
		return routing.StrategyRoundRobin
	}
	return routing.StrategyFillFirst
}

type capacityAdapterWire struct {
	Enabled    bool `json:"enabled"`
	RoundRobin bool `json:"roundRobin"`
	Pool       []struct {
		ProviderID string `json:"providerId"`
		ModelID    string `json:"modelId"`
	} `json:"pool"`
}

func parseCapacityAdapter(raw string) (CapacityAdapterConfig, error) {
	var wire capacityAdapterWire
	if strings.TrimSpace(raw) == "" {
		return CapacityAdapterConfig{}, nil
	}
	if err := json.Unmarshal([]byte(raw), &wire); err != nil {
		return CapacityAdapterConfig{}, err
	}
	config := CapacityAdapterConfig{Enabled: wire.Enabled, RoundRobin: wire.RoundRobin}
	for _, member := range wire.Pool {
		if member.ProviderID == "" || member.ModelID == "" {
			return CapacityAdapterConfig{}, errors.New("pool members require providerId and modelId")
		}
		config.Pool = append(config.Pool, ModelRef{ProviderID: member.ProviderID, ModelID: member.ModelID})
	}
	return config, nil
}

// capacityAdapterSettings maps the public capability name to its durable key.
// vision and audio-input are the visible adapters; pdf and video-input exist
// without standalone media APIs (PRD-COMBO-004). The pin's audioInput and
// videoInput are the kebab-case names here (PARITY D10).
var capacityAdapterSettings = []CapacityAdapterBinding{
	{"vision", "capacityAdapterVision"},
	{"audio-input", "capacityAdapterAudioInput"},
	{"pdf", "capacityAdapterPDF"},
	{"video-input", "capacityAdapterVideoInput"},
}

// CapacityAdapterSetting returns the settings key for a capability name.
func CapacityAdapterSetting(name string) (string, bool) {
	for _, adapter := range capacityAdapterSettings {
		if adapter.Name == name {
			return adapter.Setting, true
		}
	}
	return "", false
}

// CapacityAdapterBinding names one capability adapter and its settings key.
type CapacityAdapterBinding struct{ Name, Setting string }

// CapacityAdapters lists every capability adapter in display order.
func CapacityAdapters() []CapacityAdapterBinding {
	return append([]CapacityAdapterBinding(nil), capacityAdapterSettings...)
}

// CapacityAdapter returns the compiled adapter for one capability. An unknown
// capability or malformed stored value yields a disabled adapter, which is the
// no-op the empty/deselect contract requires.
func (s *RuntimeSnapshot) CapacityAdapter(capability string) CapacityAdapterConfig {
	setting, ok := CapacityAdapterSetting(capability)
	if !ok || s == nil {
		return CapacityAdapterConfig{}
	}
	config, err := parseCapacityAdapter(s.settings[setting])
	if err != nil {
		return CapacityAdapterConfig{}
	}
	return config
}
