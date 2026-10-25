package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

type messageEvents []map[string]any

func parseMessageEvents(t *testing.T, raw string) messageEvents {
	t.Helper()
	var events messageEvents
	for _, block := range strings.Split(raw, "\n\n") {
		var name, data string
		for _, line := range strings.Split(strings.TrimSpace(block), "\n") {
			switch {
			case strings.HasPrefix(line, "event: "):
				name = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				data = strings.TrimPrefix(line, "data: ")
			}
		}
		if data == "" {
			continue
		}
		event, err := concerns.WireObject([]byte(data))
		if err != nil {
			t.Fatalf("bad SSE data %q", data)
		}
		if event["type"] != name {
			t.Fatalf("event name %q does not match its type %v", name, event["type"])
		}
		events = append(events, event)
	}
	return events
}

func (e messageEvents) types() string {
	var names []string
	for _, event := range e {
		names = append(names, concerns.Text(event["type"]))
	}
	return strings.Join(names, " ")
}

// message folds the events into the Messages body a client would assemble.
func (e messageEvents) message(t *testing.T) map[string]any {
	t.Helper()
	var message map[string]any
	var content []any
	partial := map[int]string{}
	for _, event := range e {
		index := int(concerns.Int(event["index"]))
		switch event["type"] {
		case "message_start":
			message = concerns.Object(event["message"])
		case "content_block_start":
			block := map[string]any{}
			for key, value := range concerns.Object(event["content_block"]) {
				block[key] = value
			}
			content = append(content, block)
		case "content_block_delta":
			block := concerns.Object(content[index])
			delta := concerns.Object(event["delta"])
			switch delta["type"] {
			case "text_delta":
				block["text"] = concerns.Text(block["text"]) + concerns.Text(delta["text"])
			case "thinking_delta":
				block["thinking"] = concerns.Text(block["thinking"]) + concerns.Text(delta["thinking"])
			case "input_json_delta":
				partial[index] += concerns.Text(delta["partial_json"])
			}
		case "content_block_stop":
			if text, ok := partial[index]; ok {
				input, err := concerns.WireObject([]byte(text))
				if err != nil {
					t.Fatalf("tool input %q: %v", text, err)
				}
				concerns.Object(content[index])["input"] = input
			}
		case "message_delta":
			message["stop_reason"] = concerns.Object(event["delta"])["stop_reason"]
			message["usage"] = event["usage"]
		}
	}
	message["content"] = content
	return message
}

func streamMessages(t *testing.T, body []byte, options ResponseOptions) (messageEvents, string, error) {
	t.Helper()
	var out bytes.Buffer
	err := KiroStreamToMessages(context.Background(), bytes.NewReader(body), &out, options)
	return parseMessageEvents(t, out.String()), out.String(), err
}

func TestMessagesStreamText(t *testing.T) {
	events, _, err := streamMessages(t, join(text("Hel"), text("lo"), event("metricsEvent", `{"inputTokens":10,"outputTokens":5}`), stop("end_turn")), ResponseOptions{Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if events.types() != "message_start content_block_start content_block_delta content_block_delta content_block_stop message_delta message_stop" {
		t.Fatal(events.types())
	}
	message := events.message(t)
	start := concerns.Object(events[0]["message"])
	if !strings.HasPrefix(concerns.Text(start["id"]), "msg_") || start["model"] != "m" || start["role"] != "assistant" {
		t.Fatalf("%v", start)
	}
	if at(message, "content.0.text") != "Hello" || message["stop_reason"] != "end_turn" || concerns.JSONText(message["usage"]) != `{"input_tokens":10,"output_tokens":5}` {
		t.Fatalf("%v", message)
	}
}

func TestMessagesStreamUsage(t *testing.T) {
	events, _, err := streamMessages(t, join(text("x"), event("metricsEvent", `{"inputTokens":10,"outputTokens":5,"cacheReadInputTokens":3,"cacheCreationInputTokens":2}`), stop("")), ResponseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	// Input is what Kiro reported (cache-exclusive), the cache counts stay separate.
	usage := events.message(t)["usage"]
	if concerns.JSONText(usage) != `{"cache_creation_input_tokens":2,"cache_read_input_tokens":3,"input_tokens":10,"output_tokens":5}` {
		t.Fatalf("%v", usage)
	}
	// No measurement is reported as the zeros the Messages shape requires, not invented counts.
	events, _, _ = streamMessages(t, join(text("hello world"), event("contextUsageEvent", `{"contextUsagePercentage":50}`), stop("")), ResponseOptions{})
	if concerns.JSONText(events.message(t)["usage"]) != `{"input_tokens":0,"output_tokens":0}` {
		t.Fatalf("%v", events.message(t)["usage"])
	}
}

func TestMessagesStreamThinking(t *testing.T) {
	body := join(text("<thinking>plan"), text(" it</thinking>\nanswer"), stop(""))
	events, _, err := streamMessages(t, body, ResponseOptions{ThinkingTags: true})
	if err != nil {
		t.Fatal(err)
	}
	message := events.message(t)
	if at(message, "content.0.type") != "thinking" || at(message, "content.0.thinking") != "plan it" || at(message, "content.1.text") != "answer" {
		t.Fatalf("%v", message)
	}
	// Kiro signs nothing, so no signature is invented.
	if strings.Contains(concerns.JSONText(message), "signature") {
		t.Fatalf("%v", message)
	}
	// Without the request having asked for it, the tags are the model's own words.
	events, _, _ = streamMessages(t, body, ResponseOptions{})
	if at(events.message(t), "content.0.type") != "text" || !strings.Contains(concerns.Text(at(events.message(t), "content.0.text")), "<thinking>") {
		t.Fatalf("%v", events.message(t))
	}
	events, _, err = streamMessages(t, join(event("reasoningContentEvent", `{"text":"hmm"}`), text("ok"), stop("")), ResponseOptions{})
	if err != nil || at(events.message(t), "content.0.thinking") != "hmm" || at(events.message(t), "content.1.text") != "ok" {
		t.Fatalf("%v %v", err, events.message(t))
	}
}

func TestMessagesStreamToolCalls(t *testing.T) {
	names, err := concerns.NewToolNamesFor(toolNameRule, []string{"my.tool"})
	if err != nil {
		t.Fatal(err)
	}
	body := join(text("calling"),
		event("toolUseEvent", `{"toolUseId":"t1","name":"my_tool","input":"{\"a\":"}`),
		tool("t2", "other", `{"b":2}`),
		event("toolUseEvent", `{"toolUseId":"t1","name":"my_tool","input":"1}"}`),
		event("toolUseEvent", `{"toolUseId":"t1","name":"my_tool","stop":true}`),
		stop("tool_use"))
	events, _, err := streamMessages(t, body, ResponseOptions{Names: names})
	if err != nil {
		t.Fatal(err)
	}
	message := events.message(t)
	if at(message, "content.0.text") != "calling" || at(message, "content.1.type") != "tool_use" || at(message, "content.1.id") != "t1" || at(message, "content.1.name") != "my.tool" ||
		concerns.JSONText(at(message, "content.1.input")) != `{"a":1}` || at(message, "content.2.name") != "other" || concerns.JSONText(at(message, "content.2.input")) != `{"b":2}` ||
		message["stop_reason"] != "tool_use" {
		t.Fatalf("%v", message)
	}
	// Each block closes before the next opens.
	if events.types() != "message_start content_block_start content_block_delta content_block_stop content_block_start content_block_delta content_block_stop content_block_start content_block_delta content_block_stop message_delta message_stop" {
		t.Fatal(events.types())
	}
	// A call whose input is a blank string is dropped, as the pin's JSON.parse("") drops it
	// (executors/kiro.js:731-735): alone it is the unusable-only failure, beside a usable call it is skipped.
	if _, _, err = streamMessages(t, join(event("toolUseEvent", `{"toolUseId":"t","name":"n","input":""}`), stop("")), ResponseOptions{}); err == nil {
		t.Fatal("a blank-input call was served")
	}
	events, _, err = streamMessages(t, join(event("toolUseEvent", `{"toolUseId":"t","name":"n","input":""}`), tool("u", "m", `{}`), stop("")), ResponseOptions{})
	if err != nil || concerns.JSONText(at(events.message(t), "content.0.input")) != `{}` || at(events.message(t), "content.0.name") != "m" || len(concerns.Items(events.message(t)["content"])) != 1 {
		t.Fatalf("%v %v", err, events.message(t))
	}
}

// The Messages legs complete a clean end with output and no stop event, as the Chat leg does
// (executors/kiro.js:1008-1075).
func TestMessagesCleanEOFWithOutputCompletes(t *testing.T) {
	body := join(text("Complete answer."), event("meteringEvent", `{"usage":2,"unit":"credit"}`))
	events, raw, err := streamMessages(t, body, ResponseOptions{})
	if err != nil || events[len(events)-1]["type"] != "message_stop" {
		t.Fatalf("%v: %s", err, raw)
	}
	if buffered, err := KiroResponseToMessages(body, ResponseOptions{}); err != nil || !strings.Contains(string(buffered), `"stop_reason":"end_turn"`) {
		t.Fatalf("%v %s", err, buffered)
	}
}

func TestMessagesStopReasons(t *testing.T) {
	for name, test := range map[string]struct {
		frames [][]byte
		want   string
	}{
		"end_turn":      {[][]byte{text("x"), stop("end_turn")}, "end_turn"},
		"none":          {[][]byte{text("x"), stop("")}, "end_turn"},
		"stop_sequence": {[][]byte{text("x"), stop("stop_sequence")}, "end_turn"},
		"max_tokens":    {[][]byte{text("x"), stop("max_tokens")}, "max_tokens"},
		"window":        {[][]byte{text("x"), stop("model_context_window_exceeded")}, "max_tokens"},
		"tool_use":      {[][]byte{tool("t", "n", `{}`), stop("tool_use")}, "tool_use"},
		"tool, no stop": {[][]byte{tool("t", "n", `{}`), stop("end_turn")}, "tool_use"},
	} {
		t.Run(name, func(t *testing.T) {
			events, _, err := streamMessages(t, join(test.frames...), ResponseOptions{})
			if err != nil || events.message(t)["stop_reason"] != test.want {
				t.Fatalf("%v %v", err, events.message(t)["stop_reason"])
			}
		})
	}
}

// A failed answer is an error event and no message_stop: a client must never read
// a truncated or refused answer as a finished one.
func TestMessagesStreamFailures(t *testing.T) {
	for name, frames := range map[string][][]byte{
		"cut frame":       {text("partial")[:20]},
		"empty":           {},
		"refusal":         {text("x"), stop("refusal")},
		"cancelled":       {text("x"), stop("cancelled")},
		"unknown":         {text("x"), stop("brand_new")},
		"tool_use no use": {stop("tool_use")}, // with text the pin keeps the text (executors/kiro.js:787-790)
		"max with a call": {tool("t", "n", `{}`), stop("max_tokens")},
		"bad tool input":  {event("toolUseEvent", `{"toolUseId":"t","name":"n","input":"[1]"}`), stop("")},
		"unknown event":   {text("x"), event("mysteryEvent", `{"content":"y"}`), stop("")},
		"upstream error":  {text("x"), rawFrame(append(header(":message-type", 7, "exception"), header(":exception-type", 7, "ThrottlingException")...), `{"message":"slow down"}`)},
	} {
		t.Run(name, func(t *testing.T) {
			events, raw, err := streamMessages(t, join(frames...), ResponseOptions{})
			if err == nil {
				t.Fatalf("expected a failure: %s", raw)
			}
			last := events[len(events)-1]
			if last["type"] != "error" || concerns.Object(last["error"])["message"] == nil {
				t.Fatalf("last event %v in %s", last, events.types())
			}
			if strings.Contains(events.types(), "message_stop") || strings.Contains(raw, "[DONE]") {
				t.Fatalf("a failed answer reads as a finished one: %s", events.types())
			}
		})
	}
	// The upstream's own words reach the client.
	_, raw, _ := streamMessages(t, join(text("x"), rawFrame(append(header(":message-type", 7, "exception"), header(":exception-type", 7, "ThrottlingException")...), `{"message":"slow down"}`)), ResponseOptions{})
	if !strings.Contains(raw, "slow down") {
		t.Fatal(raw)
	}
}

func TestMessagesStreamCancelWritesNothingMore(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var out bytes.Buffer
	cancel()
	err := KiroStreamToMessages(ctx, bytes.NewReader(join(text("x"), stop(""))), &out, ResponseOptions{})
	if err == nil || strings.Contains(out.String(), "event: error") || strings.Contains(out.String(), "message_stop") {
		t.Fatalf("%v %q", err, out.String())
	}
}

func TestMessagesBufferedMatchesStream(t *testing.T) {
	names, _ := concerns.NewToolNamesFor(toolNameRule, []string{"my.tool"})
	for name, frames := range map[string][][]byte{
		"text":      {text("Hel"), text("lo"), event("metricsEvent", `{"inputTokens":10,"outputTokens":5,"cacheReadInputTokens":3}`), stop("end_turn")},
		"thinking":  {text("<thinking>a</thinking>\n\nb"), stop("")},
		"tools":     {text("t"), tool("t1", "my_tool", `{"a":1}`), tool("t2", "other", `{}`), stop("tool_use")},
		"tool only": {tool("t1", "my_tool", `{"a":1}`), stop("")},
		"max":       {text("cut"), stop("max_tokens")},
		"empty":     {stop("")},
	} {
		t.Run(name, func(t *testing.T) {
			options := ResponseOptions{Model: "m", Names: names, ThinkingTags: name == "thinking"}
			events, _, err := streamMessages(t, join(frames...), options)
			if err != nil {
				t.Fatal(err)
			}
			body, err := KiroResponseToMessages(join(frames...), options)
			if err != nil {
				t.Fatal(err)
			}
			streamed, buffered := events.message(t), mustObject(t, body)
			// The ids carry the clock; everything else must agree.
			delete(streamed, "id")
			delete(buffered, "id")
			if name == "empty" && concerns.JSONText(buffered["content"]) != `[{"text":"","type":"text"}]` {
				t.Fatalf("an empty answer is one empty text block in both paths: %v", buffered["content"])
			}
			delete(streamed, "stop_sequence")
			delete(buffered, "stop_sequence")
			a, _ := json.Marshal(streamed)
			b, _ := json.Marshal(buffered)
			if string(a) != string(b) {
				t.Fatalf("stream   %s\nbuffered %s", a, b)
			}
			if buffered["type"] != "message" || buffered["role"] != "assistant" {
				t.Fatalf("%s", b)
			}
		})
	}
}

func mustObject(t *testing.T, body []byte) map[string]any {
	t.Helper()
	object, err := concerns.WireObject(body)
	if err != nil {
		t.Fatal(err)
	}
	return object
}

func TestMessagesBufferedFailures(t *testing.T) {
	for name, frames := range map[string][][]byte{
		"cut frame": {text("x")[:20]},
		"refusal":   {text("x"), stop("refusal")},
		"bad tool":  {event("toolUseEvent", `{"toolUseId":"t","name":"n","input":"[1]"}`), stop("")},
	} {
		if body, err := KiroResponseToMessages(join(frames...), ResponseOptions{}); err == nil {
			t.Errorf("%s: a failed answer was served: %s", name, body)
		}
	}
}

func TestMessagesStreamIsIncremental(t *testing.T) {
	reader, writer := io.Pipe()
	out := &lockedBuffer{signal: make(chan struct{}, 8)}
	done := make(chan error, 1)
	go func() { done <- KiroStreamToMessages(context.Background(), reader, out, ResponseOptions{}) }()
	_, _ = writer.Write(text("first"))
	for !strings.Contains(out.String(), "first") {
		<-out.signal // the text reached the client before the body was complete
	}
	if strings.Contains(out.String(), "message_stop") {
		t.Fatalf("%s", out.String())
	}
	_, _ = writer.Write(stop(""))
	_ = writer.Close()
	if err := <-done; err != nil || !strings.Contains(out.String(), "message_stop") {
		t.Fatalf("%v %s", err, out.String())
	}
}
