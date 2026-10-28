package api

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Quota refresh job states (#226). A 202 means the refresh was accepted, never
// that anything was observed: observations come from GET /admin/v1/quota once
// the job is terminal.
const (
	quotaRefreshQueued    = "queued"
	quotaRefreshRunning   = "running"
	quotaRefreshSucceeded = "succeeded"
	quotaRefreshFailed    = "failed"

	defaultQuotaRefreshTimeout = 60 * time.Second
)

// quotaRefreshJob is one operator-requested refresh. Error is a fixed phrase:
// a reader's error text can carry upstream detail and is never echoed.
type quotaRefreshJob struct {
	ID          string     `json:"id"`
	Status      string     `json:"status"`
	RequestedAt time.Time  `json:"requestedAt"`
	StartedAt   *time.Time `json:"startedAt,omitempty"`
	FinishedAt  *time.Time `json:"finishedAt,omitempty"`
	Error       string     `json:"error,omitempty"`
}

func (j quotaRefreshJob) terminal() bool {
	return j.Status == quotaRefreshSucceeded || j.Status == quotaRefreshFailed
}

// quotaRefreshJobs holds the latest job. At most one refresh runs at a time: a
// request while the job is queued or running joins it, and a request after a job
// ended at its deadline while its refresher has still not returned is refused,
// so a refresher that ignores cancellation never runs alongside another.
type quotaRefreshJobs struct {
	mu      sync.Mutex
	latest  *quotaRefreshJob
	timeout time.Duration
	// active is true from the start of a refresher call until it returns, which
	// can be after its job has already failed at the deadline.
	active bool
}

func (q *quotaRefreshJobs) snapshot() (quotaRefreshJob, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.latest == nil {
		return quotaRefreshJob{}, false
	}
	return *q.latest, true
}

// update changes the job while it is not terminal; a terminal job is final.
func (q *quotaRefreshJobs) update(id string, change func(*quotaRefreshJob)) (quotaRefreshJob, bool) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.latest == nil || q.latest.ID != id || q.latest.terminal() {
		return quotaRefreshJob{}, false
	}
	change(q.latest)
	return *q.latest, true
}

func (q *quotaRefreshJobs) settle() {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.active = false
}

// handleQuotaRefresh accepts a background provider quota refresh. The refresh
// runs detached from the request so it can never block normal inference, and
// its deadline ends the job whether or not the refresher returns (PRD-QUOTA-001).
func (h *Handler) handleQuotaRefresh(w http.ResponseWriter, _ *http.Request) {
	if h.opts.QuotaRefresher == nil {
		writeError(w, http.StatusServiceUnavailable, "quota_unavailable", "provider quota refresh is not configured")
		return
	}
	jobs := &h.quotaJobs
	jobs.mu.Lock()
	if jobs.latest != nil && !jobs.latest.terminal() {
		job := *jobs.latest
		jobs.mu.Unlock()
		writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "job": job, "joined": true})
		return
	}
	if jobs.active {
		jobs.mu.Unlock()
		writeError(w, http.StatusConflict, "refresh_busy", "the previous quota refresh passed its deadline and has not stopped yet; try again later")
		return
	}
	job := &quotaRefreshJob{ID: uuid.NewString(), Status: quotaRefreshQueued, RequestedAt: time.Now().UTC()}
	jobs.latest, jobs.active = job, true
	accepted := *job
	timeout := jobs.timeout
	jobs.mu.Unlock()
	if timeout <= 0 {
		timeout = defaultQuotaRefreshTimeout
	}
	h.publishQuotaRefresh(accepted)
	go h.runQuotaRefresh(accepted.ID, timeout)
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true, "job": accepted, "joined": false})
}

// handleQuotaRefreshStatus reports the latest refresh job, or idle when none
// was requested since the process started.
func (h *Handler) handleQuotaRefreshStatus(w http.ResponseWriter, _ *http.Request) {
	job, ok := h.quotaJobs.snapshot()
	if !ok {
		writeJSON(w, http.StatusOK, map[string]any{"status": "idle"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"status": job.Status, "job": job})
}

func (h *Handler) runQuotaRefresh(id string, timeout time.Duration) {
	started := time.Now().UTC()
	if job, ok := h.quotaJobs.update(id, func(job *quotaRefreshJob) {
		job.Status, job.StartedAt = quotaRefreshRunning, &started
	}); ok {
		h.publishQuotaRefresh(job)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		// The refresher is held until it returns, however late: no other
		// refresh starts before then.
		defer h.quotaJobs.settle()
		done <- h.opts.QuotaRefresher(ctx)
	}()
	var err error
	select {
	case err = <-done:
	case <-ctx.Done():
		// The deadline ends the job even when the refresher ignores
		// cancellation; whatever it returns later cannot change the job. A
		// result that arrived with the deadline still counts.
		select {
		case err = <-done:
		default:
			err = ctx.Err()
		}
	}
	finished := time.Now().UTC()
	job, ok := h.quotaJobs.update(id, func(job *quotaRefreshJob) {
		job.FinishedAt = &finished
		switch {
		case err == nil:
			job.Status = quotaRefreshSucceeded
		case errors.Is(err, context.DeadlineExceeded) || ctx.Err() != nil:
			job.Status, job.Error = quotaRefreshFailed, "the refresh did not finish in time"
		default:
			job.Status, job.Error = quotaRefreshFailed, "one or more accounts could not be read"
		}
	})
	if ok {
		h.publishQuotaRefresh(job)
	}
}

func (h *Handler) publishQuotaRefresh(job quotaRefreshJob) {
	if h.opts.Events != nil {
		h.opts.Events.Publish("quota.refresh", job)
	}
}
