package runtime

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/auth"
)

type Config struct {
	Revision       uint64
	Settings       map[string]string
	APIKeys        []auth.Entry
	Models         []Model
	Aliases        map[string]ModelRef
	DisabledModels map[string]struct{}
}

// Candidate is mutable only while a serialized configuration update is built.
type Candidate struct {
	Settings       map[string]string
	APIKeys        []auth.Entry
	Models         []Model
	Aliases        map[string]ModelRef
	DisabledModels map[string]struct{}
}

func (c *Candidate) Set(key, value string)        { c.Settings[key] = value }
func (c *Candidate) SetAPIKeys(keys []auth.Entry) { c.APIKeys = append([]auth.Entry(nil), keys...) }
func (c *Candidate) AddModel(model Model) {
	for i := range c.Models {
		if c.Models[i].ProviderID == model.ProviderID && c.Models[i].ID == model.ID {
			c.Models[i] = model
			return
		}
	}
	c.Models = append(c.Models, model)
}
func (c *Candidate) SetAlias(alias string, target ModelRef) {
	if c.Aliases == nil {
		c.Aliases = make(map[string]ModelRef)
	}
	c.Aliases[alias] = target
}
func (c *Candidate) SetModelDisabled(provider, model string, disabled bool) {
	if c.DisabledModels == nil {
		c.DisabledModels = make(map[string]struct{})
	}
	key := modelKey(provider, model)
	if disabled {
		c.DisabledModels[key] = struct{}{}
	} else {
		delete(c.DisabledModels, key)
	}
}
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
	if err := validateCatalog(config); err != nil {
		return nil, err
	}
	settings := defaultSettingsCopy()
	for key, value := range config.Settings {
		settings[key] = value
	}
	models, aliases, disabled := compileCatalog(config.Models, config.Aliases, config.DisabledModels)
	return &RuntimeSnapshot{
		version:        version,
		configRevision: config.Revision,
		settings:       settings,
		keys:           auth.NewKeyIndex(config.APIKeys),
		models:         models,
		aliases:        aliases,
		disabledModels: disabled,
	}, nil
}

func validateCatalog(config Config) error {
	models := make(map[string]struct{}, len(config.Models))
	for _, model := range config.Models {
		if strings.TrimSpace(model.ProviderID) == "" || strings.TrimSpace(model.ID) == "" {
			return fmt.Errorf("model provider and id are required")
		}
		key := modelKey(model.ProviderID, model.ID)
		if _, exists := models[key]; exists {
			return fmt.Errorf("duplicate model %q", key)
		}
		models[key] = struct{}{}
	}
	keys := make(map[string]struct{}, len(config.APIKeys))
	for _, key := range config.APIKeys {
		if strings.TrimSpace(key.ID) == "" || strings.TrimSpace(key.Name) == "" {
			return fmt.Errorf("API key id and name are required")
		}
		if len(key.Hash) != 64 {
			return fmt.Errorf("API key %q has an invalid digest", key.ID)
		}
		decoded, err := hex.DecodeString(key.Hash)
		if err != nil {
			return fmt.Errorf("API key %q has an invalid digest: %w", key.ID, err)
		}
		if hex.EncodeToString(decoded) != key.Hash {
			return fmt.Errorf("API key %q digest is not canonical lowercase hex", key.ID)
		}
		if _, exists := keys[key.Hash]; exists {
			return fmt.Errorf("duplicate API key digest")
		}
		keys[key.Hash] = struct{}{}
	}
	for alias, target := range config.Aliases {
		if strings.TrimSpace(alias) == "" || strings.TrimSpace(target.ProviderID) == "" || strings.TrimSpace(target.ModelID) == "" {
			return fmt.Errorf("alias and target are required")
		}
		if _, ok := models[modelKey(target.ProviderID, target.ModelID)]; !ok {
			return fmt.Errorf("alias %q references unknown model %s/%s", alias, target.ProviderID, target.ModelID)
		}
	}
	return nil
}

func cloneSettings(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}
