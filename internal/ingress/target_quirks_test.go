package ingress

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	anthropicadapter "github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

type seenRequest struct {
	Path, Query string
	Header      http.Header
	Body        string
}

type recorder struct {
	mu   sync.Mutex
	seen []seenRequest
	srv  *httptest.Server
}

func newRecorder(t *testing.T) *recorder {
	t.Helper()
	r := &recorder{}
	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		raw, _ := io.ReadAll(req.Body)
		r.mu.Lock()
		r.seen = append(r.seen, seenRequest{req.URL.Path, req.URL.RawQuery, req.Header.Clone(), string(raw)})
		r.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	t.Cleanup(r.srv.Close)
	return r
}

func (r *recorder) only(t *testing.T) seenRequest {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.seen) != 1 {
		t.Fatalf("upstream saw %d requests, want 1: %+v", len(r.seen), r.seen)
	}
	return r.seen[0]
}

// send drives one request through the handler to the provider ref.
func send(t *testing.T, ref routing.ProviderRef, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	mux, key := keyedHandler(t, Options{AllowPrivateUpstreams: true,
		ProviderResolver: func(*runtime.RuntimeSnapshot, string) (routing.ProviderRef, bool) { return ref, true },
	})
	out := httptest.NewRecorder()
	bearer(mux, key).ServeHTTP(out, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
	return out
}

const (
	chatBody     = `{"model":"m","messages":[{"role":"user","content":"hi"}]}`
	messagesBody = `{"model":"m","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`
	respBody     = `{"model":"m","input":"hi"}`
)

func TestAzureRequestShape(t *testing.T) {
	up := newRecorder(t)
	ref := routing.ProviderRef{ProviderID: "azure", Protocol: openaiadapter.ChatProtocol, BaseURL: up.srv.URL + "/azure/", UpstreamModel: "gpt-4o", APIToken: "az-key",
		Connection: routing.ConnectionMetadata{Settings: routing.ProviderSettings{Deployment: "dep1", APIVersion: "2025-01-01", Organization: "org-1"}}}
	if out := send(t, ref, "/v1/chat/completions", chatBody); out.Code != 200 {
		t.Fatalf("status %d %s", out.Code, out.Body)
	}
	got := up.only(t)
	if got.Path != "/azure/openai/deployments/dep1/chat/completions" || got.Query != "api-version=2025-01-01" {
		t.Fatalf("url %s?%s", got.Path, got.Query)
	}
	if got.Header.Get("Api-Key") != "az-key" || got.Header.Get("Authorization") != "" || got.Header.Get("OpenAI-Organization") != "org-1" {
		t.Fatalf("headers %v", got.Header)
	}
}

func TestAzureWithoutAnEndpointOrAValidDeploymentSendsNothing(t *testing.T) {
	up := newRecorder(t)
	for name, ref := range map[string]routing.ProviderRef{
		"no endpoint":    {ProviderID: "azure", Protocol: openaiadapter.ChatProtocol, BaseURL: " ", UpstreamModel: "gpt-4o", APIToken: "k"},
		"bad deployment": {ProviderID: "azure", Protocol: openaiadapter.ChatProtocol, BaseURL: up.srv.URL, UpstreamModel: "gpt-4o", APIToken: "k", Connection: routing.ConnectionMetadata{Settings: routing.ProviderSettings{Deployment: "../x"}}},
	} {
		out := send(t, ref, "/v1/chat/completions", chatBody)
		if out.Code < 400 {
			t.Errorf("%s: status %d", name, out.Code)
		}
	}
	up.mu.Lock()
	defer up.mu.Unlock()
	if len(up.seen) != 0 {
		t.Fatalf("a misconfigured Azure connection reached an upstream: %+v", up.seen)
	}
}

func TestMessagesBetaQueryAndKimiDeviceHeaders(t *testing.T) {
	for _, id := range []string{"claude", "glm", "kimi", "minimax", "minimax-cn"} {
		t.Run(id, func(t *testing.T) {
			up := newRecorder(t)
			ref := routing.ProviderRef{ProviderID: id, Protocol: anthropicadapter.MessagesProtocol, BaseURL: up.srv.URL + "/api", Endpoint: "v1/messages", APIToken: "tok",
				Connection: routing.ConnectionMetadata{Settings: routing.ProviderSettings{DeviceID: "dev-1"}}}
			if out := send(t, ref, "/v1/messages", messagesBody); out.Code != 200 {
				t.Fatalf("status %d %s", out.Code, out.Body)
			}
			got := up.only(t)
			if got.Path != "/api/v1/messages" || got.Query != "beta=true" {
				t.Fatalf("url %s?%s", got.Path, got.Query)
			}
			if id == "kimi" && (got.Header.Get("X-Msh-Device-Id") != "dev-1" || got.Header.Get("X-Msh-Platform") != "routeweft") {
				t.Fatalf("kimi headers %v", got.Header)
			}
			if id != "kimi" && got.Header.Get("X-Msh-Device-Id") != "" {
				t.Fatalf("%s sent kimi headers", id)
			}
		})
	}
	// A provider that declares no suffix gets no query.
	up := newRecorder(t)
	ref := routing.ProviderRef{ProviderID: "deepseek", Protocol: anthropicadapter.MessagesProtocol, BaseURL: up.srv.URL, Endpoint: "anthropic/v1/messages", APIToken: "tok"}
	send(t, ref, "/v1/messages", messagesBody)
	if got := up.only(t); got.Query != "" {
		t.Fatalf("deepseek query %q", got.Query)
	}
}

func TestKimiChatAndResponsesCarryDeviceHeadersWithoutTheQuery(t *testing.T) {
	for _, c := range []struct{ protocol, path, endpoint, body string }{
		{openaiadapter.ChatProtocol, "/v1/chat/completions", "chat/completions", chatBody},
		{openaiadapter.ResponsesProtocol, "/v1/responses", "responses", respBody},
	} {
		up := newRecorder(t)
		ref := routing.ProviderRef{ProviderID: "kimi", Protocol: c.protocol, BaseURL: up.srv.URL, Endpoint: c.endpoint, APIToken: "tok"}
		send(t, ref, c.path, c.body)
		got := up.only(t)
		if got.Query != "" || got.Path != "/"+c.endpoint || got.Header.Get("Authorization") != "Bearer tok" || got.Header.Get("X-Msh-Device-Id") != "routeweft" {
			t.Errorf("%s: %s?%s %v", c.protocol, got.Path, got.Query, got.Header)
		}
	}
}

func TestTokenplanRoutesPerWire(t *testing.T) {
	up := newRecorder(t)
	base := up.srv.URL + "/v1"
	send(t, routing.ProviderRef{ProviderID: "xiaomi-tokenplan", Protocol: openaiadapter.ChatProtocol, BaseURL: base, APIToken: "tok"}, "/v1/chat/completions", chatBody)
	send(t, routing.ProviderRef{ProviderID: "xiaomi-tokenplan", Protocol: anthropicadapter.MessagesProtocol, BaseURL: base, APIToken: "tok"}, "/v1/messages", messagesBody)
	up.mu.Lock()
	defer up.mu.Unlock()
	if len(up.seen) != 2 || up.seen[0].Path != "/v1/chat/completions" || up.seen[1].Path != "/anthropic/v1/messages" {
		t.Fatalf("paths %+v", up.seen)
	}
}

func TestKilocodeOrganizationHeader(t *testing.T) {
	for org, want := range map[string]string{"org-7": "org-7", "": ""} {
		up := newRecorder(t)
		ref := routing.ProviderRef{ProviderID: "kilocode", Protocol: openaiadapter.ChatProtocol, BaseURL: up.srv.URL, Endpoint: "openrouter/chat/completions", APIToken: "tok",
			Connection: routing.ConnectionMetadata{Settings: routing.ProviderSettings{OrgID: org}}}
		send(t, ref, "/v1/chat/completions", chatBody)
		if got := up.only(t); got.Header.Get("X-Kilocode-Organizationid") != want || got.Header.Get("Authorization") != "Bearer tok" {
			t.Errorf("org %q: %v", org, got.Header)
		}
	}
}

func TestChatBodyQuirksReachTheUpstream(t *testing.T) {
	cases := []struct{ name, provider, body, want string }{
		{"github gpt-5", "github", `{"model":"gpt-5","max_tokens":9,"reasoning_effort":"none","messages":[{"role":"user","content":[{"type":"tool_result","content":"R"}]}]}`,
			`{"model":"gpt-5","max_completion_tokens":9,"messages":[{"role":"user","content":[{"type":"text","text":"R"}]}]}`},
		{"kimchi", "kimchi", `{"model":"kimi-k2.7","system":"S","top_k":1,"stop_sequences":["x"],"messages":[{"role":"user","content":"U","cache_control":{"type":"ephemeral"}}]}`,
			`{"model":"kimi-k2.7","messages":[{"role":"system","content":"S"},{"role":"user","content":"U"}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			up := newRecorder(t)
			send(t, routing.ProviderRef{ProviderID: c.provider, Protocol: openaiadapter.ChatProtocol, BaseURL: up.srv.URL, Endpoint: "chat/completions", APIToken: "tok"}, "/v1/chat/completions", c.body)
			got := up.only(t)
			var a, b any
			if json.Unmarshal([]byte(got.Body), &a) != nil || json.Unmarshal([]byte(c.want), &b) != nil || !jsonEqual(a, b) {
				t.Fatalf("upstream body\n got %s\nwant %s", got.Body, c.want)
			}
		})
	}
}

func jsonEqual(a, b any) bool {
	x, _ := json.Marshal(a)
	y, _ := json.Marshal(b)
	return string(x) == string(y)
}
