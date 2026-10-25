package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// Fixtures are shaped like the pin's own answers (tests/unit/kiro-terminal-integrity.test.js):
// an assistantResponseEvent / toolUseEvent stream closed by a messageStopEvent.

func good() []byte { return join(text("all done, the check passed"), stop("end_turn")) }

type body struct {
	io.Reader
	closed atomic.Bool
}

func (b *body) Close() error { b.closed.Store(true); return nil }

func bodyOf(data []byte) *body { return &body{Reader: bytes.NewReader(data)} }

// retries scripts the repair attempts and records what it was asked for.
type retries struct {
	mu     sync.Mutex
	kinds  []string
	answer func(call int) (io.ReadCloser, int, error)
}

func (r *retries) retry(_ context.Context, kind string) (io.ReadCloser, int, error) {
	r.mu.Lock()
	r.kinds = append(r.kinds, kind)
	call := len(r.kinds)
	r.mu.Unlock()
	return r.answer(call)
}

func answers(data ...[]byte) *retries {
	return &retries{answer: func(call int) (io.ReadCloser, int, error) {
		if call > len(data) {
			return nil, 0, errors.New("unscripted retry")
		}
		return bodyOf(data[call-1]), 200, nil
	}}
}

func runGate(t *testing.T, first []byte, config GateConfig, r *retries) (string, error) {
	t.Helper()
	var retry Retry
	if r != nil {
		retry = r.retry
	}
	out, err := Gate(context.Background(), bodyOf(first), config, ResponseOptions{Model: "m"}, retry, nil)
	if err != nil {
		return "", err
	}
	raw, _ := io.ReadAll(out)
	return string(raw), nil
}

func failureCode(t *testing.T, err error) string {
	t.Helper()
	var failure *concerns.StreamFailure
	if !errors.As(err, &failure) {
		t.Fatalf("want a stream failure, got %v", err)
	}
	return concerns.Text(failure.Value["code"])
}

func TestGateReleasesACompleteAnswerWithoutRetrying(t *testing.T) {
	r := answers()
	got, err := runGate(t, good(), GateConfig{Repair: true}, r)
	if err != nil || got != string(good()) || len(r.kinds) != 0 {
		t.Fatalf("got %v, retries %v, err %v", got == string(good()), r.kinds, err)
	}
}

// executors/kiro.js:617-624: an answer that is only an ellipsis, or only announces
// the next action, is repaired once.
func TestGateRepairsEllipsisAndShortFinalOnce(t *testing.T) {
	cases := []struct {
		name, kind string
		bad        []byte
	}{
		{"ascii ellipsis", "ellipsis", join(text("..."), stop("end_turn"))},
		{"unicode ellipsis", "ellipsis", join(text(" … "), stop("end_turn"))},
		{"reasoning only ellipsis", "ellipsis", join(event("reasoningContentEvent", `{"text":"..."}`), stop("end_turn"))},
		{"announced action", "short_final", join(text("I'll verify the deployment now"), stop("end_turn"))},
		{"announced action, Chinese", "short_final", join(text("接下來我會檢查部署狀態"), stop("end_turn"))},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := answers(good())
			got, err := runGate(t, c.bad, GateConfig{Repair: true}, r)
			if err != nil || got != string(good()) {
				t.Fatalf("answer %q, err %v", got, err)
			}
			if len(r.kinds) != 1 || r.kinds[0] != c.kind {
				t.Fatalf("retries %v, want one %q", r.kinds, c.kind)
			}
		})
	}
}

// The retry is one retry, not a loop (executors/kiro.js:483-504).
func TestGateRetriesExactlyOnce(t *testing.T) {
	cases := []struct {
		name string
		bad  []byte
		code string
	}{
		{"ellipsis", join(text("..."), stop("end_turn")), "kiro_ellipsis_retry_failed"},
		{"short final", join(text("Let me check the logs"), stop("end_turn")), "kiro_short_final_retry_failed"},
		{"malformed tool", join(tool("t1", "tool_call", `{"arguments":{}}`), stop("tool_use")), "kiro_tool_call_repair_retry_failed"},
		{"cut stream", join(text("partial")), "kiro_missing_terminal_retry_failed"},
		{"malformed model output", join(text("x"), stop("malformed_model_output")), "kiro_missing_terminal_retry_failed"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := answers(c.bad, c.bad, c.bad)
			_, err := runGate(t, c.bad, GateConfig{Repair: true}, r)
			if code := failureCode(t, err); code != c.code {
				t.Fatalf("code %s, want %s", code, c.code)
			}
			if len(r.kinds) != 1 {
				t.Fatalf("%d retries, want exactly 1", len(r.kinds))
			}
		})
	}
}

// executors/kiro.js:376-377, :448-450: the switch governs the malformed tool call
// only; an ellipsis is still repaired with it off.
func TestGateRepairSwitch(t *testing.T) {
	badTool := join(tool("t1", "tool_call", `{"arguments":{}}`), stop("tool_use"))
	off := answers(good())
	_, err := runGate(t, badTool, GateConfig{Repair: false}, off)
	if code := failureCode(t, err); code != "invalid_kiro_tool_call" || len(off.kinds) != 0 {
		t.Fatalf("switch off: code %s, retries %v", code, off.kinds)
	}
	on := answers(good())
	if got, err := runGate(t, badTool, GateConfig{Repair: true}, on); err != nil || got != string(good()) || len(on.kinds) != 1 || on.kinds[0] != "tool" {
		t.Fatalf("switch on: %v %v", err, on.kinds)
	}
	ellipsis := answers(good())
	if _, err := runGate(t, join(text("..."), stop("end_turn")), GateConfig{Repair: false}, ellipsis); err != nil || len(ellipsis.kinds) != 1 {
		t.Fatalf("switch off, ellipsis: %v %v", err, ellipsis.kinds)
	}
}

// A cut or unterminated answer is repeated unchanged (:455-457, :526-540).
func TestGateRepeatsACutAnswerWithoutAnInstruction(t *testing.T) {
	r := answers(good())
	got, err := runGate(t, join(text("partial")), GateConfig{Repair: true}, r)
	if err != nil || got != string(good()) || len(r.kinds) != 1 || r.kinds[0] != "" {
		t.Fatalf("%v %v", err, r.kinds)
	}
}

// executors/kiro.js:445-447: a final stop is reported, never retried; the response
// leg reports it from the same bytes.
func TestGatePassesFinalFailuresThroughUnretried(t *testing.T) {
	cases := map[string][]byte{
		"refusal":                join(text("no"), stop("refusal")),
		"cancelled":              join(text("no"), stop("cancelled")),
		"unknown stop":           join(text("no"), stop("weird")),
		"upstream eventstream":   rawFrame(append(header(":message-type", 7, "exception"), header(":event-type", 7, "x")...), `{"message":"boom"}`),
		"tool buffer exceeded":   join(tool("t1", "a", `"`+strings.Repeat("x", 3000)+`"`), stop("tool_use")),
		"event the leg refuses":  join(event("surpriseEvent", `{"a":1}`), stop("end_turn")),
		"content after the stop": join(stop("end_turn"), text("late")),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			r := answers(good())
			got, err := runGate(t, raw, GateConfig{Repair: true, MaxBytes: 4096}, r)
			if err != nil || got != string(raw) || len(r.kinds) != 0 {
				t.Fatalf("passed through %v, err %v, retries %v", got == string(raw), err, r.kinds)
			}
		})
	}
}

// executors/kiro.js:570-577: the buffer is bounded and over it the answer is an error.
func TestGateBufferIsBounded(t *testing.T) {
	big := join(text(strings.Repeat("a", 2000)), stop("end_turn"))
	src := bodyOf(big)
	r := answers(good())
	_, err := Gate(context.Background(), src, GateConfig{Repair: true, MaxBytes: 1024}, ResponseOptions{}, r.retry, nil)
	if code := failureCode(t, err); code != "kiro_integrity_buffer_exceeded" || len(r.kinds) != 0 {
		t.Fatalf("code %s, retries %v", code, r.kinds)
	}
	if !src.closed.Load() {
		t.Fatal("the over-bound answer was not closed")
	}
}

func TestGateBufferBoundHoldsOnAnEndlessBody(t *testing.T) {
	endless := &body{Reader: iotestRepeat{}}
	before := runtime.NumGoroutine()
	_, err := Gate(context.Background(), endless, GateConfig{MaxBytes: 64 << 10}, ResponseOptions{}, nil, nil)
	if code := failureCode(t, err); code != "kiro_integrity_buffer_exceeded" {
		t.Fatalf("code %s", code)
	}
	settle(t, before)
}

type iotestRepeat struct{}

func (iotestRepeat) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 'x'
	}
	return len(p), nil
}

// executors/kiro.js:464-481: a refused retry is reported with its status and the
// start of its body.
func TestGateReportsAFailedRetry(t *testing.T) {
	r := &retries{answer: func(int) (io.ReadCloser, int, error) {
		return bodyOf([]byte(strings.Repeat("e", 6000))), 503, nil
	}}
	_, err := runGate(t, join(text("...")), GateConfig{Repair: true}, r)
	var failure *concerns.StreamFailure
	if !errors.As(err, &failure) || concerns.Text(failure.Value["code"]) != "kiro_integrity_retry_upstream_error" {
		t.Fatalf("%v", err)
	}
	if message := concerns.Text(failure.Value["message"]); len(message) != 4096 {
		t.Fatalf("message is %d bytes, want the 4096 prefix", len(message))
	}
	if details, _ := failure.Value["details"].(map[string]any); details["status"] != 503 {
		t.Fatalf("details %v", failure.Value["details"])
	}
	transport := &retries{answer: func(int) (io.ReadCloser, int, error) { return nil, 0, errors.New("dial refused") }}
	_, err = runGate(t, join(text("...")), GateConfig{Repair: true}, transport)
	if code := failureCode(t, err); code != "kiro_integrity_retry_upstream_error" {
		t.Fatalf("code %s", code)
	}
}

// settle waits for goroutines started by the gate to end.
func settle(t *testing.T, before int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if after := runtime.NumGoroutine(); after > before {
		t.Fatalf("%d goroutines before, %d after", before, after)
	}
}

// hang never answers until it is closed.
type hang struct {
	once   sync.Once
	closed chan struct{}
	got    atomic.Bool
}

func newHang() *hang { return &hang{closed: make(chan struct{})} }
func (h *hang) Read([]byte) (int, error) {
	<-h.closed
	return 0, io.ErrClosedPipe
}
func (h *hang) Close() error { h.once.Do(func() { close(h.closed) }); h.got.Store(true); return nil }

// executors/kiro.js:559-566: both timeouts end the wait; a timeout is a missing
// terminal and so is repaired once.
func TestGateTimeoutsAreMissingTerminals(t *testing.T) {
	before := runtime.NumGoroutine()
	stalled := newHang()
	r := answers(good())
	out, err := Gate(context.Background(), stalled, GateConfig{Repair: true, TTFT: 20 * time.Millisecond, Stall: 20 * time.Millisecond}, ResponseOptions{}, r.retry, nil)
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := io.ReadAll(out); string(raw) != string(good()) || len(r.kinds) != 1 || r.kinds[0] != "" || !stalled.got.Load() {
		t.Fatalf("retries %v, closed %v", r.kinds, stalled.got.Load())
	}
	// The second attempt stalling too is the failure.
	r = &retries{answer: func(int) (io.ReadCloser, int, error) { return newHang(), 200, nil }}
	_, err = Gate(context.Background(), newHang(), GateConfig{Repair: true, TTFT: 20 * time.Millisecond, Stall: 20 * time.Millisecond}, ResponseOptions{}, r.retry, nil)
	if code := failureCode(t, err); code != "kiro_missing_terminal_retry_failed" || len(r.kinds) != 1 {
		t.Fatalf("code %s, retries %v", code, r.kinds)
	}
	settle(t, before)
}

// The first chunk gets the TTFT budget and later chunks the stall budget.
func TestGateUsesTheTwoTimeoutsSeparately(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		pw.Write(text("a"))
		time.Sleep(60 * time.Millisecond) // longer than TTFT, shorter than Stall
		pw.Write(join(text("b"), stop("end_turn")))
		pw.Close()
	}()
	r := answers()
	out, err := Gate(context.Background(), pr, GateConfig{TTFT: 20 * time.Millisecond, Stall: 2 * time.Second}, ResponseOptions{}, r.retry, nil)
	if err != nil || len(r.kinds) != 0 {
		t.Fatalf("err %v retries %v", err, r.kinds)
	}
	if raw, _ := io.ReadAll(out); !bytes.Contains(raw, []byte(`"b"`)) {
		t.Fatalf("%q", raw)
	}
}

// Cancellation reaches both attempts and leaves nothing behind (executors/kiro.js:378-379, :423-427).
func TestGateCancellationLeavesNoGoroutine(t *testing.T) {
	for _, phase := range []string{"initial", "retry"} {
		t.Run(phase, func(t *testing.T) {
			before := runtime.NumGoroutine()
			ctx, cancel := context.WithCancel(context.Background())
			first, second := newHang(), newHang()
			var src io.ReadCloser = first
			r := &retries{answer: func(int) (io.ReadCloser, int, error) { return second, 200, nil }}
			if phase == "retry" {
				src = bodyOf(join(text("...")))
			}
			done := make(chan error, 1)
			go func() {
				_, err := Gate(ctx, src, GateConfig{Repair: true}, ResponseOptions{}, r.retry, nil)
				done <- err
			}()
			time.Sleep(30 * time.Millisecond)
			cancel()
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatalf("err %v", err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("the gate outlived its context")
			}
			if phase == "initial" && !first.got.Load() || phase == "retry" && !second.got.Load() {
				t.Fatal("the upstream body was left open")
			}
			settle(t, before)
		})
	}
}

// executors/kiro.js:385-394: a heartbeat at once, then every interval, until released.
func TestGateHeartbeat(t *testing.T) {
	pr, pw := io.Pipe()
	var beats atomic.Int32
	go func() {
		time.Sleep(120 * time.Millisecond)
		pw.Write(good())
		pw.Close()
	}()
	if _, err := Gate(context.Background(), pr, GateConfig{HeartbeatEvery: 20 * time.Millisecond}, ResponseOptions{}, nil, func() { beats.Add(1) }); err != nil {
		t.Fatal(err)
	}
	if n := beats.Load(); n < 4 {
		t.Fatalf("%d heartbeats while held", n)
	}
	settledAt := beats.Load()
	time.Sleep(80 * time.Millisecond)
	if beats.Load() != settledAt {
		t.Fatal("heartbeat kept running after the answer was released")
	}
}

// executors/kiro.js:134-143.
func TestRepairRequestAppendsToTheCurrentUserTurn(t *testing.T) {
	original := `{"conversationState":{"conversationId":"c","currentMessage":{"userInputMessage":{"content":"hello","modelId":"m","origin":"AI_EDITOR"}},"history":[]},"profileArn":"arn","inferenceConfig":{"maxTokens":5}}`
	for kind, instruction := range repairInstructions {
		repaired, err := RepairRequest([]byte(original), kind)
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := json.Unmarshal(repaired, &doc); err != nil {
			t.Fatal(err)
		}
		if _, set := doc["systemPrompt"]; set {
			t.Fatal("the instruction went into a top-level systemPrompt, which kiro.dev rejects")
		}
		message := doc["conversationState"].(map[string]any)["currentMessage"].(map[string]any)["userInputMessage"].(map[string]any)
		if message["content"] != "hello\n\n"+instruction || message["modelId"] != "m" || message["origin"] != "AI_EDITOR" {
			t.Fatalf("%s: %v", kind, message)
		}
		if doc["profileArn"] != "arn" || doc["inferenceConfig"].(map[string]any)["maxTokens"] != float64(5) {
			t.Fatalf("%s: other fields changed: %s", kind, repaired)
		}
	}
	if same, err := RepairRequest([]byte(original), ""); err != nil || string(same) != original {
		t.Fatalf("an empty kind must repeat the body: %v %s", err, same)
	}
	empty, _ := RepairRequest([]byte(`{"conversationState":{"currentMessage":{"userInputMessage":{"content":""}}}}`), "ellipsis")
	if !strings.Contains(string(empty), `"content":"Retry the previous response because it ended with only an ellipsis.`) {
		t.Fatalf("empty content: %s", empty)
	}
	if _, err := RepairRequest([]byte(original), "nonsense"); err == nil {
		t.Fatal("an unknown repair kind was accepted")
	}
}

// The heuristics of executors/kiro.js:185-197, over the cases the pin's own tests name.
func TestShortFutureAction(t *testing.T) {
	yes := []string{
		"I'll verify the deployment now",
		"Let me check the logs",
		"Now I will investigate the failing test",
		"I’ll confirm this next",
		"接下來我會檢查部署",
		"我會再確認一次",
		"現在我只補查 504 log",
		"目前證據顯示 504 主要來自上游逾時，並非應用程式錯誤。最後補查 504 access log，確認 host／路徑與是否為集中流量。",
	}
	no := []string{
		"",
		"All checks passed.",
		"I'll verify it after you approve the change",
		"Let me check: the status is 200",
		"Let me check. The status is 200 and nothing failed.",
		"I will verify this and the verification is complete",
		"I need to test it, found no errors",
		"我會確認。部署狀態是正常的",
		"請你先確認授權，我會再檢查",
		strings.Repeat("I'll verify ", 100),
	}
	for _, value := range yes {
		if !isShortFutureAction(value) {
			t.Errorf("%q should be an announce-only final", value)
		}
	}
	for _, value := range no {
		if isShortFutureAction(value) {
			t.Errorf("%q must not be repaired", value)
		}
	}
	for _, value := range []string{"...", "…", " … ", " ...\n"} {
		if !isEllipsisOnly(value) {
			t.Errorf("%q is an ellipsis", value)
		}
	}
	for _, value := range []string{"", "..", "....", "ok...", "…!"} {
		if isEllipsisOnly(value) {
			t.Errorf("%q is not only an ellipsis", value)
		}
	}
}

// A tool call is an answer: an ellipsis alongside it is not repaired (:617).
func TestGateKeepsAToolTurnWhoseTextIsAnEllipsis(t *testing.T) {
	raw := join(text("..."), tool("t1", "lookup", `{"q":1}`), stop("tool_use"))
	r := answers(good())
	got, err := runGate(t, raw, GateConfig{Repair: true}, r)
	if err != nil || got != string(raw) || len(r.kinds) != 0 {
		t.Fatalf("%v %v", err, r.kinds)
	}
}
