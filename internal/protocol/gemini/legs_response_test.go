package gemini

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

const callCandidate = `{"candidates":[{"content":{"role":"model","parts":[{"text":"pong"},{"functionCall":{"name":"lookup","args":{"city":"Oslo"}},"thoughtSignature":"SIG"}]},"finishReason":"STOP"}],` +
	`"usageMetadata":{"promptTokenCount":130,"cachedContentTokenCount":100,"candidatesTokenCount":30,"thoughtsTokenCount":12,"totalTokenCount":172},"modelVersion":"gemini-x","responseId":"r1"}`

func buffered(t *testing.T, body string, options ResponseOptions) (map[string]any, error) {
	t.Helper()
	out, err := GeminiResponseToChat([]byte(body), options)
	if err != nil {
		return nil, err
	}
	result, err := concerns.WireObject(out)
	if err != nil {
		t.Fatal(err)
	}
	return result, nil
}

// streamed runs the stream leg and returns the Chat chunks (the [DONE] marker
// excluded), whether the marker was written, and the leg's error.
func streamed(t *testing.T, ctx context.Context, src string, options ResponseOptions) (chunks []map[string]any, done bool, err error) {
	t.Helper()
	var out bytes.Buffer
	err = GeminiStreamToChat(ctx, strings.NewReader(src), &out, options)
	for _, event := range strings.Split(out.String(), "\n\n") {
		payload := strings.TrimPrefix(event, "data: ")
		switch {
		case payload == "":
		case payload == "[DONE]":
			done = true
		default:
			chunk, parseErr := concerns.WireObject([]byte(payload))
			if parseErr != nil {
				t.Fatalf("bad event %q: %v", event, parseErr)
			}
			chunks = append(chunks, chunk)
		}
	}
	return chunks, done, err
}

func sse(objects ...string) string {
	var out strings.Builder
	for _, object := range objects {
		out.WriteString("data: " + object + "\n\n")
	}
	return out.String()
}

func TestBufferedResponse(t *testing.T) {
	result, err := buffered(t, callCandidate, ResponseOptions{Model: "requested"})
	if err != nil {
		t.Fatal(err)
	}
	for path, want := range map[string]string{
		"model":                     `"gemini-x"`,
		"id":                        `"chatcmpl-r1"`,
		"object":                    `"chat.completion"`,
		"choices.0.message.content": `"pong"`,
		"choices.0.message.tool_calls.0.function.name":      `"lookup"`,
		"choices.0.message.tool_calls.0.function.arguments": `"{\"city\":\"Oslo\"}"`,
		"choices.0.message.tool_calls.0.id":                 `"call_msg0_tc0_lookup"`,
		"choices.0.message.tool_calls.0.index":              `null`,
		"choices.0.finish_reason":                           `"tool_calls"`,
		"usage.prompt_tokens":                               `130`,
		"usage.completion_tokens":                           `42`,
		"usage.total_tokens":                                `172`,
		"usage.prompt_tokens_details.cached_tokens":         `100`,
		"usage.completion_tokens_details.reasoning_tokens":  `12`,
	} {
		if got := asJSON(at(result, path)); got != want {
			t.Errorf("%s = %s, want %s", path, got, want)
		}
	}
}

// The upstream's own figures decide what is observed: nothing positive is no
// usage at all, never a measured zero.
func TestGeminiUsage(t *testing.T) {
	for name, c := range map[string]struct {
		raw                       string
		observed                  bool
		prompt, completion, total int64
	}{
		"full":                   {`{"promptTokenCount":10,"candidatesTokenCount":5,"thoughtsTokenCount":2,"totalTokenCount":17}`, true, 10, 7, 17},
		"candidates derived":     {`{"promptTokenCount":10,"thoughtsTokenCount":2,"totalTokenCount":17}`, true, 10, 7, 17},
		"derivation floors":      {`{"promptTokenCount":10,"thoughtsTokenCount":20,"totalTokenCount":12}`, true, 10, 20, 12},
		"total missing":          {`{"promptTokenCount":10,"candidatesTokenCount":5}`, true, 10, 5, 15},
		"prompt only":            {`{"promptTokenCount":10}`, true, 10, 0, 10},
		"all zero":               {`{"promptTokenCount":0,"candidatesTokenCount":0,"totalTokenCount":0}`, false, 0, 0, 0},
		"empty":                  {`{}`, false, 0, 0, 0},
		"strings are not counts": {`{"promptTokenCount":"10"}`, false, 0, 0, 0},
	} {
		usage, observed := concerns.GeminiUsage(mustDecode(t, c.raw).(map[string]any))
		if observed != c.observed || usage.Prompt != c.prompt || usage.Completion != c.completion || usage.Total != c.total {
			t.Errorf("%s: %+v observed=%v", name, usage, observed)
		}
	}
	result, err := buffered(t, `{"candidates":[{"content":{"parts":[{"text":"x"}]},"finishReason":"STOP"}],"usageMetadata":{}}`, ResponseOptions{})
	if err != nil || result["usage"] != nil {
		t.Errorf("an empty usageMetadata became usage: %v %v", result["usage"], err)
	}
}

// Finish reasons, crossed with what the response holds and with the buffered and
// the streamed leg. A reason outside the enumerated set is a failure on every
// path, never a quiet "stop".
func TestFinishReasonsAreCompleteOrAFailure(t *testing.T) {
	answers := map[string]string{
		"STOP": "stop", "stop": "stop", "MAX_TOKENS": "length", "SAFETY": "content_filter", "RECITATION": "content_filter",
		"BLOCKLIST": "content_filter", "PROHIBITED_CONTENT": "content_filter", "OTHER": "stop",
	}
	failures := []string{"SPII", "IMAGE_SAFETY", "LANGUAGE", "MALFORMED_FUNCTION_CALL", "UNEXPECTED_TOOL_CALL",
		"FINISH_REASON_UNSPECIFIED", "IMAGE_PROHIBITED_CONTENT", "NO_IMAGE", "TOKEN_LIMIT", "SOMETHING_NEW", "lowercase_new"}
	contents := map[string]string{
		"text": `{"parts":[{"text":"hi"}]}`,
		"call": `{"parts":[{"functionCall":{"name":"f","args":{}}}]}`,
		"none": `{"parts":[]}`,
		"omit": ``,
	}
	for reason, want := range answers {
		for name, content := range contents {
			object := fmt.Sprintf(`{"candidates":[{%s"finishReason":%q}]}`, contentField(content), reason)
			wantFinish := want
			if name == "call" && want == "stop" {
				wantFinish = "tool_calls"
			}
			t.Run("buffered/"+reason+"/"+name, func(t *testing.T) {
				result, err := buffered(t, object, ResponseOptions{})
				if err != nil {
					t.Fatal(err)
				}
				if got := at(result, "choices.0.finish_reason"); got != wantFinish {
					t.Fatalf("finish_reason = %v, want %s", got, wantFinish)
				}
			})
			t.Run("stream/"+reason+"/"+name, func(t *testing.T) {
				chunks, done, err := streamed(t, context.Background(), sse(object), ResponseOptions{})
				if err != nil || !done {
					t.Fatalf("err=%v done=%v", err, done)
				}
				if got := at(chunks[len(chunks)-1], "choices.0.finish_reason"); got != wantFinish {
					t.Fatalf("finish_reason = %v, want %s", got, wantFinish)
				}
			})
		}
	}
	for _, reason := range failures {
		for name, content := range contents {
			object := fmt.Sprintf(`{"candidates":[{%s"finishReason":%q,"finishMessage":"why"}]}`, contentField(content), reason)
			t.Run("buffered/"+reason+"/"+name, func(t *testing.T) {
				_, err := buffered(t, object, ResponseOptions{})
				var failure *concerns.StreamFailure
				if !errors.As(err, &failure) || !strings.Contains(concerns.Text(failure.Value["message"]), reason) {
					t.Fatalf("not a failure naming the reason: %v", err)
				}
			})
			t.Run("stream/"+reason+"/"+name, func(t *testing.T) {
				chunks, done, err := streamed(t, context.Background(), sse(object), ResponseOptions{})
				if err == nil || !done {
					t.Fatalf("a failure must end the stream with the error and the marker: err=%v done=%v", err, done)
				}
				last := chunks[len(chunks)-1]
				if last["error"] == nil || last["choices"] != nil {
					t.Fatalf("last event is not a failure: %s", asJSON(last))
				}
			})
		}
	}
}

func contentField(content string) string {
	if content == "" {
		return ""
	}
	return `"content":` + content + `,`
}

func TestBlockedPromptIsAFilteredAnswer(t *testing.T) {
	blocked := `{"promptFeedback":{"blockReason":"SAFETY"},"usageMetadata":{"promptTokenCount":9}}`
	result, err := buffered(t, blocked, ResponseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if at(result, "choices.0.finish_reason") != "content_filter" || at(result, "choices.0.message.content") != "" || at(result, "usage.prompt_tokens") != float64(9) && asJSON(at(result, "usage.prompt_tokens")) != "9" {
		t.Errorf("buffered: %s", asJSON(result))
	}
	chunks, done, err := streamed(t, context.Background(), sse(blocked), ResponseOptions{})
	if err != nil || !done || at(chunks[len(chunks)-1], "choices.0.finish_reason") != "content_filter" {
		t.Errorf("stream: err=%v done=%v %s", err, done, asJSON(chunks))
	}
	// No candidate and no reason it is missing is a failure, not an empty answer.
	for _, body := range []string{`{}`, `{"candidates":[]}`, `{"promptFeedback":{}}`, `{"usageMetadata":{"promptTokenCount":1}}`} {
		if _, err := buffered(t, body, ResponseOptions{}); err == nil {
			t.Errorf("buffered %s served", body)
		}
		if _, done, err := streamed(t, context.Background(), sse(body), ResponseOptions{}); err == nil || !done {
			t.Errorf("stream %s: err=%v done=%v", body, err, done)
		}
	}
}

func TestUnknownPartsAndErrorsFail(t *testing.T) {
	for name, part := range map[string]string{
		"executableCode":      `{"executableCode":{"language":"PYTHON","code":"1"}}`,
		"codeExecutionResult": `{"codeExecutionResult":{"outcome":"OK"}}`,
		"fileData":            `{"fileData":{"fileUri":"gs://x"}}`,
		"functionResponse":    `{"functionResponse":{"name":"f"}}`,
		"mixed with text":     `{"text":"a","executableCode":{}}`,
	} {
		object := `{"candidates":[{"content":{"parts":[` + part + `]},"finishReason":"STOP"}]}`
		if _, err := buffered(t, object, ResponseOptions{}); err == nil {
			t.Errorf("%s: buffered served", name)
		}
		if _, done, err := streamed(t, context.Background(), sse(object), ResponseOptions{}); err == nil || !done {
			t.Errorf("%s: stream err=%v done=%v", name, err, done)
		}
	}
	// A part that is nothing is nothing.
	if _, err := buffered(t, `{"candidates":[{"content":{"parts":[{}]},"finishReason":"STOP"}]}`, ResponseOptions{}); err != nil {
		t.Errorf("empty part: %v", err)
	}
	for name, body := range map[string]string{
		"error object": `{"error":{"code":429,"message":"quota","status":"RESOURCE_EXHAUSTED"}}`,
		"error string": `{"error":"down"}`,
	} {
		_, err := buffered(t, body, ResponseOptions{})
		if err == nil {
			t.Errorf("%s: served", name)
		}
		var failure *concerns.StreamFailure
		if !errors.As(err, &failure) {
			t.Errorf("%s: not an upstream failure: %v", name, err)
		}
	}
	chunks, done, err := streamed(t, context.Background(), sse(`{"candidates":[{"content":{"parts":[{"text":"partial"}]}}]}`, `{"error":{"code":503,"message":"overloaded"}}`), ResponseOptions{})
	if err == nil || !done {
		t.Fatalf("err=%v done=%v", err, done)
	}
	if at(chunks[len(chunks)-1], "error.message") != "overloaded" {
		t.Errorf("the upstream's own error must reach the client: %s", asJSON(chunks))
	}
	if !strings.Contains(asJSON(chunks), "partial") {
		t.Error("content sent before the error must not be withdrawn")
	}
	for name, part := range map[string]string{"part": `"x"`, "no name": `{"functionCall":{"args":{}}}`} {
		if _, err := buffered(t, `{"candidates":[{"content":{"parts":[`+part+`]},"finishReason":"STOP"}]}`, ResponseOptions{}); err == nil {
			t.Errorf("%s: served", name)
		}
	}
}

// The id of a call, crossed with how Gemini sent it and how many calls share a
// name. Every call gets its own non-empty id, and the same response always gets
// the same ids.
func TestResponseToolCallIDs(t *testing.T) {
	cases := map[string]struct{ calls, want string }{
		"given":        {`{"id":"g1","name":"f","args":{}}`, `g1`},
		"empty":        {`{"id":"","name":"f","args":{}}`, `call_msg0_tc0_f`},
		"absent":       {`{"name":"f","args":{}}`, `call_msg0_tc0_f`},
		"non-string":   {`{"id":7,"name":"f","args":{}}`, `call_msg0_tc0_f`},
		"awkward name": {`{"name":"a.b:c","args":{}}`, `call_msg0_tc0_abc`},
		"args absent":  {`{"name":"f"}`, `call_msg0_tc0_f`},
		"args null":    {`{"name":"f","args":null}`, `call_msg0_tc0_f`},
	}
	for name, c := range cases {
		object := `{"candidates":[{"content":{"parts":[{"functionCall":` + c.calls + `}]},"finishReason":"STOP"}]}`
		result, err := buffered(t, object, ResponseOptions{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got := at(result, "choices.0.message.tool_calls.0.id"); got != c.want {
			t.Errorf("%s: id = %v, want %s", name, got, c.want)
		}
		if got := at(result, "choices.0.message.tool_calls.0.function.arguments"); got != "{}" {
			t.Errorf("%s: arguments = %v, want {}", name, got)
		}
	}
	two := `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"f","args":{"n":1}}},{"functionCall":{"name":"f","args":{"n":2}}}]},"finishReason":"STOP"}]}`
	for _, run := range []func() []any{
		func() []any {
			result, _ := buffered(t, two, ResponseOptions{})
			return concerns.Items(at(result, "choices.0.message.tool_calls"))
		},
		func() []any {
			chunks, _, _ := streamed(t, context.Background(), sse(two), ResponseOptions{})
			var calls []any
			for _, chunk := range chunks {
				calls = append(calls, concerns.Items(at(chunk, "choices.0.delta.tool_calls"))...)
			}
			return calls
		},
	} {
		calls := run()
		if len(calls) != 2 || at(calls[0], "id") == at(calls[1], "id") || at(calls[0], "id") != "call_msg0_tc0_f" || at(calls[1], "id") != "call_msg0_tc1_f" {
			t.Errorf("parallel calls of one tool: %s", asJSON(calls))
		}
	}
}

func TestToolNamesAreRestoredOnTheResponse(t *testing.T) {
	chat := `{"messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"srv.tool 1"}},{"type":"function","function":{"name":"plain"}}]}`
	_, names, err := convertWith(t, chat, RequestOptions{Repair: true})
	if err != nil {
		t.Fatal(err)
	}
	object := `{"candidates":[{"content":{"parts":[{"functionCall":{"name":"srv.tool_1","args":{}}},{"functionCall":{"name":"plain","args":{}}}]},"finishReason":"STOP"}]}`
	result, err := buffered(t, object, ResponseOptions{Names: names})
	if err != nil {
		t.Fatal(err)
	}
	if at(result, "choices.0.message.tool_calls.0.function.name") != "srv.tool 1" || at(result, "choices.0.message.tool_calls.1.function.name") != "plain" {
		t.Errorf("names: %s", asJSON(at(result, "choices.0.message.tool_calls")))
	}
	chunks, _, _ := streamed(t, context.Background(), sse(object), ResponseOptions{Names: names})
	if !strings.Contains(asJSON(chunks), `"name":"srv.tool 1"`) {
		t.Errorf("stream did not restore the name: %s", asJSON(chunks))
	}
}

func TestThoughtsAndImages(t *testing.T) {
	object := `{"candidates":[{"content":{"parts":[{"text":"hmm ","thought":true},{"thoughtSignature":"S"},{"text":"ok"},{"inlineData":{"mimeType":"image/png","data":"QUJD"}},{"inline_data":{"mime_type":"image/jpeg","data":"REVG"}}]},"finishReason":"STOP"}]}`
	result, err := buffered(t, object, ResponseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if at(result, "choices.0.message.reasoning_content") != "hmm " || at(result, "choices.0.message.content") != "ok" {
		t.Errorf("message: %s", asJSON(at(result, "choices.0.message")))
	}
	if got := asJSON(at(result, "choices.0.message.images")); !strings.Contains(got, "data:image/png;base64,QUJD") || !strings.Contains(got, "data:image/jpeg;base64,REVG") {
		t.Errorf("images = %s", got)
	}
	chunks, _, _ := streamed(t, context.Background(), sse(object), ResponseOptions{})
	text := asJSON(chunks)
	for _, want := range []string{`"reasoning_content":"hmm "`, `"content":"ok"`, `data:image/png;base64,QUJD`} {
		if !strings.Contains(text, want) {
			t.Errorf("stream lacks %s", want)
		}
	}
}

// A signature a response carried comes back on the request that continues the
// conversation, for that client and connection and nobody else.
func TestSignatureRoundTripIsScoped(t *testing.T) {
	cache := NewSignatures(10, time.Hour)
	options := ResponseOptions{Signatures: cache, Scope: "alice|conn"}
	// The signature arrives on the call's own part and, in the second call, on a
	// standalone part before it.
	object := `{"candidates":[{"content":{"parts":[
		{"functionCall":{"name":"lookup","args":{"q":1}},"thoughtSignature":"SIG-ONE"},
		{"thoughtSignature":"SIG-TWO"},
		{"functionCall":{"name":"lookup","args":{"q":2}}},
		{"functionCall":{"name":"lookup","args":{"q":3}}}]},"finishReason":"STOP"}]}`
	result, err := buffered(t, object, options)
	if err != nil {
		t.Fatal(err)
	}
	calls := concerns.Items(at(result, "choices.0.message.tool_calls"))
	history := func(scope string) map[string]any {
		var messageCalls []string
		for _, call := range calls {
			messageCalls = append(messageCalls, fmt.Sprintf(`{"id":%s,"type":"function","function":{"name":"lookup","arguments":%s}}`,
				asJSON(at(call, "id")), asJSON(at(call, "function.arguments"))))
		}
		chat := `{"messages":[{"role":"user","content":"a"},{"role":"assistant","tool_calls":[` + strings.Join(messageCalls, ",") + `]}` +
			`,{"role":"tool","tool_call_id":"call_msg0_tc0_lookup","content":"1"},{"role":"tool","tool_call_id":"call_msg0_tc1_lookup","content":"2"},{"role":"tool","tool_call_id":"call_msg0_tc2_lookup","content":"3"}]}`
		out, _, err := convertWith(t, chat, RequestOptions{Repair: true, Signatures: cache, Scope: scope})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	mine := history("alice|conn")
	for i, want := range []any{"SIG-ONE", "SIG-TWO", nil} {
		if got := at(mine, fmt.Sprintf("contents.1.parts.%d.thoughtSignature", i)); got != want {
			t.Errorf("call %d: signature %v, want %v", i, got, want)
		}
	}
	theirs := history("bob|conn")
	if strings.Contains(asJSON(theirs), "SIG-") {
		t.Errorf("another client was served a signature: %s", asJSON(theirs))
	}
	if strings.Contains(asJSON(history("")), "SIG-") {
		t.Error("a request with no scope was served a signature")
	}
	// Nothing was stored for a response that ran with no scope.
	unscoped := NewSignatures(10, time.Hour)
	if _, err := buffered(t, object, ResponseOptions{Signatures: unscoped}); err != nil {
		t.Fatal(err)
	}
	if unscoped.Len() != 0 {
		t.Errorf("stored %d signatures with no scope", unscoped.Len())
	}
}

func TestStreamMatchesBuffered(t *testing.T) {
	first := `{"candidates":[{"content":{"role":"model","parts":[{"text":"po"}]}}],"responseId":"r1","modelVersion":"gemini-x"}`
	second := `{"candidates":[{"content":{"role":"model","parts":[{"text":"ng"},{"functionCall":{"name":"lookup","args":{"city":"Oslo"}},"thoughtSignature":"SIG"}]},"finishReason":"STOP"}],` +
		`"usageMetadata":{"promptTokenCount":130,"cachedContentTokenCount":100,"candidatesTokenCount":30,"thoughtsTokenCount":12,"totalTokenCount":172},"responseId":"r1","modelVersion":"gemini-x"}`
	framings := map[string]string{
		"sse":         sse(first, second),
		"sse crlf":    strings.ReplaceAll(sse(first, second), "\n", "\r\n"),
		"array":       "[" + first + ",\r\n" + second + "]",
		"array lines": "\n[\n" + first + ",\n" + second + "\n]\n",
	}
	for name, src := range framings {
		t.Run(name, func(t *testing.T) {
			chunks, done, err := streamed(t, context.Background(), src, ResponseOptions{Model: "requested"})
			if err != nil || !done {
				t.Fatalf("err=%v done=%v", err, done)
			}
			if at(chunks[0], "choices.0.delta.role") != "assistant" {
				t.Errorf("first chunk is not the role: %s", asJSON(chunks[0]))
			}
			var text strings.Builder
			var calls []any
			for _, chunk := range chunks {
				text.WriteString(concerns.Text(at(chunk, "choices.0.delta.content")))
				calls = append(calls, concerns.Items(at(chunk, "choices.0.delta.tool_calls"))...)
				if at(chunk, "model") != "gemini-x" || at(chunk, "id") != "chatcmpl-r1" {
					t.Errorf("chunk identity: %s", asJSON(chunk))
				}
			}
			final := chunks[len(chunks)-1]
			if text.String() != "pong" || len(calls) != 1 || at(final, "choices.0.finish_reason") != "tool_calls" {
				t.Errorf("text=%q calls=%s final=%s", text.String(), asJSON(calls), asJSON(final))
			}
			for path, want := range map[string]string{"usage.prompt_tokens": "130", "usage.completion_tokens": "42", "usage.prompt_tokens_details.cached_tokens": "100"} {
				if got := asJSON(at(final, path)); got != want {
					t.Errorf("%s = %s", path, got)
				}
			}
			for _, chunk := range chunks[:len(chunks)-1] {
				if chunk["usage"] != nil {
					t.Errorf("usage before the end: %s", asJSON(chunk))
				}
			}
		})
	}
}

// Usage that arrives after the finishReason (a trailing metadata-only chunk) is
// the figure reported, because the finish chunk waits for the end of the body.
func TestUsageAfterTheFinishChunk(t *testing.T) {
	chunks, _, err := streamed(t, context.Background(), sse(
		`{"candidates":[{"content":{"parts":[{"text":"a"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":1}}`,
		`{"usageMetadata":{"promptTokenCount":5,"candidatesTokenCount":9,"totalTokenCount":14}}`), ResponseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	final := chunks[len(chunks)-1]
	if asJSON(at(final, "usage.completion_tokens")) != "9" || at(final, "choices.0.finish_reason") != "stop" {
		t.Errorf("final = %s", asJSON(final))
	}
}

// A body that ends early is a failure, whatever it carried: only a finishReason
// makes a response complete.
func TestTruncatedStreamsFail(t *testing.T) {
	for name, src := range map[string]string{
		"empty":             ``,
		"blank":             "\n\n",
		"no finish":         sse(`{"candidates":[{"content":{"parts":[{"text":"half"}]}}]}`),
		"usage only":        sse(`{"usageMetadata":{"promptTokenCount":3}}`),
		"[DONE] only":       "data: [DONE]\n\n",
		"array cut":         `[{"candidates":[{"content":{"parts":[{"text":"half"}]}}]},`,
		"array no finish":   `[{"candidates":[{"content":{"parts":[{"text":"half"}]}}]}]`,
		"bad event":         "data: {not json}\n\n",
		"array not objects": `[1,2]`,
		"array null":        `[null]`,
	} {
		t.Run(name, func(t *testing.T) {
			chunks, done, err := streamed(t, context.Background(), src, ResponseOptions{})
			if err == nil || !done {
				t.Fatalf("err=%v done=%v: %s", err, done, asJSON(chunks))
			}
			if len(chunks) == 0 || chunks[len(chunks)-1]["error"] == nil {
				t.Fatalf("the failure must be the last event: %s", asJSON(chunks))
			}
			for _, chunk := range chunks {
				if at(chunk, "choices.0.finish_reason") != nil {
					t.Errorf("a failed stream claimed to finish: %s", asJSON(chunk))
				}
			}
		})
	}
}

// A client that went away gets no terminal: there is no one to tell, and the
// upstream read stops.
func TestCancelledClientGetsNoTerminal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	chunks, done, err := streamed(t, ctx, sse(`{"candidates":[{"content":{"parts":[{"text":"x"}]},"finishReason":"STOP"}]}`), ResponseOptions{})
	if !errors.Is(err, context.Canceled) || done || len(chunks) != 0 {
		t.Fatalf("err=%v done=%v chunks=%s", err, done, asJSON(chunks))
	}
	// Cancelled while reading: the leg returns when the context ends, with the
	// upstream parked and nothing written afterwards.
	reader, writer := io.Pipe()
	ctx, cancel = context.WithCancel(context.Background())
	var out bytes.Buffer
	result := make(chan error, 1)
	go func() { result <- GeminiStreamToChat(ctx, reader, &out, ResponseOptions{}) }()
	_, _ = io.WriteString(writer, sse(`{"candidates":[{"content":{"parts":[{"text":"x"}]}}]}`))
	time.Sleep(50 * time.Millisecond)
	cancel()
	_, _ = io.WriteString(writer, sse(`{"candidates":[{"content":{"parts":[{"text":"y"}]}}]}`))
	_ = writer.Close()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the leg did not stop when its client went away")
	}
	if strings.Contains(out.String(), "[DONE]") || strings.Contains(out.String(), `"error"`) {
		t.Errorf("terminal written to a cancelled client: %s", out.String())
	}
}

func TestStreamIsBounded(t *testing.T) {
	huge := `{"candidates":[{"content":{"parts":[{"text":"` + strings.Repeat("x", 5<<20) + `"}]}}]}`
	for name, src := range map[string]string{"sse line": sse(huge), "array element": "[" + huge + "]"} {
		_, done, err := streamed(t, context.Background(), src, ResponseOptions{})
		if err == nil || !done {
			t.Errorf("%s: err=%v done=%v", name, err, done)
		}
	}
}

// Gemini CLI and Antigravity wrap each response object in {"response": ...}
// (gemini-to-openai.js:45); the pin registers this same function for them, and
// PR 5b binds it. The envelope is read the same way for both framings.
func TestCloudCodeEnvelopeIsUnwrapped(t *testing.T) {
	inner := `{"candidates":[{"content":{"parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":3,"candidatesTokenCount":1},"modelVersion":"m","responseId":"r"}`
	wrapped := `{"response":` + inner + `,"traceId":"t"}`
	plain, err := buffered(t, inner, ResponseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	enveloped, err := buffered(t, wrapped, ResponseOptions{})
	if err != nil {
		t.Fatal(err)
	}
	delete(plain, "created")
	delete(enveloped, "created")
	if asJSON(plain) != asJSON(enveloped) {
		t.Errorf("buffered differs:\n%s\n%s", asJSON(plain), asJSON(enveloped))
	}
	chunks, done, err := streamed(t, context.Background(), sse(wrapped), ResponseOptions{})
	if err != nil || !done || !strings.Contains(asJSON(chunks), `"content":"hi"`) || at(chunks[len(chunks)-1], "usage.prompt_tokens") != float64(3) && asJSON(at(chunks[len(chunks)-1], "usage.prompt_tokens")) != "3" {
		t.Errorf("stream: err=%v done=%v %s", err, done, asJSON(chunks))
	}
	// An error inside the envelope is still the upstream's error.
	if _, err := buffered(t, `{"response":{"error":{"code":500,"message":"inner"}}}`, ResponseOptions{}); err == nil {
		t.Error("an error inside the envelope was served")
	}
}
