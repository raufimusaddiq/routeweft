package app

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func TestConfiguredHistoricalAliasesWithoutDatabase(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "" && r.Header.Get("X-Api-Key") == "" && r.Header.Get("X-Goog-Api-Key") == "" {
			t.Error("missing provider credential")
		}
		body, err := io.ReadAll(r.Body)
		if err != nil || !strings.Contains(string(body), "hi") {
			t.Errorf("body: %s, error: %v", body, err)
		}
		if r.URL.Path == "/api/chat" && !strings.Contains(string(body), `"model":"ollama-model:7b"`) {
			t.Errorf("Ollama tag changed: %s", body)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	a, key := servingApp(t, upstream.URL, 1)
	ctx := context.Background()
	for _, protocol := range []string{"gemini", "ollama", "systemone"} {
		node, err := a.creds.PutNode(ctx, credentials.Node{ID: protocol, Kind: credentials.NodeGeneric, ProviderID: protocol, Name: protocol, Prefix: protocol, BaseURL: upstream.URL, Transports: []string{protocol}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := a.creds.PutConnection(ctx, credentials.Connection{ID: protocol, NodeID: node.ID, Name: protocol, Identity: protocol, AuthKind: credentials.AuthAPIKey, Enabled: true, Secret: credentials.Secret{AccessToken: "synthetic"}}); err != nil {
			t.Fatal(err)
		}
		model := protocol + "-model"
		if protocol == "ollama" {
			model += ":7b"
		}
		if err := a.runtime.PutModel(ctx, runtime.Model{ProviderID: protocol, ID: model}); err != nil {
			t.Fatal(err)
		}
	}
	if err := a.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if err := a.store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, prefix := range []string{"/v1/v1", "/api/v1"} {
		for _, tc := range []struct{ path, body string }{
			{"/chat/completions", `{"model":"fast","messages":[{"role":"user","content":"hi"}]}`},
			{"/responses", `{"model":"fast","input":"hi"}`},
			{"/responses/compact", `{"model":"fast","input":"hi"}`},
			{"/messages", `{"model":"fast","messages":[{"role":"user","content":"hi"}],"max_tokens":1}`},
			{"/messages/count_tokens", `{"model":"fast","messages":[{"role":"user","content":"hi"}]}`},
			{"/api/chat", `{"model":"ollama/ollama-model:7b","messages":[{"role":"user","content":"hi"}],"stream":false}`},
			{"/systemone", `{"model":"systemone-model","state":null,"questions":{"greeting":"hi"}}`},
		} {
			if w := serveRequest(a.Handler(), key, prefix+tc.path, tc.body); w.Code != 200 {
				t.Fatalf("%s: %d %s", prefix+tc.path, w.Code, w.Body)
			}
		}
		for _, suffix := range []string{"", "/models", "/models/info?id=test/vendor/model", "/models/test/vendor/model"} {
			r := httptest.NewRequest("GET", prefix+suffix, nil)
			r.Header.Set("X-Api-Key", key)
			w := httptest.NewRecorder()
			a.Handler().ServeHTTP(w, r)
			if w.Code != 200 {
				t.Fatalf("%s: %d %s", prefix+suffix, w.Code, w.Body)
			}
		}
	}
	if w := serveRequest(a.Handler(), key, "/api/v1beta/models/gemini-model:generateContent", `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`); w.Code != 200 {
		t.Fatalf("Gemini alias: %d %s", w.Code, w.Body)
	}
	for _, path := range []string{"/api/tags", "/api/v1beta/models"} {
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("X-Goog-Api-Key", key)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
}
