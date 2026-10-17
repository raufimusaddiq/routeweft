package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

func TestWorkflowNativeRoutesDiscoveryAndProbe(t *testing.T) {
	catalog, err := registry.NewBuiltinCatalog()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		provider, basePath, listing, probe, model, route, header, response string
	}{
		{"gemini", "/v1beta/models", "/v1beta/models", "/v1beta/models/gemini-2.5-flash:generateContent", "gemini-2.5-flash", "/v1beta/models/{model}:generateContent", "x-goog-api-key", `{"models":[{"name":"models/gemini-2.5-flash"}]}`},
		{"gemini", "/v1beta/", "/v1beta/models", "/v1beta/models/gemini-2.5-flash:generateContent", "gemini-2.5-flash", "/v1beta/models/{model}:generateContent", "x-goog-api-key", `{"models":[{"name":"models/gemini-2.5-flash"}]}`},
		{"ollama", "", "/api/tags", "/api/chat", "llama3:latest", "/api/chat", "Authorization", `{"models":[{"name":"llama3:latest"}]}`},
		{"anthropic", "/v1", "/v1/models", "/v1/messages", "claude-sonnet-4", "/v1/messages", "x-api-key", `{"data":[{"id":"claude-sonnet-4"}]}`},
		{"claude", "/v1", "/v1/models", "/v1/messages", "claude-sonnet-4", "/v1/messages", "x-api-key", `{"data":[{"id":"claude-sonnet-4"}]}`},
	} {
		t.Run(tc.provider+tc.basePath, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get(tc.header) == "" {
					t.Error("native authentication missing")
				}
				if tc.header == "x-api-key" && r.Header.Get("anthropic-version") != "2023-06-01" {
					t.Error("Anthropic version missing")
				}
				switch {
				case r.Method == "GET" && r.URL.Path == tc.listing:
					io.WriteString(w, tc.response)
				case r.Method == "POST" && r.URL.Path == tc.probe:
					io.Copy(io.Discard, r.Body)
					io.WriteString(w, `{}`)
				default:
					t.Errorf("unexpected native path: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer upstream.Close()
			h, mux, store, creds, cookie := newProvidersAPI(t, true, nil)
			defer store.Close()
			spec, ok := catalog.Lookup(tc.provider)
			if !ok {
				t.Fatal("missing builtin spec")
			}
			h.opts.Providers = append([]registry.Spec{spec}, h.opts.Providers...)
			node, err := creds.PutNode(context.Background(), credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: tc.provider, Name: tc.provider, BaseURL: upstream.URL + tc.basePath, Transports: []string{string(spec.Transports[0])}})
			if err != nil {
				t.Fatal(err)
			}
			connection, err := creds.PutConnection(context.Background(), credentials.Connection{NodeID: node.ID, Name: "synthetic", Identity: "synthetic", AuthKind: credentials.AuthAPIKey, Enabled: true, Secret: credentials.Secret{AccessToken: "synthetic"}})
			if err != nil {
				t.Fatal(err)
			}
			models, err := h.fetchConnectionModels(context.Background(), connection, node)
			if err != nil || len(models) != 1 || models[0] != tc.model {
				t.Fatalf("native discovery: %v %v", models, err)
			}
			w := doJSON(t, mux, cookie, "POST", "/admin/v1/connections/"+connection.ID+"/test", `{}`)
			if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
				t.Fatalf("native validation: %d %s", w.Code, w.Body)
			}
			w = doJSON(t, mux, cookie, "GET", "/admin/v1/provider-nodes/"+node.ID+"/routes", "")
			if w.Code != 200 || !strings.Contains(w.Body.String(), upstream.URL+tc.route) {
				t.Fatalf("native route: %d %s", w.Code, w.Body)
			}
			base, _ := url.Parse(spec.DefaultBaseURL)
			if tc.provider == "gemini" && !strings.HasSuffix(base.Path, "/models") {
				t.Fatal("test no longer covers the builtin Gemini base")
			}
		})
	}
}

func TestWorkflowIntrinsicModelKindsAndDeleteValidation(t *testing.T) {
	_, mux, store, _, cookie := newProvidersAPI(t, true, nil)
	defer store.Close()
	for _, body := range []string{
		`{"providerId":"typesafe","id":"jev-manual","kind":"llm"}`,
		`{"providerId":"cloudflare-ai","id":"@cf/cloudflare/clef","kind":"llm"}`,
	} {
		if w := doJSON(t, mux, cookie, "POST", "/admin/v1/models", body); w.Code != 400 || !strings.Contains(w.Body.String(), "intrinsic_model_kind") {
			t.Fatalf("contradictory intrinsic kind accepted: %d %s", w.Code, w.Body)
		}
	}
	for _, kind := range []string{"llm", "systemone", "llm"} {
		body := `{"providerId":"openai","id":"manual-kind","kind":"` + kind + `"}`
		if w := doJSON(t, mux, cookie, "POST", "/admin/v1/models", body); w.Code != 200 {
			t.Fatalf("ordinary kind update rejected: %d %s", w.Code, w.Body)
		}
	}
	for _, model := range []string{"bad?query", "bad#fragment", "bad%escape", `bad\\path`, "https://example.com", "../escape"} {
		body := `{"providerId":"openai","modelId":"` + model + `"}`
		if w := doJSON(t, mux, cookie, "DELETE", "/admin/v1/models", body); w.Code != 400 || !strings.Contains(w.Body.String(), "invalid_model") {
			t.Fatalf("unsafe delete accepted (%s): %d %s", model, w.Code, w.Body)
		}
	}
}
