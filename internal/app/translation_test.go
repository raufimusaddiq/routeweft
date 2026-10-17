package app

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
)

type servingTranslationFixture struct {
	Source           string          `json:"source"`
	Request          json.RawMessage `json:"request"`
	TargetRequest    json.RawMessage `json:"target_request"`
	UpstreamResponse json.RawMessage `json:"upstream_response"`
	Response         json.RawMessage `json:"response"`
}

func servingTranslationFixtures(t testing.TB) []servingTranslationFixture {
	t.Helper()
	data, err := os.ReadFile("../protocol/openai/testdata/chat-responses.json")
	if err != nil {
		t.Fatal(err)
	}
	var result []servingTranslationFixture
	if err := json.Unmarshal(data, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func assertTranslationJSON(t testing.TB, got, want []byte) {
	t.Helper()
	var left, right any
	if err := json.Unmarshal(got, &left); err != nil {
		t.Errorf("invalid JSON %s: %v", got, err)
		return
	}
	if err := json.Unmarshal(want, &right); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(left, right) {
		t.Errorf("got %s\nwant %s", got, want)
	}
}

func translationStreamFixture(t testing.TB, source string) []byte {
	t.Helper()
	name := "chat-upstream.sse"
	if source == openai.ChatProtocol {
		name = "responses-upstream.sse"
	}
	data, err := os.ReadFile("../protocol/openai/testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestConfiguredServingChatResponsesTranslation(t *testing.T) {
	for _, fixture := range servingTranslationFixtures(t) {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", fixture.Source, stream), func(t *testing.T) {
				path, target, targetPath := "/v1/responses", openai.ChatProtocol, "/native/chat/completions"
				if fixture.Source == openai.ChatProtocol {
					path, target, targetPath = "/v1/chat/completions", openai.ResponsesProtocol, "/native/responses"
				}
				var body, expected map[string]any
				json.Unmarshal(fixture.Request, &body)
				json.Unmarshal(fixture.TargetRequest, &expected)
				body["stream"], expected["stream"] = stream, stream
				if stream && target == openai.ChatProtocol {
					expected["stream_options"] = map[string]any{"include_usage": true}
				}
				request, _ := json.Marshal(body)
				wantRequest, _ := json.Marshal(expected)
				upstreamBody := fixture.UpstreamResponse
				if stream {
					upstreamBody = translationStreamFixture(t, fixture.Source)
				}
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					actual, _ := io.ReadAll(r.Body)
					assertTranslationJSON(t, actual, wantRequest)
					if r.URL.Path != targetPath || r.Header.Get("Authorization") != "Bearer synthetic-0" || r.Header.Get("Cookie") != "" || r.Header.Get("X-Secret") != "" {
						t.Errorf("incorrect dispatch path=%s headers=%v", r.URL.Path, r.Header)
					}
					w.Header().Set("Content-Type", "application/json")
					if stream {
						w.Header().Set("Content-Type", "text/event-stream")
					}
					w.Write(upstreamBody)
				}))
				defer upstream.Close()
				a, key := servingApp(t, upstream.URL+"/native", 1)
				node, err := a.creds.GetNode(context.Background(), "node")
				if err != nil {
					t.Fatal(err)
				}
				node.Transports = []string{target}
				if _, err := a.creds.PutNode(context.Background(), node); err != nil {
					t.Fatal(err)
				}
				if err := a.store.Close(); err != nil {
					t.Fatal(err)
				}
				if err := a.Initialize(context.Background()); err != nil {
					t.Fatal(err)
				}
				if err := a.store.Close(); err != nil {
					t.Fatal(err)
				}
				w := serveRequest(a.Handler(), key, path, string(request))
				if w.Code != 200 || calls.Load() != 1 {
					t.Fatalf("status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body)
				}
				if !stream {
					assertTranslationJSON(t, w.Body.Bytes(), fixture.Response)
					return
				}
				name := "responses-expected.sse"
				if fixture.Source == openai.ChatProtocol {
					name = "chat-expected.sse"
				}
				want, err := os.ReadFile("../protocol/openai/testdata/" + name)
				if err != nil {
					t.Fatal(err)
				}
				if w.Body.String() != string(want) || w.Header().Get("Content-Type") != "text/event-stream" {
					t.Fatalf("stream differs\ngot %s\nwant %s", w.Body, want)
				}
			})
		}
	}
}

func TestConfiguredServingTranslationFallback(t *testing.T) {
	fixture := servingTranslationFixtures(t)[1]
	var attempts []string
	var mu sync.Mutex
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		attempts = append(attempts, r.URL.Path+" "+r.Header.Get("Authorization"))
		mu.Unlock()
		if r.URL.Path == "/responses" {
			w.WriteHeader(503)
			fmt.Fprint(w, `{"error":"temporarily unavailable"}`)
			return
		}
		w.Write(fixture.UpstreamResponse)
	}))
	defer upstream.Close()
	a, key := servingApp(t, upstream.URL, 2)
	ctx := context.Background()
	node, _ := a.creds.GetNode(ctx, "node")
	node.Transports = []string{openai.ChatProtocol}
	if _, err := a.creds.PutNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	// The lower-priority native account must precede translated accounts.
	node.ID, node.Prefix, node.Transports = "native", "", []string{openai.ResponsesProtocol}
	if _, err := a.creds.PutNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	account, _ := a.creds.GetConnection(ctx, "account-01")
	account.NodeID = "native"
	if _, err := a.creds.PutConnection(ctx, account); err != nil {
		t.Fatal(err)
	}
	if err := a.store.Close(); err != nil {
		t.Fatal(err)
	}
	w := serveRequest(a.Handler(), key, "/v1/responses", string(fixture.Request))
	mu.Lock()
	defer mu.Unlock()
	if w.Code != 200 || !reflect.DeepEqual(attempts, []string{"/responses Bearer synthetic-1", "/chat/completions Bearer synthetic-0"}) {
		t.Fatalf("status=%d attempts=%v body=%s", w.Code, attempts, w.Body)
	}
	assertTranslationJSON(t, w.Body.Bytes(), fixture.Response)
}

func TestConfiguredServingCompactSkipsUntranslatableAccounts(t *testing.T) {
	for _, nativeAccounts := range []int{0, 1, 2} {
		t.Run(fmt.Sprintf("native=%d", nativeAccounts), func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				if r.URL.Path != "/responses/compact" || r.Header.Get("Authorization") == "Bearer synthetic-0" {
					t.Errorf("ineligible compact account dispatched: %s", r.URL.Path)
				}
				if r.Header.Get("Authorization") == "Bearer synthetic-1" {
					w.WriteHeader(503)
					fmt.Fprint(w, `{"error":"original upstream failure"}`)
					return
				}
				fmt.Fprint(w, `{"id":"compact_fixture","output":[]}`)
			}))
			defer upstream.Close()
			a, key := servingApp(t, upstream.URL, nativeAccounts+1)
			ctx := context.Background()
			node, err := a.creds.GetNode(ctx, "node")
			if err != nil {
				t.Fatal(err)
			}
			node.Transports = []string{openai.ChatProtocol}
			if _, err := a.creds.PutNode(ctx, node); err != nil {
				t.Fatal(err)
			}
			if nativeAccounts > 0 {
				node.ID, node.Prefix, node.Transports = "native", "", []string{openai.ResponsesProtocol}
				if _, err := a.creds.PutNode(ctx, node); err != nil {
					t.Fatal(err)
				}
				for i := 1; i <= nativeAccounts; i++ {
					account, err := a.creds.GetConnection(ctx, fmt.Sprintf("account-%02d", i))
					if err != nil {
						t.Fatal(err)
					}
					account.NodeID = node.ID
					if _, err := a.creds.PutConnection(ctx, account); err != nil {
						t.Fatal(err)
					}
				}
			}
			w := serveRequest(a.Handler(), key, "/v1/responses/compact", `{"model":"fast","input":"hi"}`)
			status, want := 503, "no_eligible_accounts"
			if nativeAccounts == 1 {
				want = `{"error":"original upstream failure"}`
			}
			if nativeAccounts == 2 {
				status, want = 200, `{"id":"compact_fixture","output":[]}`
			}
			if w.Code != status || !strings.Contains(w.Body.String(), want) || calls.Load() != int32(nativeAccounts) {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body)
			}
		})
	}
}

func TestConfiguredServingTranslatedStreamFailureCoolsAccount(t *testing.T) {
	for _, source := range []string{openai.ChatProtocol, openai.ResponsesProtocol} {
		for _, malformed := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/malformed=%v", source, malformed), func(t *testing.T) {
				var calls atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					calls.Add(1)
					w.Header().Set("Content-Type", "text/event-stream")
					if malformed {
						fmt.Fprint(w, "data: invalid JSON\n\n")
						return
					}
					if source == openai.ChatProtocol {
						fmt.Fprint(w, "data: {\"type\":\"response.output_text.delta\",\"item_id\":\"m\",\"delta\":\"partial\"}\n\n")
					} else {
						fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"partial\"}}]}\n\n")
					}
				}))
				defer upstream.Close()
				a, key := servingApp(t, upstream.URL, 1)
				node, err := a.creds.GetNode(context.Background(), "node")
				if err != nil {
					t.Fatal(err)
				}
				node.Transports = []string{openai.ChatProtocol}
				path, terminal := "/v1/responses", "event: response.failed\n"
				if source == openai.ChatProtocol {
					node.Transports, path, terminal = []string{openai.ResponsesProtocol}, "/v1/chat/completions", "data: [DONE]\n"
				}
				if _, err := a.creds.PutNode(context.Background(), node); err != nil {
					t.Fatal(err)
				}
				request := `{"model":"fast","input":"hi","messages":[{"role":"user","content":"hi"}],"stream":true}`
				w := serveRequest(a.Handler(), key, path, request)
				until, cooling := a.runtime.State().CooldownUntil("account-00", time.Now())
				if w.Code != 200 || strings.Count(w.Body.String(), terminal) != 1 || !cooling || time.Until(until) < 29*time.Second {
					t.Fatalf("status=%d cooldown=%v until=%s body=%s", w.Code, cooling, until, w.Body)
				}
				w = serveRequest(a.Handler(), key, path, request)
				if w.Code != 503 || !strings.Contains(w.Body.String(), "no_eligible_accounts") || calls.Load() != 1 {
					t.Fatalf("failed account reused: status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body)
				}
			})
		}
	}
}

func TestConfiguredServingTranslatedNonstreamLimit(t *testing.T) {
	for _, source := range []string{openai.ChatProtocol, openai.ResponsesProtocol} {
		for _, over := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/over=%v", source, over), func(t *testing.T) {
				body := `{"id":"synthetic","output":[],"padding":"`
				if source == openai.ResponsesProtocol {
					body = `{"id":"synthetic","choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}],"padding":"`
				}
				padding := strings.Repeat("x", openai.MaxTranslatedResponseBytes-len(body)-2)
				if over {
					padding += "x"
				}
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					io.WriteString(w, body)
					io.WriteString(w, padding)
					io.WriteString(w, `"}`)
				}))
				defer upstream.Close()
				a, key := servingApp(t, upstream.URL, 1)
				node, err := a.creds.GetNode(context.Background(), "node")
				if err != nil {
					t.Fatal(err)
				}
				node.Transports = []string{openai.ChatProtocol}
				path := "/v1/responses"
				if source == openai.ChatProtocol {
					node.Transports, path = []string{openai.ResponsesProtocol}, "/v1/chat/completions"
				}
				if _, err := a.creds.PutNode(context.Background(), node); err != nil {
					t.Fatal(err)
				}
				w := serveRequest(a.Handler(), key, path, `{"model":"fast","input":"hi","messages":[{"role":"user","content":"hi"}]}`)
				status := 200
				if over {
					status = 502
				}
				if w.Code != status || over && !strings.Contains(w.Body.String(), "upstream_response_invalid") || strings.Contains(w.Body.String(), "padding") {
					t.Fatalf("status=%d want=%d body=%s", w.Code, status, w.Body)
				}
			})
		}
	}
}

func TestConfiguredServingTranslatedStreamCancellation(t *testing.T) {
	for _, source := range []string{openai.ChatProtocol, openai.ResponsesProtocol} {
		t.Run(source, func(t *testing.T) {
			cancelled := make(chan struct{})
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				if source == openai.ChatProtocol {
					fmt.Fprint(w, "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"item_id\":\"msg_1\",\"delta\":\"first\"}\n\n")
				} else {
					fmt.Fprint(w, "data: {\"id\":\"chat_fixture\",\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n")
				}
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				close(cancelled)
			}))
			defer upstream.Close()
			a, key := servingApp(t, upstream.URL, 2)
			node, _ := a.creds.GetNode(context.Background(), "node")
			node.Transports = []string{openai.ChatProtocol}
			path := "/v1/responses"
			if source == openai.ChatProtocol {
				node.Transports, path = []string{openai.ResponsesProtocol}, "/v1/chat/completions"
			}
			if _, err := a.creds.PutNode(context.Background(), node); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(a.Handler())
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+path, strings.NewReader(`{"model":"fast","input":"hi","messages":[{"role":"user","content":"hi"}],"stream":true}`))
			request.Header.Set("Authorization", "Bearer "+key)
			response, err := server.Client().Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if _, err := response.Body.Read(make([]byte, 1)); err != nil {
				t.Fatal(err)
			}
			cancel()
			select {
			case <-cancelled:
			case <-time.After(3 * time.Second):
				t.Fatal("translated upstream was not cancelled")
			}
			if calls.Load() != 1 {
				t.Fatal("translated stream was replayed")
			}
			if _, cooling := a.runtime.State().CooldownUntil("account-00", time.Now()); cooling {
				t.Fatal("client cancellation penalized the upstream account")
			}
		})
	}
}
