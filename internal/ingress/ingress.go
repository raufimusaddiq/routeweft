// Package ingress owns public inference HTTP endpoints.
package ingress

import (
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/auth"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// Snapshot provides the immutable request-serving configuration.
type Snapshot interface {
	Load() (*runtime.RuntimeSnapshot, error)
}

// Options configure public ingress behavior such as limits and CORS policy.
type Options struct {
	MaxBodyBytes int64
	CORSOrigins  []string
}

// DefaultMaxBodyBytes matches the 128 MB compatibility target in PRD-API-005.
const DefaultMaxBodyBytes int64 = 128 << 20

// Handler serves the read-only public ingress contract delivered by this PR.
type Handler struct {
	snapshot Snapshot
	opts     Options
}

// New builds the public ingress handler.
func New(snapshot Snapshot, opts Options) *Handler {
	if opts.MaxBodyBytes <= 0 {
		opts.MaxBodyBytes = DefaultMaxBodyBytes
	}
	return &Handler{snapshot: snapshot, opts: opts}
}

// Attach registers the public inference routes on the shared mux.
func (h *Handler) Attach(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1", h.withCORS(h.handleIndex))
	mux.HandleFunc("OPTIONS /v1", h.withCORS(h.handlePreflight))
	mux.HandleFunc("GET /v1/models", h.withCORS(h.handleModels))
	mux.HandleFunc("GET /v1/models/info", h.withCORS(h.handleModelsInfo))
	mux.HandleFunc("GET /v1/models/{provider}/{model...}", h.withCORS(h.handleModel))
	mux.HandleFunc("OPTIONS /v1/{rest...}", h.withCORS(h.handlePreflight))
}

func (h *Handler) handleIndex(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "routeweft.endpoint", "version": 1})
}

func (h *Handler) handleModels(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	snapshot, err := h.snapshot.Load()
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "snapshot_unavailable", err.Error())
		return
	}
	data := make([]map[string]any, 0)
	for _, model := range snapshot.Models() {
		data = append(data, modelObject(model))
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "list", "data": data})
}

func (h *Handler) handleModelsInfo(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	snapshot, err := h.snapshot.Load()
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "snapshot_unavailable", err.Error())
		return
	}
	models := snapshot.Models()
	providers := map[string]int{}
	for _, model := range models {
		providers[model.ProviderID]++
	}
	data := make([]map[string]any, 0, len(models))
	for _, model := range models {
		data = append(data, modelObject(model))
	}
	writeJSON(w, http.StatusOK, map[string]any{"object": "routeweft.models.info", "count": len(models), "providers": providers, "data": data})
}

func (h *Handler) handleModel(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	provider := r.PathValue("provider")
	model := r.PathValue("model")
	snapshot, err := h.snapshot.Load()
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "snapshot_unavailable", err.Error())
		return
	}
	resolved, ok := snapshot.ResolveModel(provider, model)
	if !ok {
		h.writeError(w, http.StatusNotFound, "model_not_found", fmt.Sprintf("model %s/%s is not available", provider, model))
		return
	}
	writeJSON(w, http.StatusOK, modelObject(resolved))
}

func (h *Handler) handlePreflight(w http.ResponseWriter, r *http.Request) {
	if origin := h.allowedOrigin(r); origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
	}
	w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
	w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, x-api-key, x-goog-api-key")
	w.Header().Set("Access-Control-Max-Age", "600")
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) authorize(w http.ResponseWriter, r *http.Request) bool {
	snapshot, err := h.snapshot.Load()
	if err != nil {
		h.writeError(w, http.StatusServiceUnavailable, "snapshot_unavailable", err.Error())
		return false
	}
	presented, ok := clientKey(r)
	if snapshot.Settings()["requireApiKey"] == "false" {
		return true
	}
	if !ok {
		h.writeError(w, http.StatusUnauthorized, "missing_api_key", "a Routeweft API key is required")
		return false
	}
	if _, ok := snapshot.APIKeys().Lookup(presented); !ok {
		h.writeError(w, http.StatusUnauthorized, "invalid_api_key", "the Routeweft API key is invalid")
		return false
	}
	return true
}

func clientKey(r *http.Request) (string, bool) {
	var keys []string
	if key, ok := auth.FromAuthorization(r.Header.Get("Authorization")); ok {
		keys = append(keys, key)
	}
	for _, value := range r.Header.Values("X-Api-Key") {
		if key := strings.TrimSpace(value); key != "" {
			keys = append(keys, key)
		}
	}
	if key := strings.TrimSpace(r.Header.Get("X-Goog-Api-Key")); key != "" {
		keys = append(keys, key)
	}
	for _, key := range r.URL.Query()["key"] {
		if key = strings.TrimSpace(key); key != "" {
			keys = append(keys, key)
		}
	}
	if len(keys) == 0 {
		return "", false
	}
	for _, key := range keys[1:] {
		if subtle.ConstantTimeCompare([]byte(keys[0]), []byte(key)) != 1 {
			return "", false
		}
	}
	return keys[0], true
}

func (h *Handler) withCORS(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if origin := h.allowedOrigin(r); origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			if origin != "*" {
				w.Header().Add("Vary", "Origin")
			}
		}
		next(w, r)
	}
}

func (h *Handler) allowedOrigin(r *http.Request) string {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return ""
	}
	for _, allowed := range h.opts.CORSOrigins {
		if allowed == "*" {
			return "*"
		}
		if strings.EqualFold(allowed, origin) {
			return origin
		}
	}
	return ""
}

func (h *Handler) writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]any{"type": code, "code": code, "message": message}})
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func modelObject(model runtime.Model) map[string]any {
	object := map[string]any{
		"id":       model.ID,
		"object":   "model",
		"created":  0,
		"owned_by": model.ProviderID,
		"provider": model.ProviderID,
	}
	if model.Name != "" {
		object["name"] = model.Name
	}
	if model.ContextWindow > 0 {
		object["context_window"] = model.ContextWindow
	}
	if len(model.Capabilities) > 0 {
		object["capabilities"] = model.Capabilities
	}
	if model.Source != "" {
		object["source"] = model.Source
	}
	return object
}
