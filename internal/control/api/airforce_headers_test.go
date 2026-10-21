package api

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/discovery"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

func TestAirforceDiscoveryAndProbeKeepTheirAcceptHeaders(t *testing.T) {
	var seen atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		if r.Header.Get("HTTP-Referer") != "https://endpoint-proxy.local" || r.Header.Get("X-Title") != "Endpoint Proxy" || r.Header.Get("Authorization") != "Bearer fixture-key" {
			t.Errorf("identity headers: %v", r.Header)
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/models":
			if r.Header.Get("Accept") != "application/json" {
				t.Errorf("discovery Accept = %q", r.Header.Get("Accept"))
			}
			io.WriteString(w, `{"data":[{"id":"fixture-model"}]}`)
		case "POST /v1/chat/completions":
			if r.Header.Get("Accept") != "text/event-stream, application/x-ndjson, application/json" {
				t.Errorf("probe Accept = %q", r.Header.Get("Accept"))
			}
			io.WriteString(w, `{"choices":[{"message":{"content":"OK"}}]}`)
		default:
			t.Errorf("unexpected target: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer upstream.Close()
	h, mux, store, creds, cookie := newProvidersAPI(t, true, &discovery.Client{HTTP: upstream.Client()})
	defer store.Close()
	var spec registry.Spec
	for _, candidate := range registry.Builtins() {
		if candidate.ID == "api-airforce" {
			spec = candidate
			h.opts.Providers = append(h.opts.Providers, spec)
		}
	}
	node, err := creds.PutNode(context.Background(), credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: "api-airforce", Name: "Airforce", BaseURL: upstream.URL + "/v1", Transports: []string{"openai-chat"}})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := creds.PutConnection(context.Background(), credentials.Connection{NodeID: node.ID, Name: "fixture", Identity: "fixture", AuthKind: credentials.AuthAPIKey, Enabled: true, Secret: credentials.Secret{AccessToken: "fixture-key"}})
	if err != nil {
		t.Fatal(err)
	}
	headers := http.Header{"Accept": []string{"application/json"}}
	for name, values := range modelWireHeaders(probeRef(node, connection, spec, "openai-chat", "fixture-model", "fixture-key")) {
		headers[name] = values
	}
	if headers.Get("Accept") != "application/json" {
		t.Fatalf("shared discovery/probe identity overwrote JSON Accept: %v", headers)
	}
	discovered := doJSON(t, mux, cookie, "POST", "/admin/v1/connections/"+connection.ID+"/discover", "")
	if discovered.Code != 200 || !strings.Contains(discovered.Body.String(), "fixture-model") {
		t.Fatalf("discovery: %d %s", discovered.Code, discovered.Body)
	}
	probed := doJSON(t, mux, cookie, "POST", "/admin/v1/connections/"+connection.ID+"/test-models", `{"transport":"openai-chat","modelIds":["fixture-model"]}`)
	if probed.Code != 200 || !strings.Contains(probed.Body.String(), `"ok":true`) {
		t.Fatalf("probe: %d %s", probed.Code, probed.Body)
	}
	if seen.Load() != 2 {
		t.Fatalf("upstream requests = %d", seen.Load())
	}
}
