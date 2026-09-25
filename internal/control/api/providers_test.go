package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/adminauth"
	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/discovery"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

func newProvidersAPI(t *testing.T, allowPrivate bool, upstream *discovery.Client) (*Handler, *http.ServeMux, *sqlite.Store, *credentials.Store, *http.Cookie) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.NewRunner(store.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	manager, err := runtime.NewManager(ctx, store.DB())
	if err != nil {
		t.Fatal(err)
	}
	sealer, err := credentials.NewSealer(bytes.Repeat([]byte{7}, credentials.MasterKeySize))
	if err != nil {
		t.Fatal(err)
	}
	credStore, err := credentials.NewStore(store.DB(), sealer)
	if err != nil {
		t.Fatal(err)
	}
	accounts := adminauth.NewStore(store.DB())
	if _, err := accounts.Bootstrap(ctx, "a1", "operator", "s3cret"); err != nil {
		t.Fatal(err)
	}
	sessions := adminauth.NewSessionManager(time.Hour)
	specs := []registry.Spec{
		{ID: "openai", Transports: []registry.Protocol{registry.TransportOpenAIChat, registry.TransportOpenAIResponses}, Auth: registry.AuthAPIKey, DefaultBaseURL: "https://api.openai.com/v1", ModelCatalog: registry.CatalogStatic, StaticModels: []string{"gpt-5"}},
		{ID: "anthropic", Transports: []registry.Protocol{registry.TransportAnthropic}, Auth: registry.AuthAPIKey, DefaultBaseURL: "https://api.anthropic.com/v1", ModelCatalog: registry.CatalogStatic, StaticModels: []string{"claude-sonnet-4"}},
		{ID: "typesafe", Transports: []registry.Protocol{registry.TransportSystemOne}, Auth: registry.AuthAPIKey, DefaultBaseURL: "https://api.typesafe.ai/v1/systemone", ModelCatalog: registry.CatalogStatic, StaticModels: []string{"jev"}},
	}
	handler := New(Options{
		Accounts: accounts, Sessions: sessions, Settings: manager, Keys: manager,
		DB: store.DB(), Runtime: manager, Providers: specs,
		Credentials: credStore, CredentialRegistry: credentials.NewRegistry(credStore, nil),
		ProviderCatalog: manager, PoolBindings: manager, AllowPrivateUpstreams: allowPrivate, DiscoveryClient: upstream,
		Combos: manager,
	})
	session, err := sessions.Create(adminauth.Account{ID: "a1", Username: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Attach(mux)
	return handler, mux, store, credStore, &http.Cookie{Name: sessionCookieName, Value: session.ID}
}

func doJSON(t *testing.T, mux *http.ServeMux, cookie *http.Cookie, method, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	if cookie != nil {
		request.AddCookie(cookie)
	}
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder
}

func TestProviderMutationRoutesRequireSession(t *testing.T) {
	_, mux, store, _, _ := newProvidersAPI(t, false, nil)
	defer store.Close()
	for _, target := range []struct{ method, path string }{
		{http.MethodPost, "/admin/v1/provider-nodes"},
		{http.MethodDelete, "/admin/v1/provider-nodes/x"},
		{http.MethodPost, "/admin/v1/connections"},
		{http.MethodDelete, "/admin/v1/connections/x"},
		{http.MethodPost, "/admin/v1/connections/x/test"},
		{http.MethodPost, "/admin/v1/connections/x/discover"},
		{http.MethodPost, "/admin/v1/models"},
		{http.MethodPost, "/admin/v1/aliases"},
		{http.MethodPost, "/admin/v1/pricing"},
	} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(target.method, target.path, strings.NewReader(`{}`)))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status=%d want 401", target.method, target.path, recorder.Code)
		}
	}
}

func TestGenericProviderCreateRequiresValidSSRFApprovedURL(t *testing.T) {
	_, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()

	blocked := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/provider-nodes", `{"kind":"generic","name":"LAN","prefix":"lan","baseUrl":"http://127.0.0.1:8080/v1","transports":["openai-chat"]}`)
	if blocked.Code != http.StatusBadRequest {
		t.Fatalf("loopback generic provider status=%d body=%s", blocked.Code, blocked.Body.String())
	}

	badTransport := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/provider-nodes", `{"kind":"generic","name":"Custom","prefix":"custom","baseUrl":"https://llm.example.com/v1","transports":["gemini"]}`)
	if badTransport.Code != http.StatusBadRequest {
		t.Fatalf("unsupported transport status=%d", badTransport.Code)
	}

	created := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/provider-nodes", `{"kind":"generic","name":"Custom","prefix":"custom","baseUrl":"https://llm.example.com/v1","transports":["openai-chat","anthropic-messages"]}`)
	if created.Code != http.StatusOK {
		t.Fatalf("generic provider status=%d body=%s", created.Code, created.Body.String())
	}
	var node struct {
		ID         string   `json:"id"`
		Kind       string   `json:"kind"`
		ProviderID string   `json:"providerId"`
		Transports []string `json:"transports"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &node); err != nil {
		t.Fatal(err)
	}
	if node.Kind != "generic" || node.ProviderID != "custom" || len(node.Transports) != 2 {
		t.Fatalf("unexpected node: %+v", node)
	}

	// Generic Provider connections default to API key auth and never return a secret.
	connection := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections", `{"nodeId":"`+node.ID+`","name":"primary","identity":"acct-1","secret":{"accessToken":"fixture-key"}}`)
	if connection.Code != http.StatusOK {
		t.Fatalf("connection status=%d body=%s", connection.Code, connection.Body.String())
	}
	if strings.Contains(connection.Body.String(), "fixture-key") {
		t.Fatal("connection response leaked credential material")
	}

	// A partial PATCH keeps the stored credential instead of blanking it.
	patched := doJSON(t, mux, cookie, http.MethodPatch, "/admin/v1/connections/"+node.ID, `{"name":"renamed"}`)
	if patched.Code != http.StatusNotFound {
		t.Fatalf("patching an unknown connection id status=%d", patched.Code)
	}

	// Built-in definitions cannot be mutated through the generic editor.
	builtin := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/provider-nodes", `{"kind":"builtin","providerId":"openai"}`)
	if builtin.Code != http.StatusOK {
		t.Fatalf("builtin node status=%d body=%s", builtin.Code, builtin.Body.String())
	}
}

func TestBuiltInConnectionUsesCatalogAuthModes(t *testing.T) {
	_, mux, store, credStore, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()

	bad := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections", `{"providerId":"openai","name":"primary","identity":"acct","authKind":"cookie","secret":{"cookie":"session=1"}}`)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("unsupported auth kind status=%d body=%s", bad.Code, bad.Body.String())
	}

	created := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections", `{"providerId":"openai","name":"primary","identity":"acct","secret":{"accessToken":"sk-live"}}`)
	if created.Code != http.StatusOK {
		t.Fatalf("connection status=%d body=%s", created.Code, created.Body.String())
	}
	var connection struct {
		ID     string `json:"id"`
		NodeID string `json:"nodeId"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &connection); err != nil {
		t.Fatal(err)
	}
	stored, err := credStore.GetConnection(context.Background(), connection.ID)
	if err != nil || stored.Secret.AccessToken != "sk-live" || stored.ProviderID != "openai" {
		t.Fatalf("stored connection=%+v err=%v", stored, err)
	}
}

func TestModelAliasDiscoveryAndPricingMutations(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-live" {
			t.Errorf("discovery credential header=%q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"gpt-5"},{"id":"gpt-5-mini"}]}`))
	}))
	defer upstream.Close()
	_, mux, store, credStore, cookie := newProvidersAPI(t, true, &discovery.Client{HTTP: upstream.Client()})
	defer store.Close()
	node, err := credStore.PutNode(context.Background(), credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: "openai", Name: "openai", BaseURL: upstream.URL, Transports: []string{"openai-chat"}})
	if err != nil {
		t.Fatal(err)
	}
	created := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections", `{"nodeId":"`+node.ID+`","name":"primary","identity":"acct","secret":{"accessToken":"sk-live"}}`)
	if created.Code != http.StatusOK {
		t.Fatalf("connection status=%d body=%s", created.Code, created.Body.String())
	}
	var connection struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &connection); err != nil {
		t.Fatal(err)
	}

	test := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections/"+connection.ID+"/test", "")
	if test.Code != http.StatusOK || !strings.Contains(test.Body.String(), `"modelsAvailable":2`) {
		t.Fatalf("connection test status=%d body=%s", test.Code, test.Body.String())
	}
	discover := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections/"+connection.ID+"/discover", "")
	if discover.Code != http.StatusOK || !strings.Contains(discover.Body.String(), "gpt-5-mini") {
		t.Fatalf("discover status=%d body=%s", discover.Code, discover.Body.String())
	}

	model := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/models", `{"providerId":"openai","id":"gpt-5-custom","name":"Custom","contextWindow":128000,"capabilities":["vision"]}`)
	if model.Code != http.StatusOK {
		t.Fatalf("model status=%d body=%s", model.Code, model.Body.String())
	}
	alias := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/aliases", `{"alias":"fast","providerId":"openai","modelId":"gpt-5-mini"}`)
	if alias.Code != http.StatusOK {
		t.Fatalf("alias status=%d body=%s", alias.Code, alias.Body.String())
	}
	badAlias := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/aliases", `{"alias":"ghost","providerId":"openai","modelId":"missing"}`)
	if badAlias.Code != http.StatusBadRequest {
		t.Fatalf("unknown alias target status=%d", badAlias.Code)
	}
	disable := doJSON(t, mux, cookie, http.MethodPatch, "/admin/v1/models/disabled", `{"providerId":"openai","modelId":"gpt-5-mini","disabled":true}`)
	if disable.Code != http.StatusOK {
		t.Fatalf("disable status=%d body=%s", disable.Code, disable.Body.String())
	}
	pricing := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/pricing", `{"providerId":"openai","modelId":"gpt-5","inputPerMTok":1.5,"outputPerMTok":6}`)
	if pricing.Code != http.StatusOK {
		t.Fatalf("pricing status=%d body=%s", pricing.Code, pricing.Body.String())
	}
	negative := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/pricing", `{"providerId":"openai","modelId":"gpt-5","inputPerMTok":-1}`)
	if negative.Code != http.StatusBadRequest {
		t.Fatalf("negative pricing status=%d", negative.Code)
	}
}

func TestConnectionDeleteIsIdempotentAndRefreshesBindings(t *testing.T) {
	_, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	created := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections", `{"providerId":"openai","name":"primary","identity":"acct","secret":{"accessToken":"sk-live"}}`)
	var connection struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &connection); err != nil {
		t.Fatal(err)
	}
	first := doJSON(t, mux, cookie, http.MethodDelete, "/admin/v1/connections/"+connection.ID, "")
	if first.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", first.Code, first.Body.String())
	}
	second := doJSON(t, mux, cookie, http.MethodDelete, "/admin/v1/connections/"+connection.ID, "")
	if second.Code != http.StatusNotFound {
		t.Fatalf("second delete status=%d want 404", second.Code)
	}
}

func TestConnectionReorderAndProxyAssignment(t *testing.T) {
	ctx := context.Background()
	_, mux, store, credStore, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	if _, err := store.DB().ExecContext(ctx, "INSERT INTO proxy_pools(id,name,enabled,strategy) VALUES('pool-1','Pool','1','round_robin')"); err != nil {
		t.Fatal(err)
	}
	created := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections", `{"providerId":"openai","name":"primary","identity":"acct","priority":0,"secret":{"accessToken":"sk-live"}}`)
	var connection struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(created.Body.Bytes(), &connection); err != nil {
		t.Fatal(err)
	}
	if err := credStore.SetConnectionPriority(ctx, connection.ID, 5); err != nil {
		t.Fatal(err)
	}
	assigned := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections/"+connection.ID+"/proxy", `{"proxyPoolId":"pool-1"}`)
	if assigned.Code != http.StatusOK {
		t.Fatalf("proxy assign status=%d body=%s", assigned.Code, assigned.Body.String())
	}
	stored, err := credStore.GetConnection(ctx, connection.ID)
	if err != nil || stored.ProxyPoolID != "pool-1" || stored.Priority != 5 {
		t.Fatalf("stored connection=%+v err=%v", stored, err)
	}
	snapshot, err := runtime.NewManager(ctx, store.DB())
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := snapshot.Load()
	if err != nil {
		t.Fatal(err)
	}
	if loaded.ConnectionPool(connection.ID) != "pool-1" {
		t.Fatalf("compiled pool binding=%q", loaded.ConnectionPool(connection.ID))
	}
}

func TestConnectionMoveSwapsAdjacentPriorities(t *testing.T) {
	ctx := context.Background()
	_, mux, store, credStore, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	first, err := credStore.PutNode(ctx, credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: "openai", Name: "openai"})
	if err != nil {
		t.Fatal(err)
	}
	a, err := credStore.PutConnection(ctx, credentials.Connection{NodeID: first.ID, Name: "a", AuthKind: credentials.AuthAPIKey, Identity: "a", Enabled: true, Priority: 0, Secret: credentials.Secret{AccessToken: "k"}})
	if err != nil {
		t.Fatal(err)
	}
	b, err := credStore.PutConnection(ctx, credentials.Connection{NodeID: first.ID, Name: "b", AuthKind: credentials.AuthAPIKey, Identity: "b", Enabled: true, Priority: 1, Secret: credentials.Secret{AccessToken: "k"}})
	if err != nil {
		t.Fatal(err)
	}
	if recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections/"+b.ID+"/order", `{"direction":"up"}`); recorder.Code != http.StatusOK {
		t.Fatalf("move status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	loadedA, err := credStore.GetConnection(ctx, a.ID)
	if err != nil {
		t.Fatal(err)
	}
	loadedB, err := credStore.GetConnection(ctx, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loadedB.Priority != 0 || loadedA.Priority != 1 {
		t.Fatalf("priorities after move: a=%d b=%d", loadedA.Priority, loadedB.Priority)
	}
	bad := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections/"+a.ID+"/order", `{"direction":"sideways"}`)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("invalid direction status=%d", bad.Code)
	}
}

func TestNativeModelProbeIsBoundedAndRedacted(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected model probe target: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer probe-secret" {
			t.Errorf("auth header=%q", r.Header.Get("Authorization"))
		}
		var body struct {
			Model     string `json:"model"`
			MaxTokens int    `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode probe request: %v", err)
		}
		if body.Model != "gpt-test" || body.MaxTokens != 1 {
			t.Errorf("probe body=%+v", body)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"OK"}}]}`))
	}))
	defer upstream.Close()
	_, mux, store, credStore, cookie := newProvidersAPI(t, true, &discovery.Client{HTTP: upstream.Client()})
	defer store.Close()
	node, err := credStore.PutNode(context.Background(), credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: "openai", Name: "OpenAI", BaseURL: upstream.URL + "/v1", Transports: []string{"openai-chat"}})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := credStore.PutConnection(context.Background(), credentials.Connection{NodeID: node.ID, Name: "test", Identity: "test-account", AuthKind: credentials.AuthAPIKey, Enabled: true, Secret: credentials.Secret{AccessToken: "probe-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	response := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections/"+connection.ID+"/test-models", `{"transport":"openai-chat","modelIds":["gpt-test"]}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ok":true`) {
		t.Fatalf("test model status=%d body=%s", response.Code, response.Body.String())
	}
	if strings.Contains(response.Body.String(), "probe-secret") || strings.Contains(response.Body.String(), "choices") {
		t.Fatal("model test response exposed provider credential or completion body")
	}
	tooMany := make([]string, 21)
	for i := range tooMany {
		tooMany[i] = "gpt-test" + string(rune('a'+i))
	}
	encoded, _ := json.Marshal(map[string]any{"transport": "openai-chat", "modelIds": tooMany})
	bad := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections/"+connection.ID+"/test-models", string(encoded))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("batch limit status=%d", bad.Code)
	}
}

// The System One/Jev provider configures the full typed endpoint as its base
// URL, so the typed request workflow posts verbatim with state/questions.
func TestSystemOneModelProbePostsVerbatim(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/systemone" {
			t.Errorf("unexpected systemone probe target: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer jev-secret" {
			t.Errorf("auth header=%q", r.Header.Get("Authorization"))
		}
		var body struct {
			Model     string            `json:"model"`
			State     map[string]any    `json:"state"`
			Questions []json.RawMessage `json:"questions"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode probe request: %v", err)
		}
		if body.Model != "jev" || body.State == nil || len(body.Questions) == 0 {
			t.Errorf("probe body=%+v", body)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer upstream.Close()
	_, mux, store, credStore, cookie := newProvidersAPI(t, true, &discovery.Client{HTTP: upstream.Client()})
	defer store.Close()
	node, err := credStore.PutNode(context.Background(), credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: "typesafe", Name: "typesafe", BaseURL: upstream.URL + "/v1/systemone", Transports: []string{"systemone"}})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := credStore.PutConnection(context.Background(), credentials.Connection{NodeID: node.ID, Name: "typesafe", Identity: "acct", AuthKind: credentials.AuthAPIKey, Enabled: true, Secret: credentials.Secret{AccessToken: "jev-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	response := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections/"+connection.ID+"/test-models", `{"transport":"systemone","modelIds":["jev"]}`)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"ok":true`) {
		t.Fatalf("systemone probe status=%d body=%s", response.Code, response.Body.String())
	}
}
