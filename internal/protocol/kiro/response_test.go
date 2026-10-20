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
	out, err = stream(t, join(event("toolUseEvent", `[{"toolUseId":"a","name":"x","input":{}},{"toolUseId":"b","name":"y","input":""}]`), stop("tool_use")), ResponseOptions{})
	if err != nil || len(out.calls()) != 2 || out.calls()[1]["arguments"] != "{}" {
		t.Fatalf("%v %v", out.calls(), err)
	}
	// A call without an id gets a stable one.
	out, err = stream(t, join(event("toolUseEvent", `{"name":"x","input":{}}`), stop("tool_use")), ResponseOptions{})
	if err != nil || out.calls()[0]["id"] != "call_msg0_tc0_x" {
		t.Fatalf("%v %v", out.calls(), err)
	}
}

// One unusable call fails the stream and none of that turn's calls are sent.
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
		"null id":           event("toolUseEvent", `{"toolUseId":null,"name":"x","input":{}}`),
		"id a number":       event("toolUseEvent", `{"toolUseId":5,"name":"x","input":{}}`),
		"no input":          event("toolUseEvent", `{"toolUseId":"a","name":"x"}`),
		"null input":        event("toolUseEvent", `{"toolUseId":"a","name":"x","input":null}`),
		"array input":       event("toolUseEvent", `{"toolUseId":"a","name":"x","input":[]}`),
		"number input":      event("toolUseEvent", `{"toolUseId":"a","name":"x","input":5}`),
		"input not json":    event("toolUseEvent", `{"toolUseId":"a","name":"x","input":"{oops"}`),
		"input a json list": event("toolUseEvent", `{"toolUseId":"a","name":"x","input":"[1]"}`),
		"unknown content":   event("toolUseEvent", `{"toolUseId":"a","name":"x","input":{},"extra":"text"}`),
		"wrapper no name":   event("toolUseEvent", `{"toolUseId":"a","name":"tool_call","input":{"arguments":{}}}`),
		"wrapper no args":   event("toolUseEvent", `{"toolUseId":"a","name":"tool_call","input":{"name":"n"}}`),
	}
	for name, bad := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := stream(t, join(text("kept"), tool("good", "x", `{}`), bad, stop("tool_use")), ResponseOptions{})
			if err == nil || len(out.errors) != 1 || out.finish() != nil || len(out.calls()) != 0 {
				t.Fatalf("err %v %+v", err, out)
			}
			if out.delta("content") != "kept" {
				t.Fatalf("streamed text was retracted: %q", out.delta("content"))
			}
		})
	}
	for name, frames := range map[string][][]byte{
		"name changes":   {tool("a", "x", `"{"`), tool("a", "y", `"}"`)},
		"kind changes":   {event("toolUseEvent", `{"toolUseId":"a","name":"x","input":"{"}`), event("toolUseEvent", `{"toolUseId":"a","name":"x","input":{}}`)},
		"object to text": {tool("a", "x", `{}`), event("toolUseEvent", `{"toolUseId":"a","name":"x","input":"{}"}`)},
	} {
		if out, err := stream(t, join(append(frames, stop("tool_use"))...), ResponseOptions{}); err == nil || out.finish() != nil {
			t.Errorf("%s: %v", name, err)
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
		{"tool_use with none", [][]byte{text("x"), stop("tool_use")}, ""},
		{"no reason after a tool event", [][]byte{tool("a", "x", `{}`), stop("")}, "tool_calls"},
		{"context window after output", [][]byte{text("x"), stop("model_context_window_exceeded")}, "length"},
		{"context window before output", [][]byte{stop("model_context_window_exceeded")}, ""},
		{"context window with a call", [][]byte{text("x"), tool("a", "x", `{}`), stop("model_context_window_exceeded")}, ""},
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

// The pin counts a clean EOF as success when output arrived (executors/kiro.js:1065-1075);
// this does not.
func TestNoTerminalIsAFailure(t *testing.T) {
	cases := map[string][]byte{
		"empty body":              nil,
		"text only":               join(text("x")),
		"metadata without a stop": join(text("x"), event("metadataEvent", `{"tokenUsage":{}}`)),
		"usage only":              join(event("metricsEvent", `{"inputTokens":1,"outputTokens":1}`)),
		"truncated frame":         join(text("x"), stop("end_turn"))[:len(join(text("x"), stop("end_turn")))-3],
		"trailing garbage":        append(join(text("x"), stop("end_turn")), 1, 2, 3),
		"corrupt after stop":      append(join(text("x"), stop("end_turn")), make([]byte, 20)...),
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			out, err := stream(t, body, ResponseOptions{})
			if err == nil || out.finish() != nil || len(out.errors) != 1 {
				t.Fatalf("err %v %+v", err, out)
			}
			if _, err := KiroResponseToChat(body, ResponseOptions{}); err == nil {
				t.Fatal("the buffered leg must fail too")
			}
		})
	}
	// Fragmenting the transport changes nothing.
	body := join(text("a"), stop("end_turn"))
	var buffer bytes.Buffer
	if err := KiroStreamToChat(context.Background(), iotest.OneByteReader(bytes.NewReader(body)), &buffer, ResponseOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := KiroStreamToChat(context.Background(), iotest.OneByteReader(bytes.NewReader(body[:len(body)-1])), io.Discard, ResponseOptions{}); err == nil {
		t.Fatal("expected a failure")
	}
}

func TestEventsAfterTheStop(t *testing.T) {
	for name, frame := range map[string][]byte{
		"text":      text("late"),
		"code":      event("codeEvent", `{"content":"late"}`),
		"reasoning": event("reasoningContentEvent", `{"text":"late"}`),
		"tool":      tool("a", "x", `{}`),
	} {
		out, err := stream(t, join(text("x"), stop("end_turn"), frame), ResponseOptions{})
		if err == nil || out.finish() != nil {
			t.Errorf("%s: content after the stop must fail: %+v", name, out)
		}
	}
	// Usage and bookkeeping after the stop are normal.
	out, err := stream(t, join(text("x"), stop("end_turn"), event("metricsEvent", `{"inputTokens":3,"outputTokens":4}`), event("meteringEvent", `{"usage":1}`), event("contextUsageEvent", `{}`)), ResponseOptions{})
	if err != nil || out.finish() != "stop" || out.usage() == nil {
		t.Fatalf("%v %+v", err, out)
	}
}

func TestUnknownEvents(t *testing.T) {
	cases := []struct {
		name  string
		frame []byte
		ok    bool
	}{
		{"harmless contextUsageEvent", event("contextUsageEvent", `{"contextUsagePercentage":12.5}`), true},
		{"harmless meteringEvent", event("meteringEvent", `{"unit":"credit","usage":0.1}`), true},
		{"harmless metricsEvent", event("metricsEvent", `{"inputTokens":1,"outputTokens":2}`), true},
		{"metadata without a stop", event("metadataEvent", `{"tokenUsage":{"outputTokens":3}}`), true},
		{"unknown, empty payload", event("brandNewEvent", ``), false},
		{"unknown, empty object", event("brandNewEvent", `{}`), false},
		{"unknown, null", event("brandNewEvent", `null`), false},
		{"unknown, empty fields", event("brandNewEvent", `{"a":"","b":[],"c":{},"d":null}`), false},
		{"unknown with text", event("brandNewEvent", `{"content":"lost?"}`), false},
		{"unknown with a string", event("followupPromptEvent", `"hello"`), false},
		{"unknown with a number", event("brandNewEvent", `{"n":0}`), false},
		{"unknown with a list", event("supplementaryWebLinksEvent", `{"links":[{"url":"u"}]}`), false},
		{"unknown with false", event("brandNewEvent", `false`), false},
		{"no event type", rawFrame(header(":message-type", 7, "event"), `{"content":"x"}`), false},
		{"empty event type", rawFrame(header(":event-type", 7, ""), `{}`), false},
		{"event type not a string", rawFrame([]byte{11, ':', 'e', 'v', 'e', 'n', 't', '-', 't', 'y', 'p', 'e', 0}, `{}`), false},
		{"payload not json", event("brandNewEvent", `{oops`), false},
		{"payload with trailing data", event("brandNewEvent", `{} {}`), false},
		{"known event with an unread key", event("assistantResponseEvent", `{"content":"x","extra":"y"}`), false},
		{"known event with an empty unread key", event("assistantResponseEvent", `{"content":"x","extra":""}`), true},
		{"codeEvent with an unread key", event("codeEvent", `{"content":"x","language":"go"}`), false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			out, err := stream(t, join(c.frame, text("t"), stop("end_turn")), ResponseOptions{})
			if c.ok != (err == nil && out.finish() == "stop") {
				t.Fatalf("err %v %+v", err, out)
			}
			if !c.ok && len(out.errors) != 1 {
				t.Fatalf("%+v", out)
			}
		})
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

func TestKnownEventPayloadShapes(t *testing.T) {
	type row struct {
		payload string
		ok      bool
		content string
	}
	rows := map[string][]row{
		"assistantResponseEvent": {
			{``, true, ""}, {`{}`, true, ""}, {`{"content":""}`, true, ""}, {`{"content":null}`, true, ""}, {`{"content":"hi"}`, true, "hi"},
			{`null`, false, ""}, {`"hi"`, false, ""}, {`5`, false, ""}, {`[]`, false, ""}, {`true`, false, ""}, {`{"content":5}`, false, ""}, {`{"content":{}}`, false, ""}, {`{"content":["a"]}`, false, ""},
		},
		"codeEvent": {
			{``, true, ""}, {`{}`, true, ""}, {`{"content":"c"}`, true, "c"}, {`null`, false, ""}, {`"c"`, false, ""}, {`{"content":false}`, false, ""},
		},
	}
	for kind, list := range rows {
		for _, r := range list {
			out, err := stream(t, join(event(kind, r.payload), stop("end_turn")), ResponseOptions{})
			if r.ok != (err == nil) {
				t.Errorf("%s %s: err %v", kind, r.payload, err)
				continue
			}
			if r.ok && out.delta("content") != r.content {
				t.Errorf("%s %s: content %q", kind, r.payload, out.delta("content"))
			}
		}
	}
	reasoning := []row{
		{``, true, ""}, {`{}`, true, ""}, {`null`, true, ""}, {`"thought"`, true, "thought"}, {`{"text":"a"}`, true, "a"}, {`{"content":"b"}`, true, "b"},
		{`{"reasoningContentEvent":"c"}`, true, "c"}, {`{"reasoningContentEvent":{"text":"d"}}`, true, "d"}, {`{"reasoningContentEvent":{"content":"e"}}`, true, "e"},
		{`{"reasoningContentEvent":{"text":""}}`, true, ""}, {`{"text":5}`, false, ""}, {`{"reasoningContentEvent":5}`, false, ""}, {`{"reasoningContentEvent":{"text":false}}`, false, ""},
		{`5`, false, ""}, {`[]`, false, ""}, {`{"signature":"s"}`, false, ""}, {`{"reasoningContentEvent":{"text":"x","redactedContent":"r"}}`, false, ""},
	}
	for _, r := range reasoning {
		out, err := stream(t, join(event("reasoningContentEvent", r.payload), stop("end_turn")), ResponseOptions{})
		if r.ok != (err == nil) {
			t.Errorf("reasoning %s: err %v", r.payload, err)
			continue
		}
		if r.ok && out.delta("reasoning_content") != r.content {
			t.Errorf("reasoning %s: %q", r.payload, out.delta("reasoning_content"))
		}
	}
	for _, payload := range []string{`null`, `"x"`, `5`, `[]`, `{"stopReason":5}`, `{"stopReason":"end_turn","extra":"y"}`} {
		if _, err := stream(t, join(text("x"), event("messageStopEvent", payload)), ResponseOptions{}); err == nil {
			t.Errorf("messageStopEvent %s must fail", payload)
		}
	}
	for _, payload := range []string{``, `{}`, `{"stop_reason":"end_turn"}`} {
		if out, err := stream(t, join(text("x"), event("messageStopEvent", payload)), ResponseOptions{}); err != nil || out.finish() != "stop" {
			t.Errorf("messageStopEvent %q: %v", payload, err)
		}
	}
}

func TestUpstreamExceptionFrames(t *testing.T) {
	for _, kind := range []string{"exception", "error"} {
		frame := rawFrame(append(header(":message-type", 7, kind), header(":exception-type", 7, "ThrottlingException")...), `{"message":"slow down"}`)
		out, err := stream(t, join(text("x"), frame, stop("end_turn")), ResponseOptions{})
		if err == nil || out.finish() != nil || len(out.errors) != 1 || out.errors[0]["message"] != "slow down" || out.errors[0]["code"] != "upstream_eventstream_error" {
			t.Fatalf("%s: %v %+v", kind, err, out)
		}
	}
	frame := rawFrame(header(":message-type", 7, "exception"), ``)
	if out, err := stream(t, frame, ResponseOptions{}); err == nil || len(out.errors) != 1 {
		t.Fatalf("%v %+v", err, out)
	}
	if out, err := stream(t, rawFrame(header(":message-type", 7, "mystery"), `{}`), ResponseOptions{}); err == nil || len(out.errors) != 1 {
		t.Fatalf("%v %+v", err, out)
	}
	if out, err := stream(t, rawFrame(append(header(":message-type", 6, "event"), header(":event-type", 7, "x")...), `{}`), ResponseOptions{}); err == nil {
		t.Fatalf("a :message-type that is not a string must fail: %+v", out)
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
	out, err := stream(t, join(text("ok <thin")), ResponseOptions{ThinkingTags: true})
	if err == nil || out.finish() != nil {
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

func TestUnreadKeysAndAmbiguousResponseValuesFail(t *testing.T) {
	for _, kind := range []string{"assistantResponseEvent", "codeEvent", "reasoningContentEvent", "toolUseEvent", "messageStopEvent", "metadataEvent", "metricsEvent", "contextUsageEvent", "meteringEvent"} {
		for _, value := range []string{`"lost"`, `0`, `false`, `["lost"]`, `{"text":"lost"}`} {
			out, err := stream(t, join(event(kind, `{"unread":`+value+`}`), stop("")), ResponseOptions{})
			if err == nil || out.finish() != nil {
				t.Errorf("%s unread %s succeeded", kind, value)
			}
		}
	}
	for _, frame := range [][]byte{
		event("metadataEvent", `{"stopReason":5}`),
		event("metadataEvent", `{"metadata":{"stopReason":"end_turn"},"stopReason":"refusal"}`),
		event("metricsEvent", `{"metricsEvent":{"inputTokens":1},"inputTokens":2}`),
		event("metricsEvent", `{"metricsEvent":"lost"}`),
		event("reasoningContentEvent", `{"text":"one","content":"two"}`),
		event("reasoningContentEvent", `{"reasoningContentEvent":"one","text":"two"}`),
		event("toolUseEvent", `{"toolUseId":"t","name":"x","input":{},"stop":"yes"}`),
		join(tool("t", "x", `{"a":1}`), tool("t", "x", `{"b":2}`)),
	} {
		if out, err := stream(t, join(frame, stop("")), ResponseOptions{}); err == nil || out.finish() != nil {
			t.Errorf("ambiguous event succeeded: %+v", out)
		}
	}
	for _, kind := range []string{"messageStopEvent", "metadataEvent"} {
		out, err := stream(t, join(text("x"), event(kind, `{"stopReason":"end_turn","stop_reason":"refusal"}`)), ResponseOptions{})
		if err == nil || out.finish() != nil {
			t.Errorf("%s hid refusal", kind)
		}
	}
	state := newMachine(ResponseOptions{}, func(map[string]any) error { return nil })
	_, err := state.handle(concerns.EventStreamEvent{Headers: map[string]any{":event-type": "unknown"}, Payload: []byte(`{}`)})
	if err == nil || state.counts["unknown"] != 1 {
		t.Fatal("unproven events must be counted and fail")
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
			out, err := stream(t, join(event(kind, payload), stop("")), ResponseOptions{})
			valid := payload == "" || payload == `null` || payload == `{}`
			if (err == nil) != valid || !valid && out.finish() != nil {
				t.Errorf("%s %q: %v", kind, payload, err)
			}
		}
	}
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
					for _, pair := range [][]byte{join(stop(failure), stop(other)), join(stop(other), stop(failure))} {
						out, err := stream(t, join(prefix, pair), ResponseOptions{})
						if err == nil || out.finish() != nil {
							t.Fatalf("%s vs %s output=%v calls=%v hid failure", failure, other, output, calls)
						}
					}
				}
			}
		}
	}
	for _, pair := range [][]byte{join(stop("end_turn"), stop("tool_use")), join(stop("tool_use"), stop("end_turn"))} {
		if _, err := stream(t, join(text("x"), pair), ResponseOptions{}); err == nil {
			t.Fatal("end_turn hid missing call")
		}
	}
}
