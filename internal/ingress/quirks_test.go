package ingress

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// TestRegistryQuirksReachTheUpstreamBody drives a real request through the
// handler for each quirk provider and checks the body the upstream received. The
// ephemeral cache_control on the last tool is prompt-cache anchoring, which runs
// before the quirks (AGENTS §6): the quirks see, and keep, the anchored body.
func TestRegistryQuirksReachTheUpstreamBody(t *testing.T) {
	cases := []struct {
		name, provider, protocol, path, endpoint, body, want string
	}{
		{"cerebras drops client_metadata", "cerebras", "openai-chat", "/v1/chat/completions", "chat/completions",
			`{"model":"m","client_metadata":{"a":1},"messages":[{"role":"user","content":"hi"}]}`,
			`{"model":"m","messages":[{"role":"user","content":"hi"}]}`},
		{"mistral drops client_metadata", "mistral", "openai-chat", "/v1/chat/completions", "chat/completions",
			`{"model":"m","client_metadata":1,"messages":[{"role":"user","content":"hi"}]}`,
			`{"model":"m","messages":[{"role":"user","content":"hi"}]}`},
		{"minimax types tools and drops output_config", "minimax", "anthropic-messages", "/v1/messages", "v1/messages",
			`{"model":"m","max_tokens":5,"output_config":{"effort":"high"},"tools":[{"name":"a","input_schema":{"type":"object"}}],"messages":[{"role":"user","content":"hi"}]}`,
			`{"model":"m","max_tokens":5,"tools":[{"name":"a","input_schema":{"type":"object"},"type":"custom","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hi"}]}`},
		{"deepseek drops unsupported tool types", "deepseek", "anthropic-messages", "/v1/messages", "anthropic/v1/messages",
			`{"model":"m","max_tokens":5,"tools":[{"name":"a","type":"custom","input_schema":{}},{"type":"web_search_20250305","name":"w"}],"messages":[{"role":"user","content":"hi"}]}`,
			`{"model":"m","max_tokens":5,"tools":[{"type":"web_search_20250305","name":"w","cache_control":{"type":"ephemeral"}}],"messages":[{"role":"user","content":"hi"}]}`},
		{"kenari folds the system role", "kenari", "openai-responses", "/v1/responses", "v1/responses",
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
