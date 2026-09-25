package ingress

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/compat/fixtures"
	"github.com/raufimusaddiq/routeweft/compat/mockupstream"
	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func responsesFixtures(t *testing.T) []fixtures.Exchange {
	t.Helper()
	all, err := fixtures.Load()
	if err != nil {
		t.Fatal(err)
	}
	var selected []fixtures.Exchange
	for _, exchange := range all {
		if exchange.Protocol == fixtures.OpenAIResponses {
			selected = append(selected, exchange)
		}
	}
	return selected
}

func responseFixture(t *testing.T, id string) fixtures.Exchange {
	t.Helper()
	for _, exchange := range responsesFixtures(t) {
		if exchange.ID == id {
			return exchange
		}
	}
	t.Fatalf("fixture %s not found", id)
	return fixtures.Exchange{}
}

func responsesHandler(t *testing.T, baseURL string) http.Handler {
	t.Helper()
	manager := newManager(t)
	_, key, err := manager.CreateAPIKey(context.Background(), "responses")
	if err != nil {
		t.Fatal(err)
	}
	handler := New(manager, Options{
		AllowPrivateUpstreams: true,
		ProviderResolver: func(_ *runtime.RuntimeSnapshot, model string) (routing.ProviderRef, bool) {
			if model != "gpt-4.1" && model != "fixture-model" {
				return routing.ProviderRef{}, false
			}
			return routing.ProviderRef{ProviderID: "openai", Protocol: openaiadapter.ResponsesProtocol, BaseURL: baseURL + "/v1"}, true
		},
	})
	mux := http.NewServeMux()
	handler.Attach(mux)
	return bearer(mux, key)
}

func TestResponsesNativeNonStreamingAndAliases(t *testing.T) {
	server, err := mockupstream.Start(responsesFixtures(t))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	exchange := responseFixture(t, "openai-responses-nonstreaming")
	handler := responsesHandler(t, server.URL())
	for _, path := range []string{"/v1/responses", "/responses", "/codex/responses", "/v1/v1/responses"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, strings.NewReader(exchange.Request.Body)))
		if recorder.Code != http.StatusOK || recorder.Body.String() != exchange.Response.Body {
			t.Fatalf("%s response status=%d body=%s", path, recorder.Code, recorder.Body.String())
		}
	}
	requests := server.Requests()
	if len(requests) != 4 {
		t.Fatalf("upstream requests: %d", len(requests))
	}
	for _, request := range requests {
		if request.Path != "/v1/responses" || request.Body != exchange.Request.Body {
			t.Fatalf("native request changed: %+v", request)
		}
	}
}

func TestResponsesStreamingPreservesNativeEventsAndTerminal(t *testing.T) {
	server, err := mockupstream.Start(responsesFixtures(t))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	exchange := responseFixture(t, "openai-responses-streaming")
	recorder := httptest.NewRecorder()
	responsesHandler(t, server.URL()).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(exchange.Request.Body)))
	if recorder.Code != http.StatusOK || recorder.Body.String() != exchange.Response.Body {
		t.Fatalf("stream status=%d body differs", recorder.Code)
	}
	if count := strings.Count(recorder.Body.String(), "event: response.completed"); count != 1 {
		t.Fatalf("terminal event count %d", count)
	}
}

func TestResponsesCompactUsesCompactPathAndStripsInternalFlag(t *testing.T) {
	server, err := mockupstream.Start(nil, mockupstream.WithMatcher(func(_ *http.Request, _ string) mockupstream.Decision {
		return mockupstream.Decision{Status: http.StatusOK, Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"id":"resp_compact","object":"response"}`}
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	body := `{"model":"gpt-4.1","_compact":true,"input":[{"role":"user","content":[{"type":"input_text","text":"x"}]}],"parallel_tool_calls":true,"tools":[],"reasoning":{"effort":"low"},"future":{"k":1}}`
	recorder := httptest.NewRecorder()
	responsesHandler(t, server.URL()).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses/compact", strings.NewReader(body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("compact status %d: %s", recorder.Code, recorder.Body.String())
	}
	requests := server.Requests()
	if len(requests) != 1 || requests[0].Path != "/v1/responses/compact" {
		t.Fatalf("compact dispatch %+v", requests)
	}
	if strings.Contains(requests[0].Body, `"_compact"`) || !strings.Contains(requests[0].Body, `"parallel_tool_calls":true`) || !strings.Contains(requests[0].Body, `"future":{"k":1}`) {
		t.Fatalf("compact body lost/ leaked fields: %s", requests[0].Body)
	}
}

func TestResponsesTranslationHook(t *testing.T) {
	server, err := mockupstream.Start(nil, mockupstream.WithMatcher(func(_ *http.Request, _ string) mockupstream.Decision {
		return mockupstream.Decision{Status: http.StatusOK, Headers: map[string]string{"Content-Type": "application/json"}, Body: `{}`}
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	manager := newManager(t)
	_, key, err := manager.CreateAPIKey(context.Background(), "translate-responses")
	if err != nil {
		t.Fatal(err)
	}
	handler := New(manager, Options{AllowPrivateUpstreams: true,
		ProviderResolver: func(*runtime.RuntimeSnapshot, string) (routing.ProviderRef, bool) {
			return routing.ProviderRef{ProviderID: "target", Protocol: "anthropic-messages", BaseURL: server.URL() + "/v1"}, true
		},
		TranslateResponses: func(req *openaiadapter.ResponsesRequest, protocol string) (ChatTranslation, error) {
			if protocol != "anthropic-messages" || !bytes.Contains(req.Raw, []byte("parallel_tool_calls")) {
				t.Fatal("Responses translation context incomplete")
			}
			return ChatTranslation{Endpoint: "messages", Body: []byte(`{"model":"translated"}`), Headers: http.Header{"X-Protocol": {"anthropic-messages"}}}, nil
		},
	})
	mux := http.NewServeMux()
	handler.Attach(mux)
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"gpt-4.1","input":[],"parallel_tool_calls":true}`))
	request.Header.Set("Authorization", "Bearer "+key)
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	seen := server.Requests()
	if recorder.Code != http.StatusOK || len(seen) != 1 || seen[0].Path != "/v1/messages" || seen[0].Headers.Get("X-Protocol") != "anthropic-messages" {
		t.Fatalf("translation response=%d requests=%+v", recorder.Code, seen)
	}
}

func TestResponsesTerminalEventNormalization(t *testing.T) {
	cases := []struct{ name, body, want string }{
		{name: "missing", body: "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\"}\n\n", want: "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\"}\n\nevent: response.failed\ndata: {\"type\":\"response.failed\",\"sequence_number\":0,\"response\":{\"status\":\"failed\",\"error\":{\"code\":\"upstream_incomplete\",\"message\":\"upstream stream ended without a terminal event\"}}}\n\n"},
		{name: "duplicate-and-nonfinal", body: "event: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":1}\n\nevent: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":3}\n\n", want: "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\"}\n\nevent: response.completed\ndata: {\"type\":\"response.completed\",\"sequence_number\":3}\n\n"},
		{name: "long-event-then-terminal", body: "event: response.output_text.delta\ndata: " + strings.Repeat("z", 4096) + "\n\nevent: response.completed\ndata: {\"type\":\"response.completed\"}", want: "event: response.output_text.delta\ndata: " + strings.Repeat("z", 4096) + "\n\nevent: response.completed\ndata: {\"type\":\"response.completed\"}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			writer := httptest.NewRecorder()
			handler := New(nil, Options{})
			handler.copyResponsesStream(writer, httptest.NewRequest(http.MethodPost, "/", nil), &http.Response{Body: io.NopCloser(strings.NewReader(tc.body))})
			if got := writer.Body.String(); got != tc.want {
				t.Fatalf("stream differs\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}
