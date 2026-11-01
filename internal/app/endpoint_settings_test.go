package app

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/store/storetest"
)

// #228: connection settings that choose a host or a URL shape (Azure's endpoint, deployment and
// API version, local Ollama's host and optional key, Kimchi's catalog host) are saved, edited and
// restored after a restart, and they change the URL the request or the model listing really
// uses. The upstreams are in-process servers; nothing leaves the process.

type seenRequest struct {
	Method, Path, Query, Authorization, APIKey, Organization, UserAgent string
}

type recordingUpstream struct {
	*httptest.Server
	mu   sync.Mutex
	seen []seenRequest
}

func newRecordingUpstream(t *testing.T, answer func(w http.ResponseWriter, r *http.Request)) *recordingUpstream {
	t.Helper()
	u := &recordingUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		u.mu.Lock()
		u.seen = append(u.seen, seenRequest{r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization"), r.Header.Get("api-key"), r.Header.Get("OpenAI-Organization"), r.Header.Get("User-Agent")})
		u.mu.Unlock()
		answer(w, r)
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *recordingUpstream) requests() []seenRequest {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]seenRequest(nil), u.seen...)
}

func (u *recordingUpstream) last(t *testing.T) seenRequest {
	t.Helper()
	all := u.requests()
	if len(all) == 0 {
		t.Fatal("the upstream saw no request")
	}
	return all[len(all)-1]
}

// endpointApp starts the real app with an operator session and a client key.
type endpointApp struct {
	t       *testing.T
	a       *App
	key     string
	cookie  *http.Cookie
	private bool
}

func newEndpointApp(t *testing.T, allowPrivate bool) *endpointApp {
	t.Helper()
	a := New(Config{DataDir: storetest.DataDir(t), CredentialKey: []byte("01234567890123456789012345678901"), AllowPrivateUpstreams: allowPrivate}, nil)
	ctx := context.Background()
	if err := a.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.store.Close() })
	if _, err := a.store.DB().Exec("INSERT INTO admin_users (id,username,password_hash) VALUES (?,?,?)", "endpoint-admin", "operator", storetest.AdminHash(c03Password)); err != nil {
		t.Fatal(err)
	}
	_, key, err := a.runtime.CreateAPIKey(ctx, "client")
	if err != nil {
		t.Fatal(err)
	}
	e := &endpointApp{t: t, a: a, key: key, private: allowPrivate}
	e.restart()
	return e
}

// restart rebuilds the app from what was saved, as a process restart does, and signs in again.
func (e *endpointApp) restart() {
	e.t.Helper()
	if err := e.a.store.Close(); err != nil {
		e.t.Fatal(err)
	}
	if err := e.a.Initialize(context.Background()); err != nil {
		e.t.Fatal(err)
	}
	e.cookie = c03Login(e.t, e.a)
}

func (e *endpointApp) admin(method, path, body string) (int, string) {
	e.t.Helper()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(e.cookie)
	r.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	e.a.Handler().ServeHTTP(w, r)
	return w.Code, w.Body.String()
}

func (e *endpointApp) mustAdmin(method, path, body string) string {
	e.t.Helper()
	code, out := e.admin(method, path, body)
	if code != http.StatusOK {
		e.t.Fatalf("%s %s: %d %s", method, path, code, out)
	}
	return out
}

func (e *endpointApp) chat(model string) *httptest.ResponseRecorder {
	e.t.Helper()
	return serveRequest(e.a.Handler(), e.key, "/v1/chat/completions", fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, model))
}

func connectionID(t *testing.T, body string) string {
	t.Helper()
	const marker = `"id":"`
	i := strings.Index(body, marker)
	if i < 0 {
		t.Fatalf("no connection id in %s", body)
	}
	rest := body[i+len(marker):]
	return rest[:strings.Index(rest, `"`)]
}

func TestAzureSettingsSurviveEditAndRestartAndSteerTheRequest(t *testing.T) {
	upstream := newRecordingUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"azure answer"},"finish_reason":"stop"}]}`)
	})
	e := newEndpointApp(t, true)
	e.mustAdmin(http.MethodPost, "/admin/v1/models", `{"providerId":"azure","id":"gpt-4o"}`)
	saved := e.mustAdmin(http.MethodPost, "/admin/v1/connections", fmt.Sprintf(`{"providerId":"azure","name":"Azure","identity":"azure-1","authKind":"api-key","enabled":true,"secret":{"accessToken":"azure-secret-key","providerData":{"azureEndpoint":%q,"deployment":"dep-a","apiVersion":"2025-01-01","organization":"org-1"}}}`, upstream.URL))
	id := connectionID(t, saved)
	if strings.Contains(saved, "azure-secret-key") {
		t.Fatal("the save response carries the key")
	}

	if w := e.chat("azure/gpt-4o"); w.Code != http.StatusOK {
		t.Fatalf("request: %d %s", w.Code, w.Body)
	}
	got := upstream.last(t)
	if got.Path != "/openai/deployments/dep-a/chat/completions" || got.Query != "api-version=2025-01-01" || got.APIKey != "azure-secret-key" || got.Organization != "org-1" || got.Authorization != "" {
		t.Fatalf("azure request = %+v", got)
	}

	// The connection list shows the saved settings (the edit form reads them) and never the key.
	listed := e.mustAdmin(http.MethodGet, "/admin/v1/connections?provider=azure", "")
	if !strings.Contains(listed, `"azureEndpoint":"`+upstream.URL+`"`) || !strings.Contains(listed, `"deployment":"dep-a"`) || !strings.Contains(listed, `"apiVersion":"2025-01-01"`) || !strings.Contains(listed, `"organization":"org-1"`) {
		t.Fatalf("the list does not show the saved settings: %s", listed)
	}
	if strings.Contains(listed, "azure-secret-key") {
		t.Fatalf("the list carries the key: %s", listed)
	}

	// An edit that names only some settings keeps the others, and changes the next request.
	e.mustAdmin(http.MethodPatch, "/admin/v1/connections/"+id, `{"secret":{"providerData":{"deployment":"dep-b","apiVersion":"2025-02-01"}}}`)
	if w := e.chat("azure/gpt-4o"); w.Code != http.StatusOK {
		t.Fatalf("request after edit: %d %s", w.Code, w.Body)
	}
	if got := upstream.last(t); got.Path != "/openai/deployments/dep-b/chat/completions" || got.Query != "api-version=2025-02-01" || got.Organization != "org-1" || got.APIKey != "azure-secret-key" {
		t.Fatalf("edited azure request = %+v", got)
	}

	// And a restart rebuilds the same request from what was saved.
	e.restart()
	if w := e.chat("azure/gpt-4o"); w.Code != http.StatusOK {
		t.Fatalf("request after restart: %d %s", w.Code, w.Body)
	}
	if got := upstream.last(t); got.Path != "/openai/deployments/dep-b/chat/completions" || got.Query != "api-version=2025-02-01" || got.Organization != "org-1" {
		t.Fatalf("restarted azure request = %+v", got)
	}
}

func TestLocalOllamaDialsTheConnectionsHostWithItsOptionalKeyOnlyWhenTrusted(t *testing.T) {
	answer := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprint(w, `{"model":"llama3.2","message":{"role":"assistant","content":"local answer"},"done":true,"done_reason":"stop"}`+"\n")
	}
	// Trusted-local mode on: the host the connection names is the host dialled, with and without a key.
	upstream := newRecordingUpstream(t, answer)
	e := newEndpointApp(t, true)
	e.mustAdmin(http.MethodPost, "/admin/v1/models", `{"providerId":"ollama-local","id":"llama3.2"}`)
	keyless := connectionID(t, e.mustAdmin(http.MethodPost, "/admin/v1/connections", fmt.Sprintf(`{"providerId":"ollama-local","name":"Local","identity":"local","authKind":"none","enabled":true,"secret":{"providerData":{"baseUrl":%q}}}`, upstream.URL+"/")))
	if w := e.chat("ollama-local/llama3.2"); w.Code != http.StatusOK {
		t.Fatalf("keyless request: %d %s", w.Code, w.Body)
	}
	if got := upstream.last(t); got.Path != "/api/chat" || got.Authorization != "" {
		t.Fatalf("keyless local request = %+v", got)
	}

	// The same connection takes an optional key (a reverse proxy in front of Ollama) and sends it.
	e.mustAdmin(http.MethodPatch, "/admin/v1/connections/"+keyless, `{"authKind":"api-key","secret":{"accessToken":"local-proxy-key"}}`)
	e.restart()
	if w := e.chat("ollama-local/llama3.2"); w.Code != http.StatusOK {
		t.Fatalf("keyed request after restart: %d %s", w.Code, w.Body)
	}
	if got := upstream.last(t); got.Path != "/api/chat" || got.Authorization != "Bearer local-proxy-key" {
		t.Fatalf("keyed local request = %+v", got)
	}

	// Without the operator's trusted-local mode the same address can be saved but is never dialled.
	closed := newRecordingUpstream(t, answer)
	locked := newEndpointApp(t, false)
	locked.mustAdmin(http.MethodPost, "/admin/v1/models", `{"providerId":"ollama-local","id":"llama3.2"}`)
	locked.mustAdmin(http.MethodPost, "/admin/v1/connections", fmt.Sprintf(`{"providerId":"ollama-local","name":"Local","identity":"local","authKind":"none","enabled":true,"secret":{"providerData":{"baseUrl":%q}}}`, closed.URL))
	if w := locked.chat("ollama-local/llama3.2"); w.Code == http.StatusOK {
		t.Fatalf("a private host was dialled without trusted-local mode: %s", w.Body)
	}
	if n := len(closed.requests()); n != 0 {
		t.Fatalf("the private upstream saw %d requests without trusted-local mode", n)
	}
}

func TestKimchiEndpointChoosesTheCatalogHost(t *testing.T) {
	upstream := newRecordingUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"models":[{"slug":"kimchi-model-1","display_name":"Kimchi One","limits":{"context_window":65536}}]}`)
	})
	e := newEndpointApp(t, true)
	id := connectionID(t, e.mustAdmin(http.MethodPost, "/admin/v1/connections", fmt.Sprintf(`{"providerId":"kimchi","name":"Kimchi","identity":"kimchi-1","authKind":"api-key","enabled":true,"secret":{"accessToken":"kimchi-key","providerData":{"kimchiEndpoint":%q}}}`, upstream.URL+"//")))
	body := e.mustAdmin(http.MethodPost, "/admin/v1/connections/"+id+"/discover", `{}`)
	if !strings.Contains(body, "kimchi-model-1") {
		t.Fatalf("discovery did not list the endpoint's models: %s", body)
	}
	got := upstream.last(t)
	if got.Path != "/v1/models/metadata" || got.Query != "include_in_cli=true" || got.Authorization != "Bearer kimchi-key" || got.UserAgent != "kimchi/0.1.40" {
		t.Fatalf("kimchi listing request = %+v", got)
	}

	// The setting is durable and editable: a restart keeps it, and clearing it falls back to the default host.
	e.restart()
	e.mustAdmin(http.MethodPost, "/admin/v1/connections/"+id+"/discover", `{}`)
	if n := len(upstream.requests()); n != 2 {
		t.Fatalf("after a restart the endpoint saw %d listings, want 2", n)
	}
}

// The Cloudflare account ID is a connection setting that scopes both chat and
// System One URLs (registry/cloudflare-ai.js:26,30). It survives an edit and a
// restart, and the next request uses the new account.
func TestCloudflareAccountSurvivesRestartAndScopesTheRequest(t *testing.T) {
	upstream := newRecordingUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"cf answer"},"finish_reason":"stop"}]}`)
	})
	e := newEndpointApp(t, true)
	node, err := e.a.creds.PutNode(context.Background(), credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: "cloudflare-ai", Name: "Cloudflare", BaseURL: upstream.URL + "/accounts", Transports: []string{"openai-chat"}})
	if err != nil {
		t.Fatal(err)
	}
	e.mustAdmin(http.MethodPost, "/admin/v1/models", `{"providerId":"cloudflare-ai","id":"@cf/meta/llama-3.2-3b-instruct"}`)
	saved := e.mustAdmin(http.MethodPost, "/admin/v1/connections", fmt.Sprintf(`{"nodeId":%q,"name":"CF","identity":"cf","authKind":"api-key","enabled":true,"accountId":"account-a","secret":{"accessToken":"cf-key"}}`, node.ID))
	id := connectionID(t, saved)
	if strings.Contains(saved, "cf-key") || !strings.Contains(saved, `"accountId":"account-a"`) {
		t.Fatalf("the save response exposes the key or hides the account: %s", saved)
	}
	assertAccount := func(want string) {
		t.Helper()
		if w := e.chat("cloudflare-ai/@cf/meta/llama-3.2-3b-instruct"); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "cf answer") {
			t.Fatalf("request: %d %s", w.Code, w.Body)
		}
		got := upstream.last(t)
		if got.Path != "/accounts/"+want+"/ai/v1/chat/completions" || got.Authorization != "Bearer cf-key" {
			t.Fatalf("cloudflare request = %+v", got)
		}
	}
	assertAccount("account-a")

	// The edit changes the path the next request uses; the restart keeps it.
	e.mustAdmin(http.MethodPatch, "/admin/v1/connections/"+id, `{"accountId":"account-b"}`)
	assertAccount("account-b")
	e.restart()
	assertAccount("account-b")
	if listed := e.mustAdmin(http.MethodGet, "/admin/v1/connections?provider=cloudflare-ai", ""); !strings.Contains(listed, `"accountId":"account-b"`) {
		t.Fatalf("the list lost the account: %s", listed)
	}
}

func TestEndpointSettingsAreRefusedWhenTheyCouldNeverBeDialled(t *testing.T) {
	cases := []struct {
		name, provider, data string
		trusted              bool
	}{
		{"kimchi private host", "kimchi", `"kimchiEndpoint":"http://127.0.0.1:9"`, false},
		{"kimchi private literal", "kimchi", `"kimchiEndpoint":"http://10.0.0.8"`, false},
		{"kimchi not http", "kimchi", `"kimchiEndpoint":"file:///etc/passwd"`, true},
		{"kimchi userinfo", "kimchi", `"kimchiEndpoint":"https://user:pass@llm.example"`, true},
		{"kimchi query", "kimchi", `"kimchiEndpoint":"https://llm.example/?a=b"`, true},
		{"azure private host", "azure", `"azureEndpoint":"http://192.168.1.5"`, false},
		{"azure metadata address even when trusted", "azure", `"azureEndpoint":"http://169.254.169.254"`, true},
		{"azure api version", "azure", `"azureEndpoint":"https://res.openai.azure.com","apiVersion":"2024&x=1"`, false},
		{"azure deployment traversal", "azure", `"azureEndpoint":"https://res.openai.azure.com","deployment":"a/../b"`, false},
		{"azure deployment dots", "azure", `"azureEndpoint":"https://res.openai.azure.com","deployment":"a..b"`, false},
		{"azure organization lines", "azure", `"azureEndpoint":"https://res.openai.azure.com","organization":"a\r\nX-Evil: 1"`, false},
		{"ollama-local metadata address even when trusted", "ollama-local", `"baseUrl":"http://169.254.169.254"`, true},
		{"ollama-local userinfo", "ollama-local", `"baseUrl":"http://user:pass@localhost:11434"`, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newEndpointApp(t, c.trusted)
			code, out := e.admin(http.MethodPost, "/admin/v1/connections", fmt.Sprintf(`{"providerId":%q,"name":"X","identity":"x","authKind":"api-key","enabled":true,"secret":{"accessToken":"key-material","providerData":{%s}}}`, c.provider, c.data))
			if code != http.StatusBadRequest || !strings.Contains(out, "invalid_connection_setting") {
				t.Fatalf("%d %s", code, out)
			}
			if strings.Contains(out, "key-material") || strings.Contains(out, "pass@") {
				t.Fatalf("the refusal echoes a secret: %s", out)
			}
		})
	}

	// A good value is saved, and edits are checked too.
	e := newEndpointApp(t, false)
	id := connectionID(t, e.mustAdmin(http.MethodPost, "/admin/v1/connections", `{"providerId":"azure","name":"A","identity":"a","authKind":"api-key","enabled":true,"secret":{"accessToken":"k","providerData":{"azureEndpoint":"https://res.openai.azure.com","deployment":"dep","apiVersion":"2024-10-01-preview"}}}`))
	if code, out := e.admin(http.MethodPatch, "/admin/v1/connections/"+id, `{"secret":{"providerData":{"azureEndpoint":"http://127.0.0.1:1"}}}`); code != http.StatusBadRequest {
		t.Fatalf("an edit to a private endpoint: %d %s", code, out)
	}
}

// A host a setting names cannot send the router somewhere else: the clients that dial it do not
// follow a redirect (internal/security TestProtectedClientsDoNotFollowRedirects), so the redirect's
// target, even a perfectly reachable one, never sees the request or the credential.
func TestARedirectFromANamedHostIsNotFollowed(t *testing.T) {
	target := newRecordingUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"models":[{"slug":"smuggled"}],"choices":[{"message":{"role":"assistant","content":"smuggled"},"finish_reason":"stop"}]}`)
	})
	redirecting := newRecordingUpstream(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+r.URL.Path, http.StatusFound)
	})
	e := newEndpointApp(t, true)

	id := connectionID(t, e.mustAdmin(http.MethodPost, "/admin/v1/connections", fmt.Sprintf(`{"providerId":"kimchi","name":"Kimchi","identity":"k","authKind":"api-key","enabled":true,"secret":{"accessToken":"kimchi-key","providerData":{"kimchiEndpoint":%q}}}`, redirecting.URL)))
	code, body := e.admin(http.MethodPost, "/admin/v1/connections/"+id+"/discover", `{}`)
	if code == http.StatusOK && strings.Contains(body, "smuggled") {
		t.Fatalf("the listing followed a redirect: %s", body)
	}
	e.mustAdmin(http.MethodPost, "/admin/v1/models", `{"providerId":"azure","id":"gpt-4o"}`)
	e.mustAdmin(http.MethodPost, "/admin/v1/connections", fmt.Sprintf(`{"providerId":"azure","name":"Azure","identity":"a","authKind":"api-key","enabled":true,"secret":{"accessToken":"azure-secret-key","providerData":{"azureEndpoint":%q,"deployment":"d"}}}`, redirecting.URL))
	if w := e.chat("azure/gpt-4o"); w.Code == http.StatusOK && strings.Contains(w.Body.String(), "smuggled") {
		t.Fatalf("the request followed a redirect: %s", w.Body)
	}
	if n := len(target.requests()); n != 0 {
		t.Fatalf("the redirect target saw %d requests: %+v", n, target.requests())
	}
	if n := len(redirecting.requests()); n < 2 {
		t.Fatalf("the named host saw %d requests, want the listing and the chat", n)
	}
}

// An edit is checked as it will be stored: the edit laid over the saved settings, an empty value
// replacing the saved one. The form blocks an empty required field; the API must too, because a
// connection with no Azure endpoint is silently unselectable and its probes fail (Hermes round 2 on #331).
func TestTheAPIRefusesToClearARequiredSetting(t *testing.T) {
	e := newEndpointApp(t, false)
	refused := func(method, path, body, want string) {
		t.Helper()
		code, out := e.admin(method, path, body)
		if code != http.StatusBadRequest || !strings.Contains(out, "invalid_connection_setting") || !strings.Contains(out, want) {
			t.Fatalf("%s %s %s: %d %s", method, path, body, code, out)
		}
	}
	// A create without the endpoint is refused, as the form refuses it.
	refused(http.MethodPost, "/admin/v1/connections", `{"providerId":"azure","name":"A","identity":"a","authKind":"api-key","enabled":true,"secret":{"accessToken":"k","providerData":{"deployment":"d"}}}`, "azureEndpoint is required")
	refused(http.MethodPost, "/admin/v1/connections", `{"providerId":"azure","name":"A","identity":"a","authKind":"api-key","enabled":true,"secret":{"accessToken":"k"}}`, "azureEndpoint is required")

	id := connectionID(t, e.mustAdmin(http.MethodPost, "/admin/v1/connections", `{"providerId":"azure","name":"A","identity":"a","authKind":"api-key","enabled":true,"secret":{"accessToken":"k","providerData":{"azureEndpoint":"https://res.openai.azure.com","deployment":"d"}}}`))
	path := "/admin/v1/connections/" + id
	// Clearing it, by an empty value or by blanks, is refused and leaves the saved endpoint alone.
	refused(http.MethodPatch, path, `{"secret":{"providerData":{"azureEndpoint":""}}}`, "azureEndpoint is required")
	refused(http.MethodPatch, path, `{"secret":{"providerData":{"azureEndpoint":"   "}}}`, "azureEndpoint is required")
	if listed := e.mustAdmin(http.MethodGet, "/admin/v1/connections?provider=azure", ""); !strings.Contains(listed, `"azureEndpoint":"https://res.openai.azure.com"`) {
		t.Fatalf("a refused edit changed the saved endpoint: %s", listed)
	}
	// An edit of another setting, or none, still works, and keeps the endpoint.
	e.mustAdmin(http.MethodPatch, path, `{"secret":{"providerData":{"deployment":"d2"}}}`)
	e.mustAdmin(http.MethodPatch, path, `{"name":"Renamed"}`)
	if listed := e.mustAdmin(http.MethodGet, "/admin/v1/connections?provider=azure", ""); !strings.Contains(listed, `"azureEndpoint":"https://res.openai.azure.com"`) || !strings.Contains(listed, `"deployment":"d2"`) || !strings.Contains(listed, "Renamed") {
		t.Fatalf("edits lost a setting: %s", listed)
	}
	// An optional setting can be cleared (kimchi falls back to its default host).
	kimchi := connectionID(t, e.mustAdmin(http.MethodPost, "/admin/v1/connections", `{"providerId":"kimchi","name":"K","identity":"k","authKind":"api-key","enabled":true,"secret":{"accessToken":"k","providerData":{"kimchiEndpoint":"https://kimchi.example.test"}}}`))
	e.mustAdmin(http.MethodPatch, "/admin/v1/connections/"+kimchi, `{"secret":{"providerData":{"kimchiEndpoint":""}}}`)

	// A connection saved before this check, with no endpoint, can still be renamed; touching its
	// settings asks for the endpoint, since the edit would leave it unusable.
	legacy, err := e.a.creds.GetConnection(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	legacy.Secret.ProviderData = map[string]string{"deployment": "d2"}
	if _, err := e.a.creds.PutConnection(context.Background(), legacy); err != nil {
		t.Fatal(err)
	}
	e.mustAdmin(http.MethodPatch, path, `{"name":"Renamed again"}`)
	refused(http.MethodPatch, path, `{"secret":{"providerData":{"deployment":"d3"}}}`, "azureEndpoint is required")
	e.mustAdmin(http.MethodPatch, path, `{"secret":{"providerData":{"azureEndpoint":"https://res.openai.azure.com"}}}`)
}
