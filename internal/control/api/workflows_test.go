package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func workflowAccount(t *testing.T, creds *credentials.Store, provider, base, identity string) credentials.Connection {
	t.Helper()
	node, err := creds.PutNode(context.Background(), credentials.Node{ID: provider, Kind: credentials.NodeGeneric, ProviderID: provider, Prefix: provider, Name: provider, BaseURL: base, Transports: []string{"openai-chat"}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := creds.PutConnection(context.Background(), credentials.Connection{NodeID: node.ID, Name: identity, Identity: identity, AuthKind: credentials.AuthAPIKey, Enabled: true, Secret: credentials.Secret{AccessToken: identity}})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestWorkflowValidationFallbackAuthAndDiscoveryOnly(t *testing.T) {
	var discoveryStatus, inferenceStatus atomic.Int32
	var probes atomic.Int32
	discoveryStatus.Store(404)
	inferenceStatus.Store(200)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer synthetic" {
			t.Error("missing credential")
		}
		if r.URL.Path == "/models" {
			w.WriteHeader(int(discoveryStatus.Load()))
			io.WriteString(w, `{"data":[{"id":"manual:7b"}]}`)
			return
		}
		if r.URL.Path != "/chat/completions" || r.Method != "POST" {
			t.Errorf("unexpected probe %s %s", r.Method, r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), "unknown-manual") {
			w.WriteHeader(404)
			return
		}
		if string(body) != `{"max_tokens":32,"messages":[{"content":"Reply with OK.","role":"user"}],"model":"manual:7b","stream":true}` {
			t.Errorf("probe wire changed: %s", body)
		}
		probes.Add(1)
		w.WriteHeader(int(inferenceStatus.Load()))
		io.WriteString(w, `{"choices":[{"message":{"content":"OK"}}]}`)
	}))
	defer upstream.Close()
	_, mux, store, creds, cookie := newProvidersAPI(t, true, nil)
	defer store.Close()
	c := workflowAccount(t, creds, "manual", upstream.URL, "synthetic")
	path := "/admin/v1/connections/" + c.ID + "/test"
	check := func(body string, wantOK bool, wantStatus int) {
		t.Helper()
		w := doJSON(t, mux, cookie, "POST", path, body)
		if !wantOK {
			var failure struct {
				Error struct {
					Code   string `json:"code"`
					Status int    `json:"upstreamStatus"`
				} `json:"error"`
			}
			if w.Code != 502 || json.Unmarshal(w.Body.Bytes(), &failure) != nil || failure.Error.Code != "connection_test_failed" || failure.Error.Status != wantStatus {
				t.Fatalf("failed validation: %d %s", w.Code, w.Body)
			}
			return
		}
		var result validationResult
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil || result.OK != wantOK || result.Status != wantStatus {
			t.Fatalf("validation: %d %s", w.Code, w.Body)
		}
		if strings.Contains(w.Body.String(), "synthetic") {
			t.Fatal("credential leak")
		}
	}
	check(`{"modelId":"manual:7b"}`, true, 200)
	check(`{}`, false, 0)
	discoveryStatus.Store(401)
	check(`{"modelId":"manual:7b"}`, false, 401)
	if probes.Load() != 1 {
		t.Fatal("auth rejection triggered inference")
	}
	discoveryStatus.Store(200)
	inferenceStatus.Store(403)
	check(`{}`, false, 403)
	if probes.Load() != 2 {
		t.Fatal("successful discovery substituted for inference validation")
	}
	inferenceStatus.Store(200)
	discoveryStatus.Store(404)
	before := doJSON(t, mux, cookie, "GET", "/admin/v1/provider-nodes", "").Body.String()
	body := `{"baseUrl":"` + upstream.URL + `","transports":["openai-chat"],"secret":{"accessToken":"synthetic"},"modelId":"manual:7b"}`
	w := doJSON(t, mux, cookie, "POST", "/admin/v1/provider-nodes/validate", body)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
		t.Fatalf("pre-save fallback: %d %s", w.Code, w.Body)
	}
	missing := doJSON(t, mux, cookie, "POST", path, `{"modelId":"unknown-manual"}`)
	if missing.Code != 502 || !strings.Contains(missing.Body.String(), "connection_test_failed") {
		t.Fatalf("manual fallback used discovery: %d %s", missing.Code, missing.Body)
	}
	if after := doJSON(t, mux, cookie, "GET", "/admin/v1/provider-nodes", "").Body.String(); before != after {
		t.Fatal("pre-save validation persisted state")
	}
	bad := doJSON(t, mux, cookie, "POST", path, `{"modelId":"../escape"}`)
	if bad.Code != 400 {
		t.Fatalf("unsafe probe model: %d", bad.Code)
	}
}

func TestWorkflowAvailabilityCatalogSyncBatchAndCustomDelete(t *testing.T) {
	var fail atomic.Bool
	var refreshed atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/models") {
			if fail.Load() {
				w.WriteHeader(500)
				return
			}
			if r.Header.Get("Authorization") == "Bearer b" {
				io.WriteString(w, `{"data":[{"id":"b-only"},{"id":"shared"}]}`)
			} else {
				name, context := "Original", 4096
				if refreshed.Load() {
					name, context = "Refreshed", 8192
				}
				fmt.Fprintf(w, `{"data":[{"id":"a-only","name":%q,"context_window":%d,"capabilities":["vision"]},{"id":"shared"}]}`, name, context)
			}
			return
		}
		io.Copy(io.Discard, r.Body)
		if r.Header.Get("Authorization") == "Bearer b" {
			w.WriteHeader(401)
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"OK"}}]}`)
	}))
	defer upstream.Close()
	h, mux, store, creds, cookie := newProvidersAPI(t, true, nil)
	defer store.Close()
	a := workflowAccount(t, creds, "catalog", upstream.URL, "a")
	b := workflowAccount(t, creds, "catalog", upstream.URL, "b")
	other := workflowAccount(t, creds, "other", upstream.URL, "other")
	if err := creds.SetConnectionEnabled(context.Background(), other.ID, false); err != nil {
		t.Fatal(err)
	}
	state := h.opts.Runtime.State()
	state.Cooldown(a.ID, time.Now().Add(time.Hour))
	state.Cooldown(b.ID, time.Now().Add(time.Hour))
	state.Cooldown(other.ID, time.Now().Add(time.Hour))
	w := doJSON(t, mux, cookie, "GET", "/admin/v1/models/availability?pageSize=1", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"unavailableCount":3`) {
		t.Fatalf("availability: %d %s", w.Code, w.Body)
	}
	w = doJSON(t, mux, cookie, "POST", "/admin/v1/models/availability", `{"action":"clearCooldown","provider":"catalog","model":"__all"}`)
	if w.Code != 200 {
		t.Fatalf("clear: %d %s", w.Code, w.Body)
	}
	if _, active := state.CooldownUntil(a.ID, time.Now()); active {
		t.Fatal("a still cooling")
	}
	if _, active := state.CooldownUntil(b.ID, time.Now()); active {
		t.Fatal("b still cooling")
	}
	if _, active := state.CooldownUntil(other.ID, time.Now()); !active {
		t.Fatal("unrelated cooldown cleared")
	}
	w = doJSON(t, mux, cookie, "POST", "/admin/v1/models/catalog-sync", `{}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"success":true`) {
		t.Fatalf("sync: %d %s", w.Code, w.Body)
	}
	snapshot, _ := h.opts.Runtime.Load()
	for _, id := range []string{"a-only", "b-only", "shared"} {
		if _, ok := snapshot.ResolveModel("catalog", id); !ok {
			t.Fatalf("account catalog %s lost", id)
		}
	}
	if model, _ := snapshot.ResolveModel("catalog", "a-only"); model.Name != "Original" || model.ContextWindow != 4096 || len(model.Capabilities) != 1 || model.Capabilities[0] != "vision" {
		t.Fatalf("discovery dropped metadata: %+v", model)
	}
	if model, _ := snapshot.ResolveModel("catalog", "shared"); model.Name != "shared" {
		t.Fatalf("missing display fallback: %+v", model)
	}
	refreshed.Store(true)
	w = doJSON(t, mux, cookie, "POST", "/admin/v1/models/catalog-sync", `{}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"success":true`) {
		t.Fatalf("metadata refresh: %d %s", w.Code, w.Body)
	}
	restarted, err := runtime.NewManagerObserved(context.Background(), store.DB(), nil, restartSealer(t))
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ = restarted.Load()
	if model, _ := snapshot.ResolveModel("catalog", "a-only"); model.Name != "Refreshed" || model.ContextWindow != 8192 || len(model.Capabilities) != 1 || model.Capabilities[0] != "vision" {
		t.Fatalf("metadata refresh not durable: %+v", model)
	}
	before := snapshot.ConfigRevision()
	fail.Store(true)
	w = doJSON(t, mux, cookie, "POST", "/admin/v1/models/catalog-sync", `{}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"success":false`) {
		t.Fatalf("failed sync: %d %s", w.Code, w.Body)
	}
	snapshot, _ = h.opts.Runtime.Load()
	if snapshot.ConfigRevision() != before {
		t.Fatal("failed discovery mutated catalog")
	}
	w = doJSON(t, mux, cookie, "GET", "/admin/v1/models/catalog-sync", "")
	if !strings.Contains(w.Body.String(), `"lastError"`) || !strings.Contains(w.Body.String(), `"running":false`) {
		t.Fatalf("sync status: %s", w.Body)
	}
	fail.Store(false)
	w = doJSON(t, mux, cookie, "POST", "/admin/v1/providers/test-batch", `{"mode":"provider","providerId":"catalog","modelId":"shared"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"total":2`) || !strings.Contains(w.Body.String(), `"passed":1`) || !strings.Contains(w.Body.String(), `"failed":1`) {
		t.Fatalf("batch: %d %s", w.Code, w.Body)
	}
	for _, body := range []string{`{"mode":"invalid"}`, `{"mode":"provider"}`} {
		if w := doJSON(t, mux, cookie, "POST", "/admin/v1/providers/test-batch", body); w.Code != 400 {
			t.Fatalf("bad mode: %d", w.Code)
		}
	}
	corrupt := workflowAccount(t, creds, "broken", upstream.URL, "corrupt")
	if _, err := store.DB().ExecContext(context.Background(), "UPDATE provider_connections SET secret_blob='corrupt' WHERE id=?", corrupt.ID); err != nil {
		t.Fatal(err)
	}
	w = doJSON(t, mux, cookie, "POST", "/admin/v1/providers/test-batch", `{"mode":"all","modelId":"shared"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"total":3`) || !strings.Contains(w.Body.String(), `"passed":1`) {
		t.Fatalf("corrupt credential stopped batch: %d %s", w.Code, w.Body)
	}
	checkAttribution := func(w *httptest.ResponseRecorder, method string) {
		t.Helper()
		var result struct {
			Results []validationResult `json:"results"`
		}
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &result) != nil {
			t.Fatalf("workflow failed: %d %s", w.Code, w.Body)
		}
		for _, item := range result.Results {
			if item.ConnectionID == corrupt.ID {
				if item.OK || item.ProviderID != "broken" || item.Method != method || item.Error == "" {
					t.Fatalf("unattributed credential failure: %+v", item)
				}
				return
			}
		}
		t.Fatal("corrupt account result missing")
	}
	checkAttribution(w, "inference")
	checkAttribution(doJSON(t, mux, cookie, "POST", "/admin/v1/models/catalog-sync", `{}`), "models")
	w = doJSON(t, mux, cookie, "POST", "/admin/v1/providers/test-batch", `{"mode":"provider","providerId":"catalog","modelId":"shared"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"total":2`) {
		t.Fatalf("unselected corrupt credential included: %d %s", w.Code, w.Body)
	}
	for _, body := range []string{`{"providerId":"catalog","id":"bad","kind":"image"}`, `{"providerId":"catalog","id":"bad","capabilities":["unknown"]}`, `{"providerId":"catalog","id":"http://127.0.0.1"}`} {
		if w := doJSON(t, mux, cookie, "POST", "/admin/v1/models", body); w.Code != 400 {
			t.Fatalf("invalid metadata accepted: %d", w.Code)
		}
	}
	if w := doJSON(t, mux, cookie, "POST", "/admin/v1/models", `{"providerId":"catalog","id":"ocr","capabilities":["imageToText"]}`); w.Code != 200 || !strings.Contains(w.Body.String(), `"kind":""`) {
		t.Fatalf("catalog capability rejected: %d %s", w.Code, w.Body)
	}
	snapshot, _ = h.opts.Runtime.Load()
	if model, ok := snapshot.ResolveModel("catalog", "ocr"); !ok || len(model.Capabilities) != 1 || model.Capabilities[0] != "vision" {
		t.Fatalf("imageToText capability not canonicalized: %+v", model)
	}
	if w := doJSON(t, mux, cookie, "DELETE", "/admin/v1/models", `{"providerId":"catalog","modelId":"ocr"}`); w.Code != 200 {
		t.Fatalf("catalog capability cleanup: %d %s", w.Code, w.Body)
	}
	w = doJSON(t, mux, cookie, "POST", "/admin/v1/models", `{"providerId":"catalog","id":"typed","kind":"systemone","capabilities":["reasoning","audio-input"]}`)
	if w.Code != 200 {
		t.Fatalf("custom add: %d %s", w.Code, w.Body)
	}
	w = doJSON(t, mux, cookie, "GET", "/admin/v1/models", "")
	if !strings.Contains(w.Body.String(), `"kind":"systemone"`) || !strings.Contains(w.Body.String(), `"reasoning"`) || !strings.Contains(w.Body.String(), `"audio-input"`) {
		t.Fatalf("custom metadata: %s", w.Body)
	}
	w = doJSON(t, mux, cookie, "GET", "/admin/v1/systemone", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"typed"`) || strings.Contains(w.Body.String(), `"id":"shared"`) {
		t.Fatalf("typed admin catalog: %d %s", w.Code, w.Body)
	}
	if w := doJSON(t, mux, cookie, "PATCH", "/admin/v1/models/disabled", `{"providerId":"catalog","modelId":"typed","disabled":true}`); w.Code != 200 {
		t.Fatalf("disable model before delete: %d %s", w.Code, w.Body)
	}
	w = doJSON(t, mux, cookie, "DELETE", "/admin/v1/models", `{"providerId":"catalog","modelId":"typed"}`)
	if w.Code != 200 {
		t.Fatalf("custom delete: %d %s", w.Code, w.Body)
	}
	w = doJSON(t, mux, cookie, "POST", "/admin/v1/models", `{"providerId":"catalog","id":"typed"}`)
	if w.Code != 200 {
		t.Fatalf("re-add deleted model: %d %s", w.Code, w.Body)
	}
	snapshot, _ = h.opts.Runtime.Load()
	if _, ok := snapshot.ResolveModel("catalog", "typed"); !ok {
		t.Fatal("re-added custom model revived as disabled")
	}
	if w := doJSON(t, mux, cookie, "PATCH", "/admin/v1/models/disabled", `{"providerId":"catalog","modelId":"","disabled":true}`); w.Code != 400 {
		t.Fatalf("empty disabled model accepted: %d %s", w.Code, w.Body)
	}
	w = doJSON(t, mux, cookie, "DELETE", "/admin/v1/models", `{"providerId":"catalog","modelId":"shared"}`)
	if w.Code != 409 {
		t.Fatalf("deleted discovery entry: %d", w.Code)
	}
	w = doJSON(t, mux, cookie, "GET", "/admin/v1/provider-nodes/catalog/routes", "")
	if w.Code != 200 || !strings.Contains(w.Body.String(), upstream.URL+"/chat/completions") {
		t.Fatalf("routes: %d %s", w.Code, w.Body)
	}
}

func TestWorkflowAuthOriginAndNetworkBoundary(t *testing.T) {
	_, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	for _, route := range []struct{ method, path string }{
		{"POST", "/admin/v1/provider-nodes/validate"}, {"POST", "/admin/v1/providers/test-batch"},
		{"GET", "/admin/v1/models/availability"}, {"POST", "/admin/v1/models/availability"},
		{"GET", "/admin/v1/models/catalog-sync"}, {"POST", "/admin/v1/models/catalog-sync"},
		{"GET", "/admin/v1/providers/suggested-models"}, {"GET", "/admin/v1/providers/kilo/free-models"},
		{"DELETE", "/admin/v1/models"}, {"GET", "/admin/v1/provider-nodes/id/routes"},
	} {
		w := doJSON(t, mux, nil, route.method, route.path, `{}`)
		if w.Code != 401 {
			t.Fatalf("unauthenticated %s: %d", route.path, w.Code)
		}
		if route.method != "GET" {
			r := httptest.NewRequest(route.method, route.path, strings.NewReader(`{}`))
			r.AddCookie(cookie)
			r.Header.Set("Origin", "https://evil.example")
			w = httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != 403 {
				t.Fatalf("cross-origin %s: %d", route.path, w.Code)
			}
		}
	}
	for _, target := range []string{"http://127.0.0.1:8080", "http://169.254.169.254", "file:///etc/passwd"} {
		w := doJSON(t, mux, cookie, "POST", "/admin/v1/provider-nodes/validate", fmt.Sprintf(`{"baseUrl":%q,"transports":["openai-chat"],"secret":{"accessToken":"synthetic"},"modelId":"manual"}`, target))
		if w.Code != 400 {
			t.Fatalf("unsafe validation URL %s: %d", target, w.Code)
		}
	}
	w := doJSON(t, mux, cookie, "GET", "/admin/v1/providers/suggested-models?type=kenari&url=http://127.0.0.1:8080", "")
	if w.Code != 400 {
		t.Fatalf("unsafe suggestion URL: %d", w.Code)
	}
}

func TestWorkflowCancellationAndSyncSingleflight(t *testing.T) {
	started := make(chan struct{}, 2)
	canceled := make(chan struct{}, 2)
	var probe atomic.Bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if probe.Load() && r.Method == "GET" {
			w.WriteHeader(404)
			return
		}
		io.Copy(io.Discard, r.Body)
		started <- struct{}{}
		<-r.Context().Done()
		canceled <- struct{}{}
	}))
	defer upstream.Close()
	h, mux, store, creds, cookie := newProvidersAPI(t, true, nil)
	defer store.Close()
	c := workflowAccount(t, creds, "cancel", upstream.URL, "synthetic")
	for _, path := range []string{"/admin/v1/providers/test-batch", "/admin/v1/models/catalog-sync", "/admin/v1/connections/" + c.ID + "/test"} {
		probe.Store(strings.HasSuffix(path, "/test"))
		ctx, cancel := context.WithCancel(context.Background())
		r := httptest.NewRequest("POST", path, strings.NewReader(`{"mode":"all","modelId":"manual"}`)).WithContext(ctx)
		r.AddCookie(cookie)
		done := make(chan *httptest.ResponseRecorder)
		go func() { rec := httptest.NewRecorder(); mux.ServeHTTP(rec, r); done <- rec }()
		select {
		case <-started:
		case <-time.After(3 * time.Second):
			cancel()
			t.Fatal("upstream not started")
		}
		if strings.Contains(path, "catalog-sync") {
			w := doJSON(t, mux, cookie, "POST", path, `{}`)
			if w.Code != 503 {
				t.Fatalf("overlapping sync: %d", w.Code)
			}
		}
		cancel()
		var rec *httptest.ResponseRecorder
		select {
		case rec = <-done:
		case <-time.After(3 * time.Second):
			t.Fatal("workflow ignored cancellation")
		}
		if !strings.HasSuffix(path, "/test") && (rec.Code != http.StatusRequestTimeout || !strings.Contains(rec.Body.String(), "canceled")) {
			t.Fatalf("canceled %s: %d %s", path, rec.Code, rec.Body)
		}
		if strings.HasSuffix(path, "/test") && (rec.Code != http.StatusRequestTimeout || !strings.Contains(rec.Body.String(), `"code":"connection_test_canceled"`)) {
			t.Fatalf("canceled saved connection %s: %d %s", path, rec.Code, rec.Body)
		}
		select {
		case <-canceled:
		case <-time.After(3 * time.Second):
			t.Fatal("upstream ignored cancellation")
		}
	}
	if _, active := h.opts.Runtime.State().CooldownUntil(c.ID, time.Now()); active {
		t.Fatal("canceled probe cooled account")
	}
}

func TestBatchModes(t *testing.T) {
	for _, tc := range []struct {
		mode      string
		auth      credentials.AuthKind
		kind      credentials.NodeKind
		transport string
		want      bool
	}{
		{"oauth", credentials.AuthOAuth, credentials.NodeBuiltin, "openai-chat", true},
		{"free", credentials.AuthNone, credentials.NodeBuiltin, "openai-chat", true},
		{"apikey", credentials.AuthAPIKey, credentials.NodeBuiltin, "systemone", false},
		{"systemone", credentials.AuthAPIKey, credentials.NodeBuiltin, "systemone", true},
		{"compatible", credentials.AuthAPIKey, credentials.NodeGeneric, "openai-chat", true},
		{"provider", credentials.AuthAPIKey, credentials.NodeBuiltin, "openai-chat", true},
	} {
		if got := batchMatch(tc.mode, "provider", credentials.Connection{ProviderID: "provider", AuthKind: tc.auth}, credentials.Node{Kind: tc.kind, Transports: []string{tc.transport}}); got != tc.want {
			t.Errorf("%s=%v", tc.mode, got)
		}
	}
}

func TestBatchSingleflight(t *testing.T) {
	h, mux, store, _, cookie := newProvidersAPI(t, true, nil)
	defer store.Close()
	h.workflowMu.Lock()
	h.batchBusy = true
	h.workflowMu.Unlock()
	r := httptest.NewRequest("POST", "/admin/v1/providers/test-batch", strings.NewReader(`{"mode":"all"}`))
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusConflict {
		t.Fatalf("overlapping batch: %d %s", w.Code, w.Body)
	}
}
