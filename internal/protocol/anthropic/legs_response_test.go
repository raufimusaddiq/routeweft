package anthropic

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// sseData returns the JSON payloads of an SSE body, dropping `created`, with
// "[DONE]" kept as the string it is.
func sseData(t *testing.T, body string) []any {
	t.Helper()
	var events []any
	for _, block := range strings.Split(strings.TrimSpace(body), "\n\n") {
		for _, line := range strings.Split(block, "\n") {
			payload, ok := strings.CutPrefix(line, "data: ")
			if !ok {
				continue
			}
			if payload == "[DONE]" {
				events = append(events, "[DONE]")
				continue
			}
			var value map[string]any
			if err := json.Unmarshal([]byte(payload), &value); err != nil {
				t.Fatalf("bad event %q: %v", payload, err)
			}
			delete(value, "created")
			events = append(events, value)
		}
	}
	return events
}

func eventNames(body string) []string {
	var names []string
	for _, line := range strings.Split(body, "\n") {
		if name, ok := strings.CutPrefix(line, "event: "); ok {
			names = append(names, name)
		}
	}
	return names
}

func expectEvents(t *testing.T, got []any, want string) {
	t.Helper()
	var expected []any
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, expected) {
		gotJSON, _ := json.MarshalIndent(got, "", " ")
		wantJSON, _ := json.MarshalIndent(expected, "", " ")
		t.Fatalf("got\n%s\nwant\n%s", gotJSON, wantJSON)
	}
}

const messagesStream = `event: message_start
data: {"type":"message_start","message":{"id":"msg_01abc","model":"claude-x","usage":{"input_tokens":10,"cache_read_input_tokens":100,"cache_creation_input_tokens":20,"output_tokens":1}}}

event: ping
data: {"type":"ping"}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"hmm"}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"signature_delta","signature":"zz"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: content_block_start
data: {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hello"}}

event: content_block_stop
data: {"type":"content_block_stop","index":1}

event: content_block_start
data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_1","name":"srv_lookup","input":{}}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"q\":"}}

event: content_block_delta
data: {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"\"x\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":2}

event: content_block_start
data: {"type":"content_block_start","index":3,"content_block":{"type":"tool_use","id":"toolu_2","name":"noargs","input":{}}}

event: content_block_stop
data: {"type":"content_block_stop","index":3}

event: content_block_start
data: {"type":"content_block_start","index":4,"content_block":{"type":"server_tool_use","id":"srvtoolu_1","name":"web_search"}}

event: content_block_delta
data: {"type":"content_block_delta","index":4,"delta":{"type":"input_json_delta","partial_json":"{\"query\":\"a\"}"}}

event: content_block_stop
data: {"type":"content_block_stop","index":4}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"output_tokens":42}}

event: message_stop
data: {"type":"message_stop"}

`

func testNames() *concerns.ToolNames {
	names := concerns.NewToolNames(nil)
	names.Safe("srv.lookup")
	return names
}

// response anthropic-messages>openai-chat, streamed (claude-to-openai.js:19-198).
func TestMessagesStreamToChat(t *testing.T) {
	var out bytes.Buffer
	if err := MessagesStreamToChat(context.Background(), strings.NewReader(messagesStream), &out, testNames()); err != nil {
		t.Fatal(err)
	}
	chunk := func(delta, finish, extra string) string {
		if finish == "" {
			finish = "null"
		}
		if extra != "" {
			extra = "," + extra
		}
		return `{"id":"chatcmpl-msg_01abc","object":"chat.completion.chunk","model":"claude-x","choices":[{"index":0,"delta":` + delta + `,"finish_reason":` + finish + `}]` + extra + `}`
	}
	want := "[" + strings.Join([]string{
		chunk(`{"role":"assistant"}`, "", ""),
		chunk(`{"reasoning_content":"hmm"}`, "", ""),
		chunk(`{"content":"Hello"}`, "", ""),
		chunk(`{"tool_calls":[{"index":0,"id":"toolu_1","type":"function","function":{"name":"srv.lookup","arguments":""}}]}`, "", ""),
		chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"{\"q\":"}}]}`, "", ""),
		chunk(`{"tool_calls":[{"index":0,"function":{"arguments":"\"x\"}"}}]}`, "", ""),
		chunk(`{"tool_calls":[{"index":1,"id":"toolu_2","type":"function","function":{"name":"noargs","arguments":""}}]}`, "", ""),
		chunk(`{"tool_calls":[{"index":1,"function":{"arguments":"{}"}}]}`, "", ""),
		chunk(`{}`, `"tool_calls"`, `"usage":{"prompt_tokens":130,"completion_tokens":42,"total_tokens":172,"prompt_tokens_details":{"cached_tokens":100,"cache_creation_tokens":20}}`),
		`"[DONE]"`,
	}, ",") + "]"
	expectEvents(t, sseData(t, out.String()), want)
}

func TestMessagesStreamToChatRefusalAndMessageStopFallback(t *testing.T) {
	var out bytes.Buffer
	stream := `data: {"type":"message_start","message":{"id":"m1","model":"c","usage":{"input_tokens":7,"cache_read_input_tokens":3}}}

data: {"type":"message_delta","delta":{"stop_reason":"refusal","stop_details":{"explanation":"Declined."}},"usage":{"output_tokens":0}}

data: {"type":"message_stop"}

`
	if err := MessagesStreamToChat(context.Background(), strings.NewReader(stream), &out, nil); err != nil {
		t.Fatal(err)
	}
	events := sseData(t, out.String())
	explanation := events[1].(map[string]any)["choices"].([]any)[0].(map[string]any)["delta"].(map[string]any)["content"]
	final := events[2].(map[string]any)
	if explanation != "Declined." || final["choices"].([]any)[0].(map[string]any)["finish_reason"] != "content_filter" || len(events) != 4 {
		t.Fatalf("a refusal explains itself and maps to content_filter: %v", events)
	}
	// message_stop with no stop_reason in any delta: the pin reports only
	// input_tokens as the prompt on this path (claude-to-openai.js:183-187);
	// the cache-folded figure is used instead, as on the message_delta path.
	out.Reset()
	stream = `data: {"type":"message_start","message":{"id":"m1","model":"c","usage":{"input_tokens":7,"cache_read_input_tokens":3}}}

data: {"type":"message_delta","delta":{},"usage":{"output_tokens":5}}

data: {"type":"message_stop"}

`
	if err := MessagesStreamToChat(context.Background(), strings.NewReader(stream), &out, nil); err != nil {
		t.Fatal(err)
	}
	events = sseData(t, out.String())
	last := events[len(events)-2].(map[string]any)
	usage, _ := json.Marshal(last["usage"])
	if last["choices"].([]any)[0].(map[string]any)["finish_reason"] != "stop" || string(usage) != `{"completion_tokens":5,"prompt_tokens":10,"prompt_tokens_details":{"cached_tokens":3},"total_tokens":15}` {
		t.Fatalf("fallback finish: %v", last)
	}
}

// An upstream that ends early or reports an error must not look like success.
func TestMessagesStreamToChatFailureTerminals(t *testing.T) {
	cases := map[string]string{
		"truncated": `data: {"type":"message_start","message":{"id":"m1","model":"c"}}` + "\n\n",
		"error":     `data: {"type":"message_start","message":{"id":"m1","model":"c"}}` + "\n\n" + `data: {"type":"error","error":{"type":"overloaded_error","message":"busy"}}` + "\n\n",
	}
	for name, stream := range cases {
		var out bytes.Buffer
		err := MessagesStreamToChat(context.Background(), strings.NewReader(stream), &out, nil)
		if err == nil {
			t.Errorf("%s: a failed stream must return an error", name)
		}
		events := sseData(t, out.String())
		if len(events) < 2 || events[len(events)-1] != "[DONE]" || events[len(events)-2].(map[string]any)["error"] == nil {
			t.Errorf("%s: a failure terminal is an error object then [DONE]: %v", name, events)
		}
		for _, event := range events {
			if m, ok := event.(map[string]any); ok && m["choices"] != nil {
				if finish := m["choices"].([]any)[0].(map[string]any)["finish_reason"]; finish != nil {
					t.Errorf("%s: no finish_reason may be claimed: %v", name, event)
				}
			}
		}
	}
}

const chatStream = `data: {"id":"chatcmpl-1234567890","model":"m","choices":[{"index":0,"delta":{"role":"assistant","reasoning_content":"think"}}]}

data: {"id":"chatcmpl-1234567890","choices":[{"index":0,"delta":{"content":"Hi "}}]}

data: {"id":"chatcmpl-1234567890","choices":[{"index":0,"delta":{"content":"there"}}]}

data: {"id":"chatcmpl-1234567890","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_9","type":"function","function":{"name":"Read","arguments":""}}]}}]}

data: {"id":"chatcmpl-1234567890","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"call_9","function":{"arguments":"{\"file_path\":\"a.txt\","}}]}}]}

data: {"id":"chatcmpl-1234567890","choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"limit\":\"5000\"}"}}]}}]}

data: {"id":"chatcmpl-1234567890","choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}

data: {"id":"chatcmpl-1234567890","choices":[],"usage":{"prompt_tokens":130,"completion_tokens":42,"total_tokens":172,"prompt_tokens_details":{"cached_tokens":100,"cache_creation_tokens":20}}}

data: [DONE]

`

// response openai-chat>anthropic-messages, streamed (openai-to-claude.js:71-260),
// including the usage-only chunk after finish_reason that the pin drops.
func TestChatStreamToMessages(t *testing.T) {
	var out bytes.Buffer
	if err := ChatStreamToMessages(context.Background(), strings.NewReader(chatStream), &out); err != nil {
		t.Fatal(err)
	}
	want := `[
	 {"type":"message_start","message":{"id":"1234567890","type":"message","role":"assistant","model":"m","content":[],"stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":0,"output_tokens":0}}},
	 {"type":"content_block_start","index":0,"content_block":{"type":"thinking","thinking":""}},
	 {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"think"}},
	 {"type":"content_block_stop","index":0},
	 {"type":"content_block_start","index":1,"content_block":{"type":"text","text":""}},
	 {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"Hi "}},
	 {"type":"content_block_delta","index":1,"delta":{"type":"text_delta","text":"there"}},
	 {"type":"content_block_stop","index":1},
	 {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"call_9","name":"Read","input":{}}},
	 {"type":"content_block_delta","index":2,"delta":{"type":"input_json_delta","partial_json":"{\"file_path\":\"a.txt\",\"limit\":2000}"}},
	 {"type":"content_block_stop","index":2},
	 {"type":"message_delta","delta":{"stop_reason":"tool_use"},"usage":{"input_tokens":10,"output_tokens":42,"cache_read_input_tokens":100,"cache_creation_input_tokens":20}},
	 {"type":"message_stop"}
	]`
	expectEvents(t, sseData(t, out.String()), want)
	if got := eventNames(out.String()); got[0] != "message_start" || got[len(got)-1] != "message_stop" {
		t.Fatalf("event names %v", got)
	}
}

func TestChatStreamToMessagesFinishReasonsAndBareEnd(t *testing.T) {
	for chat, want := range map[string]string{"stop": "end_turn", "length": "max_tokens", "content_filter": "refusal", "tool_calls": "tool_use"} {
		stream := `data: {"id":"chatcmpl-abcdefgh","model":"m","choices":[{"delta":{"content":"x"},"finish_reason":"` + chat + `"}]}` + "\n\n"
		var out bytes.Buffer
		// Ending without [DONE] after finish_reason is a complete answer.
		if err := ChatStreamToMessages(context.Background(), strings.NewReader(stream), &out); err != nil {
			t.Fatalf("%s: %v", chat, err)
		}
		events := sseData(t, out.String())
		delta := events[len(events)-2].(map[string]any)
		if got := delta["delta"].(map[string]any)["stop_reason"]; got != want {
			t.Errorf("finish %s -> %v want %s", chat, got, want)
		}
		if !reflect.DeepEqual(delta["usage"], map[string]any{"input_tokens": float64(0), "output_tokens": float64(0)}) {
			t.Errorf("no reported usage is reported as the pin's zero object: %v", delta["usage"])
		}
	}
	// Short or generic ids fall back to the request id, then a generated one.
	var out bytes.Buffer
	stream := `data: {"id":"chat","extend_fields":{"requestId":"req-1"},"choices":[{"delta":{"content":"x"},"finish_reason":"stop"}]}` + "\n\n" + "data: [DONE]\n\n"
	if err := ChatStreamToMessages(context.Background(), strings.NewReader(stream), &out); err != nil {
		t.Fatal(err)
	}
	if first := sseData(t, out.String())[0].(map[string]any)["message"].(map[string]any); first["id"] != "req-1" || first["model"] != "unknown" {
		t.Fatalf("%v", first)
	}
}

func TestChatStreamToMessagesFailureTerminals(t *testing.T) {
	cases := map[string]string{
		"truncated": `data: {"id":"chatcmpl-abcdefgh","choices":[{"delta":{"content":"x"}}]}` + "\n\n",
		"error":     `data: {"id":"chatcmpl-abcdefgh","choices":[{"delta":{"content":"x"}}]}` + "\n\n" + `data: {"error":{"type":"rate_limit_error","message":"slow down"}}` + "\n\n",
	}
	for name, stream := range cases {
		var out bytes.Buffer
		if err := ChatStreamToMessages(context.Background(), strings.NewReader(stream), &out); err == nil {
			t.Errorf("%s: a failed stream must return an error", name)
		}
		names := eventNames(out.String())
		if names[len(names)-1] != "error" {
			t.Errorf("%s: must end with an error event, not message_stop: %v", name, names)
		}
		for _, n := range names {
			if n == "message_stop" || n == "message_delta" {
				t.Errorf("%s: claimed completion: %v", name, names)
			}
		}
	}
}

// Client cancellation emits no synthetic terminal and stops reading upstream.
func TestStreamLegsHonourCancellation(t *testing.T) {
	legs := map[string]func(context.Context, io.Reader, io.Writer) error{
		"messages>chat": func(ctx context.Context, r io.Reader, w io.Writer) error {
			return MessagesStreamToChat(ctx, r, w, nil)
		},
		"chat>messages": ChatStreamToMessages,
	}
	for name, leg := range legs {
		reader, writer := io.Pipe()
		ctx, cancel := context.WithCancel(context.Background())
		var out bytes.Buffer
		done := make(chan error, 1)
		go func() { done <- leg(ctx, reader, &out) }()
		first := `data: {"type":"message_start","message":{"id":"m1","model":"c"}}` + "\n\n"
		if name == "chat>messages" {
			first = `data: {"id":"chatcmpl-abcdefgh","choices":[{"delta":{"content":"x"}}]}` + "\n\n"
		}
		_, _ = io.WriteString(writer, first)
		cancel()
		// The leg stops reading once cancelled, so this write is never consumed.
		go func() { _, _ = io.WriteString(writer, "data: {\"type\":\"ping\"}\n\n") }()
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Errorf("%s: want context.Canceled, got %v", name, err)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("%s: did not stop after cancellation", name)
		}
		_ = reader.Close()
		_ = writer.Close()
		if strings.Contains(out.String(), "[DONE]") || strings.Contains(out.String(), "message_stop") || strings.Contains(out.String(), "error") {
			t.Errorf("%s: cancellation must not emit a terminal: %q", name, out.String())
		}
	}
}

// response anthropic-messages>openai-chat, not streamed
// (nonStreamingHandler.js:257-311).
func TestMessagesResponseToChat(t *testing.T) {
	body := `{"id":"msg_01","type":"message","model":"claude-x","stop_reason":"max_tokens","content":[
	  {"type":"thinking","thinking":"plan"},{"type":"text","text":"\u0060\u0060\u0060json\n{\"a\":1}\n\u0060\u0060\u0060"},
	  {"type":"tool_use","id":"toolu_1","name":"srv_lookup","input":{"q":"x"}}],
	  "usage":{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":20}}`
	got, err := MessagesResponseToChat([]byte(body), testNames())
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	_ = json.Unmarshal(got, &result)
	delete(result, "created")
	gotJSON, _ := json.Marshal(result)
	// The tool call forces tool_calls (nonStreamingHandler.js:397-405); content
	// beats reasoning, so reasoning_content is dropped (:428-434); the fence is
	// stripped from the text (:277).
	equalJSON(t, gotJSON, `{"id":"chatcmpl-msg_01","object":"chat.completion","model":"claude-x","choices":[{"index":0,"finish_reason":"tool_calls","message":{"role":"assistant","content":"{\"a\":1}","tool_calls":[{"id":"toolu_1","type":"function","function":{"name":"srv.lookup","arguments":"{\"q\":\"x\"}"}}]}}],
	 "usage":{"prompt_tokens":130,"completion_tokens":5,"total_tokens":135,"prompt_tokens_details":{"cached_tokens":100,"cache_creation_tokens":20}}}`)

	got, _ = MessagesResponseToChat([]byte(`{"id":"m","model":"c","stop_reason":"max_tokens","content":[{"type":"thinking","thinking":"only thoughts"}]}`), nil)
	_ = json.Unmarshal(got, &result)
	choice := result["choices"].([]any)[0].(map[string]any)
	message := choice["message"].(map[string]any)
	if choice["finish_reason"] != "length" || message["content"] != "" || message["reasoning_content"] != "only thoughts" {
		t.Fatalf("with no content the reasoning is kept and max_tokens maps to length: %v", choice)
	}
	same := `{"choices":[{"message":{"content":"x"}}]}`
	if got, _ := MessagesResponseToChat([]byte(same), nil); string(got) != same {
		t.Fatalf("a body already in Chat shape is returned as is: %s", got)
	}
}

// response openai-chat>anthropic-messages, not streamed
// (nonstream-to-claude.js:15-58).
func TestChatResponseToMessages(t *testing.T) {
	body := `{"id":"chatcmpl-abc","model":"m","choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","reasoning_content":"plan","content":"hi","tool_calls":[{"id":"call_1","type":"function","function":{"name":"x","arguments":"{\"k\":1}"}},{"function":{"name":"y","arguments":"not json"}}]}}],
	 "usage":{"prompt_tokens":130,"completion_tokens":42,"total_tokens":172,"prompt_tokens_details":{"cached_tokens":100,"cache_creation_tokens":20}}}`
	got, err := ChatResponseToMessages([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	_ = json.Unmarshal(got, &result)
	content := result["content"].([]any)
	second := content[3].(map[string]any)
	if strings.HasPrefix(second["id"].(string), "toolu_") == false {
		t.Fatalf("a call with no id gets a generated toolu_ id: %v", second)
	}
	content[3].(map[string]any)["id"] = "generated"
	result["content"] = content
	gotJSON, _ := json.Marshal(result)
	// The pin rewrites only "chatcmpl_" and "resp_" (nonstream-to-claude.js:18), so
	// the usual hyphenated "chatcmpl-" id is kept as is.
	equalJSON(t, gotJSON, `{"id":"chatcmpl-abc","type":"message","role":"assistant","model":"m","stop_reason":"tool_use","stop_sequence":null,
	 "content":[{"type":"thinking","thinking":"plan"},{"type":"text","text":"hi"},{"type":"tool_use","id":"call_1","name":"x","input":{"k":1}},{"type":"tool_use","id":"generated","name":"y","input":{}}],
	 "usage":{"input_tokens":10,"output_tokens":42,"cache_read_input_tokens":100,"cache_creation_input_tokens":20}}`)
	renamed, _ := ChatResponseToMessages([]byte(`{"id":"resp_9","choices":[{"message":{"content":"x"}}]}`))
	if !strings.Contains(string(renamed), `"id":"msg_9"`) {
		t.Fatalf("resp_ ids become msg_: %s", renamed)
	}
	empty, _ := ChatResponseToMessages([]byte(`{"choices":[{"finish_reason":"stop","message":{"role":"assistant"}}]}`))
	var parsed map[string]any
	_ = json.Unmarshal(empty, &parsed)
	if parsed["content"].([]any)[0].(map[string]any)["text"] != "" || parsed["stop_reason"] != "end_turn" || parsed["model"] != "unknown" {
		t.Fatalf("an empty answer still carries one empty text block: %s", empty)
	}
}

// The ticket's done-when: tool ids, finish reasons and usage survive a round
// trip through the hub, on the wire and in the stream.
func TestRoundTripKeepsToolIDsFinishReasonsAndUsage(t *testing.T) {
	var chat, back bytes.Buffer
	if err := MessagesStreamToChat(context.Background(), strings.NewReader(messagesStream), &chat, testNames()); err != nil {
		t.Fatal(err)
	}
	if err := ChatStreamToMessages(context.Background(), &chat, &back); err != nil {
		t.Fatal(err)
	}
	var starts []string
	var delta map[string]any
	for _, event := range sseData(t, back.String()) {
		m := event.(map[string]any)
		switch m["type"] {
		case "content_block_start":
			if block := m["content_block"].(map[string]any); block["type"] == "tool_use" {
				starts = append(starts, block["id"].(string)+":"+block["name"].(string))
			}
		case "message_delta":
			delta = m
		}
	}
	if !reflect.DeepEqual(starts, []string{"toolu_1:srv.lookup", "toolu_2:noargs"}) {
		t.Fatalf("tool ids survive: %v", starts)
	}
	usage, _ := json.Marshal(delta["usage"])
	if delta["delta"].(map[string]any)["stop_reason"] != "tool_use" ||
		string(usage) != `{"cache_creation_input_tokens":20,"cache_read_input_tokens":100,"input_tokens":10,"output_tokens":42}` {
		t.Fatalf("finish reason and usage survive: %v", delta)
	}
}
