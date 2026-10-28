package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	controlevents "github.com/raufimusaddiq/routeweft/internal/control/events"
)

// A refresh is a job (#226): 202 is acceptance, never an observation, and the
// job is followed to a terminal state an operator can read back.

type refreshReply struct {
	Accepted bool            `json:"accepted"`
	Joined   bool            `json:"joined"`
	Status   string          `json:"status"`
	Job      quotaRefreshJob `json:"job"`
}

func decodeRefresh(t *testing.T, body string) refreshReply {
	t.Helper()
	var reply refreshReply
	if err := json.Unmarshal([]byte(body), &reply); err != nil {
		t.Fatalf("%v: %s", err, body)
	}
	return reply
}

// awaitRefresh reads quota.refresh events until the job reaches status.
func awaitRefresh(t *testing.T, events <-chan controlevents.Event, id, status string) quotaRefreshJob {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case event := <-events:
			job, ok := event.Data.(quotaRefreshJob)
			if event.Type == "quota.refresh" && ok && job.ID == id && job.Status == status {
				return job
			}
		case <-deadline:
			t.Fatalf("job %s never reached %s", id, status)
		}
	}
}

func newRefreshRig(t *testing.T, refresher func(context.Context) error) (*Handler, *http.ServeMux, *http.Cookie, <-chan controlevents.Event) {
	t.Helper()
	handler, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	t.Cleanup(func() { store.Close() })
	handler.opts.Events = controlevents.New()
	events, cancel := handler.opts.Events.Subscribe()
	t.Cleanup(cancel)
	handler.opts.QuotaRefresher = refresher
	return handler, mux, cookie, events
}

func TestQuotaRefreshIsAJobNotAnObservation(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	_, mux, cookie, events := newRefreshRig(t, func(ctx context.Context) error {
		close(started)
		<-release
		return nil
	})
	if status := decodeRefresh(t, doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/quota/refresh", "").Body.String()); status.Status != "idle" {
		t.Fatalf("before any request: %+v", status)
	}
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/quota/refresh", "")
	if recorder.Code != http.StatusAccepted {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body)
	}
	accepted := decodeRefresh(t, recorder.Body.String())
	if !accepted.Accepted || accepted.Joined || accepted.Job.Status != quotaRefreshQueued || accepted.Job.ID == "" || accepted.Job.StartedAt != nil || accepted.Job.FinishedAt != nil {
		t.Fatalf("accepted %+v", accepted)
	}
	if strings.Contains(recorder.Body.String(), "observedAt") || strings.Contains(recorder.Body.String(), "remaining") {
		t.Fatalf("an accepted refresh carries no observation: %s", recorder.Body)
	}
	<-started
	awaitRefresh(t, events, accepted.Job.ID, quotaRefreshRunning)
	running := decodeRefresh(t, doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/quota/refresh", "").Body.String())
	if running.Status != quotaRefreshRunning || running.Job.ID != accepted.Job.ID || running.Job.StartedAt == nil || running.Job.FinishedAt != nil {
		t.Fatalf("running %+v", running)
	}
	close(release)
	done := awaitRefresh(t, events, accepted.Job.ID, quotaRefreshSucceeded)
	if done.FinishedAt == nil || done.Error != "" || done.FinishedAt.Before(*done.StartedAt) {
		t.Fatalf("succeeded %+v", done)
	}
	if read := decodeRefresh(t, doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/quota/refresh", "").Body.String()); read.Status != quotaRefreshSucceeded || read.Job.ID != accepted.Job.ID {
		t.Fatalf("read back %+v", read)
	}
}

func TestQuotaRefreshFailureIsReportedWithoutReaderText(t *testing.T) {
	_, mux, cookie, events := newRefreshRig(t, func(context.Context) error {
		return errors.New("upstream said: token sk-sealed-fixture rejected")
	})
	accepted := decodeRefresh(t, doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/quota/refresh", "").Body.String())
	failed := awaitRefresh(t, events, accepted.Job.ID, quotaRefreshFailed)
	if failed.Error != "one or more accounts could not be read" || failed.FinishedAt == nil {
		t.Fatalf("failed %+v", failed)
	}
	body := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/quota/refresh", "").Body.String()
	if read := decodeRefresh(t, body); read.Status != quotaRefreshFailed || strings.Contains(body, "sk-sealed-fixture") {
		t.Fatalf("read back %s", body)
	}
}

func TestQuotaRefreshThatOverrunsItsDeadlineFails(t *testing.T) {
	handler, mux, cookie, events := newRefreshRig(t, func(ctx context.Context) error {
		<-ctx.Done()
		return ctx.Err()
	})
	handler.quotaJobs.timeout = 20 * time.Millisecond
	accepted := decodeRefresh(t, doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/quota/refresh", "").Body.String())
	if failed := awaitRefresh(t, events, accepted.Job.ID, quotaRefreshFailed); failed.Error != "the refresh did not finish in time" {
		t.Fatalf("failed %+v", failed)
	}
}

// Repeated clicks join the job in flight: one refresh runs, never one per click.
func TestQuotaRefreshJoinsTheJobInFlight(t *testing.T) {
	var calls atomic.Int32
	release := make(chan struct{})
	_, mux, cookie, events := newRefreshRig(t, func(context.Context) error {
		calls.Add(1)
		<-release
		return nil
	})
	first := decodeRefresh(t, doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/quota/refresh", "").Body.String())
	for i := 0; i < 5; i++ {
		again := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/quota/refresh", "")
		joined := decodeRefresh(t, again.Body.String())
		if again.Code != http.StatusAccepted || !joined.Joined || joined.Job.ID != first.Job.ID {
			t.Fatalf("click %d: %d %s", i, again.Code, again.Body)
		}
	}
	close(release)
	awaitRefresh(t, events, first.Job.ID, quotaRefreshSucceeded)
	if n := calls.Load(); n != 1 {
		t.Fatalf("refreshes run %d, want 1", n)
	}
	next := decodeRefresh(t, doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/quota/refresh", "").Body.String())
	if next.Joined || next.Job.ID == first.Job.ID {
		t.Fatalf("a request after the job finished starts a new one: %+v", next)
	}
	awaitRefresh(t, events, next.Job.ID, quotaRefreshSucceeded)
	if n := calls.Load(); n != 2 {
		t.Fatalf("refreshes run %d, want 2", n)
	}
}

func TestQuotaRefreshStatusRequiresSession(t *testing.T) {
	_, mux, _, _ := newRefreshRig(t, func(context.Context) error { return nil })
	if recorder := doJSON(t, mux, nil, http.MethodGet, "/admin/v1/quota/refresh", ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", recorder.Code)
	}
}
