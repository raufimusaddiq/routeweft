package cursor

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

func convert(t *testing.T, chat string) (map[string]any, error) {
	t.Helper()
	body, err := concerns.WireObject([]byte(chat))
	if err != nil {
		t.Fatal(err)
	}
	out, err := ChatRequestToCursor(body, "m", true, true)
	if err != nil {
		return nil, err
	}
	result, err := concerns.WireObject(out)
	if err != nil {
		t.Fatal(err)
	}
	return result, nil
}

func mustConvert(t *testing.T, chat string) map[string]any {
	t.Helper()
	result, err := convert(t, chat)
	if err != nil {
		t.Fatalf("%s: %v", chat, err)
	}
	return result
}

func mustRefuse(t *testing.T, chat string) {
	t.Helper()
	if result, err := convert(t, chat); err == nil || !strings.HasPrefix(err.Error(), "unsupported") {
		t.Fatalf("%s: want a refusal, got %v %v", chat, err, result)
	}
}

func keys(m map[string]any) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

const user = `"messages":[{"role":"user","content":"hi"}]`

// Every Chat request field is carried, not carried for a stated reason (the
// request is still served), or refused. PARITY 6.5 is this table.
func TestChatRequestFieldsAreCarriedOrRefused(t *testing.T) {
	got := mustConvert(t, `{`+user+`}`)
	if !reflect.DeepEqual(keys(got), []string{"messages", "model", "stream"}) || got["model"] != "m" || got["stream"] != true {
		t.Errorf("a plain request carries exactly model, stream and messages: %v", got)
	}
	// Not carried, and the request is still served: the wire has no place for the
	// field, or it is bookkeeping. Nothing may reach the output.
	for _, field := range []string{`"max_tokens":10`, `"max_completion_tokens":10`, `"max_tokens":null`, `"top_logprobs":2`, `"store":true`,
		`"metadata":{"a":"b"}`, `"user":"u"`, `"parallel_tool_calls":false`, `"stream_options":{"include_usage":true}`,
		`"prompt_cache_key":"k"`, `"service_tier":"auto"`, `"modalities":["text"]`, `"temperature":null`, `"top_p":null`,
		`"stop":""`, `"stop":[]`, `"logit_bias":{}`, `"response_format":{"type":"text"}`, `"response_format":null`} {
		t.Run("not carried/"+field, func(t *testing.T) {
			result := mustConvert(t, `{`+user+`,`+field+`}`)
			if !reflect.DeepEqual(keys(result), []string{"messages", "model", "stream"}) {
				t.Errorf("%s reached the output: %v", field, result)
			}
		})
	}
	for name, body := range map[string]string{
		"n>1":                `{` + user + `,"n":2}`,
		"logprobs":           `{` + user + `,"logprobs":true}`,
		"audio output":       `{` + user + `,"modalities":["text","audio"]}`,
		"audio":              `{` + user + `,"audio":{"voice":"x"}}`,
		"functions":          `{` + user + `,"functions":[{"name":"f"}]}`,
		"function_call":      `{` + user + `,"function_call":"auto"}`,
		"temperature":        `{` + user + `,"temperature":0.5}`,
		"temperature zero":   `{` + user + `,"temperature":0}`,
		"temperature string": `{` + user + `,"temperature":"hot"}`,
		"temperature false":  `{` + user + `,"temperature":false}`,
		"top_p":              `{` + user + `,"top_p":0.9}`,
		"seed":               `{` + user + `,"seed":0}`,
		"presence_penalty":   `{` + user + `,"presence_penalty":0.5}`,
		"frequency_penalty":  `{` + user + `,"frequency_penalty":0}`,
		"stop string":        `{` + user + `,"stop":"###"}`,
		"stop list":          `{` + user + `,"stop":["a"]}`,
		"logit_bias":         `{` + user + `,"logit_bias":{"1":-100}}`,
		"logit_bias list":    `{` + user + `,"logit_bias":[1]}`,
		"logit_bias string":  `{` + user + `,"logit_bias":"x"}`,
		"json_object":        `{` + user + `,"response_format":{"type":"json_object"}}`,
		"json_schema":        `{` + user + `,"response_format":{"type":"json_schema","json_schema":{"schema":{}}}}`,
		"format not object":  `{` + user + `,"response_format":"json"}`,
		"format list":        `{` + user + `,"response_format":[]}`,
	} {
		t.Run("refused/"+name, func(t *testing.T) { mustRefuse(t, body) })
	}
}

// V31: a client's token limit is not carried and not refused, and a later change
// must not quietly start doing either.
func TestMaxTokensIsNotCarriedAndNotRefused(t *testing.T) {
	for _, field := range []string{`"max_tokens":10`, `"max_completion_tokens":10`, `"max_tokens":10,"max_completion_tokens":11`} {
		result := mustConvert(t, `{`+user+`,`+field+`}`)
		for _, name := range []string{"max_tokens", "max_completion_tokens", "max_output_tokens", "options", "params"} {
			if _, present := result[name]; present {
				t.Errorf("%s: %s reached the output: %v", field, name, result)
			}
		}
	}
}

// Cross product: tool_choice x tools. The output never names a tool_choice, and
// the tools are withheld exactly for "none".
func TestToolChoiceAcrossTools(t *testing.T) {
	const tools = `"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object"}}}]`
	type outcome int
	const (
		sent outcome = iota
		withheld
		refused
	)
	for _, choice := range []struct {
		json string
		with outcome // with tools declared
		none outcome // with no tools declared
	}{
		{``, sent, sent},
		{`,"tool_choice":null`, sent, sent},
		{`,"tool_choice":""`, sent, sent},
		{`,"tool_choice":"auto"`, sent, sent},
		{`,"tool_choice":"none"`, withheld, sent},
		{`,"tool_choice":"required"`, refused, refused},
		{`,"tool_choice":"any"`, refused, refused},
		{`,"tool_choice":{"type":"function","function":{"name":"f"}}`, refused, refused},
		{`,"tool_choice":{"type":"function","function":{"name":"gone"}}`, refused, refused},
		{`,"tool_choice":{}`, refused, refused},
		{`,"tool_choice":[]`, refused, refused},
		{`,"tool_choice":7`, refused, refused},
		{`,"tool_choice":true`, refused, refused},
	} {
		for _, declared := range []bool{true, false} {
			body, want := `{`+user+choice.json+`}`, choice.none
			if declared {
				body, want = `{`+user+`,`+tools+choice.json+`}`, choice.with
			}
			if want == refused {
				mustRefuse(t, body)
				continue
			}
			result := mustConvert(t, body)
			if _, present := result["tool_choice"]; present {
				t.Errorf("%s: tool_choice was sent", body)
			}
			_, hasTools := result["tools"]
			if wantTools := declared && want == sent; hasTools != wantTools {
				t.Errorf("%s: tools sent=%v, want %v", body, hasTools, wantTools)
			}
		}
	}
}

func TestToolsAreFunctionsOnly(t *testing.T) {
	good := `{"type":"function","function":{"name":"f","description":"d","parameters":{"type":"object"}}}`
	got := mustConvert(t, `{`+user+`,"tools":[`+good+`]}`)
	var want []any
	_ = json.Unmarshal([]byte(`[`+good+`]`), &want)
	if !reflect.DeepEqual(got["tools"], want) {
		t.Errorf("tools = %v", got["tools"])
	}
	if result := mustConvert(t, `{`+user+`,"tools":[]}`); result["tools"] != nil {
		t.Errorf("an empty tool list sends no tools: %v", result)
	}
	for name, tools := range map[string]string{
		"custom":       `[{"type":"custom","custom":{"name":"c"}}]`,
		"web search":   `[{"type":"web_search"}]`,
		"no function":  `[{"type":"function"}]`,
		"no name":      `[{"type":"function","function":{"parameters":{}}}]`,
		"empty name":   `[{"type":"function","function":{"name":""}}]`,
		"bare":         `[{"name":"f","input_schema":{}}]`,
		"not object":   `["f"]`,
		"strict":       `[{"type":"function","function":{"name":"f","strict":true}}]`,
		"not a list":   `{"type":"function"}`,
		"one bad of 2": `[` + good + `,{"type":"custom"}]`,
	} {
		t.Run(name, func(t *testing.T) { mustRefuse(t, `{`+user+`,"tools":`+tools+`}`) })
	}
	mustConvert(t, `{`+user+`,"tools":[{"type":"function","function":{"name":"f","strict":false}}]}`)
	// One rule for both legs: a named function with no parameters is a tool,
	// parameters that are not an object are refused.
	for _, parameters := range []string{``, `,"parameters":null`, `,"parameters":{}`, `,"parameters":{"type":"object"}`} {
		mustConvert(t, `{`+user+`,"tools":[{"type":"function","function":{"name":"f"`+parameters+`}}]}`)
	}
	for _, parameters := range []string{`"x"`, `[]`, `7`, `true`} {
		mustRefuse(t, `{`+user+`,"tools":[{"type":"function","function":{"name":"f","parameters":`+parameters+`}}]}`)
	}
}

func TestReasoningEffort(t *testing.T) {
	for body, want := range map[string]string{
		`"reasoning_effort":"high"`:                              "high",
		`"reasoning":{"effort":"low"}`:                           "low",
		`"reasoning":{"effort":"low","summary":"auto"}`:          "low",
		`"reasoning_effort":"high","reasoning":{"effort":"low"}`: "high",
		`"reasoning_effort":"","reasoning":{"effort":"low"}`:     "low",
		`"reasoning":{}`:          "",
		`"reasoning":null`:        "",
		`"reasoning_effort":null`: "",
	} {
		got, _ := mustConvert(t, `{`+user+`,`+body+`}`)["reasoning_effort"].(string)
		if got != want {
			t.Errorf("%s: effort %q, want %q", body, got, want)
		}
	}
	for _, body := range []string{`"reasoning_effort":3`, `"reasoning_effort":{"x":1}`, `"reasoning":"high"`, `"reasoning":["high"]`,
		`"reasoning":{"effort":3}`, `"reasoning":{"effort":"low","max_tokens":10}`, `"reasoning":{"budget":1}`} {
		mustRefuse(t, `{`+user+`,`+body+`}`)
	}
}

func TestMessagesAreConverted(t *testing.T) {
	got := mustConvert(t, `{"messages":[
		{"role":"system","content":"be brief"},
		{"role":"developer","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]},
		{"role":"user","content":[{"type":"text","text":"x"},{"type":"text","text":"y"}]},
		{"role":"assistant","content":"calling","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":1}"}},{"id":"call_2","function":{"name":"noargs"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"a<b & \u0001c"},
		{"role":"tool","tool_call_id":"call_2","name":"named","content":[{"type":"text","text":"r"}]},
		{"role":"user","content":""},
		{"role":"assistant","content":""},
		{"role":"assistant","content":"done"}]}`)
	var want []any
	_ = json.Unmarshal([]byte(`[
		{"role":"user","content":"[System Instructions]\nbe brief"},
		{"role":"user","content":"[System Instructions]\nab"},
		{"role":"user","content":"x\ny"},
		{"role":"assistant","content":"calling","tool_calls":[
			{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":1}"}},
			{"id":"call_2","type":"function","function":{"name":"noargs","arguments":"{}"}}]},
		{"role":"user","content":"<tool_result>\n<tool_name>lookup</tool_name>\n<tool_call_id>call_1</tool_call_id>\n<result>a&lt;b &amp; c</result>\n</tool_result>"},
		{"role":"user","content":"<tool_result>\n<tool_name>named</tool_name>\n<tool_call_id>call_2</tool_call_id>\n<result>r</result>\n</tool_result>"},
		{"role":"assistant","content":"done"}]`), &want)
	if !reflect.DeepEqual(got["messages"], want) {
		g, _ := json.Marshal(got["messages"])
		t.Errorf("messages = %s", g)
	}
}

// Cross product: every content shape x every role. A part the model would not
// see is refused, whichever turn holds it.
func TestContentShapesAcrossRoles(t *testing.T) {
	image := `{"type":"image_url","image_url":{"url":"data:image/png;base64,QQ=="}}`
	for _, role := range []string{"system", "developer", "user", "assistant", "tool"} {
		turn := func(content string) string {
			extra := ""
			if role == "tool" {
				extra = `,"tool_call_id":"c"`
			}
			return `{"messages":[{"role":"` + role + `","content":` + content + extra + `},{"role":"user","content":"q"}]}`
		}
		for _, content := range []string{`"text"`, `null`, `[]`, `[{"type":"text","text":"t"}]`, `[{"type":"refusal","refusal":"no"}]`} {
			mustConvert(t, turn(content))
		}
		for name, content := range map[string]string{
			"image":      `[` + image + `]`,
			"text+image": `[{"type":"text","text":"t"},` + image + `]`,
			"remote":     `[{"type":"image_url","image_url":{"url":"https://x/y.png"}}]`,
			"audio":      `[{"type":"input_audio","input_audio":{"data":"x"}}]`,
			"file":       `[{"type":"file","file":{"file_id":"f"}}]`,
			"unknown":    `[{"type":"hologram"}]`,
			"no type":    `[{"text":"t"}]`,
			"number":     `7`,
			"object":     `{"type":"text","text":"t"}`,
			"text nil":   `[{"type":"text"}]`,
			"text num":   `[{"type":"text","text":3}]`,
			"part str":   `["t"]`,
		} {
			t.Run(role+"/"+name, func(t *testing.T) { mustRefuse(t, turn(content)) })
		}
	}
}

func TestMessageShapesAreRefused(t *testing.T) {
	for name, body := range map[string]string{
		"unknown role":      `{"messages":[{"role":"critic","content":"x"}]}`,
		"function role":     `{"messages":[{"role":"function","content":"x"}]}`,
		"not an object":     `{"messages":["hi"]}`,
		"nothing survives":  `{"messages":[{"role":"user","content":""}]}`,
		"empty list":        `{"messages":[]}`,
		"no messages":       `{}`,
		"non-function call": `{"messages":[{"role":"assistant","content":"","tool_calls":[{"id":"c","type":"custom","function":{"name":"f"}}]}]}`,
		"call no name":      `{"messages":[{"role":"assistant","content":"","tool_calls":[{"id":"c","type":"function","function":{}}]}]}`,
		"call not object":   `{"messages":[{"role":"assistant","content":"","tool_calls":["x"]}]}`,
		"calls not a list":  `{"messages":[{"role":"assistant","content":"","tool_calls":{"id":"c"}}]}`,
	} {
		t.Run(name, func(t *testing.T) { mustRefuse(t, body) })
	}
	// A tool result with no id is refused in a converted body, and given one in
	// the client's own, as for CommandCode.
	noID := `{"messages":[{"role":"tool","content":"r"},{"role":"user","content":"u"}]}`
	parsed0, _ := concerns.WireObject([]byte(noID))
	if _, err := ChatRequestToCursor(parsed0, "m", true, false); err == nil {
		t.Error("a tool result with no id in a converted body must be refused")
	}
	for _, empty := range []string{`"tool_call_id":""`, `"tool_call_id":null`, `"name":"n"`} {
		parsed0, _ = concerns.WireObject([]byte(`{"messages":[{"role":"tool","content":"r",` + empty + `},{"role":"user","content":"u"}]}`))
		if _, err := ChatRequestToCursor(parsed0, "m", true, false); err == nil {
			t.Errorf("%s: must be refused", empty)
		}
	}
	// A call with no id is given one when the body is the client's own, and
	// refused when it was converted from another shape and should have had one.
	body := `{"messages":[{"role":"assistant","content":"","tool_calls":[{"type":"function","function":{"name":"f"}}]},{"role":"user","content":"u"}]}`
	parsed, _ := concerns.WireObject([]byte(body))
	if _, err := ChatRequestToCursor(parsed, "m", true, false); err == nil {
		t.Error("an id-less call in a converted body must be refused")
	}
	parsed, _ = concerns.WireObject([]byte(body))
	out, err := ChatRequestToCursor(parsed, "m", true, true)
	if err != nil || !strings.Contains(string(out), `"id":"call_msg0_tc0_f"`) {
		t.Errorf("the client's own body is repaired: %v %s", err, out)
	}
}
