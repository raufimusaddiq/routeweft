package ingress

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/providers/wire"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// newGitHubRig serves a provider with GitHub Copilot's three transports from a
// scripted upstream (#190). Wire selection follows runtime.AccountProvider: a
// forced transport first, then the client's own wire, then the first one the
// hub can translate to. Nothing leaves the process.
func newGitHubRig(t *testing.T, providerID, model string, upstream func(w http.ResponseWriter, r *http.Request)) *responsesRig {
	t.Helper()
	rig := &responsesRig{state: runtime.NewState(), accounts: []string{"acct"}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		rig.mu.Lock()
		rig.calls = append(rig.calls, upstreamCall{account: "acct", path: r.URL.Path, body: body, header: r.Header.Clone()})
		rig.mu.Unlock()
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
	endpoints := map[string]string{"openai-chat": "chat/completions", "openai-responses": "responses", "anthropic-messages": "v1/messages"}
	New(manager, Options{
		AllowPrivateUpstreams: true,
		State:                 rig.state,
		TranslateChat:         TranslateChat, TranslateResponses: TranslateResponses, TranslateMessages: TranslateMessages,
		Candidates: func(*runtime.RuntimeSnapshot, string) ([]routing.Account, bool) {
			return []routing.Account{{ID: "acct", Enabled: true}}, true
		},
		AccountProvider: func(_ *runtime.RuntimeSnapshot, _, account, source string) (routing.ProviderRef, bool) {
			transports := []string{"openai-chat", "openai-responses", "anthropic-messages"}
			if forced := wire.ForcedTransport(providerID, model); forced != "" {
				transports = []string{forced}
			}
			protocol := transports[0]
			if slices.Contains(transports, source) {
				protocol = source
			} else {
				protocol = ""
				for _, target := range transports {
					if CanTranslate(source, target) {
						protocol = target
						break
					}
				}
			}
			if protocol == "" {
				return routing.ProviderRef{}, false
			}
			return routing.ProviderRef{ProviderID: providerID, ConnectionID: account, BaseURL: server.URL, Protocol: protocol, UpstreamModel: model, APIToken: "secret", Endpoint: endpoints[protocol]}, true
		},
		OnRequestComplete: func(o RequestOutcome) { rig.mu.Lock(); rig.outcomes = append(rig.outcomes, o); rig.mu.Unlock() },
	}).Attach(rig.mux)
	return rig
}

func (rig *responsesRig) paths() []string {
	rig.mu.Lock()
	defer rig.mu.Unlock()
	var out []string
	for _, call := range rig.calls {
		out = append(out, call.path)
	}
	return out
}

const (
	copilotChatRefusal = `{"error":{"message":"model \"gpt-5.4\" is not accessible via the /chat/completions endpoint","code":"unsupported_api_for_model"}}`
	copilotNotSupport  = `{"error":{"message":"The requested model is not supported.","code":"model_not_supported"}}`
	responsesJSON      = `{"id":"resp_1","object":"response","created_at":5,"status":"completed","model":"gpt-5.4","output":[{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"pong","annotations":[]}]}],"usage":{"input_tokens":11,"output_tokens":3,"total_tokens":14}}`
)

// chatThenResponses refuses /chat/completions with the given body and serves
// /responses as JSON or, when the request streams, as SSE.
func chatThenResponses(refusal string) func(http.ResponseWriter, *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(400)
			io.WriteString(w, refusal)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		_ = raw
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, responsesJSON)
	}
}

func TestGitHubEscalatesAChatRefusalToResponsesExactlyOnce(t *testing.T) {
	for _, refusal := range []string{copilotChatRefusal, copilotNotSupport} {
		rig := newGitHubRig(t, "github", "gpt-5.4", chatThenResponses(refusal))
		out := rig.post("/v1/chat/completions", `{"model":"x","max_tokens":9,"temperature":0.4,"messages":[{"role":"user","content":"ping"}]}`)
		if out.Code != 200 || !strings.Contains(out.Body.String(), "pong") || !strings.Contains(out.Body.String(), "chat.completion") {
			t.Fatalf("%d %s", out.Code, out.Body)
		}
		if got := rig.paths(); !slices.Equal(got, []string{"/chat/completions", "/responses"}) {
			t.Fatalf("calls %v", got)
		}
		first, second := rig.calls[0], rig.calls[1]
		// Each wire gets its own body: the Chat quirk on the first, the same
		// sanitizer (gpt-5.4 drops temperature) on both, none of Chat's fields on the replay.
		if first.body["max_completion_tokens"] != float64(9) || first.body["temperature"] != nil {
			t.Fatalf("chat body %v", first.body)
		}
		if _, chatField := second.body["messages"]; chatField || second.body["temperature"] != nil || second.body["max_output_tokens"] != float64(9) {
			t.Fatalf("responses body %v", second.body)
		}
		if second.header.Get("Authorization") != "Bearer secret" || second.header.Get("Copilot-Integration-Id") == "" {
			t.Fatalf("replay lost the identity headers: %v", second.header)
		}
		if len(rig.outcomes) != 1 || rig.outcomes[0].InputTokens != 11 || rig.outcomes[0].OutputTokens != 3 {
			t.Fatalf("usage %+v", rig.outcomes)
		}
	}
}

func TestGitHubEscalatedStreamReachesAStreamingChatClient(t *testing.T) {
	rig := newGitHubRig(t, "github", "gpt-5.4", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/chat/completions") {
			w.WriteHeader(400)
			io.WriteString(w, copilotChatRefusal)
			return
		}
		serveStream(codexStream)("", w, r)
	})
	out := rig.post("/v1/chat/completions", `{"model":"x","stream":true,"messages":[{"role":"user","content":"ping"}]}`)
	if out.Code != 200 || !strings.Contains(out.Body.String(), "pong") || !strings.Contains(out.Body.String(), "chat.completion.chunk") {
		t.Fatalf("%d %s", out.Code, out.Body)
	}
	if got := rig.paths(); !slices.Equal(got, []string{"/chat/completions", "/responses"}) || rig.calls[1].body["stream"] != true {
		t.Fatalf("calls %v %v", got, rig.calls)
	}
}

// The escalation is one replay. A 400 that survives it is the answer the client
// gets, and the gateway is not asked a third time.
func TestGitHubRepeatedRefusalIsSurfacedNotLooped(t *testing.T) {
	rig := newGitHubRig(t, "github", "gpt-5.4", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(400)
		io.WriteString(w, copilotChatRefusal)
	})
	out := rig.post("/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}]}`)
	if out.Code != 400 || !strings.Contains(out.Body.String(), "not accessible via the /chat/completions endpoint") {
		t.Fatalf("%d %s", out.Code, out.Body)
	}
	if got := rig.paths(); !slices.Equal(got, []string{"/chat/completions", "/responses"}) {
		t.Fatalf("calls %v", got)
	}
}

func TestGitHubEscalationHasLimits(t *testing.T) {
	cases := []struct {
		name, provider, model, status, body string
	}{
		{"an unrelated 400", "github", "gpt-5.4", "400", `{"error":{"message":"messages: invalid"}}`},
		{"gemini is never sent to /responses", "github", "gemini-2.5-pro", "400", copilotChatRefusal},
		{"another provider's identical 400", "openrouter", "gpt-5.4", "400", copilotChatRefusal},
		{"a refusal that is not a 400", "github", "gpt-5.4", "422", copilotChatRefusal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := 400
			if tc.status == "422" {
				status = 422
			}
			rig := newGitHubRig(t, tc.provider, tc.model, func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(status)
				io.WriteString(w, tc.body)
			})
			out := rig.post("/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}]}`)
			if out.Code != status {
				t.Fatalf("%d %s", out.Code, out.Body)
			}
			if got := rig.paths(); !slices.Equal(got, []string{"/chat/completions"}) {
				t.Fatalf("calls %v", got)
			}
		})
	}
}

// The escalation must not turn a 2xx or a Responses client into a replay.
func TestGitHubResponsesClientIsNotEscalated(t *testing.T) {
	rig := newGitHubRig(t, "github", "gpt-5.4", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(400)
		io.WriteString(w, copilotChatRefusal)
	})
	if out := rig.post("/v1/responses", `{"model":"x","input":"ping"}`); out.Code != 400 {
		t.Fatalf("%d %s", out.Code, out.Body)
	}
	if got := rig.paths(); !slices.Equal(got, []string{"/responses"}) {
		t.Fatalf("calls %v", got)
	}
}

const claudeMessagesSSE = `event: message_start
data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-haiku-4.5","content":[],"stop_reason":null,"usage":{"input_tokens":11,"cache_read_input_tokens":8,"cache_creation_input_tokens":0,"output_tokens":1}}}

event: ping
data: {"type":"ping"}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"po"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"ng"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":3}}

event: message_stop
data: {"type":"message_stop"}

`

func claudeUpstream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/event-stream")
	io.WriteString(w, claudeMessagesSSE)
}

func TestGitHubClaudeIsServedOnMessagesWithAForcedStream(t *testing.T) {
	for _, tc := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}]`},
		{"/v1/responses", `{"model":"x","input":"ping"`},
		{"/v1/messages", `{"model":"x","max_tokens":16,"messages":[{"role":"user","content":"ping"}]`},
	} {
		for _, stream := range []string{"false", "true"} {
			t.Run(tc.path+"/"+stream, func(t *testing.T) {
				rig := newGitHubRig(t, "github", "claude-haiku-4.5", claudeUpstream)
				out := rig.post(tc.path, tc.body+`,"stream":`+stream+`}`)
				// A stream carries the text in the two deltas it arrived as.
				text := out.Body.String()
				if out.Code != 200 || !(strings.Contains(text, "pong") || stream == "true" && strings.Contains(text, `"po"`) && strings.Contains(text, `"ng"`)) {
					t.Fatalf("%d %s", out.Code, out.Body)
				}
				if stream == "false" && !json.Valid(out.Body.Bytes()) {
					t.Fatalf("a non-stream client must get one JSON body: %s", out.Body)
				}
				if got := rig.paths(); !slices.Equal(got, []string{"/v1/messages"}) || rig.calls[0].body["stream"] != true {
					t.Fatalf("calls %v %v", got, rig.calls)
				}
				// Streaming usage is accounted by the relay's own scanner (its input
				// count is the client wire's convention), so only the cached-token
				// and output counts are common to every client wire and mode.
				if len(rig.outcomes) != 1 || rig.outcomes[0].OutputTokens != 3 || rig.outcomes[0].CacheRead != 8 {
					t.Fatalf("usage %+v", rig.outcomes)
				}
			})
		}
	}
}

// A native Messages client that asked for a body gets the Messages document the
// fold rebuilt, with the stop reason and the usage the stream reported.
func TestGitHubClaudeMessagesFoldIsAMessagesDocument(t *testing.T) {
	rig := newGitHubRig(t, "github", "claude-haiku-4.5", claudeUpstream)
	out := rig.post("/v1/messages", `{"model":"x","max_tokens":16,"stream":false,"messages":[{"role":"user","content":"ping"}]}`)
	var got struct {
		ID, Type, Role, StopReason string `json:"-"`
		Content                    []map[string]any
		Usage                      map[string]float64
	}
	if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil || out.Code != 200 {
		t.Fatalf("%d %s (%v)", out.Code, out.Body, err)
	}
	var raw map[string]any
	_ = json.Unmarshal(out.Body.Bytes(), &raw)
	if raw["type"] != "message" || raw["role"] != "assistant" || raw["stop_reason"] != "end_turn" || len(got.Content) != 1 || got.Content[0]["text"] != "pong" ||
		got.Usage["input_tokens"] != 11 || got.Usage["output_tokens"] != 3 || got.Usage["cache_read_input_tokens"] != 8 {
		t.Fatalf("%s", out.Body)
	}
}

func TestGitHubClaudeFoldFailuresAreNotPassedOffAsAnAnswer(t *testing.T) {
	for name, body := range map[string]string{
		"truncated":     strings.SplitN(claudeMessagesSSE, "event: message_delta", 2)[0],
		"in-band error": "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"overloaded_error\",\"message\":\"Overloaded\"}}\n\n",
	} {
		t.Run(name, func(t *testing.T) {
			rig := newGitHubRig(t, "github", "claude-haiku-4.5", func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, body)
			})
			if out := rig.post("/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}]}`); out.Code != 502 {
				t.Fatalf("%d %s", out.Code, out.Body)
			}
		})
	}
}

var uuidPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Every upstream request carries its own x-request-id, the escalated replay
// included, and only github sends one.
func TestGitHubRequestIDIsPerUpstreamRequest(t *testing.T) {
	rig := newGitHubRig(t, "github", "gpt-5.4", chatThenResponses(copilotChatRefusal))
	rig.post("/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}]}`)
	if len(rig.calls) != 2 {
		t.Fatalf("calls %v", rig.paths())
	}
	a, b := rig.calls[0].header.Get("X-Request-Id"), rig.calls[1].header.Get("X-Request-Id")
	if !uuidPattern.MatchString(a) || !uuidPattern.MatchString(b) || a == b {
		t.Fatalf("request ids %q %q", a, b)
	}
	claude := newGitHubRig(t, "github", "claude-haiku-4.5", claudeUpstream)
	claude.post("/v1/messages", `{"model":"x","max_tokens":4,"messages":[{"role":"user","content":"ping"}]}`)
	if id := claude.calls[0].header.Get("X-Request-Id"); !uuidPattern.MatchString(id) {
		t.Fatalf("messages request id %q", id)
	}
	other := newGitHubRig(t, "openrouter", "gpt-5.4", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"c","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}]}`)
	})
	other.post("/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}]}`)
	if id := other.calls[0].header.Get("X-Request-Id"); id != "" {
		t.Fatalf("another provider received the request id %q", id)
	}
}

// Body sanitation stays the existing sanitizer's: it reaches the Messages body a
// Claude request now travels as, and the executor adds no rule of its own.
func TestGitHubClaudeMessagesBodyKeepsTheSanitizerRules(t *testing.T) {
	for _, tc := range []struct {
		model        string
		wantThinking bool
	}{
		{"claude-haiku-4.5", false},
		{"claude-sonnet-4.6", true},
	} {
		rig := newGitHubRig(t, "github", tc.model, claudeUpstream)
		rig.post("/v1/messages", `{"model":"x","max_tokens":2048,"temperature":0.3,"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"ping"}]}`)
		body := rig.calls[0].body
		if _, has := body["thinking"]; has != tc.wantThinking {
			t.Fatalf("%s thinking present=%v body=%v", tc.model, has, body)
		}
		if _, has := body["temperature"]; has {
			t.Fatalf("%s kept temperature: %v", tc.model, body)
		}
	}
}

// The registry endpoint the escalation names is the one the registry declares.
func TestGitHubEscalationEndpointIsTheRegistrys(t *testing.T) {
	var spec registry.Spec
	for _, candidate := range registry.Builtins() {
		if candidate.ID == "github" {
			spec = candidate
		}
	}
	protocol, endpoint := wire.Escalation(routing.ProviderRef{ProviderID: "github", Protocol: "openai-chat", UpstreamModel: "gpt-5.4"}, 400, []byte(copilotChatRefusal))
	if protocol != "openai-responses" || endpoint != spec.TransportEndpoints[registry.TransportOpenAIResponses] {
		t.Fatalf("%q %q vs registry %v", protocol, endpoint, spec.TransportEndpoints)
	}
}
