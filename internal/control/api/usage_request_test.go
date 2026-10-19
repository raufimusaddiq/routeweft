package api

import (
	"net/http"
	"testing"
	"time"
)

func TestUsageRequestRequiresSession(t *testing.T) {
	_, mux, store, _, _ := newProvidersAPI(t, false, nil)
	defer store.Close()
	if recorder := doJSON(t, mux, nil, http.MethodGet, "/admin/v1/usage/requests/r1", ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", recorder.Code)
	}
}

func TestUsageRequestReturnsEventsAndNullDetail(t *testing.T) {
	handler, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	seedUsage(t, handler)
	recorder := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/usage/requests/r3", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		RequestID string           `json:"requestId"`
		Events    []map[string]any `json:"events"`
		Detail    any              `json:"detail"`
	}
	decodeBody(t, recorder, &body)
	if body.RequestID != "r3" || len(body.Events) != 1 || body.Detail != nil {
		t.Fatalf("body=%+v want one event and null detail", body)
	}
	if body.Events[0]["connectionId"] != "c2" || body.Events[0]["apiKeyId"] != "key-a" || body.Events[0]["ttftMs"] != float64(40) {
		t.Fatalf("event=%v", body.Events[0])
	}
}

func TestUsageRequestIncludesRedactedDetail(t *testing.T) {
	handler, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	seedUsage(t, handler)
	if _, err := handler.opts.DB.Exec(`INSERT INTO request_details(request_id,route_mode,detail) VALUES('r1','native','{"Authorization":"secret","note":"ok"}')`); err != nil {
		t.Fatal(err)
	}
	recorder := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/usage/requests/r1", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if body := recorder.Body.String(); contains(body, "secret") || !contains(body, `"routeMode":"native"`) || !contains(body, "note") {
		t.Fatalf("body=%s want redacted detail with route mode", body)
	}
}

func TestUsageRequestUnknownIs404(t *testing.T) {
	_, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	if recorder := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/usage/requests/nope", ""); recorder.Code != http.StatusNotFound {
		t.Fatalf("status=%d want 404", recorder.Code)
	}
}

func TestUsageWindowTodayAnd60d(t *testing.T) {
	now := time.Date(2026, 10, 19, 15, 30, 0, 0, time.UTC)
	if since, ok := usageWindow("today", now); !ok || since != "2026-10-19T00:00:00.000Z" {
		t.Fatalf("today=%q,%v", since, ok)
	}
	if since, ok := usageWindow("60d", now); !ok || since != "2026-08-20T15:30:00.000Z" {
		t.Fatalf("60d=%q,%v", since, ok)
	}
}

func TestUsageHourlyStillRejectsTodayAnd60d(t *testing.T) {
	_, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	for _, period := range []string{"today", "60d"} {
		if recorder := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/usage/hourly?period="+period, ""); recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d want 400", period, recorder.Code)
		}
	}
}

func TestUsageRequestOmitsCountsForUnobservedUsage(t *testing.T) {
	handler, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	if _, err := handler.opts.DB.Exec(`INSERT INTO usage_events(request_id,provider_id,model_id,status,termination,usage_observed) VALUES('gone','p','m',200,'client_abandoned',0)`); err != nil {
		t.Fatal(err)
	}
	recorder := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/usage/requests/gone", "")
	var body struct {
		Events []map[string]any `json:"events"`
	}
	decodeBody(t, recorder, &body)
	if len(body.Events) != 1 {
		t.Fatalf("body=%s", recorder.Body.String())
	}
	event := body.Events[0]
	for _, key := range []string{"inputTokens", "outputTokens", "cacheReadTokens", "cacheWriteTokens"} {
		if _, present := event[key]; present {
			t.Fatalf("%s rendered for an unobserved request: %v", key, event)
		}
	}
	if event["usageObserved"] != false || event["termination"] != "client_abandoned" {
		t.Fatalf("event=%v", event)
	}
}
