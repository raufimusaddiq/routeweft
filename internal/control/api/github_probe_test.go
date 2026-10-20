package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/discovery"
	"github.com/raufimusaddiq/routeweft/internal/providers/oauthrefresh"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

func TestGitHubOnboardingProbesAndDiscovery(t *testing.T) {
	for _, first := range []string{"probe", "discovery"} {
		t.Run(first, func(t *testing.T) {
			now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
			copilotCalls, wireCalls := 0, 0
			failRenewal := false
			exchange := probeHTTPFunc(func(r *http.Request) (*http.Response, error) {
				var body string
				switch r.URL.String() {
				case "https://github.com/login/device/code":
					body = `{"device_code":"fixture-device","user_code":"ABCD-1234","verification_uri":"https://example.invalid/device","expires_in":900,"interval":1}`
				case "https://github.com/login/oauth/access_token":
					body = `{"access_token":"fixture-github","refresh_token":"fixture-refresh","token_type":"bearer"}`
				case "https://api.github.com/user":
					body = `{"login":"fixture-user","id":1}`
				case "https://api.github.com/copilot_internal/v2/token":
					copilotCalls++
					wantAuth := "token fixture-github"
					if copilotCalls == 1 {
						wantAuth = "Bearer fixture-github"
					}
					if r.Header.Get("Authorization") != wantAuth {
						t.Error("Copilot mint/renewal lost the primary GitHub token")
					}
					if failRenewal {
						return nil, fmt.Errorf("fixture renewal failure")
					}
					expiry := now.Add(time.Hour)
					if copilotCalls == 1 {
						expiry = now.Add(30 * time.Second)
					}
					body = fmt.Sprintf(`{"token":"fixture-copilot-%d","expires_at":%d}`, copilotCalls, expiry.Unix())
				default:
					t.Errorf("unexpected OAuth endpoint %s", r.URL)
					return nil, fmt.Errorf("unexpected OAuth endpoint")
				}
				return &http.Response{StatusCode: 200, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})
			h, mux, creds, cookie := newOnboardingAPI(t, exchange)
			h.opts.Providers = registry.Builtins()
			h.opts.CredentialRegistry = credentials.NewRegistry(creds, func() time.Time { return now })
			h.opts.CredentialRegistry.SetCredentialNameResolver(func(c credentials.Connection) (string, string) {
				spec, _ := h.providerSpec(c.ProviderID)
				return spec.CredentialName, spec.CredentialExpiryName
			})
			resolve, err := oauthrefresh.Resolver(exchange)
			if err != nil {
				t.Fatal(err)
			}
			h.opts.CredentialRegistry.SetExchangerResolver(resolve)
			h.oauth.now = func() time.Time { return now }
			start := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/github/device", `{"name":"Fixture GitHub","identity":"fixture-user"}`)
			var state struct {
				State string `json:"state"`
			}
			if start.Code != 200 || json.Unmarshal(start.Body.Bytes(), &state) != nil || state.State == "" {
				t.Fatalf("device start: %d %s", start.Code, start.Body)
			}
			now = now.Add(2 * time.Second)
			poll := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/github/poll", fmt.Sprintf(`{"state":%q}`, state.State))
			if poll.Code != 200 || !strings.Contains(poll.Body.String(), `"connected"`) {
				t.Fatalf("device poll: %d %s", poll.Code, poll.Body)
			}
			assertNoSecrets(t, poll.Body.String(), "fixture-github", "fixture-refresh", "fixture-copilot", "fixture-device")
			connections, err := creds.ListConnections(context.Background(), "github")
			if err != nil || len(connections) != 1 {
				t.Fatalf("connections: %d, error %v", len(connections), err)
			}
			connection := connections[0]
			if !connection.Secret.Expiry.IsZero() || connection.Secret.AccessToken != "fixture-github" || connection.Secret.RefreshToken != "fixture-refresh" {
				t.Fatal("onboarding changed primary credentials or invented expires_in")
			}
			h.opts.DiscoveryClient = &discovery.Client{HTTP: probeHTTPFunc(func(r *http.Request) (*http.Response, error) {
				wireCalls++
				if copilotCalls != 2 {
					t.Error("wire request preceded required Copilot renewal")
				}
				if r.Header.Get("Authorization") != "Bearer fixture-copilot-2" || r.Header.Get("x-api-key") != "" {
					t.Error("wire request used wrong credential or auth scheme")
				}
				for name, want := range map[string]string{
					"copilot-integration-id": "vscode-chat", "editor-version": "vscode/1.110.0", "editor-plugin-version": "copilot-chat/0.38.0", "User-Agent": "GitHubCopilotChat/0.38.0", "openai-intent": "conversation-panel", "x-github-api-version": "2025-04-01", "x-vscode-user-agent-library-version": "electron-fetch", "X-Initiator": "user",
				} {
					if r.Header.Get(name) != want {
						t.Errorf("missing/incorrect %s", name)
					}
				}
				body := `{"choices":[{"message":{"content":"OK"}}]}`
				switch r.URL.Path {
				case "/models":
					if r.Method != http.MethodGet {
						t.Error("discovery method")
					}
					body = `{"data":[{"id":"gpt-5.2"},{"id":"claude-sonnet-4.6"}]}`
				case "/v1/messages":
					if r.Header.Get("anthropic-version") != "2023-06-01" {
						t.Error("missing Messages version")
					}
					body = `{"type":"message","content":[{"type":"text","text":"OK"}]}`
				case "/responses":
					body = `{"output":[{"type":"message","content":[{"type":"output_text","text":"OK"}]}]}`
				case "/chat/completions":
				default:
					t.Errorf("unexpected wire endpoint %s", r.URL)
				}
				return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
			})}
			discover := func() {
				t.Helper()
				w := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections/"+connection.ID+"/discover", "")
				assertNoSecrets(t, w.Body.String(), "fixture-github", "fixture-refresh", "fixture-copilot", "fixture-device")
				if w.Code != 200 || !strings.Contains(w.Body.String(), "gpt-5.2") {
					t.Fatalf("discovery: %d %s", w.Code, w.Body)
				}
			}
			probe := func() {
				t.Helper()
				for _, protocol := range []string{"openai-chat", "openai-responses", "anthropic-messages"} {
					model := "gpt-5.2"
					if protocol == "anthropic-messages" {
						model = "claude-sonnet-4.6"
					}
					w := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections/"+connection.ID+"/test-models", fmt.Sprintf(`{"transport":%q,"modelIds":[%q]}`, protocol, model))
					assertNoSecrets(t, w.Body.String(), "fixture-github", "fixture-refresh", "fixture-copilot", "fixture-device")
					if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
						t.Fatalf("%s probe: %d %s", protocol, w.Code, w.Body)
					}
				}
			}
			if first == "probe" {
				probe()
				discover()
			} else {
				discover()
				probe()
			}
			if wireCalls != 4 || copilotCalls != 2 {
				t.Fatalf("wire calls %d, token calls %d", wireCalls, copilotCalls)
			}
			stored, err := creds.GetConnection(context.Background(), connection.ID)
			if err != nil || stored.Secret.AccessToken != "fixture-github" || stored.Secret.RefreshToken != "fixture-refresh" || stored.Secret.ProviderData["copilotToken"] != "fixture-copilot-2" {
				t.Fatal("renewal did not durably preserve primary credentials and update Copilot token")
			}
			h.opts.CredentialRegistry.Forget(connection.ID)
			probe()
			discover()
			h.opts.CredentialRegistry.Register(stored, nil)
			now = now.Add(2 * time.Hour)
			failRenewal = true
			w := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections/"+connection.ID+"/discover", "")
			if w.Code == 200 || wireCalls != 8 {
				t.Fatal("failed renewal dispatched a stale credential")
			}
			w = doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections/"+connection.ID+"/test-models", `{"transport":"anthropic-messages","modelIds":["claude-sonnet-4.6"]}`)
			if strings.Contains(w.Body.String(), `"ok":true`) || wireCalls != 8 {
				t.Fatal("failed probe renewal dispatched a stale credential")
			}
		})
	}
}

func TestProbeCookieCredentialKeepsAuthMode(t *testing.T) {
	for _, registered := range []bool{false, true} {
		h := New(Options{})
		c := credentials.Connection{ID: "fixture", AuthKind: credentials.AuthCookie, Secret: credentials.Secret{AccessToken: "fixture-access", Cookie: "fixture-cookie"}}
		if registered {
			h.opts.CredentialRegistry = credentials.NewRegistry(nil, nil)
			h.opts.CredentialRegistry.Register(c, nil)
		}
		token, err := h.probeCredential(context.Background(), c)
		if err != nil || token != "fixture-cookie" {
			t.Fatalf("cookie auth (registered=%v): %v", registered, err)
		}
	}
}
