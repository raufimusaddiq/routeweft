package ingress

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	anthropicadapter "github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// P08: error shapes and "no capacity / no credits" per provider. The bodies are
// the providers' own non-OpenAI or provider-keyed shapes; the pin's rules are
// open-sse/config/errorConfig.js:62-80 (text rules first, then 401/402/403/404
// cool for two minutes, 429 backs off) and services/accountFallback.js:26-70.

type errorShape struct {
	provider, protocol string
	status             int
	body               string
	cools              bool          // the account is taken out of rotation
	atLeast, atMost    time.Duration // when it cools, for how long
}

func cooldownAfter(t *testing.T, tc errorShape) (time.Duration, bool) {
	t.Helper()
	state := runtime.NewState()
	handler := New(nil, Options{State: state})
	handler.recordUpstreamFailure(routing.ProviderRef{ProviderID: tc.provider, Protocol: tc.protocol, ConnectionID: "acct"}, tc.status, http.Header{}, []byte(tc.body))
	until, cooling := state.CooldownUntil("acct", time.Now())
	if !cooling {
		return 0, false
	}
	return time.Until(until), true
}

// Each provider's own failure body must take the account out of rotation the
// way the pin does, whatever the body's envelope (Anthropic `type:error`,
// Moonshot `rate_limit_reached_error`, Mistral's bare `message`, ...).
func TestProviderErrorBodiesCoolTheAccountLikeThePin(t *testing.T) {
	for _, tc := range []errorShape{
		{provider: "anthropic", protocol: anthropicadapter.MessagesProtocol, status: 529, body: `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`, cools: true, atLeast: 20 * time.Second, atMost: 6 * time.Minute},
		{provider: "anthropic", protocol: anthropicadapter.MessagesProtocol, status: 429, body: `{"type":"error","error":{"type":"rate_limit_error","message":"slow down"}}`, cools: true, atLeast: 20 * time.Second, atMost: 6 * time.Minute},
		{provider: "minimax", protocol: anthropicadapter.MessagesProtocol, status: 429, body: `{"type":"error","error":{"type":"rate_limit_error","message":"rate limit"}}`, cools: true, atLeast: 20 * time.Second, atMost: 6 * time.Minute},
		{provider: "kimi", protocol: openaiadapter.ChatProtocol, status: 429, body: `{"error":{"type":"rate_limit_reached_error","message":"too many requests"}}`, cools: true, atLeast: 20 * time.Second, atMost: 6 * time.Minute},
		{provider: "mistral", protocol: openaiadapter.ChatProtocol, status: 429, body: `{"message":"Requests rate limit exceeded"}`, cools: true, atLeast: 20 * time.Second, atMost: 6 * time.Minute},
		{provider: "groq", protocol: openaiadapter.ChatProtocol, status: 429, body: `{"error":{"message":"Rate limit reached for model","type":"tokens","code":"rate_limit_exceeded"}}`, cools: true, atLeast: 20 * time.Second, atMost: 6 * time.Minute},
		{provider: "deepseek", protocol: openaiadapter.ChatProtocol, status: 401, body: `{"error":{"message":"Authentication Fails","type":"authentication_error","code":"invalid_request_error"}}`, cools: true, atLeast: 110 * time.Second, atMost: 130 * time.Second},
		{provider: "zed", protocol: openaiadapter.ChatProtocol, status: 403, body: `{"code":"trial_blocked","message":"trial access is blocked"}`, cools: true, atLeast: 110 * time.Second, atMost: 130 * time.Second},
		{provider: "grok-cli", protocol: openaiadapter.ResponsesProtocol, status: 402, body: `{"code":"personal-team-blocked:spending-limit","error":"Spending limit reached"}`, cools: true, atLeast: 110 * time.Second, atMost: 130 * time.Second},
		{provider: "deepseek", protocol: openaiadapter.ChatProtocol, status: 400, body: `{"error":{"message":"This model's maximum context length is 65536 tokens","type":"invalid_request_error","code":"invalid_request_error"}}`, cools: false},
	} {
		got, cooling := cooldownAfter(t, tc)
		if cooling != tc.cools || cooling && (got < tc.atLeast || got > tc.atMost) {
			t.Errorf("%s %d %s: cooling=%v for %v, want cooling=%v within [%v,%v]", tc.provider, tc.status, tc.body, cooling, got.Round(time.Second), tc.cools, tc.atLeast, tc.atMost)
		}
	}
}

// The client always sees the provider's own status and body: classification
// only refines Routeweft's accounting (policy.go recordUpstreamFailure).
func TestProviderErrorBodiesReachTheClientByteForByte(t *testing.T) {
	for _, tc := range []errorShape{
		{provider: "anthropic", protocol: anthropicadapter.MessagesProtocol, status: 529, body: `{"type":"error","error":{"type":"overloaded_error","message":"Overloaded"}}`},
		{provider: "deepseek", protocol: openaiadapter.ChatProtocol, status: 402, body: `{"error":{"message":"Insufficient Balance","type":"unknown_error","param":null,"code":"invalid_request_error"}}`},
		{provider: "grok-cli", protocol: openaiadapter.ResponsesProtocol, status: 402, body: `{"code":"personal-team-blocked:spending-limit","error":"Spending limit reached"}`},
		{provider: "codex", protocol: openaiadapter.ResponsesProtocol, status: 429, body: `{"error":{"type":"usage_limit_reached","message":"The usage limit has been reached","resets_at":1893456000,"resets_in_seconds":3600}}`},
	} {
		path, request := "/v1/chat/completions", chatRequest
		switch tc.protocol {
		case anthropicadapter.MessagesProtocol:
			path, request = "/v1/messages", messagesRequest
		case openaiadapter.ResponsesProtocol:
			path, request = "/v1/responses", responsesRequest
		}
		t.Run(tc.provider, func(t *testing.T) {
			gateway, _ := providerGateway(t, tc.provider, tc.protocol, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			response := post(t, gateway, "", path, strings.Replace(request, "%s", "", 1))
			got, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if response.StatusCode != tc.status || string(got) != tc.body {
				t.Fatalf("client saw %d %q, want %d %q", response.StatusCode, got, tc.status, tc.body)
			}
		})
	}
}

// CLOSED P08 CLASSIFICATION GAPS, kept as a guard so that reopening one is a
// visible failure. The name is the one docs/P08_TEST_MATRIX.md cites.
func TestOpenP08ErrorClassificationGaps(t *testing.T) {
	t.Run("402 payment required cools the account and falls back", func(t *testing.T) {
		// Pin: errorConfig.js:77 { status: 402, cooldownMs: 2 min } with
		// accountFallback.js:35-46 shouldFallback:true, so an out-of-credit
		// DeepSeek / OpenRouter / Grok CLI account is skipped for two minutes and
		// the next account is tried. routing.ClassifyStatus now reads 402 as an
		// account-scoped failure (OutcomeFallbackAccount, two minutes) for every
		// provider; Grok CLI keeps its own rule (wire.grokCLIPaymentRequired).
		for _, tc := range []errorShape{
			{provider: "deepseek", protocol: openaiadapter.ChatProtocol, status: 402, body: `{"error":{"message":"Insufficient Balance","type":"unknown_error","param":null,"code":"invalid_request_error"}}`},
			{provider: "openrouter", protocol: openaiadapter.ChatProtocol, status: 402, body: `{"error":{"code":402,"message":"Insufficient credits"}}`},
			{provider: "openai", protocol: openaiadapter.ChatProtocol, status: 402, body: `{"error":{"message":"payment required"}}`},
		} {
			got, cooling := cooldownAfter(t, tc)
			if !cooling || got < 110*time.Second || got > 130*time.Second {
				t.Errorf("%s 402: cooling=%v for %v, want the pin's two-minute cooldown", tc.provider, cooling, got.Round(time.Second))
			}
		}
	})
	t.Run("402 honours a Retry-After the provider sends", func(t *testing.T) {
		state := runtime.NewState()
		handler := New(nil, Options{State: state})
		header := http.Header{"Retry-After": []string{"30"}}
		handler.recordUpstreamFailure(routing.ProviderRef{ProviderID: "openrouter", Protocol: openaiadapter.ChatProtocol, ConnectionID: "acct"}, 402, header, []byte(`{"error":{"message":"Insufficient credits"}}`))
		until, cooling := state.CooldownUntil("acct", time.Now())
		if d := time.Until(until); !cooling || d > 35*time.Second || d < 25*time.Second {
			t.Fatalf("cooling=%v for %v, want about 30s", cooling, d)
		}
	})
	// Codex usage_limit_reached honouring resets_at was a gap here; P02 closed it
	// (wire/error_reset.go, TestCodexBodyResetReachesDispatchCooldown).
}

// An out-of-credit account answers 402; the next account serves and the first is
// not tried again inside its cooldown (errorConfig.js:77, accountFallback.js:35-46).
func TestPaymentRequiredAccountFallsBackToTheNextAccount(t *testing.T) {
	for _, provider := range []string{"deepseek", "openrouter"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", provider, stream), func(t *testing.T) {
				var mu sync.Mutex
				attempts := map[string]int{}
				rig := newCodeBuddyRig(t, provider, func(w http.ResponseWriter, r *http.Request) {
					account := strings.Split(strings.TrimPrefix(r.URL.Path, "/v2/"), "/")[0]
					mu.Lock()
					attempts[account]++
					mu.Unlock()
					if account == "broke" {
						w.Header().Set("Content-Type", "application/json")
						w.WriteHeader(http.StatusPaymentRequired)
						_, _ = io.WriteString(w, `{"error":{"code":402,"message":"Insufficient credits"}}`)
						return
					}
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						_, _ = io.WriteString(w, codebuddySSE)
						return
					}
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, `{"id":"c1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"pong"},"finish_reason":"stop"}],"usage":{"prompt_tokens":1,"completion_tokens":1}}`)
				})
				rig.endpoint = "chat/completions"
				rig.accounts = []string{"broke", "good"}
				body := fmt.Sprintf(`{"model":"x","stream":%v,"messages":[{"role":"user","content":"ping"}]}`, stream)
				out := rig.post("/v1/chat/completions", body)
				if out.Code != 200 || !strings.Contains(out.Body.String(), "pong") {
					t.Fatalf("the next account did not serve: %d %s", out.Code, out.Body)
				}
				if until, cooling := rig.state.CooldownUntil("broke", time.Now()); !cooling || time.Until(until) < 100*time.Second {
					t.Errorf("the out-of-credit account was not cooled for two minutes: %v %v", cooling, time.Until(until))
				}
				if _, cooling := rig.state.CooldownUntil("good", time.Now()); cooling {
					t.Error("the serving account was cooled")
				}
				rig.post("/v1/chat/completions", body)
				mu.Lock()
				defer mu.Unlock()
				if attempts["broke"] != 1 || attempts["good"] != 2 {
					t.Errorf("attempts broke=%d good=%d, want 1 and 2", attempts["broke"], attempts["good"])
				}
			})
		}
	}
}
