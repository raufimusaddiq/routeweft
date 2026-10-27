package ingress

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

const antigravityImageAnswer = `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"a cat"},{"inlineData":{"mimeType":"image/png","data":"QUJD"}}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":1290,"totalTokenCount":1297}},"traceId":"t"}`

// An Antigravity image model is called at generateContent with the image_gen
// envelope whatever the client asked (executors/antigravity.js:121-123,144-189,
// chatCore.js:149-154), and every client wire is answered in its own framing.
// A Chat client gets the picture as an image content block, streamed or not.
// Responses and Messages clients get the text and usage, as at the pin, whose
// response legs carry no images either (PARITY 6.26, open).
func TestAntigravityImageModelIsServedToEveryClientWire(t *testing.T) {
	for _, client := range clientWires {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", client, stream), func(t *testing.T) {
				var seen struct {
					sync.Mutex
					path, query, accept string
					body                map[string]any
				}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					raw, _ := io.ReadAll(r.Body)
					seen.Lock()
					seen.path, seen.query, seen.accept = r.URL.Path, r.URL.RawQuery, r.Header.Get("Accept")
					seen.body = map[string]any{}
					_ = json.Unmarshal(raw, &seen.body)
					seen.Unlock()
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, antigravityImageAnswer)
				}))
				defer upstream.Close()
				s := newServing(t, routing.ProviderRef{ProviderID: "antigravity", Protocol: "antigravity", BaseURL: upstream.URL, UpstreamModel: "gemini-3.1-flash-image-16x9", APIToken: "ya29.fixture",
					Connection: routing.ConnectionMetadata{Settings: routing.ProviderSettings{ProjectID: "fixture-project"}}})
				w := s.call(client, stream)
				if w.Code != http.StatusOK {
					t.Fatalf("client got %d: %s", w.Code, w.Body)
				}
				seen.Lock()
				defer seen.Unlock()
				if seen.path != "/v1internal:generateContent" || seen.query != "" || seen.accept != "application/json" {
					t.Errorf("upstream saw %s?%s accepting %q", seen.path, seen.query, seen.accept)
				}
				request, _ := seen.body["request"].(map[string]any)
				config, _ := request["generationConfig"].(map[string]any)
				if seen.body["requestType"] != "image_gen" || seen.body["model"] != "gemini-3.1-flash-image" || seen.body["project"] != "fixture-project" ||
					fmt.Sprint(config["imageConfig"]) != "map[aspectRatio:16:9]" || request["tools"] != nil || request["systemInstruction"] != nil {
					t.Errorf("envelope %v", seen.body)
				}
				requestID, _ := seen.body["requestId"].(string)
				if !strings.HasPrefix(requestID, "agent/") {
					t.Errorf("request id %q is not the IDE form", requestID)
				}
				body := w.Body.String()
				want := []string{"a cat", "1290"}
				if client == openai.ChatProtocol {
					want = append(want, "data:image/png;base64,QUJD")
				}
				if stream {
					want = append(want, "data: ")
				}
				for _, part := range want {
					if !strings.Contains(body, part) {
						t.Errorf("response lacks %q:\n%s", part, body)
					}
				}
				outcomes := s.outcomes()
				if len(outcomes) != 1 || outcomes[0].Status != http.StatusOK || outcomes[0].ProviderID != "antigravity" {
					t.Fatalf("outcomes %+v", outcomes)
				}
				// A buffered answer's usage is read; a streamed client's is not, because
				// the usage scanner reads SSE lines and the upstream sent one JSON body.
				// Unobserved is recorded as such, never as zero (SPEC 21.1).
				if got := outcomes[0]; stream != got.UsageUnobserved || (!stream && (got.InputTokens != 7 || got.OutputTokens != 1290)) {
					t.Errorf("usage record %+v", got)
				}
			})
		}
	}
}

// A request the image path cannot carry whole is refused before anything is
// sent, where the pin drops the rest and answers.
func TestAntigravityImageModelRefusesASystemPromptWithoutSending(t *testing.T) {
	var hits int
	upstream := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits++ }))
	defer upstream.Close()
	s := newServing(t, routing.ProviderRef{ProviderID: "antigravity", Protocol: "antigravity", BaseURL: upstream.URL, UpstreamModel: "gemini-3.1-flash-image", APIToken: "ya29.fixture",
		Connection: routing.ConnectionMetadata{Settings: routing.ProviderSettings{ProjectID: "fixture-project"}}})
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"test/model","messages":[{"role":"system","content":"terse"},{"role":"user","content":"draw"}]}`))
	r.Header.Set("Authorization", "Bearer "+s.key)
	w := httptest.NewRecorder()
	s.mux.ServeHTTP(w, r)
	// Every candidate refused the translation: the dispatcher's request_translation_failed.
	if w.Code != http.StatusBadGateway || hits != 0 || !strings.Contains(w.Body.String(), "request_translation_failed") || !strings.Contains(w.Body.String(), "system") {
		t.Errorf("status %d after %d requests: %s", w.Code, hits, w.Body)
	}
}
