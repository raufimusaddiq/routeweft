package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func (h *Handler) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	if h.opts.Keys == nil {
		writeError(w, http.StatusServiceUnavailable, "keys_unavailable", "API key management is not configured")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decodeJSON(w, r, 8<<10, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "invalid_key", "API key name is required")
		return
	}
	entry, secret, err := h.opts.Keys.CreateAPIKey(r.Context(), name)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "key_create_failed", "API key could not be created")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"key":    map[string]any{"id": entry.ID, "name": entry.Name, "prefix": entry.Prefix, "enabled": !entry.Disabled, "paused": entry.Paused},
		"secret": secret, "configRevision": h.activeConfigRevision(),
	})
}

func (h *Handler) handlePatchKey(w http.ResponseWriter, r *http.Request) {
	if h.opts.Keys == nil {
		writeError(w, http.StatusServiceUnavailable, "keys_unavailable", "API key management is not configured")
		return
	}
	var body struct {
		Paused *bool `json:"paused"`
	}
	if !decodeJSON(w, r, 8<<10, &body) {
		return
	}
	if body.Paused == nil {
		writeError(w, http.StatusBadRequest, "invalid_key", "paused is required")
		return
	}
	if err := h.opts.Keys.SetAPIKeyPaused(r.Context(), r.PathValue("id"), *body.Paused); err != nil {
		if errors.Is(err, runtime.ErrAPIKeyNotFound) {
			writeError(w, http.StatusNotFound, "key_not_found", "API key was not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "key_update_failed", "API key state could not be updated")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": r.PathValue("id"), "paused": *body.Paused, "configRevision": h.activeConfigRevision()})
}

func (h *Handler) handleDeleteKey(w http.ResponseWriter, r *http.Request) {
	if h.opts.Keys == nil {
		writeError(w, http.StatusServiceUnavailable, "keys_unavailable", "API key management is not configured")
		return
	}
	if err := h.opts.Keys.DeleteAPIKey(r.Context(), r.PathValue("id")); err != nil {
		if errors.Is(err, runtime.ErrAPIKeyNotFound) {
			writeError(w, http.StatusNotFound, "key_not_found", "API key was not found")
			return
		}
		writeError(w, http.StatusInternalServerError, "key_delete_failed", "API key could not be revoked")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"id": r.PathValue("id"), "revoked": true, "configRevision": h.activeConfigRevision()})
}

func (h *Handler) activeConfigRevision() uint64 {
	if h.opts.Runtime == nil {
		return 0
	}
	snapshot, err := h.opts.Runtime.Load()
	if err != nil {
		return 0
	}
	return snapshot.ConfigRevision()
}
