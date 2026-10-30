package app

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

	"github.com/raufimusaddiq/routeweft/internal/adminauth"
	controlapi "github.com/raufimusaddiq/routeweft/internal/control/api"
	"github.com/raufimusaddiq/routeweft/internal/providers/oauthrefresh"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/store/storetest"
)

type githubSignInClient struct {
	calls        atomic.Int32
	copilotCalls atomic.Int32
}

func (c *githubSignInClient) Do(r *http.Request) (*http.Response, error) {
	c.calls.Add(1)
	var body string
	switch r.URL.String() {
	case "https://github.com/login/device/code":
		body = `{"device_code":"fixture-device","user_code":"ABCD-1234","verification_uri":"https://example.invalid/device","expires_in":900,"interval":1}`
	case "https://github.com/login/oauth/access_token":
		body = `{"access_token":"fixture-github","token_type":"bearer","scope":"read:user"}`
	case "https://api.github.com/user":
		body = `{"login":"fixture-user","id":1}`
	case "https://api.github.com/copilot_internal/v2/token":
		token, expiry := "fixture-copilot", time.Now().Add(30*time.Minute)
		if c.copilotCalls.Add(1) == 1 {
			token, expiry = "fixture-copilot-stale", time.Now().Add(30*time.Second)
		} else if r.Header.Get("Authorization") != "token fixture-github" {
			return nil, fmt.Errorf("Copilot renewal did not use the GitHub token")
		}
		body = fmt.Sprintf(`{"token":%q,"expires_at":%d}`, token, expiry.Unix())
	default:
		return nil, fmt.Errorf("unexpected sign-in request: %s", r.URL)
	}
	return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestGitHubOnboardingServesCopilotCredential(t *testing.T) {
	var upstreamCalls atomic.Int32
	client := &githubSignInClient{}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls.Add(1)
		if client.copilotCalls.Load() != 2 {
			t.Error("inference reached upstream before Copilot renewal")
		}
		if r.Header.Get("Authorization") != "Bearer fixture-copilot" {
			t.Error("inference did not use the Copilot credential")
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.Header.Get("copilot-integration-id") != "vscode-chat" {
			t.Error("missing Copilot fingerprint")
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/chat/completions":
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"fixture answer"},"finish_reason":"stop"}]}`)
		case "/responses":
			fmt.Fprint(w, `{"object":"response","status":"completed","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"fixture answer"}]}]}`)
		case "/v1/messages":
			fmt.Fprint(w, `{"type":"message","role":"assistant","content":[{"type":"text","text":"fixture answer"}],"stop_reason":"end_turn"}`)
		default:
			t.Errorf("unexpected inference path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer upstream.Close()
	ctx := context.Background()
	a := New(Config{DataDir: storetest.DataDirWithAdmin(t, "operator", "fixture-password"), CredentialKey: []byte("01234567890123456789012345678901"), AllowPrivateUpstreams: true, AdminBootstrapUsername: "operator", AdminBootstrapPassword: "fixture-password"}, nil)
	if err := a.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	storeOpen := true
	closeStore := func() {
		t.Helper()
		if !storeOpen {
			return
		}
		if err := a.store.Close(); err != nil {
			t.Fatal(err)
		}
		storeOpen = false
	}
	t.Cleanup(closeStore)
	_, key, err := a.runtime.CreateAPIKey(ctx, "client")
	if err != nil {
		t.Fatal(err)
	}
	specs := registry.Builtins()
	for i := range specs {
		if specs[i].ID == "github" {
			specs[i].DefaultBaseURL = upstream.URL
		}
	}
	// Replace only refresh HTTP; provider selection and renewal use production code.
	resolve, err := oauthrefresh.Resolver(client)
	if err != nil {
		t.Fatal(err)
	}
	a.credReg.SetExchangerResolver(resolve)
	sessions := adminauth.NewSessionManager(time.Hour)
	control := controlapi.New(controlapi.Options{
		Accounts: a.admin, Sessions: sessions, Settings: a.runtime, Keys: a.runtime,
		DB: a.store.DB(), Runtime: a.runtime, Providers: specs, Credentials: a.creds,
		CredentialRegistry: a.credReg, ProviderCatalog: a.runtime, PoolBindings: a.runtime,
		AllowPrivateUpstreams: true, OAuthClient: client,
	})
	mux := http.NewServeMux()
	control.Attach(mux)
	login := httptest.NewRecorder()
	mux.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/admin/v1/auth/login", strings.NewReader(`{"username":"operator","password":"fixture-password"}`)))
	if login.Code != 200 || len(login.Result().Cookies()) == 0 {
		t.Fatalf("login: %d", login.Code)
	}
	post := func(path, body string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		r.AddCookie(login.Result().Cookies()[0])
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		for _, secret := range []string{"fixture-github", "fixture-copilot", "fixture-device"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("control response leaked a credential")
			}
		}
		return w
	}
	start := post("/admin/v1/oauth/github/device", `{"name":"Fixture GitHub","identity":"fixture-user"}`)
	var state struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(start.Body.Bytes(), &state); err != nil {
		t.Fatal(err)
	}
	if state.State == "" {
		t.Fatal("no device state")
	}
	time.Sleep(time.Second) // Device polling must honor the provider interval.
	poll := post("/admin/v1/oauth/github/poll", fmt.Sprintf(`{"state":%q}`, state.State))
	if !strings.Contains(poll.Body.String(), `"connected"`) {
		t.Fatalf("poll: %s", poll.Body)
	}
	if client.calls.Load() != 4 {
		t.Fatalf("sign-in calls: %d", client.calls.Load())
	}
	connections, err := a.creds.ListConnections(ctx, "github")
	if err != nil || len(connections) != 1 {
		t.Fatalf("onboarded connections: %d, error: %v", len(connections), err)
	}
	if !connections[0].Secret.Expiry.IsZero() {
		t.Fatal("GitHub token response must not supply expires_in")
	}
	if client.copilotCalls.Load() != 1 {
		t.Fatal("onboarding did not mint the first Copilot token")
	}
	assertServes := func() {
		t.Helper()
		for _, tc := range []struct{ path, body string }{
			{"/v1/chat/completions", `{"model":"github/gpt-5.2","messages":[{"role":"user","content":"hi"}]}`},
			{"/v1/responses", `{"model":"github/gpt-5.2","input":"hi"}`},
			{"/v1/messages", `{"model":"github/claude-sonnet-4.6","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`},
		} {
			w := serveRequest(a.Handler(), key, tc.path, tc.body)
			if w.Code != 200 || !strings.Contains(w.Body.String(), "fixture answer") {
				t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body)
			}
		}
	}
	assertServes()
	if client.copilotCalls.Load() != 2 || client.calls.Load() != 5 {
		t.Fatal("near-expiry Copilot token was not re-minted exactly once before serving")
	}
	closeStore()
	if err := a.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	storeOpen = true
	// Closing SQLite proves restored routing and credential selection use memory.
	closeStore()
	assertServes()
	if upstreamCalls.Load() != 6 {
		t.Fatalf("inference calls: %d", upstreamCalls.Load())
	}
}
