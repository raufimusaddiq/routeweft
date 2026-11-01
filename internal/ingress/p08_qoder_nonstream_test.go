package ingress

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// qoderTransport answers Qoder's two hosts from memory: the model catalog and the
// signed agent_chat_generation POST. Nothing leaves the process.
type qoderTransport struct {
	mu     sync.Mutex
	chats  []*http.Request
	bodies [][]byte
	answer string
}

func (q *qoderTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	respond := func(contentType, body string) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{contentType}}, Body: io.NopCloser(strings.NewReader(body)), Request: r}, nil
	}
	switch {
	case strings.HasSuffix(r.URL.Path, "/algo/api/v2/model/list"):
		return respond("application/json", `{"chat":[{"key":"auto","display_name":"Auto"}]}`)
	case strings.Contains(r.URL.Path, "agent_chat_generation"):
		raw, _ := io.ReadAll(r.Body)
		q.mu.Lock()
		q.chats, q.bodies = append(q.chats, r), append(q.bodies, raw)
		q.mu.Unlock()
		return respond("text/event-stream", q.answer)
	}
	return nil, fmt.Errorf("unexpected request %s %s", r.Method, r.URL)
}

// Qoder always asks for a stream (executors/qoder.js:613-702: Accept
// text/event-stream, the answer a {statusCodeValue, body} envelope SSE it re-wraps),
// and the reference folds that into one body for a client that did not stream
// (chatCore, forceStream). Here: a Chat, Responses or Messages client that sent no
// stream gets one document, a streaming one gets the stream, and the upstream saw
// the signed, encoded envelope either way.
func TestQoderServesNonStreamAndStreamClientsOnEveryWire(t *testing.T) {
	liftFence(t, "qoder")
	const sse = "data: {\"statusCodeValue\":200,\"body\":\"{\\\"choices\\\":[{\\\"delta\\\":{\\\"content\\\":\\\"pong\\\"}}]}\"}\n\n" +
		"data: {\"statusCodeValue\":200,\"body\":\"{\\\"choices\\\":[{\\\"delta\\\":{},\\\"finish_reason\\\":\\\"stop\\\"}],\\\"usage\\\":{\\\"prompt_tokens\\\":7,\\\"completion_tokens\\\":2}}\"}\n\ndata: [DONE]\n\n"
	for _, client := range clientWires {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", client, stream), func(t *testing.T) {
				upstream := &qoderTransport{answer: sse}
				ref := routing.ProviderRef{ProviderID: "qoder", Protocol: "qoder", UpstreamModel: "auto", BaseURL: "https://api3.qoder.sh", APIToken: "dt-synthetic", ConnectionID: "qoder-a",
					Connection: routing.ConnectionMetadata{Name: "acct", Settings: routing.ProviderSettings{UserID: "user-1", MachineID: "machine-1", Email: "e@example.invalid"}}}
				manager := newManager(t)
				_, key, err := manager.CreateAPIKey(context.Background(), "qoder-p08")
				if err != nil {
					t.Fatal(err)
				}
				var mu sync.Mutex
				var outcomes []RequestOutcome
				handler := New(manager, Options{
					TranslateChat: TranslateChat, TranslateResponses: TranslateResponses, TranslateMessages: TranslateMessages,
					ClientFor: func(string) *http.Client { return &http.Client{Transport: upstream} },
					Candidates: func(*runtime.RuntimeSnapshot, string) ([]routing.Account, bool) {
						return []routing.Account{{ID: ref.ConnectionID, Enabled: true}}, true
					},
					AccountProvider: func(_ *runtime.RuntimeSnapshot, _, _, source string) (routing.ProviderRef, bool) {
						return ref, CanTranslate(source, ref.Protocol)
					},
					OnRequestComplete: func(o RequestOutcome) { mu.Lock(); outcomes = append(outcomes, o); mu.Unlock() },
				})
				mux := http.NewServeMux()
				handler.Attach(mux)
				s := serving{mux, key, handler, func() []RequestOutcome {
					mu.Lock()
					defer mu.Unlock()
					return append([]RequestOutcome(nil), outcomes...)
				}}
				w := s.call(client, stream)
				if w.Code != 200 || !strings.Contains(w.Body.String(), "pong") {
					t.Fatalf("client got %d: %s", w.Code, w.Body)
				}
				if strings.Contains(w.Body.String(), "statusCodeValue") {
					t.Errorf("the envelope reached the client: %s", w.Body)
				}
				// A client that did not ask to stream gets one JSON document, not SSE.
				if !stream && (strings.Contains(w.Body.String(), "data: ") || !strings.HasPrefix(strings.TrimSpace(w.Body.String()), "{")) {
					t.Errorf("non-stream client was not given one JSON body: %s", w.Body)
				}
				upstream.mu.Lock()
				defer upstream.mu.Unlock()
				if len(upstream.chats) != 1 || upstream.chats[0].Header.Get("Accept") != "text/event-stream" || upstream.chats[0].Header.Get("Accept-Encoding") != "identity" || upstream.chats[0].Header.Get("X-Model-Key") != "auto" {
					t.Fatalf("upstream saw %v", upstream.chats)
				}
				if strings.Contains(string(upstream.bodies[0]), "messages") {
					t.Errorf("the plain Chat body reached Qoder instead of its encoded envelope")
				}
			})
		}
	}
}
