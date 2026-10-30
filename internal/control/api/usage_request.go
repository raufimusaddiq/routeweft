package api

import (
	"database/sql"
	"encoding/json"
	"net/http"

	"github.com/raufimusaddiq/routeweft/internal/telemetry"
)

// handleUsageRequest returns everything stored for one request id: its usage
// events (a retried or fallback request can have more than one) and, when the
// diagnostic store holds one, the redacted request detail. A missing detail is
// reported as null, not as an error: details are bounded diagnostics (PRD-OBS-002)
// and most requests have none.
func (h *Handler) handleUsageRequest(w http.ResponseWriter, r *http.Request) {
	if h.opts.DB == nil {
		writeError(w, http.StatusServiceUnavailable, "read_models_unavailable", "database read models are not configured")
		return
	}
	requestID := r.PathValue("requestId")
	if requestID == "" || len(requestID) > maxFilterLength {
		writeError(w, http.StatusBadRequest, "invalid_request_id", "requestId must be 1 to 200 characters")
		return
	}
	rows, err := h.opts.DB.QueryContext(r.Context(), "SELECT "+usageColumns+" FROM usage_events WHERE request_id = ? ORDER BY created_at,id LIMIT 50", requestID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "read_failed", "could not load request")
		return
	}
	defer rows.Close()
	events := make([]map[string]any, 0, 1)
	for rows.Next() {
		event, err := scanUsageEvent(rows)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "read_failed", "could not load request")
			return
		}
		events = append(events, event)
	}
	if rows.Err() != nil {
		writeError(w, http.StatusInternalServerError, "read_failed", "could not load request")
		return
	}

	var mode sql.NullString
	var raw, created string
	var detail any
	err = h.opts.DB.QueryRowContext(r.Context(), "SELECT route_mode,COALESCE(detail,''),created_at FROM request_details WHERE request_id = ?", requestID).Scan(&mode, &raw, &created)
	switch {
	case err == sql.ErrNoRows:
	case err != nil:
		writeError(w, http.StatusInternalServerError, "read_failed", "could not load request")
		return
	default:
		var payload any
		if json.Unmarshal([]byte(raw), &payload) != nil {
			payload = map[string]string{"omitted": "invalid stored detail"}
		}
		item := map[string]any{"detail": telemetry.DetailRedactor{}.Redact(payload), "createdAt": created}
		putNull(item, "routeMode", mode)
		detail = item
	}
	if len(events) == 0 && detail == nil {
		writeError(w, http.StatusNotFound, "request_not_found", "no usage or detail is stored for this request id")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"requestId": requestID, "events": events, "detail": detail})
}

// handleUsageActive is the live view the provider topology and the activity strip
// read: what is in flight per account and model, the latest finished requests, and
// the provider that failed in the last few seconds (usageRepo.js getActiveRequests).
// It is process memory only and carries no request content.
func (h *Handler) handleUsageActive(w http.ResponseWriter, r *http.Request) {
	if h.opts.Live == nil {
		writeJSON(w, http.StatusOK, map[string]any{"activeRequests": []any{}, "recentRequests": []any{}, "errorProvider": ""})
		return
	}
	writeJSON(w, http.StatusOK, h.opts.Live())
}
