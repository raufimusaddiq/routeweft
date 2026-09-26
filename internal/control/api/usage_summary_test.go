package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
)

func TestUsageSummaryRequiresSession(t *testing.T) {
	_, mux, store, _, _ := newProvidersAPI(t, false, nil)
	defer store.Close()
	if recorder := doJSON(t, mux, nil, http.MethodGet, "/admin/v1/usage/summary?period=7d", ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d want 401", recorder.Code)
	}
}

func TestUsageSummaryRejectsUnknownPeriod(t *testing.T) {
	_, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	if recorder := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/usage/summary?period=eternity", ""); recorder.Code != http.StatusBadRequest {
		t.Fatalf("status=%d want 400 body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestUsageSummaryAggregatesAndAttributes(t *testing.T) {
	_, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	ctx := context.Background()
	// Seed one day of usage: two successes on openai/gpt-5 and one error on
	// anthropic/claude. Only successes reach the daily rollup.
	for day, rows := range map[string][]struct {
		provider, model string
		status          int
		input, output   int64
	}{
		"2026-09-24": {{"openai", "gpt-5", 200, 100, 20}, {"openai", "gpt-5", 200, 50, 10}, {"anthropic", "claude", 500, 0, 0}},
		"2026-09-25": {{"openai", "gpt-5", 200, 30, 5}},
	} {
		for _, row := range rows {
			if _, err := store.DB().ExecContext(ctx, "INSERT INTO usage_events(request_id,provider_id,model_id,status,input_tokens,output_tokens,created_at) VALUES(?,?,?,?,?,?,?)", "req-"+day+row.model, row.provider, row.model, row.status, row.input, row.output, day+"T12:00:00.000Z"); err != nil {
				t.Fatal(err)
			}
			if row.status >= 200 && row.status < 300 {
				if _, err := store.DB().ExecContext(ctx, "INSERT INTO usage_daily(day,provider_id,model_id,requests,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(day,provider_id,model_id) DO UPDATE SET requests=requests+1,input_tokens=input_tokens+excluded.input_tokens,output_tokens=output_tokens+excluded.output_tokens", day, row.provider, row.model, 1, row.input, row.output, 0, 0); err != nil {
					t.Fatal(err)
				}
			}
		}
	}

	recorder := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/usage/summary?period=all", "")
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var body struct {
		Period string `json:"period"`
		Totals struct {
			Requests     int64 `json:"requests"`
			InputTokens  int64 `json:"inputTokens"`
			OutputTokens int64 `json:"outputTokens"`
			Errors       int64 `json:"errors"`
		} `json:"totals"`
		Series    []map[string]any `json:"series"`
		Providers []struct {
			Key      string `json:"key"`
			Requests int64  `json:"requests"`
		} `json:"providers"`
		Statuses []struct {
			Key      string `json:"key"`
			Requests int64  `json:"requests"`
		} `json:"statuses"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Period != "all" {
		t.Fatalf("period=%q", body.Period)
	}
	if body.Totals.Requests != 4 || body.Totals.InputTokens != 180 || body.Totals.OutputTokens != 35 || body.Totals.Errors != 1 {
		t.Fatalf("totals=%+v", body.Totals)
	}
	if len(body.Series) != 2 {
		t.Fatalf("series=%d want 2", len(body.Series))
	}
	if len(body.Providers) != 2 || body.Providers[0].Key != "openai" || body.Providers[0].Requests != 3 {
		t.Fatalf("providers=%+v", body.Providers)
	}
	foundError := false
	for _, status := range body.Statuses {
		if status.Key == "500" && status.Requests == 1 {
			foundError = true
		}
	}
	if !foundError {
		t.Fatalf("status breakdown missing 500: %+v", body.Statuses)
	}
}
