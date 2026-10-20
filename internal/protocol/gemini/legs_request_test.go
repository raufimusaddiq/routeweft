package gemini

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// at reads a dotted path out of decoded JSON; a numeric segment indexes a list.
func at(value any, path string) any {
	for _, segment := range strings.Split(path, ".") {
		switch v := value.(type) {
		case map[string]any:
			value = v[segment]
		case []any:
			index, err := strconv.Atoi(segment)
			if err != nil || index >= len(v) {
				return nil
			}
			value = v[index]
		default:
			return nil
		}
	}
	return value
}

func asJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

func convertWith(t *testing.T, chat string, options RequestOptions) (map[string]any, *concerns.ToolNames, error) {
	t.Helper()
	body, err := concerns.WireObject([]byte(chat))
	if err != nil {
		t.Fatalf("%s: %v", chat, err)
	}
	if options.Model == "" {
		options.Model = "gemini-2.5-flash"
	}
	out, names, err := ChatRequestToGemini(body, options)
	if err != nil {
		return nil, nil, err
	}
	result, err := concerns.WireObject(out)
	if err != nil {
		t.Fatal(err)
	}
	return result, names, nil
}

func convert(t *testing.T, chat string) (map[string]any, error) {
	t.Helper()
	result, _, err := convertWith(t, chat, RequestOptions{Repair: true})
	return result, err
}

func mustConvert(t *testing.T, chat string) map[string]any {
	t.Helper()
	result, err := convert(t, chat)
	if err != nil {
		t.Fatalf("%s: %v", chat, err)
	}
	return result
}

const user = `"messages":[{"role":"user","content":"hi"}]`
const oneTool = `"tools":[{"type":"function","function":{"name":"f","parameters":{"type":"object","properties":{"a":{"type":"string"}}}}}]`

// Every Chat request field is carried, not carried for a stated reason (the
// request is still served), or refused. PARITY 6.6 is this table.
func TestChatRequestFieldsAreCarriedOrRefused(t *testing.T) {
	carried := map[string]struct{ body, path, want string }{
		"model":             {`{` + user + `}`, "model", `"gemini-2.5-flash"`},
		"temperature":       {`{` + user + `,"temperature":0.5}`, "generationConfig.temperature", `0.5`},
		"top_p":             {`{` + user + `,"top_p":0.9}`, "generationConfig.topP", `0.9`},
		"top_k":             {`{` + user + `,"top_k":40}`, "generationConfig.topK", `40`},
		"max_tokens":        {`{` + user + `,"max_tokens":10}`, "generationConfig.maxOutputTokens", `10`},
		"max_completion":    {`{` + user + `,"max_completion_tokens":11}`, "generationConfig.maxOutputTokens", `11`},
		"max_tokens wins":   {`{` + user + `,"max_tokens":10,"max_completion_tokens":11}`, "generationConfig.maxOutputTokens", `10`},
		"stop string":       {`{` + user + `,"stop":"###"}`, "generationConfig.stopSequences", `["###"]`},
		"stop list":         {`{` + user + `,"stop":["a","b","c","d","e"]}`, "generationConfig.stopSequences", `["a","b","c","d","e"]`},
		"seed":              {`{` + user + `,"seed":7}`, "generationConfig.seed", `7`},
		"presence_penalty":  {`{` + user + `,"presence_penalty":0.5}`, "generationConfig.presencePenalty", `0.5`},
		"frequency_penalty": {`{` + user + `,"frequency_penalty":0.25}`, "generationConfig.frequencyPenalty", `0.25`},
		"json_object":       {`{` + user + `,"response_format":{"type":"json_object"}}`, "generationConfig.responseMimeType", `"application/json"`},
		"json_schema":       {`{` + user + `,"response_format":{"type":"json_schema","json_schema":{"name":"n","schema":{"type":"object","properties":{"a":{"type":"string"}}}}}}`, "generationConfig.responseSchema.properties.a.type", `"string"`},
		"text format":       {`{` + user + `,"response_format":{"type":"text"}}`, "generationConfig.responseMimeType", `null`},
		"tools":             {`{` + user + `,` + oneTool + `}`, "tools.0.functionDeclarations.0.name", `"f"`},
		"tool_choice auto":  {`{` + user + `,` + oneTool + `,"tool_choice":"auto"}`, "toolConfig", `null`},
		"tool_choice none":  {`{` + user + `,` + oneTool + `,"tool_choice":"none"}`, "toolConfig.functionCallingConfig.mode", `"NONE"`},
		"tool_choice req":   {`{` + user + `,` + oneTool + `,"tool_choice":"required"}`, "toolConfig.functionCallingConfig.mode", `"ANY"`},
		"tool_choice named": {`{` + user + `,` + oneTool + `,"tool_choice":{"type":"function","function":{"name":"f"}}}`, "toolConfig.functionCallingConfig.allowedFunctionNames", `["f"]`},
		"parallel true":     {`{` + user + `,"parallel_tool_calls":true}`, "model", `"gemini-2.5-flash"`},
		"safety settings":   {`{` + user + `}`, "safetySettings.4.threshold", `"OFF"`},
		// Not carried, and the request is still served: none changes what is asked.
		"stream_options": {`{` + user + `,"stream_options":{"include_usage":true}}`, "model", `"gemini-2.5-flash"`},
		"store":          {`{` + user + `,"store":true}`, "model", `"gemini-2.5-flash"`},
		"metadata":       {`{` + user + `,"metadata":{"a":"b"}}`, "model", `"gemini-2.5-flash"`},
		"user":           {`{` + user + `,"user":"u"}`, "model", `"gemini-2.5-flash"`},
		"service_tier":   {`{` + user + `,"service_tier":"auto"}`, "model", `"gemini-2.5-flash"`},
		// Deferred to #125, not inexpressible: thinkingConfig carries them once the
		// model capability table exists.
		"reasoning_effort": {`{` + user + `,"reasoning_effort":"high"}`, "generationConfig.thinkingConfig", `null`},
		"reasoning":        {`{` + user + `,"reasoning":{"effort":"high"}}`, "generationConfig.thinkingConfig", `null`},
	}
	for name, c := range carried {
		t.Run("carried/"+name, func(t *testing.T) {
			result := mustConvert(t, c.body)
			if got := asJSON(at(result, c.path)); got != c.want {
				t.Fatalf("%s = %s, want %s\n%s", c.path, got, c.want, asJSON(result))
			}
		})
	}
	refused := map[string]string{
		"n 2":                  `{` + user + `,"n":2}`,
		"logprobs":             `{` + user + `,"logprobs":true}`,
		"audio modality":       `{` + user + `,"modalities":["text","audio"]}`,
		"audio":                `{` + user + `,"audio":{"voice":"x"}}`,
		"functions":            `{` + user + `,"functions":[{"name":"f"}]}`,
		"function_call":        `{` + user + `,"function_call":"auto"}`,
		"parallel false":       `{` + user + `,"parallel_tool_calls":false}`,
		"web_search_options":   `{` + user + `,"web_search_options":{"search_context_size":"low"}}`,
		"prediction":           `{` + user + `,"prediction":{"type":"content","content":"x"}}`,
		"stop 6":               `{` + user + `,"stop":["a","b","c","d","e","f"]}`,
		"stop number":          `{` + user + `,"stop":5}`,
		"stop mixed list":      `{` + user + `,"stop":["a",1]}`,
		"stop empty member":    `{` + user + `,"stop":["a",""]}`,
		"format unknown":       `{` + user + `,"response_format":{"type":"xml"}}`,
		"format string":        `{` + user + `,"response_format":"json"}`,
		"json_schema none":     `{` + user + `,"response_format":{"type":"json_schema","json_schema":{"name":"n"}}}`,
		"json_schema empty":    `{` + user + `,"response_format":{"type":"json_schema","json_schema":{"name":"n","schema":{"type":"object"}}}}`,
		"tools not a list":     `{` + user + `,"tools":"f"}`,
		"tool built-in":        `{` + user + `,"tools":[{"type":"web_search"}]}`,
		"tool not an object":   `{` + user + `,"tools":["f"]}`,
		"tool no name":         `{` + user + `,"tools":[{"type":"function","function":{"description":"d"}}]}`,
		"tool declared twice":  `{` + user + `,"tools":[{"type":"function","function":{"name":"f"}},{"type":"function","function":{"name":"f"}}]}`,
		"choice unknown":       `{` + user + `,` + oneTool + `,"tool_choice":"sometimes"}`,
		"choice undeclared":    `{` + user + `,` + oneTool + `,"tool_choice":{"type":"function","function":{"name":"g"}}}`,
		"choice bad type":      `{` + user + `,` + oneTool + `,"tool_choice":{"type":"allowed_tools"}}`,
		"choice number":        `{` + user + `,` + oneTool + `,"tool_choice":3}`,
		"choice unknown/empty": `{` + user + `,"tool_choice":"sometimes"}`,
		"max_tokens string":    `{` + user + `,"max_tokens":"10"}`,
		"max_tokens zero":      `{` + user + `,"max_tokens":0}`,
		"max_tokens negative":  `{` + user + `,"max_tokens":-1}`,
		"temperature string":   `{` + user + `,"temperature":"hot"}`,
		"seed string":          `{` + user + `,"seed":"x"}`,
		"no messages":          `{}`,
		"empty messages":       `{"messages":[]}`,
		"messages object":      `{"messages":{}}`,
		"message not object":   `{"messages":["hi"]}`,
		"role function":        `{"messages":[{"role":"function","content":"x"}]}`,
		"role unknown":         `{"messages":[{"role":"critic","content":"x"}]}`,
		"user remote image":    `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://x/y.png"}}]}]}`,
		"user http image":      `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":"http://x/y.png"}]}]}`,
		"user odd image":       `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"ftp://x"}}]}]}`,
		"user audio no data":   `{"messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"format":"wav"}}]}]}`,
		"user file id":         `{"messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"f"}}]}]}`,
		"user unknown part":    `{"messages":[{"role":"user","content":[{"type":"video","video":{}}]}]}`,
		"user content number":  `{"messages":[{"role":"user","content":5}]}`,
		"user content object":  `{"messages":[{"role":"user","content":{"type":"text","text":"x"}}]}`,
		"system image":         `{"messages":[{"role":"system","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}}]},{"role":"user","content":"x"}]}`,
		"assistant image":      `{"messages":[{"role":"user","content":"x"},{"role":"assistant","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}}]}]}`,
		"assistant number":     `{"messages":[{"role":"user","content":"x"},{"role":"assistant","content":5}]}`,
		"call not function":    `{"messages":[{"role":"user","content":"x"},{"role":"assistant","tool_calls":[{"id":"c","type":"custom","function":{"name":"f"}}]}]}`,
		"call no name":         `{"messages":[{"role":"user","content":"x"},{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"arguments":"{}"}}]}]}`,
		"call not object":      `{"messages":[{"role":"user","content":"x"},{"role":"assistant","tool_calls":["c"]}]}`,
		"tool result image":    `{"messages":[{"role":"user","content":"x"},{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"f"}}]},{"role":"tool","tool_call_id":"c","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AA"}}]}]}`,
		"tool result number":   `{"messages":[{"role":"user","content":"x"},{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"f"}}]},{"role":"tool","tool_call_id":"c","content":5}]}`,
		"orphan tool message":  `{"messages":[{"role":"user","content":"x"},{"role":"tool","tool_call_id":"nobody","content":"r"}]}`,
	}
	for name, body := range refused {
		t.Run("refused/"+name, func(t *testing.T) {
			result, err := convert(t, body)
			if err == nil {
				t.Fatalf("served instead of refused: %s", asJSON(result))
			}
			if !strings.HasPrefix(err.Error(), "unsupported: ") {
				t.Fatalf("not a translation error: %v", err)
			}
		})
	}
}

// Every shape of a field that asks for nothing is accepted, and every shape that
// asks for something is refused: the guard is on the value, not on its type. PR 2
// lost three rounds to a length test written for objects that a non-empty array
// walked past.
func TestPresenceGuardsCoverEveryShape(t *testing.T) {
	asks := []string{`true`, `1`, `0`, `-1`, `"x"`, `{"1":2}`, `[1]`, `["x"]`, `[{}]`}
	asksNothing := []string{`null`, `false`, `""`, `{}`, `[]`}
	for _, field := range []string{"logit_bias", "top_logprobs", "web_search_options", "prediction"} {
		for _, shape := range asks {
			if _, err := convert(t, fmt.Sprintf(`{%s,%q:%s}`, user, field, shape)); err == nil {
				t.Errorf("%s=%s was served", field, shape)
			}
		}
		for _, shape := range asksNothing {
			if _, err := convert(t, fmt.Sprintf(`{%s,%q:%s}`, user, field, shape)); err != nil {
				t.Errorf("%s=%s asks for nothing and was refused: %v", field, shape, err)
			}
		}
	}
	for _, shape := range []string{`""`, `[]`, `null`} {
		result := mustConvert(t, fmt.Sprintf(`{%s,"stop":%s}`, user, shape))
		if at(result, "generationConfig.stopSequences") != nil {
			t.Errorf("stop=%s sent an empty list", shape)
		}
	}
	for _, shape := range []string{`null`, `[]`} {
		result := mustConvert(t, fmt.Sprintf(`{%s,"tools":%s}`, user, shape))
		if result["tools"] != nil || result["toolConfig"] != nil {
			t.Errorf("tools=%s produced tools %s", shape, asJSON(result))
		}
	}
}

// tool_choice crossed with every way tools can be declared. The choice is always
// checked; with no tools to choose among it is not forwarded (a toolConfig with
// no declaration is a 400), and it never names a tool that is not sent.
func TestToolChoiceAcrossToolShapes(t *testing.T) {
	toolSets := map[string]string{"absent": ``, "empty": `,"tools":[]`, "one": `,` + oneTool}
	choices := []struct {
		choice string
		mode   string // "" = no toolConfig
		bad    bool
	}{
		{choice: ``},
		{choice: `,"tool_choice":null`},
		{choice: `,"tool_choice":"auto"`},
		{choice: `,"tool_choice":"none"`, mode: "NONE"},
		{choice: `,"tool_choice":"required"`, mode: "ANY"},
		{choice: `,"tool_choice":{"type":"function","function":{"name":"f"}}`, mode: "ANY"},
		{choice: `,"tool_choice":{"type":"function","function":{"name":"g"}}`, mode: "ANY", bad: true},
		{choice: `,"tool_choice":{"type":"function","function":{}}`, bad: true},
		{choice: `,"tool_choice":{"type":"allowed_tools"}`, bad: true},
		{choice: `,"tool_choice":"bogus"`, bad: true},
		{choice: `,"tool_choice":[]`, bad: true},
	}
	for setName, set := range toolSets {
		for _, c := range choices {
			t.Run(setName+c.choice, func(t *testing.T) {
				result, err := convert(t, `{`+user+set+c.choice+`}`)
				// A named tool that is not declared is only an error when there are
				// tools it could have been; the shape errors are errors everywhere.
				wantErr := c.bad && (setName == "one" || !strings.Contains(c.choice, `"name":"g"`))
				if (err != nil) != wantErr {
					t.Fatalf("err = %v, want error %v", err, wantErr)
				}
				if err != nil {
					return
				}
				want := ""
				if setName == "one" {
					want = c.mode
				}
				got, _ := at(result, "toolConfig.functionCallingConfig.mode").(string)
				if got != want {
					t.Fatalf("mode = %q, want %q: %s", got, want, asJSON(result))
				}
			})
		}
	}
}

// Gemini 4 rejects the sampling controls (the pin's comment, openai-to-gemini.js:85;
// not verified here). The request is served without them, and this test is the
// visible record that they are omitted: PARITY 6.6 G6.
func TestGeminiFourOmitsSamplingVisibly(t *testing.T) {
	for _, model := range []string{"gemini-4-pro", "models/gemini-4.1-flash", "google/gemini-4-flash"} {
		result, _, err := convertWith(t, `{`+user+`,"temperature":0.4,"top_p":0.8,"top_k":20,"max_tokens":9}`, RequestOptions{Model: model, Repair: true})
		if err != nil {
			t.Fatal(err)
		}
		config := concerns.Object(result["generationConfig"])
		for _, name := range []string{"temperature", "topP", "topK"} {
			if _, present := config[name]; present {
				t.Errorf("%s: %s sent", model, name)
			}
		}
		if config["maxOutputTokens"] == nil {
			t.Errorf("%s: maxOutputTokens must still be sent", model)
		}
	}
	for _, model := range []string{"gemini-2.5-pro", "gemini-3-flash-preview", "gemini-40", "my-gemini-4-clone"} {
		result, _, _ := convertWith(t, `{`+user+`,"temperature":0.4}`, RequestOptions{Model: model, Repair: true})
		if at(result, "generationConfig.temperature") == nil {
			t.Errorf("%s: temperature dropped on a model that takes it", model)
		}
	}
	if _, err := convert(t, `{`+user+`,"temperature":"hot"}`); err == nil {
		t.Error("a malformed temperature on Gemini 4 must still be refused, not silently ignored")
	}
}

// Two tool names that are the same name once Gemini's rule is applied are
// refused, not deduplicated and not suffixed: a renamed tool the model cannot be
// called back by is a broken answer.
func TestToolNameCollisionsAreRefused(t *testing.T) {
	declare := func(names ...string) string {
		var tools []string
		for _, name := range names {
			tools = append(tools, fmt.Sprintf(`{"type":"function","function":{"name":%q}}`, name))
		}
		return `{` + user + `,"tools":[` + strings.Join(tools, ",") + `]}`
	}
	long := strings.Repeat("a", 70)
	collide := [][]string{
		{"a b", "a_b"},     // a rewritten name meets a valid declared one
		{"a b", "a/b"},     // two rewrites meet
		{"1x", "_1x"},      // a leading digit is prefixed
		{long + "x", long}, // truncation to 64
		{"mcp:srv tool", "mcp:srv_tool"},
	}
	for _, names := range collide {
		if result, err := convert(t, declare(names...)); err == nil {
			t.Errorf("%q served: %s", names, asJSON(result))
		}
	}
	// A name used in history or tool_choice but not declared takes part too.
	history := `{"messages":[{"role":"user","content":"x"},{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"a b"}}]},{"role":"tool","tool_call_id":"c","content":"r"}],"tools":[{"type":"function","function":{"name":"a_b"}}]}`
	if _, err := convert(t, history); err == nil {
		t.Error("a history call that collides with a declared tool was served")
	}
	// Names that need no rewriting, and rewrites that meet no one, are served and
	// the rewrite is reversible.
	result, names, err := convertWith(t, declare("a.b", "plain", "x:y-z", "1st"), RequestOptions{Repair: true})
	if err != nil {
		t.Fatal(err)
	}
	got := asJSON(at(result, "tools.0.functionDeclarations"))
	for _, want := range []string{`"a.b"`, `"plain"`, `"x:y-z"`, `"_1st"`} {
		if !strings.Contains(got, want) {
			t.Errorf("declarations lack %s: %s", want, got)
		}
	}
	if names.Original("_1st") != "1st" || names.Original("plain") != "plain" {
		t.Error("the rewrite is not reversible")
	}
	// The same name twice in history is one name, not a collision.
	twice := `{"messages":[{"role":"user","content":"x"},{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"a b"}},{"id":"c2","type":"function","function":{"name":"a b"}}]}],"tools":[{"type":"function","function":{"name":"a b"}}]}`
	if _, err := convert(t, twice); err != nil {
		t.Errorf("one name used twice was refused: %v", err)
	}
}

// A parameter that happens to be called like a JSON Schema keyword is a
// parameter. The pin deletes it by name at every depth.
func TestSchemaKeywordsAreNotParameterNames(t *testing.T) {
	schema := `{"type":"object","properties":{
		"title":{"type":"string","minLength":1,"format":"x"},
		"format":{"type":"string"},
		"default":{"type":"number","default":3},
		"const":{"type":"string"},
		"x-trace":{"type":"string"},
		"$ref":{"type":"string"}},
		"required":["title","format","gone"],"additionalProperties":false,"$schema":"s","title":"T"}`
	cleaned, err := CleanSchema(mustDecode(t, schema))
	if err != nil {
		t.Fatal(err)
	}
	properties := concerns.Object(cleaned["properties"])
	for _, name := range []string{"title", "format", "default", "const", "x-trace", "$ref"} {
		if properties[name] == nil {
			t.Errorf("parameter %q was deleted", name)
		}
	}
	for _, gone := range []string{"minLength", "format", "default"} {
		if inner := concerns.Object(properties["title"]); inner[gone] != nil && gone != "format" {
			t.Errorf("keyword %s not removed from title", gone)
		}
	}
	if inner := concerns.Object(properties["title"]); inner["minLength"] != nil || inner["format"] != nil {
		t.Errorf("title keywords remain: %s", asJSON(inner))
	}
	if cleaned["additionalProperties"] != nil || cleaned["$schema"] != nil || cleaned["title"] != nil {
		t.Errorf("schema keywords remain: %s", asJSON(cleaned))
	}
	if got := asJSON(cleaned["required"]); got != `["title","format"]` {
		t.Errorf("required = %s", got)
	}
}

func mustDecode(t *testing.T, text string) any {
	t.Helper()
	value, err := concerns.WireObject([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func TestSchemaNarrowing(t *testing.T) {
	cases := map[string]struct{ in, path, want string }{
		"const to enum":      {`{"type":"object","properties":{"a":{"const":"x"}}}`, "properties.a.enum", `["x"]`},
		"enum to strings":    {`{"type":"object","properties":{"a":{"enum":[1,true,null,"s"]}}}`, "properties.a.enum", `["1","true","null","s"]`},
		"enum gets type":     {`{"type":"object","properties":{"a":{"enum":[1]}}}`, "properties.a.type", `"string"`},
		"anyOf best":         {`{"type":"object","properties":{"a":{"anyOf":[{"type":"null"},{"type":"string"},{"type":"object","properties":{"z":{"type":"string"}}}]}}}`, "properties.a.type", `"object"`},
		"nested anyOf":       {`{"type":"object","properties":{"a":{"anyOf":[{"anyOf":[{"type":"integer"}]}]}}}`, "properties.a.type", `"integer"`},
		"oneOf":              {`{"type":"object","properties":{"a":{"oneOf":[{"type":"string"},{"type":"null"}]}}}`, "properties.a.type", `"string"`},
		"type list":          {`{"type":"object","properties":{"a":{"type":["null","integer"]}}}`, "properties.a.type", `"integer"`},
		"type list all null": {`{"type":"object","properties":{"a":{"type":["null"]}}}`, "properties.a.type", `"string"`},
		"allOf":              {`{"type":"object","properties":{"a":{"type":"string"}},"allOf":[{"properties":{"b":{"type":"string"}},"required":["b"]}]}`, "required", `["b"]`},
		"array items":        {`{"type":"object","properties":{"a":{"type":"array"}}}`, "properties.a.items.type", `"string"`},
		"tuple":              {`{"type":"object","properties":{"a":{"type":"array","prefixItems":[{"type":"integer"}]}}}`, "properties.a.items.type", `"integer"`},
		"missing type":       {`{"properties":{"a":{"type":"string"}}}`, "type", `"object"`},
		"inlined ref":        {`{"type":"object","properties":{"a":{"$ref":"#/$defs/A"}},"$defs":{"A":{"type":"string","description":"d"}}}`, "properties.a.description", `"d"`},
		"nested ref":         {`{"type":"object","properties":{"a":{"$ref":"#/$defs/A"}},"$defs":{"A":{"type":"object","properties":{"b":{"$ref":"#/$defs/B"}}},"B":{"type":"integer"}}}`, "properties.a.properties.b.type", `"integer"`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			cleaned, err := CleanSchema(mustDecode(t, c.in))
			if err != nil {
				t.Fatal(err)
			}
			if got := asJSON(at(cleaned, c.path)); got != c.want {
				t.Fatalf("%s = %s, want %s: %s", c.path, got, c.want, asJSON(cleaned))
			}
		})
	}
	for name, in := range map[string]string{
		"recursive ref": `{"type":"object","properties":{"a":{"$ref":"#/$defs/A"}},"$defs":{"A":{"type":"object","properties":{"a":{"$ref":"#/$defs/A"}}}}}`,
		"remote ref":    `{"type":"object","properties":{"a":{"$ref":"https://x/y.json"}}}`,
		"dangling ref":  `{"type":"object","properties":{"a":{"$ref":"#/$defs/Missing"}}}`,
		"ref to scalar": `{"type":"object","properties":{"a":{"$ref":"#/type"}}}`,
	} {
		if _, err := CleanSchema(mustDecode(t, in)); err == nil {
			t.Errorf("%s was served", name)
		}
	}
	// A tool that declares nothing is sent with no parameters, and no parameter
	// the client never declared is invented (PARITY 6.6 G5).
	for _, schema := range []string{`{}`, `{"type":"object"}`, `{"type":"object","properties":{}}`, `null`} {
		result := mustConvert(t, `{`+user+`,"tools":[{"type":"function","function":{"name":"f","parameters":`+schema+`}}]}`)
		declaration := concerns.Object(at(result, "tools.0.functionDeclarations.0"))
		if declaration["parameters"] != nil {
			t.Errorf("schema %s: parameters sent: %s", schema, asJSON(declaration))
		}
	}
	if result := mustConvert(t, `{`+user+`,"tools":[{"type":"function","function":{"name":"f"}}]}`); strings.Contains(asJSON(result), "reason") {
		t.Errorf("a placeholder parameter was invented: %s", asJSON(result))
	}
}

func TestConversationShape(t *testing.T) {
	t.Run("system messages are all kept", func(t *testing.T) {
		result := mustConvert(t, `{"messages":[{"role":"system","content":"one"},{"role":"developer","content":"two"},{"role":"user","content":"hi"},{"role":"system","content":[{"type":"text","text":"three"}]}]}`)
		if got := at(result, "systemInstruction.parts.0.text"); got != "one\n\ntwo\n\nthree" {
			t.Fatalf("systemInstruction = %v", got)
		}
		if at(result, "systemInstruction.role") != "user" {
			t.Error("systemInstruction role")
		}
	})
	t.Run("a lone system message is a user turn", func(t *testing.T) {
		result := mustConvert(t, `{"messages":[{"role":"system","content":"only"}]}`)
		if asJSON(at(result, "contents")) != `[{"parts":[{"text":"only"}],"role":"user"}]` || result["systemInstruction"] != nil {
			t.Fatal(asJSON(result))
		}
	})
	t.Run("images and audio", func(t *testing.T) {
		result := mustConvert(t, `{"messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,QUJD"}},{"type":"input_audio","input_audio":{"data":"AAA","format":"mp3"}},{"type":"file","file":{"file_data":"data:application/pdf;base64,JVBER"}}]}]}`)
		want := `[{"text":"look"},{"inlineData":{"data":"QUJD","mimeType":"image/png"}},{"inlineData":{"data":"AAA","mimeType":"audio/mpeg"}},{"inlineData":{"data":"JVBER","mimeType":"application/pdf"}}]`
		if got := asJSON(at(result, "contents.0.parts")); got != want {
			t.Fatalf("parts = %s", got)
		}
	})
	t.Run("the history is closed on both ends", func(t *testing.T) {
		start := mustConvert(t, `{"messages":[{"role":"assistant","content":"earlier"},{"role":"user","content":"now"}]}`)
		if at(start, "contents.0.parts.0.text") != "..." || at(start, "contents.1.role") != "model" {
			t.Errorf("leading model turn: %s", asJSON(at(start, "contents")))
		}
		end := mustConvert(t, `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"prefill"}]}`)
		if at(end, "contents.2.parts.0.text") != "Continue." || at(end, "contents.2.role") != "user" {
			t.Errorf("trailing model turn: %s", asJSON(at(end, "contents")))
		}
	})
	t.Run("a trailing call gets a Continue reply", func(t *testing.T) {
		result := mustConvert(t, `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"f","arguments":"{}"}}]}]}`)
		if got := asJSON(at(result, "contents.2.parts.0.functionResponse")); got != `{"id":"c1","name":"f","response":{"result":"Continue."}}` {
			t.Fatalf("closing reply = %s", got)
		}
	})
	t.Run("a call with no result mid-conversation gets an empty one", func(t *testing.T) {
		result := mustConvert(t, `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"f"}},{"id":"c2","type":"function","function":{"name":"g"}}]},{"role":"tool","tool_call_id":"c2","content":"two"},{"role":"user","content":"next"}]}`)
		replies := asJSON(at(result, "contents.2.parts"))
		if !strings.Contains(replies, `"id":"c1","name":"f","response":{"result":{"result":""}}`) || !strings.Contains(replies, `"id":"c2","name":"g","response":{"result":{"result":"two"}}`) {
			t.Fatalf("replies = %s", replies)
		}
		if at(result, "contents.2.parts.2.text") != "next" {
			t.Errorf("the user turn after the replies must merge into them: %s", asJSON(at(result, "contents")))
		}
	})
	t.Run("tool results are wrapped as the pin does", func(t *testing.T) {
		for content, want := range map[string]string{
			`{"city":"Oslo"}`:  `{"result":{"city":"Oslo"}}`,
			`42`:               `{"result":{"result":42}}`,
			`[1,2]`:            `{"result":{"result":[1,2]}}`,
			`plain text`:       `{"result":{"result":"plain text"}}`,
			`null`:             `{"result":{"result":"null"}}`,
			`{"a":1} trailing`: `{"result":{"result":"{\"a\":1} trailing"}}`,
			``:                 `{"result":{"result":""}}`,
		} {
			encoded, _ := json.Marshal(content)
			result := mustConvert(t, `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"f"}}]},{"role":"tool","tool_call_id":"c","content":`+string(encoded)+`}]}`)
			if got := asJSON(at(result, "contents.2.parts.0.functionResponse.response")); got != want {
				t.Errorf("result %q -> %s, want %s", content, got, want)
			}
		}
	})
	t.Run("a $ref key in a tool result is renamed", func(t *testing.T) {
		result := mustConvert(t, `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"f"}}]},{"role":"tool","tool_call_id":"c","content":"{\"$ref\":\"#/x\",\"n\":[{\"$ref\":1}]}"}]}`)
		got := asJSON(at(result, "contents.2.parts.0.functionResponse.response"))
		if strings.Contains(got, `"$ref"`) || !strings.Contains(got, `"_ref"`) {
			t.Fatalf("response = %s", got)
		}
	})
	t.Run("reasoning is replayed with a placeholder signature", func(t *testing.T) {
		result := mustConvert(t, `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","reasoning_content":"thinking","content":"answer"},{"role":"user","content":"more"}]}`)
		want := `[{"text":"thinking","thought":true},{"text":"","thoughtSignature":"skip_thought_signature_validator"},{"text":"answer"}]`
		if got := asJSON(at(result, "contents.1.parts")); got != want {
			t.Fatalf("parts = %s", got)
		}
	})
	t.Run("gemini 4 puts function responses last in a merged user turn", func(t *testing.T) {
		chat := `{"messages":[{"role":"user","content":"hi"},{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"f"}}]},{"role":"tool","tool_call_id":"c","content":"r"},{"role":"user","content":"and"}]}`
		result, _, err := convertWith(t, chat, RequestOptions{Model: "gemini-4-pro", Repair: true})
		if err != nil {
			t.Fatal(err)
		}
		if at(result, "contents.2.parts.0.text") != "and" || at(result, "contents.2.parts.1.functionResponse") == nil {
			t.Fatalf("parts = %s", asJSON(at(result, "contents.2.parts")))
		}
	})
}

// An OpenAI tool_call_id is unique only inside its own assistant turn, so one id
// can come back later with another result. Each call takes the oldest result for
// its id, every emitted functionCall id is unique, and a response names the id
// of the call it answers.
func TestRepeatedToolCallIDsAreAnsweredPerOccurrence(t *testing.T) {
	chat := `{"messages":[{"role":"user","content":"a"},
		{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":1}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"first"},
		{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":2}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"second"},
		{"role":"user","content":"done"}]}`
	result := mustConvert(t, chat)
	if got := at(result, "contents.1.parts.0.functionCall.id"); got != "call_1" {
		t.Errorf("first id = %v", got)
	}
	if got := at(result, "contents.3.parts.0.functionCall.id"); got != "call_1-2" {
		t.Errorf("second id = %v", got)
	}
	if got := asJSON(at(result, "contents.2.parts.0.functionResponse")); got != `{"id":"call_1","name":"lookup","response":{"result":{"result":"first"}}}` {
		t.Errorf("first reply = %s", got)
	}
	if got := asJSON(at(result, "contents.4.parts.0.functionResponse")); got != `{"id":"call_1-2","name":"lookup","response":{"result":{"result":"second"}}}` {
		t.Errorf("second reply = %s", got)
	}
	// Vertex rejects the ids; pairing then rests on order and name.
	vertex, _, err := convertWith(t, chat, RequestOptions{Model: "gemini-2.5-flash", Vertex: true, Repair: true})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(asJSON(vertex), `"id"`) {
		t.Errorf("Vertex body carries an id: %s", asJSON(vertex))
	}
	if !strings.Contains(asJSON(vertex), `"name":"lookup","response":{"result":{"result":"second"}}`) {
		t.Errorf("Vertex lost a reply: %s", asJSON(vertex))
	}
}

// The tool-call id resolver, crossed: the call's id as the client sent it, the
// result's id, and whether the body is the client's own (repaired here) or came
// from another leg (already repaired). Every cell either pairs the result with
// its call or is refused; none leaves a call and a result that name different
// ids.
func TestToolCallIDResolutionCrossProduct(t *testing.T) {
	callIDs := map[string]string{"valid": `"call_1"`, "dotted": `"call.1"`, "empty": `""`, "absent": ``, "numeric": `7`}
	resultIDs := map[string]string{"same": "same", "absent": ``, "empty": `""`, "mismatch": `"other"`}
	for callName, callID := range callIDs {
		for resultName, resultID := range resultIDs {
			for _, repair := range []bool{true, false} {
				name := fmt.Sprintf("call=%s/result=%s/repair=%v", callName, resultName, repair)
				t.Run(name, func(t *testing.T) {
					call := `{"type":"function","function":{"name":"f","arguments":"{}"}}`
					if callID != "" {
						call = `{"id":` + callID + `,` + call[1:]
					}
					tool := `{"role":"tool","content":"r"`
					switch resultName {
					case "same":
						if callID != "" {
							tool += `,"tool_call_id":` + callID
						}
					case "absent":
					default:
						tool += `,"tool_call_id":` + resultID
					}
					tool += `}`
					chat := `{"messages":[{"role":"user","content":"x"},{"role":"assistant","tool_calls":[` + call + `]},` + tool + `,{"role":"user","content":"y"}]}`
					result, _, err := convertWith(t, chat, RequestOptions{Repair: repair})
					callUsable := callName != "empty" && callName != "absent" && (callName != "numeric" || repair)
					if !repair && !callUsable {
						if err == nil {
							t.Fatalf("an id-less call was served unrepaired: %s", asJSON(result))
						}
						return
					}
					if err != nil {
						// Only a result that names something the call did not, or none
						// when repair is off, may be refused.
						mismatched := resultName == "mismatch" || resultName == "empty" && !repair || resultName == "absent" && !repair ||
							resultName == "same" && callID == "" && !repair
						if !mismatched {
							t.Fatalf("refused: %v", err)
						}
						return
					}
					callPart := concerns.Object(at(result, "contents.1.parts.0.functionCall"))
					replyPart := concerns.Object(at(result, "contents.2.parts.0.functionResponse"))
					if callPart == nil || replyPart == nil {
						t.Fatalf("call or reply missing: %s", asJSON(result))
					}
					if callPart["id"] == nil || callPart["id"] != replyPart["id"] {
						t.Fatalf("call id %v and reply id %v disagree", callPart["id"], replyPart["id"])
					}
					if got := asJSON(replyPart["response"]); got != `{"result":{"result":"r"}}` {
						t.Fatalf("the reply carries %s", got)
					}
				})
			}
		}
	}
}

func TestThoughtSignatureReplay(t *testing.T) {
	cache := NewSignatures(10, time.Hour)
	cache.Put("alice|conn", "call_1", "lookup", `{"q":1}`, "REAL-SIG")
	chat := `{"messages":[{"role":"user","content":"a"},
		{"role":"assistant","tool_calls":[
			{"id":"call_1","type":"function","function":{"name":"lookup","arguments":"{\"q\":1}"}},
			{"id":"call_2","type":"function","function":{"name":"lookup","arguments":"{\"q\":2}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"r1"},
		{"role":"tool","tool_call_id":"call_2","content":"r2"}]}`
	signatureOf := func(scope string) (first, second any) {
		result, _, err := convertWith(t, chat, RequestOptions{Repair: true, Signatures: cache, Scope: scope})
		if err != nil {
			t.Fatal(err)
		}
		return at(result, "contents.1.parts.0.thoughtSignature"), at(result, "contents.1.parts.1.thoughtSignature")
	}
	if first, second := signatureOf("alice|conn"); first != "REAL-SIG" || second != nil {
		t.Errorf("same scope: %v, %v", first, second)
	}
	// Another client, another connection, or no scope at all never sees it: the
	// first call is signed with the placeholder and nothing else is.
	for _, scope := range []string{"bob|conn", "alice|other", "", "alice"} {
		if first, second := signatureOf(scope); first != PlaceholderSignature || second != nil {
			t.Errorf("scope %q: %v, %v", scope, first, second)
		}
	}
	// The same id with other arguments is another call.
	cache.Put("alice|conn", "call_9", "lookup", `{"q":1}`, "OTHER")
	other := strings.Replace(chat, `call_1`, `call_9`, -1)
	result, _, _ := convertWith(t, other, RequestOptions{Repair: true, Signatures: cache, Scope: "alice|conn"})
	if got := at(result, "contents.1.parts.0.thoughtSignature"); got != "OTHER" {
		t.Errorf("a different id is another entry: %v", got)
	}
	if reflect.DeepEqual(nil, nil) && cache.Len() != 2 {
		t.Errorf("cache holds %d", cache.Len())
	}
}

func TestSignatureCacheIsBoundedAndScoped(t *testing.T) {
	now := time.Unix(1000, 0)
	cache := NewSignatures(3, time.Minute)
	cache.now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		cache.Put("s", fmt.Sprintf("id%d", i), "n", "{}", "sig")
	}
	if cache.Len() != 3 {
		t.Fatalf("a cap of 3 holds %d", cache.Len())
	}
	if _, ok := cache.Get("s", "id0", "n", "{}"); ok {
		t.Error("the oldest entry survived eviction")
	}
	if _, ok := cache.Get("s", "id4", "n", "{}"); !ok {
		t.Error("the newest entry is gone")
	}
	// A read refreshes: id2 survives the next insertion, id3 does not.
	cache.Get("s", "id2", "n", "{}")
	cache.Put("s", "id5", "n", "{}", "sig")
	if _, ok := cache.Get("s", "id2", "n", "{}"); !ok {
		t.Error("a recently read entry was evicted first")
	}
	if _, ok := cache.Get("s", "id3", "n", "{}"); ok {
		t.Error("the least recently used entry survived")
	}
	now = now.Add(2 * time.Minute)
	if _, ok := cache.Get("s", "id5", "n", "{}"); ok {
		t.Error("an expired entry was served")
	}
	// Length-prefixed keys: ("ab","c") is not ("a","bc").
	cache.Put("ab", "c", "n", "{}", "x")
	if _, ok := cache.Get("a", "bc", "n", "{}"); ok {
		t.Error("keys collide across field boundaries")
	}
	// Nothing is stored for no scope, an empty id, an empty or oversized signature.
	empty := NewSignatures(3, time.Minute)
	empty.Put("", "id", "n", "{}", "sig")
	empty.Put("s", "", "n", "{}", "sig")
	empty.Put("s", "id", "n", "{}", "")
	empty.Put("s", "id", "n", "{}", strings.Repeat("x", MaxSignatureBytes+1))
	if empty.Len() != 0 {
		t.Errorf("stored %d", empty.Len())
	}
	var nilCache *Signatures
	nilCache.Put("s", "id", "n", "{}", "sig")
	if _, ok := nilCache.Get("s", "id", "n", "{}"); ok {
		t.Error("a nil cache returned a hit")
	}
	if NewSignatures(0, time.Minute).Len() != 0 {
		t.Error("a zero cap")
	}
}

// A body that did not come from the client has been through the shared id repair
// already; one that still holds a call without a usable id is refused even when
// no result names it, because Gemini pairs by id and a result added later could
// not be matched.
func TestUnrepairedCallsNeedAnID(t *testing.T) {
	for name, call := range map[string]string{
		"absent":  `{"type":"function","function":{"name":"f"}}`,
		"empty":   `{"id":"","type":"function","function":{"name":"f"}}`,
		"numeric": `{"id":7,"type":"function","function":{"name":"f"}}`,
	} {
		chat := `{"messages":[{"role":"user","content":"x"},{"role":"assistant","tool_calls":[` + call + `]}]}`
		if result, _, err := convertWith(t, chat, RequestOptions{Repair: false}); err == nil {
			t.Errorf("%s: served: %s", name, asJSON(result))
		}
		result, _, err := convertWith(t, chat, RequestOptions{Repair: true})
		if err != nil {
			t.Errorf("%s repaired: %v", name, err)
		} else if id := at(result, "contents.1.parts.0.functionCall.id"); id == nil || id == "" {
			t.Errorf("%s repaired: no id minted: %s", name, asJSON(result))
		}
	}
}
