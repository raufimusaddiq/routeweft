package ingress

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	anthropicadapter "github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// P08: cached versus ordinary token accounting for the providers whose native
// usage object is shaped differently from the OpenAI Chat default. Each case
// serves the provider's own usage shape through the real dispatch path, with
// the provider's real id (so its wire hook runs), and reads the accounting
// record the gateway publishes. The record carries the figures as reported:
// cache reads are a separate field, and an input count is never recomputed.
//
// Pin conventions the shapes come from (1c3b9f47):
//   - translator/concerns/usage.js:23 and response/claude-to-openai.js:36,134
//     for the Messages cache_read/cache_creation counts;
//   - translator/concerns/usage.js:46 for a top-level cached_tokens;
//   - translator/response/openai-responses.js:44,645 for Responses
//     input_tokens_details.cached_tokens, where input_tokens already includes it;
//   - DeepSeek's documented prompt_cache_hit_tokens, which the pin only reads in
//     shared/qoder/sse.js:34-37 (Routeweft reads it for every Chat provider).

// providerGateway serves one real provider id in front of upstream.
func providerGateway(t *testing.T, providerID, protocol string, upstream http.Handler) (string, *outcomeLog) {
	t.Helper()
	up := httptest.NewServer(upstream)
	t.Cleanup(up.Close)
	log := &outcomeLog{}
	manager := newManager(t)
	_, key, err := manager.CreateAPIKey(context.Background(), "p08-tokens")
	if err != nil {
		t.Fatal(err)
	}
	handler := New(manager, Options{
		AllowPrivateUpstreams: true,
		OnRequestComplete:     log.add,
		ProviderResolver: func(_ *runtime.RuntimeSnapshot, model string) (routing.ProviderRef, bool) {
			return routing.ProviderRef{ProviderID: providerID, UpstreamModel: model, Protocol: protocol, BaseURL: up.URL + "/v1", APIToken: "fixture-credential"}, true
		},
	})
	mux := http.NewServeMux()
	handler.Attach(mux)
	gateway := httptest.NewServer(bearer(mux, key))
	t.Cleanup(gateway.Close)
	return gateway.URL, log
}

const messagesRequest = `{"model":"fixture-model","max_tokens":16,"messages":[{"role":"user","content":"hi"}]%s}`

type tokenShape struct {
	provider, protocol, path, request string
	// body answers a non-stream request, stream a streamed one.
	body, stream                   string
	in, out, cacheRead, cacheWrite int64
}

var tokenShapes = []tokenShape{
	{
		provider: "anthropic", protocol: anthropicadapter.MessagesProtocol, path: "/v1/messages", request: messagesRequest,
		body:   `{"id":"m1","type":"message","role":"assistant","content":[],"usage":{"input_tokens":12,"output_tokens":7,"cache_read_input_tokens":300,"cache_creation_input_tokens":20}}`,
		stream: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":12,\"cache_read_input_tokens\":300,\"cache_creation_input_tokens\":20,\"output_tokens\":1}}}\n\n" + "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":7}}\n\n" + "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		in:     12, out: 7, cacheRead: 300, cacheWrite: 20,
	},
	{
		provider: "minimax", protocol: anthropicadapter.MessagesProtocol, path: "/v1/messages", request: messagesRequest,
		body:   `{"id":"m2","type":"message","role":"assistant","content":[],"usage":{"input_tokens":5,"output_tokens":9,"cache_read_input_tokens":40}}`,
		stream: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"usage\":{\"input_tokens\":5,\"cache_read_input_tokens\":40,\"output_tokens\":1}}}\n\n" + "event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":9}}\n\n" + "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
		in:     5, out: 9, cacheRead: 40,
	},
	{
		// DeepSeek reports hit and miss counts next to prompt_tokens, which is
		// the sum of both: the hit count is the cache read.
		provider: "deepseek", protocol: openaiadapter.ChatProtocol, path: "/v1/chat/completions", request: chatRequest,
		body:   `{"id":"c1","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":100,"completion_tokens":20,"prompt_cache_hit_tokens":60,"prompt_cache_miss_tokens":40}}`,
		stream: "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n" + "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":100,\"completion_tokens\":20,\"prompt_cache_hit_tokens\":60,\"prompt_cache_miss_tokens\":40}}\n\n" + "data: [DONE]\n\n",
		in:     100, out: 20, cacheRead: 60,
	},
	{
		// Moonshot carries cached_tokens at the top of usage, not under details.
		provider: "kimi", protocol: openaiadapter.ChatProtocol, path: "/v1/chat/completions", request: chatRequest,
		body:   `{"id":"c2","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":80,"completion_tokens":10,"cached_tokens":64}}`,
		stream: "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n" + "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":80,\"completion_tokens\":10,\"cached_tokens\":64}}\n\n" + "data: [DONE]\n\n",
		in:     80, out: 10, cacheRead: 64,
	},
	{
		provider: "mistral", protocol: openaiadapter.ChatProtocol, path: "/v1/chat/completions", request: chatRequest,
		body:   `{"id":"c3","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":50,"completion_tokens":5,"prompt_tokens_details":{"cached_tokens":32}}}`,
		stream: "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n" + "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":50,\"completion_tokens\":5,\"prompt_tokens_details\":{\"cached_tokens\":32}}}\n\n" + "data: [DONE]\n\n",
		in:     50, out: 5, cacheRead: 32,
	},
}

// Providers served over Responses report the cached count under
// input_tokens_details; input_tokens already includes it.
func init() {
	for _, id := range []string{"codex", "grok-cli", "muse", "xai"} {
		tokenShapes = append(tokenShapes, tokenShape{
			provider: id, protocol: openaiadapter.ResponsesProtocol, path: "/v1/responses", request: responsesRequest,
			body:   `{"id":"r1","object":"response","status":"completed","output":[],"usage":{"input_tokens":200,"output_tokens":50,"input_tokens_details":{"cached_tokens":128}}}`,
			stream: "event: response.created\ndata: {\"type\":\"response.created\",\"response\":{\"usage\":null}}\n\n" + "event: response.completed\ndata: {\"type\":\"response.completed\",\"response\":{\"status\":\"completed\",\"usage\":{\"input_tokens\":200,\"output_tokens\":50,\"input_tokens_details\":{\"cached_tokens\":128}}}}\n\n",
			in:     200, out: 50, cacheRead: 128,
		})
	}
}

func TestProviderCachedAndOrdinaryTokensAreSeparateInTheAccountingRecord(t *testing.T) {
	for _, tc := range tokenShapes {
		for _, stream := range []bool{false, true} {
			name := tc.provider + "/nonstream"
			if stream {
				name = tc.provider + "/stream"
			}
			t.Run(name, func(t *testing.T) {
				contentType, body, flag := "application/json", tc.body, ""
				if stream {
					contentType, body, flag = "text/event-stream", tc.stream, `,"stream":true`
				}
				var upstreamBody string
				gateway, log := providerGateway(t, tc.provider, tc.protocol, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					raw, _ := io.ReadAll(r.Body)
					upstreamBody = string(raw)
					w.Header().Set("Content-Type", contentType)
					_, _ = io.WriteString(w, body)
				}))
				response := post(t, gateway, "", tc.path, strings.Replace(tc.request, "%s", flag, 1))
				got, _ := io.ReadAll(response.Body)
				response.Body.Close()
				if response.StatusCode != http.StatusOK || string(got) != body {
					t.Fatalf("status %d: the native body was altered or refused: %q", response.StatusCode, got)
				}
				if upstreamBody == "" {
					t.Fatal("the request never reached the upstream")
				}
				outcomes := log.wait(t)
				if len(outcomes) != 1 {
					t.Fatalf("outcomes=%d want exactly 1", len(outcomes))
				}
				o := outcomes[0]
				if o.ProviderID != tc.provider || o.InputTokens != tc.in || o.OutputTokens != tc.out || o.CacheRead != tc.cacheRead || o.CacheWrite != tc.cacheWrite || o.UsageUnobserved || o.Termination != "" || o.Stream != stream {
					t.Fatalf("outcome %+v want provider=%s in=%d out=%d cacheRead=%d cacheWrite=%d observed stream=%v", o, tc.provider, tc.in, tc.out, tc.cacheRead, tc.cacheWrite, stream)
				}
			})
		}
	}
}
