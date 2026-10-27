package app

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

// hopRecorder is a stand-in proxy: it records the destination host of every
// request it is asked to forward and answers 502 itself, so no request
// leaves the test.
type hopRecorder struct {
	mu    sync.Mutex
	hosts []string
	URL   string
}

func newHopRecorder(t *testing.T) *hopRecorder {
	t.Helper()
	recorder := &hopRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.mu.Lock()
		recorder.hosts = append(recorder.hosts, r.URL.Hostname())
		recorder.mu.Unlock()
		http.Error(w, "fixture proxy", http.StatusBadGateway)
	}))
	t.Cleanup(server.Close)
	recorder.URL = server.URL
	return recorder
}

func (r *hopRecorder) saw(host string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, seen := range r.hosts {
		if seen == host {
			return true
		}
	}
	return false
}

// TestImportedCodexConnectionProxyEndToEnd drives the production wiring: a
// Codex connection carrying LiteRouter's per-connection proxy sends inference,
// token refresh and usage reads through it, and its no-proxy list hands a host
// to the global proxy (connectionProxy.js:151-165, proxyFetch.js:62-73,112-114,
// chatCore.js:365-370,473-474, usage route.js:159-185).
func TestImportedCodexConnectionProxyEndToEnd(t *testing.T) {
	for _, tc := range []struct {
		name    string
		noProxy string
		expired bool
		work    string // "inference" or "usage"
		host    string
		via     string // "legacy" or "global"
	}{
		{"inference through the connection proxy", "", false, "inference", "chatgpt.com", "legacy"},
		{"refresh through the connection proxy", "", true, "inference", "auth.openai.com", "legacy"},
		{"usage read through the connection proxy", "", false, "usage", "chatgpt.com", "legacy"},
		{"bypassed inference takes the global proxy", "chatgpt.com", false, "inference", "chatgpt.com", "global"},
		{"bypassed usage read takes the global proxy", ".chatgpt.com", false, "usage", "chatgpt.com", "global"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			legacy, global := newHopRecorder(t), newHopRecorder(t)
			a := New(Config{DataDir: t.TempDir(), CredentialKey: []byte("01234567890123456789012345678901"), AllowPrivateUpstreams: true}, nil)
			if err := a.Initialize(ctx); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = a.store.Close() })
			if _, err := a.runtime.SetSettings(ctx, map[string]string{"outboundProxyEnabled": "true", "outboundProxyUrl": global.URL}, nil); err != nil {
				t.Fatal(err)
			}
			node, err := a.creds.PutNode(ctx, credentials.Node{ProviderID: "codex", Kind: credentials.NodeBuiltin, Name: "Codex"})
			if err != nil {
				t.Fatal(err)
			}
			expiry := time.Now().Add(time.Hour)
			if tc.expired {
				expiry = time.Now().Add(-time.Minute)
			}
			connection, err := a.creds.PutConnection(ctx, credentials.Connection{NodeID: node.ID, Name: "Imported", AuthKind: credentials.AuthOAuth, Identity: "proxy@example.invalid", Enabled: true, Secret: credentials.Secret{
				AccessToken: "fixture-access", RefreshToken: "fixture-refresh", Expiry: expiry, AuthType: credentials.AuthTypeOAuth,
				// As LiteRouter exports it and the Codex bulk import keeps it.
				ProviderData: map[string]string{"chatgptAccountId": "acct-fixture", "connectionProxyEnabled": "true", "connectionProxyUrl": legacy.URL, "connectionNoProxy": tc.noProxy},
			}})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := a.credReg.Load(ctx, "codex"); err != nil {
				t.Fatal(err)
			}
			switch tc.work {
			case "inference":
				_, key, err := a.runtime.CreateAPIKey(ctx, "fixture-client")
				if err != nil {
					t.Fatal(err)
				}
				request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"codex/gpt-5.5","input":"hi"}`))
				request.Header.Set("Authorization", "Bearer "+key)
				request.Header.Set("Content-Type", "application/json")
				a.Handler().ServeHTTP(httptest.NewRecorder(), request)
			case "usage":
				_ = a.RefreshQuota(ctx)
			}
			want, other := legacy, global
			if tc.via == "global" {
				want, other = global, legacy
			}
			if !want.saw(tc.host) {
				t.Fatalf("%s did not reach %s via the %s proxy (legacy saw %v, global saw %v)", connection.ID, tc.host, tc.via, legacy.hosts, global.hosts)
			}
			if other.saw(tc.host) {
				t.Fatalf("%s also went via the other proxy (legacy saw %v, global saw %v)", tc.host, legacy.hosts, global.hosts)
			}
		})
	}
}
