package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/adminauth"
	controlapi "github.com/raufimusaddiq/routeweft/internal/control/api"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

type metadataSignInClient struct {
	t     *testing.T
	calls int
}

func (c *metadataSignInClient) Do(r *http.Request) (*http.Response, error) {
	c.calls++
	if r.URL.String() != "https://mimo.fixture.invalid/v1/models" || r.Header.Get("Authorization") != "Bearer sk-fixture-key" {
		c.t.Fatalf("unexpected onboarding request: %s", r.URL)
	}
	return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[]}`))}, nil
}

func TestPublishedConnectionMetadataSurvivesRestartWithoutDatabase(t *testing.T) {
	ctx := context.Background()
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-fixture-rotated-2" {
			t.Error("wrong inference credential")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"fixture answer"},"finish_reason":"stop"}]}`)
	}))
	defer upstream.Close()
	a := New(Config{DataDir: t.TempDir(), CredentialKey: []byte("01234567890123456789012345678901"), AllowPrivateUpstreams: true, AdminBootstrapUsername: "operator", AdminBootstrapPassword: "fixture-password"}, nil)
	if err := a.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	open := true
	closeDB := func() {
		t.Helper()
		if open {
			if err := a.store.Close(); err != nil {
				t.Fatal(err)
			}
			open = false
		}
	}
	t.Cleanup(closeDB)
	_, key, err := a.runtime.CreateAPIKey(ctx, "fixture-client")
	if err != nil {
		t.Fatal(err)
	}
	specs := registry.Builtins()
	for i := range specs {
		if specs[i].ID == "xiaomi-mimo" {
			specs[i].DefaultBaseURL = upstream.URL
		}
	}
	client := &metadataSignInClient{t: t}
	control := controlapi.New(controlapi.Options{Accounts: a.admin, Sessions: adminauth.NewSessionManager(time.Hour), Settings: a.runtime, Keys: a.runtime, DB: a.store.DB(), Runtime: a.runtime, Providers: specs, Credentials: a.creds, CredentialRegistry: a.credReg, ProviderCatalog: a.runtime, PoolBindings: a.runtime, AllowPrivateUpstreams: true, OAuthClient: client})
	mux := http.NewServeMux()
	control.Attach(mux)
	login := httptest.NewRecorder()
	mux.ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/admin/v1/auth/login", strings.NewReader(`{"username":"operator","password":"fixture-password"}`)))
	if login.Code != 200 || len(login.Result().Cookies()) == 0 {
		t.Fatalf("login: %d", login.Code)
	}
	request := func(method, path, body string) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(login.Result().Cookies()[0])
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
		for _, secret := range []string{"sk-fixture", "fixture-client-secret"} {
			if strings.Contains(w.Body.String(), secret) {
				t.Fatal("control response leaked secret")
			}
		}
	}
	request(http.MethodPost, "/admin/v1/oauth/xiaomi-mimo/import", `{"name":"Fixture Xiaomi","identity":"fixture-user","input":"sk-fixture-key","meta":{"baseUrl":"https://mimo.fixture.invalid/v1","uid":"fixture-user"}}`)
	if client.calls != 1 {
		t.Fatalf("import calls: %d", client.calls)
	}
	connections, err := a.creds.ListConnections(ctx, "xiaomi-mimo")
	if err != nil || len(connections) != 1 {
		t.Fatalf("connections: %d, %v", len(connections), err)
	}
	c := connections[0]
	// The auth method is routing metadata; the API key must stay sealed.
	if c.Secret.ProviderData["apiKey"] != "sk-fixture-key" || c.Secret.ProviderData["authMethod"] != "api_key" {
		t.Fatal("fixture did not use the real import payload")
	}
	want := routing.ConnectionMetadata{NodeID: c.NodeID, NodeKind: "builtin", Name: c.Name, AuthKind: string(c.AuthKind), Identity: c.Identity, Settings: routing.ProviderSettings{BaseURL: "https://mimo.fixture.invalid/v1", UID: "fixture-user", AuthMethod: "api_key"}}
	snapshot, _ := a.runtime.Load()
	original, ok := snapshot.AccountProvider("xiaomi-mimo/mimo-v2-flash", c.ID, "openai-chat", nil)
	if !ok {
		t.Fatal("published connection not selectable")
	}
	// Node labels come from the actual catalog, not a test-supplied snapshot.
	node, err := a.creds.GetNode(ctx, c.NodeID)
	if err != nil {
		t.Fatal(err)
	}
	want.NodeName = node.Name
	if original.Connection != want {
		t.Fatalf("published metadata: %+v want %+v", original.Connection, want)
	}
	request(http.MethodPatch, "/admin/v1/connections/"+c.ID, `{"secret":{"providerData":{"baseUrl":"https://changed.fixture.invalid/v1"}}}`)
	want.Settings.BaseURL = "https://changed.fixture.invalid/v1"
	current, _ := a.runtime.Load()
	updated, ok := current.AccountProvider("xiaomi-mimo/mimo-v2-flash", c.ID, "openai-chat", nil)
	if !ok || updated.Connection != want {
		t.Fatalf("patched metadata: %+v", updated.Connection)
	}
	old, _ := snapshot.AccountProvider("xiaomi-mimo/mimo-v2-flash", c.ID, "openai-chat", nil)
	if old != original {
		t.Fatal("publication mutated an older snapshot")
	}
	for _, rotation := range []struct{ body, token, uid string }{
		{`{"secret":{"accessToken":"sk-fixture-rotated"}}`, "sk-fixture-rotated", "fixture-user"},
		{`{"secret":{"accessToken":"sk-fixture-rotated-2","providerData":{"uid":"rotated-user"}}}`, "sk-fixture-rotated-2", "rotated-user"},
	} {
		request(http.MethodPatch, "/admin/v1/connections/"+c.ID, rotation.body)
		want.Settings.UID = rotation.uid
		published, err := a.runtime.Load()
		if err != nil {
			t.Fatal(err)
		}
		ref, ok := published.AccountProvider("xiaomi-mimo/mimo-v2-flash", c.ID, "openai-chat", nil)
		if !ok || ref.Connection != want {
			t.Fatalf("token rotation dropped compiled metadata: %+v", ref.Connection)
		}
		durable, err := a.creds.GetConnection(ctx, c.ID)
		if err != nil || durable.Secret.AccessToken != rotation.token || durable.Secret.ProviderData["baseUrl"] != want.Settings.BaseURL || durable.Secret.ProviderData["uid"] != rotation.uid {
			t.Fatalf("rotation lost durable token or metadata: %v", err)
		}
	}
	request(http.MethodPost, "/admin/v1/models", `{"providerId":"ollama-local","id":"llama3.2","name":"Llama 3.2"}`)
	request(http.MethodPost, "/admin/v1/connections", `{"providerId":"ollama-local","name":"Local","identity":"local","authKind":"none","enabled":true,"secret":{"providerData":{"baseUrl":"http://localhost:11435"}}}`)
	locals, err := a.creds.ListConnections(ctx, "ollama-local")
	if err != nil || len(locals) != 1 {
		t.Fatalf("local connection: %v", err)
	}
	if locals[0].Secret.ProviderData["baseUrl"] != "http://localhost:11435" {
		t.Fatal("no-auth metadata not sealed durably")
	}
	request(http.MethodPatch, "/admin/v1/connections/"+locals[0].ID, `{"secret":{"providerData":{"baseUrl":"http://localhost:11436"}}}`)
	closeDB()
	if err := a.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	open = true
	closeDB()
	restored, _ := a.runtime.Load()
	got, ok := restored.AccountProvider("xiaomi-mimo/mimo-v2-flash", c.ID, "openai-chat", nil)
	if !ok || got.Connection != want {
		t.Fatalf("restored metadata: %+v", got.Connection)
	}
	local, ok := restored.AccountProvider("ollama-local/llama3.2", locals[0].ID, "ollama", nil)
	if !ok || local.Connection.Settings.BaseURL != "http://localhost:11436" || local.Connection.AuthKind != "none" {
		t.Fatalf("restored local metadata: %+v selectable=%v", local.Connection, ok)
	}
	got.Connection.Settings.BaseURL = "mutated"
	again, _ := restored.AccountProvider("xiaomi-mimo/mimo-v2-flash", c.ID, "openai-chat", nil)
	if again.Connection != want {
		t.Fatal("returned metadata mutated snapshot")
	}
	w := serveRequest(a.Handler(), key, "/v1/chat/completions", `{"model":"xiaomi-mimo/mimo-v2-flash","messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "fixture answer") {
		t.Fatalf("inference after closing SQLite: %d %s", w.Code, w.Body)
	}
}
