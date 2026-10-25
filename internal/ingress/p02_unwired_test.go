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
				// The record carries the finish event's counts as the pin reads them
				// (commandcode-to-openai.js:155-163); cachedInputTokens is an
				// observation beside them, not a cache read (PARITY V37).
				outcomes := s.outcomes()
				if len(outcomes) != 1 {
					t.Fatalf("want exactly one usage record, got %d", len(outcomes))
				}
				if got := outcomes[0]; got.Status != http.StatusOK || got.ProviderID != "commandcode" || got.UsageUnobserved || got.InputTokens != 130 || got.OutputTokens != 42 || got.CacheRead != 0 {
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
		kiroFrame("metricsEvent", `{"inputTokens":30,"outputTokens":42,"cacheReadInputTokens":100}`),
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
				// The metricsEvent's counts, cache apart from the prompt (kiro.js:917).
				if outcomes := s.outcomes(); len(outcomes) != 1 || outcomes[0].ProviderID != "kiro" || outcomes[0].Status != http.StatusOK || outcomes[0].UsageUnobserved ||
					outcomes[0].InputTokens != 30 || outcomes[0].OutputTokens != 42 || outcomes[0].CacheRead != 100 {
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

const geminiAnswer = `{"candidates":[{"content":{"role":"model","parts":[{"text":"pong"},{"functionCall":{"name":"lookup","args":{"city":"Oslo"}}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":130,"candidatesTokenCount":42,"totalTokenCount":172,"cachedContentTokenCount":100},"modelVersion":"gemini-2.5-flash"}`

// Vertex, Gemini CLI and Antigravity are served to every client wire with the
// URL, query, credential, identity and project their executors bind (vertex.js:83-123,
// gemini-cli.js:10-35, antigravity.js:118-135,306), and the usage the upstream
// reports is the usage recorded, including from inside the Cloud Code envelope.
func TestGeminiFamilyIsServedToEveryClientWire(t *testing.T) {
	cases := []struct {
		id       string
		settings routing.ProviderSettings
		token    string
		path     string
		cloud    bool
	}{
		{"vertex", routing.ProviderSettings{ProjectID: "fixture-project"}, "AIza-fixture", "/v1/projects/fixture-project/locations/global/publishers/google/models/gemini-2.5-flash:", false},
		{"gemini-cli", routing.ProviderSettings{ProjectID: "fixture-project"}, "ya29.fixture", "/v1internal:", true},
		{"antigravity", routing.ProviderSettings{ProjectID: "fixture-project"}, "ya29.fixture", "/v1internal:", true},
	}
	for _, c := range cases {
		for _, client := range clientWires {
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/stream=%v", c.id, client, stream), func(t *testing.T) {
					var seen struct {
						sync.Mutex
						path, query string
						header      http.Header
						body        map[string]any
					}
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						raw, _ := io.ReadAll(r.Body)
						seen.Lock()
						seen.path, seen.query, seen.header = r.URL.Path, r.URL.RawQuery, r.Header.Clone()
						seen.body = map[string]any{}
						_ = json.Unmarshal(raw, &seen.body)
						seen.Unlock()
						answer := geminiAnswer
						if c.cloud {
							answer = `{"response":` + geminiAnswer + `,"traceId":"t"}`
						}
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
							fmt.Fprintf(w, "data: %s\n\n", answer)
							return
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = io.WriteString(w, answer)
					}))
					defer upstream.Close()
					s := newServing(t, routing.ProviderRef{ProviderID: c.id, Protocol: c.id, BaseURL: upstream.URL, UpstreamModel: "gemini-2.5-flash", APIToken: c.token,
						Connection: routing.ConnectionMetadata{Settings: c.settings}})
					w := s.call(client, stream)
					if w.Code != http.StatusOK {
						t.Fatalf("client got %d: %s", w.Code, w.Body)
					}
					seen.Lock()
					defer seen.Unlock()
					action, query := "generateContent", ""
					if stream {
						action, query = "streamGenerateContent", "alt=sse"
					}
					if seen.path != c.path+action || seen.query != query {
						t.Errorf("upstream saw %s?%s, want %s?%s", seen.path, seen.query, c.path+action, query)
					}
					switch c.id {
					case "vertex":
						if seen.header.Get("X-Goog-Api-Key") != c.token || seen.header.Get("Authorization") != "" {
							t.Errorf("vertex credential headers %v", seen.header)
						}
					default:
						if seen.header.Get("Authorization") != "Bearer "+c.token {
							t.Errorf("%s authorization %q", c.id, seen.header.Get("Authorization"))
						}
						if envelope := seen.body; envelope["project"] != "fixture-project" || envelope["model"] != "gemini-2.5-flash" || envelope["request"] == nil {
							t.Errorf("%s envelope %v", c.id, envelope)
						}
					}
					if c.id == "gemini-cli" && !strings.HasPrefix(seen.header.Get("User-Agent"), "GeminiCLI/0.34.0/gemini-2.5-flash ") {
						t.Errorf("gemini-cli user agent %q", seen.header.Get("User-Agent"))
					}
					if c.id == "antigravity" && !strings.HasPrefix(seen.header.Get("User-Agent"), "antigravity/ide/") {
						t.Errorf("antigravity user agent %q", seen.header.Get("User-Agent"))
					}
					body := w.Body.String()
					// Messages reports the cache apart from the prompt: 30 plus 100 read.
					prompt := "130"
					if client == anthropic.MessagesProtocol {
						prompt = `"cache_read_input_tokens":100`
					}
					for _, part := range []string{"pong", "lookup", "Oslo", prompt, "42"} {
						if !strings.Contains(body, part) {
							t.Errorf("%s client (stream=%v) response lacks %q:\n%s", client, stream, part, body)
						}
					}
					outcomes := s.outcomes()
					if len(outcomes) != 1 {
						t.Fatalf("want exactly one usage record, got %d", len(outcomes))
					}
					if got := outcomes[0]; got.UsageUnobserved || got.Status != http.StatusOK || got.ProviderID != c.id || got.InputTokens != 130 || got.OutputTokens != 42 || got.CacheRead != 100 {
						t.Errorf("usage record %+v", got)
					}
				})
			}
		}
	}
}

// A Cloud Code connection with no project is refused before anything is sent,
// where the pin would make one up (antigravity.js:138).
func TestCloudCodeWithoutAProjectSendsNothing(t *testing.T) {
	for _, id := range []string{"gemini-cli", "antigravity"} {
		for _, client := range clientWires {
			var hits int
			upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
			s := newServing(t, routing.ProviderRef{ProviderID: id, Protocol: id, BaseURL: upstream.URL, UpstreamModel: "gemini-2.5-flash", APIToken: "ya29.fixture"})
			w := s.call(client, false)
			upstream.Close()
			if w.Code < 400 || w.Code == http.StatusInternalServerError || hits != 0 || !strings.Contains(w.Body.String(), "project id") {
				t.Errorf("%s/%s: status %d after %d requests: %s", id, client, w.Code, hits, w.Body)
			}
		}
	}
}

// A Vertex service account or ADC connection is minted into a bearer through the
// connection's own client before the request goes out.
func TestVertexAuthorizesWithAMintedToken(t *testing.T) {
	adc := `{"type":"authorized_user","client_id":"cid","client_secret":"csecret","refresh_token":"rtok","quota_project_id":"adc-project"}`
	for _, client := range clientWires {
		s := newServing(t, routing.ProviderRef{ProviderID: "vertex", Protocol: "vertex", BaseURL: "https://aiplatform.googleapis.com", UpstreamModel: "gemini-2.5-flash", APIToken: adc})
		var urls []string
		var authorization, key string
		s.handler.client = &http.Client{Transport: endpointTransport(func(r *http.Request) (*http.Response, error) {
			urls = append(urls, r.URL.String())
			if r.URL.Host == "oauth2.googleapis.com" {
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"access_token":"ya29.minted","expires_in":3600}`))}, nil
			}
			authorization, key = r.Header.Get("Authorization"), r.Header.Get("X-Goog-Api-Key")
			return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(geminiAnswer))}, nil
		})}
		w := s.call(client, false)
		if w.Code != http.StatusOK || authorization != "Bearer ya29.minted" || key != "" {
			t.Fatalf("%s: status %d, authorization %q, key %q: %s", client, w.Code, authorization, key, w.Body)
		}
		want := "https://aiplatform.googleapis.com/v1/projects/adc-project/locations/us-central1/publishers/google/models/gemini-2.5-flash:generateContent"
		if urls[len(urls)-1] != want {
			t.Errorf("%s: url %s, want %s", client, urls[len(urls)-1], want)
		}
	}
}

// A CommandCode answer whose body names a rate limit is accounted as one, whatever
// HTTP status carried it (executors/commandcode.js:63-78): a 400 the status
// alone would call the client's fault moves on to the next account.
func TestCommandCodeBodyStatusDecidesTheFallback(t *testing.T) {
	var mu sync.Mutex
	var credentials []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		credentials = append(credentials, r.Header.Get("Authorization"))
		first := len(credentials) == 1
		mu.Unlock()
		if first {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = io.WriteString(w, `{"error":{"code":429,"message":"slow down"}}`)
			return
		}
		_, _ = io.WriteString(w, commandCodeNDJSON)
	}))
	defer upstream.Close()
	manager := newManager(t)
	_, key, err := manager.CreateAPIKey(context.Background(), "p02")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(manager, Options{
		AllowPrivateUpstreams: true, TranslateChat: TranslateChat,
		Candidates: func(*runtime.RuntimeSnapshot, string) ([]routing.Account, bool) {
			return []routing.Account{{ID: "a", Enabled: true}, {ID: "b", Enabled: true}}, true
		},
		AccountProvider: func(_ *runtime.RuntimeSnapshot, _, account, _ string) (routing.ProviderRef, bool) {
			return routing.ProviderRef{ProviderID: "commandcode", Protocol: "commandcode", ConnectionID: account, BaseURL: upstream.URL + "/alpha", UpstreamModel: "vendor/m", APIToken: "key-" + account}, true
		},
	}).Attach(mux)
	request := clientRequests[openai.ChatProtocol]
	r := httptest.NewRequest(http.MethodPost, request.path, strings.NewReader(fmt.Sprintf(request.body, false)))
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	mu.Lock()
	defer mu.Unlock()
	if w.Code != http.StatusOK || strings.Join(credentials, ",") != "Bearer key-a,Bearer key-b" {
		t.Fatalf("status %d after %v: %s", w.Code, credentials, w.Body)
	}
}
