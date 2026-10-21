package ingress

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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

// responsesRig serves a stream-only Responses provider (Codex, Grok CLI) from a
// scripted upstream; nothing leaves the process. The upstream sees
// /{account}/responses, so a script can answer each account differently.
type responsesRig struct {
	mux      *http.ServeMux
	key      string
	state    *runtime.RuntimeState
	accounts []string
	mu       sync.Mutex
	calls    []upstreamCall
	outcomes []RequestOutcome
}

type upstreamCall struct {
	account, path string
	body          map[string]any
	header        http.Header
}

func newResponsesRig(t *testing.T, providerID, model string, accounts []string, upstream func(account string, w http.ResponseWriter, r *http.Request)) *responsesRig {
	t.Helper()
	rig := &responsesRig{state: runtime.NewState(), accounts: accounts}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		account := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)[0]
		rig.mu.Lock()
		rig.calls = append(rig.calls, upstreamCall{account: account, path: r.URL.Path, body: body, header: r.Header.Clone()})
		rig.mu.Unlock()
		upstream(account, w, r)
	}))
	t.Cleanup(server.Close)
	manager := newManager(t)
	_, key, err := manager.CreateAPIKey(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	rig.key = key
	rig.mux = http.NewServeMux()
	New(manager, Options{
		AllowPrivateUpstreams: true,
		State:                 rig.state,
		TranslateChat:         TranslateChat, TranslateResponses: TranslateResponses, TranslateMessages: TranslateMessages,
		Candidates: func(*runtime.RuntimeSnapshot, string) ([]routing.Account, bool) {
			var out []routing.Account
			for _, id := range rig.accounts {
				out = append(out, routing.Account{ID: id, Enabled: true})
			}
			return out, true
		},
		AccountProvider: func(_ *runtime.RuntimeSnapshot, _, account, _ string) (routing.ProviderRef, bool) {
			return routing.ProviderRef{ProviderID: providerID, ConnectionID: account, BaseURL: server.URL + "/" + account, Protocol: "openai-responses", UpstreamModel: model, APIToken: "secret"}, true
		},
		OnRequestComplete: func(o RequestOutcome) { rig.mu.Lock(); rig.outcomes = append(rig.outcomes, o); rig.mu.Unlock() },
	}).Attach(rig.mux)
	return rig
}

func (rig *responsesRig) post(path, body string, headers ...string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+rig.key)
	for i := 0; i+1 < len(headers); i += 2 {
		r.Header.Set(headers[i], headers[i+1])
	}
	w := httptest.NewRecorder()
	rig.mux.ServeHTTP(w, r)
	return w
}

func frame(kind, data string) string { return "event: " + kind + "\ndata: " + data + "\n\n" }

// codexStream is what the Codex endpoint writes for "ping" -> "pong": the reasoning
// and message items arrive as output_item.done events and the final
// response.completed carries an empty output with the usage.
var codexStream = frame("response.created", `{"type":"response.created","response":{"id":"resp_1","object":"response","status":"in_progress","model":"gpt-5"}}`) +
	frame("response.output_item.done", `{"type":"response.output_item.done","output_index":0,"item":{"id":"rs_1","type":"reasoning","summary":[]}}`) +
	frame("response.output_text.delta", `{"type":"response.output_text.delta","output_index":1,"delta":"pong"}`) +
	frame("response.output_item.done", `{"type":"response.output_item.done","output_index":1,"item":{"id":"msg_1","type":"message","role":"assistant","status":"completed","content":[{"type":"output_text","text":"pong","annotations":[]}]}}`) +
	frame("response.completed", `{"type":"response.completed","response":{"id":"resp_1","object":"response","created_at":5,"status":"completed","model":"gpt-5","output":[],"usage":{"input_tokens":11,"input_tokens_details":{"cached_tokens":8},"output_tokens":3,"output_tokens_details":{"reasoning_tokens":0},"total_tokens":14}}}`)

func serveStream(body string) func(string, http.ResponseWriter, *http.Request) {
	return func(_ string, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, body)
	}
}

func TestCodexNonStreamResponsesClientGetsOneJSONFromAForcedStream(t *testing.T) {
	rig := newResponsesRig(t, "codex", "gpt-5", []string{"acct"}, serveStream(codexStream))
	w := rig.post("/v1/responses", `{"model":"x","input":"ping","stream":false,"temperature":0.2,"user":"u"}`)
	if w.Code != 200 || !strings.HasPrefix(w.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("status %d type %q: %s", w.Code, w.Header().Get("Content-Type"), w.Body)
	}
	var got struct {
		ID     string
		Object string
		Status string
		Output []map[string]any
		Usage  struct {
			InputTokens        float64 `json:"input_tokens"`
			InputTokensDetails struct {
				CachedTokens float64 `json:"cached_tokens"`
			} `json:"input_tokens_details"`
			TotalTokens float64 `json:"total_tokens"`
		}
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || got.ID != "resp_1" || got.Object != "response" || got.Status != "completed" ||
		len(got.Output) != 2 || got.Output[1]["id"] != "msg_1" || got.Usage.TotalTokens != 14 || got.Usage.InputTokensDetails.CachedTokens != 8 {
		t.Fatalf("body %s (%v)", w.Body, err)
	}
	rig.mu.Lock()
	defer rig.mu.Unlock()
	if len(rig.calls) != 1 {
		t.Fatalf("%d upstream calls", len(rig.calls))
	}
	call := rig.calls[0]
	if call.path != "/acct/responses" || call.body["stream"] != true || call.body["store"] != false || call.body["model"] != "gpt-5" ||
		call.body["instructions"] == nil || call.body["temperature"] != nil || call.body["user"] != nil {
		t.Fatalf("upstream saw %s %v", call.path, call.body)
	}
	input := call.body["input"].([]any)[0].(map[string]any)
	if input["role"] != "user" || input["content"].([]any)[0].(map[string]any)["text"] != "ping" {
		t.Fatalf("input %v", input)
	}
	for name, want := range map[string]string{
		"Authorization": "Bearer secret", "Originator": "codex_cli_rs", "Accept": "text/event-stream", "Session_id": connectionKey("acct"),
	} {
		if call.header.Get(name) != want {
			t.Errorf("%s = %q, want %q", name, call.header.Get(name), want)
		}
	}
	if call.body["prompt_cache_key"] != connectionKey("acct") {
		t.Errorf("prompt_cache_key %v", call.body["prompt_cache_key"])
	}
	// The folded document keeps the endpoint's own usage, so the cached tokens reach accounting.
	if len(rig.outcomes) != 1 || rig.outcomes[0].InputTokens != 11 || rig.outcomes[0].OutputTokens != 3 || rig.outcomes[0].CacheRead != 8 ||
		rig.outcomes[0].UsageUnobserved || rig.outcomes[0].Stream {
		t.Fatalf("outcome %+v", rig.outcomes)
	}
}

// connectionKey is the opaque session a Codex request carries for a connection:
// the first 16 bytes of SHA-256 over "codex", a NUL and the connection id, in hex.
// It is written out here so a change to the derivation is a visible change.
func connectionKey(account string) string {
	sum := sha256.Sum256([]byte("codex\x00" + account))
	return hex.EncodeToString(sum[:16])
}

func TestCodexStreamingClientKeepsItsStreamByteForByte(t *testing.T) {
	rig := newResponsesRig(t, "codex", "gpt-5", []string{"acct"}, serveStream(codexStream))
	w := rig.post("/v1/responses", `{"model":"x","input":"ping","stream":true}`)
	if w.Code != 200 || w.Body.String() != codexStream {
		t.Fatalf("status %d: %q", w.Code, w.Body)
	}
	rig.mu.Lock()
	defer rig.mu.Unlock()
	if rig.calls[0].body["stream"] != true || len(rig.outcomes) != 1 || !rig.outcomes[0].Stream || rig.outcomes[0].CacheRead != 8 {
		t.Fatalf("%v %+v", rig.calls[0].body, rig.outcomes)
	}
}

func TestCodexForcedStreamReachesTranslatedClients(t *testing.T) {
	cases := map[string]struct{ path, body, want string }{
		"chat":     {"/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}]}`, `"content":"pong"`},
		"messages": {"/v1/messages", `{"model":"x","max_tokens":16,"messages":[{"role":"user","content":"ping"}]}`, `"text":"pong"`},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			rig := newResponsesRig(t, "codex", "gpt-5", []string{"acct"}, serveStream(codexStream))
			w := rig.post(tc.path, tc.body)
			if w.Code != 200 || !strings.Contains(w.Body.String(), tc.want) {
				t.Fatalf("status %d: %s", w.Code, w.Body)
			}
			rig.mu.Lock()
			defer rig.mu.Unlock()
			if len(rig.calls) != 1 || rig.calls[0].body["stream"] != true || rig.calls[0].body["store"] != false {
				t.Fatalf("upstream saw %v", rig.calls)
			}
		})
	}
}

func TestCodexCapacityInTheStreamMovesToTheNextAccount(t *testing.T) {
	capacity := frame("response.created", `{"type":"response.created","response":{"id":"r"}}`) +
		frame("error", `{"type":"error","error":{"code":"model_at_capacity","message":"Selected model is at capacity. Please try a different model."}}`)
	rig := newResponsesRig(t, "codex", "gpt-5", []string{"busy", "free"}, func(account string, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if account == "busy" {
			fmt.Fprint(w, capacity)
			return
		}
		fmt.Fprint(w, codexStream)
	})
	w := rig.post("/v1/responses", `{"model":"x","input":"ping","stream":true}`)
	if w.Code != 200 || w.Body.String() != codexStream {
		t.Fatalf("the client saw %d: %q", w.Code, w.Body)
	}
	rig.mu.Lock()
	defer rig.mu.Unlock()
	if len(rig.calls) != 2 || rig.calls[0].account != "busy" || rig.calls[1].account != "free" {
		t.Fatalf("calls %+v", rig.calls)
	}
	if len(rig.outcomes) != 1 || rig.outcomes[0].ConnectionID != "free" || rig.outcomes[0].Status != 200 {
		t.Fatalf("outcome %+v", rig.outcomes)
	}
}

func TestCodexCapacityOnTheOnlyAccountIsAnUpstreamUnavailable(t *testing.T) {
	capacity := frame("error", `{"type":"error","error":{"code":"model_at_capacity","message":"Selected model is at capacity. Please try a different model."}}`)
	rig := newResponsesRig(t, "codex", "gpt-5", []string{"only"}, serveStream(capacity))
	w := rig.post("/v1/responses", `{"model":"x","input":"ping","stream":true}`)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "Selected model is at capacity") {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
}

func TestCodexInBandFailureOfAForcedStreamIsAnAccountFailure(t *testing.T) {
	failed := frame("response.created", `{"type":"response.created","response":{"id":"r"}}`) +
		frame("response.failed", `{"type":"response.failed","response":{"id":"r","status":"failed","error":{"code":"context_length_exceeded","message":"too long"}}}`)
	rig := newResponsesRig(t, "codex", "gpt-5", []string{"a", "b"}, func(account string, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if account == "a" {
			fmt.Fprint(w, failed)
			return
		}
		fmt.Fprint(w, codexStream)
	})
	w := rig.post("/v1/responses", `{"model":"x","input":"ping"}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"resp_1"`) {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	// With nobody left to serve it, the failure is reported with its own words.
	only := newResponsesRig(t, "codex", "gpt-5", []string{"a"}, serveStream(failed))
	w = only.post("/v1/responses", `{"model":"x","input":"ping"}`)
	if w.Code != 502 || !strings.Contains(w.Body.String(), "too long") {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
}

func TestCodexCompactGoesToTheCompactEndpointAsTheSmallDocument(t *testing.T) {
	const answer = `{"output":[{"type":"compaction","encrypted_content":"abc"}],"usage":{"input_tokens":4,"output_tokens":1,"total_tokens":5}}`
	rig := newResponsesRig(t, "codex", "gpt-5", []string{"acct"}, func(_ string, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, answer)
	})
	w := rig.post("/v1/responses/compact", `{"model":"x","input":[{"role":"user","content":"hi"}],"instructions":"i","store":true,"tools":[],"prompt_cache_key":"k"}`,
		"x-codex-beta-features", "remote_compaction")
	if w.Code != 200 || w.Body.String() != answer {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	rig.mu.Lock()
	defer rig.mu.Unlock()
	call := rig.calls[0]
	if call.path != "/acct/responses/compact" || len(call.body) != 3 || call.body["model"] != "gpt-5" || call.body["instructions"] != "i" {
		t.Fatalf("upstream saw %s %v", call.path, call.body)
	}
	if call.header.Get("X-Codex-Beta-Features") != "remote_compaction" || call.header.Get("Accept") == "text/event-stream" {
		t.Fatalf("headers %v", call.header)
	}
	// A request that carries no beta opt-in sends none.
	rig2 := newResponsesRig(t, "codex", "gpt-5", []string{"acct"}, serveStream(codexStream))
	rig2.post("/v1/responses", `{"model":"x","input":"ping","stream":true}`)
	if got := rig2.calls[0].header.Get("X-Codex-Beta-Features"); got != "" {
		t.Fatalf("a beta header was invented: %q", got)
	}
}

func TestBetaHeaderIsNotForwardedToOtherProviders(t *testing.T) {
	rig := newResponsesRig(t, "kenari", "gpt-5", []string{"acct"}, serveStream(codexStream))
	rig.post("/v1/responses", `{"model":"x","input":"ping","stream":true}`, "x-codex-beta-features", "remote_compaction")
	if got := rig.calls[0].header.Get("X-Codex-Beta-Features"); got != "" {
		t.Fatalf("forwarded to kenari: %q", got)
	}
}

func TestGrokCLINonStreamClientGetsOneJSONFromAForcedStream(t *testing.T) {
	rig := newResponsesRig(t, "grok-cli", "grok-4.5-high", []string{"acct"}, serveStream(codexStream))
	w := rig.post("/v1/responses", `{"model":"x","input":"ping","tools":[{"type":"function","function":{"name":"f"}}]}`)
	if w.Code != 200 || !strings.Contains(w.Body.String(), `"id":"resp_1"`) {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	rig.mu.Lock()
	defer rig.mu.Unlock()
	call := rig.calls[0]
	if call.body["model"] != "grok-4.5" || call.body["stream"] != true || call.body["store"] != false ||
		call.body["reasoning"].(map[string]any)["effort"] != "high" || call.body["tools"].([]any)[0].(map[string]any)["name"] != "f" {
		t.Fatalf("upstream saw %v", call.body)
	}
	for name, want := range map[string]string{
		"Authorization": "Bearer secret", "X-Grok-Client-Identifier": "grok-shell", "X-Grok-Turn-Idx": "1",
		"X-Grok-Model-Override": "grok-4.5", "Accept": "text/event-stream", "User-Agent": "grok-shell/1.0.44 (linux; x86_64)",
	} {
		if call.header.Get(name) != want {
			t.Errorf("%s = %q, want %q", name, call.header.Get(name), want)
		}
	}
	if call.header.Get("X-Grok-Session-Id") == "" || call.header.Get("X-Grok-Session-Id") != call.header.Get("X-Grok-Conv-Id") {
		t.Errorf("session headers %v", call.header)
	}
}

func TestForcedStreamIsNotAppliedToProvidersThatNeverAskedForIt(t *testing.T) {
	// openai answers a non-stream request with JSON and keeps doing so.
	const answer = `{"id":"resp_x","object":"response","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}`
	rig := newResponsesRig(t, "openai", "gpt-5", []string{"acct"}, func(_ string, w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, answer)
	})
	w := rig.post("/v1/responses", `{"model":"x","input":"ping","stream":false}`)
	if w.Code != 200 || w.Body.String() != answer {
		t.Fatalf("status %d: %s", w.Code, w.Body)
	}
	if rig.calls[0].body["stream"] != false || rig.calls[0].body["input"] != "ping" {
		t.Fatalf("an unrelated provider's body was rewritten: %v", rig.calls[0].body)
	}
}

// The client leaving while the head of a Codex stream is being read stops the
// upstream request; nothing waits on a stream nobody wants.
func TestCodexInspectionEndsWhenTheClientLeaves(t *testing.T) {
	released := make(chan struct{})
	var once sync.Once
	rig := newResponsesRig(t, "codex", "gpt-5", []string{"acct"}, func(_ string, w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(200)
		fmt.Fprint(w, frame("response.created", `{"type":"response.created","response":{"id":"r"}}`))
		w.(http.Flusher).Flush()
		<-r.Context().Done() // the endpoint says nothing more
		once.Do(func() { close(released) })
	})
	ctx, cancel := context.WithCancel(context.Background())
	request := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"x","input":"ping","stream":true}`)).WithContext(ctx)
	request.Header.Set("Authorization", "Bearer "+rig.key)
	done := make(chan struct{})
	go func() {
		rig.mux.ServeHTTP(httptest.NewRecorder(), request)
		close(done)
	}()
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("the handler is still waiting on the stream after the client left")
	}
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("the upstream request was not cancelled")
	}
}
