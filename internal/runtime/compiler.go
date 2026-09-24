package runtime

import (
	"fmt"
	"strings"
)

type Config struct {
	Revision uint64
	Settings map[string]string
}

// Candidate is mutable only while a serialized configuration update is built.
type Candidate struct {
	Settings map[string]string
}

func (c *Candidate) Set(key, value string) { c.Settings[key] = value }
func (c *Candidate) Delete(key string) {
	if value, ok := defaultSettings[key]; ok {
		c.Settings[key] = value
		return
	}
	delete(c.Settings, key)
}

var defaultSettings = map[string]string{
	"requireLogin":                 "true",
	"requireApiKey":                "true",
	"stickyRoundRobinLimit":        "3",
	"providerStrategies":           "{}",
	"quotaVisibility":              "{}",
	"comboStrategy":                "fallback",
	"comboStickyRoundRobinLimit":   "1",
	"comboStrategies":              "{}",
	"capacityAdapterVision":        "{\"enabled\":false,\"pool\":[]}",
	"capacityAdapterPDF":           "{\"enabled\":false,\"pool\":[]}",
	"capacityAdapterAudioInput":    "{\"enabled\":false,\"pool\":[]}",
	"capacityAdapterVideoInput":    "{\"enabled\":false,\"pool\":[]}",
	"enableObservability":          "false",
	"observabilityMaxRecords":      "1000",
	"observabilityBatchSize":       "20",
	"observabilityFlushIntervalMs": "5000",
	"observabilityMaxJsonSize":     "5242880",
	"outboundProxyEnabled":         "false",
	"outboundProxyUrl":             "",
	"noProxy":                      "[]",
	"dnsToolEnabled":               "false",
	"providerCompatibility":        "{}",
	"rtkEnabled":                   "true",
	"headroomEnabled":              "false",
	"headroomUrl":                  "http://localhost:8787",
	"headroomCompressUserMessages": "false",
	"headroomTimeoutMs":            "3000",
	"cavemanEnabled":               "false",
	"cavemanLevel":                 "full",
	"ponytailEnabled":              "false",
	"ponytailLevel":                "full",
	"pxpipeEnabled":                "false",
	"pxpipeAutoInstall":            "true",
	"pxpipeMinChars":               "25000",
	"pxpipeTimeoutMs":              "15000",
}

func defaultSettingsCopy() map[string]string { return cloneSettings(defaultSettings) }

type Compiler struct{}

func (Compiler) Compile(config Config, version uint64) (*RuntimeSnapshot, error) {
	for key := range config.Settings {
		if strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("setting key must not be empty")
		}
	}
	settings := defaultSettingsCopy()
	for key, value := range config.Settings {
		settings[key] = value
	}
	return &RuntimeSnapshot{
		version:        version,
		configRevision: config.Revision,
		settings:       settings,
	}, nil
}

func cloneSettings(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}
