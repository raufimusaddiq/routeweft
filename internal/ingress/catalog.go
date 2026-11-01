package ingress

import (
	"fmt"
	"net/http"
	"slices"
	"sort"

	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// unknownModelMessage explains a model id that is not in the catalogue. It
// states the size of the real list and where to read it, and names no model:
// a suggestion the catalogue does not contain is what sent callers to
// "kenari-free", which never existed (#360). It never echoes the requested id
// either: an ungranted and a nonexistent id must answer identically (C02).
func unknownModelMessage(snapshot *runtime.RuntimeSnapshot, allowed func(string) bool, kind string) string {
	if kind == "" {
		kind = "llm"
	}
	listed := publicModels(snapshot, kind, allowed)
	return fmt.Sprintf("model not found. GET /v1/models lists the %d callable ids; pass one exactly as shown, including its \"<provider>/\" prefix. A provider with no discovered models has none there yet: run Discover for it on the Providers page.", len(listed))
}

func matchesModelKind(model runtime.Model, kind, requested string) bool {
	switch requested {
	case "":
		return true
	case "image-to-text", "imageToText":
		return kind == "llm" && slices.Contains(model.Capabilities, "vision")
	case "web":
		return kind == "llm" && (slices.Contains(model.Capabilities, "webSearch") || slices.Contains(model.Capabilities, "webFetch"))
	case "webSearch", "webFetch":
		return kind == "llm" && slices.Contains(model.Capabilities, requested)
	default:
		return kind == requested
	}
}

func publicModels(snapshot *runtime.RuntimeSnapshot, requestedKind string, allowed func(string) bool) []map[string]any {
	data := make([]map[string]any, 0)
	for _, model := range snapshot.Models() {
		kind := snapshot.ModelKind(model)
		if !matchesModelKind(model, kind, requestedKind) {
			continue
		}
		entry := modelObject(model)
		if model.Source != "alias" {
			entry["id"] = model.ProviderID + "/" + model.ID
		}
		if !allowed(entry["id"].(string)) {
			continue
		}
		entry["kind"] = kind
		if model.Name == "" {
			entry["name"] = model.ID
		}
		if model.ContextWindow > 0 {
			entry["contextWindow"] = model.ContextWindow
			entry["context_length"] = model.ContextWindow
		}
		data = append(data, entry)
	}
	if requestedKind == "" || requestedKind == "llm" {
		for _, combo := range snapshot.Combos() {
			if !allowed(combo.Name) {
				continue
			}
			data = append(data, map[string]any{"id": combo.Name, "name": combo.Name, "object": "model", "owned_by": "combo", "kind": "llm"})
		}
	}
	sort.SliceStable(data, func(i, j int) bool { return data[i]["id"].(string) < data[j]["id"].(string) })
	return data
}

func (h *Handler) handleModelKind(w http.ResponseWriter, r *http.Request) {
	switch r.PathValue("kind") {
	case "llm", "systemone", "image-to-text", "web":
		query := r.URL.Query()
		query.Set("kind", r.PathValue("kind"))
		r.URL.RawQuery = query.Encode()
		h.handleModels(w, r)
	default:
		if h.authorize(w, r) {
			h.writeError(w, http.StatusNotFound, "model_not_found", "unsupported model kind")
		}
	}
}

func (h *Handler) handleOllamaTags(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	snapshot, err := h.snapshot.Load()
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "snapshot_unavailable", err.Error())
		return
	}
	models := make([]map[string]any, 0)
	for _, model := range publicModels(snapshot, "llm", accessFilter(r, snapshot)) {
		models = append(models, map[string]any{"name": model["id"], "model": model["id"], "modified_at": "1970-01-01T00:00:00Z", "size": 0, "digest": "", "details": map[string]any{}})
	}
	writeJSON(w, http.StatusOK, map[string]any{"models": models})
}
