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

// The pin writes a failed status as the assistant's text with a clean stop
// (zed.js:97-107,173-179), so every client gets a completion carrying it: that is
// the observed behavior, kept (PARITY 6.19).
func TestZedFailedStatusIsTheAssistantsTextWithACleanStop(t *testing.T) {
	const failed = `{"event":{"type":"message_start","message":{"id":"m","role":"assistant","model":"glm-5.2","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}}` + "\n" +
		`{"status":{"failed":{"message":"quota exhausted"}}}` + "\n"
	for _, tc := range []struct{ path, body, want string }{
		{"/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}]}`, `"content":"[Zed error] quota exhausted"`},
		{"/v1/responses", `{"model":"x","input":"ping"}`, `[Zed error] quota exhausted`},
		{"/v1/messages", `{"model":"x","max_tokens":16,"messages":[{"role":"user","content":"ping"}]}`, `[Zed error] quota exhausted`},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/%v", tc.path, stream), func(t *testing.T) {
				rig, _ := newZedIngressRig(t, failed)
				out := rig.post(tc.path, strings.TrimSuffix(tc.body, "}")+fmt.Sprintf(`,"stream":%v}`, stream))
				if out.Code != 200 || !strings.Contains(out.Body.String(), tc.want) || strings.Contains(out.Body.String(), "upstream_stream_error") {
					t.Fatalf("%d %s", out.Code, out.Body)
				}
				if !stream && !json.Valid(out.Body.Bytes()) {
					t.Fatalf("not one JSON body: %s", out.Body)
				}
			})
		}
	}
}

// A stream that stops before stream_ended is finished as the pin finishes it
// (zed.js:200-208): what arrived is the answer, with no error invented.
func TestZedCutOffStreamIsFinishedAsThePinDoes(t *testing.T) {
	const cutOff = `{"event":{"type":"message_start","message":{"id":"m","role":"assistant","model":"glm-5.2","content":[],"usage":{"input_tokens":1,"output_tokens":0}}}}` + "\n" +
		`{"event":{"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}}` + "\n" +
		`{"event":{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}}` + "\n"
	for _, stream := range []bool{false, true} {
		t.Run(fmt.Sprint(stream), func(t *testing.T) {
			rig, _ := newZedIngressRig(t, cutOff)
			out := rig.post("/v1/chat/completions", fmt.Sprintf(`{"model":"x","stream":%v,"messages":[{"role":"user","content":"ping"}]}`, stream))
			if out.Code != 200 || !strings.Contains(out.Body.String(), "partial") || strings.Contains(out.Body.String(), "upstream_incomplete") || strings.Contains(out.Body.String(), `"error"`) {
				t.Fatalf("%d %s", out.Code, out.Body)
			}
		})
	}
}

// A line that is not JSON is skipped (zed.js:132-134).
func TestZedNonJSONLineIsSkipped(t *testing.T) {
	rig, _ := newZedIngressRig(t, "keep-alive\n"+zedNDJSON)
	out := rig.post("/v1/chat/completions", `{"model":"x","messages":[{"role":"user","content":"ping"}]}`)
	if out.Code != 200 || !strings.Contains(out.Body.String(), "pong") {
		t.Fatalf("%d %s", out.Code, out.Body)
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

// Zed's thread_id is the session the client's own request resolves to, from the
// client's headers and the body as the client sent it, before conversion
// (translator/index.js:96-99): a Messages client's metadata.user_id and a
// Responses client's headers survive the hop to Chat that would drop them.
func TestZedThreadIDIsResolvedFromTheClientRequest(t *testing.T) {
	thread := func(t *testing.T, path, body string, headers ...string) any {
		t.Helper()
		rig, seen := newZedIngressRig(t, zedNDJSON)
		if out := rig.post(path, body, headers...); out.Code != 200 {
			t.Fatalf("%d %s", out.Code, out.Body)
		}
		sent := seen("/completions")
		if len(sent) != 1 {
			t.Fatalf("completions %d", len(sent))
		}
		for _, call := range sent {
			if call.header.Get("X-Routeweft-Zed-Thread") != "" {
				t.Error("the session carrier reached the upstream")
			}
		}
		return sent[0].body["thread_id"]
	}
	const uuid = "0a1b2c3d-1111-2222-3333-444455556666"
	chat := `{"model":"x","messages":[{"role":"user","content":"ping"}]}`
	for name, tc := range map[string]struct {
		path, body string
		headers    []string
		want       any
	}{
		"chat x-session-id header":     {"/v1/chat/completions", chat, []string{"x-session-id", "s-hdr"}, "s-hdr"},
		"responses header":             {"/v1/responses", `{"model":"x","input":"ping"}`, []string{"x-client-request-id", "req-1"}, "req-1"},
		"responses claude-code header": {"/v1/responses", `{"model":"x","input":"ping"}`, []string{"x-claude-code-session-id", uuid}, "claude:" + uuid},
		"messages metadata.user_id":    {"/v1/messages", `{"model":"x","max_tokens":16,"metadata":{"user_id":"u_session_` + uuid + `"},"messages":[{"role":"user","content":"ping"}]}`, nil, "claude:" + uuid},
		"messages plain metadata":      {"/v1/messages", `{"model":"x","max_tokens":16,"metadata":{"user_id":"plain"},"messages":[{"role":"user","content":"ping"}]}`, nil, "plain"},
		"chat prompt_cache_key":        {"/v1/chat/completions", `{"model":"x","prompt_cache_key":"pck","messages":[{"role":"user","content":"ping"}]}`, nil, "pck"},
		"body thread_id beats headers": {"/v1/chat/completions", `{"model":"x","thread_id":"mine","messages":[{"role":"user","content":"ping"}]}`, []string{"x-session-id", "s-hdr"}, "mine"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := thread(t, tc.path, tc.body, tc.headers...); got != tc.want {
				t.Errorf("thread_id %v, want %v", got, tc.want)
			}
		})
	}
	// No explicit id: a conversation that shows the same opening assistant text
	// is the same thread across requests; the connection's session otherwise.
	opening := strings.Repeat("assistant-opening ", 4)
	conversation := func(more string) string {
		return `{"model":"x","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"` + opening + `"},{"role":"user","content":"` + more + `"}]}`
	}
	rig, seen := newZedIngressRig(t, zedNDJSON)
	for _, body := range []string{conversation("one"), conversation("two"), chat, chat} {
		if out := rig.post("/v1/chat/completions", body); out.Code != 200 {
			t.Fatalf("%d %s", out.Code, out.Body)
		}
	}
	threads := seen("/completions")
	if len(threads) != 4 {
		t.Fatalf("completions %d", len(threads))
	}
	id := func(i int) any { return threads[i].body["thread_id"] }
	if id(0) == nil || id(0) == "" || id(0) != id(1) {
		t.Errorf("one conversation, two turns: %v %v", id(0), id(1))
	}
	if id(2) == nil || id(2) != id(3) || id(2) == id(0) {
		t.Errorf("the connection's session is its own and stable: %v %v vs %v", id(2), id(3), id(0))
	}
}
