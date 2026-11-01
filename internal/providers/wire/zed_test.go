package wire

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// Sanitized fixtures of the shapes executors/zed.js and shared/zedAuth.js speak.
// No host, token or account here exists; the server is the test's own.

const (
	zedAccessToken = "access-token-fixture"
	zedLLMToken    = "llm-token-fixture"
)

type zedCall struct {
	Method, Path string
	Header       http.Header
	Body         string
}

// zedUpstream is a scripted cloud.zed.dev. A nil handler answers the default.
type zedUpstream struct {
	*httptest.Server
	mu    sync.Mutex
	calls []zedCall

	users, tokens, models, completions http.HandlerFunc
}

func newZedUpstream(t *testing.T) *zedUpstream {
	t.Helper()
	zedReset()
	t.Cleanup(zedReset)
	u := &zedUpstream{}
	u.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		u.mu.Lock()
		u.calls = append(u.calls, zedCall{r.Method, r.URL.Path, r.Header.Clone(), string(raw)})
		u.mu.Unlock()
		r.Body = io.NopCloser(strings.NewReader(string(raw)))
		route := map[string]*http.HandlerFunc{
			"/client/users/me":   &u.users,
			"/client/llm_tokens": &u.tokens,
			"/models":            &u.models,
			"/" + zedEndpoint:    &u.completions,
		}[r.URL.Path]
		if route == nil {
			http.NotFound(w, r)
			return
		}
		if *route != nil {
			(*route)(w, r)
			return
		}
		switch r.URL.Path {
		case "/client/users/me":
			fmt.Fprint(w, `{"default_organization_id":"org-default","organizations":[{"id":"org-personal","is_personal":true}]}`)
		case "/client/llm_tokens":
			fmt.Fprintf(w, `{"token":%q}`, zedLLMToken)
		case "/models":
			fmt.Fprint(w, `{"models":[{"id":"claude-sonnet-4.5","provider":"anthropic"},{"id":"gpt-5","provider":"open_ai"},{"id":"gemini-3","provider":"google"},{"id":"grok-4","provider":"x_ai"},{"id":["disabled-array-id"],"provider":"anthropic"}],"default_model":"claude-sonnet-4.5"}`)
		default:
			w.Header().Set("Content-Type", "application/x-ndjson")
			fmt.Fprint(w, zedAnthropicStream)
		}
	}))
	t.Cleanup(u.Close)
	return u
}

func (u *zedUpstream) seen(path string) []zedCall {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []zedCall
	for _, call := range u.calls {
		if call.Path == path {
			out = append(out, call)
		}
	}
	return out
}

func (u *zedUpstream) provider() routing.ProviderRef {
	return routing.ProviderRef{
		ProviderID: "zed", ConnectionID: "conn-1", BaseURL: u.URL, Protocol: chat, UpstreamModel: "claude-sonnet-4.5",
		APIToken: zedAccessToken,
		Connection: routing.ConnectionMetadata{Settings: routing.ProviderSettings{
			UserID: "user-1", SystemID: "system-1", OrganizationID: "org-1",
		}},
	}
}

// do sends body through ZedDo as the dispatcher would, to {base}/completions.
func (u *zedUpstream) do(t *testing.T, provider routing.ProviderRef, body string) (*http.Response, string) {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, u.URL+"/"+zedEndpoint, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	response, err := ZedDo(provider, u.Client(), request)
	if err != nil {
		t.Fatalf("ZedDo: %v", err)
	}
	defer response.Body.Close()
	text, _ := io.ReadAll(response.Body)
	return response, string(text)
}

const zedChatBody = `{"model":"claude-sonnet-4.5","stream":true,"thread_id":"thread-1","prompt_id":"prompt-1","messages":[{"role":"user","content":"ping"}]}`

const zedAnthropicStream = `{"status":"queued"}
{"event":{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4.5","content":[],"usage":{"input_tokens":11,"output_tokens":0}}}}
{"event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}
{"event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pong"}}}
{"event":{"type":"content_block_stop","index":0}}
{"event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}}
{"event":{"type":"message_stop"}}
{"status":"stream_ended"}
`

const zedGoogleStream = `{"event":{"candidates":[{"content":{"role":"model","parts":[{"text":"pong"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":3,"totalTokenCount":14}}}
{"status":{"stream_ended":{}}}
`

const zedResponsesStream = `{"event":{"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}}
{"event":{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[]}}}
{"event":{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"pong"}}
{"event":{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"pong"}]}],"usage":{"input_tokens":11,"output_tokens":3,"total_tokens":14}}}}
{"status":"stream_ended"}
`

const zedXAIStream = `{"event":{"id":"x1","object":"chat.completion.chunk","created":5,"model":"grok-4","choices":[{"index":0,"delta":{"role":"assistant","content":"pong"}}]}}
{"event":{"id":"x1","object":"chat.completion.chunk","created":5,"model":"grok-4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}}
[DONE]
`

func streamOf(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprint(w, body)
	}
}

func decodeJSON(t *testing.T, text string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(text), &out); err != nil {
		t.Fatalf("not JSON: %v: %s", err, text)
	}
	return out
}

// The whole exchange: the account's "<user> <token>" buys an LLM token, which is
// the only bearer /completions sees, and the request goes out in Zed's envelope.
func TestZedExchangesTheTokenAndSendsTheEnvelope(t *testing.T) {
	u := newZedUpstream(t)
	response, text := u.do(t, u.provider(), zedChatBody)
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("%d %s", response.StatusCode, response.Header)
	}
	if !strings.Contains(text, `"pong"`) || !strings.HasSuffix(strings.TrimSpace(text), "data: [DONE]") {
		t.Fatalf("not Chat SSE ending in [DONE]: %s", text)
	}

	exchange := u.seen("/client/llm_tokens")
	if len(exchange) != 1 {
		t.Fatalf("exchanges %d", len(exchange))
	}
	if got := exchange[0].Header.Get("Authorization"); got != "user-1 "+zedAccessToken {
		t.Errorf("exchange Authorization %q", got)
	}
	if got := exchange[0].Header.Get("x-zed-system-id"); got != "system-1" {
		t.Errorf("system id %q", got)
	}
	if got := decodeJSON(t, exchange[0].Body)["organization_id"]; got != "org-1" {
		t.Errorf("organization_id %v", got)
	}
	if len(u.seen("/client/users/me")) != 0 {
		t.Error("the connection names its organization; the account must not be read")
	}

	sent := u.seen("/" + zedEndpoint)
	if len(sent) != 1 {
		t.Fatalf("completions calls %d", len(sent))
	}
	headers := sent[0].Header
	if headers.Get("Authorization") != "Bearer "+zedLLMToken {
		t.Errorf("completions bearer %q", headers.Get("Authorization"))
	}
	for _, call := range u.calls {
		if strings.Contains(call.Header.Get("Authorization"), "Bearer "+zedAccessToken) {
			t.Errorf("the access token travelled as a bearer to %s", call.Path)
		}
	}
	for name, want := range map[string]string{
		"x-zed-version":                         zedVersion,
		"x-zed-client-supports-status-messages": "true",
		"x-zed-client-supports-stream-ended-request-completion-status": "true",
		"Content-Type": "application/json",
		"Accept":       zedAcceptCompletions,
	} {
		if got := headers.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	envelope := decodeJSON(t, sent[0].Body)
	if envelope["thread_id"] != "thread-1" || envelope["prompt_id"] != "prompt-1" || envelope["provider"] != "anthropic" || envelope["model"] != "claude-sonnet-4.5" {
		t.Errorf("envelope %v", envelope)
	}
	inner, _ := envelope["provider_request"].(map[string]any)
	if inner["stream"] != true || inner["model"] != "claude-sonnet-4.5" || inner["messages"] == nil {
		t.Errorf("provider_request is not a Claude Messages stream request: %v", inner)
	}
}

// Each vendor Zed fronts gets its own request shape and its own response leg; the
// client always gets Chat SSE with the usage the vendor reported.
func TestZedServesEveryVendorShape(t *testing.T) {
	for _, tc := range []struct {
		model, vendor, stream string
		inner                 func(map[string]any) bool
	}{
		{"claude-sonnet-4.5", "anthropic", zedAnthropicStream, func(m map[string]any) bool { return m["messages"] != nil && m["max_tokens"] != nil }},
		{"gpt-5", "open_ai", zedResponsesStream, func(m map[string]any) bool { return m["input"] != nil && m["messages"] == nil }},
		{"gemini-3", "google", zedGoogleStream, func(m map[string]any) bool {
			return m["contents"] != nil && m["safetySettings"] == nil && m["stream"] == nil
		}},
		{"grok-4", "x_ai", zedXAIStream, func(m map[string]any) bool {
			return m["messages"] != nil && m["stream"] == true && m["model"] == "grok-4"
		}},
	} {
		t.Run(tc.vendor, func(t *testing.T) {
			u := newZedUpstream(t)
			u.completions = streamOf(tc.stream)
			body := fmt.Sprintf(`{"model":%q,"stream":true,"messages":[{"role":"user","content":"ping"}]}`, tc.model)
			_, text := u.do(t, u.provider(), body)
			if !strings.Contains(text, `"pong"`) || !strings.Contains(text, `"usage"`) || !strings.Contains(text, "data: [DONE]") || strings.Contains(text, `"error"`) {
				t.Fatalf("%s", text)
			}
			sent := decodeJSON(t, u.seen("/" + zedEndpoint)[0].Body)
			if sent["provider"] != tc.vendor {
				t.Errorf("provider %v", sent["provider"])
			}
			if inner, _ := sent["provider_request"].(map[string]any); !tc.inner(inner) {
				t.Errorf("provider_request %v", inner)
			}
		})
	}
}

func TestZedGeminiDropsClientSafetySettings(t *testing.T) {
	u := newZedUpstream(t)
	u.completions = streamOf(zedGoogleStream)
	u.do(t, u.provider(), `{"model":"gemini-3","stream":true,"messages":[{"role":"user","content":"ping"}],"safety_settings":[{"category":"HARM_CATEGORY_HARASSMENT","threshold":"OFF"}]}`)
	inner, _ := decodeJSON(t, u.seen("/" + zedEndpoint)[0].Body)["provider_request"].(map[string]any)
	if _, has := inner["safetySettings"]; has {
		t.Errorf("safetySettings reached Zed's Gemini backend: %v", inner)
	}
}

// The token and the catalog are cached: a second request exchanges nothing.
func TestZedCachesTheLLMTokenAndTheCatalog(t *testing.T) {
	u := newZedUpstream(t)
	for i := 0; i < 3; i++ {
		u.do(t, u.provider(), zedChatBody)
	}
	if n := len(u.seen("/client/llm_tokens")); n != 1 {
		t.Errorf("token exchanges %d", n)
	}
	if n := len(u.seen("/models")); n != 1 {
		t.Errorf("catalog reads %d", n)
	}
	// Another account at the same host is another cache entry.
	other := u.provider()
	other.Connection.Settings.UserID = "user-2"
	u.do(t, other, zedChatBody)
	if n := len(u.seen("/client/llm_tokens")); n != 2 {
		t.Errorf("token exchanges after a second account %d", n)
	}
}

func TestZedTokenExpiresAfterFiftyMinutes(t *testing.T) {
	u := newZedUpstream(t)
	now := time.Now()
	zedState.now = func() time.Time { return now }
	u.do(t, u.provider(), zedChatBody)
	now = now.Add(zedTokenTTL + time.Second)
	u.do(t, u.provider(), zedChatBody)
	if n := len(u.seen("/client/llm_tokens")); n != 2 {
		t.Errorf("token exchanges %d", n)
	}
}

// With no organization on the connection the account names one (zedAuth.js:231-242).
func TestZedResolvesTheOrganizationFromTheAccount(t *testing.T) {
	for name, tc := range map[string]struct{ users, want string }{
		"default":  {`{"default_organization_id":"org-default","organizations":[{"id":"o2"}]}`, "org-default"},
		"personal": {`{"organizations":[{"id":"o1"},{"id":"o2","is_personal":true}]}`, "o2"},
		"first":    {`{"organizations":[{"id":"o1"},{"id":"o2"}]}`, "o1"},
		"array id": {`{"default_organization_id":["o-arr"]}`, "o-arr"},
	} {
		t.Run(name, func(t *testing.T) {
			u := newZedUpstream(t)
			u.users = func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, tc.users) }
			provider := u.provider()
			provider.Connection.Settings.OrganizationID = ""
			u.do(t, provider, zedChatBody)
			me := u.seen("/client/users/me")
			if len(me) == 0 || me[0].Header.Get("Authorization") != "user-1 "+zedAccessToken {
				t.Fatalf("users/me %v", me)
			}
			if got := decodeJSON(t, u.seen("/client/llm_tokens")[0].Body)["organization_id"]; got != tc.want {
				t.Errorf("organization_id %v, want %s", got, tc.want)
			}
		})
	}
}

// An expired token is replaced once, and only once (zedAuth.js:319-322).
func TestZedRetriesOnceWithAFreshTokenWhenTheOldOneExpired(t *testing.T) {
	for name, expire := range map[string]func(http.ResponseWriter){
		"401":      func(w http.ResponseWriter) { w.WriteHeader(401) },
		"expired":  func(w http.ResponseWriter) { w.Header().Set("x-zed-expired-token", "true"); w.WriteHeader(403) },
		"outdated": func(w http.ResponseWriter) { w.Header().Set("x-zed-outdated-token", "true"); w.WriteHeader(403) },
	} {
		t.Run(name, func(t *testing.T) {
			u := newZedUpstream(t)
			var mu sync.Mutex
			issued := 0
			u.tokens = func(w http.ResponseWriter, _ *http.Request) {
				mu.Lock()
				issued++
				n := issued
				mu.Unlock()
				fmt.Fprintf(w, `{"token":"llm-%d"}`, n)
			}
			u.completions = func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") == "Bearer llm-1" {
					expire(w)
					return
				}
				streamOf(zedAnthropicStream)(w, r)
			}
			response, text := u.do(t, u.provider(), zedChatBody)
			if response.StatusCode != 200 || !strings.Contains(text, "pong") {
				t.Fatalf("%d %s", response.StatusCode, text)
			}
			if issued != 2 {
				t.Errorf("tokens issued %d", issued)
			}
			if sent := u.seen("/" + zedEndpoint); len(sent) != 2 || sent[1].Header.Get("Authorization") != "Bearer llm-2" {
				t.Errorf("completions %v", sent)
			}
		})
	}
	// A second rejection is the answer.
	u := newZedUpstream(t)
	u.completions = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(401) }
	response, _ := u.do(t, u.provider(), zedChatBody)
	if response.StatusCode != 401 || len(u.seen("/"+zedEndpoint)) != 2 {
		t.Errorf("status %d after %d sends", response.StatusCode, len(u.seen("/"+zedEndpoint)))
	}
}

// The credential is a fail-closed input: nothing is sent without the user id the
// exchange needs, and no LLM token that was not returned is invented.
func TestZedRefusesWithoutACredentialItCanExchange(t *testing.T) {
	u := newZedUpstream(t)
	provider := u.provider()
	provider.Connection.Settings.UserID = ""
	if _, err := zedRefuseWithoutAccount(provider, nil, ""); err == nil {
		t.Fatal("the request hook let a connection without a user id through")
	} else {
		var credential *CredentialError
		if !errors.As(err, &credential) {
			t.Fatalf("%T", err)
		}
	}
	request, _ := http.NewRequest(http.MethodPost, u.URL+"/"+zedEndpoint, strings.NewReader(zedChatBody))
	if _, err := ZedDo(provider, u.Client(), request); err == nil {
		t.Fatal("ZedDo sent without a user id")
	}
	provider = u.provider()
	provider.APIToken = " "
	request, _ = http.NewRequest(http.MethodPost, u.URL+"/"+zedEndpoint, strings.NewReader(zedChatBody))
	if _, err := ZedDo(provider, u.Client(), request); err == nil {
		t.Fatal("ZedDo sent without an access token")
	}
	if len(u.calls) != 0 {
		t.Errorf("calls were made: %v", u.calls)
	}
}

func TestZedExchangeFailuresSendNothing(t *testing.T) {
	for name, tc := range map[string]struct {
		reply string
		code  int
	}{
		"no token":        {`{}`, 200},
		"empty token":     {`{"token":""}`, 200},
		"blank token":     {`{"token":"  "}`, 200},
		"spaced token":    {`{"token":"a b"}`, 200},
		"not JSON":        {`<html>`, 200},
		"null token list": {`{"token":[]}`, 200},
	} {
		t.Run(name, func(t *testing.T) {
			u := newZedUpstream(t)
			u.tokens = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(tc.code); fmt.Fprint(w, tc.reply) }
			request, _ := http.NewRequest(http.MethodPost, u.URL+"/"+zedEndpoint, strings.NewReader(zedChatBody))
			response, err := ZedDo(u.provider(), u.Client(), request)
			if err == nil {
				response.Body.Close()
				t.Fatal("a request was served without an LLM token")
			}
			if n := len(u.seen("/" + zedEndpoint)); n != 0 {
				t.Errorf("completions called %d times", n)
			}
		})
	}
	// A host that rejects the exchange has said something about the account.
	// That answer, not an error, reaches the dispatcher.
	u := newZedUpstream(t)
	u.tokens = func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
		fmt.Fprint(w, `{"code":"trial_blocked"}`)
	}
	response, text := u.do(t, u.provider(), zedChatBody)
	if response.StatusCode != 403 || !strings.Contains(text, "trial_blocked") || len(u.seen("/"+zedEndpoint)) != 0 {
		t.Errorf("%d %s", response.StatusCode, text)
	}
}

// A model the catalog does not list is looked up once on a fresh catalog; one it
// cannot read falls back to the model's name (zed.js:226-244).
func TestZedVendorResolution(t *testing.T) {
	u := newZedUpstream(t)
	var reads int
	u.models = func(w http.ResponseWriter, _ *http.Request) {
		reads++
		fmt.Fprint(w, `{"models":[{"id":"claude-sonnet-4.5","provider":"anthropic"}]}`)
	}
	u.completions = streamOf(zedXAIStream)
	u.do(t, u.provider(), `{"model":"grok-unlisted","stream":true,"messages":[{"role":"user","content":"ping"}]}`)
	if reads != 2 {
		t.Errorf("catalog reads %d, want a refresh for an unlisted model", reads)
	}
	if got := decodeJSON(t, u.seen("/" + zedEndpoint)[0].Body)["provider"]; got != "x_ai" {
		t.Errorf("an unlisted grok model is inferred as %v", got)
	}

	broken := newZedUpstream(t)
	broken.models = func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(500) }
	broken.completions = streamOf(zedAnthropicStream)
	broken.do(t, broken.provider(), zedChatBody)
	if got := decodeJSON(t, broken.seen("/" + zedEndpoint)[0].Body)["provider"]; got != "anthropic" {
		t.Errorf("an unreadable catalog must fall back to the name, got %v", got)
	}
}

func TestZedVendorNames(t *testing.T) {
	for _, tc := range []struct{ catalog, model, want string }{
		{"anthropic", "x", "anthropic"}, {"OpenAI", "x", "open_ai"}, {"open_ai", "x", "open_ai"},
		{"google", "x", "google"}, {"gemini", "x", "google"}, {"xai", "x", "x_ai"}, {"x-ai", "x", "x_ai"},
		{"", "claude-opus", "anthropic"}, {"", "gemini-3", "google"}, {"", "grok-4", "x_ai"}, {"", "xai-model", "x_ai"},
		{"", "gpt-5", "open_ai"}, {"unknown", "claude-x", "anthropic"}, {"unknown", "other", "open_ai"},
	} {
		if got := zedVendorOf(tc.catalog, tc.model); got != tc.want {
			t.Errorf("zedVendorOf(%q, %q) = %q, want %q", tc.catalog, tc.model, got, tc.want)
		}
	}
}

// thread_id is `body.thread_id || credentials._clientSessionId` (zed.js:251), and
// _clientSessionId is resolveSessionId over the client's headers and the body as
// the client sent it, scope "openai" (translator/index.js:96-99,
// utils/sessionManager.js:138-248). One row per rung of the pin's ladder.
func TestZedThreadIDFollowsThePinsSessionPrecedence(t *testing.T) {
	zedReset()
	t.Cleanup(zedReset)
	provider := routing.ProviderRef{ProviderID: "zed", ConnectionID: "conn-1"}
	session := func(headers map[string]string, body string) string {
		h := http.Header{}
		for k, v := range headers {
			h.Set(k, v)
		}
		return zedClientThread(provider, h, []byte(body))[zedThreadHeader]
	}
	const uuid = "0a1b2c3d-1111-2222-3333-444455556666"
	for _, tc := range []struct {
		name    string
		headers map[string]string
		body    string
		want    string
	}{
		{"claude user_id suffix", nil, `{"metadata":{"user_id":"user_abc_account_x_session_` + uuid + `"}}`, "claude:" + uuid},
		{"claude user_id json", nil, `{"metadata":{"user_id":"{\"session_id\":\"s-json\"}"}}`, "claude:s-json"},
		{"claude header", map[string]string{"x-claude-code-session-id": "s-hdr"}, `{}`, "claude:s-hdr"},
		{"claude user_id beats claude header", map[string]string{"x-claude-code-session-id": "s-hdr"}, `{"metadata":{"user_id":"u_session_` + uuid + `"}}`, "claude:" + uuid},
		{"claude beats x-session-id", map[string]string{"x-claude-code-session-id": "s-hdr", "x-session-id": "other"}, `{}`, "claude:s-hdr"},
		{"antigravity sessionId", map[string]string{"x-session-id": "other"}, `{"request":{"sessionId":"ag-1"}}`, "antigravity:ag-1"},
		{"antigravity numeric sessionId", nil, `{"request":{"sessionId":42}}`, "antigravity:42"},
		{"antigravity requestId", nil, `{"requestId":"agent/` + uuid + `/9"}`, "antigravity:" + uuid},
		{"x-session-id", map[string]string{"x-session-id": " h1 ", "session-id": "h2"}, `{"prompt_cache_key":"k"}`, "h1"},
		{"session-id", map[string]string{"session-id": "h2", "x-amp-thread-id": "h3"}, `{}`, "h2"},
		{"session_id header", map[string]string{"session_id": "h2b", "x-amp-thread-id": "h3"}, `{}`, "h2b"},
		{"x-amp-thread-id", map[string]string{"x-amp-thread-id": "h3", "x-opencode-session": "h4"}, `{}`, "h3"},
		{"x-opencode-session", map[string]string{"x-opencode-session": "h4", "x-client-request-id": "h5"}, `{}`, "h4"},
		{"x-client-request-id", map[string]string{"x-client-request-id": "h5"}, `{"prompt_cache_key":"k"}`, "h5"},
		{"prompt_cache_key", nil, `{"prompt_cache_key":"k","session_id":"s","conversation_id":"c"}`, "k"},
		{"session_id", nil, `{"session_id":"s","conversation_id":"c"}`, "s"},
		{"conversation_id", nil, `{"conversation_id":"c","metadata":{"user_id":"plain-user"}}`, "c"},
		{"plain metadata.user_id", nil, `{"metadata":{"user_id":"plain-user"}}`, "plain-user"},
		{"blank header falls through", map[string]string{"x-session-id": "   "}, `{"session_id":"s"}`, "s"},
		{"over-long header falls through", map[string]string{"x-session-id": strings.Repeat("a", 257)}, `{"session_id":"s"}`, "s"},
	} {
		if got := session(tc.headers, tc.body); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// Below the explicit ids: the first 50 characters of assistant text name the
// conversation within a scope and connection (sessionManager.js:160-198), then the
// connection's own session, each random, in memory, and idle for at most 2 hours.
func TestZedThreadIDFallsBackToAssistantTextThenTheConnection(t *testing.T) {
	zedReset()
	t.Cleanup(zedReset)
	now := time.Unix(1_000_000, 0)
	sessionNow = func() time.Time { return now }
	provider := routing.ProviderRef{ProviderID: "zed", ConnectionID: "conn-1"}
	resolve := func(p routing.ProviderRef, body string) string {
		return zedClientThread(p, nil, []byte(body))[zedThreadHeader]
	}
	prefix := strings.Repeat("assistant-prefix-", 4) // 68 characters
	withText := func(text string) string {
		return `{"messages":[{"role":"user","content":"q"},{"role":"assistant","content":` + fmt.Sprintf("%q", text) + `},{"role":"user","content":"more"}]}`
	}
	a := resolve(provider, withText(prefix))
	if a == "" || a != resolve(provider, withText(prefix+" and a later tail")) {
		t.Fatalf("the same opening assistant text must be the same session: %q", a)
	}
	// Responses-shaped input, and content parts, hash the same text.
	if got := resolve(provider, `{"input":[{"role":"assistant","content":[{"type":"output_text","text":"`+prefix+`"}]}]}`); got != a {
		t.Errorf("input/content parts: %q, want %q", got, a)
	}
	if b := resolve(provider, withText(strings.Repeat("different-start-", 5))); b == a {
		t.Error("different assistant text shares a session")
	}
	// Scoped to the connection.
	other := provider
	other.ConnectionID = "conn-2"
	if resolve(other, withText(prefix)) == a {
		t.Error("two connections share an assistant-text session")
	}
	// Under 50 characters of assistant text is not a conversation key: the
	// connection's session stands, and is stable.
	short := resolve(provider, withText("short"))
	if short == a || short != resolve(provider, `{"messages":[]}`) || short != resolve(provider, `{}`) {
		t.Errorf("connection fallback %q", short)
	}
	if resolve(other, `{}`) == short {
		t.Error("two connections share a session")
	}
	// A client with a session of its own never reaches the stores.
	if got := zedClientThread(provider, http.Header{"X-Session-Id": {"mine"}}, []byte(withText(prefix)))[zedThreadHeader]; got != "mine" {
		t.Errorf("explicit session %q", got)
	}
	// Lifetime: idle past two hours the sessions are new; used within, they last.
	now = now.Add(time.Hour)
	if resolve(provider, `{}`) != short {
		t.Error("a session used within the TTL changed")
	}
	now = now.Add(time.Hour + 31*time.Minute)
	if resolve(provider, `{}`) != short {
		t.Error("a session used an hour ago was swept")
	}
	now = now.Add(2*time.Hour + sessionSweepInterval + time.Minute)
	if got := resolve(provider, `{}`); got == short {
		t.Error("an idle session survived the TTL")
	}
	if got := resolve(provider, withText(prefix)); got == a {
		t.Error("an idle conversation survived the TTL")
	}
	// A process restart starts new sessions.
	before := resolve(provider, `{}`)
	zedReset()
	if resolve(provider, `{}`) == before {
		t.Error("sessions survived a restart")
	}
}

// The envelope's thread_id is the body's own when truthy, whatever its type, else
// the resolved session; prompt_id is whatever the body has, null included
// (zed.js:251-252, JSON.stringify keeps null and drops undefined).
func TestZedEnvelopeThreadAndPromptIDs(t *testing.T) {
	zedReset()
	t.Cleanup(zedReset)
	provider := routing.ProviderRef{ProviderID: "zed", ConnectionID: "conn-1"}
	envelope := func(body, resolved string) map[string]json.RawMessage {
		chat, _ := decodeObject(body)
		raw, _ := json.Marshal(zedEnvelope{ThreadID: zedThreadID(chat, resolved, provider), PromptID: zedPromptID(chat)})
		var out map[string]json.RawMessage
		_ = json.Unmarshal(raw, &out)
		return out
	}
	for name, tc := range map[string]struct{ body, resolved, thread, prompt string }{
		"body thread wins":     {`{"thread_id":"t","prompt_id":"p"}`, "resolved", `"t"`, `"p"`},
		"empty string falls":   {`{"thread_id":""}`, "resolved", `"resolved"`, ``},
		"null falls":           {`{"thread_id":null,"prompt_id":null}`, "resolved", `"resolved"`, `null`},
		"false falls":          {`{"thread_id":false}`, "resolved", `"resolved"`, ``},
		"zero falls":           {`{"thread_id":0}`, "resolved", `"resolved"`, ``},
		"number is truthy":     {`{"thread_id":7}`, "resolved", `7`, ``},
		"object is truthy":     {`{"thread_id":{}}`, "resolved", `{}`, ``},
		"empty prompt is kept": {`{"prompt_id":""}`, "resolved", `"resolved"`, `""`},
	} {
		got := envelope(tc.body, tc.resolved)
		if string(got["thread_id"]) != tc.thread || string(got["prompt_id"]) != tc.prompt {
			t.Errorf("%s: thread %s prompt %s", name, got["thread_id"], got["prompt_id"])
		}
	}
	// With no resolved session (a probe) the body it has decides, as for a client
	// that sent no headers.
	chat, _ := decodeObject(`{"session_id":"from-body"}`)
	if got := string(zedThreadID(chat, "", provider)); got != `"from-body"` {
		t.Errorf("unresolved: %s", got)
	}
}

func decodeObject(text string) (map[string]any, error) {
	decoder := json.NewDecoder(strings.NewReader(text))
	decoder.UseNumber()
	var out map[string]any
	return out, decoder.Decode(&out)
}

// ZedDo takes the session ClientRequestHeaders resolved, and never forwards the
// carrier header upstream.
func TestZedDoSendsTheResolvedSessionAndNotTheCarrier(t *testing.T) {
	u := newZedUpstream(t)
	request, _ := http.NewRequest(http.MethodPost, u.URL+"/"+zedEndpoint, strings.NewReader(`{"model":"claude-sonnet-4.5","stream":true,"messages":[{"role":"user","content":"ping"}]}`))
	request.Header.Set(zedThreadHeader, "claude:resolved-session")
	response, err := ZedDo(u.provider(), u.Client(), request)
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, response.Body)
	response.Body.Close()
	for _, call := range u.calls {
		if call.Header.Get(zedThreadHeader) != "" {
			t.Errorf("the carrier reached %s", call.Path)
		}
	}
	if got := decodeJSON(t, u.seen("/" + zedEndpoint)[0].Body)["thread_id"]; got != "claude:resolved-session" {
		t.Errorf("thread_id %v", got)
	}
}

func TestZedStatuses(t *testing.T) {
	var statuses = []struct {
		raw     string
		ended   bool
		failure string
	}{
		{`"stream_ended"`, true, ""},
		{`{"stream_ended":{}}`, true, ""},
		{`{"type":"stream_ended"}`, true, ""},
		{`"queued"`, false, ""},
		{`{"started":{"x":1}}`, false, ""},
		{`{"failed":{"message":"boom"}}`, false, "boom"},
		{`{"failed":{"error":"e"}}`, false, "e"},
		{`{"failed":{"code":"c"}}`, false, "c"},
		{`{"type":"failed","message":"m"}`, false, "m"},
		{`{"failed":{}}`, false, "request failed"},
	}
	for _, tc := range statuses {
		var raw any
		if err := json.Unmarshal([]byte(tc.raw), &raw); err != nil {
			t.Fatal(err)
		}
		ended, failure, failed := zedStatus(raw)
		if ended != tc.ended || failure != tc.failure || failed != (tc.failure != "") {
			t.Errorf("%s: ended=%v failure=%q failed=%v", tc.raw, ended, failure, failed)
		}
	}
}

// shape reduces Chat SSE to what the pin's observed output is compared on: the
// text and finish of each chunk, tool-call chunks, frames that are not objects, and
// the terminal. Role-only and usage-only chunks and ids are the legs' own.
func shape(t *testing.T, sse string) []string {
	t.Helper()
	var out []string
	for _, frame := range strings.Split(strings.TrimSpace(sse), "\n\n") {
		payload := strings.TrimPrefix(strings.TrimSpace(frame), "data: ")
		if payload == "[DONE]" {
			out = append(out, "DONE")
			continue
		}
		var chunk struct {
			Error   any `json:"error"`
			Choices []struct {
				Delta struct {
					Content   string `json:"content"`
					ToolCalls []any  `json:"tool_calls"`
				} `json:"delta"`
				Finish string `json:"finish_reason"`
			} `json:"choices"`
		}
		if json.Unmarshal([]byte(payload), &chunk) != nil || (chunk.Choices == nil && chunk.Error == nil) {
			out = append(out, "raw:"+payload)
			continue
		}
		if chunk.Error != nil {
			out = append(out, "error")
			continue
		}
		for _, choice := range chunk.Choices {
			var parts []string
			if choice.Delta.Content != "" {
				parts = append(parts, "text:"+choice.Delta.Content)
			}
			if len(choice.Delta.ToolCalls) > 0 {
				parts = append(parts, "tool")
			}
			if choice.Finish != "" {
				parts = append(parts, "finish:"+choice.Finish)
			}
			if len(parts) > 0 {
				out = append(out, strings.Join(parts, " "))
			}
		}
	}
	return out
}

const (
	zedAnthropicHead = `{"event":{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"claude-sonnet-4.5","content":[],"usage":{"input_tokens":11,"output_tokens":0}}}}
{"event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}
{"event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pong"}}}
`
	zedAnthropicTail = `{"event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}}
{"event":{"type":"message_stop"}}
{"status":"stream_ended"}
`
	zedGoogleHead = `{"event":{"candidates":[{"content":{"role":"model","parts":[{"text":"pong"}]}}],"usageMetadata":{"promptTokenCount":11,"candidatesTokenCount":3,"totalTokenCount":14}}}
`
	zedGoogleTail = `{"event":{"candidates":[{"content":{"role":"model","parts":[{"text":"."}]},"finishReason":"STOP"}]}}
{"status":"stream_ended"}
`
	zedResponsesHead = `{"event":{"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}}
{"event":{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_1","role":"assistant","content":[]}}}
{"event":{"type":"response.output_text.delta","item_id":"msg_1","output_index":0,"content_index":0,"delta":"pong"}}
`
	zedResponsesTool = `{"event":{"type":"response.created","response":{"id":"resp_1","status":"in_progress"}}}
{"event":{"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"call_1","name":"lookup","arguments":""}}}
{"event":{"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"q\":1}"}}
`
	zedResponsesDone = `{"event":{"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[],"usage":{"input_tokens":11,"output_tokens":3,"total_tokens":14}}}}
{"status":"stream_ended"}
`
	zedXAIChunk = `{"event":{"id":"x1","object":"chat.completion.chunk","created":5,"model":"grok-4","choices":[{"index":0,"delta":{"content":"pong"}}]}}
`
)

// The end of the stream, case by case, against what the pin's own wrapper
// (executors/zed.js:148-219) wrote for the same input when it was run offline on
// these sanitized lines: a failed status is the assistant's text with a clean stop,
// a body that stops short is finished (the Responses translator closes a started
// turn, the others send nothing), a line that is not JSON is skipped, and [DONE]
// comes last, once.
func TestZedStreamEndsAsThePinEndsIt(t *testing.T) {
	failedMessage := func(message string) string {
		return fmt.Sprintf(`{"status":{"failed":{"message":%q}}}`+"\n", message)
	}
	for _, tc := range []struct {
		name, model, stream string
		want                []string
	}{
		{"anthropic failed", "claude-sonnet-4.5", zedAnthropicHead + failedMessage("quota exhausted"),
			[]string{"text:pong", "text:[Zed error] quota exhausted finish:stop", "DONE"}},
		{"anthropic cut off", "claude-sonnet-4.5", zedAnthropicHead,
			[]string{"text:pong", "DONE"}},
		{"anthropic ended without a terminal event", "claude-sonnet-4.5", zedAnthropicHead + `{"status":"stream_ended"}` + "\n",
			[]string{"text:pong", "DONE"}},
		{"anthropic non-JSON line skipped", "claude-sonnet-4.5", zedAnthropicHead + "not json\n" +
			`{"event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"!"}}}` + "\n" + zedAnthropicTail,
			[]string{"text:pong", "text:!", "finish:stop", "DONE"}},
		{"google failed", "gemini-3", zedGoogleHead + failedMessage("boom"),
			[]string{"text:pong", "text:[Zed error] boom finish:stop", "DONE"}},
		{"google cut off", "gemini-3", zedGoogleHead,
			[]string{"text:pong", "DONE"}},
		{"google non-JSON line skipped", "gemini-3", "oops\n" + zedGoogleHead + zedGoogleTail,
			[]string{"text:pong", "text:.", "finish:stop", "DONE"}},
		{"responses cut off closes the started turn", "gpt-5", zedResponsesHead,
			[]string{"text:pong", "finish:stop", "DONE"}},
		{"responses failed: error text, then the flush", "gpt-5", zedResponsesHead + failedMessage("boom"),
			[]string{"text:pong", "text:[Zed error] boom finish:stop", "finish:stop", "DONE"}},
		{"responses failed as a bare string", "gpt-5", zedResponsesHead + `{"status":"failed"}` + "\n",
			[]string{"text:pong", "text:[Zed error] request failed finish:stop", "finish:stop", "DONE"}},
		{"responses tool call cut off finishes as tool_calls", "gpt-5", zedResponsesTool,
			[]string{"tool", "tool", "finish:tool_calls", "DONE"}},
		{"responses already finished is not finished twice", "gpt-5", zedResponsesTool + zedResponsesDone,
			[]string{"tool", "tool", "finish:tool_calls", "DONE"}},
		{"responses that sent nothing has nothing to flush", "gpt-5", "",
			[]string{"DONE"}},
		{"responses non-JSON line skipped", "gpt-5", "x\n" + zedResponsesHead + zedResponsesDone,
			[]string{"text:pong", "finish:stop", "DONE"}},
		{"xai cut off", "grok-4", zedXAIChunk,
			[]string{"text:pong", "DONE"}},
		{"xai failed", "grok-4", zedXAIChunk + failedMessage("boom"),
			[]string{"text:pong", "text:[Zed error] boom finish:stop", "DONE"}},
		{"xai non-JSON line skipped, [DONE] ends", "grok-4", "junk\n" + zedXAIChunk + "data: [DONE]\n" + zedXAIChunk,
			[]string{"text:pong", "DONE"}},
		// The pin relays whatever JSON an xAI line holds (enqueueSseObject): a
		// scalar as it is, an array item by item, falsy items dropped.
		{"xai non-object JSON is relayed as the pin does", "grok-4", zedXAIChunk + "123\n[1,0,null,\"\"]\n\"s\"\n0\n[DONE]\n",
			[]string{"text:pong", "raw:123", "raw:1", `raw:"s"`, "DONE"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := newZedUpstream(t)
			u.completions = streamOf(tc.stream)
			body := fmt.Sprintf(`{"model":%q,"stream":true,"messages":[{"role":"user","content":"ping"}]}`, tc.model)
			response, text := u.do(t, u.provider(), body)
			if response.StatusCode != 200 {
				t.Fatalf("%d %s", response.StatusCode, text)
			}
			if got := shape(t, text); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("got  %q\nwant %q\n%s", got, tc.want, text)
			}
			if strings.Count(text, "[DONE]") != 1 || strings.Contains(text, "upstream_incomplete") || strings.Contains(text, "zed_stream_error") {
				t.Errorf("one [DONE] and no invented error expected:\n%s", text)
			}
		})
	}
}

// The error chunk is createErrorChunk (zed.js:97-107): its own id, the model the
// request named, and the message as the assistant's content.
func TestZedErrorChunkNamesTheRequestedModel(t *testing.T) {
	u := newZedUpstream(t)
	u.completions = streamOf(zedAnthropicHead + `{"status":{"failed":{"error":"nope"}}}` + "\n")
	_, text := u.do(t, u.provider(), zedChatBody)
	var found bool
	for _, frame := range strings.Split(text, "\n\n") {
		if !strings.Contains(frame, "[Zed error]") {
			continue
		}
		found = true
		var chunk struct {
			ID, Object, Model string
			Choices           []struct {
				Delta  struct{ Content string }
				Finish string `json:"finish_reason"`
			}
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(frame, "data: ")), &chunk); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(chunk.ID, "chatcmpl-zed-error-") || chunk.Object != "chat.completion.chunk" || chunk.Model != "claude-sonnet-4.5" ||
			chunk.Choices[0].Delta.Content != "[Zed error] nope" || chunk.Choices[0].Finish != "stop" {
			t.Errorf("%+v", chunk)
		}
	}
	if !found {
		t.Fatalf("no error chunk: %s", text)
	}
}

// Whatever the pin's translators did with the lines before the failure reaches the
// client first, in order, and nothing the legs wrote about the early end does.
func TestZedFailureKeepsEarlierContentInOrder(t *testing.T) {
	u := newZedUpstream(t)
	u.completions = streamOf(zedAnthropicHead + `{"event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":" more"}}}` + "\n" + `{"status":{"failed":{"message":"x"}}}` + "\n" + zedAnthropicTail)
	_, text := u.do(t, u.provider(), zedChatBody)
	want := []string{"text:pong", "text: more", "text:[Zed error] x finish:stop", "DONE"}
	if got := shape(t, text); !reflect.DeepEqual(got, want) {
		t.Errorf("got %q want %q", got, want)
	}
}

// The reader takes the pin's framings: an SSE `data:` prefix, a bare vendor
// chunk, CRLF, and lines after [DONE] are not read.
func TestZedAcceptsTheFramingsTheCodecDoes(t *testing.T) {
	u := newZedUpstream(t)
	u.completions = streamOf("data: " + `{"event":{"id":"x1","object":"chat.completion.chunk","created":5,"model":"grok-4","choices":[{"index":0,"delta":{"content":"pong"}}]}}` + "\r\n\r\n" +
		`{"id":"x1","object":"chat.completion.chunk","created":5,"model":"grok-4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\r\n" +
		"data:[DONE]\r\n" + zedXAIChunk)
	_, text := u.do(t, u.provider(), `{"model":"grok-4","stream":true,"messages":[{"role":"user","content":"ping"}]}`)
	if want := []string{"text:pong", "finish:stop", "DONE"}; !reflect.DeepEqual(shape(t, text), want) {
		t.Fatalf("%q\n%s", shape(t, text), text)
	}
}

// What is not a stream end stays an error: the client leaving is handled in
// TestZedStopsUpstreamWhenTheClientLeaves, and an oversized line is refused
// where the pin has no bound.
func TestZedOversizedLineIsAnErrorNotAFinish(t *testing.T) {
	u := newZedUpstream(t)
	u.completions = streamOf(zedXAIChunk + strings.Repeat("a", 5<<20) + "\n")
	_, text := u.do(t, u.provider(), `{"model":"grok-4","stream":true,"messages":[{"role":"user","content":"ping"}]}`)
	if !strings.Contains(text, "zed_stream_error") || strings.Count(text, "[DONE]") != 1 {
		t.Fatalf("%s", text)
	}
}

// An upstream refusal reaches the dispatcher as it is, not as a stream.
func TestZedPassesUpstreamErrorsThrough(t *testing.T) {
	u := newZedUpstream(t)
	u.completions = func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(403)
		fmt.Fprint(w, `{"code":"trial_blocked","message":"trial access is blocked"}`)
	}
	response, text := u.do(t, u.provider(), zedChatBody)
	if response.StatusCode != 403 || !strings.Contains(text, "trial_blocked") {
		t.Errorf("%d %s", response.StatusCode, text)
	}
}

func TestZedRefusesARequestItsWireCannotCarry(t *testing.T) {
	u := newZedUpstream(t)
	for name, body := range map[string]string{
		"not an object": `[]`,
		"no model":      `{"messages":[]}`,
	} {
		response, text := u.do(t, u.provider(), body)
		if response.StatusCode != 400 || !strings.Contains(text, "invalid_request_error") {
			t.Errorf("%s: %d %s", name, response.StatusCode, text)
		}
	}
	if len(u.calls) != 0 {
		t.Errorf("calls %v", u.calls)
	}
}

// The upstream request ends with the client's.
func TestZedStopsUpstreamWhenTheClientLeaves(t *testing.T) {
	u := newZedUpstream(t)
	released := make(chan struct{})
	u.completions = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprint(w, `{"event":{"type":"message_start","message":{"id":"m","role":"assistant","model":"x","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}}`+"\n")
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		close(released)
	}
	ctx, cancel := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, u.URL+"/"+zedEndpoint, strings.NewReader(zedChatBody))
	response, err := ZedDo(u.provider(), u.Client(), request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	cancel()
	select {
	case <-released:
	case <-time.After(5 * time.Second):
		t.Fatal("the upstream request outlived the client")
	}
}

// A failed status ahead of any output is found before the reply is handed on, and
// the client text is untouched.
func TestZedHeadFailureIsMarkedAndTheTextIsStillThePins(t *testing.T) {
	for name, tc := range map[string]struct {
		stream string
		marked bool
	}{
		"failed first":       {`{"status":{"failed":{"message":"quota exhausted"}}}` + "\n", true},
		"queued then failed": {`{"status":"queued"}` + "\n" + "keep-alive\n" + `{"status":{"failed":{"message":"quota exhausted"}}}` + "\n", true},
		"as a bare string":   {`{"status":"failed"}` + "\n", true},
		"output first":       {zedAnthropicHead + `{"status":{"failed":{"message":"late"}}}` + "\n", false},
		"ended first":        {`{"status":"stream_ended"}` + "\n" + `{"status":{"failed":{"message":"ignored"}}}` + "\n", false},
		"done first":         {"[DONE]\n" + `{"status":{"failed":{"message":"ignored"}}}` + "\n", false},
		"nothing at all":     {"", false},
	} {
		t.Run(name, func(t *testing.T) {
			u := newZedUpstream(t)
			u.completions = streamOf(tc.stream)
			request, _ := http.NewRequest(http.MethodPost, u.URL+"/"+zedEndpoint, strings.NewReader(zedChatBody))
			response, err := ZedDo(u.provider(), u.Client(), request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			text, _ := io.ReadAll(response.Body)
			failure, marked := zedAcceptedFailure(response)
			if marked != tc.marked {
				t.Fatalf("marked=%v %+v", marked, failure)
			}
			if response.Header.Get(zedFailureHeader) != "" {
				t.Error("the marker survived the hook")
			}
			if tc.marked {
				if got := shape(t, string(text)); len(got) == 0 || !strings.HasPrefix(got[len(got)-2], "text:[Zed error] ") || got[len(got)-1] != "DONE" {
					t.Errorf("client text is not the pin's: %q\n%s", got, text)
				}
			}
		})
	}
}

func TestZedAcceptedFailureClassification(t *testing.T) {
	for message, want := range map[string]routing.Outcome{
		"monthly quota exhausted": routing.OutcomeQuotaLock,
		"Billing issue":           routing.OutcomeQuotaLock,
		"rate limited":            routing.OutcomeQuotaLock,
		"Rate_Limit_Exceeded":     routing.OutcomeQuotaLock,
		"too many requests":       routing.OutcomeQuotaLock,
		"monthly limit reached":   routing.OutcomeQuotaLock,
		// Transient errors that happen to contain the letters of a quota word.
		"failed to generate a response": routing.OutcomeFallbackAccount,
		"temperature must be moderate":  routing.OutcomeFallbackAccount,
		"unlimited plan, upstream busy": routing.OutcomeFallbackAccount,
		"deliberate failure":            routing.OutcomeFallbackAccount,
		"credits depleted":              routing.OutcomeQuotaLock,
		"unauthorized":                  routing.OutcomeAuthRefreshRequired,
		"Authentication failed":         routing.OutcomeAuthRefreshRequired,
		"upstream went away":            routing.OutcomeFallbackAccount,
		"request failed":                routing.OutcomeFallbackAccount,
	} {
		response := &http.Response{Header: http.Header{}}
		response.Header.Set(zedFailureHeader, base64.StdEncoding.EncodeToString([]byte(message)))
		got, ok := zedAcceptedFailure(response)
		if !ok || got.Outcome != want || !got.AllowsFallback() {
			t.Errorf("%q: %+v ok=%v, want %s", message, got, ok, want)
		}
	}
	if _, ok := zedAcceptedFailure(&http.Response{Header: http.Header{}}); ok {
		t.Error("an unmarked response is a failure")
	}
	if _, ok := zedAcceptedFailure(&http.Response{Header: http.Header{zedFailureHeader: {"!!not base64"}}}); ok {
		t.Error("a garbled marker is a failure")
	}
}

// The exchange and the catalog sit under the same path prefix as /completions.
func TestZedFollowsAPathPrefixedBase(t *testing.T) {
	u := newZedUpstream(t)
	prefixed := httptest.NewServer(http.StripPrefix("/zedproxy", u.Server.Config.Handler))
	defer prefixed.Close()
	provider := u.provider()
	provider.BaseURL = prefixed.URL + "/zedproxy"
	request, _ := http.NewRequest(http.MethodPost, prefixed.URL+"/zedproxy/"+zedEndpoint, strings.NewReader(zedChatBody))
	response, err := ZedDo(provider, prefixed.Client(), request)
	if err != nil {
		t.Fatal(err)
	}
	text, _ := io.ReadAll(response.Body)
	response.Body.Close()
	if !strings.Contains(string(text), "pong") || len(u.seen("/client/llm_tokens")) != 1 || len(u.seen("/models")) != 1 || len(u.seen("/"+zedEndpoint)) != 1 {
		t.Fatalf("calls %v\n%s", u.calls, text)
	}
}

// A body that breaks is not a body that ended. A connection reset mid-stream, or a
// line past the bound, on a vendor leg must reach the client as an error and never
// as a clean truncated answer (the pin's stream errors when its upstream read does).
func TestZedBrokenUpstreamIsAnErrorOnEveryLeg(t *testing.T) {
	broken := map[string]func(http.ResponseWriter, string){
		"connection reset": func(w http.ResponseWriter, head string) {
			w.Header().Set("Content-Type", "application/x-ndjson")
			fmt.Fprint(w, head)
			w.(http.Flusher).Flush()
			panic(http.ErrAbortHandler)
		},
		"oversized line": func(w http.ResponseWriter, head string) {
			w.Header().Set("Content-Type", "application/x-ndjson")
			fmt.Fprint(w, head)
			fmt.Fprint(w, strings.Repeat("a", 5<<20)+"\n")
		},
	}
	for _, leg := range []struct{ model, head string }{
		{"claude-sonnet-4.5", zedAnthropicHead},
		{"gemini-3", zedGoogleHead},
		{"gpt-5", zedResponsesHead},
		{"grok-4", zedXAIChunk},
	} {
		for name, fail := range broken {
			t.Run(leg.model+"/"+name, func(t *testing.T) {
				u := newZedUpstream(t)
				u.completions = func(w http.ResponseWriter, _ *http.Request) { fail(w, leg.head) }
				body := fmt.Sprintf(`{"model":%q,"stream":true,"messages":[{"role":"user","content":"ping"}]}`, leg.model)
				_, text := u.do(t, u.provider(), body)
				got := shape(t, text)
				if len(got) < 2 || got[len(got)-2] != "error" || got[len(got)-1] != "DONE" {
					t.Fatalf("a broken upstream did not end in an error: %q\n%s", got, text)
				}
				if strings.Contains(text, `"finish_reason":"stop"`) {
					t.Errorf("a broken upstream finished cleanly:\n%s", text)
				}
				if strings.Contains(text, "upstream_incomplete") || strings.Count(text, `"error"`) != 1 || strings.Count(text, "[DONE]") != 1 {
					t.Errorf("one error and one [DONE] expected:\n%s", text)
				}
			})
		}
	}
}

// Exchange is set for the providers whose probe must take the credential exchange,
// and for no provider whose Do only changes how a request is sent.
func TestOnlyCredentialExchangesAreProbedThroughDo(t *testing.T) {
	var got []string
	for _, id := range Registered() {
		if For(id).Exchange {
			got = append(got, id)
		}
		if For(id).Exchange && For(id).Do == nil {
			t.Errorf("%s declares an exchange and has no Do", id)
		}
	}
	sort.Strings(got)
	if want := []string{"mimo-free", "mmf", "qoder", "vertex", "vertex-partner", "zed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("exchange hooks %v, want %v", got, want)
	}
	for _, id := range []string{"cursor", "codex", "commandcode", "grok-cli"} {
		if Exchanges(id) || For(id).Do == nil {
			t.Errorf("%s: exchanges=%v do=%v", id, Exchanges(id), For(id).Do != nil)
		}
	}
}

// The pin looks an unlisted model up on a fresh catalog every time it is asked for
// one (zed.js:231-237; zedAuth.js:366-369, forceRefresh skips both the cache and
// the in-flight share), and so does this: the refresh is not rate-limited. A listed
// model is served from the cache.
func TestZedRefreshesTheCatalogForAnUnlistedModelAsThePinDoes(t *testing.T) {
	u := newZedUpstream(t)
	reads := 0
	u.models = func(w http.ResponseWriter, _ *http.Request) {
		reads++
		fmt.Fprint(w, `{"models":[{"id":"claude-sonnet-4.5","provider":"anthropic"}]}`)
	}
	u.completions = streamOf(zedXAIStream)
	unlisted := `{"model":"grok-unlisted","stream":true,"messages":[{"role":"user","content":"ping"}]}`
	u.do(t, u.provider(), unlisted)
	if reads != 2 {
		t.Fatalf("first request read the catalog %d times, want a load and a refresh", reads)
	}
	u.do(t, u.provider(), unlisted)
	if reads != 3 {
		t.Errorf("second request: %d reads, want one refresh more (the pin's)", reads)
	}
	u.do(t, u.provider(), zedChatBody)
	if reads != 3 {
		t.Errorf("a listed model re-read the catalog: %d", reads)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// The *http.Response client.Do returns is the transport's: its goroutines still read
// it. ZedDo hands back a copy and leaves that one exactly as it received it, on the
// served path, the failed-head path and the upstream-error path (deterministic
// stand-in for a race the detector only sometimes catches).
func TestZedDoNeverModifiesTheTransportsResponse(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		body   string
	}{
		"served":         {200, zedAnthropicStream},
		"failed head":    {200, `{"status":{"failed":{"message":"quota exhausted"}}}` + "\n"},
		"upstream error": {403, `{"code":"trial_blocked"}`},
	} {
		t.Run(name, func(t *testing.T) {
			zedReset()
			t.Cleanup(zedReset)
			var canned *http.Response
			var original struct {
				header        http.Header
				contentLength int64
				body          io.ReadCloser
			}
			reply := func(status int, contentType, text string) *http.Response {
				return &http.Response{StatusCode: status, Status: fmt.Sprint(status), Proto: "HTTP/1.1", ProtoMajor: 1, ProtoMinor: 1,
					Header:        http.Header{"Content-Type": {contentType}, "Content-Length": {fmt.Sprint(len(text))}, "Content-Encoding": {"identity"}},
					ContentLength: int64(len(text)), Body: io.NopCloser(strings.NewReader(text))}
			}
			client := &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
				switch r.URL.Path {
				case "/client/llm_tokens":
					return reply(200, "application/json", `{"token":"llm-fixture"}`), nil
				case "/models":
					return reply(200, "application/json", `{"models":[{"id":"claude-sonnet-4.5","provider":"anthropic"}]}`), nil
				}
				canned = reply(tc.status, "application/x-ndjson", tc.body)
				original.header, original.contentLength, original.body = canned.Header.Clone(), canned.ContentLength, canned.Body
				return canned, nil
			})}
			provider := routing.ProviderRef{ProviderID: "zed", ConnectionID: "c", BaseURL: "https://zed.example.test", Protocol: chat, APIToken: zedAccessToken,
				Connection: routing.ConnectionMetadata{Settings: routing.ProviderSettings{UserID: "user-1", OrganizationID: "org-1"}}}
			request, _ := http.NewRequest(http.MethodPost, "https://zed.example.test/"+zedEndpoint, strings.NewReader(zedChatBody))
			got, err := ZedDo(provider, client, request)
			if err != nil {
				t.Fatal(err)
			}
			io.Copy(io.Discard, got.Body)
			got.Body.Close()
			if got == canned {
				t.Fatal("ZedDo returned the transport's own response")
			}
			if canned.ContentLength != original.contentLength || canned.Uncompressed || canned.Body != original.body || !reflect.DeepEqual(canned.Header, original.header) {
				t.Errorf("the transport's response was modified: length %d (was %d), uncompressed %v, header %v (was %v)",
					canned.ContentLength, original.contentLength, canned.Uncompressed, canned.Header, original.header)
			}
			if tc.status == 200 && (got.ContentLength != -1 || !got.Uncompressed || got.Header.Get("Content-Type") != "text/event-stream") {
				t.Errorf("the copy was not reshaped: %d %v %v", got.ContentLength, got.Uncompressed, got.Header)
			}
			if tc.status != 200 && got.StatusCode != tc.status {
				t.Errorf("status %d", got.StatusCode)
			}
		})
	}
}

// How long a failed status keeps the account out. The pin's text rules come first
// and have no authentication wording (a rate limit is a rate limit however else the
// message reads); an exhausted quota stays locked past the 30 s default, and a rate
// limit does not.
func TestZedAcceptedFailureLockDurations(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	for _, tc := range []struct {
		message string
		outcome routing.Outcome
		lock    time.Duration
	}{
		{"monthly quota exhausted", routing.OutcomeQuotaLock, zedQuotaLock},
		{"billing problem on the account", routing.OutcomeQuotaLock, zedQuotaLock},
		{"usage limit reached", routing.OutcomeQuotaLock, zedQuotaLock},
		{"rate limit exceeded", routing.OutcomeQuotaLock, routing.DefaultFallbackBackoff},
		{"too many requests", routing.OutcomeQuotaLock, routing.DefaultFallbackBackoff},
		// Both: the pin's text rule for a rate limit matches first, and it has no
		// rule for authentication wording.
		{"rate limit reached, token invalid", routing.OutcomeQuotaLock, routing.DefaultFallbackBackoff},
		{"token invalid, rate limit reached", routing.OutcomeQuotaLock, routing.DefaultFallbackBackoff},
		{"quota exhausted: invalid token", routing.OutcomeQuotaLock, zedQuotaLock},
		{"no credentials", routing.OutcomeAuthRefreshRequired, 2 * time.Minute},
		{"invalid token", routing.OutcomeAuthRefreshRequired, 2 * time.Minute},
		{"upstream went away", routing.OutcomeFallbackAccount, routing.DefaultFallbackBackoff},
	} {
		response := &http.Response{Header: http.Header{zedFailureHeader: {base64.StdEncoding.EncodeToString([]byte(tc.message))}}}
		got, ok := zedAcceptedFailure(response)
		if !ok || got.Outcome != tc.outcome || got.Message != tc.message {
			t.Errorf("%q: %+v ok=%v", tc.message, got, ok)
			continue
		}
		if deadline := routing.CooldownDeadline(got.Classification, now, routing.DefaultFallbackBackoff); deadline.Sub(now) != tc.lock {
			t.Errorf("%q locks for %v, want %v", tc.message, deadline.Sub(now), tc.lock)
		}
	}
	// A reset the provider reports would still win over the lock.
	reset := now.Add(time.Hour)
	answer := zedClassify("quota exhausted")
	answer.ResetAt = reset.UTC().Format(time.RFC3339)
	if got := routing.CooldownDeadline(answer, now, routing.DefaultFallbackBackoff); !got.Equal(reset) {
		t.Errorf("a trusted reset did not win: %v", got)
	}
}

// A status object that holds both keys is one reading however many times it is
// read: the failure.
func TestZedStatusWithBothKeysIsAlwaysTheFailure(t *testing.T) {
	var status any
	if err := json.Unmarshal([]byte(`{"stream_ended":{},"failed":{"message":"boom"}}`), &status); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 500; i++ {
		ended, message, failed := zedStatus(status)
		if ended || !failed || message != "boom" {
			t.Fatalf("read %d: ended=%v failed=%v %q", i, ended, failed, message)
		}
	}
	var typed any
	_ = json.Unmarshal([]byte(`{"type":"stream_ended","failed":{"message":"x"}}`), &typed)
	if ended, _, failed := zedStatus(typed); ended || !failed {
		t.Errorf("a failed key beside a stream_ended type: ended=%v failed=%v", ended, failed)
	}
}

// zed.js:284-305: a trial_blocked body is rewritten with the pin's sentence and
// the upstream message appended, not shown as-is.
func TestZedTrialBlockedBodyIsRewritten(t *testing.T) {
	u := newZedUpstream(t)
	u.completions = func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		fmt.Fprint(w, `{"code":"trial_blocked","message":"trial expired"}`)
	}
	response, text := u.do(t, u.provider(), zedChatBody)
	if response.StatusCode != http.StatusForbidden {
		t.Fatalf("status %d", response.StatusCode)
	}
	var document map[string]any
	if err := json.Unmarshal([]byte(text), &document); err != nil {
		t.Fatalf("body %q: %v", text, err)
	}
	want := "Zed trial access is blocked upstream. The account can list hosted models, but Zed is refusing completions until trial/billing access is enabled or unblocked. Zed says: trial expired"
	if document["message"] != want {
		t.Fatalf("message = %q", document["message"])
	}
}

func TestZedErrorBodyBoundIsNamed(t *testing.T) {
	body, err := zedReadErrorBody(strings.NewReader(strings.Repeat("x", zedErrorLimit+1)))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "exceeds") {
		t.Fatalf("oversized body was not named: %s", body)
	}
}

func TestZedTrialBlockedRewriteShapes(t *testing.T) {
	const sentence = "Zed trial access is blocked upstream. The account can list hosted models, but Zed is refusing completions until trial/billing access is enabled or unblocked. Zed says: "
	nested, _ := zedRewriteTrialBlocked([]byte(`{"error":{"code":"trial_blocked","message":"nested"}}`), "")
	var document map[string]any
	if json.Unmarshal(nested, &document) != nil {
		t.Fatal("nested body is not JSON")
	}
	if got := document["error"].(map[string]any)["message"]; got != sentence+"nested" {
		t.Fatalf("nested message = %q", got)
	}
	if got, _ := zedRewriteTrialBlocked([]byte(`{"code":"other","message":"keep"}`), ""); string(got) != `{"code":"other","message":"keep"}` {
		t.Fatalf("non-trial body changed: %s", got)
	}
	if got, _ := zedRewriteTrialBlocked([]byte("plain text"), ""); string(got) != "plain text" {
		t.Fatalf("non-JSON body changed: %s", got)
	}
}
