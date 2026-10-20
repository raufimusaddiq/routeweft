package commandcode

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

func convertWith(t *testing.T, chat string, repair bool) (map[string]any, error) {
	t.Helper()
	body, err := concerns.WireObject([]byte(chat))
	if err != nil {
		t.Fatal(err)
	}
	out, err := ChatRequestToCommandCode(body, "m", true, repair)
	if err != nil {
		return nil, err
	}
	result, err := concerns.WireObject(out)
	if err != nil {
		t.Fatal(err)
	}
	return result, nil
}

func convert(t *testing.T, chat string) (map[string]any, error) { return convertWith(t, chat, true) }

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

func params(t *testing.T, chat string) map[string]any {
	t.Helper()
	return concerns.Object(mustConvert(t, chat)["params"])
}

func asJSON(v any) string { b, _ := json.Marshal(v); return string(b) }

const user = `"messages":[{"role":"user","content":"hi"}]`

func TestEnvelopeIsNeutral(t *testing.T) {
	oldNow, oldID := now, newThreadID
	defer func() { now, newThreadID = oldNow, oldID }()
	now = func() time.Time { return time.Date(2026, 10, 20, 23, 59, 0, 0, time.FixedZone("x", -8*3600)) }
	newThreadID = func() string { return "thread-1" }
	got := mustConvert(t, `{`+user+`}`)
	config := concerns.Object(got["config"])
	// V35: nothing about the router host reaches the upstream.
	if config["workingDir"] != "/" || config["environment"] != "linux" || config["date"] != "2026-10-21" {
		t.Errorf("config = %v", config)
	}
	if got["threadId"] != "thread-1" || got["memory"] != "" {
		t.Errorf("envelope = %v", got)
	}
	if raw := asJSON(got); strings.Contains(raw, "/home") || strings.Contains(raw, "/tmp") {
		t.Errorf("a host path leaked: %s", raw)
	}
	p := concerns.Object(got["params"])
	if p["model"] != "m" || p["stream"] != true {
		t.Errorf("params = %v", p)
	}
	if a, b := randomUUID(), randomUUID(); a == b || len(a) != 36 || a[14] != '4' {
		t.Errorf("thread ids: %s %s", a, b)
	}
}

func TestSamplingAndLimits(t *testing.T) {
	// V34: no temperature is invented, and a given one is carried, zero included.
	for body, want := range map[string]string{
		``:                    `null`,
		`,"temperature":null`: `null`,
		`,"temperature":0.5`:  `0.5`,
		`,"temperature":0`:    `0`,
	} {
		if got := asJSON(params(t, `{`+user+body+`}`)["temperature"]); got != want {
			t.Errorf("%q: temperature %s, want %s", body, got, want)
		}
	}
	if got := asJSON(params(t, `{`+user+`,"top_p":0.9}`)["top_p"]); got != "0.9" {
		t.Errorf("top_p = %s", got)
	}
	// max_tokens precedence, V22.
	for body, want := range map[string]string{
		``: `64000`, `,"max_tokens":null`: `64000`,
		`,"max_tokens":10`: `10`, `,"max_completion_tokens":11`: `11`, `,"max_output_tokens":12`: `12`,
		`,"max_tokens":10,"max_completion_tokens":11,"max_output_tokens":12`: `10`,
		`,"max_completion_tokens":11,"max_output_tokens":12`:                 `11`,
	} {
		if got := asJSON(params(t, `{`+user+body+`}`)["max_tokens"]); got != want {
			t.Errorf("%q: max_tokens %s, want %s", body, got, want)
		}
	}
}

// Every Chat field is carried, not carried for a stated reason, or refused.
func TestChatRequestFieldsAreCarriedOrRefused(t *testing.T) {
	for _, field := range []string{`"top_logprobs":2`, `"store":true`, `"metadata":{"a":"b"}`, `"user":"u"`, `"parallel_tool_calls":false`,
		`"stream_options":{"include_usage":true}`, `"prompt_cache_key":"k"`, `"service_tier":"auto"`, `"modalities":["text"]`,
		`"stop":""`, `"stop":[]`, `"logit_bias":{}`, `"reasoning_effort":""`, `"reasoning":{}`, `"reasoning":null`, `"seed":null`,
		`"presence_penalty":null`, `"response_format":{"type":"text"}`, `"response_format":null`} {
		t.Run("not carried/"+field, func(t *testing.T) {
			p := params(t, `{`+user+`,`+field+`}`)
			for _, key := range []string{"stop", "seed", "response_format", "reasoning_effort", "tool_choice", "logit_bias", "tools", "user", "metadata"} {
				if _, present := p[key]; present {
					t.Errorf("%s reached params via %s", key, field)
				}
			}
		})
	}
	for name, body := range map[string]string{
		"n>1": `{` + user + `,"n":2}`, "logprobs": `{` + user + `,"logprobs":true}`, "audio output": `{` + user + `,"modalities":["text","audio"]}`,
		"audio": `{` + user + `,"audio":{"voice":"x"}}`, "functions": `{` + user + `,"functions":[{"name":"f"}]}`, "function_call": `{` + user + `,"function_call":"auto"}`,
		"seed": `{` + user + `,"seed":0}`, "presence_penalty": `{` + user + `,"presence_penalty":0}`, "frequency_penalty": `{` + user + `,"frequency_penalty":0.1}`,
		"stop string": `{` + user + `,"stop":"x"}`, "stop list": `{` + user + `,"stop":["x"]}`,
		"logit_bias": `{` + user + `,"logit_bias":{"1":1}}`, "logit_bias list": `{` + user + `,"logit_bias":[1]}`, "logit_bias string": `{` + user + `,"logit_bias":"x"}`,
		"reasoning_effort": `{` + user + `,"reasoning_effort":"high"}`, "reasoning": `{` + user + `,"reasoning":{"effort":"low"}}`,
		"json_object": `{` + user + `,"response_format":{"type":"json_object"}}`, "json_schema": `{` + user + `,"response_format":{"type":"json_schema"}}`,
		"format string": `{` + user + `,"response_format":"json"}`,
	} {
		t.Run("refused/"+name, func(t *testing.T) { mustRefuse(t, body) })
	}
}

// Cross product: tool_choice x tools, as for Cursor. The params never name a
// tool_choice, and the tools are withheld exactly for "none".
func TestToolChoiceAcrossTools(t *testing.T) {
	const tools = `"tools":[{"type":"function","function":{"name":"f"}}]`
	for _, c := range []struct {
		json    string
		verdict string // "sent", "withheld", "refused"
	}{
		{``, "sent"}, {`,"tool_choice":null`, "sent"}, {`,"tool_choice":""`, "sent"}, {`,"tool_choice":"auto"`, "sent"},
		{`,"tool_choice":"none"`, "withheld"},
		{`,"tool_choice":"required"`, "refused"}, {`,"tool_choice":{"type":"function","function":{"name":"f"}}`, "refused"},
		{`,"tool_choice":{"type":"function","function":{"name":"gone"}}`, "refused"}, {`,"tool_choice":{}`, "refused"},
		{`,"tool_choice":[]`, "refused"}, {`,"tool_choice":7`, "refused"}, {`,"tool_choice":true`, "refused"},
	} {
		for _, declared := range []bool{true, false} {
			body := `{` + user + c.json + `}`
			if declared {
				body = `{` + user + `,` + tools + c.json + `}`
			}
			if c.verdict == "refused" {
				mustRefuse(t, body)
				continue
			}
			p := params(t, body)
			if _, present := p["tool_choice"]; present {
				t.Errorf("%s: tool_choice was sent", body)
			}
			_, hasTools := p["tools"]
			if want := declared && c.verdict == "sent"; hasTools != want {
				t.Errorf("%s: tools sent=%v want %v", body, hasTools, want)
			}
		}
	}
}

func TestToolsAreConverted(t *testing.T) {
	got := params(t, `{`+user+`,"tools":[
		{"type":"function","function":{"name":"a","description":"d","parameters":{"type":"object","properties":{}}}},
		{"type":"function","function":{"name":"b"}},
		{"name":"c","description":"e","input_schema":{"type":"object"}},
		{"name":"d","parameters":{"type":"object"}}]}`)["tools"]
	want := `[{"description":"d","input_schema":{"properties":{},"type":"object"},"name":"a"},{"input_schema":{"type":"object"},"name":"b"},` +
		`{"description":"e","input_schema":{"type":"object"},"name":"c"},{"input_schema":{"type":"object"},"name":"d"}]`
	if asJSON(got) != want {
		t.Errorf("tools = %s", asJSON(got))
	}
	if _, present := params(t, `{`+user+`,"tools":[]}`)["tools"]; present {
		t.Error("an empty list sends no tools")
	}
	for name, tools := range map[string]string{
		"custom": `[{"type":"custom","custom":{"name":"c"}}]`, "web search": `[{"type":"web_search"}]`, "function without body": `[{"type":"function"}]`,
		"no name": `[{"type":"function","function":{}}]`, "bare without schema": `[{"name":"f"}]`, "bare without name": `[{"input_schema":{}}]`,
		"not object": `["f"]`, "strict": `[{"type":"function","function":{"name":"f","strict":true}}]`, "not a list": `{"type":"function"}`,
		"one bad":    `[{"type":"function","function":{"name":"f"}},{"type":"custom"}]`,
		"typed bare": `[{"type":"x","name":"f","input_schema":{}}]`,
	} {
		t.Run(name, func(t *testing.T) { mustRefuse(t, `{`+user+`,"tools":`+tools+`}`) })
	}
	params(t, `{`+user+`,"tools":[{"type":"function","function":{"name":"f","strict":false}}]}`)
	// One rule for both legs: a named function with no parameters is a tool (with
	// an empty object schema), parameters that are not an object are refused.
	for _, parameters := range []string{``, `,"parameters":null`, `,"parameters":{}`} {
		got := asJSON(params(t, `{`+user+`,"tools":[{"type":"function","function":{"name":"f"`+parameters+`}}]}`)["tools"])
		if got != `[{"input_schema":{"type":"object"},"name":"f"}]` {
			t.Errorf("%q: tools = %s", parameters, got)
		}
	}
	for _, parameters := range []string{`"x"`, `[]`, `7`, `true`} {
		mustRefuse(t, `{`+user+`,"tools":[{"type":"function","function":{"name":"f","parameters":`+parameters+`}}]}`)
		mustRefuse(t, `{`+user+`,"tools":[{"name":"f","input_schema":`+parameters+`}]}`)
	}
}

func TestMessagesAreConverted(t *testing.T) {
	got := params(t, `{"messages":[
		{"role":"system","content":"A"},
		{"role":"developer","content":[{"type":"text","text":"B1"},{"type":"text","text":"B2"}]},
		{"role":"system","content":""},
		{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,QQ=="}}]},
		{"role":"assistant","content":"calling","reasoning_content":"think","tool_calls":[
			{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":1}"}},
			{"id":"call_2","function":{"name":"noargs","arguments":""}},
			{"id":"call_3","function":{"name":"obj","arguments":{"k":"v"}}}]},
		{"role":"tool","tool_call_id":"call_1","content":"R1"},
		{"role":"tool","tool_call_id":"call_2","name":"given","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]},
		{"role":"assistant","content":null,"tool_calls":[{"id":"call_4","function":{"name":"x","arguments":"{}"}}]},
		{"role":"assistant","content":""},
		{"role":"user","content":null}]}`)
	if got["system"] != "A\n\nB1\nB2" {
		t.Errorf("system = %q", got["system"])
	}
	var want []any
	_ = json.Unmarshal([]byte(`[
		{"role":"user","content":[{"type":"text","text":"look"},{"type":"image","image":"data:image/png;base64,QQ==","mimeType":"image/png","mediaType":"image/png"}]},
		{"role":"assistant","content":[{"type":"reasoning","text":"think"},{"type":"text","text":"calling"},
			{"type":"tool-call","toolCallId":"call_1","toolName":"lookup","input":{"q":1}},
			{"type":"tool-call","toolCallId":"call_2","toolName":"noargs","input":{}},
			{"type":"tool-call","toolCallId":"call_3","toolName":"obj","input":{"k":"v"}}]},
		{"role":"tool","content":[{"type":"tool-result","toolCallId":"call_1","toolName":"lookup","output":{"type":"text","value":"R1"}}]},
		{"role":"tool","content":[{"type":"tool-result","toolCallId":"call_2","toolName":"given","output":{"type":"text","value":"a\nb"}}]},
		{"role":"assistant","content":[{"type":"reasoning","text":" "},{"type":"tool-call","toolCallId":"call_4","toolName":"x","input":{}}]},
		{"role":"assistant","content":[{"type":"text","text":""}]},
		{"role":"user","content":[{"type":"text","text":""}]}]`), &want)
	if asJSON(got["messages"]) != asJSON(want) {
		t.Errorf("messages = %s", asJSON(got["messages"]))
	}
	for _, key := range []string{"reasoning_content", "thought", "reasoning"} {
		p := params(t, `{"messages":[{"role":"assistant","content":"t","`+key+`":"why"},{"role":"user","content":"u"}]}`)
		if !strings.Contains(asJSON(p["messages"]), `{"text":"why","type":"reasoning"}`) {
			t.Errorf("%s was not carried: %s", key, asJSON(p["messages"]))
		}
	}
}

// Cross product: every content shape x every role.
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
		if role == "user" {
			mustConvert(t, turn(`[`+image+`]`))
		} else {
			t.Run(role+"/image outside a user turn", func(t *testing.T) { mustRefuse(t, turn(`[`+image+`]`)) })
		}
		for name, content := range map[string]string{
			"remote": `[{"type":"image_url","image_url":{"url":"https://x/y.png"}}]`, "no url": `[{"type":"image_url","image_url":{}}]`,
			"audio": `[{"type":"input_audio","input_audio":{"data":"x"}}]`, "file": `[{"type":"file","file":{"file_id":"f"}}]`,
			"unknown": `[{"type":"hologram"}]`, "no type": `[{"text":"t"}]`, "number": `7`, "object": `{"type":"text","text":"t"}`,
			"text nil": `[{"type":"text"}]`, "text num": `[{"type":"text","text":3}]`, "part str": `["t"]`,
		} {
			t.Run(role+"/"+name, func(t *testing.T) { mustRefuse(t, turn(content)) })
		}
	}
}

func TestMessageShapesAreRefused(t *testing.T) {
	for name, body := range map[string]string{
		"unknown role": `{"messages":[{"role":"critic","content":"x"}]}`, "not an object": `{"messages":["hi"]}`,
		"empty list": `{"messages":[]}`, "no messages": `{}`, "only system": `{"messages":[{"role":"system","content":"s"}]}`,
		"non-function call": `{"messages":[{"role":"assistant","tool_calls":[{"id":"c","type":"custom","function":{"name":"f"}}]}]}`,
		"call no name":      `{"messages":[{"role":"assistant","tool_calls":[{"id":"c","function":{}}]}]}`,
		"call not object":   `{"messages":[{"role":"assistant","tool_calls":["x"]}]}`,
		"calls not a list":  `{"messages":[{"role":"assistant","tool_calls":{"id":"c"}}]}`,
		// V33: arguments that are not a JSON object are not rewritten to {}.
		"args malformed": `{"messages":[{"role":"assistant","tool_calls":[{"id":"c","function":{"name":"f","arguments":"{nope"}}]}]}`,
		"args array":     `{"messages":[{"role":"assistant","tool_calls":[{"id":"c","function":{"name":"f","arguments":"[1]"}}]}]}`,
		"args number":    `{"messages":[{"role":"assistant","tool_calls":[{"id":"c","function":{"name":"f","arguments":7}}]}]}`,
		"args string":    `{"messages":[{"role":"assistant","tool_calls":[{"id":"c","function":{"name":"f","arguments":"\"x\""}}]}]}`,
	} {
		t.Run(name, func(t *testing.T) { mustRefuse(t, body) })
	}
	call := `{"messages":[{"role":"assistant","content":"","tool_calls":[{"type":"function","function":{"name":"f"}}]},{"role":"user","content":"u"}]}`
	if _, err := convertWith(t, call, false); err == nil {
		t.Error("an id-less call in a converted body must be refused")
	}
	if out, err := convertWith(t, call, true); err != nil || !strings.Contains(asJSON(out), `"toolCallId":"call_msg0_tc0_f"`) {
		t.Errorf("the client's own body is repaired: %v %s", err, asJSON(out))
	}
	for _, empty := range []string{`"tool_call_id":""`, `"tool_call_id":null`, `"name":"n"`} {
		if _, err := convertWith(t, `{"messages":[{"role":"tool","content":"r",`+empty+`},{"role":"user","content":"u"}]}`, false); err == nil {
			t.Errorf("%s: must be refused", empty)
		}
	}
	toolNoID := `{"messages":[{"role":"tool","content":"r"},{"role":"user","content":"u"}]}`
	if _, err := convertWith(t, toolNoID, false); err == nil {
		t.Error("a tool result with no id in a converted body must be refused")
	}
	// An orphan result takes the fallback name, not an empty one.
	p := params(t, `{"messages":[{"role":"tool","tool_call_id":"zz","content":"r"},{"role":"user","content":"u"}]}`)
	if !strings.Contains(asJSON(p["messages"]), `"toolName":"unknown_tool"`) {
		t.Errorf("tool name: %s", asJSON(p["messages"]))
	}
}
