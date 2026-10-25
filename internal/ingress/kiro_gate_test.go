package ingress

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// The integrity gate of executors/kiro.js:370-627, end to end: the real
// translators, the Kiro wire hook, the dispatcher's one repair request and the
// response leg of each client wire. No live provider is called; the upstream is a
// scripted transport answering with sanitized EventStream frames.

func kiroAnswer(frames ...[]byte) []byte {
	var out []byte
	for _, frame := range frames {
		out = append(out, frame...)
	}
	return out
}

var (
	kiroGoodTurn     = kiroAnswer(kiroFrame("assistantResponseEvent", `{"content":"pong"}`), kiroFrame("messageStopEvent", `{}`))
	kiroEllipsisTurn = kiroAnswer(kiroFrame("assistantResponseEvent", `{"content":"..."}`), kiroFrame("messageStopEvent", `{}`))
	kiroBadToolTurn  = kiroAnswer(kiroFrame("toolUseEvent", `{"toolUseId":"t","name":"tool_call","input":{"arguments":{}}}`), kiroFrame("messageStopEvent", `{"stopReason":"tool_use"}`))
)

// scripted answers each upstream call in turn with the next scripted body.
type scripted struct {
	mu     sync.Mutex
	bodies []string
}

func kiroScripted(t *testing.T, settings routing.ProviderSettings, answers ...func() io.ReadCloser) (serving, *scripted) {
	t.Helper()
	settings.ProfileARN, settings.AuthMethod, settings.Region = "arn:aws:codewhisperer:eu-west-1:123456789012:profile/fixture", "external_idp", "eu-west-1"
	ref := routing.ProviderRef{ProviderID: "kiro", Protocol: "kiro", BaseURL: "https://runtime.us-east-1.kiro.dev", UpstreamModel: "claude-sonnet-4.5", APIToken: "fixture-token",
		Connection: routing.ConnectionMetadata{Settings: settings}}
	s := newServing(t, ref)
	seen := &scripted{}
	s.handler.client = &http.Client{Transport: endpointTransport(func(r *http.Request) (*http.Response, error) {
		raw, _ := io.ReadAll(r.Body)
		r.Body.Close()
		seen.mu.Lock()
		seen.bodies = append(seen.bodies, string(raw))
		call := len(seen.bodies)
		seen.mu.Unlock()
		if call > len(answers) {
			t.Errorf("unscripted upstream call %d", call)
			return &http.Response{StatusCode: 500, Header: http.Header{}, Body: io.NopCloser(strings.NewReader("unscripted"))}, nil
		}
		return &http.Response{StatusCode: 200, Header: http.Header{"Content-Type": []string{"application/vnd.amazon.eventstream"}}, Body: answers[call-1]()}, nil
	})}
	return s, seen
}

func answer(body []byte) func() io.ReadCloser {
	return func() io.ReadCloser { return io.NopCloser(strings.NewReader(string(body))) }
}

func (s *scripted) calls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.bodies...)
}

func TestKiroIntegrityRepairsAnEllipsisOnEveryClientWire(t *testing.T) {
	for _, client := range clientWires {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", client, stream), func(t *testing.T) {
				s, seen := kiroScripted(t, routing.ProviderSettings{}, answer(kiroEllipsisTurn), answer(kiroGoodTurn))
				w := s.call(client, stream)
				if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "pong") || strings.Contains(w.Body.String(), "kiro_") {
					t.Fatalf("client got %d: %s", w.Code, w.Body)
				}
				calls := seen.calls()
				if len(calls) != 2 {
					t.Fatalf("upstream saw %d requests, want the request and one repair", len(calls))
				}
				if strings.Contains(calls[0], "only an ellipsis") || !strings.Contains(calls[1], "ended with only an ellipsis") || strings.Contains(calls[1], "systemPrompt") {
					t.Fatalf("repair request wrong:\nfirst  %s\nsecond %s", calls[0], calls[1])
				}
				if outcomes := s.outcomes(); len(outcomes) != 1 || outcomes[0].Status != http.StatusOK {
					t.Fatalf("outcomes %+v", outcomes)
				}
				if stream && !strings.Contains(w.Body.String(), ": kiro-validation") {
					t.Fatalf("the client was not told the answer is being validated:\n%s", w.Body)
				}
			})
		}
	}
}

func TestKiroIntegrityRetriesOnceThenReportsTheCodeToEveryClientWire(t *testing.T) {
	for _, client := range clientWires {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", client, stream), func(t *testing.T) {
				s, seen := kiroScripted(t, routing.ProviderSettings{}, answer(kiroEllipsisTurn), answer(kiroEllipsisTurn), answer(kiroGoodTurn))
				w := s.call(client, stream)
				// The Messages stream error event carries a message and no code.
				want := "kiro_ellipsis_retry_failed"
				if client == anthropic.MessagesProtocol && stream {
					want = "after one bounded retry: ellipsis"
				}
				if !strings.Contains(w.Body.String(), want) || strings.Contains(w.Body.String(), "pong") {
					t.Fatalf("client got %d: %s", w.Code, w.Body)
				}
				if !stream && w.Code != http.StatusBadGateway {
					t.Fatalf("status %d", w.Code)
				}
				if n := len(seen.calls()); n != 2 {
					t.Fatalf("upstream saw %d requests, want exactly the request and one repair", n)
				}
			})
		}
	}
}

// executors/kiro.js:376-377, :448-450: the connection's switch turns the tool repair off.
func TestKiroIntegrityRepairSwitch(t *testing.T) {
	for _, off := range []bool{true, false} {
		t.Run(fmt.Sprintf("off=%v", off), func(t *testing.T) {
			settings := routing.ProviderSettings{}
			if off {
				settings.KiroToolCallRepair = "false"
			}
			s, seen := kiroScripted(t, settings, answer(kiroBadToolTurn), answer(kiroGoodTurn))
			w := s.call(openai.ChatProtocol, false)
			calls := len(seen.calls())
			if off && (!strings.Contains(w.Body.String(), "invalid_kiro_tool_call") || calls != 1) {
				t.Fatalf("switch off: %d calls, client got %d %s", calls, w.Code, w.Body)
			}
			if !off && (w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "pong") || calls != 2 || !strings.Contains(seen.calls()[1], "tool_call wrapper was malformed")) {
				t.Fatalf("switch on: %d calls, client got %d %s", calls, w.Code, w.Body)
			}
		})
	}
}

func TestKiroIntegrityEnvironmentSwitch(t *testing.T) {
	t.Setenv("ROUTEWEFT_KIRO_TOOL_CALL_REPAIR", "false")
	s, seen := kiroScripted(t, routing.ProviderSettings{}, answer(kiroBadToolTurn), answer(kiroGoodTurn))
	if w := s.call(openai.ChatProtocol, false); !strings.Contains(w.Body.String(), "invalid_kiro_tool_call") || len(seen.calls()) != 1 {
		t.Fatalf("%d calls: %s", len(seen.calls()), w.Body)
	}
}

// A complete answer is relayed as it is and costs one request.
func TestKiroIntegrityLeavesACompleteAnswerAlone(t *testing.T) {
	s, seen := kiroScripted(t, routing.ProviderSettings{}, answer(kiroGoodTurn), answer(kiroGoodTurn))
	if w := s.call(openai.ChatProtocol, true); w.Code != 200 || !strings.Contains(w.Body.String(), "pong") || len(seen.calls()) != 1 {
		t.Fatalf("%d calls: %s", len(seen.calls()), w.Body)
	}
}

// Cancellation reaches the upstream read and leaves nothing behind (executors/kiro.js:378-379).
func TestKiroIntegrityCancellationLeavesNoGoroutine(t *testing.T) {
	closed := make(chan struct{})
	release := make(chan struct{})
	s, seen := kiroScripted(t, routing.ProviderSettings{}, func() io.ReadCloser { return &blockedBody{release: release, closed: closed} })
	before := runtime.NumGoroutine() // after the fixture's own goroutines exist
	ctx, cancel := context.WithCancel(context.Background())
	request := clientRequests[openai.ChatProtocol]
	r := httptest.NewRequest(http.MethodPost, request.path, strings.NewReader(fmt.Sprintf(request.body, true))).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+s.key)
	done := make(chan struct{})
	go func() { s.mux.ServeHTTP(httptest.NewRecorder(), r); close(done) }()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("the request outlived its client")
	}
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("the upstream body was not closed")
	}
	if n := len(seen.calls()); n != 1 {
		t.Fatalf("a cancelled request made %d upstream requests", n)
	}
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("%d goroutines before, %d after", before, after)
	}
}

type blockedBody struct {
	release chan struct{}
	closed  chan struct{}
	once    sync.Once
}

func (b *blockedBody) Read([]byte) (int, error) {
	<-b.release
	return 0, io.ErrClosedPipe
}

func (b *blockedBody) Close() error {
	b.once.Do(func() { close(b.release); close(b.closed) })
	return nil
}
