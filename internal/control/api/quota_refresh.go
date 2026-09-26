package api

import (
	"context"
	"net/http"
	"time"
)

// handleQuotaRefresh triggers a background provider quota refresh. The refresh
// runs detached from the request so it can never block normal inference, and a
// bounded timeout keeps a stuck provider from leaking the goroutine (PRD-QUOTA-001).
func (h *Handler) handleQuotaRefresh(w http.ResponseWriter, r *http.Request) {
	if h.opts.QuotaRefresher == nil {
		writeError(w, http.StatusServiceUnavailable, "quota_unavailable", "provider quota refresh is not configured")
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()
		_ = h.opts.QuotaRefresher(ctx)
	}()
	if h.opts.Events != nil {
		h.opts.Events.Publish("quota.refresh", map[string]any{"requestedAt": time.Now().UTC()})
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
}
