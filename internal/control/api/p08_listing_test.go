package api

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// listingRig answers every listing with body and records what was asked.
func listingRig(t *testing.T, body string) (*Handler, func() []recordedCall) {
	t.Helper()
	h := New(Options{Providers: registry.Builtins()})
	var mu sync.Mutex
	var calls []recordedCall
	h.opts.DiscoveryClient.HTTP = probeHTTPFunc(func(r *http.Request) (*http.Response, error) {
		var raw []byte
		if r.Body != nil {
			raw, _ = io.ReadAll(r.Body)
		}
		mu.Lock()
		calls = append(calls, recordedCall{r.Method + " " + r.URL.String(), r.Header.Clone(), string(raw)})
		mu.Unlock()
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	return h, func() []recordedCall { mu.Lock(); defer mu.Unlock(); return append([]recordedCall(nil), calls...) }
}

// Claude is listed at https://api.anthropic.com/v1/models (route.js:131-141). An
// API key goes as x-api-key with anthropic-version 2023-06-01 and no bearer, as the
// route sends it. An OAuth token stays the registry's bearer (registry/claude.js:48-51),
// which is how the pin's own serving sends it; the route's x-api-key for it is a
// recorded divergence (PARITY 6.15).
func TestClaudeListingIsThePinsRequest(t *testing.T) {
	for _, tc := range []struct {
		auth       credentials.AuthKind
		wantKey    string
		wantBearer string
	}{
		{credentials.AuthAPIKey, "tok", ""},
		{credentials.AuthOAuth, "", "Bearer tok"},
	} {
		h, calls := listingRig(t, `{"data":[{"id":"claude-sonnet-4-5","display_name":"Claude Sonnet 4.5","type":"model"},{"id":"claude-opus-4-1"}],"has_more":false}`)
		node, connection := builtinConnection("claude", routing.ProviderSettings{}, tc.auth)
		models, err := h.fetchConnectionModelEntries(context.Background(), connection, node)
		if err != nil {
			t.Fatalf("%s: %v", tc.auth, err)
		}
		got := calls()
		if len(got) != 1 || got[0].URL != "GET https://api.anthropic.com/v1/models" {
			t.Fatalf("%s: listing went to %+v", tc.auth, got)
		}
		header := got[0].Header
		if header.Get("x-api-key") != tc.wantKey || header.Get("Authorization") != tc.wantBearer || header.Get("anthropic-version") != "2023-06-01" {
			t.Errorf("%s: headers %v", tc.auth, header)
		}
		ids := map[string]bool{}
		for _, model := range models {
			ids[model.ID] = true
		}
		if len(models) != 2 || !ids["claude-sonnet-4-5"] || !ids["claude-opus-4-1"] {
			t.Errorf("%s: models %+v", tc.auth, models)
		}
	}
}

// ollama-local is listed at {host}/api/tags with no credential (route.js:491-507),
// the host being the connection's own base or http://localhost:11434
// (config/providers.js:5-10); the ids are the tags' names.
func TestOllamaLocalListingReadsApiTags(t *testing.T) {
	const tags = `{"models":[{"name":"llama3.2:latest","model":"llama3.2:latest","size":2019393189},{"name":"qwen2.5-coder:7b","model":"qwen2.5-coder:7b"}]}`
	for _, tc := range []struct{ name, base, want string }{
		{"default host", "", "GET http://localhost:11434/api/tags"},
		{"connection host", "http://gpu-box.lan:11434/", "GET http://gpu-box.lan:11434/api/tags"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, calls := listingRig(t, tags)
			h.opts.AllowPrivateUpstreams = true
			node, connection := builtinConnection("ollama-local", routing.ProviderSettings{}, credentials.AuthNone)
			node.BaseURL = tc.base
			models, err := h.fetchConnectionModelEntries(context.Background(), connection, node)
			if err != nil {
				t.Fatal(err)
			}
			got := calls()
			if len(got) != 1 || got[0].URL != tc.want {
				t.Fatalf("listing went to %+v, want %s", got, tc.want)
			}
			if got[0].Header.Get("Authorization") != "" || got[0].Header.Get("x-api-key") != "" {
				t.Errorf("a credential was sent: %v", got[0].Header)
			}
			if len(models) != 2 || models[0].ID != "llama3.2:latest" || models[1].ID != "qwen2.5-coder:7b" {
				t.Errorf("models %+v", models)
			}
		})
	}
}
