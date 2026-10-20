package commandcode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

func streamOf(t *testing.T, ctx context.Context, upstream string, unknown func(string)) (events []map[string]any, done bool, err error) {
	t.Helper()
	var out bytes.Buffer
	err = CommandCodeStreamToChat(ctx, strings.NewReader(upstream), &out, "req-model", unknown)
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

func stream(t *testing.T, upstream string) ([]map[string]any, bool, error) {
	t.Helper()
	return streamOf(t, context.Background(), upstream, nil)
}

func choice(event map[string]any) (delta map[string]any, finish any) {
	c := concerns.Object(concerns.Items(event["choices"])[0])
	return concerns.Object(c["delta"]), c["finish_reason"]
}

// toolCalls folds the streamed tool call deltas the way a Chat client does.
func toolCalls(events []map[string]any) (calls []map[string]any, ids []string) {
	for _, event := range events {
		delta, _ := choice(event)
		for _, raw := range concerns.Items(delta["tool_calls"]) {
			piece := concerns.Object(raw)
			index := int(concerns.Int(piece["index"]))
			for len(calls) <= index {
				calls = append(calls, map[string]any{"name": "", "arguments": ""})
				ids = append(ids, "")
			}
			if id := concerns.Text(piece["id"]); id != "" {
				ids[index] += id
			}
			function := concerns.Object(piece["function"])
			calls[index]["name"] = concerns.Text(calls[index]["name"]) + concerns.Text(function["name"])
			calls[index]["arguments"] = concerns.Text(calls[index]["arguments"]) + concerns.Text(function["arguments"])
		}
	}
	return calls, ids
}

const finishLine = `{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":130,"outputTokens":42,"totalTokens":172}}` + "\n"

func TestStreamIsConvertedLineByLine(t *testing.T) {
	events, done, err := stream(t, `{"type":"start"}
{"type":"start-step"}
{"type":"reasoning-start","id":"r"}
{"type":"reasoning-delta","text":"hm"}
{"type":"reasoning-end","id":"r"}
{"type":"text-start","id":"t"}
{"type":"text-delta","text":"po"}
{"type":"text-delta","delta":"ng"}
{"type":"text-end","id":"t"}
{"type":"tool-input-start","id":"call_a","toolName":"one"}
{"type":"tool-input-delta","id":"call_a","delta":"{\"x\""}
{"type":"tool-input-delta","id":"call_a","delta":":1}"}
{"type":"tool-input-end","id":"call_a"}
{"type":"tool-call","toolCallId":"call_a","toolName":"one","input":{"x":1}}
{"type":"tool-call","toolCallId":"call_b","toolName":"two","input":{"y":2}}
{"type":"provider-metadata"}
{"type":"finish-step","finishReason":"tool-calls","usage":{"inputTokens":1,"outputTokens":1}}
`+`{"type":"finish","finishReason":"stop","totalUsage":{"inputTokens":130,"outputTokens":42,"totalTokens":172,"cachedInputTokens":100,"reasoningTokens":7}}
`)
	if err != nil || !done {
		t.Fatalf("err=%v done=%v", err, done)
	}
	first, _ := choice(events[0])
	if first["role"] != "assistant" || first["reasoning_content"] != "hm" {
		t.Errorf("first chunk = %v", first)
	}
	var text, thinking string
	for position, event := range events {
		delta, _ := choice(event)
		text += concerns.Text(delta["content"])
		thinking += concerns.Text(delta["reasoning_content"])
		if delta["role"] != nil && position > 0 {
			t.Errorf("only the first chunk carries the role: %v", delta)
		}
	}
	if text != "pong" || thinking != "hm" {
		t.Errorf("content %q thinking %q", text, thinking)
	}
	calls, ids := toolCalls(events)
	if len(calls) != 2 || ids[0] != "call_a" || ids[1] != "call_b" || calls[0]["name"] != "one" || calls[0]["arguments"] != `{"x":1}` || calls[1]["arguments"] != `{"y":2}` {
		t.Errorf("calls %v ids %v (the consolidated tool-call must not repeat a streamed call)", calls, ids)
	}
	last := events[len(events)-1]
	_, finish := choice(last)
	if finish != "tool_calls" {
		t.Errorf("finish = %v (the last finish-step wins, as at the pin)", finish)
	}
	// V37: counts as given, the extras beside them and never folded in.
	want := `{"commandcode_observed":{"cached_input_tokens":100,"reasoning_tokens":7},"completion_tokens":42,"prompt_tokens":130,"total_tokens":172}`
	if got, _ := json.Marshal(last["usage"]); string(got) != want {
		t.Errorf("usage = %s", got)
	}
}

// V36: a tool-call whose id differs from its tool-input-start is the same call.
func TestToolCallIDQuirk(t *testing.T) {
	for name, c := range map[string]struct {
		upstream string
		args     []string
		ids      []string
	}{
		"same id": {`{"type":"tool-input-start","id":"a","toolName":"f"}
{"type":"tool-input-delta","id":"a","delta":"{}"}
{"type":"tool-call","toolCallId":"a","toolName":"f","input":{}}
`, []string{"{}"}, []string{"a"}},
		"different id": {`{"type":"tool-input-start","id":"a","toolName":"f"}
{"type":"tool-input-delta","id":"a","delta":"{\"k\":1}"}
{"type":"tool-call","toolCallId":"b","toolName":"f","input":{"k":1}}
`, []string{`{"k":1}`}, []string{"a"}},
		"start with both ids": {`{"type":"tool-input-start","id":"a","toolCallId":"b","toolName":"f"}
{"type":"tool-input-delta","toolCallId":"b","delta":"{}"}
{"type":"tool-call","toolCallId":"b","toolName":"f","input":{}}
`, []string{"{}"}, []string{"b"}},
		"start only, no deltas": {`{"type":"tool-input-start","id":"a","toolName":"f"}
{"type":"tool-call","toolCallId":"a","toolName":"f","input":{"k":1}}
`, []string{`{"k":1}`}, []string{"a"}},
		"start only, different id": {`{"type":"tool-input-start","id":"a","toolName":"f"}
{"type":"tool-call","toolCallId":"b","toolName":"f","input":{"k":1}}
`, []string{`{"k":1}`}, []string{"a"}},
		"id-carrying tool-call": {`{"type":"tool-input-start","id":"a","toolName":"f"}
{"type":"tool-input-delta","id":"a","delta":"{\"k\":1}"}
{"type":"tool-call","id":"a","toolName":"f","input":{"k":1}}
`, []string{`{"k":1}`}, []string{"a"}},
		"start with both ids, delta on id": {`{"type":"tool-input-start","id":"a","toolCallId":"b","toolName":"f"}
{"type":"tool-input-delta","id":"a","delta":"{\"k\":1}"}
`, []string{`{"k":1}`}, []string{"b"}},
		"start with both ids, delta on toolCallId, call on id": {`{"type":"tool-input-start","id":"a","toolCallId":"b","toolName":"f"}
{"type":"tool-input-delta","toolCallId":"b","delta":"{\"k\":1}"}
{"type":"tool-call","id":"a","toolName":"f","input":{"k":1}}
`, []string{`{"k":1}`}, []string{"b"}},
		"start on id, call with both ids": {`{"type":"tool-input-start","id":"a","toolName":"f"}
{"type":"tool-input-delta","id":"a","delta":"{}"}
{"type":"tool-call","id":"a","toolCallId":"b","toolName":"f","input":{}}
{"type":"tool-call","toolCallId":"b","toolName":"f","input":{}}
`, []string{"{}"}, []string{"a"}},
		"id-only tool-call, no start": {`{"type":"tool-call","id":"z","toolName":"f","input":{"k":1}}
{"type":"tool-call","id":"z","toolName":"f","input":{"k":1}}
`, []string{`{"k":1}`}, []string{"z"}},
		"id-less tool-call after a start": {`{"type":"tool-input-start","id":"a","toolName":"f"}
{"type":"tool-input-delta","id":"a","delta":"{\"k\":1}"}
{"type":"tool-call","toolName":"f","input":{"k":1}}
`, []string{`{"k":1}`}, []string{"a"}},
		"id-less tool-call, start only": {`{"type":"tool-input-start","id":"a","toolName":"f"}
{"type":"tool-call","toolName":"f","input":{"k":1}}
`, []string{`{"k":1}`}, []string{"a"}},
		"id-less tool-call of another tool": {`{"type":"tool-input-start","id":"a","toolName":"f"}
{"type":"tool-call","toolName":"g","input":{}}
`, []string{"{}", "{}"}, []string{"a", "call_msg0_tc1_g"}},
		"id-less tool-call, no start": {`{"type":"tool-call","toolName":"f","input":{"k":1}}
`, []string{`{"k":1}`}, []string{"call_msg0_tc0_f"}},
		"two id-less calls, two starts": {`{"type":"tool-input-start","id":"a","toolName":"f"}
{"type":"tool-input-start","id":"b","toolName":"f"}
{"type":"tool-call","toolName":"f","input":{"n":1}}
{"type":"tool-call","toolName":"f","input":{"n":2}}
`, []string{`{"n":1}`, `{"n":2}`}, []string{"a", "b"}},
		"tool-call only": {`{"type":"tool-call","toolCallId":"b","toolName":"f","input":"{\"s\":1}"}
`, []string{`{"s":1}`}, []string{"b"}},
		"tool-call only, no input": {`{"type":"tool-call","toolCallId":"b","toolName":"f"}
`, []string{`{}`}, []string{"b"}},
		"two different tools": {`{"type":"tool-input-start","id":"a","toolName":"f"}
{"type":"tool-call","toolCallId":"b","toolName":"g","input":{}}
{"type":"tool-call","toolCallId":"a2","toolName":"f","input":{"z":1}}
`, []string{`{"z":1}`, `{}`}, []string{"a", "b"}},
		"repeated tool-call": {`{"type":"tool-call","toolCallId":"b","toolName":"f","input":{"k":1}}
{"type":"tool-call","toolCallId":"b","toolName":"f","input":{"k":1}}
`, []string{`{"k":1}`}, []string{"b"}},
	} {
		t.Run(name, func(t *testing.T) {
			events, done, err := stream(t, c.upstream+`{"type":"finish","finishReason":"tool-calls"}`+"\n")
			if err != nil || !done {
				t.Fatalf("err=%v done=%v", err, done)
			}
			calls, ids := toolCalls(events)
			var args []string
			for _, call := range calls {
				args = append(args, concerns.Text(call["arguments"]))
			}
			if !reflect.DeepEqual(args, c.args) || !reflect.DeepEqual(ids, c.ids) {
				t.Errorf("args %v ids %v, want %v %v", args, ids, c.args, c.ids)
			}
			// The buffered leg agrees with the stream.
			body, err := CommandCodeResponseToChat([]byte(c.upstream+`{"type":"finish","finishReason":"tool-calls"}`+"\n"), "req-model", nil)
			if err != nil {
				t.Fatal(err)
			}
			var completion map[string]any
			_ = json.Unmarshal(body, &completion)
			message := concerns.Object(concerns.Object(concerns.Items(completion["choices"])[0])["message"])
			var bufferedArgs, bufferedIDs []string
			for _, raw := range concerns.Items(message["tool_calls"]) {
				call := concerns.Object(raw)
				bufferedIDs = append(bufferedIDs, concerns.Text(call["id"]))
				bufferedArgs = append(bufferedArgs, concerns.Text(concerns.Object(call["function"])["arguments"]))
			}
			if !reflect.DeepEqual(bufferedArgs, c.args) || !reflect.DeepEqual(bufferedIDs, c.ids) {
				t.Errorf("buffered args %v ids %v", bufferedArgs, bufferedIDs)
			}
		})
	}
}

func TestToolInputForAnUnknownCallFails(t *testing.T) {
	_, done, err := stream(t, `{"type":"tool-input-delta","id":"ghost","delta":"{}"}`+"\n"+finishLine)
	if err == nil || !done {
		t.Errorf("err=%v done=%v", err, done)
	}
	// An empty fragment for an unknown call carries nothing.
	if _, _, err := stream(t, `{"type":"tool-input-delta","id":"ghost","delta":""}`+"\n"+finishLine); err != nil {
		t.Errorf("err=%v", err)
	}
}

// Cross product: every event type class x with or without content.
func TestUnknownEventTypes(t *testing.T) {
	for _, c := range []struct {
		event   string
		counted bool
		fails   bool
	}{
		{`{"type":"source","url":"https://x"}`, true, false},
		{`{"type":"raw","rawValue":{"a":1}}`, true, false},
		{`{"type":"file","mediaType":"image/png"}`, true, false},
		{`{"type":"custom","text":""}`, true, false},
		{`{"type":"custom","delta":null}`, true, false},
		{`{"type":"custom","text":"lost"}`, true, true},
		{`{"type":"custom","delta":"lost"}`, true, true},
		{`{"type":"custom","textDelta":"lost"}`, true, true},
		{`{"type":"custom","inputTextDelta":"{"}`, true, true},
		{`{"type":"custom","input":{"a":1}}`, true, true},
		{`{"type":"tool-result","toolCallId":"c","toolName":"f"}`, true, true},
		{`{"type":"tool-error","toolName":"f"}`, true, true},
		{`{"type":"custom","text":7}`, true, true},
		{`{"text":"typeless"}`, true, true},
		{`{}`, true, false},
		{`{"type":"start"}`, false, false},
		{`{"type":"tool-input-end","id":"x"}`, false, false},
		{`{"type":"text-end","id":"x","text":"ignored by design"}`, false, false},
	} {
		t.Run(c.event, func(t *testing.T) {
			var seen []string
			_, done, err := streamOf(t, context.Background(), c.event+"\n"+finishLine, func(kind string) { seen = append(seen, kind) })
			if c.fails == (err == nil) {
				t.Errorf("fails=%v err=%v", c.fails, err)
			}
			if !done {
				t.Error("a failed stream still ends with the marker")
			}
			if counted := len(seen) > 0; counted != c.counted {
				t.Errorf("counted=%v seen=%v", counted, seen)
			}
		})
	}
}

func TestStreamFailures(t *testing.T) {
	for name, c := range map[string]struct{ upstream, want string }{
		"error event":                 {`{"type":"text-delta","text":"a"}` + "\n" + `{"type":"error","error":"rate limit"}` + "\n", "[CommandCode error: rate limit]"},
		"error object":                {`{"type":"error","error":{"message":"x","statusCode":429}}` + "\n", "[CommandCode error: {"},
		"error message":               {`{"type":"error","message":"m"}` + "\n", "[CommandCode error: m]"},
		"error nothing":               {`{"type":"error"}` + "\n", "[CommandCode error: unknown]"},
		"no finish":                   {`{"type":"text-delta","text":"a"}` + "\n", "upstream_incomplete"},
		"empty":                       {``, "upstream_incomplete"},
		"truncated line":              {`{"type":"text-delta","te`, "invalid upstream stream event"},
		"garbage line":                {"not json\n", "upstream_incomplete"},
		"tool finish no call":         {`{"type":"finish","finishReason":"tool-calls"}` + "\n", "upstream_incomplete"},
		"text of wrong type":          {`{"type":"text-delta","text":5}` + "\n", "upstream_incomplete"},
		"reason of wrong type":        {`{"type":"finish-step","finishReason":5}` + "\n", "upstream_incomplete"},
		"finish reason of wrong type": {`{"type":"finish","finishReason":42}` + "\n", "upstream_incomplete"},
		"finish reason object":        {`{"type":"finish","finishReason":{"a":1}}` + "\n", "upstream_incomplete"},
	} {
		t.Run(name, func(t *testing.T) {
			events, done, err := stream(t, c.upstream)
			if err == nil || !done {
				t.Fatalf("a failed stream must end with an error and the marker: err=%v done=%v", err, done)
			}
			last, _ := json.Marshal(events[len(events)-1])
			if !strings.Contains(string(last), `"error"`) {
				t.Errorf("terminal = %s", last)
			}
			if c.want != "invalid upstream stream event" && c.want != "upstream_incomplete" && !strings.Contains(string(last), strings.ReplaceAll(c.want, `"`, `\"`)) {
				t.Errorf("terminal = %s, want %s", last, c.want)
			}
		})
	}
	var failure *concerns.StreamFailure
	if _, _, err := stream(t, `{"type":"error","error":"x"}`+"\n"); !errors.As(err, &failure) {
		t.Errorf("the upstream's own error is the cause: %v", err)
	}
}

func TestFramingAndFinishReasons(t *testing.T) {
	// `data:` framing and [DONE] lines are tolerated, as at the pin.
	events, done, err := stream(t, "data: "+`{"type":"text-delta","text":"a"}`+"\n\n"+"data: [DONE]\n"+"data:"+finishLine)
	if err != nil || !done || len(events) != 2 {
		t.Fatalf("err=%v done=%v events=%d", err, done, len(events))
	}
	// An unterminated last line is read.
	if _, _, err := stream(t, `{"type":"text-delta","text":"a"}`+"\n"+strings.TrimSpace(finishLine)); err != nil {
		t.Errorf("err=%v", err)
	}
	for _, c := range []struct{ step, finish, want string }{
		{``, `"finishReason":"stop"`, "stop"},
		{``, ``, "stop"},
		{``, `"finishReason":"length"`, "length"},
		{``, `"finishReason":"content-filter"`, "content_filter"},
		{``, `"finishReason":"error"`, "stop"},
		{`{"type":"finish-step","finishReason":"length"}` + "\n", `"finishReason":"stop"`, "length"},
		{`{"type":"finish-step","finishReason":"stop"}` + "\n", `"finishReason":"length"`, "stop"},
	} {
		events, _, err := stream(t, `{"type":"text-delta","text":"a"}`+"\n"+c.step+`{"type":"finish"`+strings.TrimSuffix(","+c.finish, ",")+"}\n")
		if err != nil {
			t.Fatal(err)
		}
		if _, finish := choice(events[len(events)-1]); finish != c.want {
			t.Errorf("%q %q: finish %v, want %s", c.step, c.finish, finish, c.want)
		}
	}
	// Tool calls with a "stop" finish stay "stop": the leg does not force it.
	events, _, _ = stream(t, `{"type":"tool-call","toolCallId":"c","toolName":"f","input":{}}`+"\n"+finishLine)
	if _, finish := choice(events[len(events)-1]); finish != "stop" {
		t.Errorf("finish = %v", finish)
	}
}

// V37, and V26/V8 for this wire: usage only when the upstream measured something.
func TestUsage(t *testing.T) {
	for name, c := range map[string]struct{ finish, want string }{
		"plain":          {`"totalUsage":{"inputTokens":3,"outputTokens":4}`, `{"completion_tokens":4,"prompt_tokens":3,"total_tokens":7}`},
		"upstream total": {`"totalUsage":{"inputTokens":3,"outputTokens":4,"totalTokens":9}`, `{"completion_tokens":4,"prompt_tokens":3,"total_tokens":9}`},
		"cached only":    {`"totalUsage":{"inputTokens":3,"outputTokens":4,"cachedInputTokens":2}`, `{"commandcode_observed":{"cached_input_tokens":2},"completion_tokens":4,"prompt_tokens":3,"total_tokens":7}`},
		"reasoning only": {`"totalUsage":{"inputTokens":3,"outputTokens":4,"reasoningTokens":1}`, `{"commandcode_observed":{"reasoning_tokens":1},"completion_tokens":4,"prompt_tokens":3,"total_tokens":7}`},
		"zero extras":    {`"totalUsage":{"inputTokens":3,"outputTokens":4,"cachedInputTokens":0,"reasoningTokens":0}`, `{"completion_tokens":4,"prompt_tokens":3,"total_tokens":7}`},
		"all zero":       {`"totalUsage":{"inputTokens":0,"outputTokens":0,"totalTokens":0}`, ``},
		"empty object":   {`"totalUsage":{}`, ``},
		"not an object":  {`"totalUsage":5`, ``},
		"absent":         {`"x":1`, ``},
		"total only":     {`"totalUsage":{"totalTokens":9}`, `{"completion_tokens":0,"prompt_tokens":0,"total_tokens":9}`},
	} {
		t.Run(name, func(t *testing.T) {
			events, _, err := stream(t, `{"type":"text-delta","text":"a"}`+"\n"+`{"type":"finish",`+c.finish+"}\n")
			if err != nil {
				t.Fatal(err)
			}
			usage, present := events[len(events)-1]["usage"]
			got := ""
			if present {
				encoded, _ := json.Marshal(usage)
				got = string(encoded)
			}
			if got != c.want {
				t.Errorf("usage = %s, want %s", got, c.want)
			}
		})
	}
	// The usage of the last finish-step stands in when `finish` carries none.
	events, _, _ := stream(t, `{"type":"finish-step","usage":{"inputTokens":5,"outputTokens":6}}`+"\n"+`{"type":"finish"}`+"\n")
	if got, _ := json.Marshal(events[len(events)-1]["usage"]); string(got) != `{"completion_tokens":6,"prompt_tokens":5,"total_tokens":11}` {
		t.Errorf("usage = %s", got)
	}
}

func TestCancelledClientGetsNoTerminal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	events, done, err := streamOf(t, ctx, `{"type":"text-delta","text":"a"}`+"\n"+finishLine, nil)
	if !errors.Is(err, context.Canceled) || done || len(events) != 0 {
		t.Errorf("err=%v done=%v events=%v", err, done, events)
	}
}

func TestBufferedBodyIsAggregated(t *testing.T) {
	body, err := CommandCodeResponseToChat([]byte(`{"type":"start"}
{"type":"reasoning-delta","text":"hm"}
{"type":"text-delta","text":"po"}
{"type":"text-delta","text":"ng"}
`+finishLine), "req-model", nil)
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	_ = json.Unmarshal(body, &got)
	choiceValue := concerns.Object(concerns.Items(got["choices"])[0])
	message := concerns.Object(choiceValue["message"])
	if got["object"] != "chat.completion" || got["model"] != "req-model" || message["role"] != "assistant" || message["content"] != "pong" ||
		message["reasoning_content"] != "hm" || message["tool_calls"] != nil || choiceValue["finish_reason"] != "stop" {
		t.Errorf("body = %s", body)
	}
	if usage, _ := json.Marshal(got["usage"]); string(usage) != `{"completion_tokens":42,"prompt_tokens":130,"total_tokens":172}` {
		t.Errorf("usage = %s", usage)
	}
	// Tool calls only: no content key.
	body, _ = CommandCodeResponseToChat([]byte(`{"type":"tool-call","toolCallId":"c","toolName":"f","input":{"a":1}}`+"\n"+`{"type":"finish","finishReason":"tool-calls"}`+"\n"), "", nil)
	_ = json.Unmarshal(body, &got)
	message = concerns.Object(concerns.Object(concerns.Items(got["choices"])[0])["message"])
	if _, hasContent := message["content"]; hasContent || got["model"] != "commandcode" {
		t.Errorf("body = %s", body)
	}
	for name, upstream := range map[string]string{
		"no finish": `{"type":"text-delta","text":"a"}` + "\n", "error": `{"type":"error","error":"x"}` + "\n", "empty": ``,
		"garbage": "nope\n", "unknown content": `{"type":"novel","text":"x"}` + "\n" + finishLine,
	} {
		if _, err := CommandCodeResponseToChat([]byte(upstream), "m", nil); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}
