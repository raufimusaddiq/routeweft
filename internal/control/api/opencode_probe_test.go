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

// OpenCode Zen has no account, so its connection is AuthNone; the probe and the
// listing still send the public key serving sends (executors/opencode.js:492,
// testUtils.js:767).
func TestProbeSendsOpenCodePublicKey(t *testing.T) {
	var seen atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.Add(1)
		if got := r.Header.Get("Authorization"); got != "Bearer public" {
			t.Errorf("%s %s: Authorization %q", r.Method, r.URL.Path, got)
		}
		w.Header().Set("Content-Type", "application/json")
		if r.Method == "GET" {
			io.WriteString(w, `{"data":[{"id":"fixture-model"}]}`)
			return
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"OK"}}]}`)
	}))
	defer upstream.Close()
	h, mux, store, creds, cookie := newProvidersAPI(t, true, &discovery.Client{HTTP: upstream.Client()})
	defer store.Close()
	for _, candidate := range registry.Builtins() {
		if candidate.ID == "opencode" {
			h.opts.Providers = append(h.opts.Providers, candidate)
		}
	}
	node, err := creds.PutNode(context.Background(), credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: "opencode", Name: "OpenCode", BaseURL: upstream.URL, Transports: []string{"openai-chat"}})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := creds.PutConnection(context.Background(), credentials.Connection{NodeID: node.ID, Name: "fixture", Identity: "fixture", AuthKind: credentials.AuthNone, Enabled: true})
	if err != nil {
		t.Fatal(err)
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
