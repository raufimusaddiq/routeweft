package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// chatSSE frames Chat chunks as the upstream would send them.
func chatSSE(chunks ...string) string {
	var b strings.Builder
	for _, chunk := range chunks {
		b.WriteString("data: " + chunk + "\n\n")
	}
	return b.String()
}

func chunk(delta, finish string) string {
	if finish == "" {
		finish = "null"
	} else {
		finish = `"` + finish + `"`
	}
	return `{"id":"chatcmpl-1","model":"m","choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + finish + `}]}`
}

// toAntigravity runs the leg and returns the decoded events and the error.
func toAntigravity(t *testing.T, ctx context.Context, src string) (events []map[string]any, raw string, err error) {
	t.Helper()
	var out bytes.Buffer
	err = ChatStreamToAntigravity(ctx, strings.NewReader(src), &out)
	raw = out.String()
	for _, block := range strings.Split(strings.TrimSpace(raw), "\n\n") {
		if block == "" {
			continue
		}
		var event map[string]any
		if e := json.Unmarshal([]byte(strings.TrimPrefix(block, "data: ")), &event); e != nil {
			t.Fatalf("event %q: %v", block, e)
		}
		events = append(events, event)
	}
	return events, raw, err
}

func lastResponse(events []map[string]any) map[string]any {
	if len(events) == 0 {
		return nil
	}
	return concerns.Object(events[len(events)-1]["response"])
}

func TestAntigravityStreamText(t *testing.T) {
	src := chatSSE(chunk(`{"role":"assistant"}`, ""), chunk(`{"reasoning_content":"hm"}`, ""), chunk(`{"content":"he"}`, ""), chunk(`{"content":"llo"}`, ""), chunk(`{}`, "stop"), `[DONE]`)
	events, raw, err := toAntigravity(t, context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "[DONE]") {
		t.Error("the Gemini-family stream must not end with [DONE] (utils/stream.js:467)")
	}
	if len(events) != 4 {
		t.Fatalf("%d events: %s", len(events), raw)
	}
	for path, want := range map[string]string{
		"0.response.candidates.0.content.parts.0.thought": `true`, "0.response.candidates.0.content.parts.0.text": `"hm"`,
		"1.response.candidates.0.content.parts.0.text": `"he"`, "2.response.candidates.0.content.parts.0.text": `"llo"`,
		"3.response.candidates.0.finishReason": `"STOP"`, "3.response.candidates.0.content.parts.0.text": `""`,
		"3.response.candidates.0.content.role": `"model"`, "3.response.responseId": `"chatcmpl-1"`, "3.response.modelVersion": `"m"`,
	} {
		if got := asJSON(at(anyList(events), path)); got != want {
			t.Errorf("%s = %s, want %s", path, got, want)
		}
	}
	if at(anyList(events), "3.response.usageMetadata") != nil {
		t.Error("usage was invented for a stream that reported none")
	}
}

// Finish reasons: the pin's four, and anything else a failure (openai-to-antigravity.js:85-94).
func TestAntigravityFinishReasons(t *testing.T) {
	for reason, want := range map[string]string{"stop": "STOP", "length": "MAX_TOKENS", "tool_calls": "STOP", "content_filter": "SAFETY", "function_call": "STOP"} {
		events, _, err := toAntigravity(t, context.Background(), chatSSE(chunk(`{"content":"x"}`, ""), chunk(`{}`, reason), `[DONE]`))
		if err != nil || asJSON(at(lastResponse(events), "candidates.0.finishReason")) != `"`+want+`"` {
			t.Errorf("%s: %v %v", reason, err, events)
		}
	}
	for _, reason := range []string{"mystery", "STOP", "Length", "error"} {
		events, _, err := toAntigravity(t, context.Background(), chatSSE(chunk(`{"content":"x"}`, ""), chunk(`{}`, reason), `[DONE]`))
		if err == nil || events[len(events)-1]["error"] == nil {
			t.Errorf("finish %q was served: %v", reason, events)
		}
		if strings.Contains(asJSON(events), "finishReason") {
			t.Errorf("finish %q produced a finishReason", reason)
		}
	}
	// finish_reason of the wrong type, and an empty one that means "not yet".
	if _, _, err := toAntigravity(t, context.Background(), chatSSE(`{"choices":[{"index":0,"delta":{},"finish_reason":5}]}`, `[DONE]`)); err == nil {
		t.Error("a numeric finish_reason was served")
	}
	events, _, err := toAntigravity(t, context.Background(), chatSSE(chunk(`{"content":"x"}`, ""), `{"choices":[{"index":0,"delta":{},"finish_reason":""}]}`, chunk(`{}`, "stop"), `[DONE]`))
	if err != nil || len(events) != 2 {
		t.Errorf("empty finish_reason: %v %v", err, events)
	}
}

// Tool calls are sent whole at the finish with the id the model gave them; the
// pin dropped the id (:48,65-70).
func TestAntigravityToolCalls(t *testing.T) {
	src := chatSSE(
		chunk(`{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"look","arguments":""}}]}`, ""),
		chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":"}}]}`, ""),
		chunk(`{"tool_calls":[{"index":1,"id":"call_b","function":{"name":"look","arguments":"{}"}}]}`, ""),
		chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"1}"}}]}`, ""),
		chunk(`{}`, "tool_calls"), `[DONE]`)
	events, _, err := toAntigravity(t, context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatalf("calls must wait for the finish: %d events", len(events))
	}
	parts := concerns.Items(at(lastResponse(events), "candidates.0.content.parts"))
	if len(parts) != 2 || asJSON(at(parts[0], "functionCall")) != `{"args":{"q":1},"id":"call_a","name":"look"}` || asJSON(at(parts[1], "functionCall.id")) != `"call_b"` {
		t.Errorf("parts %s", asJSON(parts))
	}
	if asJSON(at(lastResponse(events), "candidates.0.finishReason")) != `"STOP"` {
		t.Error("tool_calls maps to STOP")
	}
	// No id: deterministic, the same every run.
	idless := chatSSE(chunk(`{"tool_calls":[{"index":2,"function":{"name":"look"}}]}`, "tool_calls"), `[DONE]`)
	one, _, _ := toAntigravity(t, context.Background(), idless)
	two, _, _ := toAntigravity(t, context.Background(), idless)
	if asJSON(one) != asJSON(two) || asJSON(at(lastResponse(one), "candidates.0.content.parts.0.functionCall.id")) != `"call_msg0_tc2_look"` {
		t.Errorf("idless call: %s", asJSON(one))
	}
	// Text that came with the calls goes out in the same candidate, before them.
	mixed := chatSSE(chunk(`{"content":"ok","tool_calls":[{"index":0,"id":"c","function":{"name":"f","arguments":"{}"}}]}`, "tool_calls"), `[DONE]`)
	events, _, err = toAntigravity(t, context.Background(), mixed)
	parts = concerns.Items(at(lastResponse(events), "candidates.0.content.parts"))
	if err != nil || len(parts) != 2 || at(parts[0], "text") != "ok" || at(parts[1], "functionCall") == nil {
		t.Errorf("mixed %v %s", err, asJSON(events))
	}
}

// Arguments the pin turned into {} (:62): the cross product of what they may be.
func TestAntigravityToolArgumentsCrossProduct(t *testing.T) {
	huge := strings.Repeat("a", 5<<20)
	cases := map[string]struct {
		args string // JSON-escaped argument text, or "-" for none
		want string // expected args JSON, or "" when it must fail
	}{
		"absent": {`-`, `{}`}, "empty": {`""`, `{}`}, "blank": {`"  "`, `{}`}, "empty object": {`"{}"`, `{}`},
		"populated": {`"{\"a\":1}"`, `{"a":1}`}, "nested": {`"{\"a\":{\"b\":[1,2]}}"`, `{"a":{"b":[1,2]}}`},
		"null": {`"null"`, ``}, "number": {`"5"`, ``}, "string": {`"\"x\""`, ``}, "array": {`"[1]"`, ``}, "bool": {`"true"`, ``},
		"malformed": {`"{\"a\":"`, ``}, "truncated": {`"{\"a\":1"`, ``}, "garbage": {`"nope"`, ``},
		"trailing data": {`"{} {}"`, ``}, "trailing junk": {`"{}x"`, ``},
		"oversized": {`"` + huge + `"`, ``},
	}
	for name, c := range cases {
		fn := `{"name":"f"`
		if c.args != "-" {
			fn += `,"arguments":` + c.args
		}
		fn += `}`
		src := chatSSE(chunk(`{"tool_calls":[{"index":0,"id":"c","function":`+fn+`}]}`, "tool_calls"), `[DONE]`)
		events, _, err := toAntigravity(t, context.Background(), src)
		if c.want == "" {
			if err == nil || events[len(events)-1]["error"] == nil || strings.Contains(asJSON(events), "functionCall") {
				t.Errorf("%s was served: %v", name, asJSON(events))
			}
			continue
		}
		if err != nil || asJSON(at(lastResponse(events), "candidates.0.content.parts.0.functionCall.args")) != c.want {
			t.Errorf("%s: %v %s", name, err, asJSON(events))
		}
	}
	// Shapes of the call itself.
	for name, call := range map[string]string{
		"no name": `{"index":0,"id":"c","function":{"arguments":"{}"}}`, "name number": `{"index":0,"function":{"name":5}}`,
		"id number": `{"index":0,"id":5,"function":{"name":"f"}}`, "negative index": `{"index":-1,"function":{"name":"f"}}`,
		"huge index": `{"index":9999,"function":{"name":"f"}}`, "index string": `{"index":"0","function":{"name":"f"}}`,
		"function string": `{"index":0,"function":"f"}`, "call string": `"f"`, "arguments number": `{"index":0,"function":{"name":"f","arguments":5}}`,
	} {
		src := chatSSE(chunk(`{"tool_calls":[`+call+`]}`, "tool_calls"), `[DONE]`)
		if _, _, err := toAntigravity(t, context.Background(), src); err == nil {
			t.Errorf("%s was served", name)
		}
	}
	if _, _, err := toAntigravity(t, context.Background(), chatSSE(chunk(`{"tool_calls":"x"}`, "stop"), `[DONE]`)); err == nil {
		t.Error("a tool_calls string was served")
	}
}

// Usage that arrives after the finish chunk (stream_options) was lost at the pin
// (:13-17); the finish is held until it is known. Reasoning is counted once.
func TestAntigravityUsage(t *testing.T) {
	usage := `{"id":"chatcmpl-1","choices":[],"usage":{"prompt_tokens":10,"completion_tokens":7,"total_tokens":17,` +
		`"prompt_tokens_details":{"cached_tokens":4},"completion_tokens_details":{"reasoning_tokens":3}}}`
	for name, src := range map[string]string{
		"after the finish":  chatSSE(chunk(`{"content":"x"}`, ""), chunk(`{}`, "stop"), usage, `[DONE]`),
		"with the finish":   chatSSE(chunk(`{"content":"x"}`, ""), strings.Replace(chunk(`{}`, "stop"), `"choices"`, `"usage":`+concerns.JSONText(map[string]any{"prompt_tokens": 10, "completion_tokens": 7, "total_tokens": 17, "prompt_tokens_details": map[string]any{"cached_tokens": 4}, "completion_tokens_details": map[string]any{"reasoning_tokens": 3}})+`,"choices"`, 1), `[DONE]`),
		"before the finish": chatSSE(chunk(`{"content":"x"}`, ""), usage, chunk(`{}`, "stop"), `[DONE]`),
		"no [DONE]":         chatSSE(chunk(`{"content":"x"}`, ""), chunk(`{}`, "stop"), usage),
	} {
		events, _, err := toAntigravity(t, context.Background(), src)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		got := asJSON(at(lastResponse(events), "usageMetadata"))
		want := `{"cachedContentTokenCount":4,"candidatesTokenCount":4,"promptTokenCount":10,"thoughtsTokenCount":3,"totalTokenCount":17}`
		if got != want {
			t.Errorf("%s: usage %s", name, got)
		}
		if strings.Count(asJSON(events), "usageMetadata") != 1 {
			t.Errorf("%s: usage must ride the finish once", name)
		}
	}
	// A total of zero or none is prompt plus completion; reasoning above completion clamps at 0.
	events, _, _ := toAntigravity(t, context.Background(), chatSSE(chunk(`{}`, "stop"), `{"choices":[],"usage":{"prompt_tokens":2,"completion_tokens":1,"completion_tokens_details":{"reasoning_tokens":9}}}`, `[DONE]`))
	if got := asJSON(at(lastResponse(events), "usageMetadata")); got != `{"candidatesTokenCount":0,"promptTokenCount":2,"thoughtsTokenCount":9,"totalTokenCount":3}` {
		t.Errorf("usage %s", got)
	}
}

func TestAntigravityStreamFailures(t *testing.T) {
	for name, src := range map[string]string{
		"two choices":     chatSSE(`{"choices":[{"index":0,"delta":{"content":"a"}},{"index":1,"delta":{"content":"b"}}]}`, `[DONE]`),
		"second choice":   chatSSE(`{"choices":[{"index":1,"delta":{"content":"a"}}]}`, `[DONE]`),
		"choice string":   chatSSE(`{"choices":["x"]}`, `[DONE]`),
		"delta string":    chatSSE(`{"choices":[{"index":0,"delta":"x"}]}`, `[DONE]`),
		"content object":  chatSSE(chunk(`{"content":{}}`, "stop"), `[DONE]`),
		"refusal":         chatSSE(chunk(`{"refusal":"no"}`, "stop"), `[DONE]`),
		"bad image":       chatSSE(chunk(`{"images":[{"image_url":{"url":"https://x/y.png"}}]}`, "stop"), `[DONE]`),
		"after finish":    chatSSE(chunk(`{}`, "stop"), chunk(`{"content":"late"}`, ""), `[DONE]`),
		"text and finish": chatSSE(chunk(`{}`, "stop"), chunk(`{"content":"late"}`, "stop"), `[DONE]`),
		"late tool":       chatSSE(chunk(`{"content":"x"}`, ""), chunk(`{}`, "stop"), chunk(`{"tool_calls":[{"index":0,"id":"late","function":{"name":"f","arguments":"{}"}}]}`, ""), `[DONE]`),
		"tool and finish": chatSSE(chunk(`{}`, "stop"), chunk(`{"tool_calls":[{"index":0,"id":"late","function":{"name":"f","arguments":"{}"}}]}`, "tool_calls"), `[DONE]`),
		"no finish":       chatSSE(chunk(`{"content":"x"}`, ""), `[DONE]`),
		"nothing":         `[DONE]`,
		"truncated":       chatSSE(chunk(`{"content":"x"}`, "")),
		"upstream error":  chatSSE(`{"error":{"message":"boom","code":500}}`),
		"error string":    chatSSE(`{"error":"boom"}`),
		"invalid json":    "data: {nope\n\n",
		"error after ok":  chatSSE(chunk(`{"content":"x"}`, ""), `{"error":{"message":"late"}}`),
		"truncated calls": chatSSE(chunk(`{"tool_calls":[{"index":0,"id":"c","function":{"name":"f","arguments":"{}"}}]}`, "")),
	} {
		events, raw, err := toAntigravity(t, context.Background(), src)
		if err == nil {
			t.Errorf("%s: served: %s", name, raw)
			continue
		}
		if last := events[len(events)-1]; last["error"] == nil || last["response"] != nil {
			t.Errorf("%s: no error terminal: %s", name, raw)
		}
		if strings.Contains(raw, "finishReason") || strings.Contains(raw, "[DONE]") {
			t.Errorf("%s: a failed stream looks finished: %s", name, raw)
		}
	}
	// The upstream's own error object reaches the client as it was sent.
	events, _, _ := toAntigravity(t, context.Background(), chatSSE(`{"error":{"message":"boom","code":500}}`))
	if asJSON(at(anyList(events), "0.error")) != `{"code":500,"message":"boom"}` {
		t.Errorf("error object %s", asJSON(events))
	}
	// A body that ends cleanly after the finish without [DONE] is complete.
	if _, _, err := toAntigravity(t, context.Background(), chatSSE(chunk(`{"content":"x"}`, ""), chunk(`{}`, "stop"))); err != nil {
		t.Errorf("a finished body with no [DONE]: %v", err)
	}
}

// A cancelled client gets no terminal, and a stalled upstream is released.
func TestAntigravityStreamCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	events, raw, err := toAntigravity(t, ctx, chatSSE(chunk(`{"content":"x"}`, ""), chunk(`{}`, "stop"), `[DONE]`))
	if err == nil || len(events) != 0 || raw != "" {
		t.Errorf("a cancelled client was written to: err=%v %q", err, raw)
	}
	reader, writer := io.Pipe()
	ctx, cancel = context.WithCancel(context.Background())
	var out bytes.Buffer
	done := make(chan error, 1)
	go func() { done <- ChatStreamToAntigravity(ctx, reader, &out) }()
	_, _ = writer.Write([]byte(chatSSE(chunk(`{"content":"x"}`, ""))))
	time.Sleep(20 * time.Millisecond)
	cancel()
	_ = writer.Close()
	select {
	case err := <-done:
		if err == nil {
			t.Error("cancelled stream reported success")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("the leg did not return after the client went away")
	}
	if strings.Contains(out.String(), `"error"`) {
		t.Errorf("a terminal was written to a cancelled client: %q", out.String())
	}
}

func TestAntigravityImagesAndNoID(t *testing.T) {
	src := chatSSE(`{"model":"m","choices":[{"index":0,"delta":{"images":[{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}]},"finish_reason":null}]}`, `{"model":"m","choices":[{"index":0,"delta":{},"finish_reason":"stop"}]}`, `[DONE]`)
	events, _, err := toAntigravity(t, context.Background(), src)
	if err != nil || asJSON(at(anyList(events), "0.response.candidates.0.content.parts.0.inlineData")) != `{"data":"QUJD","mimeType":"image/png"}` {
		t.Errorf("image: %v %s", err, asJSON(events))
	}
	if at(anyList(events), "0.response.responseId") != antigravityNoResponseID {
		t.Errorf("response id %v", at(anyList(events), "0.response.responseId"))
	}
}

// The two response legs the pin shares with Gemini read the Cloud Code envelope
// for both wires; the scope they store signatures under is the family scope.
func TestCloudCodeResponseLegsServeBothWires(t *testing.T) {
	object := `{"response":{"candidates":[{"content":{"parts":[{"text":"hi"},{"functionCall":{"id":"c1","name":"lookup","args":{"q":1}},"thoughtSignature":"SIG"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":2,"thoughtsTokenCount":1},"modelVersion":"m","responseId":"r"}}`
	for _, model := range []string{"gemini-3-flash", "claude-sonnet-4-6"} {
		cache := NewSignatures(10, time.Hour)
		options := ResponseOptions{Model: model, Signatures: cache, Scope: "alice|conn"}
		result, err := buffered(t, object, options)
		if err != nil {
			t.Fatal(err)
		}
		for path, want := range map[string]string{
			"choices.0.finish_reason": `"tool_calls"`, "choices.0.message.content": `"hi"`, "choices.0.message.tool_calls.0.id": `"c1"`,
			"usage.prompt_tokens": `3`, "usage.completion_tokens": `3`,
		} {
			if got := asJSON(at(result, path)); got != want {
				t.Errorf("%s %s = %s, want %s", model, path, got, want)
			}
		}
		if _, ok := cache.Get(FamilyScope("alice|conn", model), "c1", "lookup", `{"q":1}`); !ok {
			t.Errorf("%s: the signature was not kept under the family scope", model)
		}
		if _, ok := cache.Get("alice|conn", "c1", "lookup", `{"q":1}`); ok {
			t.Errorf("%s: the signature was kept under the bare scope", model)
		}
	}
}

func anyList(events []map[string]any) []any {
	list := make([]any, len(events))
	for i, event := range events {
		list[i] = event
	}
	return list
}
