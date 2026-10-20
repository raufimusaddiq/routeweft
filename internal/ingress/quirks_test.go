package ingress

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	anthropicadapter "github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
	"github.com/raufimusaddiq/routeweft/internal/transforms/body"
)

// TestRegistryQuirksReachTheUpstreamBody drives a real request through the
// handler for each quirk provider and checks the body the upstream received. The
// ephemeral cache_control on the last tool is prompt-cache anchoring, which runs
// before the quirks (AGENTS §6); TestQuirksRunAfterTheSavers pins the order.
func TestRegistryQuirksReachTheUpstreamBody(t *testing.T) {
	cases := []struct {
		name, provider, protocol, path, endpoint, body, want string
	}{
		{"cerebras drops client_metadata", "cerebras", openaiadapter.ChatProtocol, "/v1/chat/completions", "chat/completions",
			`{"model":"m","client_metadata":{"a":1},"messages":[{"role":"user","content":"hi"}]}`,
			`{"model":"m","messages":[{"role":"user","content":"hi"}]}`},
		{"mistral drops client_metadata", "mistral", openaiadapter.ChatProtocol, "/v1/chat/completions", "chat/completions",
			`{"model":"m","client_metadata":1,"messages":[{"role":"user","content":"hi"}]}`,
			`{"model":"m","messages":[{"role":"user","content":"hi"}]}`},
		{"minimax types tools and drops output_config", "minimax", anthropicadapter.MessagesProtocol, "/v1/messages", "v1/messages",
			`{"model":"m","max_tokens":5,"output_config":{"effort":"high"},"tools":[{"name":"a","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`,
			`{"model":"m","max_tokens":5,"tools":[{"name":"a","input_schema":{"type":"object"},"type":"custom","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hi"}]}`},
		{"deepseek drops unsupported tool types", "deepseek", anthropicadapter.MessagesProtocol, "/v1/messages", "anthropic/v1/messages",
			`{"model":"m","max_tokens":5,"tools":[{"name":"a","type":"custom","input_schema":{}},{"type":"web_search_20250305","name":"w"}],"messages":[{"role":"user","content":"hi"}]}`,
			`{"model":"m","max_tokens":5,"tools":[{"type":"web_search_20250305","name":"w","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hi"}]}`},
		{"kenari folds the system role", "kenari", openaiadapter.ResponsesProtocol, "/v1/responses", "v1/responses",
			`{"model":"m","input":[{"type":"message","role":"system","content":"sys"},{"type":"message","role":"user","content":"hi"}]}`,
			`{"model":"m","input":[{"type":"message","role":"user","content":"sys\n\nhi"}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got []byte
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got, _ = io.ReadAll(r.Body)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer upstream.Close()
			mux, key := keyedHandler(t, Options{AllowPrivateUpstreams: true,
				ProviderResolver: func(*runtime.RuntimeSnapshot, string) (routing.ProviderRef, bool) {
					return routing.ProviderRef{ProviderID: c.provider, Protocol: c.protocol, BaseURL: upstream.URL, Endpoint: c.endpoint, APIToken: "token"}, true
				},
			})
			recorder := httptest.NewRecorder()
			bearer(mux, key).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, c.path, strings.NewReader(c.body)))
			var g, w any
			if json.Unmarshal(got, &g) != nil || json.Unmarshal([]byte(c.want), &w) != nil || !reflect.DeepEqual(g, w) {
				t.Fatalf("upstream body\n got %s\nwant %s (status %d)", got, c.want, recorder.Code)
			}
		})
	}
}

// TestQuirksRunAfterTheSavers is the AGENTS §6 step 8 guard. The final-body
// transform stands in for the saver pipeline: it records what it is handed, and
// it re-injects the fields a quirk removes, as the Responses policy injector does
// with `instructions` (transforms/body/body.go). If the quirks ran first, the
// transform would see the quirked body and the injected field would reach the
// upstream.
func TestQuirksRunAfterTheSavers(t *testing.T) {
	cases := []struct {
		name, provider, protocol, path, endpoint, body string
		seen                                           string // substring the transform must still see
		inject                                         func(map[string]any)
		absent                                         string // substring the upstream body must not carry
	}{
		{"kenari instructions", "kenari", openaiadapter.ResponsesProtocol, "/v1/responses", "v1/responses",
			`{"model":"m","input":[{"type":"message","role":"system","content":"sys"},{"type":"message","role":"user","content":"hi"}]}`,
			`"role":"system"`,
			func(m map[string]any) { m["instructions"] = "caveman policy" },
			`"instructions"`},
		{"cerebras client_metadata", "cerebras", openaiadapter.ChatProtocol, "/v1/chat/completions", "chat/completions",
			`{"model":"m","client_metadata":{"a":1},"messages":[{"role":"user","content":"hi"}]}`,
			`"client_metadata"`,
			func(m map[string]any) { m["client_metadata"] = "reintroduced" },
			`"client_metadata"`},
		{"minimax output_config", "minimax", anthropicadapter.MessagesProtocol, "/v1/messages", "v1/messages",
			`{"model":"m","max_tokens":5,"output_config":{"effort":"high"},"tools":[{"name":"a","input_schema":{}}],"messages":[{"role":"user","content":"hi"}]}`,
			`"output_config"`,
			func(m map[string]any) { m["output_config"] = map[string]any{"effort": "low"} },
			`"output_config"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var upstreamBody, transformSaw string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				upstreamBody = string(raw)
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{}`))
			}))
			defer upstream.Close()
			mux, key := keyedHandler(t, Options{AllowPrivateUpstreams: true,
				TransformFinalBody: func(_ context.Context, _ *runtime.RuntimeSnapshot, _ string, body []byte) ([]byte, error) {
					transformSaw = string(body)
					var fields map[string]any
					if err := json.Unmarshal(body, &fields); err != nil {
						return nil, err
					}
					c.inject(fields)
					return json.Marshal(fields)
				},
				ProviderResolver: func(*runtime.RuntimeSnapshot, string) (routing.ProviderRef, bool) {
					return routing.ProviderRef{ProviderID: c.provider, Protocol: c.protocol, BaseURL: upstream.URL, Endpoint: c.endpoint, APIToken: "token"}, true
				},
			})
			recorder := httptest.NewRecorder()
			bearer(mux, key).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, c.path, strings.NewReader(c.body)))
			if !strings.Contains(transformSaw, c.seen) {
				t.Fatalf("the transform saw a body the quirk had already rewritten (missing %s): %s", c.seen, transformSaw)
			}
			if upstreamBody == "" || strings.Contains(upstreamBody, c.absent) {
				t.Fatalf("a transform reintroduced what the quirk removes (status %d): %s", recorder.Code, upstreamBody)
			}
		})
	}
}

// The real Responses policy injector writes the caveman policy into
// `instructions`, the field kenari's fold removes. With the quirk last, the
// upstream carries the policy folded into the user turn and no `instructions`.
func TestKenariFoldSurvivesTheRealPolicyInjector(t *testing.T) {
	for _, input := range []string{`[{"type":"message","role":"user","content":"hi"}]`, `"hi"`} {
		t.Run(input, func(t *testing.T) { kenariFoldWithRealInjector(t, input) })
	}
}

func kenariFoldWithRealInjector(t *testing.T, input string) {
	var upstreamBody string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		upstreamBody = string(raw)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer upstream.Close()
	pipeline := body.Compile(map[string]string{"cavemanEnabled": "true"})
	mux, key := keyedHandler(t, Options{AllowPrivateUpstreams: true,
		TransformFinalBody: func(ctx context.Context, _ *runtime.RuntimeSnapshot, protocol string, raw []byte) ([]byte, error) {
			return pipeline.Run(ctx, protocol, raw, true)
		},
		ProviderResolver: func(*runtime.RuntimeSnapshot, string) (routing.ProviderRef, bool) {
			return routing.ProviderRef{ProviderID: "kenari", Protocol: openaiadapter.ResponsesProtocol, BaseURL: upstream.URL, Endpoint: "v1/responses", APIToken: "token"}, true
		},
	})
	recorder := httptest.NewRecorder()
	bearer(mux, key).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(
		`{"model":"m","input":`+input+`}`)))
	if strings.Contains(upstreamBody, `"instructions"`) || !strings.Contains(upstreamBody, "[routeweft:caveman]") || !strings.Contains(upstreamBody, "hi") {
		t.Fatalf("kenari body (status %d): %s", recorder.Code, upstreamBody)
	}
}

// kenari answers a converted custom tool with a function_call; the Responses client
// declared a custom tool and must get a custom_tool_call back, on a stream and off
// it. Unreadable arguments are refused with a named error, not relayed.
func TestKenariCustomToolResponseRewrite(t *testing.T) {
	const request = `{"model":"m","stream":%t,"input":"hi","tools":[{"type":"custom","name":"exec"}]}`
	const goodBody = `{"id":"r","object":"response","status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"c","name":"exec","arguments":"{\"input\":\"ls\"}"}],"usage":{"input_tokens":1,"output_tokens":1}}`
	const goodStream = "event: response.output_item.done\ndata: {\"type\":\"response.output_item.done\",\"output_index\":0,\"item\":{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"c\",\"name\":\"exec\",\"arguments\":\"{\\\"input\\\":\\\"ls\\\"}\"}}\n\n" +
		"event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"output\":[]}}\n\n"
	cases := []struct {
		name         string
		stream       bool
		upstream     string
		status       int
		contains     []string
		doesNotCarry []string
	}{
		{"body", false, goodBody, 200, []string{`"type":"custom_tool_call"`, `"input":"ls"`, `"id":"ctc_1"`}, []string{`function_call"`, `arguments`}},
		{"stream", true, goodStream, 200, []string{`"type":"custom_tool_call"`, `"input":"ls"`}, []string{`"type":"function_call"`}},
		{"body refused", false, strings.Replace(goodBody, `{\"input\":\"ls\"}`, `ls`, 1), 502, []string{"kenari_custom_tool_arguments_invalid"}, []string{"custom_tool_call"}},
		{"stream refused", true, strings.Replace(goodStream, "{\\\"input\\\":\\\"ls\\\"}", "ls", 1), 200, []string{"response.failed", "kenari_custom_tool_arguments_invalid"}, []string{"custom_tool_call"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var sent string
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				raw, _ := io.ReadAll(r.Body)
				sent = string(raw)
				if c.stream {
					w.Header().Set("Content-Type", "text/event-stream")
				} else {
					w.Header().Set("Content-Type", "application/json")
				}
				_, _ = w.Write([]byte(c.upstream))
			}))
			defer upstream.Close()
			mux, key := keyedHandler(t, Options{AllowPrivateUpstreams: true,
				ProviderResolver: func(*runtime.RuntimeSnapshot, string) (routing.ProviderRef, bool) {
					return routing.ProviderRef{ProviderID: "kenari", Protocol: openaiadapter.ResponsesProtocol, BaseURL: upstream.URL, Endpoint: "v1/responses", APIToken: "token"}, true
				},
			})
			recorder := httptest.NewRecorder()
			bearer(mux, key).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(fmt.Sprintf(request, c.stream))))
			got := recorder.Body.String()
			if !strings.Contains(sent, `"type":"function"`) || strings.Contains(sent, `"custom"`) {
				t.Errorf("upstream request kept the custom tool: %s", sent)
			}
			if recorder.Code != c.status {
				t.Errorf("status %d, want %d: %s", recorder.Code, c.status, got)
			}
			for _, want := range c.contains {
				if !strings.Contains(got, want) {
					t.Errorf("client body lacks %s: %s", want, got)
				}
			}
			for _, bad := range c.doesNotCarry {
				if strings.Contains(got, bad) {
					t.Errorf("client body carries %s: %s", bad, got)
				}
			}
		})
	}
}
