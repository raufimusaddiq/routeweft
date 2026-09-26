package api

import (
	"net/http"
	"net/url"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func (h *Handler) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	if h.opts.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable", "settings are not configured")
		return
	}
	settings := safeSettings(h.opts.Settings.Settings())
	writable := runtime.WritableSettingNames()
	writeJSON(w, http.StatusOK, map[string]any{"settings": settings, "writable": writable})
}

func (h *Handler) handlePatchSettings(w http.ResponseWriter, r *http.Request) {
	if h.opts.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable", "settings are not configured")
		return
	}
	var body struct {
		Set    map[string]string `json:"set"`
		Remove []string          `json:"remove"`
	}
	if !decodeJSON(w, r, 1<<20, &body) {
		return
	}
	if len(body.Set) == 0 && len(body.Remove) == 0 {
		writeError(w, http.StatusBadRequest, "empty_patch", "at least one setting must be set or removed")
		return
	}
	if proxyURL, ok := body.Set["outboundProxyUrl"]; ok && proxyURL != "" {
		if _, err := h.validateOutboundURL(proxyURL); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_setting", "outbound proxy URL is not allowed")
			return
		}
	}
	revision, err := h.opts.Settings.SetSettings(r.Context(), body.Set, body.Remove)
	if err != nil {
		// An allowlist rejection is a client error; anything else is internal.
		writeError(w, http.StatusBadRequest, "invalid_setting", err.Error())
		return
	}
	if h.opts.Events != nil {
		h.opts.Events.Publish("config.updated", map[string]any{"resource": "settings", "configRevision": revision})
	}
	writeJSON(w, http.StatusOK, map[string]any{"configRevision": revision, "settings": safeSettings(h.opts.Settings.Settings())})
}

// safeSettings prevents generic settings responses from exposing embedded URL
// credentials or secret-shaped setting values. URL query strings are omitted;
// operators can still inspect the configured scheme/host/path.
func safeSettings(values map[string]string) map[string]string {
	safe := make(map[string]string, len(values))
	for key, value := range values {
		normalized := strings.ToLower(strings.NewReplacer("_", "", "-", "", ".", "").Replace(key))
		if strings.Contains(normalized, "url") {
			parsed, err := url.Parse(value)
			if err == nil && parsed.Scheme != "" && parsed.Host != "" {
				parsed.User = nil
				parsed.RawQuery = ""
				parsed.Fragment = ""
				safe[key] = parsed.String()
			} else {
				safe[key] = "[redacted]"
			}
			continue
		}
		if settingContainsCredentialName(normalized) {
			safe[key] = "[redacted]"
			continue
		}
		safe[key] = value
	}
	return safe
}

func settingContainsCredentialName(normalized string) bool {
	for _, marker := range []string{"authorization", "apikey", "secret", "token", "password", "credential", "cookie", "privatekey"} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}
