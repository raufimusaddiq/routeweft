package ollama

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

func chatStream(t *testing.T, ctx context.Context, upstream string) (events []map[string]any, done bool, err error) {
	t.Helper()
	var out bytes.Buffer
	err = OllamaStreamToChat(ctx, strings.NewReader(upstream), &out, "req-model")
	for _, block := range strings.Split(strings.TrimSpace(out.String()), "\n\n") {
		payload, ok := strings.CutPrefix(block, "data: ")
		switch {
		case !ok:
		case payload == "[DONE]":
			done = true
		default:
			var value map[string]any
			if jsonErr := json.Unmarshal([]byte(payload), &value); jsonErr != nil {
				t.Fatalf("bad event %q", payload)
			}
			delete(value, "created")
			delete(value, "id")
			events = append(events, value)
		}
	}
	return events, done, err
}

func choice(event map[string]any) (delta map[string]any, finish any) {
	c := concerns.Object(concerns.Items(event["choices"])[0])
	return concerns.Object(c["delta"]), c["finish_reason"]
}

func TestStreamIsConvertedLineByLine(t *testing.T) {
	events, done, err := chatStream(t, context.Background(), `{"model":"m1","message":{"role":"assistant","thinking":"hm"},"done":false}
{"model":"m1","message":{"role":"assistant","content":"po"},"done":false}
{"model":"m1","message":{"role":"assistant","content":"ng"},"done":false}
{"model":"m1","message":{"role":"assistant","content":"","tool_calls":[{"id":"call_a","function":{"index":0,"name":"one","arguments":{"x":1}}}]},"done":false}
{"model":"m1","message":{"role":"assistant","content":"","tool_calls":[{"function":{"index":0,"name":"two","arguments":"{\"y\":2}"}}]},"done":false}
{"model":"m1","message":{"role":"assistant","content":"!"},"done":true,"done_reason":"stop","prompt_eval_count":130,"prompt_eval_cached_count":100,"eval_count":42}
`)
	if err != nil || !done {
		t.Fatalf("err=%v done=%v", err, done)
	}
	if delta, _ := choice(events[0]); delta["role"] != nil {
		t.Errorf("the pin sends no role on the first chunk: %v", delta)
	}
	var texts, thinking string
	var calls []any
	for _, event := range events[:len(events)-1] {
		delta, finish := choice(event)
		if finish != nil {
			t.Errorf("finish before the last chunk: %v", event)
		}
		texts += concerns.Text(delta["content"])
		thinking += concerns.Text(delta["reasoning_content"])
		calls = append(calls, concerns.Items(delta["tool_calls"])...)
	}
	// The final line's own text is carried (the pin loses it).
	if texts != "pong!" || thinking != "hm" {
		t.Errorf("content %q thinking %q", texts, thinking)
	}
	want := `[{"index":0,"id":"call_a","type":"function","function":{"name":"one","arguments":"{\"x\":1}"}},
	          {"index":1,"id":"call_msg0_tc1_two","type":"function","function":{"name":"two","arguments":"{\"y\":2}"}}]`
	var expected []any
	_ = json.Unmarshal([]byte(want), &expected)
	if !reflect.DeepEqual(calls, expected) {
		got, _ := json.Marshal(calls)
		t.Errorf("tool calls (two calls on two lines must not share an index): %s", got)
	}
	last := events[len(events)-1]
	if _, finish := choice(last); finish != "tool_calls" {
		t.Errorf("finish %v: a response that made tool calls finishes for them", finish)
	}
	if got, _ := json.Marshal(last["usage"]); string(got) != `{"completion_tokens":42,"prompt_tokens":130,"prompt_tokens_details":{"cached_tokens":100},"total_tokens":172}` {
		t.Errorf("usage %s", got)
	}
	if last["model"] != "m1" {
		t.Errorf("model %v", last["model"])
	}
}

func TestStreamFinishAndUsageRules(t *testing.T) {
	for name, c := range map[string]struct{ line, finish string }{
		"stop":      {`"done_reason":"stop"`, "stop"},
		"length":    {`"done_reason":"length"`, "length"},
		"unknown":   {`"done_reason":"load"`, "stop"},
		"no reason": {`"x":1`, "stop"},
	} {
		t.Run(name, func(t *testing.T) {
			events, _, err := chatStream(t, context.Background(), `{"message":{"content":"a"},"done":false}`+"\n"+`{"done":true,`+c.line+"}\n")
			if err != nil {
				t.Fatal(err)
			}
			last := events[len(events)-1]
			if _, finish := choice(last); finish != c.finish {
				t.Errorf("finish %v", finish)
			}
			// No counts reported means no usage, not a measured zero.
			if _, present := last["usage"]; present {
				t.Errorf("usage invented: %v", last["usage"])
			}
			if last["model"] != "req-model" {
				t.Errorf("model %v", last["model"])
			}
		})
	}
}

func TestStreamFailuresEndWithATerminal(t *testing.T) {
	for name, c := range map[string]struct{ upstream, message string }{
		"truncated":         {`{"message":{"content":"a"},"done":false}` + "\n", "upstream stream ended without a terminal event"},
		"error line":        {`{"message":{"content":"a"},"done":false}` + "\n" + `{"error":"model requires more memory"}` + "\n", "model requires more memory"},
		"unterminated tail": {`{"message":{"content":"a"},"done":false}` + "\n" + `{"message":{"content":"b"},"done":fal`, "upstream stream ended without a terminal event"},
	} {
		t.Run(name, func(t *testing.T) {
			events, done, err := chatStream(t, context.Background(), c.upstream)
			if err == nil || !done {
				t.Fatalf("err=%v done=%v: a broken stream reports the failure and still terminates", err, done)
			}
			failure := concerns.Object(events[len(events)-1]["error"])
			if failure == nil || concerns.Text(failure["message"]) != c.message {
				t.Errorf("failure %v", events[len(events)-1])
			}
		})
	}
}

func TestTheUnterminatedFinalLineIsStillRead(t *testing.T) {
	events, done, err := chatStream(t, context.Background(), `{"message":{"content":"a"},"done":false}`+"\n"+`{"done":true,"done_reason":"stop","eval_count":3,"prompt_eval_count":4}`)
	if err != nil || !done {
		t.Fatalf("err=%v done=%v", err, done)
	}
	if usage := concerns.Object(events[len(events)-1]["usage"]); concerns.Int(usage["total_tokens"]) != 7 {
		t.Errorf("usage %v", usage)
	}
}

func TestCancelledClientGetsNoTerminal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	reader, writer := io.Pipe()
	defer writer.Close()
	var out bytes.Buffer
	result := make(chan error, 1)
	go func() { result <- OllamaStreamToChat(ctx, reader, &out, "m") }()
	go func() {
		_, _ = writer.Write([]byte(`{"message":{"content":"a"},"done":false}` + "\n"))
		cancel()
		_, _ = writer.Write([]byte(`{"message":{"content":"b"},"done":false}` + "\n"))
	}()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("err %v", err)
	}
	if strings.Contains(out.String(), "[DONE]") || strings.Contains(out.String(), "error") {
		t.Errorf("a client that left must get no terminal: %q", out.String())
	}
}

func TestBufferedBodyIsConverted(t *testing.T) {
	body := `{"model":"m1","message":{"role":"assistant","content":"pong","thinking":"hm","tool_calls":[{"function":{"name":"one","arguments":{"x":1}}},{"id":"call_b","function":{"name":"two","arguments":"{}"}}]},"done":true,"done_reason":"stop","prompt_eval_count":130,"prompt_eval_cached_count":100,"eval_count":42}`
	out, err := OllamaResponseToChat([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(out, &got)
	c := concerns.Object(concerns.Items(got["choices"])[0])
	message := concerns.Object(c["message"])
	if c["finish_reason"] != "tool_calls" || message["content"] != "pong" || message["reasoning_content"] != "hm" || got["model"] != "m1" {
		t.Errorf("%s", out)
	}
	calls := concerns.Items(message["tool_calls"])
	first, second := concerns.Object(calls[0]), concerns.Object(calls[1])
	if first["id"] != "call_msg0_tc0_one" || concerns.Int(second["index"]) != 1 || second["id"] != "call_b" || concerns.Object(first["function"])["arguments"] != `{"x":1}` {
		t.Errorf("tool calls %v", calls)
	}
	if usage, _ := json.Marshal(got["usage"]); string(usage) != `{"completion_tokens":42,"prompt_tokens":130,"prompt_tokens_details":{"cached_tokens":100},"total_tokens":172}` {
		t.Errorf("usage %s", usage)
	}
	// An empty answer is an empty string, never a missing field; no counts means
	// no usage; a body that names no model says so.
	out, _ = OllamaResponseToChat([]byte(`{"message":{"role":"assistant","content":""},"done":true,"done_reason":"length"}`))
	got = nil
	_ = json.Unmarshal(out, &got)
	c = concerns.Object(concerns.Items(got["choices"])[0])
	if concerns.Object(c["message"])["content"] != "" || c["finish_reason"] != "length" || got["usage"] != nil || got["model"] != "ollama" {
		t.Errorf("%s", out)
	}
	if _, err := OllamaResponseToChat([]byte(`{"error":"model not found"}`)); err == nil {
		t.Error("an error body is not an empty completion")
	}
	if _, err := OllamaResponseToChat([]byte(`[]`)); err == nil {
		t.Error("a body that is not an object")
	}
}
