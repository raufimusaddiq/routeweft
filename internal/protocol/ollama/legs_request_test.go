package ollama

import (
	"encoding/json"
	"reflect"
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
	out, err := ChatRequestToOllama(body, "m", true, true)
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

// Every Chat request field is carried, not carried for a stated reason (the
// request is still served), or refused. PARITY 6.4 is this table.
func TestChatRequestFieldsAreCarriedOrRefused(t *testing.T) {
	const user = `"messages":[{"role":"user","content":"hi"}]`
	carried := map[string]struct{ body, path, want string }{
		"model and stream":    {`{` + user + `}`, "model", `"m"`},
		"temperature":         {`{` + user + `,"temperature":0.5}`, "options.temperature", `0.5`},
		"top_p":               {`{` + user + `,"top_p":0.9}`, "options.top_p", `0.9`},
		"max_tokens":          {`{` + user + `,"max_tokens":10}`, "options.num_predict", `10`},
		"max_completion":      {`{` + user + `,"max_completion_tokens":11}`, "options.num_predict", `11`},
		"max_tokens wins":     {`{` + user + `,"max_tokens":10,"max_completion_tokens":11}`, "options.num_predict", `10`},
		"tools":               {`{` + user + `,"tools":[{"type":"function","function":{"name":"f"}}]}`, "tools", `[{"function":{"name":"f"},"type":"function"}]`},
		"tool_choice auto":    {`{` + user + `,"tools":[{"type":"function","function":{"name":"f"}}],"tool_choice":"auto"}`, "tools.0.function.name", `"f"`},
		"stop string":         {`{` + user + `,"stop":"###"}`, "options.stop", `["###"]`},
		"stop list":           {`{` + user + `,"stop":["a","b"]}`, "options.stop", `["a","b"]`},
		"seed":                {`{` + user + `,"seed":7}`, "options.seed", `7`},
		"presence_penalty":    {`{` + user + `,"presence_penalty":0.5}`, "options.presence_penalty", `0.5`},
		"frequency_penalty":   {`{` + user + `,"frequency_penalty":0.25}`, "options.frequency_penalty", `0.25`},
		"json_object":         {`{` + user + `,"response_format":{"type":"json_object"}}`, "format", `"json"`},
		"json_schema":         {`{` + user + `,"response_format":{"type":"json_schema","json_schema":{"name":"n","schema":{"type":"object"}}}}`, "format", `{"type":"object"}`},
		"text format":         {`{` + user + `,"response_format":{"type":"text"}}`, "format", `null`},
		"tool_choice none":    {`{` + user + `,"tools":[{"type":"function","function":{"name":"f"}}],"tool_choice":"none"}`, "tools", `null`},
		"empty logit_bias":    {`{` + user + `,"logit_bias":{}}`, "options", `null`},
		"text parts joined":   {`{"messages":[{"role":"user","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]}]}`, "messages.0.content", `"a\nb"`},
		"developer is system": {`{"messages":[{"role":"developer","content":"be brief"},{"role":"user","content":"hi"}]}`, "messages.0.role", `"system"`},
	}
	for name, c := range carried {
		t.Run("carried/"+name, func(t *testing.T) {
			got, _ := json.Marshal(lookup(mustConvert(t, c.body), c.path))
			if string(got) != c.want {
				t.Errorf("%s = %s, want %s", c.path, got, c.want)
			}
		})
	}
	// Not carried: the pin sends none of these and nothing the request asks for
	// is lost by leaving them out (PARITY 6.4 gives the reason for each).
	for _, field := range []string{`"top_logprobs":2`, `"store":true`, `"metadata":{"a":"b"}`, `"user":"u"`,
		`"parallel_tool_calls":false`, `"stream_options":{"include_usage":true}`, `"reasoning_effort":"high"`, `"reasoning":{"effort":"low"}`,
		`"prompt_cache_key":"k"`, `"service_tier":"auto"`, `"modalities":["text"]`} {
		t.Run("not carried/"+field, func(t *testing.T) {
			result := mustConvert(t, `{`+user+`,`+field+`}`)
			for _, key := range []string{"response_format", "think", "format", "keep_alive", "tool_choice"} {
				if _, present := result[key]; present {
					t.Errorf("%s was sent: %v", key, result)
				}
			}
			if _, present := result["options"]; present {
				t.Errorf("options set by %s: %v", field, result["options"])
			}
		})
	}
	for name, body := range map[string]string{
		"n>1":                  `{` + user + `,"n":2}`,
		"logprobs":             `{` + user + `,"logprobs":true}`,
		"audio output":         `{` + user + `,"modalities":["text","audio"]}`,
		"audio":                `{` + user + `,"audio":{"voice":"x"}}`,
		"functions":            `{` + user + `,"functions":[{"name":"f"}]}`,
		"function_call":        `{` + user + `,"function_call":"auto"}`,
		"input_audio part":     `{"messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"x"}}]}]}`,
		"file part":            `{"messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"f"}}]}]}`,
		"unknown part":         `{"messages":[{"role":"user","content":[{"type":"hologram"}]}]}`,
		"remote image":         `{"messages":[{"role":"user","content":[{"type":"text","text":"t"},{"type":"image_url","image_url":{"url":"https://x/y.png"}}]}]}`,
		"image without url":    `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{}}]}]}`,
		"unknown role":         `{"messages":[{"role":"critic","content":"x"}]}`,
		"non-function call":    `{"messages":[{"role":"assistant","content":"","tool_calls":[{"id":"c","type":"custom","function":{"name":"f"}}]}]}`,
		"image in tool turn":   `{"messages":[{"role":"assistant","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,QQ=="}}],"tool_calls":[{"id":"c","type":"function","function":{"name":"f"}}]}]}`,
		"nothing survives":     `{"messages":[{"role":"user","content":""}]}`,
		"logit_bias":           `{` + user + `,"logit_bias":{"50256":-100}}`,
		"logit_bias list":      `{` + user + `,"logit_bias":[1,2]}`,
		"logit_bias string":    `{` + user + `,"logit_bias":"nope"}`,
		"logit_bias number":    `{` + user + `,"logit_bias":3}`,
		"tool_choice required": `{` + user + `,"tool_choice":"required"}`,
		"tool_choice named":    `{` + user + `,"tool_choice":{"type":"function","function":{"name":"f"}}}`,
		"unknown format":       `{` + user + `,"response_format":{"type":"xml"}}`,
		"schema missing":       `{` + user + `,"response_format":{"type":"json_schema"}}`,
		"stop of numbers":      `{` + user + `,"stop":[1]}`,
		"n":                    `{` + user + `,"n":2}`,
	} {
		t.Run("refused/"+name, func(t *testing.T) {
			if _, err := convert(t, body); err == nil || !strings.HasPrefix(err.Error(), "unsupported") {
				t.Fatalf("want a refusal, got %v", err)
			}
		})
	}
}

func lookup(value map[string]any, path string) any {
	var current any = value
	for _, step := range strings.Split(path, ".") {
		switch node := current.(type) {
		case map[string]any:
			current = node[step]
		case []any:
			var index int
			for _, digit := range step {
				index = index*10 + int(digit-'0')
			}
			if index >= len(node) {
				return nil
			}
			current = node[index]
		}
	}
	return current
}

func TestMessagesAreNormalisedLikeThePin(t *testing.T) {
	result := mustConvert(t, `{"model":"x","messages":[
		{"role":"system","content":"sys"},
		{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}}]},
		{"role":"assistant","content":"","tool_calls":[
			{"id":"c1","type":"function","function":{"name":"one","arguments":"{\"a\":1}"}},
			{"id":"c2","type":"function","function":{"name":"two","arguments":"not json"}},
			{"id":"c3","type":"function","function":{"name":"three","arguments":{"b":2}}}]},
		{"role":"tool","tool_call_id":"c1","content":"r1"},
		{"role":"tool","tool_call_id":"zz","name":"named","content":"r2"},
		{"role":"tool","tool_call_id":"zz2","content":"r3"},
		{"role":"tool","tool_call_id":"c2","content":""},
		{"role":"user","content":[{"type":"image_url","image_url":"data:image/png;base64,UVJT"}]},
		{"role":"assistant","content":null}
	]}`)
	want := `[
		{"role":"system","content":"sys"},
		{"role":"user","content":"look","images":["QUJD"]},
		{"role":"assistant","content":"","tool_calls":[
			{"type":"function","function":{"index":0,"name":"one","arguments":{"a":1}}},
			{"type":"function","function":{"index":1,"name":"two","arguments":{}}},
			{"type":"function","function":{"index":2,"name":"three","arguments":{"b":2}}}]},
		{"role":"tool","tool_name":"one","content":"r1"},
		{"role":"tool","tool_name":"named","content":"r2"},
		{"role":"tool","tool_name":"unknown_tool","content":"r3"},
		{"role":"assistant","content":""}
	]`
	var expected []any
	if err := json.Unmarshal([]byte(want), &expected); err != nil {
		t.Fatal(err)
	}
	var got []any
	raw, _ := json.Marshal(result["messages"])
	_ = json.Unmarshal(raw, &got)
	if !reflect.DeepEqual(got, expected) {
		gotJSON, _ := json.MarshalIndent(got, "", " ")
		t.Fatalf("messages:\n%s", gotJSON)
	}
	if result["stream"] != true || result["model"] != "m" {
		t.Errorf("model/stream: %v", result)
	}
}

// A client's own tool call id is repaired, and a call with none gets one the
// result can name; a Chat body from another format is not repaired again.
func TestToolIDsAreRepairedOnlyOnTheClientsOwnBody(t *testing.T) {
	const chat = `{"messages":[{"role":"user","content":"q"},{"role":"assistant","content":"","tool_calls":[{"type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","content":"done"}]}`
	own := mustConvert(t, chat)
	if name := lookup(own, "messages.2.tool_name"); name != "f" {
		t.Errorf("a result with no id must answer the call: %v", own["messages"])
	}
	body, _ := concerns.WireObject([]byte(chat))
	out, err := ChatRequestToOllama(body, "m", false, false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), `"tool_name":"f"`) {
		t.Errorf("a derived body must not be repaired again: %s", out)
	}
}
