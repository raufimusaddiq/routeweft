package ingress

import (
	"context"
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
