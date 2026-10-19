package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	controlevents "github.com/raufimusaddiq/routeweft/internal/control/events"
)

const (
	// usageHeartbeat matches LiteRouter's 25 s ping: under typical proxy idle
	// timeouts, and the cadence at which a dead client is detected.
	usageHeartbeat = 25 * time.Second
	// usageWriteTimeout bounds one frame write. A client that stops reading
	// blocks only its own handler goroutine, then loses the stream.
	usageWriteTimeout = 5 * time.Second
	usageRetryMS      = 5000
)

// liveFilter is the usage filter parsed once per connection, so each event is
// matched without re-parsing the query.
type liveFilter struct {
	requestID, providerID, modelID, connectionID, apiKeyID string
	status                                                 int
	start, end                                             time.Time
}

// parseLiveFilter accepts exactly the query the usage log accepts, validated by
// the same function, so the stream cannot be scoped more loosely than the log.
func parseLiveFilter(query url.Values) (liveFilter, error) {
	if _, _, err := usageFilter(query); err != nil {
		return liveFilter{}, err
	}
	f := liveFilter{
		requestID:    strings.TrimSpace(query.Get("requestId")),
		providerID:   strings.TrimSpace(query.Get("providerId")),
		modelID:      strings.TrimSpace(query.Get("modelId")),
		connectionID: strings.TrimSpace(query.Get("connectionId")),
		apiKeyID:     strings.TrimSpace(query.Get("apiKeyId")),
	}
	if raw := strings.TrimSpace(query.Get("status")); raw != "" {
		f.status, _ = strconv.Atoi(raw)
	}
	f.start, _ = time.Parse(time.RFC3339, strings.TrimSpace(query.Get("startDate")))
	f.end, _ = time.Parse(time.RFC3339, strings.TrimSpace(query.Get("endDate")))
	return f, nil
}

func (f liveFilter) match(e controlevents.UsageEvent) bool {
	switch {
	case f.requestID != "" && e.RequestID != f.requestID,
		f.providerID != "" && e.ProviderID != f.providerID,
		f.modelID != "" && e.ModelID != f.modelID,
		f.connectionID != "" && e.ConnectionID != f.connectionID,
		f.apiKeyID != "" && e.APIKeyID != f.apiKeyID,
		f.status != 0 && e.Status != f.status,
		!f.start.IsZero() && e.CreatedAt.Before(f.start),
		!f.end.IsZero() && e.CreatedAt.After(f.end):
		return false
	}
	return true
}

func (h *Handler) handleUsageStream(w http.ResponseWriter, r *http.Request) {
	h.streamUsage(w, r, usageHeartbeat, usageWriteTimeout)
}

func (h *Handler) streamUsage(w http.ResponseWriter, r *http.Request, heartbeat, writeTimeout time.Duration) {
	if h.opts.UsageFeed == nil {
		writeError(w, http.StatusServiceUnavailable, "events_unavailable", "live usage is not configured")
		return
	}
	filter, err := parseLiveFilter(r.URL.Query())
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid_filter", err.Error())
		return
	}
	if _, ok := w.(http.Flusher); !ok {
		writeError(w, http.StatusInternalServerError, "stream_unsupported", "streaming is unavailable")
		return
	}
	sub, err := h.opts.UsageFeed.Subscribe()
	if errors.Is(err, controlevents.ErrTooManySubscribers) {
		slog.Warn("live usage subscriber rejected: limit reached", "limit", controlevents.MaxUsageSubscribers)
		w.Header().Set("Retry-After", "5")
		writeError(w, http.StatusServiceUnavailable, "stream_limit", "too many live usage streams")
		return
	}
	slog.Info("live usage subscriber connected", "subscribers", h.opts.UsageFeed.Subscribers())
	defer func() {
		sub.Close()
		slog.Info("live usage subscriber disconnected", "subscribers", h.opts.UsageFeed.Subscribers(), "lagged", sub.Lagged())
	}()

	rc := http.NewResponseController(w)
	defer func() { _ = rc.SetWriteDeadline(time.Time{}) }()
	write := func(frame string) bool {
		if rc.SetWriteDeadline(time.Now().Add(writeTimeout)) != nil {
			return false
		}
		if _, err := fmt.Fprint(w, frame); err != nil {
			return false
		}
		return rc.Flush() == nil
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	if !write("retry: " + strconv.Itoa(usageRetryMS) + "\n: connected\n\n") {
		return
	}
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-r.Context().Done():
			return
		case event, open := <-sub.C():
			if !open {
				return
			}
			// A revoked or expired session ends the stream rather than keep
			// receiving events it could no longer fetch.
			if _, ok := h.sessionFor(r); !ok {
				return
			}
			if !filter.match(event) {
				continue
			}
			payload, err := json.Marshal(event)
			if err != nil {
				continue
			}
			if !write(fmt.Sprintf("id: %d\nevent: request.completed\ndata: %s\n\n", event.ID, payload)) {
				return
			}
		case <-ticker.C:
			if _, ok := h.sessionFor(r); !ok {
				return
			}
			if !write(": heartbeat\n\n") {
				return
			}
		}
	}
}
