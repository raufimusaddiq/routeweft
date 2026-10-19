package api

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/transforms"
	"github.com/raufimusaddiq/routeweft/internal/transforms/headroom"
	"github.com/raufimusaddiq/routeweft/internal/transforms/pxpipe"
)

// Routeweft reaches Headroom and PXPIPE only as configured HTTP services
// (SPEC §17). These read-only handlers report their health and, for PXPIPE, the
// diagnostics the saver pipeline recorded. Installing, starting, stopping or
// restarting a helper is a privileged host operation and is deliberately not
// exposed over this API.

const (
	helperProbeCap     = 30 * time.Second
	pxpipeDefaultLimit = 50
	pxpipeMaxLimit     = 500
)

// helperHealth is a classified probe outcome; it never carries raw error text.
type helperHealth struct {
	Healthy    bool   `json:"healthy"`
	Class      string `json:"class,omitempty"`
	Message    string `json:"message,omitempty"`
	StatusCode int    `json:"statusCode,omitempty"`
	LatencyMs  int64  `json:"latencyMs"`
}

var helperFailureMessages = map[string]string{
	"not_configured": "no helper URL is configured",
	"blocked":        "helper URL is not allowed by the outbound network policy",
	"bad_status":     "helper answered its health endpoint with an error status",
	"dns":            "helper host name could not be resolved",
	"refused":        "helper refused the connection",
	"timeout":        "helper did not respond in time",
	"tls":            "TLS handshake with the helper failed",
	"unreachable":    "helper could not be reached",
}

func failedHelper(class string, started time.Time) helperHealth {
	return helperHealth{Class: class, Message: helperFailureMessages[class], LatencyMs: time.Since(started).Milliseconds()}
}

// probeHelper checks one helper's health endpoint. The URL passes the shared
// outbound policy before any connection, and the probe uses the same guarded
// client as discovery and provider tests.
func (h *Handler) probeHelper(ctx context.Context, rawURL string, health func(context.Context) error) helperHealth {
	started := time.Now()
	if strings.TrimSpace(rawURL) == "" {
		return failedHelper("not_configured", started)
	}
	if _, err := h.validateOutboundURL(strings.TrimSpace(rawURL)); err != nil {
		return failedHelper("blocked", started)
	}
	if err := health(ctx); err != nil {
		var status *transforms.HealthStatusError
		if errors.As(err, &status) {
			result := failedHelper("bad_status", started)
			result.StatusCode = status.StatusCode
			return result
		}
		class := classifyProxyError(err)
		if _, known := helperFailureMessages[class]; !known {
			class = "unreachable"
		}
		return failedHelper(class, started)
	}
	return helperHealth{Healthy: true, LatencyMs: time.Since(started).Milliseconds()}
}

func helperTimeout(raw string) time.Duration {
	n, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || n <= 0 {
		return 0
	}
	timeout := time.Duration(n) * time.Millisecond
	if timeout > helperProbeCap {
		return helperProbeCap
	}
	return timeout
}

func (h *Handler) saverSettings(w http.ResponseWriter) (map[string]string, bool) {
	if h.opts.Settings == nil {
		writeError(w, http.StatusServiceUnavailable, "settings_unavailable", "settings are not configured")
		return nil, false
	}
	return h.opts.Settings.Settings(), true
}

func (h *Handler) handleHeadroomStatus(w http.ResponseWriter, r *http.Request) {
	settings, ok := h.saverSettings(w)
	if !ok {
		return
	}
	probe := headroom.Transform{URL: strings.TrimSpace(settings["headroomUrl"]), Timeout: helperTimeout(settings["headroomTimeoutMs"]), Client: h.saverClient, AllowPrivate: h.opts.AllowPrivateUpstreams}
	writeJSON(w, http.StatusOK, map[string]any{
		"enabled":              settings["headroomEnabled"] == "true",
		"url":                  safeURL(probe.URL),
		"timeoutMs":            settings["headroomTimeoutMs"],
		"compressUserMessages": settings["headroomCompressUserMessages"] == "true",
		"health":               h.probeHelper(r.Context(), probe.URL, probe.Health),
	})
}

func (h *Handler) pxpipeProbe(settings map[string]string) pxpipe.Transform {
	return pxpipe.Transform{URL: strings.TrimSpace(settings["pxpipeUrl"]), Timeout: helperTimeout(settings["pxpipeTimeoutMs"]), Client: h.saverClient, AllowPrivate: h.opts.AllowPrivateUpstreams}
}

func (h *Handler) handlePXPIPEHealth(w http.ResponseWriter, r *http.Request) {
	settings, ok := h.saverSettings(w)
	if !ok {
		return
	}
	probe := h.pxpipeProbe(settings)
	writeJSON(w, http.StatusOK, h.probeHelper(r.Context(), probe.URL, probe.Health))
}

func (h *Handler) handlePXPIPEStatus(w http.ResponseWriter, r *http.Request) {
	settings, ok := h.saverSettings(w)
	if !ok {
		return
	}
	probe := h.pxpipeProbe(settings)
	response := map[string]any{
		"enabled":     settings["pxpipeEnabled"] == "true",
		"url":         safeURL(probe.URL),
		"autoInstall": settings["pxpipeAutoInstall"] == "true",
		"minChars":    settings["pxpipeMinChars"],
		"timeoutMs":   settings["pxpipeTimeoutMs"],
		"health":      h.probeHelper(r.Context(), probe.URL, probe.Health),
	}
	if h.opts.PXPIPE != nil {
		response["stats"] = h.opts.PXPIPE.Stats()
	}
	writeJSON(w, http.StatusOK, response)
}

type pxpipeLogEntry struct {
	Step        string `json:"step"`
	Status      string `json:"status"`
	Skipped     bool   `json:"skipped"`
	Attempted   bool   `json:"attempted"`
	Succeeded   bool   `json:"succeeded"`
	InputChars  int    `json:"inputChars"`
	OutputChars int    `json:"outputChars"`
	DurationMs  int64  `json:"durationMs"`
	Error       string `json:"error,omitempty"`
}

// recentPXPIPE returns up to limit of the newest diagnostics, oldest first.
func (h *Handler) recentPXPIPE(w http.ResponseWriter, r *http.Request) ([]pxpipeLogEntry, bool) {
	if h.opts.PXPIPE == nil {
		writeError(w, http.StatusServiceUnavailable, "pxpipe_unavailable", "PXPIPE diagnostics are not configured")
		return nil, false
	}
	limit := pxpipeDefaultLimit
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > pxpipeMaxLimit {
			writeError(w, http.StatusBadRequest, "invalid_limit", "limit must be between 1 and 500")
			return nil, false
		}
		limit = value
	}
	diagnostics := h.opts.PXPIPE.Logs()
	if len(diagnostics) > limit {
		diagnostics = diagnostics[len(diagnostics)-limit:]
	}
	entries := make([]pxpipeLogEntry, 0, len(diagnostics))
	for _, d := range diagnostics {
		message := d.Error
		if len(message) > 300 {
			message = message[:300]
		}
		entries = append(entries, pxpipeLogEntry{Step: d.Step, Status: d.Status, Skipped: d.Skipped, Attempted: d.Attempted, Succeeded: d.Succeeded, InputChars: d.InputChars, OutputChars: d.OutputChars, DurationMs: d.Duration.Milliseconds(), Error: message})
	}
	return entries, true
}

func (h *Handler) handlePXPIPELogs(w http.ResponseWriter, r *http.Request) {
	if entries, ok := h.recentPXPIPE(w, r); ok {
		writeJSON(w, http.StatusOK, map[string]any{"logs": entries})
	}
}

func (h *Handler) handlePXPIPEStats(w http.ResponseWriter, r *http.Request) {
	if entries, ok := h.recentPXPIPE(w, r); ok {
		writeJSON(w, http.StatusOK, map[string]any{"stats": h.opts.PXPIPE.Stats(), "recent": entries})
	}
}
