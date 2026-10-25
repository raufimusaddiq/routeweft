package ingress

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/protocol/hub"
	kiroadapter "github.com/raufimusaddiq/routeweft/internal/protocol/kiro"
	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// Kiro's four legs exist and the pin dispatches the Messages pair directly. A
// Messages client must not pivot through Chat where the pin has a direct pair.
// The endpoint binding (three-host order, region rewrite, 401/403/404 fallback)
// is built and proven end to end in TestKiroIsServedToEveryClientWire, so every
// client wire reaches Kiro.
func TestKiroLegsAreDeclaredAndKiroIsRoutable(t *testing.T) {
	for _, leg := range []hub.Leg{
		{From: openai.ChatProtocol, To: kiroadapter.Protocol, Kind: hub.Request},
		{From: kiroadapter.Protocol, To: openai.ChatProtocol, Kind: hub.Response},
		{From: anthropic.MessagesProtocol, To: kiroadapter.Protocol, Kind: hub.Request},
		{From: kiroadapter.Protocol, To: anthropic.MessagesProtocol, Kind: hub.Response},
	} {
		if !hub.Has(leg.From, leg.To, leg.Kind) {
			t.Errorf("leg %v is not declared", leg)
		}
	}
	for _, kind := range []hub.Kind{hub.Request, hub.Response} {
		from, to := anthropic.MessagesProtocol, kiroadapter.Protocol
		if kind == hub.Response {
			from, to = to, from
		}
		if route, ok := hub.Route(from, to, kind); !ok || len(route) != 1 {
			t.Errorf("%s Messages<->Kiro is %v, want the direct leg", kind, route)
		}
	}
	for _, client := range []string{openai.ChatProtocol, openai.ResponsesProtocol, anthropic.MessagesProtocol} {
		if !hub.CanTranslate(client, kiroadapter.Protocol) {
			t.Errorf("%s client has no legs to Kiro", client)
		}
		if !CanTranslate(client, kiroadapter.Protocol) {
			t.Errorf("%s client cannot reach Kiro", client)
		}
	}
}

// Every translator arm a client can reach must exist, or a request would be routed
// to Kiro and fail at translation.
func TestKiroHasATranslatorForEveryClientWire(t *testing.T) {
	chat, _ := openai.ParseChatRequest([]byte(`{"model":"claude-sonnet-4.5","messages":[{"role":"user","content":"hi"}]}`))
	if _, err := TranslateChat(chat, kiroadapter.Protocol); err != nil {
		t.Errorf("Chat: %v", err)
	}
	responses, err := openai.ParseResponsesRequest([]byte(`{"model":"claude-sonnet-4.5","input":"hi"}`), false)
	if err != nil {
		t.Fatal(err)
	}
	translation, err := TranslateResponses(responses, kiroadapter.Protocol)
	if err != nil {
		t.Fatalf("Responses: %v", err)
	}
	frames := append(kiroFrame("assistantResponseEvent", `{"content":"yo"}`), kiroFrame("messageStopEvent", `{}`)...)
	body, err := translation.Response(frames)
	if err != nil || !strings.Contains(string(body), `"yo"`) || !strings.Contains(string(body), `"object":"response"`) {
		t.Errorf("Responses answer: %v %s", err, body)
	}
	var out bytes.Buffer
	if err := translation.Stream(context.Background(), bytes.NewReader(frames), &out); err != nil || !strings.Contains(out.String(), "response.completed") {
		t.Errorf("Responses stream: %v %s", err, out.String())
	}
	compact := *responses
	compact.Compact = true
	if _, err := TranslateResponses(&compact, kiroadapter.Protocol); err == nil {
		t.Error("a compact Responses request was translated")
	}
	messages, err := anthropic.ParseMessagesRequest([]byte(`{"model":"claude-sonnet-4.5","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := TranslateMessages(messages, kiroadapter.Protocol); err != nil {
		t.Errorf("Messages: %v", err)
	}
}

func TestMessagesTranslationForKiro(t *testing.T) {
	request, err := anthropic.ParseMessagesRequest([]byte(`{"model":"claude-sonnet-4.5","max_tokens":64,"stream":true,
		"tools":[{"name":"a.b","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	translation, err := TranslateMessages(request, kiroadapter.Protocol)
	if err != nil {
		t.Fatal(err)
	}
	if translation.Endpoint != "generateAssistantResponse" || !strings.Contains(string(translation.Body), `"name":"a_b"`) || !strings.Contains(string(translation.Body), `"maxTokens":64`) {
		t.Fatalf("%s %s", translation.Endpoint, translation.Body)
	}
	// The names the request leg rewrote are put back on the way out, in Messages terms.
	frames := kiroToolTurn()
	var out bytes.Buffer
	if err := translation.Stream(context.Background(), bytes.NewReader(frames), &out); err != nil ||
		!strings.Contains(out.String(), `"name":"a.b"`) || !strings.Contains(out.String(), `"stop_reason":"tool_use"`) || !strings.Contains(out.String(), "event: message_stop") {
		t.Fatalf("%v %s", err, out.String())
	}
	body, err := translation.Response(frames)
	if err != nil || !strings.Contains(string(body), `"name":"a.b"`) || !strings.Contains(string(body), `"type":"message"`) {
		t.Fatalf("%v %s", err, body)
	}
	// A request the wire cannot carry is refused before anything is sent.
	for name, bad := range map[string]string{
		"url image":   `{"model":"m","max_tokens":5,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://x/y.png"}}]}]}`,
		"document":    `{"model":"m","max_tokens":5,"messages":[{"role":"user","content":[{"type":"text","text":"x"},{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"aGk="}}]}]}`,
		"top_k":       `{"model":"m","max_tokens":5,"top_k":3,"messages":[{"role":"user","content":"hi"}]}`,
		"prefill":     `{"model":"m","max_tokens":5,"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"The"}]}`,
		"tool_choice": `{"model":"m","max_tokens":5,"tool_choice":{"type":"any"},"tools":[{"name":"t","input_schema":{}}],"messages":[{"role":"user","content":"hi"}]}`,
	} {
		parsed, err := anthropic.ParseMessagesRequest([]byte(bad))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if _, err := TranslateMessages(parsed, kiroadapter.Protocol); err == nil || !strings.Contains(err.Error(), "unsupported") {
			t.Errorf("%s: expected a refusal, got %v", name, err)
		}
	}
}

// The interleaved-thinking beta is an inbound request header: the Messages leg
// reads it and nothing is forwarded to Kiro (executors/kiro.js sends none).
func TestMessagesBetaHeaderSelectsThinkingAndIsNotForwarded(t *testing.T) {
	var seen struct {
		sync.Mutex
		header http.Header
		body   string
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		seen.Lock()
		seen.header, seen.body = r.Header.Clone(), string(raw)
		seen.Unlock()
		w.Header().Set("Content-Type", "application/vnd.amazon.eventstream")
		_, _ = w.Write(append(kiroFrame("assistantResponseEvent", `{"content":"<thinking>plan</thinking>\nanswer"}`), kiroFrame("messageStopEvent", `{}`)...))
	}))
	defer upstream.Close()
	// The handler's own seam is Options.TranslateMessages: the provider speaks
	// Chat, and the translator it is given is the Kiro one, so the real header
	// plumbing, request leg and response leg run against a local upstream (the
	// real Kiro hosts are not reachable from a test).
	ref := routing.ProviderRef{ProviderID: "seam", Protocol: openai.ChatProtocol, BaseURL: upstream.URL, APIToken: "fixture-token"}
	toKiro := func(request *anthropic.MessagesRequest, _ string) (ChatTranslation, error) {
		return TranslateMessages(request, kiroadapter.Protocol)
	}
	mux, key := keyedHandler(t, Options{AllowPrivateUpstreams: true, TranslateChat: TranslateChat, TranslateResponses: TranslateResponses, TranslateMessages: toKiro,
		ProviderResolver: func(*runtime.RuntimeSnapshot, string) (routing.ProviderRef, bool) { return ref, true }})
	call := func(beta, extra string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(`{"model":"claude-sonnet-4.5","max_tokens":50`+extra+`,"messages":[{"role":"user","content":"hi"}]}`))
		if beta != "" {
			request.Header.Set("Anthropic-Beta", beta)
		}
		out := httptest.NewRecorder()
		bearer(mux, key).ServeHTTP(out, request)
		return out
	}
	out := call("claude-code-20250219,interleaved-thinking-2025-05-14", "")
	seen.Lock()
	body, header := seen.body, seen.header
	seen.Unlock()
	if out.Code != 200 || !strings.Contains(body, "thinking_mode") {
		t.Fatalf("status %d: upstream body %s, response %s", out.Code, body, out.Body)
	}
	if header.Get("Anthropic-Beta") != "" {
		t.Fatalf("headers %v", header)
	}
	// The thinking tags the model answered with come back as a thinking block.
	if !strings.Contains(out.Body.String(), `"type":"thinking"`) || !strings.Contains(out.Body.String(), `"text":"answer"`) {
		t.Fatalf("%s", out.Body)
	}
	if out := call("", ""); out.Code != 200 {
		t.Fatalf("status %d %s", out.Code, out.Body)
	}
	seen.Lock()
	body = seen.body
	seen.Unlock()
	if strings.Contains(body, "thinking_mode") {
		t.Fatalf("thinking was switched on without the header: %s", body)
	}
	if out := call("interleaved-thinking-2025-05-14", `,"thinking":{"type":"disabled"}`); out.Code != 200 {
		t.Fatalf("status %d %s", out.Code, out.Body)
	}
	seen.Lock()
	body = seen.body
	seen.Unlock()
	if strings.Contains(body, "thinking_mode") {
		t.Fatalf("explicit disabled thinking lost to the header: %s", body)
	}
	// A request Kiro cannot carry never reaches it.
	seen.Lock()
	seen.body = ""
	seen.Unlock()
	if out := call("", `,"top_k":4`); out.Code < 400 {
		t.Fatalf("status %d %s", out.Code, out.Body)
	}
	seen.Lock()
	reached := seen.body
	seen.Unlock()
	if reached != "" {
		t.Fatalf("a refused request reached Kiro: %s", reached)
	}
}

func TestChatTranslationForKiro(t *testing.T) {
	request, err := openai.ParseChatRequest([]byte(`{"model":"claude-sonnet-4.5","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"a.b"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	translation, err := TranslateChat(request, kiroadapter.Protocol)
	if err != nil {
		t.Fatal(err)
	}
	if translation.Endpoint != "generateAssistantResponse" || !strings.Contains(string(translation.Body), `"name":"a_b"`) {
		t.Fatalf("%s %s", translation.Endpoint, translation.Body)
	}
	// The names the request leg rewrote are put back on the way out.
	frames := kiroToolTurn()
	var out bytes.Buffer
	if err := translation.Stream(context.Background(), bytes.NewReader(frames), &out); err != nil || !strings.Contains(out.String(), `"name":"a.b"`) {
		t.Fatalf("%v %s", err, out.String())
	}
	body, err := translation.Response(frames)
	if err != nil || !strings.Contains(string(body), `"name":"a.b"`) {
		t.Fatalf("%v %s", err, body)
	}
	// A request the wire cannot carry is refused before anything is sent.
	bad, _ := openai.ParseChatRequest([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stop":["x"]}`))
	if _, err := TranslateChat(bad, kiroadapter.Protocol); err == nil {
		t.Fatal("expected a refusal")
	}
}
