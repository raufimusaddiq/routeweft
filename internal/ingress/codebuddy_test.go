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
	"time"

	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

type codebuddyRig struct {
	handler  *Handler
	mux      *http.ServeMux
	key      string
	state    *runtime.RuntimeState
	mu       sync.Mutex
	seen     []map[string]any
	headers  []http.Header
	outcomes []RequestOutcome
	accounts []string
}

// newCodeBuddyRig serves a CodeBuddy provider from a scripted upstream; nothing
// leaves the process.
func newCodeBuddyRig(t *testing.T, providerID string, upstream http.HandlerFunc) *codebuddyRig {
	t.Helper()
	rig := &codebuddyRig{state: runtime.NewState()}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		rig.mu.Lock()
		rig.seen = append(rig.seen, body)
		rig.headers = append(rig.headers, r.Header.Clone())
		rig.mu.Unlock()
		r.Body = io.NopCloser(strings.NewReader(string(raw)))
		upstream(w, r)
	}))
	t.Cleanup(server.Close)
	manager := newManager(t)
	_, key, err := manager.CreateAPIKey(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	rig.key = key
	rig.mux = http.NewServeMux()
	rig.handler = New(manager, Options{
		AllowPrivateUpstreams: true,
		State:                 rig.state,
		TranslateChat:         TranslateChat, TranslateResponses: TranslateResponses, TranslateMessages: TranslateMessages,
		Candidates: func(*runtime.RuntimeSnapshot, string) ([]routing.Account, bool) {
			if len(rig.accounts) == 0 {
				return []routing.Account{{ID: "acct", Enabled: true}}, true
			}
			var out []routing.Account
			for _, id := range rig.accounts {
				out = append(out, routing.Account{ID: id, Enabled: true})
			}
			return out, true
		},
		AccountProvider: func(_ *runtime.RuntimeSnapshot, _, account, _ string) (routing.ProviderRef, bool) {
			return routing.ProviderRef{ProviderID: providerID, ConnectionID: account, BaseURL: server.URL + "/v2/" + account, Protocol: "openai-chat", UpstreamModel: "glm-5.2", APIToken: "secret"}, true
		},
		OnRequestComplete: func(o RequestOutcome) { rig.mu.Lock(); rig.outcomes = append(rig.outcomes, o); rig.mu.Unlock() },
	})
	rig.handler.Attach(rig.mux)
	return rig
}

func (rig *codebuddyRig) post(path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+rig.key)
	w := httptest.NewRecorder()
	rig.mux.ServeHTTP(w, r)
	return w
}

const codebuddySSE = `data: {"id":"cb-1","object":"chat.completion.chunk","created":5,"model":"glm-5.2","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"hm","tool_calls":[]}}]}

data: {"id":"cb-1","choices":[{"index":0,"delta":{"content":"pong","tool_calls":[]}}]}

data: {"id":"cb-1","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}

data: {"id":"cb-1","choices":[],"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}

data: [DONE]

`

func sseUpstream(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, body)
	}
}

func TestCodeBuddyNonStreamClientsGetOneJSONBodyFromAForcedStream(t *testing.T) {
	for _, id := range []string{"codebuddy-cn", "codebuddy-intl"} {
		t.Run(id+"/chat", func(t *testing.T) {
			rig := newCodeBuddyRig(t, id, sseUpstream(codebuddySSE))
			w := rig.post("/v1/chat/completions", `{"model":"x","stream":false,"messages":[{"role":"user","content":"ping"}]}`)
			if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
				t.Fatalf("status %d type %q: %s", w.Code, w.Header().Get("Content-Type"), w.Body)
			}
			var got struct {
				ID      string
				Choices []struct {
					Message struct {
						Content   string
						Reasoning string `json:"reasoning_content"`
					}
					FinishReason string `json:"finish_reason"`
				}
				Usage map[string]float64
			}
			if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.ID != "cb-1" || len(got.Choices) != 1 ||
				got.Choices[0].Message.Content != "pong" || got.Choices[0].Message.Reasoning != "hm" || got.Choices[0].FinishReason != "stop" || got.Usage["total_tokens"] != 14 {
				t.Fatalf("body %s (%v)", w.Body, err)
			}
			rig.mu.Lock()
			defer rig.mu.Unlock()
			if len(rig.seen) != 1 || rig.seen[0]["stream"] != true {
				t.Fatalf("upstream saw %v", rig.seen)
			}
			headers := rig.headers[0]
			if headers.Get("User-Agent") != "Routeweft" || headers.Get("Authorization") != "Bearer secret" {
				t.Fatalf("upstream headers %v", headers)
			}
			for _, name := range []string{"X-Product", "X-Ide-Type", "X-Ide-Name", "X-Requested-With", "X-Codebuddy-Request"} {
				if headers.Get(name) != "" {
					t.Fatalf("%s sent: %v", name, headers)
				}
			}
			if len(rig.outcomes) != 1 || rig.outcomes[0].InputTokens != 11 || rig.outcomes[0].OutputTokens != 3 || rig.outcomes[0].UsageUnobserved || rig.outcomes[0].Stream {
				t.Fatalf("outcome %+v", rig.outcomes)
			}
		})
	}
}

func TestCodeBuddyForcedStreamReachesTranslatedClients(t *testing.T) {
	cases := map[string]struct{ path, body, want string }{
		"messages":  {"/v1/messages", `{"model":"x","max_tokens":16,"messages":[{"role":"user","content":"ping"}]}`, `"text":"pong"`},
		"responses": {"/v1/responses", `{"model":"x","input":"ping"}`, `pong`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rig := newCodeBuddyRig(t, "codebuddy-intl", sseUpstream(codebuddySSE))
			w := rig.post(tc.path, tc.body)
			if w.Code != 200 || !strings.Contains(w.Body.String(), tc.want) {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			rig.mu.Lock()
			defer rig.mu.Unlock()
			if len(rig.seen) != 1 || rig.seen[0]["stream"] != true {
				t.Fatalf("upstream saw %v", rig.seen)
			}
		})
	}
}

func TestCodeBuddyJSONAnswerToANonStreamRequestPassesThrough(t *testing.T) {
	const answer = `{"id":"j","object":"chat.completion","model":"glm-5.2","choices":[{"index":0,"message":{"role":"assistant","content":"plain"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`
	rig := newCodeBuddyRig(t, "codebuddy-cn", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, answer)
	})
	w := rig.post("/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}]}`)
	if w.Code != 200 || w.Body.String() != answer {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
}

// A stream the client asked for is relayed, with only the empty tool_calls array
// removed from the deltas.
func TestCodeBuddyStreamingClientKeepsItsStreamMinusEmptyToolCalls(t *testing.T) {
	rig := newCodeBuddyRig(t, "codebuddy-cn", sseUpstream(codebuddySSE))
	w := rig.post("/v1/chat/completions", `{"model":"x","stream":true,"messages":[{"role":"user","content":"ping"}]}`)
	body := w.Body.String()
	if w.Code != 200 || strings.Contains(body, "tool_calls") {
		t.Fatalf("status %d: %s", w.Code, body)
	}
	for _, want := range []string{`"content":"pong"`, `"reasoning_content":"hm"`, `"finish_reason":"stop"`, `"total_tokens":14`, "data: [DONE]"} {
		if !strings.Contains(body, want) {
			t.Fatalf("stream lost %q: %s", want, body)
		}
	}
	rig.mu.Lock()
	defer rig.mu.Unlock()
	if rig.seen[0]["stream"] != true {
		t.Fatalf("upstream saw %v", rig.seen[0])
	}
}

func TestOtherProvidersStreamsAreNotRewritten(t *testing.T) {
	rig := newCodeBuddyRig(t, "openrouter", sseUpstream(codebuddySSE))
	w := rig.post("/v1/chat/completions", `{"model":"x","stream":true,"messages":[{"role":"user","content":"ping"}]}`)
	if !strings.Contains(w.Body.String(), `"tool_calls":[]`) {
		t.Fatalf("another provider's stream was rewritten: %s", w.Body)
	}
	rig.mu.Lock()
	defer rig.mu.Unlock()
	if rig.seen[0]["stream"] != true {
		t.Fatalf("the client's own stream flag changed: %v", rig.seen[0])
	}
}

func TestOtherProvidersDoNotForceStream(t *testing.T) {
	rig := newCodeBuddyRig(t, "openrouter", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"id":"j","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"x"},"finish_reason":"stop"}]}`)
	})
	rig.post("/v1/chat/completions", `{"model":"x","stream":false,"messages":[{"role":"user","content":"ping"}]}`)
	rig.mu.Lock()
	defer rig.mu.Unlock()
	if rig.seen[0]["stream"] != false {
		t.Fatalf("stream forced on a provider that does not need it: %v", rig.seen[0])
	}
}

// A forced stream that cannot be returned whole is an error, never a 200 with
// text missing.
func TestCodeBuddyForcedStreamFailuresAreErrorsNotPartialAnswers(t *testing.T) {
	cases := map[string]string{
		"truncated":              "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"half\"}}]}\n\n",
		"in-band error":          "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"}}]}\n\ndata: {\"error\":{\"message\":\"overloaded\"}}\n\n",
		"unrecognised delta key": "data: {\"choices\":[{\"index\":0,\"delta\":{\"thinking\":\"lost\",\"content\":\"x\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n",
		"malformed payload":      "data: {nope\n\ndata: [DONE]\n\n",
		"empty stream":           "",
	}
	for name, stream := range cases {
		t.Run(name, func(t *testing.T) {
			rig := newCodeBuddyRig(t, "codebuddy-cn", sseUpstream(stream))
			w := rig.post("/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}]}`)
			if w.Code != http.StatusBadGateway {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
		})
	}
}

// The client sees the status and body the gateway sent; only Routeweft's own
// cooldown accounting reads the frequency limit.
func TestCodeBuddyFrequencyLimitCoolsTheAccountAndRelaysTheGatewayError(t *testing.T) {
	reset := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	body := fmt.Sprintf(`{"code":6004,"message":"frequency limit, retry after %s UTC+0"}`, reset.Format("2006-01-02 15:04:05"))
	rig := newCodeBuddyRig(t, "codebuddy-intl", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, body)
	})
	w := rig.post("/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}]}`)
	if w.Code != http.StatusBadRequest || w.Body.String() != body {
		t.Fatalf("the gateway's answer was changed: %d %s", w.Code, w.Body)
	}
	until, cooling := rig.state.CooldownUntil("acct", time.Now())
	if !cooling || !until.Equal(reset) {
		t.Fatalf("cooldown until %v (cooling=%v), want the parsed reset %v", until, cooling, reset)
	}
}

func TestCodeBuddyOtherUpstreamErrorsAreNotReadAsALimit(t *testing.T) {
	rig := newCodeBuddyRig(t, "codebuddy-cn", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"code":11101,"message":"Non-stream chat request is currently not supported"}`)
	})
	w := rig.post("/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}]}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status %d", w.Code)
	}
	if _, cooling := rig.state.CooldownUntil("acct", time.Now()); cooling {
		t.Fatal("a client error cooled the account")
	}
}

func TestCodeBuddyIntlSendsCallerSystemPromptNotAMadeUpOne(t *testing.T) {
	rig := newCodeBuddyRig(t, "codebuddy-intl", sseUpstream(codebuddySSE))
	rig.post("/v1/chat/completions", `{"model":"x","messages":[{"role":"developer","content":"Answer in French."},{"role":"user","content":"ping"}]}`)
	rig.mu.Lock()
	defer rig.mu.Unlock()
	raw, _ := json.Marshal(rig.seen[0]["messages"])
	if string(raw) != `[{"content":"Answer in French.","role":"system"},{"content":[{"text":"ping","type":"text"}],"role":"user"}]` {
		t.Fatalf("upstream messages %s", raw)
	}
	if strings.Contains(string(raw), "CodeBuddy Code") {
		t.Fatal("an identity prompt was invented")
	}
}

func TestCodeBuddyTranslatedStreamsKeepUsageOptIn(t *testing.T) {
	for _, tc := range []struct{ path, body string }{
		{"/v1/messages", `{"model":"x","stream":true,"max_tokens":16,"messages":[{"role":"user","content":"ping"}]}`},
		{"/v1/responses", `{"model":"x","stream":true,"input":"ping"}`},
	} {
		rig := newCodeBuddyRig(t, "codebuddy-intl", sseUpstream(codebuddySSE))
		w := rig.post(tc.path, tc.body)
		if w.Code != 200 {
			t.Fatalf("%s: %d %s", tc.path, w.Code, w.Body)
		}
		rig.mu.Lock()
		options, _ := rig.seen[0]["stream_options"].(map[string]any)
		rig.mu.Unlock()
		if options["include_usage"] != true {
			t.Fatalf("%s did not opt in to usage: %v", tc.path, options)
		}
	}
}

func TestCodeBuddyFoldFailureFallsBackToHealthyCandidate(t *testing.T) {
	rig := newCodeBuddyRig(t, "codebuddy-cn", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if strings.HasPrefix(r.URL.Path, "/v2/bad/") {
			fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\",\"surprise\":1}}]}\n\ndata: [DONE]\n\n")
			return
		}
		sseUpstream(codebuddySSE)(w, r)
	})
	rig.accounts = []string{"bad", "good"}
	w := rig.post("/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}]}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "pong") {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
	rig.mu.Lock()
	calls := len(rig.seen)
	rig.mu.Unlock()
	if calls != 2 {
		t.Fatalf("upstream calls %d, want the bad then the good candidate", calls)
	}
	if _, cooling := rig.state.CooldownUntil("bad", time.Now()); !cooling {
		t.Fatal("the account whose stream could not be folded was not classified")
	}
}

func TestCodeBuddyInbandStreamErrorRelaysParsedMessage(t *testing.T) {
	rig := newCodeBuddyRig(t, "codebuddy-cn", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"error\":{\"message\":\"model overloaded\",\"code\":9}}\n\n")
	})
	w := rig.post("/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}]}`)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "model overloaded") || strings.Contains(w.Body.String(), `\"code\"`) {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
}

func TestCodeBuddyMissingUsageStaysUnobserved(t *testing.T) {
	rig := newCodeBuddyRig(t, "codebuddy-cn", sseUpstream("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"pong\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	w := rig.post("/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}]}`)
	rig.mu.Lock()
	defer rig.mu.Unlock()
	if w.Code != 200 || len(rig.outcomes) != 1 || !rig.outcomes[0].UsageUnobserved {
		t.Fatalf("status %d outcomes %+v", w.Code, rig.outcomes)
	}
	if _, set := rig.seen[0]["stream_options"]; set {
		t.Fatal("opted in to usage")
	}
}

func TestCodeBuddyForcedStreamCancellationReachesUpstream(t *testing.T) {
	for _, providerID := range []string{"codebuddy-cn", "api-airforce"} {
		for _, streaming := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", providerID, streaming), func(t *testing.T) {
				started, stopped := make(chan struct{}), make(chan struct{})
				rig := newCodeBuddyRig(t, providerID, func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"half\"}}]}\n\n")
					w.(http.Flusher).Flush()
					close(started)
					<-r.Context().Done()
					close(stopped)
				})
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fmt.Sprintf(`{"model":"x","stream":%t,"messages":[{"role":"user","content":"ping"}]}`, streaming))).WithContext(ctx)
				r.Header.Set("Authorization", "Bearer "+rig.key)
				done := make(chan struct{})
				go func() {
					defer close(done)
					rig.mux.ServeHTTP(httptest.NewRecorder(), r)
				}()
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("upstream did not start")
				}
				cancel()
				for _, ch := range []chan struct{}{stopped, done} {
					select {
					case <-ch:
					case <-time.After(5 * time.Second):
						t.Fatal("cancellation did not stop upstream and relay")
					}
				}
			})
		}
	}
}

func TestCodeBuddyErrorBodySurvivesFailedFallbackBeyondPeekLimit(t *testing.T) {
	body := `{"code":6004,"message":"frequency limit"}` + strings.Repeat(" ", int(errorBodyPeek)+10)
	rig := newCodeBuddyRig(t, "codebuddy-cn", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, body)
	})
	rig.handler.opts.Candidates = func(*runtime.RuntimeSnapshot, string) ([]routing.Account, bool) {
		return []routing.Account{{ID: "acct", Enabled: true}, {ID: "unavailable", Enabled: true}}, true
	}
	rig.handler.opts.CredentialFor = func(_ context.Context, id string) (string, error) {
		if id == "unavailable" {
			return "", fmt.Errorf("fixture credentials unavailable")
		}
		return "secret", nil
	}
	w := rig.post("/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}]}`)
	if w.Code != http.StatusBadRequest || w.Body.String() != body {
		t.Fatalf("error changed: status %d, bytes %d want %d", w.Code, w.Body.Len(), len(body))
	}
}
