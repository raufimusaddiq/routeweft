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

// ingress.unwired exists so that a provider is never routed to before its last
// hop is complete. A provider leaves it only when these tests prove the whole path
// from each client wire: the real translators, the provider's wire hook, the
// upstream request, and the response read back in the client's convention (#194).

// What every client wire must read back from the exchanges below.
const (
	commandCodeNDJSON = `{"type":"text-delta","text":"pong"}
{"type":"tool-call","toolCallId":"call_1","toolName":"lookup","input":{"city":"Oslo"}}
{"type":"finish","finishReason":"tool-calls","totalUsage":{"inputTokens":130,"outputTokens":42,"totalTokens":172,"cachedInputTokens":100}}
`
)

type serving struct {
	mux      http.Handler
	key      string
	handler  *Handler
	outcomes func() []RequestOutcome
}

// newServing mounts one account whose provider is ref for every client wire that
// reaches ref's protocol, with the real translators.
func newServing(t *testing.T, ref routing.ProviderRef) serving {
	t.Helper()
	manager := newManager(t)
	_, key, err := manager.CreateAPIKey(context.Background(), "p02")
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var outcomes []RequestOutcome
	handler := New(manager, Options{
		AllowPrivateUpstreams: true,
		TranslateChat:         TranslateChat, TranslateResponses: TranslateResponses, TranslateMessages: TranslateMessages,
		Candidates: func(*runtime.RuntimeSnapshot, string) ([]routing.Account, bool) {
			return []routing.Account{{ID: "a", Enabled: true}}, true
		},
		AccountProvider: func(_ *runtime.RuntimeSnapshot, _, account, source string) (routing.ProviderRef, bool) {
			if !CanTranslate(source, ref.Protocol) && source != ref.Protocol {
				return routing.ProviderRef{}, false
			}
			served := ref
			served.ConnectionID = account
			return served, true
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

func (s serving) call(client string, stream bool) *httptest.ResponseRecorder {
	request := clientRequests[client]
	r := httptest.NewRequest(http.MethodPost, request.path, strings.NewReader(fmt.Sprintf(request.body, stream)))
	r.Header.Set("Authorization", "Bearer "+s.key)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	return w
}

var clientWires = []string{openai.ChatProtocol, openai.ResponsesProtocol, anthropic.MessagesProtocol}

func TestCommandCodeIsServedToEveryClientWire(t *testing.T) {
	for _, client := range clientWires {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", client, stream), func(t *testing.T) {
				var seen struct {
					sync.Mutex
					path, auth, session, version string
					body                         map[string]any
				}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					raw, _ := io.ReadAll(r.Body)
					seen.Lock()
					seen.path, seen.auth, seen.session, seen.version = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("X-Session-Id"), r.Header.Get("X-Command-Code-Version")
					seen.body = map[string]any{}
					_ = json.Unmarshal(raw, &seen.body)
					seen.Unlock()
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, commandCodeNDJSON)
				}))
				defer upstream.Close()
				s := newServing(t, routing.ProviderRef{ProviderID: "commandcode", Protocol: "commandcode", BaseURL: upstream.URL + "/alpha", UpstreamModel: "vendor/m", APIToken: "secret"})
				w := s.call(client, stream)
				if w.Code != http.StatusOK {
					t.Fatalf("client got %d: %s", w.Code, w.Body)
				}
				seen.Lock()
				defer seen.Unlock()
				if seen.path != "/alpha/generate" || seen.auth != "Bearer secret" || seen.session == "" || seen.version == "" {
					t.Errorf("upstream saw path %q auth %q session %q version %q", seen.path, seen.auth, seen.session, seen.version)
				}
				// The upstream always streams, whatever the client asked for.
				params, _ := seen.body["params"].(map[string]any)
				if seen.body["stream"] != true || params["stream"] != true || params["model"] != "vendor/m" {
					t.Errorf("upstream envelope %v", seen.body)
				}
				body := w.Body.String()
				for _, part := range []string{"pong", "call_1", "lookup", "130", "42"} {
					if !strings.Contains(body, part) {
						t.Errorf("%s client (stream=%v) response lacks %q:\n%s", client, stream, part, body)
					}
				}
				// The usage scanner reads the families it knows (shared.ParseUsage), and
				// the CommandCode NDJSON is not one: the request is recorded once, with
				// usage marked unobserved rather than measured as zero (PARITY 6.19).
				outcomes := s.outcomes()
				if len(outcomes) != 1 {
					t.Fatalf("want exactly one usage record, got %d", len(outcomes))
				}
				if got := outcomes[0]; got.Status != http.StatusOK || got.ProviderID != "commandcode" || !got.UsageUnobserved || got.InputTokens != 0 || got.OutputTokens != 0 {
					t.Errorf("usage record %+v", got)
				}
			})
		}
	}
}

// An error event inside a 200 stream is an HTTP error for the client, as the pin
// makes it (executors/commandcode.js:140-241), not a 500 and not a success.
func TestCommandCodeErrorEventReachesEveryClientAsAnHTTPError(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, `{"type":"error","error":{"message":"bad model","statusCode":400}}`+"\n")
	}))
	defer upstream.Close()
	for _, client := range clientWires {
		s := newServing(t, routing.ProviderRef{ProviderID: "commandcode", Protocol: "commandcode", BaseURL: upstream.URL + "/alpha", UpstreamModel: "vendor/m", APIToken: "secret"})
		w := s.call(client, false)
		if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "bad model") {
			t.Errorf("%s client got %d: %s", client, w.Code, w.Body)
		}
	}
}

type kiroHost func(host string) (status int, body []byte)

// kiroServing serves one Kiro account whose upstream is answer, host by host.
func kiroServing(t *testing.T, answer kiroHost) (serving, func() []string) {
	t.Helper()
	ref := routing.ProviderRef{ProviderID: "kiro", Protocol: "kiro", BaseURL: "https://runtime.us-east-1.kiro.dev", UpstreamModel: "claude-sonnet-4.5", APIToken: "fixture-token",
		Connection: routing.ConnectionMetadata{Settings: routing.ProviderSettings{
			ProfileARN: "arn:aws:codewhisperer:eu-west-1:123456789012:profile/fixture", AuthMethod: "external_idp", Region: "eu-west-1",
		}}}
	s := newServing(t, ref)
	var mu sync.Mutex
	var hosts []string
	var firstBody string
	s.handler.client = &http.Client{Transport: endpointTransport(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		r.Body.Close()
		mu.Lock()
		defer mu.Unlock()
		hosts = append(hosts, r.URL.Hostname())
		if firstBody == "" {
			firstBody = string(raw)
		}
		if string(raw) != firstBody || r.Header.Get("Authorization") != "Bearer fixture-token" || r.URL.Path != "/generateAssistantResponse" {
			t.Errorf("host %s: the retry changed the request: path %s body %s", r.URL.Hostname(), r.URL.Path, raw)
		}
		status, body := answer(r.URL.Hostname())
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/vnd.amazon.eventstream"}}, Body: io.NopCloser(strings.NewReader(string(body)))}, nil
	})}
	return s, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), hosts...)
	}
}

func kiroTurn() []byte {
	var body []byte
	for _, frame := range [][]byte{
		kiroFrame("assistantResponseEvent", `{"content":"pong"}`),
		kiroFrame("toolUseEvent", `{"toolUseId":"call_1","name":"lookup","input":"{\"city\":\"Oslo\"}"}`),
		kiroFrame("messageStopEvent", `{"stopReason":"tool_use"}`),
	} {
		body = append(body, frame...)
	}
	return body
}

// Every client wire reaches Kiro through the three-host fallback: the first
// host rejects the credential for its surface, the second answers, and the client
// reads the answer in its own convention. The hosts are those of the account's
// region, Amazon surfaces first (executors/kiro.js:299-328).
func TestKiroIsServedToEveryClientWire(t *testing.T) {
	for _, client := range clientWires {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", client, stream), func(t *testing.T) {
				s, hosts := kiroServing(t, func(host string) (int, []byte) {
					if strings.HasPrefix(host, "q.") {
						return http.StatusForbidden, []byte(`{"message":"bearer token invalid"}`)
					}
					return http.StatusOK, kiroTurn()
				})
				w := s.call(client, stream)
				if w.Code != http.StatusOK {
					t.Fatalf("client got %d: %s", w.Code, w.Body)
				}
				if got := strings.Join(hosts(), ","); got != "q.eu-west-1.amazonaws.com,codewhisperer.eu-west-1.amazonaws.com" {
					t.Errorf("hosts %s", got)
				}
				body := w.Body.String()
				for _, part := range []string{"pong", "call_1", "lookup"} {
					if !strings.Contains(body, part) {
						t.Errorf("%s client (stream=%v) response lacks %q:\n%s", client, stream, part, body)
					}
				}
				// Kiro's event stream is not a family the usage scanner reads either, so
				// usage is recorded as unobserved (PARITY 6.19).
				if outcomes := s.outcomes(); len(outcomes) != 1 || outcomes[0].ProviderID != "kiro" || outcomes[0].Status != http.StatusOK || !outcomes[0].UsageUnobserved {
					t.Errorf("usage records %+v", outcomes)
				}
			})
		}
	}
}

// When all three hosts refuse, the client is told what the last host said, not a
// 500 with no clue.
func TestKiroExhaustedHostsAnswerTheClientWithTheUpstreamStatus(t *testing.T) {
	for _, client := range clientWires {
		s, hosts := kiroServing(t, func(host string) (int, []byte) {
			return http.StatusNotFound, []byte(`{"message":"not found on ` + host + `"}`)
		})
		w := s.call(client, false)
		if w.Code != http.StatusNotFound || !strings.Contains(w.Body.String(), "runtime.us-east-1.kiro.dev") {
			t.Errorf("%s client got %d: %s", client, w.Code, w.Body)
		}
		if len(hosts()) != 3 {
			t.Errorf("%s: hosts %v", client, hosts())
		}
	}
}
