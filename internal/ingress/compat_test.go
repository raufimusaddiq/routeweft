package ingress

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/compat/fixtures"
	"github.com/raufimusaddiq/routeweft/compat/mockupstream"
	geminiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/gemini"
	ollamaadapter "github.com/raufimusaddiq/routeweft/internal/protocol/ollama"
	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	systemoneadapter "github.com/raufimusaddiq/routeweft/internal/protocol/systemone"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func compatFixtures(t *testing.T, protocol fixtures.Protocol) []fixtures.Exchange {
	t.Helper()
	all, err := fixtures.Load()
	if err != nil {
		t.Fatal(err)
	}
	var selected []fixtures.Exchange
	for _, exchange := range all {
		if exchange.Protocol == protocol {
			selected = append(selected, exchange)
		}
	}
	return selected
}

func compatFixture(t *testing.T, protocol fixtures.Protocol, id string) fixtures.Exchange {
	t.Helper()
	for _, exchange := range compatFixtures(t, protocol) {
		if exchange.ID == id {
			return exchange
		}
	}
	t.Fatalf("fixture %s/%s not found", protocol, id)
	return fixtures.Exchange{}
}

func keyedHandler(t *testing.T, opts Options) (http.Handler, string) {
	t.Helper()
	manager := newManager(t)
	_, key, err := manager.CreateAPIKey(context.Background(), "compat")
	if err != nil {
		t.Fatal(err)
	}
	handler := New(manager, opts)
	mux := http.NewServeMux()
	handler.Attach(mux)
	return mux, key
}

func fixedProvider(protocol, baseURL string) ProviderResolver {
	return func(*runtime.RuntimeSnapshot, string) (routing.ProviderRef, bool) {
		return routing.ProviderRef{ProviderID: protocol, Protocol: protocol, BaseURL: baseURL, APIToken: "provider-credential"}, true
	}
}

func TestGeminiGenerateContentNative(t *testing.T) {
	exchange := compatFixture(t, fixtures.Gemini, "gemini-generate-content")
	server, err := mockupstream.Start(compatFixtures(t, fixtures.Gemini))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	mux, key := keyedHandler(t, Options{AllowPrivateUpstreams: true, ProviderResolver: fixedProvider(geminiadapter.Protocol, server.URL())})
	recorder := httptest.NewRecorder()
	bearer(mux, key).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:generateContent", strings.NewReader(exchange.Request.Body)))
	if recorder.Code != http.StatusOK || recorder.Body.String() != exchange.Response.Body {
		t.Fatalf("gemini status %d body %s", recorder.Code, recorder.Body.String())
	}
	seen := server.Requests()
	if len(seen) != 1 || seen[0].Path != "/v1beta/models/gemini-2.5-flash:generateContent" || seen[0].Body != exchange.Request.Body {
		t.Fatalf("gemini upstream %+v", seen)
	}
	if got := seen[0].Headers.Get("X-Goog-Api-Key"); got != "provider-credential" {
		t.Fatalf("provider credential header %q", got)
	}
}

func TestGeminiStreamGenerateContentRelaysBytes(t *testing.T) {
	exchange := compatFixture(t, fixtures.Gemini, "gemini-streaming")
	server, err := mockupstream.Start(compatFixtures(t, fixtures.Gemini))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	mux, key := keyedHandler(t, Options{AllowPrivateUpstreams: true, ProviderResolver: fixedProvider(geminiadapter.Protocol, server.URL())})
	recorder := httptest.NewRecorder()
	bearer(mux, key).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1beta/models/gemini-2.5-flash:streamGenerateContent", strings.NewReader(exchange.Request.Body)))
	if recorder.Code != http.StatusOK || recorder.Body.String() != exchange.Response.Body {
		t.Fatalf("gemini stream status %d body %s", recorder.Code, recorder.Body.String())
	}
	if seen := server.Requests(); len(seen) != 1 || seen[0].Path != "/v1beta/models/gemini-2.5-flash:streamGenerateContent" {
		t.Fatalf("gemini upstream %+v", seen)
	}
}

func TestGeminiModelsListingUsesSnapshot(t *testing.T) {
	manager := newManager(t)
	_, key, err := manager.CreateAPIKey(context.Background(), "gemini-list")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Update(context.Background(), func(candidate *runtime.Candidate) error {
		candidate.AddModel(runtime.Model{ProviderID: "gemini", ID: "gemini-2.5-flash", Name: "Flash"})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	handler := New(manager, Options{})
	mux := http.NewServeMux()
	handler.Attach(mux)
	recorder := httptest.NewRecorder()
	bearer(mux, key).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/v1beta/models", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "models/gemini-2.5-flash") {
		t.Fatalf("gemini list status %d body %s", recorder.Code, recorder.Body.String())
	}
}

func TestGeminiRejectsUnsupportedAndMalformedRequests(t *testing.T) {
	mux, key := keyedHandler(t, Options{AllowPrivateUpstreams: true, ProviderResolver: fixedProvider(geminiadapter.Protocol, "http://127.0.0.1:1")})
	valid := `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`
	cases := []struct {
		name   string
		target string
		body   string
	}{
		{"unsupported method", "/v1beta/models/gemini-2.5-flash:countTokens", valid},
		{"empty body", "/v1beta/models/gemini-2.5-flash:generateContent", ""},
		{"missing contents", "/v1beta/models/gemini-2.5-flash:generateContent", `{"generationConfig":{}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			bearer(mux, key).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, tc.target, strings.NewReader(tc.body)))
			if recorder.Code != http.StatusNotFound && recorder.Code != http.StatusBadRequest {
				t.Fatalf("status %d", recorder.Code)
			}
		})
	}
}

func TestOllamaChatNativeReturnsNdjson(t *testing.T) {
	server, err := mockupstream.Start(nil, mockupstream.WithMatcher(func(_ *http.Request, _ string) mockupstream.Decision {
		return mockupstream.Decision{Status: http.StatusOK, Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"model":"llama3.2","message":{"role":"assistant","content":"Hi."},"done":true}`}
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	mux, key := keyedHandler(t, Options{AllowPrivateUpstreams: true, ProviderResolver: fixedProvider(ollamaadapter.Protocol, server.URL())})
	recorder := httptest.NewRecorder()
	bearer(mux, key).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/api/chat", strings.NewReader(`{"model":"llama3.2","messages":[{"role":"user","content":"Say hi."}],"stream":false}`)))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"done":true`) {
		t.Fatalf("ollama status %d body %s", recorder.Code, recorder.Body.String())
	}
	if got := recorder.Header().Get("Content-Type"); got != "application/x-ndjson" {
		t.Fatalf("ollama content type %q", got)
	}
	seen := server.Requests()
	if len(seen) != 1 || !strings.HasSuffix(seen[0].Path, "/api/chat") {
		t.Fatalf("ollama upstream %+v", seen)
	}
}

func TestOllamaChatRejectsInvalidChatBody(t *testing.T) {
	mux, key := keyedHandler(t, Options{AllowPrivateUpstreams: true, ProviderResolver: fixedProvider(ollamaadapter.Protocol, "http://127.0.0.1:1")})
	recorder := httptest.NewRecorder()
	bearer(mux, key).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/api/chat", strings.NewReader(`{"model":"","messages":[]}`)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("invalid ollama status %d", recorder.Code)
	}
}

func TestSystemOneNativePassthroughPreservesTypedEnvelope(t *testing.T) {
	exchange := compatFixture(t, fixtures.SystemOne, "systemone-nonstreaming")
	server, err := mockupstream.Start(compatFixtures(t, fixtures.SystemOne))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	mux, key := keyedHandler(t, Options{AllowPrivateUpstreams: true, ProviderResolver: fixedProvider(systemoneadapter.Protocol, server.URL()+"/v1/systemone")})
	recorder := httptest.NewRecorder()
	bearer(mux, key).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(exchange.Request.Body)))
	if recorder.Code != http.StatusOK || recorder.Body.String() != exchange.Response.Body {
		t.Fatalf("systemone status %d body %s", recorder.Code, recorder.Body.String())
	}
	seen := server.Requests()
	if len(seen) != 1 || seen[0].Path != "/v1/systemone" || seen[0].Body != exchange.Request.Body {
		t.Fatalf("systemone upstream %+v", seen)
	}
}

func TestSystemOneRequiresStateAndQuestions(t *testing.T) {
	mux, key := keyedHandler(t, Options{AllowPrivateUpstreams: true, ProviderResolver: fixedProvider(systemoneadapter.Protocol, "http://127.0.0.1:1")})
	recorder := httptest.NewRecorder()
	bearer(mux, key).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(`{"model":"jev-latest","state":{}}`)))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("systemone status %d", recorder.Code)
	}
}

func TestCompatRoutesRequireClientKey(t *testing.T) {
	manager := newManager(t)
	handler := New(manager, Options{ProviderResolver: fixedProvider(ollamaadapter.Protocol, "http://127.0.0.1:1")})
	mux := http.NewServeMux()
	handler.Attach(mux)
	for _, target := range []string{"/v1/api/chat", "/v1/systemone"} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, target, strings.NewReader(`{"model":"m","messages":[]}`)))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s status %d", target, recorder.Code)
		}
	}
}

func TestSystemOneModelRemapChangesOnlyModel(t *testing.T) {
	exchange := compatFixture(t, fixtures.SystemOne, "systemone-nonstreaming")
	server, err := mockupstream.Start(compatFixtures(t, fixtures.SystemOne))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	manager := newManager(t)
	_, key, err := manager.CreateAPIKey(context.Background(), "remap")
	if err != nil {
		t.Fatal(err)
	}
	handler := New(manager, Options{AllowPrivateUpstreams: true, ProviderResolver: func(*runtime.RuntimeSnapshot, string) (routing.ProviderRef, bool) {
		return routing.ProviderRef{ProviderID: "typesafe", Protocol: systemoneadapter.Protocol, BaseURL: server.URL() + "/v1/systemone", UpstreamModel: "jev-remapped"}, true
	}})
	mux := http.NewServeMux()
	handler.Attach(mux)
	recorder := httptest.NewRecorder()
	bearer(mux, key).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/systemone", strings.NewReader(exchange.Request.Body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("remap status %d", recorder.Code)
	}
	seen := server.Requests()
	if len(seen) != 1 || !strings.Contains(seen[0].Body, "jev-remapped") {
		t.Fatalf("remap body %+v", seen)
	}
}

func TestCompatTranslationHookEndpoints(t *testing.T) {
	server, err := mockupstream.Start(nil, mockupstream.WithMatcher(func(_ *http.Request, _ string) mockupstream.Decision {
		return mockupstream.Decision{Status: http.StatusOK, Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"ok":true}`}
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	manager := newManager(t)
	_, key, err := manager.CreateAPIKey(context.Background(), "hook")
	if err != nil {
		t.Fatal(err)
	}
	handler := New(manager, Options{AllowPrivateUpstreams: true,
		ProviderResolver: func(*runtime.RuntimeSnapshot, string) (routing.ProviderRef, bool) {
			return routing.ProviderRef{ProviderID: "openai", Protocol: "openai-chat", BaseURL: server.URL()}, true
		},
		TranslateChat: func(*openaiadapter.ChatRequest, string) (ChatTranslation, error) {
			return ChatTranslation{Endpoint: "chat/completions", Body: []byte(`{"model":"translated"}`), Headers: http.Header{"X-Target-Auth": {"provider-secret"}}}, nil
		},
	})
	mux := http.NewServeMux()
	handler.Attach(mux)
	recorder := httptest.NewRecorder()
	bearer(mux, key).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/api/chat", strings.NewReader(`{"model":"llama3.2","messages":[{"role":"user","content":"hi"}]}`)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("hook status %d body %s", recorder.Code, recorder.Body.String())
	}
	seen := server.Requests()
	if len(seen) != 1 || seen[0].Path != "/chat/completions" || seen[0].Body != `{"model":"translated"}` || seen[0].Headers.Get("X-Target-Auth") != "provider-secret" || seen[0].Headers.Get("Authorization") != "" {
		t.Fatalf("translated upstream %+v", seen)
	}
}
