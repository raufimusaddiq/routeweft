package ingress

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/compat/fixtures"
	"github.com/raufimusaddiq/routeweft/compat/mockupstream"
	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

func chatFixtures(t *testing.T) []fixtures.Exchange {
	t.Helper()
	all, err := fixtures.Load()
	if err != nil {
		t.Fatal(err)
	}
	var chat []fixtures.Exchange
	for _, exchange := range all {
		if exchange.Protocol == fixtures.OpenAIChat {
			chat = append(chat, exchange)
		}
	}
	return chat
}

func fixtureByID(t *testing.T, id string) fixtures.Exchange {
	t.Helper()
	for _, exchange := range chatFixtures(t) {
		if exchange.ID == id {
			return exchange
		}
	}
	t.Fatalf("fixture %s not found", id)
	return fixtures.Exchange{}
}

func chatHandler(t *testing.T, baseURL string) http.Handler {
	t.Helper()
	manager := newManager(t)
	_, key, err := manager.CreateAPIKey(context.Background(), "chat")
	if err != nil {
		t.Fatal(err)
	}
	handler := New(manager, Options{
		AllowPrivateUpstreams: true,
		ProviderResolver: func(_ *runtime.RuntimeSnapshot, model string) (routing.ProviderRef, bool) {
			if model != "gpt-4o-mini" && model != "fixture-model" {
				return routing.ProviderRef{}, false
			}
			return routing.ProviderRef{ProviderID: "openai", Protocol: openaiadapter.ChatProtocol, BaseURL: baseURL + "/v1"}, true
		},
	})
	mux := http.NewServeMux()
	handler.Attach(mux)
	t.Cleanup(func() {})
	return bearer(mux, key)
}

func bearer(next http.Handler, key string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.Header.Set("Authorization", "Bearer "+key)
		next.ServeHTTP(w, r)
	})
}

func TestChatCompletionsNativeNonStreaming(t *testing.T) {
	server, err := mockupstream.Start(chatFixtures(t))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	exchange := fixtureByID(t, "openai-chat-nonstreaming")
	handler := chatHandler(t, server.URL())

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(exchange.Request.Body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d body %s", recorder.Code, recorder.Body.String())
	}
	if recorder.Body.String() != exchange.Response.Body {
		t.Fatalf("response body not native passthrough")
	}
	requests := server.Requests()
	if len(requests) != 1 || requests[0].Path != "/v1/chat/completions" {
		t.Fatalf("upstream requests %+v", requests)
	}
	if requests[0].Body != exchange.Request.Body {
		t.Fatalf("upstream body changed: %s", requests[0].Body)
	}
}

func TestChatTranslatorUsesTargetEndpointAndHeaders(t *testing.T) {
	server, err := mockupstream.Start(nil, mockupstream.WithMatcher(func(_ *http.Request, _ string) mockupstream.Decision {
		return mockupstream.Decision{Status: http.StatusOK, Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"ok":true}`}
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	manager := newManager(t)
	_, key, err := manager.CreateAPIKey(context.Background(), "translation")
	if err != nil {
		t.Fatal(err)
	}
	handler := New(manager, Options{
		AllowPrivateUpstreams: true,
		ProviderResolver: func(*runtime.RuntimeSnapshot, string) (routing.ProviderRef, bool) {
			return routing.ProviderRef{ProviderID: "anthropic", Protocol: "anthropic-messages", BaseURL: server.URL() + "/v1", APIToken: "provider-secret"}, true
		},
		TranslateChat: func(_ *openaiadapter.ChatRequest, protocol string) (ChatTranslation, error) {
			if protocol != "anthropic-messages" {
				t.Fatalf("target protocol %q", protocol)
			}
			return ChatTranslation{Endpoint: "messages", Body: []byte(`{"model":"target-model"}`), Headers: http.Header{"X-Target-Auth": {"provider-secret"}}}, nil
		},
	})
	mux := http.NewServeMux()
	handler.Attach(mux)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"x"}]}`))
	request.Header.Set("Authorization", "Bearer "+key)
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d body %s", recorder.Code, recorder.Body.String())
	}
	seen := server.Requests()
	if len(seen) != 1 || seen[0].Path != "/v1/messages" || seen[0].Body != `{"model":"target-model"}` || seen[0].Headers.Get("X-Target-Auth") != "provider-secret" || seen[0].Headers.Get("Authorization") != "" {
		t.Fatalf("translated request %+v", seen)
	}
}

func TestChatCompletionsStreamsBytesAndCancelsUpstream(t *testing.T) {
	exchange := fixtureByID(t, "openai-chat-streaming")
	server, err := mockupstream.Start(chatFixtures(t))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	if err := openaiadapter.ValidateStreamTerminal(exchange.Response.Body); err != nil {
		t.Fatalf("fixture stream invalid: %v", err)
	}
	handler := chatHandler(t, server.URL())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(exchange.Request.Body)))
	if recorder.Code != http.StatusOK || recorder.Body.String() != exchange.Response.Body {
		t.Fatalf("stream mismatch status=%d", recorder.Code)
	}
}

func TestChatStreamEnforcesSingleFinalTerminalMarker(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{name: "missing", body: "data: {\"chunk\":1}\n\n", want: "data: {\"chunk\":1}\n\ndata: [DONE]\n\n"},
		{name: "duplicate", body: "data: [DONE]\n\ndata: {\"late\":true}\n\ndata: [DONE]\n\n", want: "data: {\"late\":true}\n\ndata: [DONE]\n\n"},
		{name: "already-final", body: "data: {\"chunk\":1}\n\ndata: [DONE]\n\n", want: "data: {\"chunk\":1}\n\ndata: [DONE]\n\n"},
		{name: "large-duplicate", body: "data: {\"pad\":\"" + strings.Repeat("x", 200) + "\"}\n\ndata: [DONE]\n\ndata: " + strings.Repeat("y", 128) + "\n\ndata: [DONE]\n\n", want: "data: {\"pad\":\"" + strings.Repeat("x", 200) + "\"}\n\ndata: " + strings.Repeat("y", 128) + "\n\ndata: [DONE]\n\n"},
		{name: "long-non-terminal", body: "data: " + strings.Repeat("z", 4096) + "\n\ndata: [DONE]\n\n", want: "data: " + strings.Repeat("z", 4096) + "\n\ndata: [DONE]\n\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := New(nil, Options{AllowPrivateUpstreams: true})
			recorder := httptest.NewRecorder()
			response := &http.Response{Body: io.NopCloser(strings.NewReader(tc.body))}
			handler.copyStream(recorder, httptest.NewRequest(http.MethodPost, "/", nil), response)
			if got := recorder.Body.String(); got != tc.want {
				t.Fatalf("stream %q, want %q", got, tc.want)
			}
			if err := openaiadapter.ValidateStreamTerminal(recorder.Body.String()); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestChatCompletionsRejectsBadRequests(t *testing.T) {
	server, err := mockupstream.Start(chatFixtures(t))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	handler := chatHandler(t, server.URL())

	missingModel := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"messages":[{"role":"user","content":"x"}]}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, missingModel)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing model status %d", recorder.Code)
	}

	malformed := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("not json"))
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, malformed)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("malformed status %d", recorder.Code)
	}

	unknown := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"nope","messages":[{"role":"user","content":"x"}]}`))
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, unknown)
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("unknown model status %d", recorder.Code)
	}
}

func TestChatCompletionsPropagatesClientCancellation(t *testing.T) {
	exchange := fixtureByID(t, "openai-chat-cancellation")
	server, err := mockupstream.Start(chatFixtures(t))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	handler := chatHandler(t, server.URL())
	delayed, err := mockupstream.Start(chatFixtures(t), mockupstream.WithMatcher(func(_ *http.Request, _ string) mockupstream.Decision {
		return mockupstream.Decision{Status: http.StatusOK, Headers: map[string]string{"Content-Type": "text/event-stream"}, Body: "data: {\"partial\":true}\n\n", Delay: time.Second}
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer delayed.Close()
	handler = chatHandler(t, delayed.URL())
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(exchange.Request.Body)).WithContext(ctx)
	go func() {
		deadline := time.After(time.Second)
		for len(delayed.Requests()) == 0 {
			select {
			case <-deadline:
				return
			case <-time.After(time.Millisecond):
			}
		}
		cancel()
	}()
	start := time.Now()
	recorder := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		handler.ServeHTTP(recorder, request)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not return after client cancellation")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancellation took %s", elapsed)
	}
}

func TestUpstreamAuthErrorIsRelayed(t *testing.T) {
	exchange := fixtureByID(t, "openai-chat-auth-error")
	server, err := mockupstream.Start(chatFixtures(t))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	handler := chatHandler(t, server.URL())
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(exchange.Request.Body)))
	if recorder.Code != exchange.Response.Status {
		t.Fatalf("status %d", recorder.Code)
	}
	if strings.Contains(recorder.Body.String(), "fixture-client-key") {
		t.Fatal("upstream error leaked client credential")
	}
}

func TestChatCompletionsOverBodyLimitRejected(t *testing.T) {
	manager := newManager(t)
	_, key, err := manager.CreateAPIKey(context.Background(), "limit")
	if err != nil {
		t.Fatal(err)
	}
	handler := New(manager, Options{MaxBodyBytes: 8, ProviderResolver: func(*runtime.RuntimeSnapshot, string) (routing.ProviderRef, bool) {
		return routing.ProviderRef{ProviderID: "p", Protocol: "openai-chat", BaseURL: "http://127.0.0.1:1"}, true
	}})
	mux := http.NewServeMux()
	handler.Attach(mux)
	recorder := httptest.NewRecorder()
	bearer(mux, key).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m"}`)))
	if recorder.Code != http.StatusRequestEntityTooLarge && recorder.Code != http.StatusBadRequest {
		t.Fatalf("oversize status %d", recorder.Code)
	}
}

func TestChatHandlerPropagatesUpstreamFailure(t *testing.T) {
	manager := newManager(t)
	_, key, err := manager.CreateAPIKey(context.Background(), "dead")
	if err != nil {
		t.Fatal(err)
	}
	dead := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		http.Error(w, "upstream down", http.StatusBadGateway)
	}))
	defer dead.Close()
	handler := New(manager, Options{AllowPrivateUpstreams: true, ProviderResolver: func(*runtime.RuntimeSnapshot, string) (routing.ProviderRef, bool) {
		return routing.ProviderRef{ProviderID: "p", Protocol: "openai-chat", BaseURL: dead.URL}, true
	}})
	mux := http.NewServeMux()
	handler.Attach(mux)
	recorder := httptest.NewRecorder()
	bearer(mux, key).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"m","messages":[{"role":"user","content":"x"}]}`)))
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("dead upstream status %d", recorder.Code)
	}
}

func BenchmarkChatCompletionsNative(b *testing.B) {
	exchanges, err := fixtures.Load()
	if err != nil {
		b.Fatal(err)
	}
	var fixture fixtures.Exchange
	for _, exchange := range exchanges {
		if exchange.ID == "openai-chat-nonstreaming" {
			fixture = exchange
			break
		}
	}
	if fixture.ID == "" {
		b.Fatal("OpenAI Chat fixture missing")
	}
	server, err := mockupstream.Start(exchanges)
	if err != nil {
		b.Fatal(err)
	}
	defer server.Close()
	store, err := sqlite.Open(context.Background(), b.TempDir()+"/routeweft.sqlite")
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	if err := migrations.NewRunner(store.DB()).Apply(context.Background()); err != nil {
		b.Fatal(err)
	}
	manager, err := runtime.NewManager(context.Background(), store.DB())
	if err != nil {
		b.Fatal(err)
	}
	_, key, err := manager.CreateAPIKey(context.Background(), "benchmark")
	if err != nil {
		b.Fatal(err)
	}
	options := Options{AllowPrivateUpstreams: true, ProviderResolver: func(*runtime.RuntimeSnapshot, string) (routing.ProviderRef, bool) {
		return routing.ProviderRef{ProviderID: "openai", Protocol: openaiadapter.ChatProtocol, BaseURL: server.URL() + "/v1"}, true
	}}
	mux := http.NewServeMux()
	New(manager, options).Attach(mux)
	body := []byte(fixture.Request.Body)
	b.SetBytes(int64(len(body) + len(fixture.Response.Body)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(body))
		request.Header.Set("Authorization", "Bearer "+key)
		mux.ServeHTTP(recorder, request)
		if recorder.Code != fixture.Response.Status {
			b.Fatalf("status %d, want %d", recorder.Code, fixture.Response.Status)
		}
	}
}
