package api

import (
	"encoding/json"
	"net/http"
	"strings"
)

// capacityAdapters maps a public capability name to its durable settings key.
// vision and audio-input are the initially visible adapters; pdf and
// video-input exist for compatibility/future use without standalone media APIs
// (PRD-COMBO-004).
var capacityAdapters = []struct{ Name, Setting string }{
	{"vision", "capacityAdapterVision"},
	{"audio-input", "capacityAdapterAudioInput"},
	{"pdf", "capacityAdapterPDF"},
	{"video-input", "capacityAdapterVideoInput"},
}

func capacityAdapterSetting(name string) (string, bool) {
	for _, adapter := range capacityAdapters {
		if adapter.Name == name {
			return adapter.Setting, true
		}
	}
	return "", false
}

// adapterPoolEntry is the durable/API shape of one capacity-adapter member. An
// empty pool is a deliberate no-op, so members are optional but must reference a
// configured model when present.
type adapterPoolEntry struct {
	ProviderID string `json:"providerId"`
	ModelID    string `json:"modelId"`
}

type adapterConfig struct {
	Enabled bool               `json:"enabled"`
	Pool    []adapterPoolEntry `json:"pool"`
}

func parseAdapterConfig(raw string) adapterConfig {
	config := adapterConfig{Pool: []adapterPoolEntry{}}
	if strings.TrimSpace(raw) != "" {
		var decoded adapterConfig
		if err := json.Unmarshal([]byte(raw), &decoded); err == nil {
			config.Enabled = decoded.Enabled
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
	items := make([]map[string]any, 0, len(capacityAdapters))
	for _, adapter := range capacityAdapters {
		config := parseAdapterConfig(settings[adapter.Setting])
		items = append(items, map[string]any{"capability": adapter.Name, "enabled": config.Enabled, "pool": config.Pool})
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
	encoded, err := json.Marshal(adapterConfig{Enabled: body.Enabled, Pool: pool})
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
	writeJSON(w, http.StatusOK, map[string]any{"configRevision": revision, "capability": name, "enabled": body.Enabled, "pool": pool})
}
