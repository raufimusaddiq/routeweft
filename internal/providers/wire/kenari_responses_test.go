package wire

import (
	"errors"
	"io"
	"strings"
	"testing"
)

// The request the pin converts (responsesFunctionTools.js:35-73, 89-122): Codex's
// freeform `exec` tool, a recap text.format, and a replayed custom call.
const kenariCodexRequest = `{"model":"m","text":{"format":{"type":"json_schema","name":"r"},"verbosity":"low"},` +
	`"tools":[{"type":"custom","name":" exec ","description":"Run","format":{"type":"grammar","syntax":"lark","definition":"start: /.+/"}},{"type":"function","name":"keep","parameters":{}}],` +
	`"input":[{"type":"custom_tool_call","id":"ctc_1","call_id":"c1","name":"exec","input":"ls -la"},` +
	`{"type":"custom_tool_call_output","id":"ctco_1","call_id":"c1","output":[{"type":"input_text","text":"a"},{"type":"input_text","text":"b"}]}]}`

func TestKenariRequestDeclaresCustomToolsAsFunctions(t *testing.T) {
	same(t, finalize(t, "kenari", responses, kenariCodexRequest), `{"model":"m","text":{"verbosity":"low"},`+
		`"tools":[{"type":"function","name":"exec","description":"Run\n\nlark\nstart: /.+/","parameters":{"type":"object","properties":{"input":{"type":"string","description":"Raw freeform input for this custom tool"}},"required":["input"],"additionalProperties":false},"strict":false},{"type":"function","name":"keep","parameters":{}}],`+
		`"input":[{"type":"function_call","id":"fc_1","call_id":"c1","name":"exec","arguments":"{\"input\":\"ls -la\"}"},`+
		`{"type":"function_call_output","id":"fco_1","call_id":"c1","output":"ab"}]}`)
}

func TestKenariRequestLeavesWhatItCannotConvertAlone(t *testing.T) {
	for name, in := range map[string]string{
		"chat wire":              `{"tools":[{"type":"custom","name":"exec"}]}`,
		"nameless custom tool":   `{"tools":[{"type":"custom","description":"x"}]}`,
		"custom call, no input":  `{"input":[{"type":"custom_tool_call","call_id":"c","name":"exec"}]}`,
		"no custom tools at all": `{"model":"m","tools":[{"type":"function","name":"f"}],"text":{"verbosity":"low"}}`,
	} {
		protocol := responses
		if name == "chat wire" {
			protocol = chat
		}
		if got := finalize(t, "kenari", protocol, in); got != in {
			t.Errorf("%s: body changed: %s", name, got)
		}
	}
}

func TestResponsesCustomToolNames(t *testing.T) {
	got := ResponsesCustomToolNames("kenari", []byte(kenariCodexRequest))
	if len(got) != 1 || !got["exec"] {
		t.Fatalf("names = %v, want {exec}", got)
	}
	if ResponsesCustomToolNames("openai", []byte(kenariCodexRequest)) != nil {
		t.Error("a provider without the quirk reported names")
	}
	if ResponsesCustomToolNames("kenari", []byte(`{"tools":[{"type":"function","name":"f"}]}`)) != nil {
		t.Error("a request with no custom tool reported names")
	}
}

var execNames = map[string]bool{"exec": true}

// The shape kenari answers with (a function_call carrying {"input": ...}) and the
// shape the client declared (custom_tool_call carrying input).
func TestRewriteResponsesBody(t *testing.T) {
	in := `{"id":"resp_1","object":"response","status":"completed","output":[` +
		`{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"ok"}]},` +
		`{"type":"function_call","id":"fc_9","call_id":"c9","name":"exec","arguments":"{\"input\":\"ls -la\"}","status":"completed"},` +
		`{"type":"function_call","id":"fc_8","call_id":"c8","name":"keep","arguments":"{\"x\":1}","status":"completed"}],"usage":{"input_tokens":3}}`
	got, err := RewriteResponsesBody(execNames, []byte(in))
	if err != nil {
		t.Fatal(err)
	}
	same(t, string(got), `{"id":"resp_1","object":"response","status":"completed","output":[`+
		`{"type":"message","id":"msg_1","role":"assistant","content":[{"type":"output_text","text":"ok"}]},`+
		`{"type":"custom_tool_call","id":"ctc_9","call_id":"c9","name":"exec","input":"ls -la","status":"completed"},`+
		`{"type":"function_call","id":"fc_8","call_id":"c8","name":"keep","arguments":"{\"x\":1}","status":"completed"}],"usage":{"input_tokens":3}}`)

	// Nothing to rewrite: byte for byte.
	plain := `{"output":[{"type":"function_call","name":"keep","arguments":"{}"}]}`
	if got, err := RewriteResponsesBody(execNames, []byte(plain)); err != nil || string(got) != plain {
		t.Errorf("untouched body = %s, %v", got, err)
	}
}

func TestRewriteResponsesBodyRefusesArgumentsItCannotRead(t *testing.T) {
	for name, arguments := range map[string]string{
		"not json":        `ls -la`,
		"empty":           ``,
		"no input":        `{\"cmd\":\"ls\"}`,
		"non-string":      `{\"input\":[\"ls\"]}`,
		"null input":      `{\"input\":null}`,
		"number input":    `{\"input\":5}`,
		"json not object": `[1]`,
	} {
		in := `{"output":[{"type":"function_call","id":"fc_1","name":"exec","arguments":"` + arguments + `"}]}`
		got, err := RewriteResponsesBody(execNames, []byte(in))
		if !errors.Is(err, ErrCustomToolArguments) {
			t.Errorf("%s: err = %v, body %s; want ErrCustomToolArguments", name, err, got)
		}
	}
}

const kenariStream = `event: response.created
data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress","output":[]}}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"c1","name":"exec","arguments":"","status":"in_progress"}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"{\"input\":"}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_1","output_index":0,"delta":"\"ls\"}"}

event: response.function_call_arguments.done
data: {"type":"response.function_call_arguments.done","item_id":"fc_1","output_index":0,"name":"exec","arguments":"{\"input\":\"ls\"}"}

event: response.output_item.done
data: {"type":"response.output_item.done","output_index":0,"item":{"type":"function_call","id":"fc_1","call_id":"c1","name":"exec","arguments":"{\"input\":\"ls\"}","status":"completed"}}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_2","call_id":"c2","name":"keep","arguments":"","status":"in_progress"}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_2","output_index":1,"delta":"{}"}

event: response.completed
data: {"type":"response.completed","response":{"id":"resp_1","status":"completed","output":[{"type":"function_call","id":"fc_1","call_id":"c1","name":"exec","arguments":"{\"input\":\"ls\"}"},{"type":"function_call","id":"fc_2","call_id":"c2","name":"keep","arguments":"{}"}]}}

`

func relay(t *testing.T, upstream string) string {
	t.Helper()
	out, err := io.ReadAll(ResponsesStream(execNames, io.NopCloser(strings.NewReader(upstream))))
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestResponsesStreamRewritesConvertedCalls(t *testing.T) {
	got := relay(t, kenariStream)
	want := `event: response.created
data: {"type":"response.created","response":{"id":"resp_1","status":"in_progress","output":[]}}

event: response.output_item.added
data: {"item":{"call_id":"c1","id":"ctc_1","input":"","name":"exec","status":"in_progress","type":"custom_tool_call"},"output_index":0,"type":"response.output_item.added"}

event: response.custom_tool_call_input.done
data: {"input":"ls","item_id":"ctc_1","name":"exec","output_index":0,"type":"response.custom_tool_call_input.done"}

event: response.output_item.done
data: {"item":{"call_id":"c1","id":"ctc_1","input":"ls","name":"exec","status":"completed","type":"custom_tool_call"},"output_index":0,"type":"response.output_item.done"}

event: response.output_item.added
data: {"type":"response.output_item.added","output_index":1,"item":{"type":"function_call","id":"fc_2","call_id":"c2","name":"keep","arguments":"","status":"in_progress"}}

event: response.function_call_arguments.delta
data: {"type":"response.function_call_arguments.delta","item_id":"fc_2","output_index":1,"delta":"{}"}

event: response.completed
data: {"response":{"id":"resp_1","output":[{"arguments":null,"call_id":"c1","id":"ctc_1","input":"ls","name":"exec","type":"custom_tool_call"},{"type":"function_call","id":"fc_2","call_id":"c2","name":"keep","arguments":"{}"}],"status":"completed"},"type":"response.completed"}

`
	// response.completed: the arguments key must be gone, not null.
	want = strings.Replace(want, `"arguments":null,`, "", 1)
	if got != want {
		t.Errorf("stream\n got %s\nwant %s", got, want)
	}
}

func TestResponsesStreamIsByteIdenticalWithoutConvertedCalls(t *testing.T) {
	in := strings.Replace(kenariStream, `"name":"exec"`, `"name":"other"`, -1)
	if got := relay(t, in); got != in {
		t.Errorf("stream changed:\n%s", got)
	}
}

func TestResponsesStreamRefusesUnreadableArguments(t *testing.T) {
	const good = `"arguments":"{\"input\":\"ls\"}"`
	for _, bad := range []string{
		// output_item.done, then the arguments.done event, then response.completed.
		strings.Replace(kenariStream, `"call_id":"c1","name":"exec",`+good, `"call_id":"c1","name":"exec","arguments":"ls"`, 1),
		strings.Replace(kenariStream, `"name":"exec",`+good+`}`, `"name":"exec","arguments":"{}"}`, 1),
		strings.Replace(kenariStream, `"name":"exec",`+good+`},{`, `"name":"exec","arguments":"ls"},{`, 1),
	} {
		if bad == kenariStream {
			t.Fatal("fixture edit did not apply")
		}
		got := relay(t, bad)
		if !strings.Contains(got, "event: response.failed\n") || !strings.Contains(got, "kenari_custom_tool_arguments_invalid") {
			t.Errorf("no named refusal:\n%s", got)
		}
		if strings.Contains(got, "response.completed") {
			t.Errorf("stream continued after the refusal:\n%s", got)
		}
		if strings.Contains(got, `"input":"{}"`) || strings.Contains(got, `"input":"ls"}`) {
			t.Errorf("a wrong custom call was emitted:\n%s", got)
		}
	}
}

func TestResponsesStreamRefusesAnEventItCannotInspect(t *testing.T) {
	huge := "event: x\ndata: " + strings.Repeat("a", maxResponsesEvent) + "\n\n"
	got := relay(t, huge)
	if !strings.Contains(got, "kenari_responses_event_too_large") {
		t.Errorf("no refusal: %.200s", got)
	}
}

func TestResponsesStreamWithoutNamesIsTheBodyItself(t *testing.T) {
	body := io.NopCloser(strings.NewReader("x"))
	if ResponsesStream(nil, body) != body {
		t.Error("a request with no converted tool wrapped the stream")
	}
}

func TestResponsesStreamRefusalContinuesTheSequence(t *testing.T) {
	in := strings.Replace(kenariStream, `"type":"response.output_item.added","output_index":0`, `"type":"response.output_item.added","sequence_number":7,"output_index":0`, 1)
	in = strings.Replace(in, `"name":"exec","arguments":"{\"input\":\"ls\"}"}`, `"name":"exec","arguments":"{\"input\":null}"}`, 1)
	if got := relay(t, in); !strings.Contains(got, `"sequence_number":8`) {
		t.Errorf("injected event not numbered after 7:\n%s", got)
	}
}

func TestResponsesStreamRewritesTruncatedTerminalEvents(t *testing.T) {
	for _, kind := range []string{"response.incomplete", "response.done"} {
		in := "event: " + kind + "\ndata: {\"type\":\"" + kind + "\",\"response\":{\"status\":\"incomplete\",\"output\":[{\"type\":\"function_call\",\"id\":\"fc_1\",\"call_id\":\"c\",\"name\":\"exec\",\"arguments\":\"{\\\"input\\\":\\\"ls\\\"}\"}]}}\n\n"
		got := relay(t, in)
		if !strings.HasPrefix(got, "event: "+kind+"\n") || !strings.Contains(got, `"type":"custom_tool_call"`) || !strings.Contains(got, `"input":"ls"`) || strings.Contains(got, `"type":"function_call"`) {
			t.Errorf("%s not rewritten:\n%s", kind, got)
		}
	}
}

func TestResponsesStreamRewritesArgumentsDoneForAnUnannouncedItem(t *testing.T) {
	in := "event: response.function_call_arguments.done\ndata: {\"type\":\"response.function_call_arguments.done\",\"item_id\":\"fc_1\",\"name\":\"exec\",\"arguments\":\"{\\\"input\\\":\\\"ls\\\"}\"}\n\n"
	if got := relay(t, in); !strings.Contains(got, "response.custom_tool_call_input.done") || !strings.Contains(got, `"item_id":"ctc_1"`) {
		t.Errorf("not rewritten:\n%s", got)
	}
}

func TestCustomToolNamesSkipsAMalformedEntry(t *testing.T) {
	got := ResponsesCustomToolNames("kenari", []byte(`{"tools":["junk",null,{"type":"custom","name":"exec"}]}`))
	if len(got) != 1 || !got["exec"] {
		t.Errorf("names = %v, want {exec}", got)
	}
}
