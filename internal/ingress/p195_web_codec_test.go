package ingress

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// grok-web and perplexity-web are converted by a hook Codec (#195): the pin has no
// translator leg for them. They stay in ingress.unwired until the owner approves it
// after a supervised live check, so these tests lift the fence for their own run
// only and prove the whole path from each client wire with offline fixtures.

func liftFence(t *testing.T, protocols ...string) {
	t.Helper()
	lifted := map[string]bool{}
	for _, protocol := range protocols {
		lifted[protocol] = unwired[protocol]
		delete(unwired, protocol)
	}
	t.Cleanup(func() {
		for protocol, was := range lifted {
			if was {
				unwired[protocol] = true
			}
		}
	})
}

func TestWebCodecProvidersStayFencedAndAreReachableOnlyThroughHasCodec(t *testing.T) {
	for _, target := range []string{"grok-web", "perplexity-web"} {
		for _, source := range clientWires {
			if CanTranslate(source, target) {
				t.Fatalf("%s is routable from %s while it is in unwired", target, source)
			}
		}
	}
	liftFence(t, "grok-web", "perplexity-web")
	for _, target := range []string{"grok-web", "perplexity-web"} {
		for _, source := range clientWires {
			if !CanTranslate(source, target) {
				t.Errorf("%s is not reachable from %s through its codec", target, source)
			}
		}
		// A codec takes Chat: no other client wire and no hub leg reaches it.
		for _, source := range []string{"ollama", "gemini", "commandcode", target} {
			if CanTranslate(source, target) {
				t.Errorf("%s reaches %s", source, target)
			}
		}
	}
	h := &Handler{opts: Options{TranslateChat: TranslateChat, TranslateResponses: TranslateResponses, TranslateResponsesCompact: TranslateResponses, TranslateMessages: TranslateMessages}}
	if !h.translationAvailable(openai.ResponsesProtocol, "grok-web", false) || h.translationAvailable(openai.ResponsesProtocol, "grok-web", true) {
		t.Fatal("Responses Compact has no codec, Responses does")
	}
}

const grokFixture = `{"result":{"response":{"llmInfo":{"modelHash":"h1"}}}}
{"result":{"response":{"token":"pong"}}}
{"result":{"response":{"modelResponse":{"message":"pong"}}}}
`

func pplxFixture(uuid, text string) string {
	return fmt.Sprintf("data: {\"backend_uuid\":%q,\"blocks\":[{\"intended_usage\":\"ask_text_0_markdown\",\"markdown_block\":{\"progress\":\"IN_PROGRESS\",\"chunks\":[%q]}}]}\n\ndata: {\"final\":true}\n\n", uuid, text)
}

type webUpstream struct {
	*httptest.Server
	mu   sync.Mutex
	seen []webRequest
}

type webRequest struct {
	path, cookie, auth, agent string
	body                      map[string]any
}

func newWebUpstream(t *testing.T, status int, answer func(n int) string) *webUpstream {
	t.Helper()
	u := &webUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		got := webRequest{path: r.URL.Path, cookie: r.Header.Get("Cookie"), auth: r.Header.Get("Authorization"), agent: r.Header.Get("User-Agent"), body: map[string]any{}}
		_ = json.Unmarshal(raw, &got.body)
		u.mu.Lock()
		u.seen = append(u.seen, got)
		n := len(u.seen)
		u.mu.Unlock()
		w.WriteHeader(status)
		_, _ = io.WriteString(w, answer(n))
	}))
	t.Cleanup(u.Close)
	return u
}

func newWebServing(t *testing.T, ref routing.ProviderRef, state *runtime.RuntimeState) serving {
	t.Helper()
	manager := newManager(t)
	_, key, err := manager.CreateAPIKey(context.Background(), "p195")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var outcomes []RequestOutcome
	handler := New(manager, Options{
		AllowPrivateUpstreams: true, State: state,
		TranslateChat: TranslateChat, TranslateResponses: TranslateResponses, TranslateMessages: TranslateMessages,
		Candidates: func(*runtime.RuntimeSnapshot, string) ([]routing.Account, bool) {
			return []routing.Account{{ID: ref.ConnectionID, Enabled: true}}, true
		},
		AccountProvider: func(_ *runtime.RuntimeSnapshot, _, account, source string) (routing.ProviderRef, bool) {
			if !CanTranslate(source, ref.Protocol) {
				return routing.ProviderRef{}, false
			}
			return ref, true
		},
		OnRequestComplete: func(outcome RequestOutcome) { mu.Lock(); outcomes = append(outcomes, outcome); mu.Unlock() },
	})
	mux := http.NewServeMux()
	handler.Attach(mux)
	return serving{mux, key, handler, func() []RequestOutcome {
		mu.Lock()
		defer mu.Unlock()
		return append([]RequestOutcome(nil), outcomes...)
	}}
}

func TestGrokWebIsServedToEveryClientWire(t *testing.T) {
	liftFence(t, "grok-web")
	for _, client := range clientWires {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", client, stream), func(t *testing.T) {
				up := newWebUpstream(t, 200, func(int) string { return grokFixture })
				s := newWebServing(t, routing.ProviderRef{ProviderID: "grok-web", Protocol: "grok-web", BaseURL: up.URL, UpstreamModel: "grok-4", APIToken: "sso=fixture-sso", ConnectionID: "conn-a"}, nil)
				w := s.call(client, stream)
				if w.Code != 200 || !strings.Contains(w.Body.String(), "pong") {
					t.Fatalf("client got %d: %s", w.Code, w.Body)
				}
				got := up.seen[0]
				if got.path != "/rest/app-chat/conversations/new" || got.cookie != "sso=fixture-sso" || got.auth != "" || !strings.Contains(got.agent, "Chrome/136") {
					t.Errorf("upstream saw %+v", got)
				}
				if got.body["modelName"] != "grok-4" || got.body["message"] != "ping" {
					t.Errorf("envelope %v", got.body)
				}
				outcomes := s.outcomes()
				if len(outcomes) != 1 || outcomes[0].Status != 200 || outcomes[0].ProviderID != "grok-web" {
					t.Fatalf("outcomes %+v", outcomes)
				}
				// The pin's length/4 figure may reach the client; accounting never books it.
				if !outcomes[0].UsageUnobserved || outcomes[0].InputTokens != 0 || outcomes[0].OutputTokens != 0 {
					t.Errorf("an estimate was recorded as measured usage: %+v", outcomes[0])
				}
			})
		}
	}
}

func TestGrokWebBufferedChatCarriesThePinsEstimateOnly(t *testing.T) {
	liftFence(t, "grok-web")
	up := newWebUpstream(t, 200, func(int) string { return grokFixture })
	s := newWebServing(t, routing.ProviderRef{ProviderID: "grok-web", Protocol: "grok-web", BaseURL: up.URL, APIToken: "fixture-sso", ConnectionID: "conn-a"}, nil)
	w := s.call(openai.ChatProtocol, false)
	var body struct {
		Usage struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
		}
	}
	_ = json.Unmarshal(w.Body.Bytes(), &body)
	if w.Code != 200 || body.Usage.Prompt != 1 || body.Usage.Completion != 1 {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
}

func TestWebSiteFailureIsRecordedAsFailedWhileTheClientSeesThePinsFrame(t *testing.T) {
	liftFence(t, "grok-web")
	const failing = `{"result":{"response":{"token":"par"}}}` + "\n" + `{"error":{"message":"rate limited"}}` + "\n"
	for _, client := range clientWires {
		t.Run(client+"/stream", func(t *testing.T) {
			up := newWebUpstream(t, 200, func(int) string { return failing })
			s := newWebServing(t, routing.ProviderRef{ProviderID: "grok-web", Protocol: "grok-web", BaseURL: up.URL, APIToken: "fixture-sso", ConnectionID: "conn-a"}, nil)
			w := s.call(client, true)
			if w.Code != 200 || !strings.Contains(w.Body.String(), "[Error: rate limited]") {
				t.Fatalf("client got %d: %s", w.Code, w.Body)
			}
			outcomes := s.outcomes()
			if len(outcomes) != 1 || outcomes[0].Status != http.StatusBadGateway || !outcomes[0].UsageUnobserved {
				t.Fatalf("the failed attempt was not recorded as failed: %+v", outcomes)
			}
		})
	}
	up := newWebUpstream(t, 200, func(int) string { return failing })
	s := newWebServing(t, routing.ProviderRef{ProviderID: "grok-web", Protocol: "grok-web", BaseURL: up.URL, APIToken: "fixture-sso", ConnectionID: "conn-a"}, nil)
	w := s.call(openai.ChatProtocol, false)
	if w.Code != http.StatusBadGateway || strings.TrimSpace(w.Body.String()) != `{"error":{"code":"GROK_ERROR","message":"rate limited","type":"upstream_error"}}` {
		t.Fatalf("buffered failure: %d %s", w.Code, w.Body)
	}
	if outcomes := s.outcomes(); len(outcomes) != 1 || outcomes[0].Status != http.StatusBadGateway {
		t.Fatalf("outcomes %+v", outcomes)
	}
}

func TestWebRefusedSessionIsThePinsErrorDocumentWithTheStatusKept(t *testing.T) {
	liftFence(t, "grok-web")
	up := newWebUpstream(t, 403, func(int) string { return "<html>challenge fixture-sso</html>" })
	s := newWebServing(t, routing.ProviderRef{ProviderID: "grok-web", Protocol: "grok-web", BaseURL: up.URL, APIToken: "fixture-sso", ConnectionID: "conn-a"}, nil)
	w := s.call(openai.ChatProtocol, false)
	if w.Code != 403 || !strings.Contains(w.Body.String(), `"code":"HTTP_403"`) || !strings.Contains(w.Body.String(), "SSO cookie may be expired") || strings.Contains(w.Body.String(), "fixture-sso") {
		t.Fatalf("got %d: %s", w.Code, w.Body)
	}
}

func TestPerplexityWebThreadIsContinuedOnlyOnTheSameConnection(t *testing.T) {
	liftFence(t, "perplexity-web")
	follow := map[string]string{
		openai.ChatProtocol:        `{"model":"test/model","stream":false,"messages":[{"role":"user","content":"ping"},{"role":"assistant","content":"first answer"},{"role":"user","content":"again"}]}`,
		anthropic.MessagesProtocol: `{"model":"test/model","max_tokens":50,"stream":false,"messages":[{"role":"user","content":"ping"},{"role":"assistant","content":"first answer"},{"role":"user","content":"again"}]}`,
	}
	for client, followBody := range follow {
		t.Run(client, func(t *testing.T) {
			state := runtime.NewState()
			up := newWebUpstream(t, 200, func(n int) string { return pplxFixture(fmt.Sprintf("thread-%d", n), "first answer") })
			ref := func(conn string) routing.ProviderRef {
				return routing.ProviderRef{ProviderID: "perplexity-web", Protocol: "perplexity-web", BaseURL: up.URL, UpstreamModel: "pplx-auto", APIToken: "fixture-session", ConnectionID: conn}
			}
			a := newWebServing(t, ref("conn-a"), state)
			if w := a.call(openai.ChatProtocol, false); w.Code != 200 || !strings.Contains(w.Body.String(), "first answer") {
				t.Fatalf("first turn %d: %s", w.Code, w.Body)
			}
			send := func(s serving) {
				r := httptest.NewRequest(http.MethodPost, clientRequests[client].path, strings.NewReader(followBody))
				r.Header.Set("Authorization", "Bearer "+s.key)
				w := httptest.NewRecorder()
				s.mux.ServeHTTP(w, r)
				if w.Code != 200 {
					t.Fatalf("follow-up %d: %s", w.Code, w.Body)
				}
			}
			send(a)
			b := newWebServing(t, ref("conn-b"), state)
			send(b)
			first, same, other := up.seen[0], up.seen[1], up.seen[2]
			params := func(r webRequest) map[string]any { return r.body["params"].(map[string]any) }
			if first.cookie != "__Secure-next-auth.session-token=fixture-session" || first.auth != "" || first.path != "/rest/sse/perplexity_ask" {
				t.Errorf("first request %+v", first)
			}
			if params(same)["last_backend_uuid"] != "thread-1" || same.body["query_str"] != "again" {
				t.Errorf("same connection did not continue: %v", same.body)
			}
			if params(other)["last_backend_uuid"] != nil || !strings.HasPrefix(other.body["query_str"].(string), `{"instructions"`) {
				t.Errorf("another connection continued the thread: %v", other.body)
			}
		})
	}
}
