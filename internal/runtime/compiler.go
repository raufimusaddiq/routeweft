package runtime

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/auth"
	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/capabilities"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	transformbody "github.com/raufimusaddiq/routeweft/internal/transforms/body"
	"github.com/raufimusaddiq/routeweft/internal/transforms/pxpipe"
)

type Config struct {
	Providers      credentials.Configuration
	Revision       uint64
	Settings       map[string]string
	APIKeys        []auth.Entry
	Models         []Model
	Aliases        map[string]ModelRef
	DisabledModels map[string]struct{}
	Combos         []Combo
	PoolBindings   map[string]string
	ProxyPools     []ProxyPool
}

// ProxyPool is a compiled proxy pool input. It mirrors the durable record but is
// owned by runtime so the snapshot carries pool membership without importing the
// proxy package (avoiding an import cycle).
type ProxyPool struct {
	ID       string
	Enabled  bool
	Strategy string
	Members  []ProxyMember
	// Kind is "http" (members are forward proxies) or a relay kind (vercel,
	// cloudflare, deno, netlify), whose members carry a request in x-relay-*
	// headers. Strict and NoProxy are the pool's own guarantee and bypass list;
	// UpdatedAt orders no-auth rotation (newest first), as the reference does.
	Kind      string
	Strict    bool
	NoProxy   string
	UpdatedAt string
}

// Candidate is mutable only while a serialized configuration update is built.
type Candidate struct {
	Providers      credentials.Configuration
	Settings       map[string]string
	APIKeys        []auth.Entry
	Models         []Model
	Aliases        map[string]ModelRef
	DisabledModels map[string]struct{}
	Combos         []Combo
	PoolBindings   map[string]string
	ProxyPools     []ProxyPool
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
	"requireLogin":                  "true",
	"requireApiKey":                 "true",
	"stickyRoundRobinLimit":         "3",
	"providerStrategies":            "{}",
	"quotaVisibility":               "{}",
	"claudeAutoPing":                `{"connections":{}}`,
	"codexAutoPing":                 `{"connections":{}}`,
	"comboStrategy":                 "fallback",
	"comboStickyRoundRobinLimit":    "1",
	"comboStrategies":               "{}",
	"capacityAdapterVision":         "{\"enabled\":false,\"roundRobin\":false,\"pool\":[]}",
	"capacityAdapterPDF":            "{\"enabled\":false,\"roundRobin\":false,\"pool\":[]}",
	"capacityAdapterAudioInput":     "{\"enabled\":false,\"roundRobin\":false,\"pool\":[]}",
	"capacityAdapterVideoInput":     "{\"enabled\":false,\"roundRobin\":false,\"pool\":[]}",
	"enableObservability":           "false",
	"observabilityMaxRecords":       "1000",
	"observabilityBatchSize":        "20",
	"observabilityFlushIntervalMs":  "5000",
	"observabilityMaxJsonSize":      "5242880",
	"observabilityMaxDetailRecords": "1000",
	"outboundProxyEnabled":          "false",
	"outboundProxyUrl":              "",
	"noProxy":                       "[]",
	"dnsToolEnabled":                "{}",
	"providerCompatibility":         "{}",
	"rtkEnabled":                    "true",
	"headroomEnabled":               "false",
	"headroomUrl":                   "http://localhost:8787",
	"headroomCompressUserMessages":  "false",
	"headroomTimeoutMs":             "3000",
	"cavemanEnabled":                "false",
	"cavemanLevel":                  "full",
	"ponytailEnabled":               "false",
	"ponytailLevel":                 "full",
	"pxpipeEnabled":                 "false",
	"pxpipeUrl":                     "",
	"pxpipeAutoInstall":             "true",
	"pxpipeMinChars":                "25000",
	"pxpipeTimeoutMs":               "15000",
}

func defaultSettingsCopy() map[string]string { return cloneSettings(defaultSettings) }

type Compiler struct {
	// PXPIPEObserver receives PXPIPE attempt diagnostics from every compiled
	// snapshot's saver pipeline.
	PXPIPEObserver func(pxpipe.Diagnostic)
}

func (c Compiler) Compile(config Config, version uint64) (*RuntimeSnapshot, error) {
	// Built-in catalog seeds are selectable without requiring discovery first.
	config.Models = append([]Model(nil), config.Models...)
	known := make(map[string]bool)
	for _, model := range config.Models {
		known[modelKey(model.ProviderID, model.ID)] = true
	}
	configured := make(map[string]bool)
	for _, node := range config.Providers.Nodes {
		if node.Kind == credentials.NodeBuiltin {
			configured[node.ProviderID] = true
		}
	}
	for _, spec := range registry.Builtins() {
		if !configured[spec.ID] {
			continue
		}
		for _, seed := range spec.SeedModels {
			if !known[modelKey(spec.ID, seed.ID)] {
				config.Models = append(config.Models, Model{ProviderID: spec.ID, ID: seed.ID, Name: seed.Name, ContextWindow: seed.ContextWindow, Source: "seed"})
				known[modelKey(spec.ID, seed.ID)] = true
			}
		}
	}
	for key := range config.Settings {
		if strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("setting key must not be empty")
		}
	}
	if err := validateCatalog(config); err != nil {
		return nil, err
	}
	providers, err := compileProviders(config.Providers)
	if err != nil {
		return nil, err
	}
	settings := defaultSettingsCopy()
	for key, value := range config.Settings {
		settings[key] = value
	}
	normalizeLegacySettings(settings)
	// requireLogin is immutable-true (#78 C03, docs/SPEC.md 12.2). Nothing reads
	// it to gate a route; pinning the compiled value keeps a stored or restored
	// "false" from being reported as if login were optional.
	settings["requireLogin"] = "true"
	models, aliases, disabled := compileCatalog(config.Models, config.Aliases, config.DisabledModels)
	combos, err := compileCombos(config.Combos, models, aliases)
	if err != nil {
		return nil, err
	}
	snapshot := &RuntimeSnapshot{
		modelCapabilities: capabilities.New(),
		savers:            transformbody.Compile(settings, c.PXPIPEObserver),
		telemetryOptions:  telemetryOptions(settings),
		detailSizeLimit:   observabilityMaxJSONSize(settings),
		detailMaxRecords:  observabilityMaxDetailRecords(settings),
		providerConfig:    config.Providers.Clone(),
		providers:         providers,
		version:           version,
		configRevision:    config.Revision,
		settings:          settings,
		keys:              auth.NewKeyIndex(config.APIKeys),
		models:            models,
		aliases:           aliases,
		disabledModels:    disabled,
		combos:            combos,
		poolBindings:      cloneStringMap(config.PoolBindings),
		poolPools:         compileProxyPools(config.ProxyPools),
		connectionProxies: compileConnectionProxies(config.Providers.Connections),
	}
	snapshot.proxyEnabled, _ = strconv.ParseBool(strings.TrimSpace(settings["outboundProxyEnabled"]))
	snapshot.proxyURL = strings.TrimSpace(settings["outboundProxyUrl"])
	snapshot.noProxy, _ = parseNoProxy(settings["noProxy"])
	snapshot.compileCapacityAdapters()
	snapshot.compileNoAuthProxies(config.Providers.Connections)
	snapshot.nested = nestedCombos(combos)
	snapshot.fusion = effectiveFusion(combos, strategyOverridesTolerant(settings["comboStrategies"], true))
	snapshot.compileServingIndexes()
	snapshot.compileKeyAccess(config.APIKeys)
	return snapshot, nil
}

func compileProxyPools(pools []ProxyPool) map[string]proxyPoolEntry {
	if len(pools) == 0 {
		return nil
	}
	compiled := make(map[string]proxyPoolEntry, len(pools))
	for _, pool := range pools {
		if strings.TrimSpace(pool.ID) == "" {
			continue
		}
		strategy := strings.TrimSpace(pool.Strategy)
		if strategy == "" {
			strategy = "round_robin"
		}
		kind := strings.TrimSpace(pool.Kind)
		if kind == "" {
			kind = "http"
		}
		compiled[pool.ID] = proxyPoolEntry{enabled: pool.Enabled, strategy: strategy, members: append([]ProxyMember(nil), pool.Members...),
			kind: kind, strict: pool.Strict, noProxy: strings.TrimSpace(pool.NoProxy), updatedAt: pool.UpdatedAt}
	}
	return compiled
}

// compileConnectionProxies compiles every connection's LiteRouter
// per-connection proxy, disabled connections included, since a connection test
// or usage read of a disabled connection still leaves through it.
func compileConnectionProxies(connections []credentials.Connection) map[string]ConnectionProxy {
	var compiled map[string]ConnectionProxy
	for _, connection := range connections {
		settings := connection.Settings
		// The pin enables only a JSON true (connectionProxy.js:40-41). Import stores
		// this key as its JSON text (onboarding.typedProviderData) and the admin
		// API writes "true"/"false", so only a boolean true reads "true"; a string
		// "true" is stored quoted.
		if settings.ConnectionProxyEnabled != "true" {
			continue
		}
		proxyURL := LegacyProxyURL(settings.ConnectionProxyURL)
		if proxyURL == "" {
			continue
		}
		if compiled == nil {
			compiled = make(map[string]ConnectionProxy)
		}
		compiled[connection.ID] = ConnectionProxy{URL: proxyURL, NoProxy: legacyNoProxy(settings.ConnectionNoProxy)}
	}
	return compiled
}

// LegacyProxyURL trims a stored connectionProxyUrl and, like the pin's
// normalizeProxyUrl (open-sse/utils/proxyFetch.js:48-60), reads a value with
// no scheme and host, such as "127.0.0.1:7890", as an http proxy.
func LegacyProxyURL(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return ""
	}
	if parsed, err := url.Parse(trimmed); err == nil && parsed.Scheme != "" && parsed.Host != "" {
		return trimmed
	}
	return "http://" + trimmed
}

// legacyNoProxy splits a stored connectionNoProxy on commas, as the pin's
// shouldBypassByNoProxy does (open-sse/utils/proxyFetch.js:11-24).
func legacyNoProxy(raw string) []string {
	var entries []string
	for _, entry := range strings.Split(raw, ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			entries = append(entries, entry)
		}
	}
	return entries
}

func cloneStringMap(src map[string]string) map[string]string {
	if len(src) == 0 {
		return nil
	}
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}

func validateCatalog(config Config) error {
	models := make(map[string]struct{}, len(config.Models))
	for _, model := range config.Models {
		if strings.TrimSpace(model.ProviderID) == "" || strings.TrimSpace(model.ID) == "" {
			return fmt.Errorf("model provider and id are required")
		}
		if model.Kind != "" && model.Kind != "llm" && model.Kind != "systemone" || model.ContextWindow < 0 {
			return fmt.Errorf("invalid model kind or context window")
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
		if _, err := auth.NormalizeAllow(key.Allow); err != nil {
			return fmt.Errorf("API key %q access: %w", key.ID, err)
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
			if _, known := models[modelKey(canonicalProvider(target.ProviderID), target.ModelID)]; !known {
				return fmt.Errorf("alias %q references unknown model %s/%s", alias, target.ProviderID, target.ModelID)
			}
		}
	}
	return nil
}

// canonicalProvider maps a registry alias prefix to the provider id it names.
func canonicalProvider(provider string) string {
	if canonical, ok := capabilities.ProviderAlias(provider); ok {
		return canonical
	}
	return provider
}

func cloneSettings(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	for key, value := range src {
		dst[key] = value
	}
	return dst
}
