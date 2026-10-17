package api

import (
	"errors"
	"net/http"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/auth"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func (h *Handler) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	if h.opts.Keys == nil {
		writeError(w, http.StatusServiceUnavailable, "keys_unavailable", "API key management is not configured")
		return
	}
	var body struct {
		Name   string     `json:"name"`
		Access *keyAccess `json:"access"`
	}
	if !decodeJSON(w, r, 64<<10, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeError(w, http.StatusBadRequest, "invalid_key", "API key name is required")
		return
	}
	restricted, allow := false, []string(nil)
	if body.Access != nil {
		var ok bool
		if allow, ok = h.checkAccess(w, body.Access); !ok {
			return
		}
		restricted = body.Access.Restricted
	}
	entry, secret, err := h.opts.Keys.CreateAPIKeyWithAccess(r.Context(), name, restricted, allow)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "key_create_failed", "API key could not be created")
		return
	}
	writeJSON(w, http.StatusCreated, map[string]any{
		"key":    map[string]any{"id": entry.ID, "name": entry.Name, "prefix": entry.Prefix, "enabled": !entry.Disabled, "paused": entry.Paused, "access": accessJSON(entry.Restricted, entry.Allow)},
		"secret": secret, "configRevision": h.activeConfigRevision(),
	})
}

func (h *Handler) handlePatchKey(w http.ResponseWriter, r *http.Request) {
	if h.opts.Keys == nil {
		writeError(w, http.StatusServiceUnavailable, "keys_unavailable", "API key management is not configured")
		return
	}
	var body struct {
		Paused *bool      `json:"paused"`
		Access *keyAccess `json:"access"`
	}
	if !decodeJSON(w, r, 64<<10, &body) {
		return
	}
	if body.Paused == nil && body.Access == nil {
		writeError(w, http.StatusBadRequest, "invalid_key", "paused or access is required")
		return
	}
	id := r.PathValue("id")
	result := map[string]any{"id": id}
	if body.Access != nil {
		allow, ok := h.checkAccess(w, body.Access)
		if !ok {
			return
		}
		if err := h.opts.Keys.SetAPIKeyAccess(r.Context(), id, body.Access.Restricted, allow); err != nil {
			h.keyMutationError(w, err, "API key access could not be updated")
			return
		}
		result["access"] = accessJSON(body.Access.Restricted, allow)
	}
	if body.Paused != nil {
		if err := h.opts.Keys.SetAPIKeyPaused(r.Context(), id, *body.Paused); err != nil {
			h.keyMutationError(w, err, "API key state could not be updated")
			return
		}
		result["paused"] = *body.Paused
	}
	result["configRevision"] = h.activeConfigRevision()
	writeJSON(w, http.StatusOK, result)
}

// keyAccess is the admin-API form of a key's model/Combo allowlist.
type keyAccess struct {
	Restricted bool     `json:"restricted"`
	Allow      []string `json:"allow"`
}

func accessJSON(restricted bool, allow []string) map[string]any {
	if allow == nil {
		allow = []string{}
	}
	return map[string]any{"restricted": restricted, "allow": allow}
}

// checkAccess normalizes an allowlist; an unrestricted key keeps no list so a
// stale list cannot silently re-apply when restriction is switched back on.
func (h *Handler) checkAccess(w http.ResponseWriter, access *keyAccess) ([]string, bool) {
	if !access.Restricted {
		access.Allow = nil
		return nil, true
	}
	allow, err := auth.NormalizeAllow(access.Allow)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_access", err.Error())
		return nil, false
	}
	return allow, true
}

func (h *Handler) keyMutationError(w http.ResponseWriter, err error, message string) {
	if errors.Is(err, runtime.ErrAPIKeyNotFound) {
		writeError(w, http.StatusNotFound, "key_not_found", "API key was not found")
		return
	}
	writeError(w, http.StatusInternalServerError, "key_update_failed", message)
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
