package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/adminauth"
	"github.com/raufimusaddiq/routeweft/internal/buildinfo"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
	"github.com/raufimusaddiq/routeweft/internal/store/storetest"
)

func newReadAPI(t *testing.T) (*Handler, *http.ServeMux, *sqlite.Store) {
	t.Helper()
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "routeweft.sqlite")
	storetest.Seed(t, dbPath)
	store, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.NewRunner(store.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	manager, err := runtime.NewManagerObserved(ctx, store.DB(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	specs := []registry.Spec{{ID: "openai", Transports: []registry.Protocol{registry.TransportOpenAIChat, registry.TransportOpenAIResponses}, Auth: registry.AuthAPIKey, ModelCatalog: registry.CatalogStatic}}
	handler := New(Options{
		Accounts:        adminauth.NewStore(store.DB()),
		Sessions:        adminauth.NewSessionManager(time.Hour),
		Settings:        manager,
		Keys:            manager,
		DB:              store.DB(),
		Runtime:         manager,
		ProviderCatalog: manager,
		Providers:       specs,
		Ready:           func() bool { return true },
		Build:           buildinfo.Info{Version: "test", Commit: "deadbeef"},
	})
	mux := http.NewServeMux()
	handler.Attach(mux)
	return handler, mux, store
}

func TestReadModelsRequireSession(t *testing.T) {
	_, mux, store := newReadAPI(t)
	defer store.Close()
	for _, path := range []string{"/admin/v1/overview", "/admin/v1/providers", "/admin/v1/connections", "/admin/v1/models", "/admin/v1/keys", "/admin/v1/usage"} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s status=%d want 401", path, recorder.Code)
		}
	}
}

func TestEveryReadResourceRespondsForAdmin(t *testing.T) {
	handler, mux, store := newReadAPI(t)
	defer store.Close()
	handler.opts.Logs = NewLogBuffer(8)
	ctx := context.Background()
	if err := bootstrapOperator(ctx, store.DB()); err != nil {
		t.Fatal(err)
	}
	session, err := handler.opts.Sessions.Create(adminauth.Account{ID: "a1", Username: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookieName, Value: session.ID}
	for _, path := range []string{
		"overview", "providers", "provider-nodes", "connections", "models", "aliases", "pricing", "combos", "proxy-pools", "keys", "usage", "requests", "quota", "token-saver", "systemone", "logs",
	} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, "/admin/v1/"+path, nil)
		request.AddCookie(cookie)
		mux.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Errorf("%s status=%d body=%s", path, recorder.Code, recorder.Body.String())
		}
	}
}

func TestOverviewAndProvidersShapes(t *testing.T) {
	handler, mux, store := newReadAPI(t)
	defer store.Close()
	ctx := context.Background()
	if err := bootstrapOperator(ctx, store.DB()); err != nil {
		t.Fatal(err)
	}
	session, err := handler.opts.Sessions.Create(adminauth.Account{ID: "a1", Username: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookieName, Value: session.ID}

	overview := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/admin/v1/overview", nil)
	request.AddCookie(cookie)
	mux.ServeHTTP(overview, request)
	if overview.Code != http.StatusOK {
		t.Fatalf("overview status=%d body=%s", overview.Code, overview.Body.String())
	}
	var payload struct {
		Runtime struct {
			Health string `json:"health"`
			Commit string `json:"commit"`
		} `json:"runtime"`
		Providers struct {
			Catalog int `json:"catalog"`
		} `json:"providers"`
	}
	if err := json.Unmarshal(overview.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Runtime.Health != "ready" || payload.Providers.Catalog != 1 || payload.Runtime.Commit != "deadbeef" {
		t.Fatalf("unexpected overview payload: %+v", payload)
	}

	providers := httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/admin/v1/providers", nil)
	request.AddCookie(cookie)
	mux.ServeHTTP(providers, request)
	if providers.Code != http.StatusOK {
		t.Fatalf("providers status=%d", providers.Code)
	}
	var list struct {
		Items []struct {
			ID         string   `json:"id"`
			Transports []string `json:"transports"`
			AuthModes  []string `json:"authModes"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal(providers.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if list.Total != 1 || len(list.Items) != 1 || len(list.Items[0].AuthModes) != 1 {
		t.Fatalf("unexpected providers payload: %s", providers.Body.String())
	}
}

func TestReadModelsBoundedPagination(t *testing.T) {
	handler, mux, store := newReadAPI(t)
	defer store.Close()
	ctx := context.Background()
	if err := bootstrapOperator(ctx, store.DB()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if _, err := store.DB().ExecContext(ctx, "INSERT INTO api_keys(id,name,key_hash,key_prefix) VALUES(?,?,?,?)", string(rune('a'+i)), "key", "hash"+string(rune('a'+i)), "rw_"); err != nil {
			t.Fatal(err)
		}
	}
	session, err := handler.opts.Sessions.Create(adminauth.Account{ID: "a1", Username: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookieName, Value: session.ID}

	// pageSize is clamped to the maximum.
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/admin/v1/keys?pageSize=100000&page=1", nil)
	request.AddCookie(cookie)
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("keys status=%d", recorder.Code)
	}
	var page1 struct {
		PageSize int `json:"pageSize"`
		Total    int `json:"total"`
		Items    []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &page1); err != nil {
		t.Fatal(err)
	}
	if page1.PageSize != maxPageSize || page1.Total != 5 || len(page1.Items) != 5 {
		t.Fatalf("pageSize not clamped: %+v", page1)
	}

	// A later page is bounded and disjoint from the first.
	recorder = httptest.NewRecorder()
	request = httptest.NewRequest(http.MethodGet, "/admin/v1/keys?pageSize=2&page=2", nil)
	request.AddCookie(cookie)
	mux.ServeHTTP(recorder, request)
	var page2 struct {
		Items []struct {
			ID string `json:"id"`
		} `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &page2); err != nil {
		t.Fatal(err)
	}
	if len(page2.Items) != 2 || page2.Items[0].ID == page1.Items[0].ID {
		t.Fatalf("unexpected page2: %s", recorder.Body.String())
	}

	// Invalid pagination is a client error, not a 500.
	for _, query := range []string{"page=0", "page=abc", "pageSize=-1"} {
		recorder = httptest.NewRecorder()
		request = httptest.NewRequest(http.MethodGet, "/admin/v1/keys?"+query, nil)
		request.AddCookie(cookie)
		mux.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("query %q status=%d want 400", query, recorder.Code)
		}
	}
}

func TestModelsReadModelFiltersSystemOne(t *testing.T) {
	handler, mirror, store := newReadAPI(t)
	defer store.Close()
	handler.opts.Providers = []registry.Spec{
		{ID: "openai", Transports: []registry.Protocol{registry.TransportOpenAIChat}, Auth: registry.AuthAPIKey, ModelCatalog: registry.CatalogStatic},
		{ID: "typesafe", Transports: []registry.Protocol{registry.TransportSystemOne}, Auth: registry.AuthAPIKey, ModelCatalog: registry.CatalogStatic},
	}
	ctx := context.Background()
	if _, err := store.DB().ExecContext(ctx, "INSERT INTO provider_nodes(id,kind,provider_id,name,transports) VALUES('n1','builtin','typesafe','TypeSafe','[\"systemone\"]')"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, "INSERT INTO provider_nodes(id,kind,provider_id,name,transports) VALUES('n2','builtin','cloudflare-ai','Cloudflare','[\"openai-chat\",\"systemone\"]')"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, "INSERT INTO provider_models(provider_id,model_id,display_name,context_window,capabilities) VALUES('openai','gpt-x','GPT X',1000,'[\"vision\"]'),('typesafe','jev-1','Jev',2000,'[]'),('cloudflare-ai','@cf/cloudflare/clef','Clef',2000,'[]')"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO provider_nodes(id,kind,provider_id,name,prefix,base_url,transports) VALUES('n3','generic','typed-custom','Typed','typed','https://typed.example.com/systemone','["systemone"]');
INSERT INTO provider_models(provider_id,model_id,capabilities) VALUES('cloudflare-ai','@cf/meta/llama-3-8b-instruct','[]'),('typed-custom','generic-typed','[]');
INSERT INTO disabled_models(provider_id,model_id) VALUES('typed-custom','generic-typed');`); err != nil {
		t.Fatal(err)
	}
	manager, err := runtime.NewManagerObserved(ctx, store.DB(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	handler.opts.Runtime = manager
	if err := bootstrapOperator(ctx, store.DB()); err != nil {
		t.Fatal(err)
	}
	session, err := handler.opts.Sessions.Create(adminauth.Account{ID: "a1", Username: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookieName, Value: session.ID}
	mux := mirror

	read := func(path string) (int, string) {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(cookie)
		mux.ServeHTTP(recorder, request)
		return recorder.Code, recorder.Body.String()
	}
	code, body := read("/admin/v1/models")
	if code != http.StatusOK || !contains(body, "gpt-x") || !contains(body, "jev-1") {
		t.Fatalf("models status=%d body=%s", code, body)
	}
	code, body = read("/admin/v1/systemone")
	if code != http.StatusOK || !contains(body, "jev-1") || !contains(body, "clef") || !contains(body, "generic-typed") || contains(body, "gpt-x") || contains(body, "llama-3") || !contains(body, `"total":3`) {
		t.Fatalf("systemone status=%d body=%s", code, body)
	}
	code, body = read("/admin/v1/systemone?pageSize=1&page=2")
	if code != 200 || !contains(body, `"total":3`) || !contains(body, "generic-typed") || !contains(body, `"disabled":true`) || !contains(body, `"kind":"systemone"`) {
		t.Fatalf("typed pagination/disabled metadata: %d %s", code, body)
	}
}

func TestReadModelsNeverExposeSecrets(t *testing.T) {
	handler, mux, store := newReadAPI(t)
	defer store.Close()
	ctx := context.Background()
	if err := bootstrapOperator(ctx, store.DB()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, "INSERT INTO provider_nodes(id,kind,provider_id,name,base_url) VALUES('n1','generic','openai','node','https://user:pass@example.invalid/v1?token=leak')"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, "INSERT INTO provider_connections(id,node_id,name,auth_kind,identity,secret_blob) VALUES('c1','n1','acct','api-key','acct','sealed-blob-material')"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, "INSERT INTO api_keys(id,name,key_hash,key_prefix) VALUES('k1','prod','topsecret-hash','rw_abcd1234')"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, "INSERT INTO proxy_pools(id,name,enabled,strategy) VALUES('p1','proxy',1,'round_robin')"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, "INSERT INTO proxy_pool_members(id,pool_id,position,proxy_url,enabled) VALUES('pm1','p1',0,'http://user:pass@example.invalid:8080?token=leak',1)"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, "INSERT INTO request_details(request_id,route_mode,detail) VALUES('r1','native','{\"Authorization\":\"secret\"}')"); err != nil {
		t.Fatal(err)
	}
	session, err := handler.opts.Sessions.Create(adminauth.Account{ID: "a1", Username: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	cookie := &http.Cookie{Name: sessionCookieName, Value: session.ID}

	for _, path := range []string{"/admin/v1/provider-nodes", "/admin/v1/connections", "/admin/v1/keys", "/admin/v1/proxy-pools", "/admin/v1/requests"} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(cookie)
		mux.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s status=%d", path, recorder.Code)
		}
		body := recorder.Body.String()
		for _, secret := range []string{"sealed-blob-material", "topsecret-hash", "pass@", "token=leak", "\"secret\""} {
			if contains(body, secret) {
				t.Fatalf("%s leaked %q: %s", path, secret, body)
			}
		}
	}
}

func TestQuotaReadModelClassifiesStateWithoutLeakingError(t *testing.T) {
	handler, mux, store := newReadAPI(t)
	defer store.Close()
	ctx := context.Background()
	if err := bootstrapOperator(ctx, store.DB()); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, "INSERT INTO provider_nodes(id,kind,provider_id,name) VALUES('n1','builtin','openai','OpenAI')"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, "INSERT INTO provider_connections(id,node_id,name,auth_kind,identity) VALUES('c1','n1','primary','api-key','acct-1')"); err != nil {
		t.Fatal(err)
	}
	session, err := handler.opts.Sessions.Create(adminauth.Account{ID: "a1", Username: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	remaining := 0.0
	reset := time.Now().Add(time.Hour)
	handler.opts.Runtime.State().ObserveQuotaResult("c1", &remaining, reset, time.Now(), "")
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/admin/v1/quota", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || !contains(recorder.Body.String(), "exhausted") {
		t.Fatalf("quota state not exposed: status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), "credential") || strings.Contains(recorder.Body.String(), "token") {
		t.Fatalf("quota output contains internal error material: %s", recorder.Body.String())
	}
}

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestProviderReadModelKeepsTheStaticModelsWireContract is the seam test for the
// third blocker: the dashboard and key-access screens parse `staticModels` as an
// array of model ids. A rename on the wire silently emptied both, so this asserts
// the exact JSON a real client receives rather than the Go field name.
func TestProviderReadModelKeepsTheStaticModelsWireContract(t *testing.T) {
	handler, mux, store := newReadAPI(t)
	defer store.Close()
	handler.opts.Providers = []registry.Spec{{
		ID: "openai", Transports: []registry.Protocol{registry.TransportOpenAIChat},
		Auth: registry.AuthAPIKey, ModelCatalog: registry.CatalogStatic,
		SeedModels: []registry.SeedModel{{ID: "gpt-5", Name: "GPT-5", ContextWindow: 400000}},
	}}
	ctx := context.Background()
	if err := bootstrapOperator(ctx, store.DB()); err != nil {
		t.Fatal(err)
	}
	session, err := handler.opts.Sessions.Create(adminauth.Account{ID: "a1", Username: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/admin/v1/providers", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("providers status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	// Decoded the way the UI does: a plain array of ids under `staticModels`.
	var raw struct {
		Items []map[string]json.RawMessage `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &raw); err != nil {
		t.Fatal(err)
	}
	if len(raw.Items) != 1 {
		t.Fatalf("items=%d", len(raw.Items))
	}
	field, ok := raw.Items[0]["staticModels"]
	if !ok {
		t.Fatalf("provider read model has no staticModels field: %s", recorder.Body.String())
	}
	var models []string
	if err := json.Unmarshal(field, &models); err != nil {
		t.Fatalf("staticModels is not an array of ids: %s", field)
	}
	if len(models) != 1 || models[0] != "gpt-5" {
		t.Fatalf("staticModels=%v", models)
	}
	if _, renamed := raw.Items[0]["seedModels"]; renamed {
		t.Fatalf("the wire name was renamed to seedModels: %s", recorder.Body.String())
	}
	// This provider has seed models only and no provider_models rows, so both
	// counts must be a present zero: that zero is the signal #360 exists for,
	// and an omitted field would read as "unknown" in the dashboard (#367).
	for _, field := range []string{"discoveredModels", "customModels"} {
		if value, ok := raw.Items[0][field]; !ok || string(value) != "0" {
			t.Fatalf("%s=%s, want a present 0: %s", field, value, recorder.Body.String())
		}
	}
}

// TestProviderReadModelReportsSignInModes is the seam test for the dashboard: it
// must be told which affordances exist. Before this the UI offered a browser and a
// device button for every oauth provider, so half of them 404'd.
func TestProviderReadModelReportsSignInModes(t *testing.T) {
	handler, mux, store := newReadAPI(t)
	defer store.Close()
	handler.opts.Providers = registry.Builtins()
	handler.opts.PublicOrigin = "https://dashboard.example.test"
	ctx := context.Background()
	if err := bootstrapOperator(ctx, store.DB()); err != nil {
		t.Fatal(err)
	}
	session, err := handler.opts.Sessions.Create(adminauth.Account{ID: "a1", Username: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodGet, "/admin/v1/providers?pageSize=200", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("providers status=%d", recorder.Code)
	}
	var payload struct {
		Items []struct {
			ID          string   `json:"id"`
			FlowType    string   `json:"flowType"`
			SignInModes []string `json:"signInModes"`
			MetaKeys    []string `json:"metaKeys"`
		} `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	modes := make(map[string][]string, len(payload.Items))
	types := make(map[string]string, len(payload.Items))
	for _, item := range payload.Items {
		modes[item.ID] = item.SignInModes
		types[item.ID] = item.FlowType
	}
	// A device provider must not advertise a browser affordance, and vice versa.
	if types["github"] != "device_code" || !hasMode(modes["github"], "device") || hasMode(modes["github"], "browser") {
		t.Fatalf("github flowType=%q modes=%v", types["github"], modes["github"])
	}
	if types["claude"] != "authorization_code_pkce" || !hasMode(modes["claude"], "browser") || hasMode(modes["claude"], "device") {
		t.Fatalf("claude flowType=%q modes=%v", types["claude"], modes["claude"])
	}
	// An import mode must be advertised alongside the provider's other flow.
	if !hasMode(modes["codex"], "import") {
		t.Fatalf("codex modes=%v, want import", modes["codex"])
	}
	// A provider with no flow at all keeps the manual form only.
	if len(modes["openai"]) != 0 {
		t.Fatalf("openai modes=%v, want none", modes["openai"])
	}
	// Only the flows that read operator input advertise fields for it, so claude
	// and github do not ask for a client id or region nothing consumes.
	metaKeys := make(map[string][]string, len(payload.Items))
	for _, item := range payload.Items {
		metaKeys[item.ID] = item.MetaKeys
	}
	if !hasMode(metaKeys["gitlab"], "clientId") {
		t.Fatalf("gitlab metaKeys=%v, want clientId", metaKeys["gitlab"])
	}
	if len(metaKeys["claude"]) != 0 {
		t.Fatalf("claude metaKeys=%v, want none", metaKeys["claude"])
	}
	if len(metaKeys["github"]) != 0 {
		t.Fatalf("github metaKeys=%v, want none", metaKeys["github"])
	}
}

func hasMode(modes []string, want string) bool {
	for _, mode := range modes {
		if mode == want {
			return true
		}
	}
	return false
}
