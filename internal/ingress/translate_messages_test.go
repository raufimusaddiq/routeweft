package ingress

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// One synthetic exchange per wire: the model says "pong", calls lookup once with
// id call_1, and stops for tool use. Usage is 130 prompt tokens of which 100 were
// served from cache, 42 output. Each wire reports that in its own convention:
// Chat and Responses count the cache inside the prompt, Messages reports it
// separately (input_tokens 30 + cache_read 100).
var upstreamBodies = map[string]map[bool]string{
	openai.ChatProtocol: {
		false: `{"id":"chatcmpl-up0001","object":"chat.completion","model":"vendor/m","choices":[{"index":0,"message":{"role":"assistant","content":"pong","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"city\":\"Oslo\"}"}}]},"finish_reason":"tool_calls"}],"usage":{"prompt_tokens":130,"completion_tokens":42,"total_tokens":172,"prompt_tokens_details":{"cached_tokens":100}}}`,
		true: `data: {"id":"chatcmpl-up0001","model":"vendor/m","choices":[{"index":0,"delta":{"role":"assistant","content":"pong"}}]}

data: {"id":"chatcmpl-up0001","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"city\":"}}]}}]}

data: {"id":"chatcmpl-up0001","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"Oslo\"}"}}]}}]}

data: {"id":"chatcmpl-up0001","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: {"id":"chatcmpl-up0001","choices":[],"usage":{"prompt_tokens":130,"completion_tokens":42,"total_tokens":172,"prompt_tokens_details":{"cached_tokens":100}}}

data: [DONE]

`,
	},
	anthropic.MessagesProtocol: {
		false: `{"id":"msg_up0001","type":"message","role":"assistant","model":"vendor/m","content":[{"type":"text","text":"pong"},{"type":"tool_use","id":"call_1","name":"lookup","input":{"city":"Oslo"}}],"stop_reason":"tool_use","usage":{"input_tokens":30,"cache_read_input_tokens":100,"output_tokens":42}}`,
		true: `event: message_start
data: {"type":"message_start","message":{"id":"msg_up0001","model":"vendor/m","usage":{"input_tokens":30,"cache_read_input_tokens":100,"output_tokens":1}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pong"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"call_1","name":"lookup","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\":\"Oslo\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":42}}

event: message_stop
data: {"type":"message_stop"}

`,
	},
	openai.ResponsesProtocol: {
		false: `{"id":"resp_up0001","object":"response","status":"completed","model":"vendor/m","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"pong"}]},{"type":"function_call","call_id":"call_1","name":"lookup","arguments":"{\"city\":\"Oslo\"}"}],"usage":{"input_tokens":130,"output_tokens":42,"total_tokens":172,"input_tokens_details":{"cached_tokens":100}}}`,
		true: `event: response.created
data: {"type":"response.created","response":{"id":"resp_up0001","created_at":1735689602,"model":"vendor/m","status":"in_progress"}}

event: response.output_text.delta
data: {"type":"response.output_text.delta","item_id":"msg_1","delta":"pong"}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":""}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","delta":"{\"city\":\"Oslo\"}"}

event: response.output_item.done
data: {"type":"response.output_item.done","item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":"{\"city\":\"Oslo\"}"}}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_up0001","status":"completed","output":[],"usage":{"input_tokens":130,"output_tokens":42,"total_tokens":172,"input_tokens_details":{"cached_tokens":100}}}}

`,
	},
}

var clientRequests = map[string]struct{ path, body string }{
	openai.ChatProtocol:        {"/v1/chat/completions", `{"model":"test/model","stream":%t,"messages":[{"role":"user","content":"ping"}]}`},
	openai.ResponsesProtocol:   {"/v1/responses", `{"model":"test/model","stream":%t,"input":"ping"}`},
	anthropic.MessagesProtocol: {"/v1/messages", `{"model":"test/model","max_tokens":50,"stream":%t,"messages":[{"role":"user","content":"ping"}]}`},
}

var upstreamPaths = map[string]string{openai.ChatProtocol: "/chat/completions", openai.ResponsesProtocol: "/responses", anthropic.MessagesProtocol: "/messages"}

// What a client in each wire must be able to read back from the exchange above.
func assertClientSees(t *testing.T, client string, stream bool, body string) {
	t.Helper()
	need := func(parts ...string) {
		t.Helper()
		for _, part := range parts {
			if !strings.Contains(body, part) {
				t.Errorf("%s client (stream=%v) response lacks %q:\n%s", client, stream, part, body)
			}
		}
	}
	switch client {
	case openai.ChatProtocol:
		need("pong", `"call_1"`, `"lookup"`, `"tool_calls"`, `"prompt_tokens":130`, `"cached_tokens":100`, `"completion_tokens":42`)
	case anthropic.MessagesProtocol:
		need("pong", `"call_1"`, `"lookup"`, `"tool_use"`, `"input_tokens":30`, `"cache_read_input_tokens":100`, `"output_tokens":42`)
	case openai.ResponsesProtocol:
		need("pong", `"call_1"`, `"lookup"`, `"input_tokens":130`, `"cached_tokens":100`, `"output_tokens":42`)
	}
}

// upstreamRejects is the part of each real upstream's contract that a leg can
// violate: Anthropic gates cache ttl behind a beta header, wants a signed
// thinking block or none, takes no empty list, and nothing may claim to be
// someone else's CLI; Chat takes no empty message list or tools array either.
func upstreamRejects(provider string, header http.Header, body map[string]any, raw string) string {
	switch provider {
	case anthropic.MessagesProtocol:
		if strings.Contains(raw, `"ttl"`) && !strings.Contains(header.Get("Anthropic-Beta"), "extended-cache-ttl") {
			return "cache_control.ttl requires the extended-cache-ttl beta"
		}
		if strings.Contains(raw, "Claude Code") {
			return "system prompt claims an identity the caller never set"
		}
		if tools, present := body["tools"].([]any); present && len(tools) == 0 {
			return "tools: must not be empty"
		}
		if messages, _ := body["messages"].([]any); len(messages) == 0 {
			return "messages: at least one message is required"
		}
		for _, message := range concerns.Items(body["messages"]) {
			for _, block := range concerns.Items(concerns.Object(message)["content"]) {
				if b := concerns.Object(block); b["type"] == "thinking" && concerns.Text(b["signature"]) == "" {
					return "thinking.signature: field required"
				}
			}
		}
	case openai.ChatProtocol:
		if messages, _ := body["messages"].([]any); len(messages) == 0 {
			return "messages: must not be empty"
		}
		if tools, present := body["tools"].([]any); present && len(tools) == 0 {
			return "tools: must not be empty"
		}
	}
	return ""
}

// Every client wire reaches every provider wire it can be converted to, on both
// the buffered and the streamed path, with the provider's own credential and
// path, the client's tool id, text, finish reason and usage intact, and exactly
// one usage record carrying what the upstream reported.
func TestEveryConvertedPairServesAClient(t *testing.T) {
	wires := []string{openai.ChatProtocol, openai.ResponsesProtocol, anthropic.MessagesProtocol}
	for _, client := range wires {
		for _, provider := range wires {
			if client == provider {
				continue
			}
			for _, stream := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s->%s/stream=%v", client, provider, stream), func(t *testing.T) {
					var seen struct {
						sync.Mutex
						path, auth, apiKey, version string
						status                      int
						body                        map[string]any
					}
					upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						seen.Lock()
						defer seen.Unlock()
						seen.path, seen.auth, seen.apiKey, seen.version = r.URL.Path, r.Header.Get("Authorization"), r.Header.Get("X-Api-Key"), r.Header.Get("Anthropic-Version")
						raw, _ := io.ReadAll(r.Body)
						seen.body = map[string]any{}
						_ = json.Unmarshal(raw, &seen.body)
						// The fake enforces what each real upstream enforces, so a body
						// the header or the wire contract does not allow is a 400 here
						// as it would be there, not a 200 the test cannot see.
						if reason := upstreamRejects(provider, r.Header, seen.body, string(raw)); reason != "" {
							seen.status = http.StatusBadRequest
							w.Header().Set("Content-Type", "application/json")
							w.WriteHeader(http.StatusBadRequest)
							fmt.Fprintf(w, `{"error":{"type":"invalid_request_error","message":%q}}`, reason)
							return
						}
						seen.status = http.StatusOK
						if stream {
							w.Header().Set("Content-Type", "text/event-stream")
						} else {
							w.Header().Set("Content-Type", "application/json")
						}
						w.WriteHeader(http.StatusOK)
						fmt.Fprint(w, upstreamBodies[provider][stream])
					}))
					defer upstream.Close()
					manager := newManager(t)
					_, key, err := manager.CreateAPIKey(context.Background(), "test")
					if err != nil {
						t.Fatal(err)
					}
					var outcomes []RequestOutcome
					var mu sync.Mutex
					mux := http.NewServeMux()
					New(manager, Options{
						AllowPrivateUpstreams: true,
						TranslateChat:         TranslateChat, TranslateResponses: TranslateResponses, TranslateMessages: TranslateMessages,
						Candidates: func(*runtime.RuntimeSnapshot, string) ([]routing.Account, bool) {
							return []routing.Account{{ID: "a", Enabled: true}}, true
						},
						AccountProvider: func(_ *runtime.RuntimeSnapshot, _, account, source string) (routing.ProviderRef, bool) {
							if !CanTranslate(source, provider) && source != provider {
								return routing.ProviderRef{}, false
							}
							return routing.ProviderRef{ProviderID: "test", ConnectionID: account, BaseURL: upstream.URL, Protocol: provider, UpstreamModel: "vendor/m", APIToken: "secret"}, true
						},
						OnRequestComplete: func(outcome RequestOutcome) { mu.Lock(); outcomes = append(outcomes, outcome); mu.Unlock() },
					}).Attach(mux)
					request := clientRequests[client]
					r := httptest.NewRequest(http.MethodPost, request.path, strings.NewReader(fmt.Sprintf(request.body, stream)))
					r.Header.Set("Authorization", "Bearer "+key)
					w := httptest.NewRecorder()
					mux.ServeHTTP(w, r)
					seen.Lock()
					defer seen.Unlock()
					if seen.status != http.StatusOK || w.Code != http.StatusOK {
						t.Fatalf("upstream answered %d, client got %d: %s", seen.status, w.Code, w.Body)
					}
					if seen.path != upstreamPaths[provider] {
						t.Errorf("upstream path %q want %q", seen.path, upstreamPaths[provider])
					}
					if seen.body["model"] != "vendor/m" || seen.body["stream"] != stream {
						t.Errorf("upstream body model/stream: %v", seen.body)
					}
					if provider == anthropic.MessagesProtocol {
						if seen.apiKey != "secret" || seen.auth != "" || seen.version == "" {
							t.Errorf("Messages upstream credential: x-api-key=%q authorization=%q version=%q", seen.apiKey, seen.auth, seen.version)
						}
					} else if seen.auth != "Bearer secret" {
						t.Errorf("upstream authorization %q", seen.auth)
					}
					assertClientSees(t, client, stream, w.Body.String())
					mu.Lock()
					defer mu.Unlock()
					if len(outcomes) != 1 {
						t.Fatalf("want exactly one usage record, got %d", len(outcomes))
					}
					got := outcomes[0]
					// The record carries the upstream's own figures. A Messages
					// upstream reports its cache outside the prompt, so its input
					// is 30 where Chat and Responses report 130; reconciling
					// that is the accounting rule awaiting a decision on #119.
					wantInput := int64(130)
					if provider == anthropic.MessagesProtocol {
						wantInput = 30
					}
					if got.UsageUnobserved || got.Termination != "" || got.InputTokens != wantInput || got.CacheRead != 100 || got.OutputTokens != 42 {
						t.Errorf("usage record %+v", got)
					}
				})
			}
		}
	}
}

// A Chat provider that omits the usage chunk for a Messages client's stream is
// asked for it, because the Messages protocol reports usage in message_delta.
func TestMessagesStreamAsksChatProviderForUsage(t *testing.T) {
	var body map[string]any
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, upstreamBodies[openai.ChatProtocol][true])
	}))
	defer upstream.Close()
	manager := newManager(t)
	_, key, _ := manager.CreateAPIKey(context.Background(), "test")
	mux := http.NewServeMux()
	New(manager, Options{
		AllowPrivateUpstreams: true, TranslateMessages: TranslateMessages,
		Candidates: func(*runtime.RuntimeSnapshot, string) ([]routing.Account, bool) {
			return []routing.Account{{ID: "a", Enabled: true}}, true
		},
		AccountProvider: func(_ *runtime.RuntimeSnapshot, _, account, _ string) (routing.ProviderRef, bool) {
			return routing.ProviderRef{ProviderID: "test", ConnectionID: account, BaseURL: upstream.URL, Protocol: openai.ChatProtocol}, true
		},
	}).Attach(mux)
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(fmt.Sprintf(clientRequests[anthropic.MessagesProtocol].body, true)))
	r.Header.Set("Authorization", "Bearer "+key)
	mux.ServeHTTP(httptest.NewRecorder(), r)
	if options := concerns.Object(body["stream_options"]); options["include_usage"] != true {
		t.Fatalf("stream_options %v", body["stream_options"])
	}
}

// A provider whose wire has no leg yet is skipped rather than half-served.
func TestUnconvertedWireIsStillSkipped(t *testing.T) {
	for _, client := range []string{openai.ChatProtocol, openai.ResponsesProtocol, anthropic.MessagesProtocol} {
		for _, provider := range []string{"gemini", "kiro", "cursor", "ollama", "antigravity", "vertex", "gemini-cli", "commandcode"} {
			if CanTranslate(client, provider) {
				t.Errorf("%s client claims to reach %s, which has no conversion leg", client, provider)
			}
		}
	}
}

// A first leg parked reading the upstream must be released when the second leg
// fails, or a stalled upstream parks the handler forever.
func TestChainStreamsReleasesAStalledFirstLeg(t *testing.T) {
	reader, writer := io.Pipe()
	defer writer.Close()
	first := func(_ context.Context, src io.Reader, _ io.Writer) error {
		_, err := io.Copy(io.Discard, src)
		return err
	}
	second := func(context.Context, io.Reader, io.Writer) error { return errors.New("second leg failed") }
	done := make(chan error, 1)
	go func() {
		done <- chainStreams(first, second)(context.Background(), struct {
			io.Reader
			io.Closer
		}{reader, reader}, io.Discard)
	}()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("the failure must be returned")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("chainStreams did not return while the first leg was blocked on the source")
	}
}

// A candidate whose wire cannot express the request is skipped, not fatal: the
// request succeeds on the next candidate whatever the order.
func TestTranslationFailureMovesToTheNextCandidate(t *testing.T) {
	var contacted []string
	var mu sync.Mutex
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		contacted = append(contacted, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, upstreamBodies[openai.ResponsesProtocol][false])
	}))
	defer upstream.Close()
	manager := newManager(t)
	_, key, _ := manager.CreateAPIKey(context.Background(), "test")
	mux := http.NewServeMux()
	New(manager, Options{
		AllowPrivateUpstreams: true,
		TranslateMessages: func(request *anthropic.MessagesRequest, target string) (ChatTranslation, error) {
			if target == openai.ChatProtocol {
				return ChatTranslation{}, errors.New("cannot express this request for Chat")
			}
			return TranslateMessages(request, target)
		},
		Candidates: func(*runtime.RuntimeSnapshot, string) ([]routing.Account, bool) {
			return []routing.Account{{ID: "chat", Enabled: true}, {ID: "responses", Enabled: true}}, true
		},
		AccountProvider: func(_ *runtime.RuntimeSnapshot, _, account, _ string) (routing.ProviderRef, bool) {
			protocol := openai.ChatProtocol
			if account == "responses" {
				protocol = openai.ResponsesProtocol
			}
			return routing.ProviderRef{ProviderID: "test", ConnectionID: account, BaseURL: upstream.URL, Protocol: protocol}, true
		},
	}).Attach(mux)
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(fmt.Sprintf(clientRequests[anthropic.MessagesProtocol].body, false)))
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	mu.Lock()
	defer mu.Unlock()
	if w.Code != 200 || len(contacted) != 1 || contacted[0] != "/responses" {
		t.Fatalf("status %d contacted %v body %s", w.Code, contacted, w.Body)
	}
}

// When the upstream rejects what the leg sent, the client sees that rejection,
// with its status and body, and the failure is recorded once as a failure.
func TestUpstreamRejectionReachesTheClient(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"type":"error","error":{"type":"invalid_request_error","message":"cache_control.ttl requires the extended-cache-ttl beta"}}`)
	}))
	defer upstream.Close()
	manager := newManager(t)
	_, key, _ := manager.CreateAPIKey(context.Background(), "test")
	var outcomes []RequestOutcome
	var mu sync.Mutex
	mux := http.NewServeMux()
	New(manager, Options{
		AllowPrivateUpstreams: true, TranslateChat: TranslateChat,
		Candidates: func(*runtime.RuntimeSnapshot, string) ([]routing.Account, bool) {
			return []routing.Account{{ID: "a", Enabled: true}}, true
		},
		AccountProvider: func(_ *runtime.RuntimeSnapshot, _, account, _ string) (routing.ProviderRef, bool) {
			return routing.ProviderRef{ProviderID: "test", ConnectionID: account, BaseURL: upstream.URL, Protocol: anthropic.MessagesProtocol}, true
		},
		OnRequestComplete: func(o RequestOutcome) { mu.Lock(); outcomes = append(outcomes, o); mu.Unlock() },
	}).Attach(mux)
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(fmt.Sprintf(clientRequests[openai.ChatProtocol].body, false)))
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	mu.Lock()
	defer mu.Unlock()
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "extended-cache-ttl") || len(outcomes) != 1 || outcomes[0].Status != http.StatusBadRequest {
		t.Fatalf("status %d body %s outcomes %+v", w.Code, w.Body, outcomes)
	}
}

// With nothing attempted, the translation failure is what is reported, not an
// unrelated error from a candidate skipped afterwards.
func TestTranslationErrorIsKeptWhenNothingWasAttempted(t *testing.T) {
	manager := newManager(t)
	_, key, _ := manager.CreateAPIKey(context.Background(), "test")
	mux := http.NewServeMux()
	New(manager, Options{
		AllowPrivateUpstreams: true,
		TranslateMessages: func(*anthropic.MessagesRequest, string) (ChatTranslation, error) {
			return ChatTranslation{}, errors.New("unsupported: document source \"url\"")
		},
		Candidates: func(*runtime.RuntimeSnapshot, string) ([]routing.Account, bool) {
			return []routing.Account{{ID: "first", Enabled: true}, {ID: "second", Enabled: true}}, true
		},
		CredentialFor: func(_ context.Context, account string) (string, error) {
			if account == "second" {
				return "", errors.New("vault unavailable")
			}
			return "token", nil
		},
		AccountProvider: func(_ *runtime.RuntimeSnapshot, _, account, _ string) (routing.ProviderRef, bool) {
			return routing.ProviderRef{ProviderID: "test", ConnectionID: account, BaseURL: "http://127.0.0.1:1", Protocol: openai.ChatProtocol}, true
		},
	}).Attach(mux)
	r := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(fmt.Sprintf(clientRequests[anthropic.MessagesProtocol].body, false)))
	r.Header.Set("Authorization", "Bearer "+key)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != http.StatusBadGateway || !strings.Contains(w.Body.String(), "request_translation_failed") {
		t.Fatalf("status %d body %s", w.Code, w.Body)
	}
}
