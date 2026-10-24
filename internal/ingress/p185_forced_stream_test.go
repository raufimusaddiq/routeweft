package ingress

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestZedForcedStreamAcrossClientProtocols(t *testing.T) {
	for _, tc := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}]`},
		{"/v1/responses", `{"model":"x","input":"ping"`},
		{"/v1/messages", `{"model":"x","max_tokens":16,"messages":[{"role":"user","content":"ping"}]`},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", tc.path, stream), func(t *testing.T) {
				rig := newCodeBuddyRig(t, "zed", sseUpstream(codebuddySSE))
				out := rig.post(tc.path, tc.body+fmt.Sprintf(`,"stream":%v}`, stream))
				if out.Code != 200 || !strings.Contains(out.Body.String(), "pong") || (!stream && !json.Valid(out.Body.Bytes())) {
					t.Fatalf("%d %s", out.Code, out.Body)
				}
				if len(rig.seen) != 1 || rig.seen[0]["stream"] != true {
					t.Fatalf("outbound %v", rig.seen)
				}
				if len(rig.outcomes) != 1 || rig.outcomes[0].InputTokens != 11 || rig.outcomes[0].OutputTokens != 3 {
					t.Fatalf("usage %+v", rig.outcomes)
				}
			})
		}
	}
}

func TestOpenCodeGoForcedStreamAcrossClientProtocols(t *testing.T) {
	for _, tc := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}]`},
		{"/v1/responses", `{"model":"x","input":"ping"`},
		{"/v1/messages", `{"model":"x","max_tokens":16,"messages":[{"role":"user","content":"ping"}]`},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", tc.path, stream), func(t *testing.T) {
				rig := newResponsesRig(t, "opencode-go", "gpt-5.6-luna", []string{"acct"}, serveStream(codexStream))
				out := rig.post(tc.path, tc.body+fmt.Sprintf(`,"stream":%v}`, stream))
				if out.Code != 200 || !strings.Contains(out.Body.String(), "pong") || (!stream && !json.Valid(out.Body.Bytes())) {
					t.Fatalf("%d %s", out.Code, out.Body)
				}
				if len(rig.calls) != 1 || rig.calls[0].body["stream"] != true || rig.calls[0].path != "/acct/responses" {
					t.Fatalf("outbound %v", rig.calls)
				}
				if len(rig.outcomes) != 1 || rig.outcomes[0].InputTokens != 11 || rig.outcomes[0].OutputTokens != 3 || rig.outcomes[0].CacheRead != 8 {
					t.Fatalf("usage %+v", rig.outcomes)
				}
			})
		}
	}
}

func TestNewForcedStreamsRejectTruncatedReplies(t *testing.T) {
	zed := newCodeBuddyRig(t, "zed", sseUpstream("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))
	if out := zed.post("/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}]}`); out.Code != 502 {
		t.Fatalf("%d %s", out.Code, out.Body)
	}
	goRig := newResponsesRig(t, "opencode-go", "gpt-5.6-luna", []string{"acct"}, serveStream(frame("response.created", `{"type":"response.created","response":{"id":"r1"}}`)))
	if out := goRig.post("/v1/responses", `{"model":"x","input":"ping"}`); out.Code != 502 {
		t.Fatalf("%d %s", out.Code, out.Body)
	}
}

func TestCodexClientSessionHeadersDoNotOverrideTheCacheSession(t *testing.T) {
	for _, key := range []string{"", "explicit-thread"} {
		rig := newResponsesRig(t, "codex", "gpt-5", []string{"acct"}, serveStream(codexStream))
		body := `{"model":"x","input":[{"role":"assistant","content":"` + strings.Repeat("assistant-prefix", 5) + `"},{"role":"user","content":"ping"}]}`
		if key != "" {
			body = strings.TrimSuffix(body, "}") + `,"prompt_cache_key":"explicit-thread"}`
		}
		out := rig.post("/v1/responses", body, "x-session-id", "untrusted", "session-id", "untrusted", "session_id", "untrusted", "x-amp-thread-id", "untrusted", "x-opencode-session", "untrusted", "x-client-request-id", "untrusted", "x-claude-code-session-id", "untrusted")
		if out.Code != 200 {
			t.Fatalf("%d %s", out.Code, out.Body)
		}
		call := rig.calls[0]
		session := call.header.Get("session_id")
		if session != call.body["prompt_cache_key"] || (key == "" && session != connectionKey("acct")) || (key != "" && session != key) {
			t.Fatalf("session %q body %v", session, call.body)
		}
		for _, h := range []string{"x-session-id", "session-id", "x-amp-thread-id", "x-opencode-session", "x-client-request-id", "x-claude-code-session-id"} {
			if call.header.Get(h) != "" {
				t.Fatalf("forwarded %s", h)
			}
		}
	}
}

func TestNewForcedStreamsCancelUpstream(t *testing.T) {
	for _, provider := range []string{"zed", "opencode-go"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", provider, stream), func(t *testing.T) {
				started, stopped := make(chan struct{}), make(chan struct{})
				upstream := func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "text/event-stream")
					w.WriteHeader(200)
					w.(http.Flusher).Flush()
					close(started)
					<-r.Context().Done()
					close(stopped)
				}
				var mux *http.ServeMux
				var key string
				path, body := "/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}]`
				if provider == "zed" {
					rig := newCodeBuddyRig(t, provider, upstream)
					mux, key = rig.mux, rig.key
				} else {
					rig := newResponsesRig(t, provider, "gpt-5.6-luna", []string{"acct"}, func(_ string, w http.ResponseWriter, r *http.Request) { upstream(w, r) })
					mux, key = rig.mux, rig.key
					path, body = "/v1/responses", `{"model":"x","input":"ping"`
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body+fmt.Sprintf(`,"stream":%v}`, stream))).WithContext(ctx)
				r.Header.Set("Authorization", "Bearer "+key)
				done := make(chan struct{})
				go func() { defer close(done); mux.ServeHTTP(httptest.NewRecorder(), r) }()
				select {
				case <-started:
				case <-time.After(5 * time.Second):
					t.Fatal("upstream did not start")
				}
				cancel()
				for _, ch := range []chan struct{}{stopped, done} {
					select {
					case <-ch:
					case <-time.After(5 * time.Second):
						t.Fatal("cancellation did not stop upstream and relay")
					}
				}
			})
		}
	}
}

func TestOpenCodeGoSessionHeaderAndBodyOnTheWire(t *testing.T) {
	const body = `{"model":"x","stream":false,"max_tokens":64,"reasoning_effort":"high","temperature":0.2,"previous_response_id":"resp_1","prompt_cache_key":"thread-1",` +
		`"input":[{"type":"reasoning","id":"rs_1","encrypted_content":"opaque"},{"role":"system","content":"s"},{"role":"user","content":"ping"}]}`
	run := func(t *testing.T, model string, headers ...string) upstreamCall {
		t.Helper()
		rig := newResponsesRig(t, "opencode-go", model, []string{"acct"}, serveStream(codexStream))
		if out := rig.post("/v1/responses", body, headers...); out.Code != 200 {
			t.Fatalf("%d %s", out.Code, out.Body)
		}
		return rig.calls[0]
	}

	luna := run(t, "gpt-5.6-luna", "x-session-id", "untrusted", "User-Agent", "codex-tui/1")
	if got := luna.header.Get("x-opencode-session"); !strings.HasPrefix(got, "ses_") || len(got) != 36 || strings.Contains(got, "untrusted") {
		t.Fatalf("derived session %q", got)
	}
	if luna.header.Get("x-session-id") != "" {
		t.Fatal("client session header forwarded")
	}
	for _, gone := range []string{"temperature", "previous_response_id", "max_tokens", "max_output_tokens", "reasoning_effort"} {
		if _, has := luna.body[gone]; has {
			t.Fatalf("%s sent: %v", gone, luna.body)
		}
	}
	input := luna.body["input"].([]any)
	if luna.body["stream"] != true || luna.body["store"] != false || luna.body["prompt_cache_key"] != "thread-1" ||
		luna.body["reasoning"].(map[string]any)["effort"] != "high" || len(input) != 2 || input[0].(map[string]any)["role"] != "developer" {
		t.Fatalf("body %v", luna.body)
	}

	grok := run(t, "grok-4.6", "x-opencode-session", "ses_client")
	if grok.header.Get("x-opencode-session") != "ses_client" {
		t.Fatalf("native session %q", grok.header.Get("x-opencode-session"))
	}
	if grok.body["temperature"] != 0.2 || grok.body["max_output_tokens"] != 64.0 || grok.body["previous_response_id"] != nil {
		t.Fatalf("grok body %v", grok.body)
	}

	// The same conversation gets the same session on every request, a different client another.
	again := run(t, "gpt-5.6-luna", "x-session-id", "untrusted", "User-Agent", "codex-tui/1")
	other := run(t, "gpt-5.6-luna", "x-session-id", "untrusted", "User-Agent", "claude-cli/2")
	if again.header.Get("x-opencode-session") != luna.header.Get("x-opencode-session") || other.header.Get("x-opencode-session") == luna.header.Get("x-opencode-session") {
		t.Fatalf("stability: %q %q %q", luna.header.Get("x-opencode-session"), again.header.Get("x-opencode-session"), other.header.Get("x-opencode-session"))
	}
}

// The pin resolves the session from the body the client sent, before translation
// (handlers/chatCore.js:69-74). A conversion leg drops metadata.user_id; the
// session must still come from it.
func TestOpenCodeGoSessionIsResolvedBeforeConversionDropsIt(t *testing.T) {
	digest := func(tool, session string) string {
		sum := sha256.Sum256([]byte("opencode-go\x00" + tool + "\x00" + session))
		return "ses_" + hex.EncodeToString(sum[:])[:32]
	}
	for _, tc := range []struct{ name, path, body, want string }{
		{"messages to responses", "/v1/messages", `{"model":"x","max_tokens":16,"metadata":{"user_id":"user_x_session_ab12-cd"},"messages":[{"role":"user","content":"ping"}]}`, digest("claude", "claude:ab12-cd")},
		{"chat to responses", "/v1/chat/completions", `{"model":"x","metadata":{"user_id":"user_x_session_ab12-cd"},"messages":[{"role":"user","content":"ping"}]}`, digest("claude", "claude:ab12-cd")},
		{"chat plain user_id", "/v1/chat/completions", `{"model":"x","metadata":{"user_id":"plain-user"},"messages":[{"role":"user","content":"ping"}]}`, digest("claude", "plain-user")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rig := newResponsesRig(t, "opencode-go", "gpt-5.6-luna", []string{"acct"}, serveStream(codexStream))
			if out := rig.post(tc.path, tc.body, "User-Agent", "claude-cli/2"); out.Code != 200 {
				t.Fatalf("%d %s", out.Code, out.Body)
			}
			call := rig.calls[0]
			if _, has := call.body["metadata"]; has {
				t.Fatalf("metadata reached Luna: %v", call.body)
			}
			if got := call.header.Get("x-opencode-session"); got != tc.want {
				t.Fatalf("session %q want %q", got, tc.want)
			}
		})
	}
}
