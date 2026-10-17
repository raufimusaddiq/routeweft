package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func TestConfiguredWorkflowAppWiring(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models", "/suggestions":
			wantAuth := "Bearer synthetic-0"
			if r.URL.Path == "/suggestions" {
				wantAuth = ""
			}
			if r.Header.Get("Authorization") != wantAuth {
				t.Error("wrong catalog credential")
			}
			io.WriteString(w, `{"data":[{"id":"vendor/model","endpoints":["chat"]},{"id":"new/model"}]}`)
		case "/chat/completions":
			io.Copy(io.Discard, r.Body)
			io.WriteString(w, `{"choices":[{"message":{"content":"OK"}}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer upstream.Close()
	a, key := servingApp(t, upstream.URL, 1)
	if _, err := a.admin.Bootstrap(context.Background(), "workflow-admin", "operator", "synthetic-password"); err != nil {
		t.Fatal(err)
	}
	handler := a.Handler()
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, httptest.NewRequest("POST", "/admin/v1/auth/login", strings.NewReader(`{"username":"operator","password":"synthetic-password"}`)))
	if w.Code != 200 || len(w.Result().Cookies()) != 1 {
		t.Fatalf("login: %d %s", w.Code, w.Body)
	}
	cookie := w.Result().Cookies()[0]
	request := func(method, path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct{ method, path, body, contains string }{
		{"POST", "/admin/v1/connections/account-00/test", `{"modelId":"vendor/model"}`, `"ok":true`},
		{"POST", "/admin/v1/providers/test-batch", `{"mode":"all","modelId":"vendor/model"}`, `"passed":1`},
		{"POST", "/admin/v1/models/catalog-sync", `{}`, `"success":true`},
		{"GET", "/admin/v1/providers/suggested-models?type=kenari&url=" + url.QueryEscape(upstream.URL+"/suggestions"), "", `"id":"vendor/model"`},
		{"POST", "/admin/v1/models", `{"providerId":"test","id":"typed/custom:tag","kind":"systemone","capabilities":["reasoning"]}`, `"source":"custom"`},
	} {
		w := request(tc.method, tc.path, tc.body)
		if w.Code != 200 || !strings.Contains(w.Body.String(), tc.contains) {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body)
		}
	}
	snapshot, _ := a.runtime.Load()
	if model, ok := snapshot.ResolveModel("test", "typed/custom:tag"); !ok || snapshot.ModelKind(model) != "systemone" {
		t.Fatalf("custom metadata not active: %+v", model)
	}
	if err := a.runtime.PutModel(context.Background(), runtime.Model{ProviderID: "test", ID: "manual:7b", Capabilities: []string{"vision"}}); err != nil {
		t.Fatal(err)
	}
	w = request("DELETE", "/admin/v1/models", `{"providerId":"test","modelId":"manual:7b"}`)
	if w.Code != 200 {
		t.Fatalf("delete: %d %s", w.Code, w.Body)
	}
	a.runtime.State().Cooldown("account-00", time.Now().Add(time.Hour))
	w = serveRequest(handler, key, "/api/v1/chat/completions", `{"model":"fast","messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != 503 {
		t.Fatalf("cooldown not serving: %d %s", w.Code, w.Body)
	}
	w = request("POST", "/admin/v1/models/availability", `{"action":"clearCooldown","provider":"test","model":"__all"}`)
	if w.Code != 200 {
		t.Fatalf("reset: %d %s", w.Code, w.Body)
	}
	if err := a.store.Close(); err != nil {
		t.Fatal(err)
	}
	w = serveRequest(handler, key, "/api/v1/chat/completions", `{"model":"fast","messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != 200 {
		t.Fatalf("serving after reset/closed DB: %d %s", w.Code, w.Body)
	}
}
