package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

func TestKenariNativeModelProbesStopAtFirstOutput(t *testing.T) {
	for _, tc := range []struct{ protocol, path, token string }{
		{"openai-chat", "/v1/chat/completions", `{"choices":[{"delta":{"reasoning_content":"thinking"}}]}`},
		{"openai-responses", "/v1/responses", `{"type":"response.output_item.added","item":{"type":"function_call","name":"exec","id":"fc_fixture"}}`},
		{"anthropic-messages", "/v1/messages", `{"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"OK"}}`},
	} {
		t.Run(tc.protocol, func(t *testing.T) {
			canceled := make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer close(canceled)
				if r.Method != "POST" || r.URL.Path != tc.path || r.URL.RawQuery != "" {
					t.Errorf("probe used wrong endpoint: %s %s", r.Method, r.URL)
				}
				auth, key := "Bearer fixture-key", ""
				if tc.protocol == "anthropic-messages" {
					auth, key = "", "fixture-key"
					if r.Header.Get("Anthropic-Version") != "2023-06-01" {
						t.Error("probe lost Messages version")
					}
				}
				if r.Header.Get("Authorization") != auth || r.Header.Get("X-Api-Key") != key || !strings.Contains(r.Header.Get("Accept"), "text/event-stream") {
					t.Error("probe lost native auth/stream headers")
				}
				var body map[string]any
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["model"] != "manual-model" || body["stream"] != true {
					t.Errorf("probe body: %v %v", body, err)
				}
				w.Header().Set("Content-Type", "text/event-stream")
				io.WriteString(w, "data: "+tc.token+"\n\n")
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
				case <-time.After(5 * time.Second):
					t.Error("probe waited for completion instead of canceling at first output")
				}
			}))
			defer upstream.Close()
			h, mux, store, _, cookie := newProvidersAPI(t, true, nil)
			defer store.Close()
			h.opts.Providers = registry.Builtins()
			created := doJSON(t, mux, cookie, "POST", "/admin/v1/provider-nodes", fmt.Sprintf(`{"kind":"builtin","providerId":"kenari","baseUrl":%q}`, upstream.URL))
			var node credentials.Node
			if created.Code != 200 || json.Unmarshal(created.Body.Bytes(), &node) != nil {
				t.Fatalf("node: %d %s", created.Code, created.Body)
			}
			created = doJSON(t, mux, cookie, "POST", "/admin/v1/connections", fmt.Sprintf(`{"nodeId":%q,"name":"fixture","identity":"fixture","secret":{"accessToken":"fixture-key"}}`, node.ID))
			var connection credentials.Connection
			if created.Code != 200 || json.Unmarshal(created.Body.Bytes(), &connection) != nil {
				t.Fatalf("connection: %d %s", created.Code, created.Body)
			}
			result := doJSON(t, mux, cookie, "POST", "/admin/v1/connections/"+connection.ID+"/test-models", fmt.Sprintf(`{"transport":%q,"modelIds":["manual-model"]}`, tc.protocol))
			if result.Code != 200 || !strings.Contains(result.Body.String(), `"classification":"reachable"`) || !strings.Contains(result.Body.String(), `"upstreamModelId":"manual-model"`) || strings.Contains(result.Body.String(), "fixture-key") {
				t.Fatalf("first-output probe: %d %s", result.Code, result.Body)
			}
			waitProbeSignal(t, canceled)
		})
	}
}
