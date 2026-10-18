package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/transforms/pxpipe"
)

func setSaverSettings(t *testing.T, mux *http.ServeMux, cookie *http.Cookie, settings string) {
	t.Helper()
	if recorder := doJSON(t, mux, cookie, http.MethodPatch, "/admin/v1/settings", `{"set":{`+settings+`}}`); recorder.Code != http.StatusOK {
		t.Fatalf("settings status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

type helperStatusBody struct {
	Enabled bool   `json:"enabled"`
	URL     string `json:"url"`
	Health  struct {
		Healthy    bool   `json:"healthy"`
		Class      string `json:"class"`
		Message    string `json:"message"`
		StatusCode int    `json:"statusCode"`
	} `json:"health"`
	Stats *pxpipe.Stats `json:"stats"`
}

func TestSaverRoutesRequireSession(t *testing.T) {
	_, mux, store, _, _ := newProvidersAPI(t, false, nil)
	defer store.Close()
	for _, path := range []string{"headroom/status", "pxpipe/status", "pxpipe/health", "pxpipe/logs", "pxpipe/stats"} {
		if recorder := doJSON(t, mux, nil, http.MethodGet, "/admin/v1/"+path, ""); recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s status=%d want 401", path, recorder.Code)
		}
	}
}

func TestHeadroomStatusReportsHealthyLocalHelper(t *testing.T) {
	_, mux, store, _, cookie := newProvidersAPI(t, true, nil)
	defer store.Close()
	var sawAuth atomic.Bool
	helper := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" || r.Header.Get("Cookie") != "" {
			sawAuth.Store(true)
		}
		if r.URL.Path != "/health" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer helper.Close()
	setSaverSettings(t, mux, cookie, `"headroomEnabled":"true","headroomUrl":"`+helper.URL+`"`)

	recorder := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/headroom/status", "")
	var status helperStatusBody
	decodeBody(t, recorder, &status)
	if recorder.Code != http.StatusOK || !status.Enabled || !status.Health.Healthy || status.URL != helper.URL {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if sawAuth.Load() {
		t.Fatal("probe forwarded credentials to the helper")
	}
}

func TestSaverHealthClassifiesUnavailableUpstream(t *testing.T) {
	_, mux, store, _, cookie := newProvidersAPI(t, true, nil)
	defer store.Close()
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }))
	defer failing.Close()

	cases := []struct {
		name, settings, route, class string
		statusCode                   int
	}{
		{"headroom refused", `"headroomUrl":"http://` + closedPort(t) + `"`, "headroom/status", "refused", 0},
		{"pxpipe refused", `"pxpipeUrl":"http://` + closedPort(t) + `"`, "pxpipe/health", "refused", 0},
		{"pxpipe bad status", `"pxpipeUrl":"` + failing.URL + `"`, "pxpipe/status", "bad_status", 503},
		{"headroom bad status", `"headroomUrl":"` + failing.URL + `"`, "headroom/status", "bad_status", 503},
	}
	for _, c := range cases {
		setSaverSettings(t, mux, cookie, c.settings)
		recorder := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/"+c.route, "")
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s: status=%d want 200 body=%s", c.name, recorder.Code, recorder.Body.String())
		}
		var health helperStatusBody
		decodeBody(t, recorder, &health)
		got := health.Health
		if strings.HasSuffix(c.route, "/health") { // health route returns the probe itself
			decodeBody(t, recorder, &got)
		}
		if got.Healthy || got.Class != c.class || got.StatusCode != c.statusCode || got.Message == "" {
			t.Fatalf("%s: %+v body=%s", c.name, got, recorder.Body.String())
		}
	}
}

func TestSaverHealthNotConfiguredAndBlocked(t *testing.T) {
	handler, mux, store, _, cookie := newProvidersAPI(t, true, nil)
	defer store.Close()
	var hits atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer local.Close()

	// pxpipeUrl is empty by default: reported, not an error and not contacted.
	recorder := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/pxpipe/health", "")
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"class":"not_configured"`) {
		t.Fatalf("unconfigured status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	// A URL saved under the trusted-local option must be refused once the
	// deployment no longer allows private upstreams, without a connection.
	setSaverSettings(t, mux, cookie, `"headroomUrl":"`+local.URL+`","pxpipeUrl":"`+local.URL+`"`)
	handler.opts.AllowPrivateUpstreams = false
	for _, route := range []string{"headroom/status", "pxpipe/status", "pxpipe/health"} {
		blocked := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/"+route, "")
		if blocked.Code != http.StatusOK || !strings.Contains(blocked.Body.String(), `"class":"blocked"`) {
			t.Fatalf("%s status=%d body=%s", route, blocked.Code, blocked.Body.String())
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("private helper was contacted %d times", hits.Load())
	}
}

func TestPXPIPELogsAndStats(t *testing.T) {
	handler, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()

	if recorder := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/pxpipe/logs", ""); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("without service status=%d want 503", recorder.Code)
	}
	service := pxpipe.NewService(pxpipe.Transform{})
	handler.opts.PXPIPE = service
	service.Observe(pxpipe.Diagnostic{Step: "pxpipe", Status: "skipped", Skipped: true, InputChars: 10})
	service.Observe(pxpipe.Diagnostic{Step: "pxpipe", Status: "ok", Attempted: true, Succeeded: true, InputChars: 30000, OutputChars: 9000, Duration: 120 * time.Millisecond})
	service.Observe(pxpipe.Diagnostic{Step: "pxpipe", Status: "failed", Attempted: true, InputChars: 30000, Error: strings.Repeat("e", 1000)})

	logs := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/pxpipe/logs?limit=2", "")
	var logBody struct {
		Logs []struct {
			Status     string `json:"status"`
			DurationMs int64  `json:"durationMs"`
			Error      string `json:"error"`
		} `json:"logs"`
	}
	decodeBody(t, logs, &logBody)
	if logs.Code != http.StatusOK || len(logBody.Logs) != 2 || logBody.Logs[0].Status != "ok" || logBody.Logs[0].DurationMs != 120 || len(logBody.Logs[1].Error) != 300 {
		t.Fatalf("logs status=%d body=%s", logs.Code, logs.Body.String())
	}

	stats := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/pxpipe/stats", "")
	var statsBody struct {
		Stats  pxpipe.Stats `json:"stats"`
		Recent []any        `json:"recent"`
	}
	decodeBody(t, stats, &statsBody)
	if stats.Code != http.StatusOK || statsBody.Stats.Attempts != 2 || statsBody.Stats.Succeeded != 1 || statsBody.Stats.Failed != 1 || statsBody.Stats.Skipped != 1 || len(statsBody.Recent) != 3 {
		t.Fatalf("stats status=%d body=%s", stats.Code, stats.Body.String())
	}
	for _, bad := range []string{"0", "501", "abc", "-1"} {
		if recorder := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/pxpipe/logs?limit="+bad, ""); recorder.Code != http.StatusBadRequest {
			t.Fatalf("limit=%s status=%d want 400", bad, recorder.Code)
		}
	}
	status := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/pxpipe/status", "")
	var statusBody helperStatusBody
	decodeBody(t, status, &statusBody)
	if statusBody.Stats == nil || statusBody.Stats.Attempts != 2 {
		t.Fatalf("status should embed stats: %s", status.Body.String())
	}
}
