package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func TestConfiguredKenariNativeWire(t *testing.T) {
	for _, tc := range []struct {
		path, request, response, event string
	}{
		{"/v1/chat/completions", `"messages":[{"role":"system","content":"policy"},{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"exec","parameters":{"type":"object"}}}]`,
			`{"choices":[{"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`, `data: {"choices":[{"delta":{"content":"OK"},"finish_reason":"stop"}]}` + "\n\ndata: [DONE]\n\n"},
		{"/v1/responses", `"instructions":"policy","input":[{"type":"message","role":"system","content":"system"},{"type":"message","role":"user","content":"hi"}],"tools":[{"type":"custom","name":"exec"}],"text":{"format":{"type":"json_schema"},"verbosity":"low"}`,
			`{"id":"resp_fixture","status":"completed","output":[{"type":"function_call","id":"fc_1","name":"exec","call_id":"call_1","arguments":"{\"input\":\"ls\"}"}]}`,
			"event: response.completed\ndata: " + `{"type":"response.completed","response":{"status":"completed","output":[{"type":"function_call","id":"fc_1","name":"exec","call_id":"call_1","arguments":"{\"input\":\"ls\"}"}]}}` + "\n\n"},
		{"/v1/messages", `"system":"policy","messages":[{"role":"user","content":"hi"}],"max_tokens":32,"tools":[{"name":"exec","input_schema":{"type":"object"}}]`,
			`{"id":"msg_fixture","type":"message","role":"assistant","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn"}`,
			"event: content_block_delta\ndata: " + `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"OK"}}` + "\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"},
	} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%t", tc.path, stream), func(t *testing.T) {
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != "POST" || r.URL.Path != tc.path || r.URL.RawQuery != "" {
						t.Errorf("native endpoint: %s %s", r.Method, r.URL)
					}
					auth, key := "Bearer synthetic-0", ""
					if tc.path == "/v1/messages" {
						auth, key = "", "synthetic-0"
						if r.Header.Get("Anthropic-Version") != "2023-06-01" {
							t.Error("missing Messages version")
						}
					}
					if r.Header.Get("Authorization") != auth || r.Header.Get("X-Api-Key") != key || r.Header.Get("Content-Type") != "application/json" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Secret") != "" {
						t.Error("wrong native headers or leaked client cookie")
					}
					body, _ := io.ReadAll(r.Body)
					var parsed map[string]json.RawMessage
					if json.Unmarshal(body, &parsed) != nil || string(parsed["model"]) != `"model"` || string(parsed["stream"]) != fmt.Sprint(stream) || string(parsed["future"]) != `{"keep":true}` {
						t.Errorf("native body: %s", body)
					}
					if tc.path == "/v1/responses" {
						if parsed["instructions"] != nil || strings.Contains(string(body), `"role":"system"`) || !strings.Contains(string(body), `policy\n\nsystem\n\nhi`) || !strings.Contains(string(body), `"type":"function"`) || strings.Contains(string(body), `"format"`) || !strings.Contains(string(body), `"verbosity":"low"`) {
							t.Errorf("Responses quirks: %s", body)
						}
					} else {
						var original map[string]json.RawMessage
						if err := json.Unmarshal([]byte("{"+tc.request+"}"), &original); err != nil {
							t.Error(err)
							return
						}
						schema := `"parameters":{"type":"object"}`
						if tc.path == "/v1/messages" {
							schema = `"input_schema":{"type":"object"}`
						}
						if string(parsed["messages"]) != string(original["messages"]) || !strings.Contains(string(body), "policy") || !strings.Contains(string(parsed["tools"]), `"name":"exec"`) || !strings.Contains(string(parsed["tools"]), schema) {
							t.Errorf("native system/tools changed: %s", body)
						}
					}
					w.Header().Set("Content-Type", "application/json")
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
						io.WriteString(w, tc.event)
					} else {
						io.WriteString(w, tc.response)
					}
				}))
				defer upstream.Close()
				a, key := servingApp(t, upstream.URL, 1)
				if _, err := a.creds.PutNode(context.Background(), credentials.Node{ID: "node", Kind: credentials.NodeBuiltin, ProviderID: "kenari", Name: "kenari", BaseURL: upstream.URL}); err != nil {
					t.Fatal(err)
				}
				if err := a.runtime.PutModel(context.Background(), runtime.Model{ProviderID: "kenari", ID: "model"}); err != nil {
					t.Fatal(err)
				}
				body := fmt.Sprintf(`{"model":"kenari/model","stream":%t,"future":{"keep":true},%s}`, stream, tc.request)
				got := serveRequest(a.Handler(), key, tc.path, body)
				want := tc.response
				if stream {
					want = tc.event
				}
				if tc.path == "/v1/responses" {
					if !strings.Contains(got.Body.String(), `"type":"custom_tool_call"`) || !strings.Contains(got.Body.String(), `"input":"ls"`) || strings.Contains(got.Body.String(), `"type":"function_call"`) {
						t.Errorf("Responses custom-tool reply: %s", got.Body)
					}
				} else if got.Body.String() != want {
					t.Errorf("native reply changed: %s", got.Body)
				}
				if got.Code != 200 {
					t.Fatalf("native request: %d %s", got.Code, got.Body)
				}
			})
		}
	}
}
