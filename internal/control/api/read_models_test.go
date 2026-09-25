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
)

func newReadAPI(t *testing.T) (*Handler, *http.ServeMux, *sqlite.Store) {
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
	specs := []registry.Spec{{ID: "openai", Transports: []registry.Protocol{registry.TransportOpenAIChat, registry.TransportOpenAIResponses}, Auth: registry.AuthAPIKey, ModelCatalog: registry.CatalogStatic}}
	handler := New(Options{
		Accounts:  adminauth.NewStore(store.DB()),
		Sessions:  adminauth.NewSessionManager(time.Hour),
		Settings:  manager,
		Keys:      manager,
		DB:        store.DB(),
		Runtime:   manager,
		Providers: specs,
		Ready:     func() bool { return true },
		Build:     buildinfo.Info{Version: "test", Commit: "deadbeef"},
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
	if _, err := handler.opts.Accounts.Bootstrap(ctx, "a1", "operator", "s3cret"); err != nil {
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
	if _, err := handler.opts.Accounts.Bootstrap(ctx, "a1", "operator", "s3cret"); err != nil {
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
	if _, err := handler.opts.Accounts.Bootstrap(ctx, "a1", "operator", "s3cret"); err != nil {
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
	if _, err := store.DB().ExecContext(ctx, "INSERT INTO provider_models(provider_id,model_id,display_name,context_window,capabilities) VALUES('openai','gpt-x','GPT X',1000,'[\"vision\"]'),('typesafe','jev-1','Jev',2000,'[]')"); err != nil {
		t.Fatal(err)
	}
	if _, err := handler.opts.Accounts.Bootstrap(ctx, "a1", "operator", "s3cret"); err != nil {
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
	if code != http.StatusOK || !contains(body, "jev-1") || contains(body, "gpt-x") {
		t.Fatalf("systemone status=%d body=%s", code, body)
	}
}

func TestReadModelsNeverExposeSecrets(t *testing.T) {
	handler, mux, store := newReadAPI(t)
	defer store.Close()
	ctx := context.Background()
	if _, err := handler.opts.Accounts.Bootstrap(ctx, "a1", "operator", "s3cret"); err != nil {
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
	if _, err := handler.opts.Accounts.Bootstrap(ctx, "a1", "operator", "s3cret"); err != nil {
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
