package ingress

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/providers/wire"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// Zed and MiMo Free served end to end through the dispatcher against scripted
// upstreams that speak the pin's wire (executors/zed.js, shared/zedAuth.js,
// executors/mimo-free.js). Sanitized shapes; nothing leaves the process.

const zedNDJSON = `{"status":"queued"}
{"event":{"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"glm-5.2","content":[],"usage":{"input_tokens":11,"output_tokens":0}}}}
{"event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}
{"event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"pong"}}}
{"event":{"type":"content_block_stop","index":0}}
{"event":{"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}}
{"event":{"type":"message_stop"}}
{"status":"stream_ended"}
`

type p192Call struct {
	path   string
	header http.Header
	body   map[string]any
}

// zedUpstream serves the exchange, the catalog and /completions under the
// account's base path and records what each was sent.
func zedUpstream(t *testing.T, completions string, record *[]p192Call, mu *sync.Mutex) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		*record = append(*record, p192Call{r.URL.Path, r.Header.Clone(), body})
		mu.Unlock()
		switch {
		case strings.HasSuffix(r.URL.Path, "/client/llm_tokens"):
			fmt.Fprint(w, `{"token":"llm-fixture"}`)
		case strings.HasSuffix(r.URL.Path, "/models"):
			fmt.Fprint(w, `{"models":[{"id":"glm-5.2","provider":"anthropic"}]}`)
		default:
			w.Header().Set("Content-Type", "application/x-ndjson")
			fmt.Fprint(w, completions)
		}
	}
}

func newZedIngressRig(t *testing.T, completions string) (*codebuddyRig, func(path string) []p192Call) {
	t.Helper()
	var calls []p192Call
	var mu sync.Mutex
	rig := newCodeBuddyRig(t, "zed", zedUpstream(t, completions, &calls, &mu))
	rig.settings = routing.ProviderSettings{UserID: "user-1", OrganizationID: "org-1"}
	rig.endpoint = "chat/completions" // the catalog's Chat endpoint; the Zed hook replaces it
	return rig, func(suffix string) []p192Call {
		mu.Lock()
		defer mu.Unlock()
		var out []p192Call
		for _, call := range calls {
			if strings.HasSuffix(call.path, suffix) {
				out = append(out, call)
			}
		}
		return out
	}
}

func TestZedIsServedThroughItsExchangeAndEnvelope(t *testing.T) {
	for _, tc := range []struct{ path, body string }{
		{"/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}]`},
		{"/v1/responses", `{"model":"x","input":"ping"`},
		{"/v1/messages", `{"model":"x","max_tokens":16,"messages":[{"role":"user","content":"ping"}]`},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", tc.path, stream), func(t *testing.T) {
				rig, seen := newZedIngressRig(t, zedNDJSON)
				out := rig.post(tc.path, tc.body+fmt.Sprintf(`,"stream":%v}`, stream))
				if out.Code != 200 || !strings.Contains(out.Body.String(), "pong") || (!stream && !json.Valid(out.Body.Bytes())) {
					t.Fatalf("%d %s", out.Code, out.Body)
				}
				if n := len(seen("/client/llm_tokens")); n != 1 {
					t.Fatalf("token exchanges %d", n)
				}
				sent := seen("/completions")
				if len(sent) != 1 || !strings.HasSuffix(sent[0].path, "/v2/acct/completions") {
					t.Fatalf("completions %+v", sent)
				}
				if got := sent[0].header.Get("Authorization"); got != "Bearer llm-fixture" {
					t.Errorf("bearer %q: the access token must not travel", got)
				}
				inner, _ := sent[0].body["provider_request"].(map[string]any)
				if sent[0].body["provider"] != "anthropic" || sent[0].body["model"] != "glm-5.2" || inner["stream"] != true || inner["messages"] == nil {
					t.Errorf("envelope %v", sent[0].body)
				}
				if len(rig.outcomes) != 1 || rig.outcomes[0].InputTokens != 11 || rig.outcomes[0].OutputTokens != 3 {
					t.Fatalf("usage %+v", rig.outcomes)
				}
			})
		}
	}
}

func TestZedWithoutItsUserIDIsRefusedBeforeAnythingIsSent(t *testing.T) {
	rig, seen := newZedIngressRig(t, zedNDJSON)
	rig.settings = routing.ProviderSettings{}
	out := rig.post("/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}]}`)
	if out.Code != 503 || !strings.Contains(out.Body.String(), "credentials_unavailable") {
		t.Fatalf("%d %s", out.Code, out.Body)
	}
	if len(seen("")) != 0 {
		t.Errorf("a request left the process: %+v", seen(""))
	}
}

func TestZedFailedStatusIsAnUpstreamErrorNotAnAnswer(t *testing.T) {
	rig, _ := newZedIngressRig(t, `{"status":{"failed":{"message":"quota exhausted"}}}`+"\n")
	out := rig.post("/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}]}`)
	if out.Code != 502 || strings.Contains(out.Body.String(), `"finish_reason"`) {
		t.Fatalf("%d %s", out.Code, out.Body)
	}
	// The failed account is cooled, so the streaming client gets a rig of its own.
	rig, _ = newZedIngressRig(t, `{"status":{"failed":{"message":"quota exhausted"}}}`+"\n")
	streamed := rig.post("/v1/chat/completions", `{"model":"x","stream":true,"messages":[{"role":"user","content":"ping"}]}`)
	if !strings.Contains(streamed.Body.String(), "quota exhausted") || strings.Contains(streamed.Body.String(), `"finish_reason":"stop"`) {
		t.Fatalf("%s", streamed.Body)
	}
}

// MiMo Free and its alias, end to end: the bootstrap JWT, the gateway's headers
// and the MiMoCode marker reach the chat endpoint; no account is involved.
func TestMimoFreeAndMmfAreServedWithABootstrappedJWT(t *testing.T) {
	for _, id := range []string{"mimo-free", "mmf"} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", id, stream), func(t *testing.T) {
				var calls []p192Call
				var mu sync.Mutex
				rig := newCodeBuddyRig(t, id, func(w http.ResponseWriter, r *http.Request) {
					raw, _ := io.ReadAll(r.Body)
					var body map[string]any
					_ = json.Unmarshal(raw, &body)
					mu.Lock()
					calls = append(calls, p192Call{r.URL.Path, r.Header.Clone(), body})
					mu.Unlock()
					if strings.HasSuffix(r.URL.Path, "/api/free-ai/bootstrap") {
						fmt.Fprint(w, `{"jwt":"eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0.sig"}`)
						return
					}
					w.Header().Set("Content-Type", "text/event-stream")
					fmt.Fprint(w, codebuddySSE)
				})
				rig.noToken, rig.endpoint = true, "api/free-ai/openai/chat"
				out := rig.post("/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}],"stream":`+fmt.Sprint(stream)+`}`)
				if out.Code != 200 || !strings.Contains(out.Body.String(), "pong") {
					t.Fatalf("%d %s", out.Code, out.Body)
				}
				var chat p192Call
				for _, call := range calls {
					if strings.HasSuffix(call.path, "/api/free-ai/openai/chat") {
						chat = call
					}
				}
				if chat.path == "" || chat.header.Get("Authorization") != "Bearer eyJhbGciOiJub25lIn0.eyJzdWIiOiJ4In0.sig" || chat.header.Get("X-Mimo-Source") != "mimocode-cli-free" {
					t.Fatalf("chat call %+v", chat)
				}
				messages, _ := chat.body["messages"].([]any)
				first, _ := messages[0].(map[string]any)
				if len(messages) != 2 || first["role"] != "system" || first["content"] != wire.MimoSystemMarker {
					t.Errorf("messages %v", messages)
				}
			})
		}
	}
}

func TestMimoFreeWithoutAJWTSendsNothingAndFails(t *testing.T) {
	var chatCalls int
	var mu sync.Mutex
	rig := newCodeBuddyRig(t, "mimo-free", func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/api/free-ai/bootstrap") {
			w.WriteHeader(500)
			return
		}
		mu.Lock()
		chatCalls++
		mu.Unlock()
	})
	rig.noToken, rig.endpoint = true, "api/free-ai/openai/chat"
	out := rig.post("/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}]}`)
	if out.Code < 500 || chatCalls != 0 {
		t.Fatalf("%d %s after %d chat calls", out.Code, out.Body, chatCalls)
	}
}
