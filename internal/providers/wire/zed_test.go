package wire

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

func TestZedThreadIDs(t *testing.T) {
	zedReset()
	provider := routing.ProviderRef{ConnectionID: "c1"}
	if got := zedThreadID(map[string]any{"thread_id": "t", "session_id": "s"}, provider); got != "t" {
		t.Errorf("thread_id %q", got)
	}
	if got := zedThreadID(map[string]any{"prompt_cache_key": "k"}, provider); got != "k" {
		t.Errorf("prompt_cache_key %q", got)
	}
	first, again := zedThreadID(map[string]any{}, provider), zedThreadID(map[string]any{}, provider)
	if first == "" || first != again {
		t.Errorf("a connection's thread is stable: %q %q", first, again)
	}
	if other := zedThreadID(map[string]any{}, routing.ProviderRef{ConnectionID: "c2"}); other == first {
		t.Error("two connections share a thread")
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

// A failed status is the end of the stream with an error the dispatcher and the
// client can see, not text the assistant said.
func TestZedFailedStatusIsAnInBandError(t *testing.T) {
	u := newZedUpstream(t)
	u.completions = streamOf(`{"event":{"type":"message_start","message":{"id":"m","role":"assistant","model":"claude-sonnet-4.5","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}}
{"status":{"failed":{"message":"quota exhausted"}}}
`)
	response, text := u.do(t, u.provider(), zedChatBody)
	if response.StatusCode != 200 || !strings.Contains(text, `"error"`) || !strings.Contains(text, "quota exhausted") || !strings.HasSuffix(strings.TrimSpace(text), "[DONE]") {
		t.Fatalf("%s", text)
	}
	if strings.Contains(text, `"finish_reason":"stop"`) {
		t.Errorf("a failure was reported as a clean stop: %s", text)
	}
}

// A stream that stops without stream_ended or [DONE] was cut off.
func TestZedTruncatedStreamIsAnError(t *testing.T) {
	for name, body := range map[string]string{
		"ends mid-message": `{"event":{"type":"message_start","message":{"id":"m","role":"assistant","model":"claude-sonnet-4.5","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}}` + "\n",
		"no terminal":      strings.Replace(zedAnthropicStream, `{"status":"stream_ended"}`+"\n", "", 1),
		"garbled line":     "not json\n" + zedAnthropicStream,
	} {
		t.Run(name, func(t *testing.T) {
			u := newZedUpstream(t)
			u.completions = streamOf(body)
			_, text := u.do(t, u.provider(), zedChatBody)
			if !strings.Contains(text, `"error"`) || !strings.Contains(text, "zed_stream_error") {
				t.Fatalf("a cut-off stream looked like an answer: %s", text)
			}
		})
	}
}

// The unwrapped stream also accepts the pin's other framings: SSE `data:` prefix,
// a bare vendor chunk, CRLF.
func TestZedAcceptsTheFramingsTheCodecDoes(t *testing.T) {
	u := newZedUpstream(t)
	u.completions = streamOf("data: " + `{"event":{"id":"x1","object":"chat.completion.chunk","created":5,"model":"grok-4","choices":[{"index":0,"delta":{"content":"pong"}}]}}` + "\r\n\r\n" +
		`{"id":"x1","object":"chat.completion.chunk","created":5,"model":"grok-4","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\r\n" +
		"data: [DONE]\r\n")
	_, text := u.do(t, u.provider(), `{"model":"grok-4","stream":true,"messages":[{"role":"user","content":"ping"}]}`)
	if !strings.Contains(text, "pong") || strings.Contains(text, `"error"`) {
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
