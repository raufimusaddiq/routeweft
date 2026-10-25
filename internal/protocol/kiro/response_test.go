package kiro

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"hash/crc32"
	"io"
	"strings"
	"sync"
	"testing"
	"testing/iotest"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

func header(name string, kind byte, value string) []byte {
	out := []byte{byte(len(name))}
	out = append(out, name...)
	out = append(out, kind, 0, byte(len(value)))
	return append(out, value...)
}

func rawFrame(headers []byte, payload string) []byte {
	frame := make([]byte, 16+len(headers)+len(payload))
	binary.BigEndian.PutUint32(frame[:4], uint32(len(frame)))
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(headers)))
	binary.BigEndian.PutUint32(frame[8:12], crc32.ChecksumIEEE(frame[:8]))
	copy(frame[12:], headers)
	copy(frame[12+len(headers):], payload)
	binary.BigEndian.PutUint32(frame[len(frame)-4:], crc32.ChecksumIEEE(frame[:len(frame)-4]))
	return frame
}

func event(kind, payload string) []byte {
	return rawFrame(append(header(":message-type", 7, "event"), header(":event-type", 7, kind)...), payload)
}

func text(content string) []byte {
	return event("assistantResponseEvent", jsonObject("content", content))
}
func stop(reason string) []byte {
	if reason == "" {
		return event("messageStopEvent", `{}`)
	}
	return event("messageStopEvent", jsonObject("stopReason", reason))
}
func jsonObject(key, value string) string { return fmt.Sprintf(`{%q:%q}`, key, value) }

func tool(id, name, input string) []byte {
	return event("toolUseEvent", fmt.Sprintf(`{"toolUseId":%q,"name":%q,"input":%s}`, id, name, input))
}

func join(frames ...[]byte) []byte { return bytes.Join(frames, nil) }

type sse struct {
	raw    string
	chunks []map[string]any
	errors []map[string]any
	done   bool
}

func parseSSE(t *testing.T, raw string) sse {
	t.Helper()
	out := sse{raw: raw}
	for _, block := range strings.Split(raw, "\n\n") {
		data := strings.TrimPrefix(strings.TrimSpace(block), "data: ")
		switch {
		case data == "":
		case data == "[DONE]":
			out.done = true
		default:
			parsed, err := concerns.WireObject([]byte(data))
			if err != nil {
				t.Fatalf("bad SSE data %q", data)
			}
			if parsed["error"] != nil {
				out.errors = append(out.errors, concerns.Object(parsed["error"]))
			} else {
				out.chunks = append(out.chunks, parsed)
			}
		}
	}
	return out
}

func (s sse) delta(key string) string {
	var joined strings.Builder
	for _, chunk := range s.chunks {
		joined.WriteString(concerns.Text(concerns.Object(concerns.Object(concerns.Items(chunk["choices"])[0])["delta"])[key]))
	}
	return joined.String()
}

func (s sse) finish() any {
	for _, chunk := range s.chunks {
		if reason := concerns.Object(concerns.Items(chunk["choices"])[0])["finish_reason"]; reason != nil {
			return reason
		}
	}
	return nil
}

func (s sse) calls() []map[string]any {
	var calls []map[string]any
	for _, chunk := range s.chunks {
		delta := concerns.Object(concerns.Object(concerns.Items(chunk["choices"])[0])["delta"])
		for _, raw := range concerns.Items(delta["tool_calls"]) {
			call := concerns.Object(raw)
			function := concerns.Object(call["function"])
			if call["id"] != nil {
				calls = append(calls, map[string]any{"id": call["id"], "name": function["name"], "arguments": concerns.Text(function["arguments"])})
			} else {
				last := calls[len(calls)-1]
				last["arguments"] = last["arguments"].(string) + concerns.Text(function["arguments"])
			}
		}
	}
	return calls
}

func (s sse) usage() map[string]any {
	for _, chunk := range s.chunks {
		if usage := concerns.Object(chunk["usage"]); usage != nil {
			return usage
		}
	}
	return nil
}

func stream(t *testing.T, body []byte, options ResponseOptions) (sse, error) {
	t.Helper()
	var out bytes.Buffer
	err := KiroStreamToChat(context.Background(), bytes.NewReader(body), &out, options)
	return parseSSE(t, out.String()), err
}

func TestStreamText(t *testing.T) {
	out, err := stream(t, join(text("Hel"), text("lo"), event("metricsEvent", `{"inputTokens":10,"outputTokens":5}`), stop("end_turn")), ResponseOptions{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if out.delta("content") != "Hello" || out.finish() != "stop" || !out.done || len(out.errors) != 0 {
		t.Fatalf("%+v", out)
	}
	first := concerns.Object(concerns.Object(concerns.Items(out.chunks[0]["choices"])[0])["delta"])
	if first["role"] != "assistant" || out.chunks[0]["model"] != "m" || out.chunks[0]["object"] != "chat.completion.chunk" {
		t.Fatalf("%v", out.chunks[0])
	}
	if fmt.Sprint(out.usage()) != "map[completion_tokens:5 prompt_tokens:10 total_tokens:15]" {
		t.Fatalf("usage %v", out.usage())
	}
}

func TestStreamIsIncremental(t *testing.T) {
	reader, writer := io.Pipe()
	out := &lockedBuffer{signal: make(chan struct{}, 8)}
	done := make(chan error, 1)
	go func() { done <- KiroStreamToChat(context.Background(), reader, out, ResponseOptions{}) }()
	_, _ = writer.Write(text("first"))
	for !strings.Contains(out.String(), "first") {
		<-out.signal // the text reached the client before the body was complete
	}
	if strings.Contains(out.String(), "finish_reason\":\"stop") {
		t.Fatalf("%s", out.String())
	}
	_, _ = writer.Write(stop("end_turn"))
	_ = writer.Close()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

type lockedBuffer struct {
	mutex  sync.Mutex
	buffer bytes.Buffer
	signal chan struct{}
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.buffer.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mutex.Lock()
	defer b.mutex.Unlock()
	return b.buffer.String()
}

func (b *lockedBuffer) Flush() {
	select {
	case b.signal <- struct{}{}:
	default:
	}
}

func TestCacheCountsAreSeparate(t *testing.T) {
	out, err := stream(t, join(text("x"), event("metricsEvent", `{"inputTokens":10,"outputTokens":5,"cacheReadInputTokens":3,"cacheCreationInputTokens":2}`), stop("")), ResponseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	usage := out.usage()
	details := concerns.Object(usage["prompt_tokens_details"])
	if fmt.Sprintf("%v %v %v %v %v", usage["prompt_tokens"], usage["completion_tokens"], usage["total_tokens"], details["cached_tokens"], details["cache_creation_tokens"]) != "15 5 20 3 2" {
		t.Fatalf("%v", usage)
	}
	// Back in Messages terms the input is what Kiro reported.
	if input := concerns.ClaudeUsageFromOpenAI(usage)["input_tokens"]; input != int64(10) {
		t.Fatalf("input_tokens %v", input)
	}
}

func TestUsageOnlyWhenMeasured(t *testing.T) {
	for name, payload := range map[string]string{
		"absent":         `{}`,
		"negative":       `{"inputTokens":-1,"outputTokens":4}`,
		"string":         `{"inputTokens":"7","outputTokens":4}`,
		"null":           `null`,
		"negative cache": `{"inputTokens":1,"outputTokens":1,"cacheReadInputTokens":-2}`,
	} {
		out, err := stream(t, join(text("x"), event("metricsEvent", payload), stop("")), ResponseOptions{})
		if err != nil || out.usage() != nil {
			t.Errorf("%s: usage %v err %v", name, out.usage(), err)
		}
	}
	// Output alone is a measurement; the nested spelling and the later event win.
	out, _ := stream(t, join(text("x"), event("metricsEvent", `{"metricsEvent":{"inputTokens":1,"outputTokens":1}}`), event("metricsEvent", `{"outputTokens":9}`), stop("")), ResponseOptions{})
	if fmt.Sprintf("%v %v", out.usage()["completion_tokens"], out.usage()["prompt_tokens"]) != "9 0" {
		t.Fatalf("%v", out.usage())
	}
	// Estimates and credits are not carried.
	out, _ = stream(t, join(text("hello world"), event("contextUsageEvent", `{"contextUsagePercentage":50}`), event("meteringEvent", `{"usage":0.5,"unit":"credit"}`), stop("")), ResponseOptions{})
	if out.usage() != nil || strings.Contains(out.raw, "kiro_credits") {
		t.Fatalf("%v", out.usage())
	}
}

func TestToolCallsAreDeferredAndRestored(t *testing.T) {
	names, err := concerns.NewToolNamesFor(toolNameRule, []string{"my.tool"})
	if err != nil {
		t.Fatal(err)
	}
	body := join(
		text("calling"),
		event("toolUseEvent", `{"toolUseId":"t1","name":"my_tool","input":"{\"a\":"}`),
		tool("t2", "other", `{"b":2}`),
		event("toolUseEvent", `{"toolUseId":"t1","name":"my_tool","input":"1}"}`),
		event("toolUseEvent", `{"toolUseId":"t1","name":"my_tool","stop":true}`),
		stop("tool_use"))
	out, err := stream(t, body, ResponseOptions{Names: names})
	if err != nil {
		t.Fatal(err)
	}
	calls := out.calls()
	if len(calls) != 2 || calls[0]["id"] != "t1" || calls[0]["name"] != "my.tool" || calls[0]["arguments"] != `{"a":1}` ||
		calls[1]["id"] != "t2" || calls[1]["name"] != "other" || calls[1]["arguments"] != `{"b":2}` || out.finish() != "tool_calls" {
		t.Fatalf("%v %v", calls, out.finish())
	}
	// Text reached the client before any tool call.
	if !strings.Contains(out.raw, "calling") || strings.Index(out.raw, "calling") > strings.Index(out.raw, "tool_calls") {
		t.Fatalf("%s", out.raw)
	}
	// A turn that ends on end_turn after calling tools is a tool turn.
	out, err = stream(t, join(tool("t", "x", `{}`), stop("end_turn")), ResponseOptions{})
	if err != nil || out.finish() != "tool_calls" {
		t.Fatalf("%v %v", out.finish(), err)
	}
	// An array payload carries several calls.
	out, err = stream(t, join(event("toolUseEvent", `[{"toolUseId":"a","name":"x","input":{}},{"toolUseId":"b","name":"y","input":"{}"}]`), stop("tool_use")), ResponseOptions{})
	if err != nil || len(out.calls()) != 2 || out.calls()[1]["arguments"] != "{}" {
		t.Fatalf("%v %v", out.calls(), err)
	}
	// A call without an id gets a stable one.
	out, err = stream(t, join(event("toolUseEvent", `{"name":"x","input":{}}`), stop("tool_use")), ResponseOptions{})
	if err != nil || out.calls()[0]["id"] != "call_msg0_tc0_x" {
		t.Fatalf("%v %v", out.calls(), err)
	}
}

// One unusable tool fragment or call does not take the turn with it: the pin keeps
// the usable calls and the text, and the turn fails only when nothing else was
// produced (kiro-usage-and-tool-integrity.test.js "C", executors/kiro.js:738-759,
// :972-980, :1053-1063).
func TestToolFailuresAreLocalToTheStream(t *testing.T) {
	cases := map[string][]byte{
		"empty event":       event("toolUseEvent", ``),
		"null":              event("toolUseEvent", `null`),
		"empty array":       event("toolUseEvent", `[]`),
		"not an object":     event("toolUseEvent", `"x"`),
		"array of null":     event("toolUseEvent", `[null]`),
		"no name":           event("toolUseEvent", `{"toolUseId":"a","input":{}}`),
		"blank name":        event("toolUseEvent", `{"toolUseId":"a","name":" ","input":{}}`),
		"name not text":     event("toolUseEvent", `{"toolUseId":"a","name":5,"input":{}}`),
		"blank id":          event("toolUseEvent", `{"toolUseId":" ","name":"x","input":{}}`),
		"id a number":       event("toolUseEvent", `{"toolUseId":5,"name":"x","input":{}}`),
		"no input":          event("toolUseEvent", `{"toolUseId":"a","name":"x"}`),
		"null input":        event("toolUseEvent", `{"toolUseId":"a","name":"x","input":null}`),
		"array input":       event("toolUseEvent", `{"toolUseId":"a","name":"x","input":[]}`),
		"number input":      event("toolUseEvent", `{"toolUseId":"a","name":"x","input":5}`),
		"input not json":    event("toolUseEvent", `{"toolUseId":"a","name":"x","input":"{oops"}`),
		"input a json list": event("toolUseEvent", `{"toolUseId":"a","name":"x","input":"[1]"}`),
		"wrapper no name":   event("toolUseEvent", `{"toolUseId":"a","name":"tool_call","input":{"arguments":{}}}`),
		"wrapper no args":   event("toolUseEvent", `{"toolUseId":"a","name":"tool_call","input":{"name":"n"}}`),
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := stream(t, join(text("kept"), tool("good", "x", `{}`), bad, stop("tool_use")), ResponseOptions{})
			if err != nil || out.finish() != "tool_calls" || len(out.calls()) != 1 || out.calls()[0]["id"] != "good" {
				t.Fatalf("the usable call was lost: err %v %+v", err, out)
			}
			if out.delta("content") != "kept" {
				t.Fatalf("streamed text was lost: %q", out.delta("content"))
			}
			// With no usable call and no text, the turn is the failure the gate repairs.
			out, err = stream(t, join(bad, stop("tool_use")), ResponseOptions{})
			if err == nil || len(out.errors) != 1 || out.errors[0]["code"] != "invalid_kiro_tool_call" || out.finish() != nil {
				t.Fatalf("an unusable-only turn must fail as invalid_kiro_tool_call: %v %+v", err, out)
			}
			// With text and no usable call the text stands (:1053).
			out, err = stream(t, join(text("only text"), bad, stop("tool_use")), ResponseOptions{})
			if err != nil || out.finish() != "stop" || out.delta("content") != "only text" {
				t.Fatalf("text was thrown away with the bad call: %v %+v", err, out)
			}
		})
	}
	// A key the pin does not read is not read (executors/kiro.js:846-871): the call is served.
	if out, err := stream(t, join(text("kept"), event("toolUseEvent", `{"toolUseId":"a","name":"x","input":{},"extra":"text"}`), stop("tool_use")), ResponseOptions{}); err != nil || out.finish() != "tool_calls" {
		t.Errorf("an unread key on a tool call: %v %+v", err, out)
	}
	// A null toolUseId is an absent one: the pin generates an id (executors/kiro.js:854).
	if out, err := stream(t, join(event("toolUseEvent", `{"toolUseId":null,"name":"x","input":{}}`), stop("tool_use")), ResponseOptions{}); err != nil || len(out.calls()) != 1 {
		t.Errorf("null id: %v %+v", err, out)
	}
	// Fragment errors are per tool: the rejected fragment is skipped, what was buffered is kept.
	for name, frames := range map[string][][]byte{
		"name changes":   {tool("a", "x", `"{"`), tool("a", "y", `"}"`)},
		"kind changes":   {event("toolUseEvent", `{"toolUseId":"a","name":"x","input":"{"}`), event("toolUseEvent", `{"toolUseId":"a","name":"x","input":{}}`)},
		"object to text": {tool("a", "x", `{}`), event("toolUseEvent", `{"toolUseId":"a","name":"x","input":"{}"}`)},
	} {
		out, err := stream(t, join(append(append([][]byte{tool("good", "x", `{}`)}, frames...), stop("tool_use"))...), ResponseOptions{})
		if err != nil || out.finish() != "tool_calls" || len(out.calls()) < 1 || out.calls()[0]["id"] != "good" {
			t.Errorf("%s: %v %+v", name, err, out)
		}
	}
	// A wrapper call that is whole is served.
	if _, err := stream(t, join(event("toolUseEvent", `{"toolUseId":"a","name":"tool_call","input":{"name":"n","arguments":{}}}`), stop("tool_use")), ResponseOptions{}); err != nil {
		t.Fatal(err)
	}
	// The buffer is bounded.
	if _, err := stream(t, join(event("toolUseEvent", `{"toolUseId":"a","name":"x","input":"`+strings.Repeat("a", 200)+`"}`), stop("")), ResponseOptions{MaxToolBytes: 100}); err == nil {
		t.Fatal("tool input over its bound must fail")
	}
}

func TestStopReasons(t *testing.T) {
	type outcome struct {
		finish string // "" means the stream fails
	}
	cases := []struct {
		name   string
		frames [][]byte
		want   string
	}{
		{"no reason", [][]byte{text("x"), stop("")}, "stop"},
		{"end_turn", [][]byte{text("x"), stop("end_turn")}, "stop"},
		{"EndTurn", [][]byte{text("x"), stop("EndTurn")}, "stop"},
		{"stop_sequence", [][]byte{text("x"), stop("stop_sequence")}, "stop"},
		{"empty answer", [][]byte{stop("end_turn")}, "stop"},
		{"max_tokens", [][]byte{text("x"), stop("max_tokens")}, "length"},
		{"MaxTokens", [][]byte{text("x"), stop("MaxTokens")}, "length"},
		{"max tokens with a call", [][]byte{tool("a", "x", `{}`), stop("max_tokens")}, ""},
		{"tool_use with a call", [][]byte{tool("a", "x", `{}`), stop("tool_use")}, "tool_calls"},
		// The turn declared a tool call and made none, but it has text: the pin keeps the text (:787-790).
		{"tool_use with none", [][]byte{text("x"), stop("tool_use")}, "stop"},
		{"tool_use with none and no text", [][]byte{stop("tool_use")}, ""},
		{"no reason after a tool event", [][]byte{tool("a", "x", `{}`), stop("")}, "tool_calls"},
		{"context window after output", [][]byte{text("x"), stop("model_context_window_exceeded")}, "length"},
		{"context window before output", [][]byte{stop("model_context_window_exceeded")}, ""},
		// A truncation after output keeps the output, calls included (:1014-1018, :1079-1083).
		{"context window with a call", [][]byte{text("x"), tool("a", "x", `{}`), stop("model_context_window_exceeded")}, "length"},
		{"context window with only a call", [][]byte{tool("a", "x", `{}`), stop("model_context_window_exceeded")}, ""},
		{"max tokens with text and a call", [][]byte{text("x"), tool("a", "x", `{}`), stop("max_tokens")}, "length"},
		{"refusal", [][]byte{text("x"), stop("refusal")}, ""},
		{"content filtered", [][]byte{text("x"), stop("content_filtered")}, ""},
		{"guardrail", [][]byte{text("x"), stop("guardrail_intervened")}, ""},
		{"cancelled", [][]byte{text("x"), stop("cancelled")}, ""},
		{"pause_turn", [][]byte{text("x"), stop("pause_turn")}, ""},
		{"malformed", [][]byte{text("x"), stop("malformed_model_output")}, ""},
		{"unknown", [][]byte{text("x"), stop("something_new")}, ""},
		{"a later mild reason does not hide a failure", [][]byte{text("x"), stop("refusal"), stop("end_turn")}, ""},
		{"metadata stop", [][]byte{text("x"), event("metadataEvent", `{"stopReason":"end_turn"}`)}, "stop"},
		{"metadata stop, nested", [][]byte{text("x"), event("MetadataEvent", `{"metadata":{"stop_reason":"max_tokens"}}`)}, "length"},
		{"metadata stop refusal", [][]byte{text("x"), event("metadataEvent", `{"metadataEvent":{"stopReason":"refusal"}}`)}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := stream(t, join(c.frames...), ResponseOptions{})
			if c.want == "" {
				if err == nil || out.finish() != nil || len(out.errors) != 1 {
					t.Fatalf("err %v %+v", err, out)
				}
				return
			}
			if err != nil || out.finish() != c.want || !out.done {
				t.Fatalf("err %v finish %v", err, out.finish())
			}
		})
	}
}

// A clean end with output and no stop event completes the answer, exactly as the pin
// does (executors/kiro.js:1008-1075; tests/unit/kiro-terminal-integrity.test.js:169
// "accepts CLI-compatible text and usage frames at clean EOF without messageStop").
// This replaces the earlier approved rule "require a terminal" for Kiro only; the
// generic EventStream reader still reports the missing terminal (concerns.NoTerminalError).
// What still fails is what the pin fails: nothing at all, a cut frame, corrupt bytes.
func TestCleanEOFWithOutputCompletes(t *testing.T) {
	complete := map[string][]byte{
		"text only":               join(text("x")),
		"text with usage frames":  join(text("Complete answer."), event("meteringEvent", `{"usage":2,"unit":"credit"}`), event("contextUsageEvent", `{"contextUsagePercentage":10}`)),
		"metadata without a stop": join(text("x"), event("metadataEvent", `{"tokenUsage":{}}`)),
		"reasoning only":          join(event("reasoningContentEvent", `{"text":"thinking"}`)),
	}
	for name, body := range complete {
		t.Run(name, func(t *testing.T) {
			out, err := stream(t, body, ResponseOptions{})
			if err != nil || out.finish() != "stop" || len(out.errors) != 0 {
				t.Fatalf("err %v %+v", err, out)
			}
			if buffered, err := KiroResponseToChat(body, ResponseOptions{}); err != nil || !strings.Contains(string(buffered), `"finish_reason":"stop"`) {
				t.Fatalf("buffered: %v %s", err, buffered)
			}
		})
	}
	// A tool call with no stop event is a tool turn (stopDisposition(null, sawToolUse)).
	if out, err := stream(t, join(tool("a", "x", `{}`)), ResponseOptions{}); err != nil || out.finish() != "tool_calls" {
		t.Fatalf("tool turn: %v %+v", err, out)
	}
	// An explicit stop with no output is still an explicit completion.
	if out, err := stream(t, join(stop("end_turn")), ResponseOptions{}); err != nil || out.finish() != "stop" {
		t.Fatalf("%v %+v", err, out)
	}
	fails := map[string][]byte{
		"empty body":         nil,
		"usage only":         join(event("metricsEvent", `{"inputTokens":1,"outputTokens":1}`)),
		"metering only":      join(event("meteringEvent", `{"usage":2}`)),
		"truncated frame":    join(text("x"), stop("end_turn"))[:len(join(text("x"), stop("end_turn")))-3],
		"trailing garbage":   append(join(text("x"), stop("end_turn")), 1, 2, 3),
		"corrupt after stop": append(join(text("x"), stop("end_turn")), make([]byte, 20)...),
	}
	for name, body := range fails {
		t.Run(name, func(t *testing.T) {
			out, err := stream(t, body, ResponseOptions{})
			if err == nil || out.finish() != nil || len(out.errors) != 1 || out.errors[0]["code"] != "kiro_missing_terminal" {
				t.Fatalf("err %v %+v", err, out)
			}
		})
	}
	// Fragmenting the transport changes nothing.
	body := join(text("a"))
	var buffer bytes.Buffer
	if err := KiroStreamToChat(context.Background(), iotest.OneByteReader(bytes.NewReader(body)), &buffer, ResponseOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := KiroStreamToChat(context.Background(), iotest.OneByteReader(bytes.NewReader(body[:len(body)-1])), io.Discard, ResponseOptions{}); err == nil {
		t.Fatal("expected a failure for a cut frame")
	}
	// The empty answer is empty_response_eof (:1066-1075).
	if out, _ := stream(t, nil, ResponseOptions{}); out.errors[0]["details"].(map[string]any)["terminal_provenance"] != "empty_response_eof" {
		t.Fatalf("%+v", out.errors)
	}
}

// The pin has no rule against content after a stop event: processEvent handles every frame
// the same (executors/kiro.js:792-923), so late text is delivered and a late call is served.
func TestEventsAfterTheStop(t *testing.T) {
	for name, frame := range map[string][]byte{
		"text":      text("late"),
		"code":      event("codeEvent", `{"content":"late"}`),
		"reasoning": event("reasoningContentEvent", `{"text":"late"}`),
	} {
		out, err := stream(t, join(text("x"), stop("end_turn"), frame), ResponseOptions{})
		if err != nil || out.finish() != "stop" || (out.delta("content")+out.delta("reasoning_content")) != "xlate" {
			t.Errorf("%s: %v %+v", name, err, out)
		}
	}
	out, err := stream(t, join(text("x"), stop("end_turn"), tool("a", "x", `{}`)), ResponseOptions{})
	if err != nil || out.finish() != "tool_calls" || len(out.calls()) != 1 {
		t.Errorf("a call after the stop: %v %+v", err, out)
	}
	// Usage and bookkeeping after the stop are normal.
	out, err = stream(t, join(text("x"), stop("end_turn"), event("metricsEvent", `{"inputTokens":3,"outputTokens":4}`), event("meteringEvent", `{"usage":1}`), event("contextUsageEvent", `{}`)), ResponseOptions{})
	if err != nil || out.finish() != "stop" || out.usage() == nil {
		t.Fatalf("%v %+v", err, out)
	}
}

// An event the pin has no branch for is counted under "other" and ignored; a field it does
// not read is not read (executors/kiro.js:806-807, :923; kiro-terminal-integrity.test.js
// "caps diagnostic event-type cardinality"). The one thing that ends the answer is a frame
// whose payload is not one JSON document (:964-967).
func TestUnknownEvents(t *testing.T) {
	served := map[string][]byte{
		"harmless contextUsageEvent":           event("contextUsageEvent", `{"contextUsagePercentage":12.5}`),
		"harmless meteringEvent":               event("meteringEvent", `{"unit":"credit","usage":0.1}`),
		"harmless metricsEvent":                event("metricsEvent", `{"inputTokens":1,"outputTokens":2}`),
		"metadata without a stop":              event("metadataEvent", `{"tokenUsage":{"outputTokens":3}}`),
		"unknown, empty payload":               event("brandNewEvent", ``),
		"unknown, empty object":                event("brandNewEvent", `{}`),
		"unknown, null":                        event("brandNewEvent", `null`),
		"unknown, empty fields":                event("brandNewEvent", `{"a":"","b":[],"c":{},"d":null}`),
		"unknown with text":                    event("brandNewEvent", `{"content":"lost?"}`),
		"unknown with a string":                event("followupPromptEvent", `"hello"`),
		"unknown with a number":                event("brandNewEvent", `{"n":0}`),
		"unknown with a list":                  event("supplementaryWebLinksEvent", `{"links":[{"url":"u"}]}`),
		"unknown with false":                   event("brandNewEvent", `false`),
		"no event type":                        rawFrame(header(":message-type", 7, "event"), `{"content":"x"}`),
		"empty event type":                     rawFrame(header(":event-type", 7, ""), `{}`),
		"event type not a string":              rawFrame([]byte{11, ':', 'e', 'v', 'e', 'n', 't', '-', 't', 'y', 'p', 'e', 0}, `{}`),
		"known event with an empty unread key": event("assistantResponseEvent", `{"content":"","extra":""}`),
	}
	for name, frame := range served {
		t.Run(name, func(t *testing.T) {
			out, err := stream(t, join(frame, text("t"), stop("end_turn")), ResponseOptions{})
			if err != nil || out.finish() != "stop" || out.delta("content") != "t" {
				t.Fatalf("the pin serves this answer: err %v %+v", err, out)
			}
		})
	}
	// Fields the pin does not read do not fail the event it does read.
	for name, frame := range map[string][]byte{
		"assistantResponseEvent with an unread key": event("assistantResponseEvent", `{"content":"x","extra":"y"}`),
		"codeEvent with an unread key":              event("codeEvent", `{"content":"x","language":"go"}`),
	} {
		out, err := stream(t, join(frame, text("t"), stop("end_turn")), ResponseOptions{})
		if err != nil || out.finish() != "stop" || out.delta("content") != "xt" {
			t.Errorf("%s: %v %+v", name, err, out)
		}
	}
	for name, frame := range map[string][]byte{
		"payload not json":           event("brandNewEvent", `{oops`),
		"payload with trailing data": event("brandNewEvent", `{} {}`),
	} {
		out, err := stream(t, join(frame, text("t"), stop("end_turn")), ResponseOptions{})
		if err == nil || len(out.errors) != 1 || out.errors[0]["code"] != "kiro_missing_terminal" ||
			out.errors[0]["details"].(map[string]any)["terminal_provenance"] != "corrupt_eventstream_frame" {
			t.Errorf("%s: %v %+v", name, err, out)
		}
	}
}

func TestUnknownEventsAreCounted(t *testing.T) {
	state := newMachine(ResponseOptions{}, func(map[string]any) error { return nil })
	for _, frame := range [][]byte{event("contextUsageEvent", `{}`), event("contextUsageEvent", `{}`), event("metadataEvent", `{}`)} {
		err := concerns.ReadEventStream(context.Background(), bytes.NewReader(append(append([]byte{}, frame...), stop("")...)), func(e concerns.EventStreamEvent) (bool, error) {
			return state.handle(e)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if state.counts["contextUsageEvent"] != 2 || state.counts["metadataEvent"] != 1 {
		t.Fatalf("%v", state.counts)
	}
}

// Fields are read as the pin reads them: a value of the wrong kind is simply not the text
// (executors/kiro.js:808, :834-845, :872-888). Nothing here fails the answer.
func TestKnownEventPayloadShapes(t *testing.T) {
	type row struct {
		payload string
		content string
	}
	rows := map[string][]row{
		"assistantResponseEvent": {
			{``, ""}, {`{}`, ""}, {`{"content":""}`, ""}, {`{"content":null}`, ""}, {`{"content":"hi"}`, "hi"},
			{`null`, ""}, {`"hi"`, ""}, {`5`, ""}, {`[]`, ""}, {`true`, ""}, {`{"content":5}`, ""}, {`{"content":{}}`, ""}, {`{"content":["a"]}`, ""},
		},
		"codeEvent": {
			{``, ""}, {`{}`, ""}, {`{"content":"c"}`, "c"}, {`null`, ""}, {`"c"`, ""}, {`{"content":false}`, ""},
		},
	}
	for kind, list := range rows {
		for _, r := range list {
			out, err := stream(t, join(event(kind, r.payload), stop("end_turn")), ResponseOptions{})
			if err != nil || out.delta("content") != r.content {
				t.Errorf("%s %s: err %v content %q", kind, r.payload, err, out.delta("content"))
			}
		}
	}
	reasoning := []row{
		{``, ""}, {`{}`, ""}, {`null`, ""}, {`"thought"`, "thought"}, {`{"text":"a"}`, "a"}, {`{"content":"b"}`, "b"},
		{`{"reasoningContentEvent":"c"}`, "c"}, {`{"reasoningContentEvent":{"text":"d"}}`, "d"}, {`{"reasoningContentEvent":{"content":"e"}}`, "e"},
		{`{"reasoningContentEvent":{"text":""}}`, ""}, {`{"text":"one","content":"two"}`, "one"}, {`{"reasoningContentEvent":"one","text":"two"}`, "one"},
		{`{"text":5}`, ""}, {`{"reasoningContentEvent":5}`, ""}, {`5`, ""}, {`[]`, ""}, {`{"signature":"s"}`, ""},
	}
	for _, r := range reasoning {
		out, err := stream(t, join(event("reasoningContentEvent", r.payload), stop("end_turn")), ResponseOptions{})
		if err != nil || out.delta("reasoning_content") != r.content {
			t.Errorf("reasoning %s: err %v %q", r.payload, err, out.delta("reasoning_content"))
		}
	}
	// A stop event's reason is String(stopReason ?? stop_reason) normalized; no reason is end_turn.
	for payload, want := range map[string]string{`null`: "stop", `"x"`: "stop", `5`: "stop", `[]`: "stop", ``: "stop", `{}`: "stop",
		`{"stop_reason":"end_turn"}`: "stop", `{"stopReason":"end_turn","extra":"y"}`: "stop", `{"stopReason":"max_tokens"}`: "length",
		`{"stopReason":"end_turn","stop_reason":"refusal"}`: "stop", `{"stopReason":null,"stop_reason":"max_tokens"}`: "length"} {
		out, err := stream(t, join(text("x"), event("messageStopEvent", payload)), ResponseOptions{})
		if err != nil || out.finish() != want {
			t.Errorf("messageStopEvent %q: %v %v", payload, err, out.finish())
		}
	}
	// A reason that is not a known string is an unknown stop (String(5) is "5").
	if out, err := stream(t, join(text("x"), event("messageStopEvent", `{"stopReason":5}`)), ResponseOptions{}); err == nil || out.errors[0]["code"] != "kiro_unknown_stop_reason" {
		t.Errorf("stopReason 5: %v %+v", err, out)
	}
}

func TestUpstreamExceptionFrames(t *testing.T) {
	for _, kind := range []string{"exception", "error"} {
		frame := rawFrame(append(header(":message-type", 7, kind), header(":exception-type", 7, "ThrottlingException")...), `{"message":"slow down"}`)
		out, err := stream(t, join(text("x"), frame, stop("end_turn")), ResponseOptions{})
		if err == nil || out.finish() != nil || len(out.errors) != 1 || out.errors[0]["message"] != "slow down" || out.errors[0]["code"] != "kiro_upstream_eventstream_error" {
			t.Fatalf("%s: %v %+v", kind, err, out)
		}
	}
	frame := rawFrame(header(":message-type", 7, "exception"), ``)
	if out, err := stream(t, frame, ResponseOptions{}); err == nil || len(out.errors) != 1 || out.errors[0]["message"] != "Kiro upstream sent an EventStream exception" {
		t.Fatalf("%v %+v", err, out)
	}
	// Only "error" and "exception" are special: any other :message-type, of any header kind, is an event (:793-803).
	for name, frame := range map[string][]byte{
		"other type":     rawFrame(append(header(":message-type", 7, "mystery"), header(":event-type", 7, "assistantResponseEvent")...), `{"content":"x"}`),
		"not a string":   rawFrame(append(header(":message-type", 6, "event"), header(":event-type", 7, "assistantResponseEvent")...), `{"content":"x"}`),
		"without a type": rawFrame(header(":event-type", 7, "assistantResponseEvent"), `{"content":"x"}`),
	} {
		if out, err := stream(t, join(frame, stop("end_turn")), ResponseOptions{}); err != nil || out.delta("content") != "x" {
			t.Errorf("%s: %v %+v", name, err, out)
		}
	}
}

// A tag split across frames at any point, in any number of pieces, is never
// text: the reasoning is reasoning_content and the answer is the answer.
func TestThinkingTagsAcrossFrames(t *testing.T) {
	full := "Before. <thinking>weighing it up</thinking>\nThe answer is 42."
	wantContent, wantReasoning := "Before. The answer is 42.", "weighing it up"
	run := func(pieces []string) {
		t.Helper()
		var frames [][]byte
		for _, piece := range pieces {
			frames = append(frames, text(piece))
		}
		out, err := stream(t, join(append(frames, stop("end_turn"))...), ResponseOptions{ThinkingTags: true})
		if err != nil {
			t.Fatalf("%q: %v", pieces, err)
		}
		if out.delta("content") != wantContent || out.delta("reasoning_content") != wantReasoning {
			t.Fatalf("%q: content %q reasoning %q", pieces, out.delta("content"), out.delta("reasoning_content"))
		}
	}
	run([]string{full})
	for i := 0; i <= len(full); i++ {
		run([]string{full[:i], full[i:]})
	}
	for i := 0; i <= len(full); i++ {
		for j := i; j <= len(full); j++ {
			run([]string{full[:i], full[i:j], full[j:]})
		}
	}
	var bytewise []string
	for i := 0; i < len(full); i++ {
		bytewise = append(bytewise, full[i:i+1])
	}
	run(bytewise)
	// The newline after the close belongs to the tag even when it arrives alone.
	run([]string{"Before. <thinking>weighing it up</thinking>", "\n", "The answer is 42."})
}

func TestThinkingTagEdges(t *testing.T) {
	cases := []struct {
		name    string
		pieces  []string
		tags    bool
		content string
		thought string
	}{
		{"tags off: literal", []string{"a <thinking>b</thinking> c"}, false, "a <thinking>b</thinking> c", ""},
		{"partial open tag at the end is text", []string{"x <thin"}, true, "x <thin", ""},
		{"partial close tag at the end is reasoning", []string{"<thinking>a</think"}, true, "", "a</think"},
		{"unclosed tag", []string{"a<thinking>b"}, true, "a", "b"},
		{"two blocks", []string{"<thinking>a</thinking>x<thinking>b</thinking>y"}, true, "xy", "ab"},
		{"adjacent", []string{"<thinking></thinking>q"}, true, "q", ""},
		{"lookalike", []string{"<thinkingx>a</thinkingx>"}, true, "<thinkingx>a</thinkingx>", ""},
		{"angle bracket alone", []string{"1 < 2 <", " 3"}, true, "1 < 2 < 3", ""},
		{"only a newline after the close is eaten", []string{"<thinking>a</thinking>\n\nb"}, true, "\nb", "a"},
		{"no newline after the close", []string{"<thinking>a</thinking>b"}, true, "b", "a"},
		{"empty pieces", []string{"", "a", ""}, true, "a", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var frames [][]byte
			for _, piece := range c.pieces {
				frames = append(frames, text(piece))
			}
			out, err := stream(t, join(append(frames, stop("end_turn"))...), ResponseOptions{ThinkingTags: c.tags})
			if err != nil || out.delta("content") != c.content || out.delta("reasoning_content") != c.thought {
				t.Fatalf("err %v content %q reasoning %q", err, out.delta("content"), out.delta("reasoning_content"))
			}
		})
	}
	// A held partial tag is flushed before the finish, and a stream that fails drops it with the stream.
	out, err := stream(t, join(text("ok <thin"), text("more")[:20]), ResponseOptions{ThinkingTags: true})
	if err == nil || out.finish() != nil {
		t.Fatalf("%v %+v", err, out)
	}
	// ... while a clean end completes the answer, the held text flushed as the model's own.
	out, err = stream(t, join(text("ok <thin")), ResponseOptions{ThinkingTags: true})
	if err != nil || out.delta("content") != "ok <thin" || out.finish() != "stop" {
		t.Fatalf("%v %+v", err, out)
	}
}

func TestReasoningAndCodeEvents(t *testing.T) {
	out, err := stream(t, join(
		event("reasoningContentEvent", `{"text":"hm"}`),
		event("assistantResponseEvent", `{"content":"a"}`),
		event("codeEvent", `{"content":"b"}`),
		stop("end_turn")), ResponseOptions{})
	if err != nil || out.delta("reasoning_content") != "hm" || out.delta("content") != "ab" {
		t.Fatalf("%v %+v", err, out)
	}
}

func TestBufferedMatchesStreamed(t *testing.T) {
	names, _ := concerns.NewToolNamesFor(toolNameRule, []string{"my.tool"})
	body := join(event("reasoningContentEvent", `{"text":"think"}`), text("hello"),
		event("toolUseEvent", `{"toolUseId":"t","name":"my_tool","input":"{\"a\":"}`), event("toolUseEvent", `{"toolUseId":"t","name":"my_tool","input":"1}"}`),
		event("metricsEvent", `{"inputTokens":4,"outputTokens":6,"cacheReadInputTokens":1}`), stop("tool_use"))
	options := ResponseOptions{Model: "claude", Names: names}
	streamed, err := stream(t, body, options)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := KiroResponseToChat(body, options)
	if err != nil {
		t.Fatal(err)
	}
	buffered, _ := concerns.WireObject(encoded)
	choice := concerns.Object(concerns.Items(buffered["choices"])[0])
	message := concerns.Object(choice["message"])
	calls := concerns.Items(message["tool_calls"])
	function := concerns.Object(concerns.Object(calls[0])["function"])
	if buffered["object"] != "chat.completion" || buffered["model"] != "claude" || message["role"] != "assistant" ||
		message["content"] != streamed.delta("content") || message["reasoning_content"] != streamed.delta("reasoning_content") ||
		choice["finish_reason"] != streamed.finish() || function["name"] != "my.tool" || function["arguments"] != `{"a":1}` ||
		concerns.Object(calls[0])["id"] != "t" || concerns.Object(calls[0])["type"] != "function" ||
		fmt.Sprint(buffered["usage"]) != fmt.Sprint(streamed.usage()) {
		t.Fatalf("%v\n%+v", buffered, streamed)
	}
	// An empty answer is an empty string, as for the other wires.
	encoded, err = KiroResponseToChat(join(stop("end_turn")), ResponseOptions{})
	if err != nil || !strings.Contains(string(encoded), `"content":""`) {
		t.Fatalf("%s %v", encoded, err)
	}
}

func TestCancellationWritesNothing(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var out bytes.Buffer
	if err := KiroStreamToChat(ctx, bytes.NewReader(join(text("x"), stop(""))), &out, ResponseOptions{}); err == nil || out.Len() != 0 {
		t.Fatalf("err %v wrote %q", err, out.String())
	}
	// Cancelled between frames: the client that left gets no terminal.
	reader, writer := io.Pipe()
	ctx, cancel = context.WithCancel(context.Background())
	locked := &lockedBuffer{signal: make(chan struct{}, 8)}
	done := make(chan error, 1)
	go func() { done <- KiroStreamToChat(ctx, reader, locked, ResponseOptions{}) }()
	_, _ = writer.Write(text("x"))
	cancel()
	_ = reader.Close()
	if err := <-done; err == nil || strings.Contains(locked.String(), "[DONE]") || strings.Contains(locked.String(), `"error"`) {
		t.Fatalf("err %v wrote %q", err, locked.String())
	}
}

func TestSourceErrorIsAFailureNotAnEnd(t *testing.T) {
	failing := io.MultiReader(bytes.NewReader(join(text("x"))), iotest.ErrReader(io.ErrClosedPipe))
	out, err := stream2(t, failing)
	if err == nil || out.finish() != nil || len(out.errors) != 1 {
		t.Fatalf("%v %+v", err, out)
	}
}

func stream2(t *testing.T, src io.Reader) (sse, error) {
	t.Helper()
	var out bytes.Buffer
	err := KiroStreamToChat(context.Background(), src, &out, ResponseOptions{})
	return parseSSE(t, out.String()), err
}

func TestKiroUsage(t *testing.T) {
	usage, observed := concerns.KiroUsage(map[string]any{"inputTokens": int64(10), "outputTokens": int64(5), "cache_read_input_tokens": int64(3), "cache_creation_input_tokens": int64(2)})
	if !observed || usage.Prompt != 15 || usage.Completion != 5 || usage.Total != 20 || usage.Cached != 3 || usage.CacheCreation != 2 {
		t.Fatalf("%+v", usage)
	}
	if _, observed := concerns.KiroUsage(nil); observed {
		t.Fatal("nil is not a measurement")
	}
}

// Unread keys and ambiguous envelopes are resolved the way the pin resolves them: first set
// wins, nothing fails (executors/kiro.js:834-841, :872-888, :906-921).
func TestUnreadKeysAndAmbiguousResponseValuesAreReadAsThePinReadsThem(t *testing.T) {
	for _, kind := range []string{"assistantResponseEvent", "codeEvent", "reasoningContentEvent", "toolUseEvent", "messageStopEvent", "metadataEvent", "metricsEvent", "contextUsageEvent", "meteringEvent"} {
		for _, value := range []string{`"lost"`, `0`, `false`, `["lost"]`, `{"text":"lost"}`} {
			out, err := stream(t, join(event(kind, `{"unread":`+value+`}`), text("t"), stop("end_turn")), ResponseOptions{})
			if err != nil || out.finish() != "stop" {
				t.Errorf("%s unread %s: %v %+v", kind, value, err, out)
			}
		}
	}
	// A metadata envelope is the first truthy of metadataEvent, metadata, the payload (:880-881).
	for _, frame := range [][]byte{
		event("metadataEvent", `{"metadata":{"stopReason":"end_turn"},"stopReason":"refusal"}`),
		event("metricsEvent", `{"metricsEvent":{"inputTokens":1},"inputTokens":2}`),
		event("metricsEvent", `{"metricsEvent":"lost"}`),
	} {
		if out, err := stream(t, join(frame, text("t"), stop("end_turn")), ResponseOptions{}); err != nil || out.finish() != "stop" {
			t.Errorf("%s: %v %+v", frame, err, out)
		}
	}
	if out, err := stream(t, join(text("x"), event("metadataEvent", `{"stopReason":5}`)), ResponseOptions{}); err == nil || out.errors[0]["code"] != "kiro_unknown_stop_reason" {
		t.Errorf("a numeric stop reason is an unknown one: %v %+v", err, out)
	}
	for _, kind := range []string{"messageStopEvent", "metadataEvent"} {
		out, err := stream(t, join(text("x"), event(kind, `{"stopReason":"end_turn","stop_reason":"refusal"}`)), ResponseOptions{})
		if err != nil || out.finish() != "stop" {
			t.Errorf("%s: stopReason ?? stop_reason takes the first: %v %+v", kind, err, out)
		}
	}
	state := newMachine(ResponseOptions{}, func(map[string]any) error { return nil })
	_, err := state.handle(concerns.EventStreamEvent{Headers: map[string]any{":event-type": "unknown"}, Payload: []byte(`{}`)})
	// Counted under "other", as the pin caps the diagnostic's cardinality
	// (executors/kiro.js:806-807, kiro-terminal-integrity.test.js "caps diagnostic event-type cardinality").
	if err != nil || state.counts["other"] != 1 || state.counts["unknown"] != 0 {
		t.Fatalf("an unknown event is counted and ignored: %v %v", err, state.counts)
	}
	for i := 0; i < 99; i++ {
		_, _ = state.handle(concerns.EventStreamEvent{Headers: map[string]any{":event-type": fmt.Sprintf("unknownEvent%d", i)}, Payload: []byte(`{}`)})
	}
	if _, err := state.handle(concerns.EventStreamEvent{Headers: map[string]any{":event-type": "assistantResponseEvent"}, Payload: []byte(`{"content":"done"}`)}); err != nil {
		t.Fatal(err)
	}
	if len(state.counts) != 2 || state.counts["other"] != 100 || state.counts["assistantResponseEvent"] != 1 {
		t.Fatalf("event counts %v", state.counts)
	}
}

func TestMeasuredUsageValueKinds(t *testing.T) {
	for _, key := range []string{"inputTokens", "outputTokens", "cacheReadInputTokens", "cache_read_input_tokens", "cacheCreationInputTokens", "cache_creation_input_tokens"} {
		for _, value := range []string{`null`, `0`, `0.0`, `1`, `1.0`, `1e0`, `-1`, `-0.5`, `0.5`, `1.0000000000000000001`, `"1"`, `false`, `[]`, `{}`, `9223372036854775808`, `1e999`} {
			payload := `{"outputTokens":1,` + fmt.Sprintf("%q:%s", key, value) + `}`
			// Avoid duplicate JSON fields in the outputTokens row.
			if key == "outputTokens" {
				payload = fmt.Sprintf(`{"inputTokens":1,%q:%s}`, key, value)
			}
			out, err := stream(t, join(event("metricsEvent", payload), stop("")), ResponseOptions{})
			want := value == `null` || value == `0` || value == `0.0` || value == `1` || value == `1.0` || value == `1e0`
			if err != nil || (out.usage() != nil) != want {
				t.Errorf("%s=%s: %v %v", key, value, out.usage(), err)
			}
		}
	}
	for _, payload := range []string{
		`{"inputTokens":0,"outputTokens":0}`,
		`{"cacheReadInputTokens":2}`,
		`{"cacheReadInputTokens":0,"cache_read_input_tokens":0}`,
	} {
		out, err := stream(t, join(event("metricsEvent", payload), stop("")), ResponseOptions{})
		if err != nil || out.usage() == nil {
			t.Errorf("measured zero/cache-only: %s: %v", payload, err)
		}
	}
	for _, payload := range []string{
		`{"inputTokens":9223372036854775807,"outputTokens":1}`,
		`{"inputTokens":1,"cacheReadInputTokens":0,"cache_read_input_tokens":2}`,
		`{"inputTokens":1,"cacheCreationInputTokens":0,"cache_creation_input_tokens":false}`,
	} {
		out, err := stream(t, join(event("metricsEvent", payload), stop("")), ResponseOptions{})
		if err != nil || out.usage() != nil {
			t.Errorf("invalid counts: %s: %v %v", payload, out.usage(), err)
		}
	}
}

func BenchmarkKiroStreamToChat(b *testing.B) {
	body := join(text("hello"), event("metricsEvent", `{"inputTokens":10,"outputTokens":2}`), stop("end_turn"))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if err := KiroStreamToChat(context.Background(), bytes.NewReader(body), io.Discard, ResponseOptions{}); err != nil {
			b.Fatal(err)
		}
	}
}

func TestBookkeepingPayloadKinds(t *testing.T) {
	for _, kind := range []string{"metadataEvent", "metricsEvent", "contextUsageEvent", "meteringEvent"} {
		for _, payload := range []string{"", `null`, `{}`, `[]`, `""`, `"lost"`, `0`, `false`, `{oops`, `{} {}`} {
			out, err := stream(t, join(event(kind, payload), text("t"), stop("end_turn")), ResponseOptions{})
			// Only a payload that is not one JSON document ends the answer (:964-967).
			corrupt := payload == `{oops` || payload == `{} {}`
			if corrupt != (err != nil) || !corrupt && out.finish() != "stop" {
				t.Errorf("%s %q: %v", kind, payload, err)
			}
		}
	}
}

// pinMerge is mergeStopReason (executors/kiro.js:170-183) and pinFails is the
// verdict finish() reaches (:1009-1099), restated independently of the code under
// test so the matrix below is a differential check against the pin's own rules.
func pinSeverity(reason string) int {
	switch reason {
	case "cancelled", "pause_turn", "model_context_window_exceeded":
		return 5
	case "refusal", "content_filtered":
		return 6
	case "malformed_model_output":
		return 3
	case "max_tokens":
		return 2
	case "end_turn", "tool_use":
		return 1
	}
	return 4 // something_new: unknown_failure
}

func pinMerge(current, incoming string) string {
	if current == "" || pinSeverity(incoming) > pinSeverity(current) {
		return incoming
	}
	return current
}

func pinFails(merged string, output, calls bool) bool {
	truncation := merged == "max_tokens" || merged == "model_context_window_exceeded"
	var failing bool
	switch merged {
	case "malformed_model_output", "cancelled", "pause_turn", "model_context_window_exceeded", "refusal", "content_filtered", "something_new":
		failing = true
	case "max_tokens":
		failing = calls // stopDisposition: max_tokens with tool calls is terminal_incomplete
	}
	return failing && !(truncation && output)
}

func TestTerminalReasonsCannotHideFailures(t *testing.T) {
	for _, failure := range []string{"refusal", "content_filtered", "cancelled", "pause_turn", "malformed_model_output", "something_new"} {
		for _, other := range []string{"end_turn", "tool_use", "max_tokens", "model_context_window_exceeded"} {
			for _, output := range []bool{false, true} {
				for _, calls := range []bool{false, true} {
					var prefix []byte
					if output {
						prefix = append(prefix, text("x")...)
					}
					if calls {
						prefix = append(prefix, tool("t", "x", `{}`)...)
					}
					for _, order := range [][2]string{{failure, other}, {other, failure}} {
						merged := pinMerge(pinMerge("", order[0]), order[1])
						want := pinFails(merged, output, calls)
						out, err := stream(t, join(prefix, stop(order[0]), stop(order[1])), ResponseOptions{})
						if want != (err != nil) {
							t.Fatalf("%v output=%v calls=%v: pin fails=%v, got err=%v finish=%v", order, output, calls, want, err, out.finish())
						}
					}
				}
			}
		}
	}
}

// An empty codeEvent is output: the pin sets hasCode before it looks at the length
// (executors/kiro.js:842-845) and hasOutput counts it (:1065), so the answer completes. An
// empty assistantResponseEvent is not: hasText needs content (:830).
func TestEmptyCodeEventIsOutputAsAtThePin(t *testing.T) {
	out, err := stream(t, join(event("codeEvent", `{"content":""}`)), ResponseOptions{})
	if err != nil || out.finish() != "stop" {
		t.Fatalf("%v %+v", err, out)
	}
	for name, frame := range map[string][]byte{"empty text": text(""), "no content": event("assistantResponseEvent", `{}`)} {
		out, err := stream(t, join(frame), ResponseOptions{})
		if err == nil || len(out.errors) != 1 || out.errors[0]["details"].(map[string]any)["terminal_provenance"] != "empty_response_eof" {
			t.Errorf("%s: %v %+v", name, err, out)
		}
	}
	// ... and with a tool_use stop and nothing else, an empty codeEvent is the "something else".
	if out, err := stream(t, join(event("codeEvent", `{"content":""}`), stop("tool_use")), ResponseOptions{}); err != nil || out.finish() != "stop" {
		t.Fatalf("%v %+v", err, out)
	}
}
