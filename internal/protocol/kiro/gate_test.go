package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		{"cut stream", join(text("partial"), text("more")[:20]), "kiro_missing_terminal_retry_failed"},
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

// A cut or empty answer is repeated unchanged (:455-457, :526-540).
func TestGateRepeatsACutAnswerWithoutAnInstruction(t *testing.T) {
	for name, raw := range map[string][]byte{"cut frame": join(text("partial"), text("more")[:20]), "empty": nil} {
		r := answers(good())
		got, err := runGate(t, raw, GateConfig{Repair: true}, r)
		if err != nil || got != string(good()) || len(r.kinds) != 1 || r.kinds[0] != "" {
			t.Fatalf("%s: %v %v", name, err, r.kinds)
		}
	}
}

// executors/kiro.js:1008-1075, kiro-terminal-integrity.test.js:169: output at a clean EOF
// without a stop event is a complete answer, served on the first attempt.
func TestGateAcceptsCleanEOFWithoutAStopEvent(t *testing.T) {
	raw := join(text("Complete answer."), event("meteringEvent", `{"usage":2,"unit":"credit"}`), event("contextUsageEvent", `{"contextUsagePercentage":10}`))
	r := answers(good())
	got, err := runGate(t, raw, GateConfig{Repair: true}, r)
	if err != nil || got != string(raw) || len(r.kinds) != 0 {
		t.Fatalf("err %v retries %v", err, r.kinds)
	}
	// A stop with no output is an explicit completion (test "accepts messageStop without semantic output").
	r = answers(good())
	if got, err := runGate(t, join(stop("end_turn")), GateConfig{Repair: true}, r); err != nil || got == "" || len(r.kinds) != 0 {
		t.Fatalf("err %v retries %v", err, r.kinds)
	}
	// ... but frames that carry nothing are not an answer.
	r = answers(good())
	if got, err := runGate(t, join(event("meteringEvent", `{"usage":2}`)), GateConfig{Repair: true}, r); err != nil || got != string(good()) || len(r.kinds) != 1 {
		t.Fatalf("err %v retries %v", err, r.kinds)
	}
}

// executors/kiro.js:445-447, :507-519: a final stop is reported with the pin's code
// and diagnostics, never retried (kiro-terminal-integrity.test.js "fails closed").
func TestGateReportsFinalFailuresWithPinCodesAndDiagnostics(t *testing.T) {
	exception := rawFrame(append(header(":message-type", 7, "exception"), header(":event-type", 7, "x")...), `{"message":"upstream failed"}`)
	cases := []struct {
		name, code, provenance, disposition, stop string
		raw                                       []byte
	}{
		{"refusal", "kiro_terminal_refusal", "message_stop_event", "terminal_refusal", "refusal", join(text("no"), stop("refusal"))},
		{"content filtered", "kiro_terminal_refusal", "metadata_stop_reason", "terminal_refusal", "content_filtered", join(text("no"), event("metadataEvent", `{"stopReason":"content_filtered"}`))},
		{"cancelled", "kiro_terminal_incomplete", "message_stop_event", "terminal_incomplete", "cancelled", join(text("no"), stop("cancelled"))},
		{"pause_turn", "kiro_terminal_incomplete", "message_stop_event", "terminal_incomplete", "pause_turn", join(text("no"), stop("pause_turn"))},
		{"novel reason", "kiro_unknown_stop_reason", "message_stop_event", "unknown_failure", "novel_reason", join(text("no"), stop("novel_reason"))},
		{"max_tokens with only a call", "kiro_terminal_incomplete", "message_stop_event", "terminal_incomplete", "max_tokens", join(tool("t", "read_file", `{"path":"a"}`), stop("max_tokens"))},
		{"upstream exception", "kiro_upstream_eventstream_error", "upstream_eventstream_error", "terminal_incomplete", "", join(text("private"), exception)},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := answers(good())
			_, err := runGate(t, c.raw, GateConfig{Repair: true}, r)
			value := failureValue(t, err)
			details, _ := value["details"].(map[string]any)
			if value["code"] != c.code || value["type"] != "upstream_error" || len(r.kinds) != 0 {
				t.Fatalf("%v, retries %v", value, r.kinds)
			}
			if details["terminal_provenance"] != c.provenance || details["stop_disposition"] != c.disposition || details["attempt"] != "initial" {
				t.Fatalf("details %v", details)
			}
			if c.stop != "" && details["stop_reason"] != c.stop {
				t.Fatalf("stop_reason %v", details["stop_reason"])
			}
			for _, field := range []string{"transport_state", "response_state", "event_counts", "incomplete_frame_bytes"} {
				if _, set := details[field]; !set {
					t.Fatalf("diagnostics lack %s: %v", field, details)
				}
			}
			if strings.Contains(fmt.Sprint(value), "private") {
				t.Fatal("the held output leaked into the failure")
			}
		})
	}
}

// What Routeweft used to refuse, the pin serves, so the gate releases it on the first attempt.
func TestGateServesWhatThePinServes(t *testing.T) {
	for name, raw := range map[string][]byte{
		"event the pin has no branch for": join(event("surpriseEvent", `{"a":1}`), text("answer"), stop("end_turn")),
		"content after the stop":          join(text("a"), stop("end_turn"), text("late")),
		"unread key":                      join(event("assistantResponseEvent", `{"content":"a","extra":"y"}`), stop("end_turn")),
	} {
		r := answers(good())
		got, err := runGate(t, raw, GateConfig{Repair: true}, r)
		if err != nil || got != string(raw) || len(r.kinds) != 0 {
			t.Errorf("%s: err %v retries %v", name, err, r.kinds)
		}
	}
	// A payload that is not JSON is a corrupt frame: repaired once.
	r := answers(good())
	if got, err := runGate(t, join(text("a"), event("surpriseEvent", `{oops`)), GateConfig{Repair: true}, r); err != nil || got != string(good()) || len(r.kinds) != 1 || r.kinds[0] != "" {
		t.Errorf("err %v retries %v", err, r.kinds)
	}
}

func failureValue(t *testing.T, err error) map[string]any {
	t.Helper()
	var failure *concerns.StreamFailure
	if !errors.As(err, &failure) {
		t.Fatalf("want a stream failure, got %v", err)
	}
	return failure.Value
}

// executors/kiro.js:570-577, kiro-terminal-integrity.test.js "fails safely when the
// private gate exceeds its configured bound": the bound is on the converted output.
func TestGateBufferIsBoundedOnConvertedOutput(t *testing.T) {
	big := join(text(strings.Repeat("a", 2000)), stop("end_turn"))
	src := bodyOf(big)
	r := answers(good())
	_, err := Gate(context.Background(), src, GateConfig{Repair: true, MaxBytes: 1024}, ResponseOptions{}, r.retry, nil)
	value := failureValue(t, err)
	if value["code"] != "kiro_integrity_buffer_exceeded" || value["message"] != "Kiro integrity buffer exceeded 1024 bytes" || len(r.kinds) != 0 {
		t.Fatalf("%v, retries %v", value, r.kinds)
	}
	if details, _ := value["details"].(map[string]any); details["terminal_provenance"] != "integrity_buffer_exceeded" {
		t.Fatalf("details %v", value["details"])
	}
	if !src.closed.Load() {
		t.Fatal("the over-bound answer was not closed")
	}
}

// The unit of the bound is the Chat SSE the pin buffers: the exact byte count of a
// known answer is the exact threshold. The expected sizes are written out from the
// pin's chunk shape (executors/kiro.js:677-689, :1121-1122), not computed by the code.
func TestGateBoundIsTheConvertedByteCount(t *testing.T) {
	const envelope = `"id":"chatcmpl-1700000000000","object":"chat.completion.chunk","created":1700000000,"model":"m"`
	first := `data: {` + envelope + `,"choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}` + "\n\n"
	finish := `data: {` + envelope + `,"choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}` + "\n\n"
	exact := len(first) + len(finish) + len("data: [DONE]\n\n")
	raw := join(text("hi"), stop("end_turn"))
	for limit, want := range map[int]bool{exact: true, exact + 1: true, exact - 1: false} {
		_, err := Gate(context.Background(), bodyOf(raw), GateConfig{MaxBytes: limit}, ResponseOptions{Model: "m"}, nil, nil)
		if (err == nil) != want {
			t.Errorf("limit %d (converted size %d): err %v", limit, exact, err)
		}
	}
	// Frames that convert to nothing are not output: a megabyte of metering fits a small bound,
	// beyond the one keep-alive comment the pin writes per read (:1162-1164).
	metering := bytes.Repeat(event("meteringEvent", `{"usage":1,"unit":"credit"}`), 20000)
	if len(metering) < 1<<20 {
		t.Fatalf("fixture is %d bytes", len(metering))
	}
	chunky := &body{Reader: io.MultiReader(bytes.NewReader(metering), bytes.NewReader(raw))}
	if _, err := Gate(context.Background(), chunky, GateConfig{MaxBytes: 4096}, ResponseOptions{Model: "m"}, nil, nil); err != nil {
		t.Fatalf("non-output frames were counted against the bound: %v", err)
	}
	// ... while the same count of converted bytes is not.
	many := bytes.Repeat(text("x"), 200)
	if _, err := Gate(context.Background(), bodyOf(join(many, stop("end_turn"))), GateConfig{MaxBytes: 4096}, ResponseOptions{Model: "m"}, nil, nil); err == nil {
		t.Fatal("converted output over the bound was accepted")
	}
}

// Tool input is held against half the bound (:546, :701-706, "counts deferred tool fragments").
func TestGateCountsToolFragmentsAgainstHalfTheBound(t *testing.T) {
	raw := join(tool("large-tool", "read_file", `{"path":"`+strings.Repeat("x", 200)+`"}`), stop("tool_use"))
	r := answers(good())
	_, err := runGate(t, raw, GateConfig{Repair: true, MaxBytes: 128}, r)
	value := failureValue(t, err)
	if value["code"] != "kiro_integrity_buffer_exceeded" || value["message"] != "Kiro buffered tool input exceeded the integrity memory bound" || len(r.kinds) != 0 {
		t.Fatalf("%v retries %v", value, r.kinds)
	}
	if details, _ := value["details"].(map[string]any); details["terminal_provenance"] != "integrity_buffer_exceeded" || details["stop_disposition"] != "terminal_incomplete" {
		t.Fatalf("details %v", value["details"])
	}
}

func TestGateBufferBoundHoldsOnAnEndlessBody(t *testing.T) {
	endless := &body{Reader: iotestRepeat{}}
	before := runtime.NumGoroutine()
	_, err := Gate(context.Background(), endless, GateConfig{MaxBytes: 64 << 10}, ResponseOptions{}, nil, nil)
	if err == nil {
		t.Fatal("an endless body was accepted")
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
	// A repair that throws is the gate's own failure with the error's message (:407-416).
	transport := &retries{answer: func(int) (io.ReadCloser, int, error) { return nil, 0, errors.New("dial refused") }}
	_, err = runGate(t, join(text("...")), GateConfig{Repair: true}, transport)
	if value := failureValue(t, err); value["code"] != "kiro_integrity_gate_failed" || value["message"] != "dial refused" || value["type"] != "upstream_error" {
		t.Fatalf("%v", value)
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

// kiro-usage-and-tool-integrity.test.js "C": the valid call is shipped and only the
// invalid one is dropped; the gate must not turn that into a repair retry.
func TestGateKeepsTheValidCallWhenOnlyOneIsInvalid(t *testing.T) {
	bad := tool("bad", "tool_call", `{"arguments":{"q":"router"}}`)
	good := tool("good", "tool_call", `{"name":"mcp_search","arguments":{"q":"router"}}`)
	raw := join(good, bad, stop("tool_use"))
	r := answers(join(text("never used")))
	out, err := runGate(t, raw, GateConfig{Repair: true}, r)
	if err != nil || out != string(raw) || len(r.kinds) != 0 {
		t.Fatalf("err %v retries %v", err, r.kinds)
	}
	// ... and the held bytes convert to the one good call, not to a failure.
	converted, err := KiroResponseToChat([]byte(out), ResponseOptions{})
	if err != nil || !strings.Contains(string(converted), `mcp_search`) || strings.Contains(string(converted), `"bad"`) {
		t.Fatalf("%v %s", err, converted)
	}
	// Text with only an unusable call keeps the text (test "keeps streamed text when the only tool call is unusable").
	kept := join(text("Here is what I found."), bad, stop("tool_use"))
	r = answers(good)
	if out, err := runGate(t, kept, GateConfig{Repair: true}, r); err != nil || out != string(kept) || len(r.kinds) != 0 {
		t.Fatalf("err %v retries %v", err, r.kinds)
	}
}

// kiro-terminal-integrity.test.js: an authoritative stop outranks a malformed deferred
// tool (finish() judges the stop before it validates tools), and a rejected fragment
// is skipped, not fatal.
func TestGateJudgesTheStopBeforeTheTools(t *testing.T) {
	for _, stopReason := range []string{"content_filtered", "cancelled", "max_tokens"} {
		r := answers(good())
		_, err := runGate(t, join(event("toolUseEvent", `{"toolUseId":"bad-tool","name":"read_file"}`), event("metadataEvent", `{"stopReason":"`+stopReason+`"}`)), GateConfig{Repair: true}, r)
		want := "kiro_terminal_incomplete"
		if stopReason == "content_filtered" {
			want = "kiro_terminal_refusal"
		}
		if code := failureCode(t, err); code != want || len(r.kinds) != 0 {
			t.Errorf("%s: code %s retries %v", stopReason, code, r.kinds)
		}
	}
	for stopReason, frames := range map[string][]byte{
		"content_filtered": event("toolUseEvent", `{"toolUseId":123,"name":"read_file","input":{"path":"bad.txt"}}`),
		"cancelled":        event("toolUseEvent", `{"toolUseId":"missing-name","input":{"path":"bad.txt"}}`),
		"max_tokens":       join(event("toolUseEvent", `{"toolUseId":"changing","name":"read_file"}`), event("toolUseEvent", `{"toolUseId":"changing","name":"write_file"}`)),
	} {
		r := answers(good())
		_, err := runGate(t, join(frames, event("metadataEvent", `{"stopReason":"`+stopReason+`"}`)), GateConfig{Repair: true}, r)
		if code := failureCode(t, err); strings.Contains(code, "repair_retry_failed") || len(r.kinds) != 0 {
			t.Errorf("%s: code %s retries %v", stopReason, code, r.kinds)
		}
	}
	// A non-string toolUseId on its own is repaired.
	r := answers(join(tool("valid-tool-id", "read_file", `{"path":"safe.txt"}`), stop("tool_use")))
	if _, err := runGate(t, join(event("toolUseEvent", `{"toolUseId":123,"name":"read_file","input":{"path":"bad.txt"}}`), stop("tool_use")), GateConfig{Repair: true}, r); err != nil || len(r.kinds) != 1 || r.kinds[0] != "tool" {
		t.Fatalf("err %v retries %v", err, r.kinds)
	}
}

// kiro-terminal-integrity.test.js "retries %s and releases only the valid attempt".
func TestGateRetriesCorruptAndCutAnswers(t *testing.T) {
	frame := func() []byte { return append([]byte(nil), text("corrupt")...) }
	cases := map[string][]byte{
		"message CRC": func() []byte { f := frame(); f[len(f)-1] ^= 0xff; return f }(),
		"prelude CRC": func() []byte { f := frame(); f[8] ^= 0xff; return f }(),
		"truncated":   frame()[:len(frame())-3],
		"duplicate headers": rawFrame(append(header(":event-type", 7, "assistantResponseEvent"), header(":event-type", 7, "metadataEvent")...),
			`{"content":"duplicate"}`),
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			r := answers(good())
			got, err := runGate(t, join(text("must stay private"), bad), GateConfig{Repair: true}, r)
			if err != nil || got != string(good()) || len(r.kinds) != 1 || r.kinds[0] != "" {
				t.Fatalf("err %v retries %v", err, r.kinds)
			}
		})
	}
	// A body that fails mid-read is retried once too ("retries a response-body read failure once").
	broken := &body{Reader: io.MultiReader(bytes.NewReader(text("partial")), errReader{errors.New("socket reset")})}
	r := answers(good())
	out, err := Gate(context.Background(), broken, GateConfig{Repair: true}, ResponseOptions{}, r.retry, nil)
	if raw, _ := io.ReadAll(out); err != nil || string(raw) != string(good()) || len(r.kinds) != 1 {
		t.Fatalf("err %v retries %v", err, r.kinds)
	}
}

type errReader struct{ err error }

func (e errReader) Read([]byte) (int, error) { return 0, e.err }

// "reports corrupt-frame provenance when the bounded retry also fails" and the
// attempts the pin lists (:500-504, :586-595).
func TestGateReportsBothAttemptsDiagnostics(t *testing.T) {
	corrupt := func() []byte { f := append([]byte(nil), text("corrupt")...); f[len(f)-1] ^= 0xff; return f }
	r := answers(corrupt())
	_, err := runGate(t, corrupt(), GateConfig{Repair: true}, r)
	value := failureValue(t, err)
	details, _ := value["details"].(map[string]any)
	attempts, _ := details["attempts"].([]any)
	if value["code"] != "kiro_missing_terminal_retry_failed" || len(attempts) != 2 {
		t.Fatalf("%v", value)
	}
	for i, label := range []string{"initial", "retry"} {
		d, _ := attempts[i].(map[string]any)
		if d["attempt"] != label || d["terminal_provenance"] != "corrupt_eventstream_frame" || d["transport_state"] != "corrupt_frame" || d["stop_disposition"] != "terminal_incomplete" {
			t.Fatalf("attempt %d: %v", i, d)
		}
	}
	// A frame cut off mid-way reports how many of its bytes arrived.
	cut := text("truncated")[:20]
	_, err = runGate(t, cut, GateConfig{Repair: true}, answers(cut))
	attempts, _ = failureValue(t, err)["details"].(map[string]any)["attempts"].([]any)
	if d, _ := attempts[0].(map[string]any); d["terminal_provenance"] != "incomplete_eventstream_frame" || d["transport_state"] != "incomplete_frame" || d["incomplete_frame_bytes"] != 20 {
		t.Fatalf("%v", d)
	}
	// An empty answer is empty_response_eof.
	_, err = runGate(t, nil, GateConfig{Repair: true}, answers(nil))
	attempts, _ = failureValue(t, err)["details"].(map[string]any)["attempts"].([]any)
	if d, _ := attempts[0].(map[string]any); d["terminal_provenance"] != "empty_response_eof" || d["response_state"] != "no_semantic_output" {
		t.Fatalf("%v", d)
	}
	// Unknown event types are counted under "other" (:806-807); the failure keeps the counts.
	_, err = runGate(t, join(text("a"), event("surprise1", `{}`), stop("refusal")), GateConfig{Repair: true}, nil)
	counts, _ := failureValue(t, err)["details"].(map[string]any)["event_counts"].(map[string]any)
	if counts["other"] != 1 || counts["assistantResponseEvent"] != 1 || counts["messageStopEvent"] != 1 || len(counts) != 3 {
		t.Fatalf("event counts %v", counts)
	}
}

// The watchdog restarts on validated frames, not on bytes (kiro-terminal-integrity.test.js
// "treats validated non-semantic frames as watchdog activity").
func TestGateWatchdogFollowsValidatedFrames(t *testing.T) {
	pr, pw := io.Pipe()
	go func() {
		for _, f := range [][]byte{event("meteringEvent", `{"usage":1}`), event("contextUsageEvent", `{"contextUsagePercentage":5}`), join(text("Completed after active frames."), stop("end_turn"))} {
			time.Sleep(20 * time.Millisecond)
			pw.Write(f)
		}
		pw.Close()
	}()
	r := answers()
	if _, err := Gate(context.Background(), pr, GateConfig{TTFT: 40 * time.Millisecond, Stall: 40 * time.Millisecond}, ResponseOptions{}, r.retry, nil); err != nil || len(r.kinds) != 0 {
		t.Fatalf("err %v retries %v", err, r.kinds)
	}
	// Bytes of a frame that never completes are not progress.
	pr, pw = io.Pipe()
	frame := text("never finished")
	go func() {
		for i := 0; i < len(frame)-1; i++ {
			time.Sleep(10 * time.Millisecond)
			if _, err := pw.Write(frame[i : i+1]); err != nil {
				return
			}
		}
	}()
	defer pw.Close()
	r = answers(good())
	out, err := Gate(context.Background(), pr, GateConfig{Repair: true, TTFT: 60 * time.Millisecond, Stall: 60 * time.Millisecond}, ResponseOptions{}, r.retry, nil)
	if err != nil || len(r.kinds) != 1 {
		t.Fatalf("a dribbling partial frame kept the watchdog alive: err %v retries %v", err, r.kinds)
	}
	if raw, _ := io.ReadAll(out); string(raw) != string(good()) {
		t.Fatal("wrong answer released")
	}
}

// A refusal returned by the bounded retry is the answer, not a retry failure ("preserves an
// authoritative refusal returned by the bounded retry").
func TestGatePreservesARefusalFromTheRetry(t *testing.T) {
	r := answers(join(text("private filtered retry"), event("metadataEvent", `{"stopReason":"content_filtered"}`)))
	_, err := runGate(t, nil, GateConfig{Repair: true}, r)
	if code := failureCode(t, err); code != "kiro_terminal_refusal" || len(r.kinds) != 1 {
		t.Fatalf("code %s retries %v", code, r.kinds)
	}
}

// executors/kiro.js:139-140: content that is not text is empty, so the instruction replaces it.
func TestRepairRequestTreatsNonTextContentAsEmpty(t *testing.T) {
	for name, content := range map[string]string{"missing": ``, "array": `"content":[{"text":"x"}],`, "number": `"content":5,`, "null": `"content":null,`} {
		body := `{"conversationState":{"currentMessage":{"userInputMessage":{` + content + `"modelId":"m"}}}}`
		repaired, err := RepairRequest([]byte(body), "short_final")
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var doc struct {
			ConversationState struct {
				CurrentMessage struct {
					UserInputMessage struct {
						Content string `json:"content"`
						ModelID string `json:"modelId"`
					} `json:"userInputMessage"`
				} `json:"currentMessage"`
			} `json:"conversationState"`
		}
		if err := json.Unmarshal(repaired, &doc); err != nil {
			t.Fatal(err)
		}
		got := doc.ConversationState.CurrentMessage.UserInputMessage
		if got.Content != repairInstructions["short_final"] || got.ModelID != "m" {
			t.Errorf("%s: %+v", name, got)
		}
	}
	// No current user message: the body is repeated as it is (:138).
	for _, body := range []string{`{}`, `{"conversationState":{}}`, `{"conversationState":{"currentMessage":{}}}`} {
		if repaired, err := RepairRequest([]byte(body), "tool"); err != nil || string(repaired) != body {
			t.Errorf("%s: %v %s", body, err, repaired)
		}
	}
}

// executors/kiro.js:731-735: a blank-string input is not JSON and the call is dropped.
func TestGateDropsABlankInputCallAndKeepsTheOthers(t *testing.T) {
	blank := event("toolUseEvent", `{"toolUseId":"blank","name":"lookup","input":""}`)
	usable := tool("ok", "lookup", `{"q":1}`)
	r := answers(good())
	raw := join(usable, blank, stop("tool_use"))
	if got, err := runGate(t, raw, GateConfig{Repair: true}, r); err != nil || got != string(raw) || len(r.kinds) != 0 {
		t.Fatalf("err %v retries %v", err, r.kinds)
	}
	converted, err := KiroResponseToChat([]byte(raw), ResponseOptions{})
	if err != nil || strings.Contains(string(converted), `"blank"`) || !strings.Contains(string(converted), `"ok"`) {
		t.Fatalf("%v %s", err, converted)
	}
	// Alone it is the unusable-only turn: repaired once, then reported.
	r = answers(join(blank, stop("tool_use")))
	_, err = runGate(t, join(blank, stop("tool_use")), GateConfig{Repair: true}, r)
	if code := failureCode(t, err); code != "kiro_tool_call_repair_retry_failed" || len(r.kinds) != 1 || r.kinds[0] != "tool" {
		t.Fatalf("code %s retries %v", code, r.kinds)
	}
}

// The heartbeat is a timer of its own in the pin (executors/kiro.js:393-394, :418-419): it
// keeps running while the repair waits for response headers and for an error body. beat
// is called without a lock, so -race fails this test if it ever ran off the gate's goroutine.
func TestGateHeartbeatContinuesWhileTheRepairWaitsForHeaders(t *testing.T) {
	beats := 0
	release := make(chan struct{})
	r := &retries{answer: func(int) (io.ReadCloser, int, error) {
		<-release
		return bodyOf(good()), 200, nil
	}}
	go func() { time.Sleep(150 * time.Millisecond); close(release) }()
	out, err := Gate(context.Background(), bodyOf(join(text("..."))), GateConfig{Repair: true, HeartbeatEvery: 20 * time.Millisecond}, ResponseOptions{}, r.retry, func() { beats++ })
	if err != nil {
		t.Fatal(err)
	}
	if raw, _ := io.ReadAll(out); string(raw) != string(good()) {
		t.Fatal("wrong answer")
	}
	if beats < 5 {
		t.Fatalf("%d heartbeats across a 150 ms header wait at a 20 ms interval", beats)
	}
}

func TestGateHeartbeatContinuesWhileTheRepairErrorBodyIsRead(t *testing.T) {
	beats := 0
	slow := &slowBody{delay: 150 * time.Millisecond, data: "unauthorized"}
	r := &retries{answer: func(int) (io.ReadCloser, int, error) { return slow, 401, nil }}
	_, err := Gate(context.Background(), bodyOf(join(text("..."))), GateConfig{Repair: true, HeartbeatEvery: 20 * time.Millisecond}, ResponseOptions{}, r.retry, func() { beats++ })
	value := failureValue(t, err)
	if value["code"] != "kiro_integrity_retry_upstream_error" || value["message"] != "unauthorized" {
		t.Fatalf("%v", value)
	}
	if beats < 5 {
		t.Fatalf("%d heartbeats across a 150 ms body wait", beats)
	}
}

type slowBody struct {
	delay  time.Duration
	data   string
	read   bool
	closed atomic.Bool
}

func (s *slowBody) Read(p []byte) (int, error) {
	if s.read || s.closed.Load() {
		return 0, io.EOF
	}
	time.Sleep(s.delay)
	s.read = true
	return copy(p, s.data), nil
}
func (s *slowBody) Close() error { s.closed.Store(true); return nil }

// Cancelling during either wait ends the gate, closes what the repair returned and leaves no goroutine.
func TestGateCancellationDuringTheRepairWait(t *testing.T) {
	before := runtime.NumGoroutine()
	ctx, cancel := context.WithCancel(context.Background())
	late := bodyOf(good())
	r := &retries{answer: func(int) (io.ReadCloser, int, error) {
		<-ctx.Done()
		return late, 200, nil
	}}
	done := make(chan error, 1)
	go func() {
		_, err := Gate(ctx, bodyOf(join(text("..."))), GateConfig{Repair: true, HeartbeatEvery: 10 * time.Millisecond}, ResponseOptions{}, r.retry, nil)
		done <- err
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the gate outlived its context")
	}
	if !late.closed.Load() {
		t.Fatal("the late repair response was left open")
	}
	settle(t, before)
}

// The bound is counted on what the pin writes. The pin always drops what is inside
// <thinking> tags (executors/kiro.js:808-833), whatever the request asked for, so that text is
// not converted output; a tag split across frames is not seen and is written; an unfinished
// tag leaves an empty content delta. The expected sizes are spelled out from the pin's chunk
// shape, one envelope per delta with the role merged into the first.
func TestGateBoundIgnoresThinkingTextAsThePinDoes(t *testing.T) {
	const envelope = `"id":"chatcmpl-1700000000000","object":"chat.completion.chunk","created":1700000000,"model":"m"`
	chunk := func(delta, finish string) int {
		return len(`data: {` + envelope + `,"choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + finish + `}]}` + "\n\n")
	}
	finish := chunk(`{}`, `"stop"`) + len("data: [DONE]\n\n")
	big := strings.Repeat("x", 5000)
	cases := []struct {
		name   string
		frames [][]byte
		size   int
	}{
		{"closed tag is dropped", [][]byte{text("<thinking>" + big + "</thinking>\nok")}, chunk(`{"role":"assistant","content":"ok"}`, `null`) + finish},
		{"tag around text keeps the text", [][]byte{text("a<thinking>" + big + "</thinking>\nb")}, chunk(`{"role":"assistant","content":"ab"}`, `null`) + finish},
		{"tag open across frames", [][]byte{text("<thinking>" + big), text(big + "</thinking>\nZ")}, chunk(`{"role":"assistant","content":""}`, `null`) + chunk(`{"content":"Z"}`, `null`) + finish},
		// JSON.stringify writes <, >, & and U+2028 as they are; the size is theirs, not Go's escaped one.
		{"markup and separators are not escaped", [][]byte{text("a<b>&c\u2028d")}, chunk("{\"role\":\"assistant\",\"content\":\"a<b>&c\u2028d\"}", `null`) + finish},
		{"tag split across frames is not seen", [][]byte{text("<thin"), text("king>q")}, chunk(`{"role":"assistant","content":"<thin"}`, `null`) + chunk(`{"content":"king>q"}`, `null`) + finish},
	}
	for _, c := range cases {
		for _, tags := range []bool{false, true} {
			raw := join(append(append([][]byte{}, c.frames...), stop("end_turn"))...)
			for limit, want := range map[int]bool{c.size: true, c.size - 1: false} {
				_, err := Gate(context.Background(), bodyOf(raw), GateConfig{MaxBytes: limit}, ResponseOptions{Model: "m", ThinkingTags: tags}, nil, nil)
				if (err == nil) != want {
					t.Errorf("%s (thinking tags %v): limit %d, pin size %d: err %v", c.name, tags, limit, c.size, err)
				}
			}
		}
	}
	// What the client receives is unchanged: the reasoning inside the tags is delivered when the request asked.
	delivered, err := KiroResponseToChat(join(text("<thinking>why</thinking>\nok"), stop("end_turn")), ResponseOptions{ThinkingTags: true})
	if err != nil || !strings.Contains(string(delivered), `"reasoning_content":"why"`) || !strings.Contains(string(delivered), `"content":"ok"`) {
		t.Fatalf("%v %s", err, delivered)
	}
}
