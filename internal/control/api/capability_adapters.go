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
	RoundRobin *bool              `json:"roundRobin"`
	Pool       []adapterPoolEntry `json:"pool"`
}

// adapterPut is the PUT body. Every field is optional and an omitted field keeps
// the stored value, so a partial body such as {"roundRobin":true} cannot disable
// the adapter or empty its pool.
type adapterPut struct {
	Enabled    *bool               `json:"enabled"`
	RoundRobin *bool               `json:"roundRobin"`
	Pool       *[]adapterPoolEntry `json:"pool"`
}

// storedMember reports whether a member is already in the stored pool, so the UI
// resending the whole pool (to add one entry) does not re-reject an imported
// member that is still unresolved.
func storedMember(pool []adapterPoolEntry, provider, model string) (adapterPoolEntry, bool) {
	for _, member := range pool {
		if member.ProviderID == provider && member.ModelID == model {
			return member, true
		}
	}
	return adapterPoolEntry{}, false
}

func parseAdapterConfig(raw string) adapterConfig {
	roundRobin := false
	config := adapterConfig{RoundRobin: &roundRobin, Pool: []adapterPoolEntry{}}
	if strings.TrimSpace(raw) != "" {
		var decoded adapterConfig
		if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
			config.Enabled = decoded.Enabled
			// Values stored before C04 have no roundRobin key; keep the default.
			if decoded.RoundRobin != nil {
				config.RoundRobin = decoded.RoundRobin
			}
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
	var body adapterPut
	if !decodeJSON(w, r, 1<<20, &body) {
		return
	}
	settings := h.opts.Settings.Settings()
	stored := parseAdapterConfig(settings[setting])
	enabled, roundRobin := stored.Enabled, *stored.RoundRobin
	var supplied []adapterPoolEntry
	if body.Enabled != nil {
		enabled = *body.Enabled
	}
	if body.RoundRobin != nil {
		roundRobin = *body.RoundRobin
	}
	if body.Pool != nil {
		supplied = *body.Pool
	}
	snapshot, err := h.opts.Runtime.Load()
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "runtime_unavailable", "runtime configuration is unavailable")
		return
	}
	// Only members the body supplied are checked against the catalog. A stored
	// pool (for example one imported with unresolved pin aliases) is kept as is,
	// so toggling roundRobin or enabled never fails on a member the operator did
	// not touch; Remove still works because it supplies the shorter pool.
	pool := append([]adapterPoolEntry(nil), stored.Pool...)
	if body.Pool != nil {
		pool = make([]adapterPoolEntry, 0, len(supplied))
		seen := make(map[string]struct{}, len(supplied))
		for _, member := range supplied {
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
			_, stays := storedMember(stored.Pool, provider, model)
			if _, ok := snapshot.ResolveModel(provider, model); !ok && !stays {
				writeError(w, http.StatusBadRequest, "invalid_adapter", "pool member must reference a configured model")
				return
			}
			seen[key] = struct{}{}
			pool = append(pool, adapterPoolEntry{ProviderID: provider, ModelID: model})
		}
	}
	encoded, err := json.Marshal(adapterConfig{Enabled: enabled, RoundRobin: &roundRobin, Pool: pool})
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
	view := adapterView(name, enabled, roundRobin, pool)
	view["configRevision"] = revision
	writeJSON(w, http.StatusOK, view)
}

// adapterView is the public adapter shape; strategy is derived the way the pin
// derives the active one (capacityAdapter.js:57-72): round-robin only for an
// enabled, non-empty pool with roundRobin set, otherwise fallback.
func adapterView(name string, enabled, roundRobin bool, pool []adapterPoolEntry) map[string]any {
	strategy := "fallback"
	if enabled && roundRobin && len(pool) > 0 {
		strategy = "round-robin"
	}
	return map[string]any{"capability": name, "enabled": enabled, "roundRobin": roundRobin, "strategy": strategy, "pool": pool}
}
