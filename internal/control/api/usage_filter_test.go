package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func seedUsage(t *testing.T, handler *Handler) {
	t.Helper()
	rows := []string{
		`('r1','openai','gpt-5','c1','key-a',200,10,5,0,0,100,50,'2026-10-01T10:00:00.000Z')`,
		`('r2','openai','gpt-5-mini','c1','key-b',429,0,0,0,0,10,NULL,'2026-10-02T10:00:00.000Z')`,
		`('r3','anthropic','claude','c2','key-a',200,7,3,1,1,80,40,'2026-10-03T10:00:00.000Z')`,
		`('r4','anthropic','claude','c2',NULL,500,0,0,0,0,5,NULL,'2026-10-04T10:00:00.000Z')`,
	}
	for _, row := range rows {
		if _, err := handler.opts.DB.Exec(`INSERT INTO usage_events(request_id,provider_id,model_id,connection_id,api_key_id,status,input_tokens,output_tokens,cache_read_tokens,cache_write_tokens,duration_ms,ttft_ms,created_at) VALUES` + row); err != nil {
			t.Fatal(err)
		}
	}
}

func usageIDs(t *testing.T, mux *http.ServeMux, cookie *http.Cookie, query string) ([]string, int) {
	t.Helper()
	recorder := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/usage?"+query, "")
	if recorder.Code != http.StatusOK {
		return nil, recorder.Code
	}
	var body struct {
		Items []struct {
			RequestID string `json:"requestId"`
		} `json:"items"`
	}
	decodeBody(t, recorder, &body)
	var ids []string
	for _, item := range body.Items {
		ids = append(ids, item.RequestID)
	}
	return ids, recorder.Code
}

func TestUsageFilters(t *testing.T) {
	handler, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	seedUsage(t, handler)
	cases := []struct {
		name, query string
		want        []string
	}{
		{"none", "", []string{"r4", "r3", "r2", "r1"}},
		{"provider", "providerId=openai", []string{"r2", "r1"}},
		{"model", "modelId=claude", []string{"r4", "r3"}},
		{"connection", "connectionId=c1", []string{"r2", "r1"}},
		{"api key", "apiKeyId=key-a", []string{"r3", "r1"}},
		{"status", "status=429", []string{"r2"}},
		{"combined", "providerId=anthropic&status=200", []string{"r3"}},
		{"start", "startDate=" + url.QueryEscape("2026-10-03T00:00:00Z"), []string{"r4", "r3"}},
		{"end inclusive", "endDate=" + url.QueryEscape("2026-10-02T10:00:00Z"), []string{"r2", "r1"}},
		{"offset zone", "startDate=" + url.QueryEscape("2026-10-03T03:00:00+02:00") + "&endDate=" + url.QueryEscape("2026-10-03T23:00:00Z"), []string{"r3"}},
		{"request id", "requestId=r3", []string{"r3"}},
		{"no match", "providerId=nobody", nil},
		{"injection is inert", "providerId=" + url.QueryEscape("openai' OR '1'='1"), nil},
	}
	for _, c := range cases {
		got, code := usageIDs(t, mux, cookie, c.query)
		if code != http.StatusOK || strings.Join(got, ",") != strings.Join(c.want, ",") {
			t.Fatalf("%s: code=%d got=%v want=%v", c.name, code, got, c.want)
		}
	}
}

func TestUsageFilterRejectsBadInputWithoutEchoingIt(t *testing.T) {
	handler, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	seedUsage(t, handler)
	for _, query := range []string{
		"status=abc", "status=99", "status=600",
		"startDate=yesterday", "endDate=2026-13-45",
		"startDate=" + url.QueryEscape("2026-10-05T00:00:00Z") + "&endDate=" + url.QueryEscape("2026-10-01T00:00:00Z"),
		"providerId=" + strings.Repeat("a", 201),
	} {
		recorder := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/usage?"+query, "")
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "invalid_filter") {
			t.Fatalf("%s: status=%d body=%s", query, recorder.Code, recorder.Body.String())
		}
		for _, echoed := range []string{"abc", "yesterday", "2026-13-45", strings.Repeat("a", 50)} {
			if strings.Contains(recorder.Body.String(), echoed) {
				t.Fatalf("%s: error echoed caller input %q: %s", query, echoed, recorder.Body.String())
			}
		}
	}
}

func TestClearConsoleLogs(t *testing.T) {
	handler, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	if recorder := doJSON(t, mux, nil, http.MethodDelete, "/admin/v1/logs", ""); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status=%d want 401", recorder.Code)
	}
	if recorder := doJSON(t, mux, cookie, http.MethodDelete, "/admin/v1/logs", ""); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured status=%d want 503", recorder.Code)
	}
	handler.opts.Logs = NewLogBuffer(10)
	logger := slog.New(LogHandler(slog.NewTextHandler(&strings.Builder{}, nil), handler.opts.Logs))
	logger.Info("one")
	logger.Info("two")
	cleared := doJSON(t, mux, cookie, http.MethodDelete, "/admin/v1/logs", "")
	var body struct {
		Cleared int `json:"cleared"`
	}
	decodeBody(t, cleared, &body)
	if cleared.Code != http.StatusOK || body.Cleared != 2 {
		t.Fatalf("clear status=%d body=%s", cleared.Code, cleared.Body.String())
	}
	list := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/logs", "")
	var page struct {
		Total int64 `json:"total"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &page); err != nil || page.Total != 0 {
		t.Fatalf("buffer not cleared: %s", list.Body.String())
	}
	logger.Info("three")
	if after := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/logs", ""); !strings.Contains(after.Body.String(), "three") {
		t.Fatalf("buffer must keep recording after clear: %s", after.Body.String())
	}
}
