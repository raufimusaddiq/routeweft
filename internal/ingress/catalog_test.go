package ingress

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func TestHistoricalAliasesShareHandlersAndAuthentication(t *testing.T) {
	manager := newManager(t)
	_, key, err := manager.CreateAPIKey(context.Background(), "aliases")
	if err != nil {
		t.Fatal(err)
	}
	h := New(manager, Options{CORSOrigins: []string{"https://client.example"}})
	mux := http.NewServeMux()
	h.Attach(mux)
	paths := []struct{ method, suffix string }{
		{"GET", ""}, {"GET", "/models"}, {"GET", "/models/info?id=missing/model"},
		{"GET", "/models/llm"}, {"GET", "/models/missing/model"},
		{"POST", "/chat/completions"}, {"POST", "/responses"}, {"POST", "/responses/compact"},
		{"POST", "/messages"}, {"POST", "/messages/count_tokens"}, {"POST", "/api/chat"}, {"POST", "/systemone"},
	}
	for _, route := range paths {
		for _, prefix := range []string{"/v1/v1", "/api/v1"} {
			t.Run(route.method+prefix+route.suffix, func(t *testing.T) {
				var canonical *httptest.ResponseRecorder
				for _, path := range []string{"/v1" + route.suffix, prefix + route.suffix} {
					for _, credential := range []string{"", "invalid", key} {
						r := httptest.NewRequest(route.method, path, strings.NewReader(`{}`))
						r.Header.Set("X-Api-Key", credential)
						r.Header.Set("Origin", "https://client.example")
						w := httptest.NewRecorder()
						mux.ServeHTTP(w, r)
						if credential != key {
							if w.Code != 401 {
								t.Fatalf("%s unauthenticated: %d", path, w.Code)
							}
							continue
						}
						if canonical == nil {
							canonical = w
						} else if w.Code != canonical.Code || w.Body.String() != canonical.Body.String() || !reflect.DeepEqual(w.Header(), canonical.Header()) {
							t.Fatalf("alias differs: %d %s; canonical %d %s", w.Code, w.Body, canonical.Code, canonical.Body)
						}
					}
					preflight := httptest.NewRequest("OPTIONS", strings.Split(path, "?")[0], nil)
					preflight.Header.Set("Origin", "https://client.example")
					w := httptest.NewRecorder()
					mux.ServeHTTP(w, preflight)
					if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "https://client.example" {
						t.Fatalf("preflight: %d %v", w.Code, w.Header())
					}
				}
			})
		}
	}
	for _, path := range []string{"/api/v1beta/models", "/api/v1beta/models/demo:generateContent", "/api/v1beta/models/demo:streamGenerateContent"} {
		method := "POST"
		if path == "/api/v1beta/models" {
			method = "GET"
		}
		for _, credential := range []string{"", key} {
			w := httptest.NewRecorder()
			r := httptest.NewRequest(method, path, strings.NewReader(`{}`))
			r.Header.Set("X-Goog-Api-Key", credential)
			mux.ServeHTTP(w, r)
			want := 401
			if credential == key {
				want = 400
				if method == "GET" {
					want = 200
				}
			}
			if w.Code != want {
				t.Fatalf("%s: %d %s, want %d", path, w.Code, w.Body, want)
			}
		}
	}
	for _, path := range []string{"/api/v1/images/generations", "/api/v1/admin", "/api/v1beta", "/v1/v1beta/models"} {
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, httptest.NewRequest("POST", path, nil))
		if w.Code != 404 && w.Code != 405 {
			t.Fatalf("unretained path %s: %d", path, w.Code)
		}
	}
}

func TestCatalogInfoKindsAndOllamaTags(t *testing.T) {
	ctx := context.Background()
	manager := newManager(t)
	entry, key, err := manager.CreateAPIKey(ctx, "catalog")
	if err != nil {
		t.Fatal(err)
	}
	_, err = manager.Update(ctx, func(candidate *runtime.Candidate) error {
		for _, model := range []runtime.Model{
			{ProviderID: "alpha", ID: "gizmo:7b", Name: "Gizmo", ContextWindow: 8192, Capabilities: []string{"vision", "webSearch"}},
			{ProviderID: "beta", ID: "gizmo:7b"},
			{ProviderID: "alpha", ID: "vendor/nested"},
			{ProviderID: "alpha", ID: "hidden"},
			{ProviderID: "typesafe", ID: "jev-latest"},
			{ProviderID: "cloudflare-ai", ID: "@cf/cloudflare/clef"},
			{ProviderID: "typed", ID: "custom"},
		} {
			candidate.AddModel(model)
		}
		candidate.SetAlias("fast", runtime.ModelRef{ProviderID: "alpha", ModelID: "gizmo:7b"})
		candidate.SetAlias("hidden-alias", runtime.ModelRef{ProviderID: "alpha", ModelID: "hidden"})
		candidate.SetAlias("typed-alias", runtime.ModelRef{ProviderID: "cloudflare-ai", ModelID: "@cf/cloudflare/clef"})
		candidate.SetModelDisabled("alpha", "hidden", true)
		candidate.Providers.Nodes = append(candidate.Providers.Nodes, credentials.Node{ID: "typed", ProviderID: "typed", Kind: credentials.NodeGeneric, Transports: []string{"systemone"}})
		candidate.Combos = append(candidate.Combos, runtime.Combo{ID: "combo", Name: "balanced", Members: []runtime.ComboMember{{ProviderID: "alpha", ModelID: "gizmo:7b", Selected: true}}})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	handler := newHandler(t, manager)
	get := func(path, credential string) *httptest.ResponseRecorder {
		t.Helper()
		r := httptest.NewRequest("GET", path, nil)
		r.Header.Set("Authorization", "Bearer "+credential)
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	for _, tc := range []struct {
		path string
		ids  []string
	}{
		{"/v1/models", []string{"alpha/gizmo:7b", "alpha/vendor/nested", "balanced", "beta/gizmo:7b", "fast"}},
		{"/v1/models?kind=systemone", []string{"cloudflare-ai/@cf/cloudflare/clef", "typed-alias", "typed/custom", "typesafe/jev-latest"}},
		{"/v1/models/image-to-text", []string{"alpha/gizmo:7b", "fast"}},
		{"/v1/models/web", []string{"alpha/gizmo:7b", "fast"}},
		{"/v1/models?kind=webFetch", []string{}},
		{"/v1/models/arbitrary/path?kind=webSearch", []string{"alpha/gizmo:7b", "fast"}},
		{"/v1/models?kind=embedding", []string{}},
		{"/v1/models?kind=unknown", []string{}},
	} {
		w := get(tc.path, key)
		var list struct{ Data []struct{ ID string } }
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &list) != nil {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body)
		}
		ids := make([]string, 0, len(list.Data))
		for _, model := range list.Data {
			ids = append(ids, model.ID)
		}
		if !reflect.DeepEqual(ids, tc.ids) {
			t.Fatalf("%s: %v, want %v", tc.path, ids, tc.ids)
		}
	}
	for _, tc := range []struct{ path, fixture string }{
		{"/v1/models/info?id=alpha/gizmo:7b&kind=llm", "model-info.json"},
		{"/api/tags", "ollama-tags.json"},
	} {
		w := get(tc.path, key)
		want, err := os.ReadFile("testdata/" + tc.fixture)
		if err != nil {
			t.Fatal(err)
		}
		var actual, expected any
		if w.Code != 200 || json.Unmarshal(w.Body.Bytes(), &actual) != nil || json.Unmarshal(want, &expected) != nil || !reflect.DeepEqual(actual, expected) {
			t.Fatalf("%s: %d %s, want %s", tc.path, w.Code, w.Body, want)
		}
	}
	for _, path := range []string{"/v1/models/info?id=fast", "/v1/models/info?id=balanced", "/v1/models/info?id=typed-alias&kind=systemone", "/v1/models/alpha/vendor/nested"} {
		if w := get(path, key); w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	for _, path := range []string{"/v1/models/info?id=alpha/hidden", "/v1/models/info?id=hidden-alias", "/v1/models/info?id=gizmo:7b", "/v1/models/info?id=alpha/gizmo:7b&kind=systemone", "/v1/models/image", "/v1/models/tts", "/v1/models/unknown"} {
		if w := get(path, key); w.Code != 404 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body)
		}
	}
	miss := get("/v1/models/info?id=kenari/kenari-free", key)
	if miss.Code != 404 || !strings.Contains(miss.Body.String(), "GET /v1/models") || !strings.Contains(miss.Body.String(), "5 callable ids") {
		t.Fatalf("unknown id does not point at the real catalogue: %d %s", miss.Code, miss.Body)
	}
	if w := get("/v1/models/info", key); w.Code != 400 {
		t.Fatalf("missing id: %d %s", w.Code, w.Body)
	}
	for _, path := range []string{"/api/tags", "/v1/models/info?id=fast", "/v1/models?kind=llm"} {
		if w := get(path, "invalid"); w.Code != 401 {
			t.Fatalf("invalid key: %s: %d", path, w.Code)
		}
	}
	paused := true
	if err := manager.UpdateAPIKey(ctx, entry.ID, &paused, nil); err != nil {
		t.Fatal(err)
	}
	if w := get("/api/tags", key); w.Code != 401 {
		t.Fatalf("paused key: %d", w.Code)
	}
	if _, err := manager.Update(ctx, func(candidate *runtime.Candidate) error { candidate.Set("requireApiKey", "false"); return nil }); err != nil {
		t.Fatal(err)
	}
	if w := get("/api/tags", ""); w.Code != 200 {
		t.Fatalf("optional key: %d", w.Code)
	}
}

// TestModelMissMessageCountsItsOwnKind pins #367 findings 1 and 5: the count
// belongs to the requested kind, the pointer names the path serving that kind,
// and a one-entry list is not "1 callable ids".
func TestModelMissMessageCountsItsOwnKind(t *testing.T) {
	manager := newManager(t)
	if _, err := manager.Update(context.Background(), func(candidate *runtime.Candidate) error {
		candidate.AddModel(runtime.Model{ProviderID: "typesafe", ID: "jev"})
		candidate.AddModel(runtime.Model{ProviderID: "alpha", ID: "chat"})
		candidate.AddModel(runtime.Model{ProviderID: "alpha", ID: "other"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	allowed := func(string) bool { return true }
	if message := unknownModelMessage(snapshot, allowed, "systemone"); !strings.Contains(message, "GET /v1/models?kind=systemone lists the 1 callable id;") {
		t.Fatalf("systemone miss: %s", message)
	}
	if message := unknownModelMessage(snapshot, allowed, ""); !strings.Contains(message, "GET /v1/models lists the 2 callable ids;") {
		t.Fatalf("llm miss: %s", message)
	}
}
