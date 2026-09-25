package ingress

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/runtime"
	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

func newManager(t *testing.T) *runtime.Manager {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, t.TempDir()+"/routeweft.sqlite")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := migrations.NewRunner(store.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	manager, err := runtime.NewManager(ctx, store.DB())
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func newHandler(t *testing.T, manager *runtime.Manager) http.Handler {
	t.Helper()
	handler := New(manager, Options{})
	mux := http.NewServeMux()
	handler.Attach(mux)
	return mux
}

func TestIndexAndModelsRequireAuth(t *testing.T) {
	manager := newManager(t)
	handler := newHandler(t, manager)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1/models", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status %d, want 401", recorder.Code)
	}
}

func TestModelLifecycleVisibleThroughAPI(t *testing.T) {
	ctx := context.Background()
	manager := newManager(t)
	entry, plaintext, err := manager.CreateAPIKey(ctx, "test key")
	if err != nil {
		t.Fatal(err)
	}
	if entry.Hash != "" {
		t.Fatal("returned entry must not expose the stored hash")
	}
	if len(plaintext) < 40 || !strings.HasPrefix(plaintext, "rw_") {
		t.Fatalf("generated key is malformed: %q", plaintext)
	}
	if err := manager.PutModel(ctx, runtime.Model{ProviderID: "openai", ID: "gpt-4o-mini", Name: "gpt-4o-mini"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.PutAlias(ctx, "fast", runtime.ModelRef{ProviderID: "openai", ModelID: "gpt-4o-mini"}); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetModelDisabled(ctx, "openai", "gpt-4o", true); err != nil {
		t.Fatal(err)
	}
	if err := manager.PutModel(ctx, runtime.Model{ProviderID: "openai", ID: "gpt-4o", Name: "gpt-4o"}); err != nil {
		t.Fatal(err)
	}

	handler := newHandler(t, manager)
	auth := func(r *http.Request) *http.Request {
		r.Header.Set("Authorization", "Bearer "+plaintext)
		return r
	}

	list := httptest.NewRecorder()
	handler.ServeHTTP(list, auth(httptest.NewRequest(http.MethodGet, "/v1/models", nil)))
	if list.Code != http.StatusOK {
		t.Fatalf("models status %d body %s", list.Code, list.Body.String())
	}
	var payload struct {
		Data []map[string]any `json:"data"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	ids := map[string]bool{}
	for _, model := range payload.Data {
		ids[model["provider"].(string)+"/"+model["id"].(string)] = true
	}
	for _, want := range []string{"openai/gpt-4o-mini", "openai/fast"} {
		if !ids[want] {
			t.Fatalf("model list missing %s: %v", want, ids)
		}
	}
	if ids["openai/gpt-4o"] {
		t.Fatal("disabled model appeared in the list")
	}

	detail := httptest.NewRecorder()
	handler.ServeHTTP(detail, auth(httptest.NewRequest(http.MethodGet, "/v1/models/openai/gpt-4o-mini", nil)))
	if detail.Code != http.StatusOK {
		t.Fatalf("model detail status %d", detail.Code)
	}
	missing := httptest.NewRecorder()
	handler.ServeHTTP(missing, auth(httptest.NewRequest(http.MethodGet, "/v1/models/openai/gpt-4o", nil)))
	if missing.Code != http.StatusNotFound {
		t.Fatalf("disabled model detail status %d, want 404", missing.Code)
	}
}

func TestAnthropicAndGeminiKeyForms(t *testing.T) {
	ctx := context.Background()
	manager := newManager(t)
	_, plaintext, err := manager.CreateAPIKey(ctx, "compat")
	if err != nil {
		t.Fatal(err)
	}
	handler := newHandler(t, manager)

	anthropic := httptest.NewRequest(http.MethodGet, "/v1", nil)
	anthropic.Header.Set("X-Api-Key", plaintext)
	anthropicRecorder := httptest.NewRecorder()
	handler.ServeHTTP(anthropicRecorder, anthropic)
	if anthropicRecorder.Code != http.StatusOK {
		t.Fatalf("anthropic key rejected with %d", anthropicRecorder.Code)
	}

	gemini := httptest.NewRequest(http.MethodGet, "/v1?key="+plaintext, nil)
	geminiRecorder := httptest.NewRecorder()
	handler.ServeHTTP(geminiRecorder, gemini)
	if geminiRecorder.Code != http.StatusOK {
		t.Fatalf("gemini query key rejected with %d", geminiRecorder.Code)
	}
	conflict := httptest.NewRequest(http.MethodGet, "/v1?key=other-key", nil)
	conflict.Header.Set("X-Api-Key", plaintext)
	conflictRecorder := httptest.NewRecorder()
	handler.ServeHTTP(conflictRecorder, conflict)
	if conflictRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("conflicting key forms status %d, want 401", conflictRecorder.Code)
	}
}

func TestCORSAllowlistAndModelInfo(t *testing.T) {
	manager := newManager(t)
	_, key, err := manager.CreateAPIKey(context.Background(), "cors")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.PutModel(context.Background(), runtime.Model{ProviderID: "local", ID: "model:small", Name: "Small"}); err != nil {
		t.Fatal(err)
	}
	handler := New(manager, Options{CORSOrigins: []string{"https://console.example"}})
	mux := http.NewServeMux()
	handler.Attach(mux)

	preflight := httptest.NewRequest(http.MethodOptions, "/v1", nil)
	preflight.Header.Set("Origin", "https://console.example")
	preflightRecorder := httptest.NewRecorder()
	mux.ServeHTTP(preflightRecorder, preflight)
	if preflightRecorder.Code != http.StatusNoContent || preflightRecorder.Header().Get("Access-Control-Allow-Origin") != "https://console.example" {
		t.Fatalf("preflight status=%d CORS=%q", preflightRecorder.Code, preflightRecorder.Header().Get("Access-Control-Allow-Origin"))
	}

	denied := httptest.NewRequest(http.MethodOptions, "/v1/models", nil)
	denied.Header.Set("Origin", "https://attacker.example")
	deniedRecorder := httptest.NewRecorder()
	mux.ServeHTTP(deniedRecorder, denied)
	if deniedRecorder.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("unconfigured origin received CORS permission")
	}

	info := httptest.NewRequest(http.MethodGet, "/v1/models/info", nil)
	info.Header.Set("X-Goog-Api-Key", key)
	infoRecorder := httptest.NewRecorder()
	mux.ServeHTTP(infoRecorder, info)
	if infoRecorder.Code != http.StatusOK || !strings.Contains(infoRecorder.Body.String(), "model:small") {
		t.Fatalf("model info status=%d body=%s", infoRecorder.Code, infoRecorder.Body.String())
	}

	detail := httptest.NewRequest(http.MethodGet, "/v1/models/local/model:small", nil)
	detail.Header.Set("Authorization", "Bearer "+key)
	detailRecorder := httptest.NewRecorder()
	mux.ServeHTTP(detailRecorder, detail)
	if detailRecorder.Code != http.StatusOK {
		t.Fatalf("model detail status=%d body=%s", detailRecorder.Code, detailRecorder.Body.String())
	}
}

func TestRevokedKeyRejectedImmediately(t *testing.T) {
	ctx := context.Background()
	manager := newManager(t)
	entry, plaintext, err := manager.CreateAPIKey(ctx, "revoke me")
	if err != nil {
		t.Fatal(err)
	}
	handler := newHandler(t, manager)
	request := func() int {
		r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		r.Header.Set("Authorization", "Bearer "+plaintext)
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, r)
		return recorder.Code
	}
	if code := request(); code != http.StatusOK {
		t.Fatalf("active key status %d", code)
	}
	if err := manager.DeleteAPIKey(ctx, entry.ID); err != nil {
		t.Fatal(err)
	}
	if code := request(); code != http.StatusUnauthorized {
		t.Fatalf("revoked key status %d, want 401", code)
	}
}
