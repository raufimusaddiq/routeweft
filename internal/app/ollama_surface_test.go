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
)

// #377: /v1/api/chat is an Ollama-shaped answer to the Chat handler, not a
// separate routing rule. The oracle runs its Chat handler and converts the
// result afterwards (src/app/api/v1/api/chat/route.js:26-27), so a model the
// catalogue lists and /v1/chat/completions serves must be served here too, on
// whichever wire the account speaks.
func TestOllamaSurfaceServesCatalogueModels(t *testing.T) {
	type wire struct {
		name, transport, path, response, stream string
	}
	chat := wire{
		name: "openai-chat", transport: "openai-chat", path: "/chat/completions",
		response: `{"id":"chatcmpl_1","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant","content":"OK"},"finish_reason":"stop"}]}`,
		stream: "data: {\"id\":\"chatcmpl_1\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"OK\"},\"finish_reason\":null}]}\n\n" +
			"data: {\"id\":\"chatcmpl_1\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
			"data: [DONE]\n\n",
	}
	messages := wire{
		name: "anthropic-messages", transport: "anthropic-messages", path: "/messages",
		response: `{"id":"msg_1","type":"message","role":"assistant","content":[{"type":"text","text":"OK"}],"stop_reason":"end_turn","usage":{"input_tokens":3,"output_tokens":1}}`,
		stream: "event: message_start\ndata: {\"type\":\"message_start\",\"message\":{\"id\":\"msg_1\",\"type\":\"message\",\"role\":\"assistant\",\"content\":[],\"stop_reason\":null,\"usage\":{\"input_tokens\":3,\"output_tokens\":1}}}\n\n" +
			"event: content_block_start\ndata: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"text\",\"text\":\"\"}}\n\n" +
			"event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"OK\"}}\n\n" +
			"event: content_block_stop\ndata: {\"type\":\"content_block_stop\",\"index\":0}\n\n" +
			"event: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"},\"usage\":{\"output_tokens\":1}}\n\n" +
			"event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n",
	}
	for _, w := range []wire{chat, messages} {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", w.name, stream), func(t *testing.T) {
				var paths []string
				var body []byte
				upstream := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
					paths = append(paths, r.URL.Path)
					body, _ = io.ReadAll(r.Body)
					if r.URL.Path != w.path {
						t.Errorf("dispatched %s, want %s", r.URL.Path, w.path)
					}
					answer := w.response
					if stream {
						rw.Header().Set("Content-Type", "text/event-stream")
						answer = w.stream
					}
					io.WriteString(rw, answer)
				}))
				defer upstream.Close()
				a, key := servingApp(t, upstream.URL, 1)
				node, err := a.creds.GetNode(context.Background(), "node")
				if err != nil {
					t.Fatal(err)
				}
				node.Transports = []string{w.transport}
				if _, err := a.creds.PutNode(context.Background(), node); err != nil {
					t.Fatal(err)
				}
				// The id is taken from discovery, exactly as a client reads it.
				listing := httptest.NewRecorder()
				listRequest := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
				listRequest.Header.Set("Authorization", "Bearer "+key)
				a.Handler().ServeHTTP(listing, listRequest)
				var catalogue struct {
					Data []struct {
						ID string `json:"id"`
					} `json:"data"`
				}
				if listing.Code != http.StatusOK || json.Unmarshal(listing.Body.Bytes(), &catalogue) != nil {
					t.Fatalf("models %d %s", listing.Code, listing.Body)
				}
				model := ""
				for _, entry := range catalogue.Data {
					if strings.HasPrefix(entry.ID, "test/") {
						model = entry.ID
						break
					}
				}
				if model == "" {
					t.Fatalf("no provider-qualified model listed: %s", listing.Body)
				}
				request := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}],"stream":%v}`, model, stream)
				// /v1/chat/completions serves it, so the model and account are usable.
				served := serveRequest(a.Handler(), key, "/v1/chat/completions", request)
				if served.Code != http.StatusOK {
					t.Fatalf("chat/completions status=%d body=%s", served.Code, served.Body)
				}
				paths = nil
				recorder := serveRequest(a.Handler(), key, "/v1/api/chat", request)
				if recorder.Code != http.StatusOK {
					t.Fatalf("/v1/api/chat status=%d body=%s outbound=%s", recorder.Code, recorder.Body, body)
				}
				wantType := "application/json"
				if stream {
					wantType = "application/x-ndjson"
				}
				if got := recorder.Header().Get("Content-Type"); got != wantType {
					t.Fatalf("content type %q", got)
				}
				if !strings.Contains(recorder.Body.String(), `"content":"OK"`) || !strings.Contains(recorder.Body.String(), `"done":true`) {
					t.Fatalf("ollama body %q", recorder.Body.String())
				}
				if stream && !recorder.Flushed {
					t.Fatal("ndjson stream was not flushed")
				}
				if len(paths) != 1 {
					t.Fatalf("upstream paths %v", paths)
				}
			})
		}
	}
}
