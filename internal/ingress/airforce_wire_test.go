package ingress

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
)

func TestAirforceForcedStreamAcrossClientProtocols(t *testing.T) {
	for _, tc := range []struct{ path, body, terminal string }{
		{"/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}]`, "data: [DONE]"},
		{"/v1/responses", `{"model":"x","input":"ping"`, "event: response.completed"},
		{"/v1/messages", `{"model":"x","max_tokens":16,"messages":[{"role":"user","content":"ping"}]`, "event: message_stop"},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", tc.path, stream), func(t *testing.T) {
				rig := newCodeBuddyRig(t, "api-airforce", func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("HTTP-Referer") != "https://endpoint-proxy.local" || r.Header.Get("X-Title") != "Endpoint Proxy" || r.Header.Get("Authorization") != "Bearer secret" || r.Header.Get("Accept") != "text/event-stream" {
						t.Errorf("headers %v", r.Header)
					}
					sseUpstream(codebuddySSE)(w, r)
				})
				out := rig.post(tc.path, tc.body+fmt.Sprintf(`,"stream":%v}`, stream))
				if out.Code != 200 || !strings.Contains(out.Body.String(), "pong") {
					t.Fatalf("%d %s", out.Code, out.Body)
				}
				if stream && strings.Count(out.Body.String(), tc.terminal) != 1 {
					t.Fatalf("terminal: %s", out.Body)
				}
				if !stream && !json.Valid(out.Body.Bytes()) {
					t.Fatalf("non-JSON response: %s", out.Body)
				}
				rig.mu.Lock()
				defer rig.mu.Unlock()
				if len(rig.seen) != 1 || rig.seen[0]["stream"] != true || (!stream && rig.seen[0]["stream_options"] != nil) {
					t.Fatalf("outbound body %v", rig.seen)
				}
				if len(rig.outcomes) != 1 || rig.outcomes[0].InputTokens != 11 || rig.outcomes[0].OutputTokens != 3 || rig.outcomes[0].Stream != stream || rig.outcomes[0].UsageUnobserved {
					t.Fatalf("outcomes %+v", rig.outcomes)
				}
			})
		}
	}
}

func TestAirforceStreamFailureDoesNotReturnPartialJSON(t *testing.T) {
	rig := newCodeBuddyRig(t, "api-airforce", sseUpstream("data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n"))
	out := rig.post("/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}]}`)
	if out.Code != 502 || strings.Contains(out.Body.String(), "partial") {
		t.Fatalf("%d %s", out.Code, out.Body)
	}
}
