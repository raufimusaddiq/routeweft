package kiro

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

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

func fixedID() string { return "conversation-1" }

func convertWith(t *testing.T, chat string, options RequestOptions) (map[string]any, *concerns.ToolNames, RequestInfo, error) {
	t.Helper()
	body, err := concerns.WireObject([]byte(chat))
	if err != nil {
		t.Fatalf("%s: %v", chat, err)
	}
	if options.Model == "" {
		options.Model = "claude-sonnet-4.5"
	}
	options.NewConversationID = fixedID
	out, names, info, err := ChatRequestToKiro(body, options)
	if err != nil {
		return nil, nil, info, err
	}
	result, err := concerns.WireObject(out)
	if err != nil {
		t.Fatal(err)
	}
	return result, names, info, nil
}

func convert(t *testing.T, chat string) map[string]any {
	t.Helper()
	result, _, _, err := convertWith(t, chat, RequestOptions{})
	if err != nil {
		t.Fatalf("%s: %v", chat, err)
	}
	return result
}

func refused(t *testing.T, chat string) string {
	t.Helper()
	_, _, _, err := convertWith(t, chat, RequestOptions{})
	if err == nil {
		t.Fatalf("expected a refusal: %s", chat)
	}
	if !strings.HasPrefix(err.Error(), "unsupported: ") && !strings.Contains(err.Error(), "unsupported: ") {
		t.Fatalf("not an unsupported error: %v", err)
	}
	return err.Error()
}

const user = `{"role":"user","content":"hi"}`

func wrap(fields string) string {
	return fmt.Sprintf(`{"messages":[%s]%s}`, user, fields)
}

func TestConversationShape(t *testing.T) {
	result := convert(t, `{"messages":[
		{"role":"system","content":"be brief"},
		{"role":"user","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]},
		{"role":"assistant","content":"calling","tool_calls":[{"id":"call_1","type":"function","function":{"name":"get","arguments":"{\"q\":1}"}}]},
		{"role":"tool","tool_call_id":"call_1","content":"result"},
		{"role":"user","content":"thanks"}],
		"tools":[{"type":"function","function":{"name":"get","description":"d","parameters":{"type":"object","properties":{"q":{"type":"number"}}}}}]}`)
	state := at(result, "conversationState").(map[string]any)
	if state["chatTriggerType"] != "MANUAL" || state["conversationId"] != "conversation-1" {
		t.Fatalf("state %v", state)
	}
	history := state["history"].([]any)
	if len(history) != 2 {
		t.Fatalf("history %d", len(history))
	}
	if got := at(history[0], "userInputMessage.content"); got != "<instructions>\nbe brief\n</instructions>\n\na\nb" {
		t.Fatalf("first user %q", got)
	}
	if at(history[0], "userInputMessage.modelId") != "claude-sonnet-4.5" {
		t.Fatalf("modelId %v", history[0])
	}
	if at(history[1], "assistantResponseMessage.content") != "calling" ||
		at(history[1], "assistantResponseMessage.toolUses.0.toolUseId") != "call_1" ||
		at(history[1], "assistantResponseMessage.toolUses.0.input.q") != json.Number("1") {
		t.Fatalf("assistant %v", history[1])
	}
	current := at(state, "currentMessage.userInputMessage").(map[string]any)
	if current["content"] != "Tool results provided.\n\nthanks" && current["content"] != "thanks" {
		t.Fatalf("current %v", current)
	}
	if current["origin"] != "AI_EDITOR" {
		t.Fatalf("origin %v", current)
	}
	// Tools ride on the current message only; the results that answer the call ride on the turn that follows it.
	if at(current, "userInputMessageContext.tools.0.toolSpecification.name") != "get" {
		t.Fatalf("tools %v", current)
	}
}

// The tool result belongs to the user turn right after the call, with the text it came with.
func TestToolResultTurn(t *testing.T) {
	result := convert(t, `{"messages":[`+user+`,
		{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"get","arguments":""}},{"id":"b","type":"function","function":{"name":"get","arguments":"{}"}}]},
		{"role":"tool","tool_call_id":"a","content":[{"type":"text","text":"one"},{"type":"text","text":"two"}]},
		{"role":"tool","tool_call_id":"b","content":"x","is_error":true}],
		"tools":[{"type":"function","function":{"name":"get"}}]}`)
	current := at(result, "conversationState.currentMessage.userInputMessage").(map[string]any)
	if current["content"] != "Tool results provided." {
		t.Fatalf("placeholder %v", current["content"])
	}
	if at(current, "userInputMessageContext.toolResults.0.content.0.text") != "one\ntwo" ||
		at(current, "userInputMessageContext.toolResults.0.status") != "success" ||
		at(current, "userInputMessageContext.toolResults.1.status") != "error" {
		t.Fatalf("results %v", current)
	}
	assistant := at(result, "conversationState.history.1.assistantResponseMessage").(map[string]any)
	if assistant["content"] != "..." || len(assistant["toolUses"].([]any)) != 2 {
		t.Fatalf("assistant %v", assistant)
	}
	if at(assistant, "toolUses.0.input") == nil || len(at(assistant, "toolUses.0.input").(map[string]any)) != 0 {
		t.Fatalf("empty arguments must be {}: %v", assistant)
	}
}

// An explicit zero is a value; absent and null are not.
func TestSamplingZeroIsNotAbsent(t *testing.T) {
	cases := []struct {
		fields string
		config string
		want   map[string]string
	}{
		{``, "", nil},
		{`,"temperature":null,"top_p":null,"max_tokens":null`, "", nil},
		{`,"temperature":0`, "inferenceConfig", map[string]string{"temperature": "0"}},
		{`,"top_p":0`, "inferenceConfig", map[string]string{"topP": "0"}},
		{`,"temperature":0,"top_p":0.0`, "inferenceConfig", map[string]string{"temperature": "0", "topP": "0.0"}},
		{`,"temperature":0.7,"top_p":1,"max_tokens":99`, "inferenceConfig", map[string]string{"temperature": "0.7", "topP": "1", "maxTokens": "99"}},
		{`,"max_completion_tokens":7`, "inferenceConfig", map[string]string{"maxTokens": "7"}},
		{`,"max_tokens":5,"max_completion_tokens":7`, "inferenceConfig", map[string]string{"maxTokens": "5"}},
		{`,"max_tokens":null,"max_completion_tokens":7`, "inferenceConfig", map[string]string{"maxTokens": "7"}},
		{`,"presence_penalty":0,"frequency_penalty":0.0`, "", nil},
		{`,"n":1,"logprobs":false,"modalities":["text"],"response_format":{"type":"text"}`, "", nil},
	}
	for _, c := range cases {
		result := convert(t, wrap(c.fields))
		config, present := result["inferenceConfig"]
		if c.want == nil {
			if present {
				t.Errorf("%s: inferenceConfig %v", c.fields, config)
			}
			continue
		}
		got := config.(map[string]any)
		if len(got) != len(c.want) {
			t.Errorf("%s: %v", c.fields, got)
		}
		for key, want := range c.want {
			if fmt.Sprint(got[key]) != want {
				t.Errorf("%s: %s = %v, want %s", c.fields, key, got[key], want)
			}
		}
	}
	// The zero survives encoding: the body text says 0, not a missing key.
	body, _ := concerns.WireObject([]byte(wrap(`,"temperature":0,"top_p":0`)))
	out, _, _, err := ChatRequestToKiro(body, RequestOptions{Model: "m"})
	if err != nil || !strings.Contains(string(out), `"temperature":0`) || !strings.Contains(string(out), `"topP":0`) {
		t.Fatalf("%s %v", out, err)
	}
}

func TestRefusedFields(t *testing.T) {
	cases := []string{
		`,"max_tokens":0`, `,"max_tokens":-1`, `,"max_tokens":1.5`, `,"max_tokens":"9"`, `,"max_completion_tokens":0`,
		`,"temperature":"hot"`, `,"top_p":true`, `,"temperature":[]`,
		`,"stop":"x"`, `,"stop":[]`, `,"seed":0`, `,"seed":7`, `,"logit_bias":{}`, `,"top_logprobs":2`, `,"logprobs":true`,
		`,"n":2`, `,"n":0`, `,"presence_penalty":0.5`, `,"frequency_penalty":-1`, `,"presence_penalty":"a"`,
		`,"response_format":{"type":"json_object"}`, `,"response_format":"text"`,
		`,"tool_choice":"none"`, `,"tool_choice":"required"`, `,"tool_choice":{"type":"function","function":{"name":"x"}}`,
		`,"parallel_tool_calls":false`, `,"modalities":["audio"]`, `,"audio":{"voice":"a"}`, `,"functions":[{"name":"f"}]`,
		`,"function_call":"auto"`, `,"web_search_options":{}`, `,"prediction":{}`, `,"top_k":3`, `,"min_p":0.1`,
		`,"tools":{}`, `,"tools":[1]`, `,"tools":[{"type":"web_search"}]`, `,"tools":[{"type":"function"}]`,
		`,"tools":[{"type":"function","function":{}}]`, `,"tools":[{"type":"function","function":{"name":"  "}}]`,
		`,"tools":[{"type":"function","function":{"name":5}}]`,
	}
	for _, fields := range cases {
		refused(t, wrap(fields))
	}
}

func TestAdvisoryFieldsAreAccepted(t *testing.T) {
	convert(t, wrap(`,"stream":true,"stream_options":{"include_usage":true},"user":"u","store":false,"metadata":{"a":"b"},"service_tier":"auto","prompt_cache_key":"k","safety_identifier":"s","parallel_tool_calls":true,"tool_choice":"auto"`))
}

func TestMessageRefusals(t *testing.T) {
	msgs := []string{
		`[]`, `{}`, `null`, `[1]`, `[null]`, `[{}]`, `[{"role":"nobody","content":"x"}]`, `[{"role":null,"content":"x"}]`,
		`[{"role":"user","content":5}]`, `[{"role":"user","content":{"type":"text","text":"x"}}]`, `[{"role":"user","content":true}]`,
		`[{"role":"user","content":[5]}]`, `[{"role":"user","content":[{"type":"text","text":5}]}]`,
		`[{"role":"user","content":[{"type":"text"}]}]`, `[{"role":"user","content":[{"type":"input_audio","input_audio":{}}]}]`,
		`[{"role":"user","content":[{"type":"file","file":{}}]}]`, `[{"role":"user","content":[{"text":"no type"}]}]`,
		`[{"role":"user","content":"x","name":"bob"}]`, `[{"role":"user","content":"x","function_call":{}}]`, `[{"role":"user","content":"x","audio":{"id":"a"}}]`,
		`[{"role":"assistant","content":"x"}]`,
		`[{"role":"user","content":"x"},{"role":"assistant","content":"y"}]`,
		`[{"role":"assistant","content":"y"},{"role":"user","content":"x"}]`,
		`[{"role":"user","content":""}]`, `[{"role":"user","content":null}]`, `[{"role":"user","content":[]}]`, `[{"role":"user","content":"   "}]`,
		`[{"role":"user","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]`,
		`[{"role":"tool","tool_call_id":"a","content":"x"}]`,
		`[{"role":"user","content":"x"},{"role":"tool","tool_call_id":"a","content":"x"}]`,
		`[{"role":"user","content":"x"},{"role":"tool","content":"x"}]`,
		`[{"role":"user","content":"x"},{"role":"assistant","tool_calls":{}}]`,
		`[{"role":"assistant","content":[{"type":"image_url"}]},{"role":"user","content":"x"}]`,
	}
	for _, messages := range msgs {
		refused(t, `{"messages":`+messages+`}`)
	}
}

func TestToolHistoryIsRefusedNotRepaired(t *testing.T) {
	const tools = `,"tools":[{"type":"function","function":{"name":"get"}}]`
	call := func(id, name, args string) string {
		return fmt.Sprintf(`{"role":"assistant","tool_calls":[{"id":%q,"type":"function","function":{"name":%q,"arguments":%q}}]}`, id, name, args)
	}
	result := func(id string) string { return fmt.Sprintf(`{"role":"tool","tool_call_id":%q,"content":"r"}`, id) }
	cases := map[string]string{
		"unanswered call":         `[` + user + `,` + call("a", "get", "{}") + `,{"role":"user","content":"next"}]`,
		"orphan result":           `[` + user + `,{"role":"assistant","content":"x"},` + result("a") + `]`,
		"result for another call": `[` + user + `,` + call("a", "get", "{}") + `,` + result("b") + `]`,
		"result answered twice":   `[` + user + `,` + call("a", "get", "{}") + `,` + result("a") + `,` + result("a") + `]`,
		"call not declared":       `[` + user + `,` + call("a", "other", "{}") + `,` + result("a") + `]`,
		"arguments not json":      `[` + user + `,` + call("a", "get", "{oops") + `,` + result("a") + `]`,
		"arguments an array":      `[` + user + `,` + call("a", "get", "[1]") + `,` + result("a") + `]`,
		"arguments a number":      `[` + user + `,` + call("a", "get", "5") + `,` + result("a") + `]`,
		"duplicate ids":           `[` + user + `,` + call("a", "get", "{}") + `,` + result("a") + `,{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"get","arguments":"{}"}}]},` + result("a") + `]`,
		"ids equal once cleaned":  `[` + user + `,{"role":"assistant","tool_calls":[{"id":"a.b","type":"function","function":{"name":"get","arguments":"{}"}},{"id":"a:b","type":"function","function":{"name":"get","arguments":"{}"}}]},` + result("a.b") + `,` + result("a:b") + `]`,
		"id with nothing usable":  `[` + user + `,` + call("...", "get", "{}") + `,` + result("...") + `]`,
		"id over the limit":       `[` + user + `,` + call(strings.Repeat("a", 65), "get", "{}") + `,` + result(strings.Repeat("a", 65)) + `]`,
		"call without function":   `[` + user + `,{"role":"assistant","tool_calls":[{"id":"a","type":"function"}]},` + result("a") + `]`,
		"call of another type":    `[` + user + `,{"role":"assistant","tool_calls":[{"id":"a","type":"custom","function":{"name":"get"}}]},` + result("a") + `]`,
		"result with an image":    `[` + user + `,` + call("a", "get", "{}") + `,{"role":"tool","tool_call_id":"a","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]`,
	}
	for name, messages := range cases {
		t.Run(name, func(t *testing.T) { refused(t, `{"messages":`+messages+tools+`}`) })
	}
	// The same conversation, whole, is served; ids that only need cleaning are cleaned on both sides.
	result2 := convert(t, `{"messages":[`+user+`,`+call("a.b", "get", "{}")+`,`+result("a.b")+`]`+tools+`}`)
	if at(result2, "conversationState.history.1.assistantResponseMessage.toolUses.0.toolUseId") != "ab" ||
		at(result2, "conversationState.currentMessage.userInputMessage.userInputMessageContext.toolResults.0.toolUseId") != "ab" {
		t.Fatalf("%v", result2)
	}
}

func TestMissingToolIDsAreRepairedOnlyOnTheClientsOwnBody(t *testing.T) {
	chat := `{"messages":[` + user + `,{"role":"assistant","tool_calls":[{"type":"function","function":{"name":"get","arguments":"{}"}}]},{"role":"tool","content":"r"}],"tools":[{"type":"function","function":{"name":"get"}}]}`
	result, _, _, err := convertWith(t, chat, RequestOptions{Repair: true})
	if err != nil {
		t.Fatal(err)
	}
	id := at(result, "conversationState.history.1.assistantResponseMessage.toolUses.0.toolUseId")
	if id == nil || id != at(result, "conversationState.currentMessage.userInputMessage.userInputMessageContext.toolResults.0.toolUseId") {
		t.Fatalf("%v", result)
	}
	if _, _, _, err := convertWith(t, chat, RequestOptions{Repair: false}); err == nil {
		t.Fatal("a body that is not the client's own is not repaired")
	}
}

func TestToolNames(t *testing.T) {
	result, names, _, err := convertWith(t, wrap(`,"tools":[
		{"type":"function","function":{"name":"mcp__gitea__search_repos"}},
		{"type":"function","function":{"name":"_leading_"}},
		{"type":"function","function":{"name":"my.tool/search"}}]`), RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, spec := range at(result, "conversationState.currentMessage.userInputMessage.userInputMessageContext.tools").([]any) {
		got = append(got, at(spec, "toolSpecification.name").(string))
	}
	if strings.Join(got, ",") != "mcp__gitea__search_repos,_leading_,my_tool_search" {
		t.Fatalf("%v", got)
	}
	if names.Original("my_tool_search") != "my.tool/search" || names.Original("_leading_") != "_leading_" {
		t.Fatal("names do not map back")
	}
	for name, fields := range map[string]string{
		"collides with a valid name":  `[{"type":"function","function":{"name":"my.tool"}},{"type":"function","function":{"name":"my_tool"}}]`,
		"collide with each other":     `[{"type":"function","function":{"name":"a.b"}},{"type":"function","function":{"name":"a/b"}}]`,
		"empty once cleaned, twice":   `[{"type":"function","function":{"name":"."}},{"type":"function","function":{"name":"/"}}]`,
		"same name, different":        `[{"type":"function","function":{"name":"a","description":"x"}},{"type":"function","function":{"name":"a","description":"y"}}]`,
		"description over the limit":  `[{"type":"function","function":{"name":"a","description":"` + strings.Repeat("é", 10238) + `"}}]`,
		"description not a string":    `[{"type":"function","function":{"name":"a","description":5}}]`,
		"parameters not an object":    `[{"type":"function","function":{"name":"a","parameters":[]}}]`,
		"parameters root not object":  `[{"type":"function","function":{"name":"a","parameters":{"type":"string"}}}]`,
		"parameters root a type list": `[{"type":"function","function":{"name":"a","parameters":{"type":["object","null"]}}}]`,
		"properties not an object":    `[{"type":"function","function":{"name":"a","parameters":{"properties":[]}}}]`,
		"required not strings":        `[{"type":"function","function":{"name":"a","parameters":{"properties":{"x":{}},"required":[1]}}}]`,
		"required not a list":         `[{"type":"function","function":{"name":"a","parameters":{"required":"x"}}}]`,
	} {
		t.Run(name, func(t *testing.T) { refused(t, wrap(`,"tools":`+fields)) })
	}
	// The description limit is in characters; even identical duplicate tools are refused.
	convert(t, wrap(`,"tools":[{"type":"function","function":{"name":"a","description":"`+strings.Repeat("é", 10237)+`"}}]`))
	refused(t, wrap(`,"tools":[{"type":"function","function":{"name":"a"}},{"type":"function","function":{"name":"a"}}]`))
}

func TestToolDescriptionDefault(t *testing.T) {
	for _, description := range []string{``, `,"description":null`, `,"description":""`} {
		result := convert(t, wrap(`,"tools":[{"type":"function","function":{"name":"f.g"`+description+`}}]`))
		if at(result, "conversationState.currentMessage.userInputMessage.userInputMessageContext.tools.0.toolSpecification.description") != "Tool: f.g" {
			t.Fatalf("%s: %v", description, result)
		}
	}
}

// Each row of PARITY 6.8's schema table.
func TestSchemaRows(t *testing.T) {
	schema := func(parameters string) (map[string]any, Normalized, error) {
		result, _, info, err := convertWith(t, wrap(`,"tools":[{"type":"function","function":{"name":"f","parameters":`+parameters+`}}]`), RequestOptions{})
		if err != nil {
			return nil, info.Normalized, err
		}
		return at(result, "conversationState.currentMessage.userInputMessage.userInputMessageContext.tools.0.toolSpecification.inputSchema.json").(map[string]any), info.Normalized, nil
	}
	rows := []struct {
		name       string
		parameters string
		check      func(map[string]any, Normalized) string
		refuse     bool
	}{
		{"absent parameters", `null`, func(s map[string]any, n Normalized) string {
			if s["type"] != "object" || len(s["properties"].(map[string]any)) != 0 {
				return fmt.Sprint(s)
			}
			return ""
		}, false},
		{"empty parameters", `{}`, func(s map[string]any, n Normalized) string {
			if s["type"] != "object" {
				return fmt.Sprint(s)
			}
			return ""
		}, false},
		{"additionalProperties true", `{"type":"object","properties":{},"additionalProperties":true}`, func(s map[string]any, n Normalized) string {
			if _, there := s["additionalProperties"]; there || n.AdditionalPropertiesStripped != 1 || n.AdditionalPropertiesFalse != 0 {
				return fmt.Sprint(s, n)
			}
			return ""
		}, false},
		{"additionalProperties false", `{"type":"object","properties":{},"additionalProperties":false}`, func(s map[string]any, n Normalized) string {
			if _, there := s["additionalProperties"]; there || n.AdditionalPropertiesStripped != 1 || n.AdditionalPropertiesFalse != 1 {
				return fmt.Sprint(s, n)
			}
			return ""
		}, false},
		{"additionalProperties empty schema", `{"additionalProperties":{}}`, nil, true},
		{"additionalProperties null", `{"additionalProperties":null}`, func(s map[string]any, n Normalized) string {
			if _, there := s["additionalProperties"]; there {
				return fmt.Sprint(s)
			}
			return ""
		}, false},
		{"additionalProperties a schema", `{"additionalProperties":{"type":"string"}}`, nil, true},
		{"additionalProperties a schema, nested", `{"properties":{"a":{"type":"object","additionalProperties":{"properties":{"x":{}}}}}}`, nil, true},
		{"additionalProperties a string", `{"additionalProperties":"no"}`, nil, true},
		{"additionalProperties nested false", `{"properties":{"a":{"type":"object","additionalProperties":false,"items":{"additionalProperties":false}}}}`, func(s map[string]any, n Normalized) string {
			a := at(s, "properties.a").(map[string]any)
			if _, there := a["additionalProperties"]; there || n.AdditionalPropertiesFalse != 2 {
				return fmt.Sprint(s, n)
			}
			return ""
		}, false},
		{"a property named additionalProperties is a property", `{"properties":{"additionalProperties":{"type":"string"}},"required":["additionalProperties"]}`, func(s map[string]any, n Normalized) string {
			if at(s, "properties.additionalProperties.type") != "string" || n.AdditionalPropertiesStripped != 0 || n.RequiredDropped != 0 {
				return fmt.Sprint(s, n)
			}
			return ""
		}, false},
		{"enum, const and default are data", `{"properties":{"a":{"enum":[{"additionalProperties":false,"required":[]}],"const":{"required":[]},"default":{"additionalProperties":true}}}}`, func(s map[string]any, n Normalized) string {
			if n.AdditionalPropertiesStripped != 0 || n.EmptyRequiredDropped != 0 || at(s, "properties.a.default.additionalProperties") != true {
				return fmt.Sprint(s, n)
			}
			return ""
		}, false},
		{"required naming an undeclared property", `{"properties":{"a":{}},"required":["a","ghost","ghost2","a"]}`, func(s map[string]any, n Normalized) string {
			if fmt.Sprint(s["required"]) != "[a]" || n.RequiredDropped != 2 {
				return fmt.Sprint(s, n)
			}
			return ""
		}, false},
		{"required all undeclared", `{"required":["ghost"]}`, func(s map[string]any, n Normalized) string {
			if _, there := s["required"]; there || n.RequiredDropped != 1 {
				return fmt.Sprint(s, n)
			}
			return ""
		}, false},
		{"required kept where properties come from composition", `{"allOf":[{"properties":{"a":{}}}],"required":["a"]}`, func(s map[string]any, n Normalized) string {
			if fmt.Sprint(s["required"]) != "[a]" || n.RequiredDropped != 0 {
				return fmt.Sprint(s, n)
			}
			return ""
		}, false},
		{"empty required, root and nested", `{"required":[],"properties":{"a":{"type":"object","required":[]}}}`, func(s map[string]any, n Normalized) string {
			if _, there := s["required"]; there || n.EmptyRequiredDropped != 2 {
				return fmt.Sprint(s, n)
			}
			return ""
		}, false},
		{"nested object schemas elsewhere", `{"properties":{"a":{"anyOf":[{"additionalProperties":false},{"type":"null"}]}},"$defs":{"d":{"additionalProperties":false}}}`, func(s map[string]any, n Normalized) string {
			if n.AdditionalPropertiesFalse != 2 {
				return fmt.Sprint(s, n)
			}
			return ""
		}, false},
		{"root type object", `{"type":"object"}`, nil, false},
		{"root type number", `{"type":"number"}`, nil, true},
		{"root type null", `{"type":null}`, nil, true},
		{"parameters a string", `"x"`, nil, true},
		{"parameters a number", `5`, nil, true},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			s, n, err := schema(row.parameters)
			if row.refuse {
				if err == nil {
					t.Fatalf("expected a refusal, got %v", s)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if row.check != nil {
				if problem := row.check(s, n); problem != "" {
					t.Fatal(problem)
				}
			}
		})
	}
}

func TestImages(t *testing.T) {
	image := func(url string) string {
		return `{"messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":` + strconv.Quote(url) + `,"detail":"high"}}]}]}`
	}
	result := convert(t, image("data:image/PNG;base64,AAAA"))
	current := at(result, "conversationState.currentMessage.userInputMessage")
	if at(current, "images.0.format") != "png" || at(current, "images.0.source.bytes") != "AAAA" || at(current, "content") != "look" {
		t.Fatalf("%v", current)
	}
	for _, url := range []string{"https://example.com/a.png", "http://x/y", "data:text/plain;base64,AAAA", "data:image/png;base64,@@@@", "data:image/;base64,AAAA", "data:image/png,AAAA", "", "AAAA"} {
		refused(t, image(url))
	}
	refused(t, `{"messages":[{"role":"user","content":[{"type":"image_url","image_url":"data:image/png;base64,AAAA"}]}]}`)
	refused(t, `{"messages":[{"role":"user","content":[{"type":"image_url"}]}]}`)
	refused(t, `{"messages":[{"role":"assistant","content":[{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]},{"role":"user","content":"x"}]}`)
}

func TestConsecutiveTurnsMergeWithoutLosingContent(t *testing.T) {
	result := convert(t, `{"messages":[{"role":"user","content":"a"},{"role":"user","content":"b"},
		{"role":"assistant","content":"c"},{"role":"assistant","content":"d"},{"role":"user","content":"e"}]}`)
	history := at(result, "conversationState.history").([]any)
	if len(history) != 2 || at(history[0], "userInputMessage.content") != "a\n\nb" || at(history[1], "assistantResponseMessage.content") != "c\n\nd" {
		t.Fatalf("%v", history)
	}
}

func TestDeveloperIsSystemAndEchoedFieldsAreAdvisory(t *testing.T) {
	result := convert(t, `{"messages":[{"role":"developer","content":"rules"},{"role":"user","content":"hi","cache_control":{"type":"ephemeral"}},
		{"role":"assistant","content":"yes","reasoning_content":"thought","refusal":null},{"role":"user","content":"ok"}]}`)
	if at(result, "conversationState.history.0.userInputMessage.content") != "<instructions>\nrules\n</instructions>\n\nhi" {
		t.Fatalf("%v", result)
	}
}

func TestConversationIDsAreFreshPerRequest(t *testing.T) {
	body := func() map[string]any {
		b, _ := concerns.WireObject([]byte(wrap(``)))
		return b
	}
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		out, _, _, err := ChatRequestToKiro(body(), RequestOptions{Model: "m"})
		if err != nil {
			t.Fatal(err)
		}
		parsed, _ := concerns.WireObject(out)
		id, _ := at(parsed, "conversationState.conversationId").(string)
		if id == "" || seen[id] {
			t.Fatalf("id %q repeated or empty", id)
		}
		seen[id] = true
	}
}

func TestNoTimeContextIsInvented(t *testing.T) {
	out, _, _, err := ChatRequestToKiro(map[string]any{"messages": []any{map[string]any{"role": "user", "content": "hi"}}}, RequestOptions{Model: "m"})
	if err != nil || strings.Contains(string(out), "Current time") {
		t.Fatalf("%s %v", out, err)
	}
}

func TestThinking(t *testing.T) {
	prefix := func(budget string) string {
		return "<thinking_mode>enabled</thinking_mode>\n<max_thinking_length>" + budget + "</max_thinking_length>"
	}
	content := func(result map[string]any) string {
		return at(result, "conversationState.currentMessage.userInputMessage.content").(string)
	}
	cases := []struct {
		name    string
		model   string
		fields  string
		content string
		tags    bool
		model_  string
		extra   string
	}{
		{"nothing asked", "claude-sonnet-4.5", ``, "hi", false, "claude-sonnet-4.5", ""},
		{"thinking suffix", "claude-sonnet-4.5-thinking", ``, prefix("16000") + "\n\nhi", true, "claude-sonnet-4.5", ""},
		{"thinking suffix, explicitly off", "claude-sonnet-4.5-thinking", `,"reasoning_effort":"none"`, "hi", false, "claude-sonnet-4.5", ""},
		{"agentic", "claude-sonnet-4.5-agentic", ``, agenticSystemPrompt + "\n\nhi", false, "claude-sonnet-4.5", ""},
		{"thinking and agentic", "claude-sonnet-4.5-thinking-agentic", ``, prefix("16000") + "\n\n" + agenticSystemPrompt + "\n\nhi", true, "claude-sonnet-4.5", ""},
		{"level suffix", "claude-sonnet-4.5(high)", ``, prefix("24576") + "\n\nhi", true, "claude-sonnet-4.5", ""},
		{"budget suffix", "claude-sonnet-4.5(99999)", ``, prefix("32000") + "\n\nhi", true, "claude-sonnet-4.5", ""},
		{"none suffix", "claude-sonnet-4.5-thinking(none)", ``, "hi", false, "claude-sonnet-4.5", ""},
		{"reasoning_effort", "claude-sonnet-4.5", `,"reasoning_effort":"medium"`, prefix("8192") + "\n\nhi", true, "claude-sonnet-4.5", ""},
		{"reasoning.effort", "claude-sonnet-4.5", `,"reasoning":{"effort":"low"}`, prefix("1024") + "\n\nhi", true, "claude-sonnet-4.5", ""},
		{"thinking budget", "claude-sonnet-4.5", `,"thinking":{"type":"enabled","budget_tokens":2000}`, prefix("2000") + "\n\nhi", true, "claude-sonnet-4.5", ""},
		{"thinking disabled", "claude-sonnet-4.5", `,"thinking":{"type":"disabled"}`, "hi", false, "claude-sonnet-4.5", ""},
		{"unknown level", "claude-sonnet-4.5", `,"reasoning_effort":"ultra"`, prefix("16000") + "\n\nhi", true, "claude-sonnet-4.5", ""},
		{"enable_thinking", "claude-sonnet-4.5", `,"enable_thinking":true,"thinking_budget":3000`, prefix("3000") + "\n\nhi", true, "claude-sonnet-4.5", ""},
		{"claude 4.7 adaptive fields", "claude-opus-4.7", `,"reasoning_effort":"high"`, prefix("24576") + "\n\nhi", true, "claude-opus-4.7", `{"output_config":{"effort":"high"},"thinking":{"display":"summarized","type":"adaptive"}}`},
		{"claude 4.5 has no fields", "claude-opus-4.5", `,"reasoning_effort":"high"`, prefix("24576") + "\n\nhi", true, "claude-opus-4.5", ""},
		{"gpt native effort has no tag", "gpt-5.6-sol", `,"reasoning_effort":"high"`, "hi", false, "gpt-5.6-sol", `{"reasoning":{"effort":"high"}}`},
		{"gpt effort none is no field", "gpt-5.6-sol", `,"reasoning_effort":"none"`, "hi", false, "gpt-5.6-sol", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result, _, info, err := convertWith(t, wrap(c.fields), RequestOptions{Model: c.model})
			if err != nil {
				t.Fatal(err)
			}
			if got := content(result); got != c.content {
				t.Errorf("content %q, want %q", got, c.content)
			}
			if info.ThinkingTags != c.tags || info.UpstreamModel != c.model_ {
				t.Errorf("info %+v", info)
			}
			if at(result, "conversationState.currentMessage.userInputMessage.modelId") != c.model_ {
				t.Errorf("modelId %v", result)
			}
			extra := ""
			if fields, there := result["additionalModelRequestFields"]; there {
				extra = asJSON(fields)
			}
			if c.extra != "" {
				var want any
				_ = json.Unmarshal([]byte(c.extra), &want)
				if extra != asJSON(want) {
					t.Errorf("fields %s, want %s", extra, c.extra)
				}
			} else if extra != "" {
				t.Errorf("fields %s", extra)
			}
		})
	}
}

func asJSON(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}

// The prefix goes in front of the first user turn only, whichever turn that is.
func TestPrefixIsOnTheFirstUserTurnOnly(t *testing.T) {
	result, _, _, err := convertWith(t, `{"messages":[{"role":"user","content":"one"},{"role":"assistant","content":"two"},{"role":"user","content":"three"}]}`, RequestOptions{Model: "claude-sonnet-4.5-thinking"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(at(result, "conversationState.history.0.userInputMessage.content").(string), "<thinking_mode>") ||
		at(result, "conversationState.currentMessage.userInputMessage.content") != "three" {
		t.Fatalf("%v", result)
	}
}

func TestThinkingModeTagInTheConversationTurnsItOn(t *testing.T) {
	for _, content := range []string{`"<thinking_mode>enabled</thinking_mode> go"`, `[{"type":"text","text":"x <thinking_mode>interleaved</thinking_mode>"}]`} {
		_, _, info, err := convertWith(t, `{"messages":[{"role":"user","content":`+content+`}]}`, RequestOptions{})
		if err != nil || !info.ThinkingTags {
			t.Fatalf("%s %+v %v", content, info, err)
		}
	}
	_, _, info, _ := convertWith(t, `{"messages":[{"role":"assistant","content":"<thinking_mode>enabled</thinking_mode>"},{"role":"user","content":"x"}]}`, RequestOptions{})
	if info.ThinkingTags {
		t.Fatal("only system and user turns count")
	}
}

func TestModelThatNamesNothingIsRefused(t *testing.T) {
	body, _ := concerns.WireObject([]byte(wrap(``)))
	if _, _, _, err := ChatRequestToKiro(body, RequestOptions{Model: "-agentic"}); err == nil {
		t.Fatal("expected a refusal")
	}
	if _, _, _, err := ChatRequestToKiro(body, RequestOptions{Model: ""}); err == nil {
		t.Fatal("expected a refusal")
	}
}

func TestParseSuffix(t *testing.T) {
	cases := map[string]struct {
		model string
		mode  string
	}{
		"m":           {"m", ""},
		"m(high)":     {"m", "level"},
		"m (HIGH) ":   {"m", "level"},
		"m(ultra)":    {"m", "level"},
		"m(8192)":     {"m", "budget"},
		"m(auto)":     {"m", "auto"},
		"m(off)":      {"m", "none"},
		"m(none)":     {"m", "none"},
		"m(bogus)":    {"m", ""},
		"m()":         {"m()", ""},
		"m(a)(b)":     {"m(a)", ""},
		"m(1.5)":      {"m", ""},
		"m(-3)":       {"m", ""},
		"a(b)c(high)": {"a(b)c", "level"},
	}
	for model, want := range cases {
		clean, over := parseSuffix(model)
		mode := ""
		if over != nil {
			mode = over.mode
		}
		if clean != want.model || mode != want.mode {
			t.Errorf("%q: %q %q, want %q %q", model, clean, mode, want.model, want.mode)
		}
	}
}

func TestGuardValueKinds(t *testing.T) {
	for _, key := range []string{"temperature", "top_p", "max_tokens", "max_completion_tokens", "presence_penalty", "frequency_penalty", "n", "logprobs", "parallel_tool_calls", "tool_choice", "reasoning_effort", "thinking", "reasoning", "output_config", "thinkingConfig", "thinking_budget", "enable_thinking", "modalities"} {
		for _, value := range []string{`null`, `""`, `[]`, `{}`, `false`, `0`, `1.5`, `"wrong"`, `1e999`, `9223372036854775808`} {
			t.Run(key+"="+value, func(t *testing.T) {
				_, _, _, err := convertWith(t, wrap(fmt.Sprintf(",%q:%s", key, value)), RequestOptions{})
				allowed := value == `null` ||
					((key == "temperature" || key == "top_p") && (value == `0` || value == `1.5` || value == `9223372036854775808`)) ||
					((key == "presence_penalty" || key == "frequency_penalty") && value == `0`) ||
					((key == "logprobs" || key == "enable_thinking") && value == `false`)
				if allowed != (err == nil) {
					t.Fatalf("allowed %v: %v", allowed, err)
				}
			})
		}
	}
}

func TestUnreadNestedRequestFieldsAreRefused(t *testing.T) {
	for _, fields := range []string{
		`,"n":1.5`, `,"response_format":{"type":"text","schema":{}}`,
		`,"reasoning":{"effort":"high","summary":"detailed"}`, `,"thinking":{"type":"enabled","unknown":0}`,
		`,"thinking":{"type":"enabled","budget_tokens":1.5}`, `,"thinkingConfig":{"thinkingBudget":1e999}`,
		`,"tools":[{"type":"function","unknown":[],"function":{"name":"f"}}]`,
		`,"tools":[{"type":"function","function":{"name":"f","strict":true}}]`,
		`,"tools":[{"type":"function","function":{"name":"f","extra":""}}]`,
		`,"tools":[{"type":"function","function":{"name":"f","parameters":{"properties":{"x":{"required":[1]}}}}}]`,
	} {
		refused(t, wrap(fields))
	}
	for _, message := range []string{
		`{"role":"user","content":"hi","name":""}`, `{"role":"user","content":"hi","name":0}`,
		`{"role":"user","content":"hi","tool_calls":[]}`, `{"role":"user","content":"hi","is_error":false}`,
		`{"role":"user","content":[{"type":"text","text":"hi","extra":[]}]}`,
		`{"role":"user","content":[{"type":"image_url","text":"lost","image_url":{"url":"data:image/png;base64,AAAA"}}]}`,
	} {
		refused(t, `{"messages":[`+message+`]}`)
	}
}

func TestRepairCannotHideInvalidSuppliedIDs(t *testing.T) {
	for _, id := range []string{`""`, `"..."`, `0`, `false`, `[]`, `{}`, `"` + strings.Repeat("a", 65) + `"`} {
		for _, repair := range []bool{false, true} {
			body := `{"messages":[` + user + `,{"role":"assistant","tool_calls":[{"id":` + id + `,"type":"function","function":{"name":"get","arguments":"{}"}}]},{"role":"tool","tool_call_id":` + id + `,"content":"r"}],"tools":[{"type":"function","function":{"name":"get"}}]}`
			if _, _, _, err := convertWith(t, body, RequestOptions{Repair: repair}); err == nil {
				t.Errorf("repair %v accepted %s", repair, id)
			}
		}
	}
}

func TestNestedRequiredEntriesAreNormalized(t *testing.T) {
	result, _, info, err := convertWith(t, wrap(`,"tools":[{"type":"function","function":{"name":"f","parameters":{"properties":{"obj":{"type":"object","properties":{"yes":{}},"required":["yes","no"]}}}}}]`), RequestOptions{})
	if err != nil || info.Normalized.RequiredDropped != 1 || fmt.Sprint(at(result, "conversationState.currentMessage.userInputMessage.userInputMessageContext.tools.0.toolSpecification.inputSchema.json.properties.obj.required")) != "[yes]" {
		t.Fatalf("%v %+v %v", result, info, err)
	}
}

func TestSchemaKeywordLocations(t *testing.T) {
	for _, location := range []string{`"dependencies":{"x":%s}`, `"contentSchema":%s`, `"items":[%s]`, `"allOf":[%s]`, `"$defs":{"x":%s}`} {
		for _, properties := range []string{`false`, `true`, `{}`, `{"type":"string"}`} {
			parameters := "{" + fmt.Sprintf(location, `{"additionalProperties":`+properties+`}`) + "}"
			_, _, info, err := convertWith(t, wrap(`,"tools":[{"type":"function","function":{"name":"f","parameters":`+parameters+`}}]`), RequestOptions{})
			refuse := properties != `false` && properties != `true`
			if (err != nil) != refuse || !refuse && info.Normalized.AdditionalPropertiesStripped != 1 {
				t.Errorf("%s: %+v %v", parameters, info, err)
			}
		}
	}
	for _, parameters := range []string{`{"items":[5]}`, `{"not":[]}`, `{"contains":null}`, `{"oneOf":["bad"]}`, `{"dependencies":{"x":[1]}}`} {
		refused(t, wrap(`,"tools":[{"type":"function","function":{"name":"f","parameters":`+parameters+`}}]`))
	}
	convert(t, wrap(`,"tools":[{"type":"function","function":{"name":"f","parameters":{"dependencies":{"x":["y"]},"not":false,"items":[true]}}}]`))
}

func TestUnknownThinkingIntentIsRefused(t *testing.T) {
	for _, model := range []string{"m(bogus)", "m(0)", "m(1.5)", "m(-3)", "m(99999999999999999999999)"} {
		if _, _, _, err := convertWith(t, wrap(""), RequestOptions{Model: model}); err == nil {
			t.Errorf("silently consumed %s", model)
		}
	}
	for _, fields := range []string{`,"reasoning_effort":"bogus"`, `,"thinkingConfig":{"thinkingLevel":"bogus"}`, `,"output_config":{"effort":"bogus"}`} {
		refused(t, wrap(fields))
	}
}

func TestExactNumericGuards(t *testing.T) {
	for _, fields := range []string{`,"n":1.0000000000000000001`, `,"presence_penalty":1e-999`, `,"frequency_penalty":-1e-999`} {
		refused(t, wrap(fields))
	}
	for _, fields := range []string{`,"thinking":{"type":"enabled","budget_tokens":9223372036854775807}`, `,"enable_thinking":true,"thinking_budget":9223372036854775807`, `,"thinkingConfig":{"thinkingBudget":9223372036854775807}`} {
		out := convert(t, wrap(fields))
		if !strings.Contains(at(out, "conversationState.currentMessage.userInputMessage.content").(string), "<max_thinking_length>32000</max_thinking_length>") {
			t.Fatalf("budget overflowed: %v", out)
		}
	}
}

func TestComposedRequiredEntriesAreNotLost(t *testing.T) {
	for _, parameters := range []string{`{"properties":{"a":{}},"allOf":[{"required":["a"]}]}`, `{"properties":{"a":{}},"$defs":{"rule":{"required":["a"]}},"$ref":"#/$defs/rule"}`, `{"properties":{"a":{}},"if":{"required":["a"]}}`} {
		out, _, info, err := convertWith(t, wrap(`,"tools":[{"type":"function","function":{"name":"f","parameters":`+parameters+`}}]`), RequestOptions{})
		if err != nil || info.Normalized.RequiredDropped != 0 || !strings.Contains(asJSON(out), `"required":["a"]`) {
			t.Fatalf("%s: %+v %v", parameters, info, err)
		}
	}
}
