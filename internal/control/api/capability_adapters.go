package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func capacityAdapterSetting(name string) (string, bool) { return runtime.CapacityAdapterSetting(name) }

// adapterPoolEntry is the durable/API shape of one capacity-adapter member. An
// empty pool is a deliberate no-op, so members are optional but must reference a
// configured model when present.
type adapterPoolEntry struct {
	ProviderID string `json:"providerId"`
	ModelID    string `json:"modelId"`
}

// adapterConfig carries the pin's per-capability {enabled, roundRobin, models}
// (open-sse/services/capacityAdapter.js:18-30). RoundRobin is a pointer on the
// request side so a PUT that omits it keeps the stored policy instead of
// silently resetting it to fallback.
type adapterConfig struct {
	Enabled    bool               `json:"enabled"`
	RoundRobin *bool              `json:"roundRobin,omitempty"`
	Pool       []adapterPoolEntry `json:"pool"`
}

func parseAdapterConfig(raw string) adapterConfig {
	roundRobin := false
	config := adapterConfig{RoundRobin: &roundRobin, Pool: []adapterPoolEntry{}}
	if strings.TrimSpace(raw) != "" {
		var decoded adapterConfig
		if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
			config.Enabled = decoded.Enabled
			config.RoundRobin = decoded.RoundRobin
			if decoded.Pool != nil {
				config.Pool = decoded.Pool
			}
		}
	}
	return config
}

func (h *Handler) handleGetCapabilityAdapters(w http.ResponseWriter, r *http.Request) {
	if h.opts.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable", "settings are not configured")
		return
	}
	settings := h.opts.Settings.Settings()
	items := make([]map[string]any, 0, len(runtime.CapacityAdapters()))
	for _, adapter := range runtime.CapacityAdapters() {
		config := parseAdapterConfig(settings[adapter.Setting])
		items = append(items, adapterView(adapter.Name, config.Enabled, *config.RoundRobin, config.Pool))
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

func (h *Handler) handlePutCapabilityAdapter(w http.ResponseWriter, r *http.Request) {
	if h.opts.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable", "settings are not configured")
		return
	}
	name := strings.TrimSpace(r.PathValue("capability"))
	setting, ok := capacityAdapterSetting(name)
	if !ok {
		writeError(w, http.StatusNotFound, "unknown_capability", "unknown capability adapter")
		return
	}
	var body adapterConfig
	if !decodeJSON(w, r, 1<<20, &body) {
		return
	}
	settings := h.opts.Settings.Settings()
	snapshot, err := h.opts.Runtime.Load()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "runtime_unavailable", "runtime configuration is unavailable")
		return
	}
	pool := make([]adapterPoolEntry, 0, len(body.Pool))
	seen := make(map[string]struct{}, len(body.Pool))
	for _, member := range body.Pool {
		provider := strings.TrimSpace(member.ProviderID)
		model := strings.TrimSpace(member.ModelID)
		if provider == "" || model == "" {
			writeError(w, http.StatusBadRequest, "invalid_adapter", "pool members require providerId and modelId")
			return
		}
		key := provider + "\x00" + model
		if _, exists := seen[key]; exists {
			writeError(w, http.StatusBadRequest, "invalid_adapter", "duplicate pool member")
			return
		}
		if _, ok := snapshot.ResolveModel(provider, model); !ok {
			writeError(w, http.StatusBadRequest, "invalid_adapter", "pool member must reference a configured model")
			return
		}
		seen[key] = struct{}{}
		pool = append(pool, adapterPoolEntry{ProviderID: provider, ModelID: model})
	}
	roundRobin := *parseAdapterConfig(settings[setting]).RoundRobin
	if body.RoundRobin != nil {
		roundRobin = *body.RoundRobin
	}
	encoded, err := json.Marshal(adapterConfig{Enabled: body.Enabled, RoundRobin: &roundRobin, Pool: pool})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "encode_failed", "could not encode adapter configuration")
		return
	}
	revision, err := h.opts.Settings.SetSettings(r.Context(), map[string]string{setting: string(encoded)}, nil)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_adapter", err.Error())
		return
	}
	if h.opts.Events != nil {
		h.opts.Events.Publish("config.updated", map[string]any{"resource": "capability-adapter", "capability": name, "configRevision": revision})
	}
	view := adapterView(name, body.Enabled, roundRobin, pool)
	view["configRevision"] = revision
	writeJSON(w, http.StatusOK, view)
}

// adapterView is the public adapter shape; strategy is derived the way the pin
// derives it (capacityAdapter.js:57-60): round-robin only for an enabled pool
// with roundRobin set, otherwise fallback.
func adapterView(name string, enabled, roundRobin bool, pool []adapterPoolEntry) map[string]any {
	strategy := "fallback"
	if enabled && roundRobin {
		strategy = "round-robin"
	}
	return map[string]any{"capability": name, "enabled": enabled, "roundRobin": roundRobin, "strategy": strategy, "pool": pool}
}
