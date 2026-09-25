package ingress

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/compat/fixtures"
	"github.com/raufimusaddiq/routeweft/compat/mockupstream"
	anthropicadapter "github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func anthropicFixtures(t *testing.T) []fixtures.Exchange {
	t.Helper()
	all, err := fixtures.Load()
	if err != nil {
		t.Fatal(err)
	}
	var selected []fixtures.Exchange
	for _, exchange := range all {
		if exchange.Protocol == fixtures.Anthropic {
			selected = append(selected, exchange)
		}
	}
	return selected
}

func anthropicFixture(t *testing.T, id string) fixtures.Exchange {
	t.Helper()
	for _, exchange := range anthropicFixtures(t) {
		if exchange.ID == id {
			return exchange
		}
	}
	t.Fatalf("fixture %s not found", id)
	return fixtures.Exchange{}
}

func anthropicHandler(t *testing.T, baseURL string) http.Handler {
	t.Helper()
	manager := newManager(t)
	_, key, err := manager.CreateAPIKey(context.Background(), "messages")
	if err != nil {
		t.Fatal(err)
	}
	handler := New(manager, Options{AllowPrivateUpstreams: true, ProviderResolver: func(_ *runtime.RuntimeSnapshot, model string) (routing.ProviderRef, bool) {
		if !strings.HasPrefix(model, "claude-") {
			return routing.ProviderRef{}, false
		}
		return routing.ProviderRef{ProviderID: "anthropic", Protocol: anthropicadapter.MessagesProtocol, BaseURL: baseURL + "/v1", APIToken: "provider-secret"}, true
	}})
	mux := http.NewServeMux()
	handler.Attach(mux)
	return bearer(mux, key)
}

func TestMessagesNativePassthroughPreservesHeadersAndBytes(t *testing.T) {
	server, err := mockupstream.Start(anthropicFixtures(t))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	exchange := anthropicFixture(t, "anthropic-messages-nonstreaming")
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(exchange.Request.Body))
	request.Header.Set("Anthropic-Beta", "prompt-caching-2024-07-31")
	anthropicHandler(t, server.URL()).ServeHTTP(recorder, request)
	if recorder.Code != exchange.Response.Status || recorder.Body.String() != exchange.Response.Body {
		t.Fatalf("response status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	seen := server.Requests()
	if len(seen) != 1 || seen[0].Path != "/v1/messages" || seen[0].Body != exchange.Request.Body || seen[0].Headers.Get("X-Api-Key") != "provider-secret" || seen[0].Headers.Get("Anthropic-Version") != "2023-06-01" || seen[0].Headers.Get("Anthropic-Beta") != "prompt-caching-2024-07-31" {
		t.Fatalf("upstream request %+v", seen)
	}
}

func TestMessagesAliasAndStreamTerminal(t *testing.T) {
	server, err := mockupstream.Start(anthropicFixtures(t))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	exchange := anthropicFixture(t, "anthropic-messages-streaming")
	recorder := httptest.NewRecorder()
	anthropicHandler(t, server.URL()).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/messages", strings.NewReader(exchange.Request.Body)))
	if recorder.Code != http.StatusOK || recorder.Body.String() != exchange.Response.Body {
		t.Fatalf("stream status=%d body mismatch", recorder.Code)
	}
	if strings.Count(recorder.Body.String(), "event: message_stop") != 1 {
		t.Fatal("message_stop terminal count incorrect")
	}
}

func TestMessagesCountTokensMatchesCharEstimator(t *testing.T) {
	manager := newManager(t)
	_, key, err := manager.CreateAPIKey(context.Background(), "count")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(manager, Options{}).Attach(mux)
	body := `{"system":"abcd","tools":[{"name":"f"}],"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`
	request := httptest.NewRequest(http.MethodPost, "/v1/messages/count_tokens", strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+key)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	var got struct {
		InputTokens int `json:"input_tokens"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.InputTokens != 4 {
		t.Fatalf("input_tokens=%d want=4", got.InputTokens)
	}
}

func TestMessagesTranslationHookAndErrorStatus(t *testing.T) {
	server, err := mockupstream.Start(nil, mockupstream.WithMatcher(func(_ *http.Request, _ string) mockupstream.Decision {
		return mockupstream.Decision{Status: 401, Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"error":{"type":"authentication_error"}}`}
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	manager := newManager(t)
	_, key, err := manager.CreateAPIKey(context.Background(), "translated")
	if err != nil {
		t.Fatal(err)
	}
	handler := New(manager, Options{AllowPrivateUpstreams: true, ProviderResolver: func(*runtime.RuntimeSnapshot, string) (routing.ProviderRef, bool) {
		return routing.ProviderRef{ProviderID: "target", Protocol: "openai-chat", BaseURL: server.URL() + "/v1"}, true
	}, TranslateMessages: func(_ *anthropicadapter.MessagesRequest, protocol string) (ChatTranslation, error) {
		if protocol != "openai-chat" {
			t.Fatalf("protocol %s", protocol)
		}
		return ChatTranslation{Endpoint: "chat/completions", Body: []byte(`{"model":"m"}`), Headers: http.Header{"Authorization": {"Bearer target-secret"}}}, nil
	}})
	mux := http.NewServeMux()
	handler.Attach(mux)
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-x","messages":[{"role":"user","content":"x"}]}`))
	request.Header.Set("Authorization", "Bearer "+key)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status %d", recorder.Code)
	}
}
