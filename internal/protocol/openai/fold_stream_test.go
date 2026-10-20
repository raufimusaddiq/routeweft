package openai

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func fixedNow() time.Time { return time.Unix(1735689600, 0) }

func fold(t *testing.T, stream string) (map[string]any, Folded, error) {
	t.Helper()
	folded, err := FoldChatStream(strings.NewReader(stream), "fallback-model", fixedNow)
	if err != nil {
		return nil, folded, err
	}
	var document map[string]any
	if err := json.Unmarshal(folded.Body, &document); err != nil {
		t.Fatalf("folded body is not JSON: %v: %s", err, folded.Body)
	}
	return document, folded, nil
}

func sse(chunks ...string) string {
	var out strings.Builder
	for _, chunk := range chunks {
		out.WriteString("data: " + chunk + "\n\n")
	}
	return out.String()
}

func firstMessage(t *testing.T, document map[string]any) map[string]any {
	t.Helper()
	choices := document["choices"].([]any)
	return choices[0].(map[string]any)["message"].(map[string]any)
}

func TestFoldChatStreamFoldsContentReasoningAndUsage(t *testing.T) {
	document, folded, err := fold(t, sse(
		`{"id":"c1","object":"chat.completion.chunk","created":7,"model":"glm-5.2","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"think "}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{"reasoning_content":"hard","content":"Hel"}}]}`,
		`{"id":"c1","choices":[{"index":0,"delta":{"content":"lo","tool_calls":[]},"finish_reason":"stop"}],"obfuscation":"xx"}`,
		`{"id":"c1","choices":[],"usage":{"prompt_tokens":7,"completion_tokens":2,"total_tokens":9,"prompt_tokens_details":{"cached_tokens":3}}}`,
		`[DONE]`,
	))
	if err != nil {
		t.Fatal(err)
	}
	message := firstMessage(t, document)
	if message["content"] != "Hello" || message["reasoning_content"] != "think hard" || message["role"] != "assistant" {
		t.Fatalf("message %v", message)
	}
	if document["id"] != "c1" || document["model"] != "glm-5.2" || document["created"] != float64(7) || document["object"] != "chat.completion" {
		t.Fatalf("envelope %v", document)
	}
	usage := document["usage"].(map[string]any)
	if usage["prompt_tokens"] != float64(7) || usage["prompt_tokens_details"].(map[string]any)["cached_tokens"] != float64(3) {
		t.Fatalf("usage %v", usage)
	}
	if document["choices"].([]any)[0].(map[string]any)["finish_reason"] != "stop" {
		t.Fatalf("choice %v", document["choices"])
	}
	if folded.Ignored["object"] != 1 || folded.Ignored["obfuscation"] != 1 || folded.Ignored["delta.tool_calls(empty)"] != 1 {
		t.Fatalf("ignored metadata not counted: %v", folded.Ignored)
	}
}

func TestFoldChatStreamMergesToolCallsByIndex(t *testing.T) {
	document, _, err := fold(t, sse(
		`{"id":"c","choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"id":"call_b","type":"function","function":{"name":"b","arguments":"{\"y\""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_a","type":"function","function":{"name":"a","arguments":"{}"}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"tool_calls":[{"index":1,"function":{"arguments":":2}"}}]},"finish_reason":"tool_calls"}]}`,
		`[DONE]`,
	))
	if err != nil {
		t.Fatal(err)
	}
	message := firstMessage(t, document)
	calls := message["tool_calls"].([]any)
	if len(calls) != 2 || message["content"] != nil {
		t.Fatalf("message %v", message)
	}
	b := calls[1].(map[string]any)
	if calls[0].(map[string]any)["id"] != "call_a" || b["id"] != "call_b" || b["function"].(map[string]any)["arguments"] != `{"y":2}` {
		t.Fatalf("calls %v", calls)
	}
}

// The pin reads choices[0] only; every index is folded.
func TestFoldChatStreamKeepsEveryChoice(t *testing.T) {
	document, _, err := fold(t, sse(
		`{"id":"c","choices":[{"index":1,"delta":{"content":"second"},"finish_reason":"length"},{"index":0,"delta":{"content":"first"},"finish_reason":"stop"}]}`,
		`[DONE]`,
	))
	if err != nil {
		t.Fatal(err)
	}
	choices := document["choices"].([]any)
	if len(choices) != 2 {
		t.Fatalf("choices %v", choices)
	}
	zero, one := choices[0].(map[string]any), choices[1].(map[string]any)
	if zero["index"] != float64(0) || zero["message"].(map[string]any)["content"] != "first" || one["message"].(map[string]any)["content"] != "second" || one["finish_reason"] != "length" {
		t.Fatalf("choices %v", choices)
	}
}

func TestFoldChatStreamFramingVariants(t *testing.T) {
	// CRLF, no space after "data:", comments and event names, a multi-line data
	// field, and a final event with no blank line after it.
	stream := ": keepalive\r\nevent: message\r\ndata:{\"id\":\"c\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"a\"}}]}\r\n\r\n" +
		"data: {\"choices\":[{\"index\":0,\r\ndata: \"delta\":{\"content\":\"b\"},\"finish_reason\":\"stop\"}]}\r\n\r\n" +
		"data: [DONE]"
	document, _, err := fold(t, stream)
	if err != nil {
		t.Fatal(err)
	}
	if firstMessage(t, document)["content"] != "ab" {
		t.Fatalf("document %v", document)
	}
}

func TestFoldChatStreamEmptyAndMissingContent(t *testing.T) {
	cases := map[string]struct {
		stream string
		want   any
	}{
		"empty string content": {sse(`{"choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}]}`, `[DONE]`), ""},
		"null content":         {sse(`{"choices":[{"index":0,"delta":{"content":null},"finish_reason":"stop"}]}`, `[DONE]`), ""},
		"no delta":             {sse(`{"choices":[{"index":0,"finish_reason":"stop"}]}`, `[DONE]`), ""},
		"null delta":           {sse(`{"choices":[{"index":0,"delta":null,"finish_reason":"stop"}]}`, `[DONE]`), ""},
		"DONE without finish":  {sse(`{"choices":[{"index":0,"delta":{"content":"x"}}]}`, `[DONE]`), "x"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			document, _, err := fold(t, tc.stream)
			if err != nil {
				t.Fatal(err)
			}
			if got := firstMessage(t, document)["content"]; got != tc.want {
				t.Fatalf("content %v want %v", got, tc.want)
			}
			if document["choices"].([]any)[0].(map[string]any)["finish_reason"] != "stop" {
				t.Fatal("finish_reason")
			}
		})
	}
}

func TestFoldChatStreamFabricatesOnlyTheEnvelope(t *testing.T) {
	document, _, err := fold(t, sse(`{"choices":[{"index":0,"delta":{"content":"x"},"finish_reason":"stop"}]}`, `[DONE]`))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(document["id"].(string), "chatcmpl-") || document["created"] != float64(1735689600) || document["model"] != "fallback-model" {
		t.Fatalf("envelope %v", document)
	}
}

// Every field that can carry text the caller would lose fails the fold, in the
// chunk, the choice, the delta and a tool call; the transport-metadata allowlist
// is the only thing ignored.
func TestFoldChatStreamFailsOnContentBearingUnknownFields(t *testing.T) {
	cases := map[string]string{
		"chunk field":               `{"choices":[],"citations":["a"]}`,
		"choice field":              `{"choices":[{"index":0,"delta":{},"text":"legacy"}]}`,
		"delta field":               `{"choices":[{"index":0,"delta":{"reasoning":"hidden"}}]}`,
		"delta function_call":       `{"choices":[{"index":0,"delta":{"function_call":{"name":"f"}}}]}`,
		"delta audio":               `{"choices":[{"index":0,"delta":{"audio":{"data":"x"}}}]}`,
		"non-null logprobs":         `{"choices":[{"index":0,"delta":{},"logprobs":{"content":[]}}]}`,
		"content object":            `{"choices":[{"index":0,"delta":{"content":{"text":"x"}}}]}`,
		"content array":             `{"choices":[{"index":0,"delta":{"content":[{"type":"text","text":"x"}]}}]}`,
		"content number":            `{"choices":[{"index":0,"delta":{"content":1}}]}`,
		"content bool":              `{"choices":[{"index":0,"delta":{"content":true}}]}`,
		"reasoning_content number":  `{"choices":[{"index":0,"delta":{"reasoning_content":2}}]}`,
		"refusal object":            `{"choices":[{"index":0,"delta":{"refusal":{}}}]}`,
		"delta is a string":         `{"choices":[{"index":0,"delta":"x"}]}`,
		"delta is an array":         `{"choices":[{"index":0,"delta":[]}]}`,
		"choices is an object":      `{"choices":{"index":0}}`,
		"choice is a string":        `{"choices":["x"]}`,
		"index is a string":         `{"choices":[{"index":"0","delta":{}}]}`,
		"index is negative":         `{"choices":[{"index":-1,"delta":{}}]}`,
		"finish_reason is a number": `{"choices":[{"index":0,"delta":{},"finish_reason":1}]}`,
		"role is a number":          `{"choices":[{"index":0,"delta":{"role":1}}]}`,
		"usage is a string":         `{"choices":[],"usage":"x"}`,
		"tool_calls is an object":   `{"choices":[{"index":0,"delta":{"tool_calls":{}}}]}`,
		"tool_calls is a string":    `{"choices":[{"index":0,"delta":{"tool_calls":"x"}}]}`,
		"tool call is a string":     `{"choices":[{"index":0,"delta":{"tool_calls":["x"]}}]}`,
		"tool call unknown field":   `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"extra":1}]}}]}`,
		"tool call custom type":     `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"type":"custom"}]}}]}`,
		"tool call index string":    `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":"0"}]}}]}`,
		"tool call id number":       `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":1}]}}]}`,
		"function is a string":      `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":"f"}]}}]}`,
		"function unknown field":    `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"x":1}}]}}]}`,
		"function arguments object": `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":{}}}]}}]}`,
		"function name number":      `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"name":3}}]}}]}`,
	}
	for name, chunk := range cases {
		t.Run(name, func(t *testing.T) {
			_, _, err := fold(t, sse(chunk, `[DONE]`))
			if err == nil {
				t.Fatal("a content-bearing field this fold cannot place was dropped with a result")
			}
		})
	}
}

func TestFoldChatStreamFailsOnBrokenStreams(t *testing.T) {
	cases := map[string]string{
		"no chunks":             "",
		"only DONE":             sse(`[DONE]`),
		"malformed payload":     sse(`{"choices":[`, `[DONE]`),
		"payload is an array":   sse(`[]`, `[DONE]`),
		"payload is a string":   sse(`"x"`, `[DONE]`),
		"payload is null":       sse(`null`, `[DONE]`),
		"truncated, no finish":  sse(`{"choices":[{"index":0,"delta":{"content":"half an ans"}}]}`),
		"truncated mid-payload": "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"x\"",
		"comment only":          ": ping\n\n",
	}
	for name, stream := range cases {
		t.Run(name, func(t *testing.T) {
			if _, _, err := fold(t, stream); err == nil {
				t.Fatal("a stream that cannot be returned whole produced a result")
			}
		})
	}
}

func TestFoldChatStreamAcceptsTheEndOfStreamAfterAFinishReason(t *testing.T) {
	// finish_reason arrived and the connection ended before [DONE]: the answer is
	// whole, so it is returned.
	document, _, err := fold(t, sse(`{"choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"}]}`))
	if err != nil || firstMessage(t, document)["content"] != "done" {
		t.Fatalf("%v %v", document, err)
	}
}

func TestFoldChatStreamReportsAnInBandError(t *testing.T) {
	_, _, err := fold(t, sse(`{"choices":[{"index":0,"delta":{"content":"x"}}]}`, `{"error":{"message":"model overloaded"}}`))
	var inband *StreamError
	if !errors.As(err, &inband) || !strings.Contains(string(inband.Raw), "model overloaded") {
		t.Fatalf("error %v", err)
	}
	// A null error field is not an error.
	if _, _, err := fold(t, sse(`{"error":null,"choices":[{"index":0,"delta":{"content":"x"},"finish_reason":"stop"}]}`, `[DONE]`)); err != nil {
		t.Fatal(err)
	}
}

func TestFoldChatStreamIgnoresEventsAfterDone(t *testing.T) {
	document, _, err := fold(t, sse(`{"choices":[{"index":0,"delta":{"content":"a"},"finish_reason":"stop"}]}`, `[DONE]`, `not json`))
	if err != nil || firstMessage(t, document)["content"] != "a" {
		t.Fatalf("%v %v", document, err)
	}
}

func TestFoldChatStreamIsBounded(t *testing.T) {
	big := strings.Repeat("x", MaxTranslatedResponseBytes+10)
	_, err := FoldChatStream(strings.NewReader("data: "+big+"\n\n"), "m", fixedNow)
	if err == nil {
		t.Fatal("an oversized stream was folded")
	}
}

func TestFoldChatStreamKeepsExplicitEmptyText(t *testing.T) {
	document, _, err := fold(t, sse(`{"choices":[{"index":0,"delta":{"content":"","reasoning_content":"","refusal":"","tool_calls":[{"index":0,"id":"c","type":"function","function":{"name":"f","arguments":""}}]},"finish_reason":"tool_calls"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	message := firstMessage(t, document)
	for _, field := range []string{"content", "reasoning_content", "refusal"} {
		if value, exists := message[field]; !exists || value != "" {
			t.Fatalf("explicit empty %s lost: %v", field, message)
		}
	}
}

func TestFoldChatStreamRequiresEveryChoiceToFinish(t *testing.T) {
	for _, chunk := range []string{
		`{"choices":[]}`,
		`{"choices":null}`,
		`{"usage":{}}`,
		`{"choices":[{"index":0,"delta":{"content":"done"},"finish_reason":"stop"},{"index":1,"delta":{"content":"half"}}]}`,
	} {
		if _, _, err := fold(t, sse(chunk)); err == nil {
			t.Fatalf("incomplete stream accepted: %s", chunk)
		}
	}
}

func TestFoldChatStreamTextGuards(t *testing.T) {
	for _, field := range []string{"content", "reasoning_content", "refusal"} {
		for _, value := range []string{`null`, `""`, `"text"`, `0`, `false`, `{}`, `[]`} {
			chunk := `{"choices":[{"index":0,"delta":{"` + field + `":` + value + `},"finish_reason":"stop"}]}`
			_, _, err := fold(t, sse(chunk))
			wantError := value != `null` && !strings.HasPrefix(value, `"`)
			if (err != nil) != wantError {
				t.Fatalf("%s=%s: %v", field, value, err)
			}
		}
	}
}

func TestFoldChatStreamExplicitEmptyRoleAndCallID(t *testing.T) {
	document, _, err := fold(t, sse(
		`{"choices":[{"index":0,"delta":{"role":"assistant","tool_calls":[{"index":0,"id":"first","type":"function","function":{"name":"f","arguments":""}}]}}]}`,
		`{"choices":[{"index":0,"delta":{"role":"","tool_calls":[{"index":0,"id":""}]},"finish_reason":"tool_calls"}]}`,
	))
	if err != nil {
		t.Fatal(err)
	}
	message := firstMessage(t, document)
	if message["role"] != "" || message["tool_calls"].([]any)[0].(map[string]any)["id"] != "" {
		t.Fatalf("explicit empty value replaced: %v", message)
	}
	for _, chunk := range []string{
		`{"choices":[{"delta":{},"finish_reason":""}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"type":""}]},"finish_reason":"stop"}]}`,
	} {
		if _, _, err := fold(t, sse(chunk, `[DONE]`)); err == nil {
			t.Fatalf("unrepresentable empty value accepted: %s", chunk)
		}
	}
}
