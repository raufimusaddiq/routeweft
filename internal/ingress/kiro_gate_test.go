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
	hosts  []string
}

// kiroHandler answers one upstream request: n counts them from 1.
type kiroHandler func(n int, host string, body string) (int, io.ReadCloser, error)

func kiroScripted(t *testing.T, settings routing.ProviderSettings, answers ...func() io.ReadCloser) (serving, *scripted) {
	t.Helper()
	return kiroTransport(t, settings, func(n int, _ string, _ string) (int, io.ReadCloser, error) {
		if n > len(answers) {
			t.Errorf("unscripted upstream call %d", n)
			return 500, io.NopCloser(strings.NewReader("unscripted")), nil
		}
		return 200, answers[n-1](), nil
	})
}

func kiroTransport(t *testing.T, settings routing.ProviderSettings, handle kiroHandler) (serving, *scripted) {
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
		seen.hosts = append(seen.hosts, r.URL.Hostname())
		call := len(seen.bodies)
		seen.mu.Unlock()
		status, body, err := handle(call, r.URL.Hostname(), string(raw))
		if err != nil {
			return nil, err
		}
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/vnd.amazon.eventstream"}}, Body: body}, nil
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

// The repair request is the pin's base executor, not the attempt again
// (executors/base.js:100-184): it starts at the first host, retries a host in place
// on 502/503/504 with the pin's backoff, moves on at 429 and 401/403/404, and a
// final non-2xx is reported with its body.

const (
	hostQ    = "q.eu-west-1.amazonaws.com"
	hostCW   = "codewhisperer.eu-west-1.amazonaws.com"
	hostKiro = "runtime.us-east-1.kiro.dev"
)

func stubRepairSleep(t *testing.T) *[]time.Duration {
	t.Helper()
	var slept []time.Duration
	previous := repairSleep
	repairSleep = func(_ context.Context, d time.Duration) error { slept = append(slept, d); return nil }
	t.Cleanup(func() { repairSleep = previous })
	return &slept
}

func status(code int, body string) (int, io.ReadCloser, error) {
	return code, io.NopCloser(strings.NewReader(body)), nil
}

func TestKiroRepairStartsAtTheFirstHostAndWalksLikeTheBaseExecutor(t *testing.T) {
	slept := stubRepairSleep(t)
	// The first request is served by the second host (the first refused the credential);
	// the repair does not remember that: it starts again at the first host (base.js:127).
	s, seen := kiroTransport(t, routing.ProviderSettings{}, func(n int, host, _ string) (int, io.ReadCloser, error) {
		switch n {
		case 1:
			return status(403, "no")
		case 2:
			return 200, io.NopCloser(strings.NewReader(string(kiroEllipsisTurn))), nil
		case 3: // repair: 503, 503 in place on the first host (2 s each), then it answers
			return status(503, "busy")
		case 4:
			return status(503, "busy")
		}
		return 200, io.NopCloser(strings.NewReader(string(kiroGoodTurn))), nil
	})
	w := s.call(openai.ChatProtocol, false)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "pong") {
		t.Fatalf("%d %s", w.Code, w.Body)
	}
	if got := strings.Join(seen.hosts, ","); got != strings.Join([]string{hostQ, hostCW, hostQ, hostQ, hostQ}, ",") {
		t.Fatalf("hosts %s", got)
	}
	if len(*slept) != 2 || (*slept)[0] != 2*time.Second || (*slept)[1] != 2*time.Second {
		t.Fatalf("in-place retry delays %v, want the pin's 503 ladder (2s each)", *slept)
	}
	if strings.Contains(seen.bodies[0], "only an ellipsis") || !strings.Contains(seen.bodies[2], "only an ellipsis") || seen.bodies[2] != seen.bodies[3] {
		t.Fatalf("repair bodies wrong:\n%v", seen.bodies)
	}
}

func TestKiroRepairMovesToTheNextHostOn401403404And429(t *testing.T) {
	for _, code := range []int{401, 403, 404, 429} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			slept := stubRepairSleep(t)
			s, seen := kiroTransport(t, routing.ProviderSettings{}, func(n int, host, _ string) (int, io.ReadCloser, error) {
				switch n {
				case 1:
					return 200, io.NopCloser(strings.NewReader(string(kiroEllipsisTurn))), nil
				case 2:
					return status(code, "refused")
				}
				return 200, io.NopCloser(strings.NewReader(string(kiroGoodTurn))), nil
			})
			if w := s.call(openai.ChatProtocol, false); w.Code != 200 || !strings.Contains(w.Body.String(), "pong") {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			if got := strings.Join(seen.hosts, ","); got != strings.Join([]string{hostQ, hostQ, hostCW}, ",") || len(*slept) != 0 {
				t.Fatalf("hosts %s, delays %v", got, *slept)
			}
		})
	}
}

// kiro-terminal-integrity.test.js "surfaces retry HTTP failures as SSE" / "bounds the retry HTTP error body".
func TestKiroRepairReportsTheLastHostsRefusal(t *testing.T) {
	stubRepairSleep(t)
	s, seen := kiroTransport(t, routing.ProviderSettings{}, func(n int, host, _ string) (int, io.ReadCloser, error) {
		if n == 1 {
			return 200, io.NopCloser(strings.NewReader(string(kiroEllipsisTurn))), nil
		}
		return status(401, "error-start-"+strings.Repeat("x", 10000)+"-error-tail")
	})
	w := s.call(openai.ChatProtocol, true)
	body := w.Body.String()
	if !strings.Contains(body, "kiro_integrity_retry_upstream_error") || !strings.Contains(body, "error-start-") || strings.Contains(body, "error-tail") || len(body) > 6000 {
		t.Fatalf("%d (%d bytes): %.300s", w.Code, len(body), body)
	}
	if got := strings.Join(seen.hosts, ","); got != strings.Join([]string{hostQ, hostQ, hostCW, hostKiro}, ",") {
		t.Fatalf("every host is tried once: %s", got)
	}
}

func TestKiroRepairRetriesInPlaceThenFallsThroughWhenTheHostKeepsFailing(t *testing.T) {
	slept := stubRepairSleep(t)
	s, seen := kiroTransport(t, routing.ProviderSettings{}, func(n int, host, _ string) (int, io.ReadCloser, error) {
		if n == 1 {
			return 200, io.NopCloser(strings.NewReader(string(kiroEllipsisTurn))), nil
		}
		return status(502, "bad gateway")
	})
	w := s.call(openai.ChatProtocol, false)
	// 502 is not a host-fallback status: after its in-place ladder (3 retries, 3 s) the answer is returned.
	if !strings.Contains(w.Body.String(), "kiro_integrity_retry_upstream_error") || len(seen.hosts) != 5 {
		t.Fatalf("hosts %v: %s", seen.hosts, w.Body)
	}
	if len(*slept) != 3 || (*slept)[0] != 3*time.Second {
		t.Fatalf("delays %v", *slept)
	}
}

func TestKiroRepairNetworkFailureIsTheGatesOwnFailure(t *testing.T) {
	slept := stubRepairSleep(t)
	s, seen := kiroTransport(t, routing.ProviderSettings{}, func(n int, host, _ string) (int, io.ReadCloser, error) {
		if n == 1 {
			return 200, io.NopCloser(strings.NewReader(string(kiroEllipsisTurn))), nil
		}
		return 0, nil, fmt.Errorf("dial refused by %s", host)
	})
	w := s.call(openai.ChatProtocol, true)
	body := w.Body.String()
	// Per host: the request and three in-place retries (a network error is a 502); then the next host.
	if !strings.Contains(body, "kiro_integrity_gate_failed") || !strings.Contains(body, "dial refused") || len(seen.hosts) != 1+4*3 {
		t.Fatalf("%d requests: %.300s", len(seen.hosts), body)
	}
	if len(*slept) != 9 {
		t.Fatalf("delays %v", *slept)
	}
}

// kiro-usage-and-tool-integrity.test.js "C", through every client wire: the valid call is
// served and only the invalid one is dropped, with no repair request.
func TestKiroServesTheValidCallAndDropsTheInvalidOne(t *testing.T) {
	turn := kiroAnswer(
		kiroFrame("toolUseEvent", `{"toolUseId":"good","name":"lookup","input":{"city":"Oslo"}}`),
		kiroFrame("toolUseEvent", `{"toolUseId":"bad","name":"tool_call","input":{"arguments":{}}}`),
		kiroFrame("messageStopEvent", `{"stopReason":"tool_use"}`))
	for _, client := range clientWires {
		for _, stream := range []bool{false, true} {
			s, seen := kiroScripted(t, routing.ProviderSettings{}, answer(turn), answer(kiroGoodTurn))
			w := s.call(client, stream)
			body := w.Body.String()
			if w.Code != 200 || !strings.Contains(body, "lookup") || strings.Contains(body, `"bad"`) || strings.Contains(body, "invalid_kiro_tool_call") || len(seen.calls()) != 1 {
				t.Errorf("%s stream=%v: %d, %d requests: %.300s", client, stream, w.Code, len(seen.calls()), body)
			}
		}
	}
}

// The pin's heartbeat is its own timer (executors/kiro.js:393-394): it keeps going while the
// repair waits for response headers and for a refused repair's body. The recorder is not safe
// for concurrent writes, so -race fails these if a heartbeat is written beside another write.
type delayedBody struct {
	io.Reader
	delay time.Duration
	once  sync.Once
}

func (d *delayedBody) Read(p []byte) (int, error) {
	d.once.Do(func() { time.Sleep(d.delay) })
	return d.Reader.Read(p)
}
func (d *delayedBody) Close() error { return nil }

func TestKiroHeartbeatContinuesWhileTheRepairHeadersAreDelayed(t *testing.T) {
	previous := kiroHeartbeatEvery
	kiroHeartbeatEvery = 20 * time.Millisecond
	t.Cleanup(func() { kiroHeartbeatEvery = previous })
	for _, client := range clientWires {
		t.Run(client, func(t *testing.T) {
			s, _ := kiroTransport(t, routing.ProviderSettings{}, func(n int, _ string, _ string) (int, io.ReadCloser, error) {
				if n == 1 {
					return 200, io.NopCloser(strings.NewReader(string(kiroEllipsisTurn))), nil
				}
				time.Sleep(200 * time.Millisecond) // the repair's response headers are late
				return 200, io.NopCloser(strings.NewReader(string(kiroGoodTurn))), nil
			})
			w := s.call(client, true)
			if !strings.Contains(w.Body.String(), "pong") {
				t.Fatalf("%d %s", w.Code, w.Body)
			}
			if beats := strings.Count(w.Body.String(), ": kiro-validation"); beats < 6 {
				t.Fatalf("%d heartbeats over a 200 ms header wait at 20 ms", beats)
			}
		})
	}
}

func TestKiroHeartbeatContinuesWhileARefusedRepairBodyIsRead(t *testing.T) {
	previous := kiroHeartbeatEvery
	kiroHeartbeatEvery = 20 * time.Millisecond
	t.Cleanup(func() { kiroHeartbeatEvery = previous })
	stubRepairSleep(t)
	s, _ := kiroTransport(t, routing.ProviderSettings{}, func(n int, _ string, _ string) (int, io.ReadCloser, error) {
		if n == 1 {
			return 200, io.NopCloser(strings.NewReader(string(kiroEllipsisTurn))), nil
		}
		return 401, &delayedBody{Reader: strings.NewReader("unauthorized"), delay: 200 * time.Millisecond}, nil
	})
	w := s.call(openai.ChatProtocol, true)
	body := w.Body.String()
	if !strings.Contains(body, "kiro_integrity_retry_upstream_error") || !strings.Contains(body, "unauthorized") {
		t.Fatalf("%d %.300s", w.Code, body)
	}
	if beats := strings.Count(body, ": kiro-validation"); beats < 6 {
		t.Fatalf("%d heartbeats over a 200 ms body wait at 20 ms", beats)
	}
}

// Clean EOF with output and no stop event, through every client wire (executors/kiro.js:1008-1075).
func TestKiroServesACleanEOFWithoutAStopEvent(t *testing.T) {
	turn := kiroAnswer(kiroFrame("assistantResponseEvent", `{"content":"Complete answer."}`), kiroFrame("meteringEvent", `{"usage":2,"unit":"credit"}`),
		kiroFrame("contextUsageEvent", `{"contextUsagePercentage":10}`))
	for _, client := range clientWires {
		for _, stream := range []bool{false, true} {
			s, seen := kiroScripted(t, routing.ProviderSettings{}, answer(turn), answer(kiroGoodTurn))
			w := s.call(client, stream)
			if w.Code != 200 || !strings.Contains(w.Body.String(), "Complete answer.") || strings.Contains(w.Body.String(), "kiro_") || len(seen.calls()) != 1 {
				t.Errorf("%s stream=%v: %d, %d requests: %.300s", client, stream, w.Code, len(seen.calls()), w.Body)
			}
		}
	}
}
