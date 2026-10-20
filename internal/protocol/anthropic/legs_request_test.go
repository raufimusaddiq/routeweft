package anthropic

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

func equalJSON(t *testing.T, got []byte, want string) {
	t.Helper()
	var left, right any
	if err := json.Unmarshal(got, &left); err != nil {
		t.Fatalf("invalid JSON %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &right); err != nil {
		t.Fatalf("invalid want: %v", err)
	}
	if !reflect.DeepEqual(left, right) {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
}

// request anthropic-messages>openai-chat (claude-to-openai.js:14-283). The
// conversation covers every branch the leg has: billing-header stripping, array
// system, a mid-conversation system turn, base64 images, thinking dropped, text
// before a tool_use, a tool_result that carries an image, and tool_choice.
func TestMessagesRequestToChat(t *testing.T) {
	request := `{
	  "model":"client-model","max_tokens":1000,"temperature":0.2,"stream":true,
	  "system":[{"type":"text","text":"x-anthropic-billing-header: cch=abc\nYou are terse."},{"type":"text","text":"Be kind."}],
	  "messages":[
	    {"role":"user","content":[{"type":"text","text":"look"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}}]},
	    {"role":"assistant","content":[{"type":"thinking","thinking":"hmm","signature":"s"},{"type":"text","text":"checking"},{"type":"tool_use","id":"toolu_1","name":"read_file","input":{"path":"a.go"}}]},
	    {"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"line1"},{"type":"text","text":"line2"},{"type":"image","source":{"type":"base64","media_type":"image/jpeg","data":"BBBB"}}]},{"type":"text","text":"thanks"}]},
	    {"role":"system","content":"remember the budget"},
	    {"role":"user","content":"go on"}
	  ],
	  "tools":[{"name":"read_file","description":"Read","input_schema":{"type":"object","properties":{"path":{"type":"string"}}}},{"name":"noop"}],
	  "tool_choice":{"type":"tool","name":"read_file"},
	  "reasoning":{"effort":"high"}
	}`
	got, err := MessagesRequestToChat([]byte(request), "upstream-model", true)
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, got, `{
	  "model":"upstream-model","max_tokens":32000,"temperature":0.2,"stream":true,"stream_options":{"include_usage":true},
	  "messages":[
	    {"role":"system","content":"You are terse.\nBe kind."},
	    {"role":"user","content":[{"type":"text","text":"look"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]},
	    {"role":"assistant","content":"checking","tool_calls":[{"id":"toolu_1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.go\"}"}}]},
	    {"role":"tool","tool_call_id":"toolu_1","content":"line1\nline2"},
	    {"role":"user","content":[{"type":"text","text":"[Image from tool result toolu_1]"},{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,BBBB"}},{"type":"text","text":"thanks"}]},
	    {"role":"user","content":"<instructions>\nremember the budget\n</instructions>"},
	    {"role":"user","content":"go on"}
	  ],
	  "tools":[
	    {"type":"function","function":{"name":"read_file","description":"Read","parameters":{"type":"object","properties":{"path":{"type":"string"}}}}},
	    {"type":"function","function":{"name":"noop","description":"","parameters":{"type":"object","properties":{}}}}
	  ],
	  "tool_choice":{"type":"function","function":{"name":"read_file"}},
	  "reasoning":{"effort":"high"},"reasoning_effort":"high"
	}`)
}

// The pin's adjustMaxTokens: a tool-using request never goes below 32000, a
// thinking budget forces max_tokens past it, and the ceiling caps everything.
func TestAdjustMaxTokens(t *testing.T) {
	cases := []struct {
		body string
		want int64
	}{
		{`{"max_tokens":100}`, 100},
		{`{}`, 64000},
		{`{"max_tokens":100,"tools":[{"name":"t"}]}`, 32000},
		{`{"max_tokens":100,"tools":[]}`, 100},
		{`{"max_tokens":4000,"thinking":{"budget_tokens":8000}}`, 9024},
		{`{"max_tokens":999999}`, 64000},
		{`{"max_tokens":50000,"thinking":{"budget_tokens":60000}}`, 61024},
		{`{"max_tokens":50000,"thinking":{"budget_tokens":70000}}`, 64000},
	}
	for _, c := range cases {
		body, _ := concerns.WireObject([]byte(c.body))
		if got := AdjustMaxTokens(body, 0); got != c.want {
			t.Errorf("%s -> %d want %d", c.body, got, c.want)
		}
	}
}

func TestMessagesRequestToChatEdgeCases(t *testing.T) {
	// A tool_use the client never answered gets the pin's placeholder reply, an
	// unanswered call among answered ones is filled individually, and an empty
	// content array and an image given by URL behave as the pin does.
	got, err := MessagesRequestToChat([]byte(`{"model":"m","max_tokens":9,"messages":[
	  {"role":"user","content":"a"},
	  {"role":"assistant","content":[{"type":"tool_use","id":"t1","name":"x","input":{}},{"type":"tool_use","id":"t2","name":"y"}]},
	  {"role":"user","content":[{"type":"tool_result","tool_use_id":"t2","content":"done"}]},
	  {"role":"assistant","content":[]},
	  {"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://example.invalid/i.png"}},{"type":"text","text":"b"}]}
	]}`), "m", false)
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, got, `{"model":"m","max_tokens":9,"stream":false,"messages":[
	  {"role":"user","content":"a"},
	  {"role":"assistant","tool_calls":[{"id":"t1","type":"function","function":{"name":"x","arguments":"{}"}},{"id":"t2","type":"function","function":{"name":"y","arguments":"{}"}}]},
	  {"role":"tool","tool_call_id":"t2","content":"done"},
	  {"role":"tool","tool_call_id":"t1","content":"[No response received]"},
	  {"role":"user","content":[{"type":"image_url","image_url":{"url":"https://example.invalid/i.png"}},{"type":"text","text":"b"}]}
	]}`)
}

// A Messages client may name a tool with characters Chat upstreams reject; ids
// that Anthropic would reject are repaired before conversion, in both halves.
func TestMessagesRequestToChatRepairsToolIDs(t *testing.T) {
	got, err := MessagesRequestToChat([]byte(`{"model":"m","messages":[
	  {"role":"user","content":"a"},
	  {"role":"assistant","content":[{"type":"tool_use","id":"call.7","name":"x","input":{"k":1}}]},
	  {"role":"user","content":[{"type":"tool_result","tool_use_id":"call.7","content":"ok"}]}]}`), "m", false)
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	_ = json.Unmarshal(got, &body)
	messages := concerns.Items(body["messages"])
	call := concerns.Object(concerns.Items(concerns.Object(messages[1])["tool_calls"])[0])
	if call["id"] != "call7" || concerns.Object(messages[2])["tool_call_id"] != "call7" {
		t.Fatalf("ids %v / %v", call["id"], concerns.Object(messages[2])["tool_call_id"])
	}
}

// The leg rebuilds every block without cache_control, so a Messages client's
// breakpoints never reach a Chat upstream (claude-to-openai.js:171-175); the
// preserve quirk only matters to bodies that arrive in Chat shape.
func TestMessagesRequestToChatDropsCacheControl(t *testing.T) {
	got, err := MessagesRequestToChat([]byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}}]}]}`), "m", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(got), "cache_control") {
		t.Fatalf("cache_control leaked: %s", got)
	}
}

// request openai-chat>anthropic-messages (openai-to-claude.js:17-338): the
// Claude Code system prompt is injected first, system messages become one
// cached block, consecutive turns merge, tool results get their own user turn,
// the last assistant block and the last tool carry cache breakpoints, and
// MCP-style tool names are rewritten for the upstream. A developer message joins
// the system prompt (the pin drops it, PARITY V12).
func TestChatRequestToMessages(t *testing.T) {
	request := `{
	  "model":"client-model","max_tokens":2000,"temperature":0.5,
	  "messages":[
	    {"role":"system","content":"Be brief."},
	    {"role":"developer","content":"dev note"},
	    {"role":"user","content":"hi"},
	    {"role":"user","content":[{"type":"text","text":"again"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}},{"type":"image_url","image_url":{"url":"https://example.invalid/x.png"}}]},
	    {"role":"assistant","reasoning_content":"plan","content":"calling","tool_calls":[{"id":"call_1","type":"function","function":{"name":"srv.lookup","arguments":"{\"q\":\"x\"}"}}]},
	    {"role":"tool","tool_call_id":"call_1","content":"result"},
	    {"role":"assistant","content":"done"}
	  ],
	  "tools":[{"type":"function","function":{"name":"srv.lookup","description":"Look up","parameters":{"type":"object","properties":{"q":{"type":"string"}}}}}],
	  "tool_choice":{"type":"function","function":{"name":"srv.lookup"}},
	  "response_format":{"type":"json_object"}
	}`
	parsed, _ := concerns.WireObject([]byte(request))
	got, names, err := ChatRequestToMessages(parsed, "upstream", false, true, true)
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, got, `{
	  "model":"upstream","max_tokens":32000,"stream":false,"temperature":0.5,
	  "system":[
	    {"type":"text","text":"Be brief.\ndev note\nYou must respond with valid JSON. Respond ONLY with a JSON object, no other text.","cache_control":{"type":"ephemeral"}}
	  ],
	  "messages":[
	    {"role":"user","content":[{"type":"text","text":"hi"},{"type":"text","text":"again"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}},{"type":"image","source":{"type":"url","url":"https://example.invalid/x.png"}}]},
	    {"role":"assistant","content":[{"type":"text","text":"calling"},{"type":"tool_use","id":"call_1","name":"srv_lookup","input":{"q":"x"}}]},
	    {"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"result"}]},
	    {"role":"assistant","content":[{"type":"text","text":"done","cache_control":{"type":"ephemeral"}}]}
	  ],
	  "tools":[{"name":"srv_lookup","description":"Look up","input_schema":{"type":"object","properties":{"q":{"type":"string"}}},"cache_control":{"type":"ephemeral"}}],
	  "tool_choice":{"type":"tool","name":"srv_lookup"}
	}`)
	if names.Original("srv_lookup") != "srv.lookup" {
		t.Fatal("the rewritten tool name must map back for the response")
	}
}

// max_tokens is always set (the pin defaults it), a client that ended on
// assistant is prefilling, and one whose last turn was emptied gets "Continue.".
func TestChatRequestToMessagesTrailingTurnAndDefaults(t *testing.T) {
	body, _ := concerns.WireObject([]byte(`{"model":"m","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"},{"role":"user","content":""}]}`))
	got, _, err := ChatRequestToMessages(body, "m", true, true, false)
	if err != nil {
		t.Fatal(err)
	}
	var result map[string]any
	_ = json.Unmarshal(got, &result)
	if result["max_tokens"] != float64(64000) || result["stream"] != true {
		t.Fatalf("defaults %v", result)
	}
	messages := concerns.Items(result["messages"])
	last := concerns.Object(messages[len(messages)-1])
	if last["role"] != "user" || concerns.Text(concerns.Object(concerns.Items(last["content"])[0])["text"]) != "Continue." {
		t.Fatalf("an emptied final user turn must be restored: %v", messages)
	}
	body, _ = concerns.WireObject([]byte(`{"model":"m","messages":[{"role":"user","content":"a"},{"role":"assistant","content":"b"}]}`))
	got, _, _ = ChatRequestToMessages(body, "m", false, true, true)
	_ = json.Unmarshal(got, &result)
	if messages = concerns.Items(result["messages"]); concerns.Text(concerns.Object(messages[len(messages)-1])["role"]) != "assistant" {
		t.Fatalf("a prefilling client keeps its assistant turn: %v", messages)
	}
}

func TestChatRequestToMessagesToolChoiceAndBuiltInTools(t *testing.T) {
	cases := map[string]string{
		`"required"`:                   `{"type":"any"}`,
		`"none"`:                       `{"type":"none"}`,
		`"auto"`:                       `{"type":"auto"}`,
		`{"type":"any"}`:               `{"type":"any"}`,
		`{"type":"tool","name":"a.b"}`: `{"type":"tool","name":"a_b"}`,
		`{"function":{"name":"a.b"}}`:  `{"type":"tool","name":"a_b"}`,
	}
	for choice, want := range cases {
		body, _ := concerns.WireObject([]byte(`{"model":"m","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"a.b"}}],"tool_choice":` + choice + `}`))
		got, _, err := ChatRequestToMessages(body, "m", false, true, false)
		if err != nil {
			t.Fatal(err)
		}
		var result map[string]any
		_ = json.Unmarshal(got, &result)
		wanted, _ := json.Marshal(result["tool_choice"])
		equalJSON(t, wanted, want)
	}
	body, _ := concerns.WireObject([]byte(`{"model":"m","messages":[{"role":"user","content":"x"}],"tools":[{"type":"web_search_20250305","name":"web_search"},{"function":{"name":"bare"}}]}`))
	got, _, _ := ChatRequestToMessages(body, "m", false, true, false)
	var result map[string]any
	_ = json.Unmarshal(got, &result)
	tools := concerns.Items(result["tools"])
	if concerns.Text(concerns.Object(tools[0])["type"]) != "web_search_20250305" || concerns.Text(concerns.Object(tools[1])["name"]) != "bare" {
		t.Fatalf("built-in tools pass through and a bare function is a function: %v", tools)
	}
}

func TestMessagesRequestToChatKeepsClientStreamOptions(t *testing.T) {
	got, err := MessagesRequestToChat([]byte(`{"model":"m","stream":true,"stream_options":{"include_usage":false},"messages":[{"role":"user","content":"a"}]}`), "m", true)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(got), `"stream_options":{"include_usage":false}`) {
		t.Fatalf("the client's stream_options must win: %s", got)
	}
}

// A request whose only content the conversion would drop must still be a valid
// Chat request: the url image is forwarded, and an otherwise empty list keeps a
// turn.
func TestMessagesRequestToChatNeverProducesAnEmptyMessageList(t *testing.T) {
	got, err := MessagesRequestToChat([]byte(`{"model":"m","max_tokens":16,"messages":[{"role":"user","content":[{"type":"image","source":{"type":"url","url":"https://x/y.png"}}]}]}`), "m", false)
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, got, `{"model":"m","max_tokens":16,"stream":false,"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"https://x/y.png"}}]}]}`)
	got, err = MessagesRequestToChat([]byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"thinking","thinking":"x"}]}]}`), "m", false)
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, got, `{"model":"m","stream":false,"messages":[{"role":"user","content":[{"type":"text","text":""}]}]}`)
	body, _ := concerns.WireObject([]byte(`{"model":"m","messages":[{"role":"system","content":"only a system prompt"}]}`))
	encoded, _, err := ChatRequestToMessages(body, "m", false, true, false)
	if err != nil || !strings.Contains(string(encoded), `"role":"user"`) {
		t.Fatalf("a Chat request with no turns still needs one for Anthropic: %s %v", encoded, err)
	}
}

// Every field a Messages request can carry, converted to Chat: carried, not
// carried for a stated reason, or refused. This table is the proof that none is
// silently dropped; PARITY 6.3 records the reasons.
func TestMessagesRequestFieldsAreCarriedOrRefused(t *testing.T) {
	base := `"model":"m","messages":[{"role":"user","content":"hi"}]`
	carried := map[string]struct{ extra, want string }{
		"max_tokens":       {`"max_tokens":9`, `"max_tokens":9`},
		"temperature":      {`"temperature":0.3`, `"temperature":0.3`},
		"top_p":            {`"top_p":0.5`, `"top_p":0.5`},
		"stop_sequences":   {`"stop_sequences":["END"]`, `"stop":["END"]`},
		"metadata.user_id": {`"metadata":{"user_id":"u1"}`, `"user":"u1"`},
		"system":           {`"system":"be brief"`, `"content":"be brief"`},
		"tools":            {`"tools":[{"name":"t","input_schema":{"type":"object"}}]`, `"name":"t"`},
		"tool_choice any":  {`"tools":[{"name":"t"}],"tool_choice":{"type":"any"}`, `"tool_choice":"required"`},
		"tool_choice none": {`"tools":[{"name":"t"}],"tool_choice":{"type":"none"}`, `"tool_choice":"none"`},
		"tool_choice auto": {`"tools":[{"name":"t"}],"tool_choice":{"type":"auto"}`, `"tool_choice":"auto"`},
		"tool_choice tool": {`"tools":[{"name":"t"}],"tool_choice":{"type":"tool","name":"t"}`, `"tool_choice":{"function":{"name":"t"},"type":"function"}`},
		"disable_parallel": {`"tools":[{"name":"t"}],"tool_choice":{"type":"auto","disable_parallel_tool_use":true}`, `"parallel_tool_calls":false`},
		"reasoning_effort": {`"reasoning_effort":"high"`, `"reasoning_effort":"high"`},
		"stream":           {`"stream":true`, `"stream":true`},
	}
	for name, c := range carried {
		got, err := MessagesRequestToChat([]byte(`{`+base+`,`+c.extra+`}`), "m", strings.Contains(c.extra, `"stream":true`))
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if !strings.Contains(string(got), c.want) {
			t.Errorf("%s: %s not carried into %s", name, c.want, got)
		}
	}
	// Not carried, deliberately (PARITY 6.3): they shape sampling or billing, not
	// content, and Chat has no equivalent that OpenAI-compatible upstreams accept.
	for _, field := range []string{`"top_k":5`, `"service_tier":"auto"`, `"thinking":{"type":"enabled","budget_tokens":1024}`, `"output_config":{"effort":"high"}`} {
		got, err := MessagesRequestToChat([]byte(`{`+base+`,`+field+`}`), "m", false)
		if err != nil || strings.Contains(string(got), strings.Split(field, ":")[0]) {
			t.Errorf("%s must be accepted and not forwarded: %s %v", field, got, err)
		}
	}
	// Refused: serving the rest would be a confident wrong answer.
	refused := []string{
		`"mcp_servers":[{"name":"s","url":"https://x"}]`,
		`"container":"c1"`,
		`"context_management":{"edits":[]}`,
		`"messages":[{"role":"user","content":[{"type":"text","text":"a"},{"type":"audio","source":{}}]}]`,
		`"messages":[{"role":"user","content":[{"type":"document","source":{"type":"url","url":"https://x/a.pdf"}}]}]`,
		`"messages":[{"role":"user","content":[{"type":"image","source":{"type":"file","file_id":"f"}}]}]`,
		`"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"search_result"}]}]}]`,
	}
	for _, field := range refused {
		raw := `{"model":"m",` + field + `}`
		if !strings.Contains(field, `"messages"`) {
			raw = `{` + base + `,` + field + `}`
		}
		if _, err := MessagesRequestToChat([]byte(raw), "m", false); err == nil || !strings.HasPrefix(err.Error(), "unsupported:") {
			t.Errorf("%s must be refused, got %v", field, err)
		}
	}
}

// A document reaches the model, in both directions, and does not vanish.
func TestDocumentBlocksAreCarriedBothWays(t *testing.T) {
	got, err := MessagesRequestToChat([]byte(`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"summarise this"},{"type":"document","title":"spec.pdf","source":{"type":"base64","media_type":"application/pdf","data":"JVBERi0="}},{"type":"document","source":{"type":"text","media_type":"text/plain","data":"plain body"}}]}]}`), "m", false)
	if err != nil {
		t.Fatal(err)
	}
	equalJSON(t, got, `{"model":"m","stream":false,"messages":[{"role":"user","content":[{"type":"text","text":"summarise this"},{"type":"file","file":{"filename":"spec.pdf","file_data":"data:application/pdf;base64,JVBERi0="}},{"type":"text","text":"plain body"}]}]}`)
	var chat map[string]any
	_ = json.Unmarshal(got, &chat)
	back, _, err := ChatRequestToMessages(chat, "m", false, true, false)
	if err != nil || !strings.Contains(string(back), `"type":"document"`) || !strings.Contains(string(back), `"title":"spec.pdf"`) || !strings.Contains(string(back), `"data":"JVBERi0="`) {
		t.Fatalf("the document must survive the round trip: %s %v", back, err)
	}
}

// Every field a Chat request can carry, converted to Messages.
func TestChatRequestFieldsAreCarriedOrRefused(t *testing.T) {
	base := `"model":"m","messages":[{"role":"user","content":"hi"}]`
	carried := map[string]struct{ extra, want string }{
		"max_tokens":            {`"max_tokens":100`, `"max_tokens":100`},
		"max_completion_tokens": {`"max_completion_tokens":100`, `"max_tokens":100`},
		"temperature":           {`"temperature":0.3`, `"temperature":0.3`},
		"top_p":                 {`"top_p":0.5`, `"top_p":0.5`},
		"stop string":           {`"stop":"END"`, `"stop_sequences":["END"]`},
		"stop list":             {`"stop":["A","B"]`, `"stop_sequences":["A","B"]`},
		"user":                  {`"user":"u1"`, `"metadata":{"user_id":"u1"}`},
		"n=1":                   {`"n":1`, `"model":"m"`},
		"tool_choice":           {`"tools":[{"type":"function","function":{"name":"t"}}],"tool_choice":"required"`, `"tool_choice":{"type":"any"}`},
		"parallel false":        {`"tools":[{"type":"function","function":{"name":"t"}}],"parallel_tool_calls":false`, `"disable_parallel_tool_use":true`},
		"response_format":       {`"response_format":{"type":"json_object"}`, `valid JSON`},
	}
	for name, c := range carried {
		body, _ := concerns.WireObject([]byte(`{` + base + `,` + c.extra + `}`))
		got, _, err := ChatRequestToMessages(body, "m", false, true, false)
		if err != nil || !strings.Contains(string(got), c.want) {
			t.Errorf("%s: want %s in %s (%v)", name, c.want, got, err)
		}
	}
	// max_tokens wins over max_completion_tokens when both are set.
	body, _ := concerns.WireObject([]byte(`{` + base + `,"max_tokens":50,"max_completion_tokens":900}`))
	if got, _, _ := ChatRequestToMessages(body, "m", false, true, false); !strings.Contains(string(got), `"max_tokens":50`) {
		t.Errorf("max_tokens must win: %s", got)
	}
	// An empty tools array is not forwarded, and neither is a tool_choice that
	// has no tools to choose from.
	body, _ = concerns.WireObject([]byte(`{` + base + `,"tools":[],"tool_choice":"auto"}`))
	got, _, err := ChatRequestToMessages(body, "m", false, true, false)
	if err != nil || strings.Contains(string(got), `"tools"`) || strings.Contains(string(got), `"tool_choice"`) {
		t.Errorf("empty tools: %s %v", got, err)
	}
	// Not carried, deliberately (PARITY 6.3): Anthropic has no equivalent and none
	// of them changes what is asked.
	for _, field := range []string{`"seed":7`, `"presence_penalty":0.5`, `"frequency_penalty":0.5`, `"logit_bias":{"1":2}`, `"top_logprobs":2`, `"store":true`, `"metadata":{"a":"b"}`, `"prompt_cache_key":"k"`, `"service_tier":"auto"`, `"stream_options":{"include_usage":true}`, `"reasoning_effort":"low"`} {
		body, _ := concerns.WireObject([]byte(`{` + base + `,` + field + `}`))
		got, _, err := ChatRequestToMessages(body, "m", false, true, false)
		key := strings.Split(field, ":")[0]
		if err != nil || (key != `"metadata"` && strings.Contains(string(got), key)) {
			t.Errorf("%s must be accepted and not forwarded: %s %v", field, got, err)
		}
	}
	// Refused.
	for _, field := range []string{
		`"n":2`, `"logprobs":true`, `"modalities":["text","audio"]`, `"audio":{"voice":"x"}`, `"functions":[{"name":"f"}]`,
		`"messages":[{"role":"function","content":"x"}]`,
		`"messages":[{"role":"user","content":[{"type":"input_audio","input_audio":{"data":"x","format":"wav"}}]}]`,
		`"messages":[{"role":"user","content":[{"type":"file","file":{"file_id":"f1"}}]}]`,
		`"messages":[{"role":"user","content":[{"type":"file","file":{"file_data":"data:text/plain;base64,QQ=="}}]}]`,
		`"messages":[{"role":"user","content":[{"type":"image_url","image_url":{"url":"ftp://x/y"}}]}]`,
		`"messages":[{"role":"assistant","tool_calls":[{"id":"c","type":"custom","custom":{"name":"x"}}]}]`,
	} {
		raw := `{"model":"m",` + field + `}`
		if !strings.Contains(field, `"messages"`) {
			raw = `{` + base + `,` + field + `}`
		}
		body, _ := concerns.WireObject([]byte(raw))
		if _, _, err := ChatRequestToMessages(body, "m", false, true, false); err == nil || !strings.HasPrefix(err.Error(), "unsupported:") {
			t.Errorf("%s must be refused, got %v", field, err)
		}
	}
	// A developer message is instructions, not nothing.
	body, _ = concerns.WireObject([]byte(`{"model":"m","messages":[{"role":"developer","content":"obey"},{"role":"user","content":"hi"}]}`))
	if got, _, _ := ChatRequestToMessages(body, "m", false, true, false); !strings.Contains(string(got), `obey`) {
		t.Errorf("developer content vanished: %s", got)
	}
}

// A non-string text block inside a tool_result is JSON, not Go's map syntax.
func TestToolResultNestedObjectIsJSON(t *testing.T) {
	got, err := MessagesRequestToChat([]byte(`{"model":"m","messages":[{"role":"user","content":"a"},{"role":"assistant","content":[{"type":"tool_use","id":"t","name":"x","input":{}}]},{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":[{"type":"text","text":{"k":[1,2]}}]}]}]}`), "m", false)
	if err != nil || !strings.Contains(string(got), `{\"k\":[1,2]}`) || strings.Contains(string(got), "map[") {
		t.Fatalf("%s %v", got, err)
	}
}

// What the leg sends, the receiving side must have agreed to. No ttl without the
// beta header that grants it, no identity the caller did not write, no thinking
// block Anthropic did not sign, and no sampling pair current models reject.
func TestChatRequestToMessagesSendsNothingTheUpstreamHasNotAgreedTo(t *testing.T) {
	body, _ := concerns.WireObject([]byte(`{"model":"m","temperature":0.2,"top_p":0.9,
	  "messages":[{"role":"user","content":"hi"},
	   {"role":"assistant","reasoning_content":"unsigned","content":[{"type":"text","text":"a"},{"type":"thinking","thinking":"also unsigned"},{"type":"thinking","thinking":"signed","signature":"sig"}]},
	   {"role":"user","content":"more"}],
	  "tools":[{"type":"function","function":{"name":"t"}}]}`))
	got, _, err := ChatRequestToMessages(body, "m", false, true, false)
	if err != nil {
		t.Fatal(err)
	}
	text := string(got)
	for _, forbidden := range []string{`"ttl"`, "Claude Code", "unsigned", `"top_p"`} {
		if strings.Contains(text, forbidden) {
			t.Errorf("%s must not be sent: %s", forbidden, text)
		}
	}
	if strings.Contains(text, `"system"`) || !strings.Contains(text, `"signature":"sig"`) {
		t.Errorf("no system block without caller system text, and a signed thinking block is kept: %s", text)
	}
	body, _ = concerns.WireObject([]byte(`{"model":"m","top_p":0.9,"messages":[{"role":"user","content":"hi"}]}`))
	if got, _, _ := ChatRequestToMessages(body, "m", false, true, false); !strings.Contains(string(got), `"top_p":0.9`) {
		t.Errorf("top_p alone is carried: %s", got)
	}
}

func roles(t *testing.T, encoded []byte) []string {
	t.Helper()
	var result map[string]any
	if err := json.Unmarshal(encoded, &result); err != nil {
		t.Fatal(err)
	}
	var list []string
	for _, message := range concerns.Items(result["messages"]) {
		list = append(list, concerns.Text(concerns.Object(message)["role"]))
	}
	return list
}

// Anthropic rejects consecutive turns of one role. Parallel tool results,
// a result followed by text, and a split assistant turn all have to merge into
// one turn, with the tool_result blocks first.
func TestChatRequestToMessagesAlternatesRoles(t *testing.T) {
	calls2 := `{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"x","arguments":"{}"}},{"id":"b","type":"function","function":{"name":"y","arguments":"{}"}}]}`
	call1 := `{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"x","arguments":"{}"}}]}`
	cases := map[string]string{
		"parallel results":   `[{"role":"user","content":"q"},` + calls2 + `,{"role":"tool","tool_call_id":"a","content":"ra"},{"role":"tool","tool_call_id":"b","content":"rb"}]`,
		"result then text":   `[{"role":"user","content":"q"},` + call1 + `,{"role":"tool","tool_call_id":"a","content":"ra"},{"role":"user","content":"next"}]`,
		"parallel then text": `[{"role":"user","content":"q"},` + calls2 + `,{"role":"tool","tool_call_id":"a","content":"ra"},{"role":"tool","tool_call_id":"b","content":"rb"},{"role":"user","content":"next"}]`,
		"split assistant":    `[{"role":"user","content":"q"},{"role":"assistant","content":"thinking aloud"},` + call1 + `,{"role":"tool","tool_call_id":"a","content":"ra"}]`,
		"user user":          `[{"role":"user","content":"a"},{"role":"user","content":"b"}]`,
		"two rounds":         `[{"role":"user","content":"q"},` + call1 + `,{"role":"tool","tool_call_id":"a","content":"ra"},{"role":"assistant","content":"mid"},{"role":"user","content":"again"}]`,
	}
	for name, messages := range cases {
		body, _ := concerns.WireObject([]byte(`{"model":"m","messages":` + messages + `}`))
		got, _, err := ChatRequestToMessages(body, "m", false, true, false)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		list := roles(t, got)
		for i := 1; i < len(list); i++ {
			if list[i] == list[i-1] {
				t.Errorf("%s: consecutive %s turns: %v\n%s", name, list[i], list, got)
			}
		}
		// Every tool_result sits before any other block of its turn.
		var result map[string]any
		_ = json.Unmarshal(got, &result)
		for _, message := range concerns.Items(result["messages"]) {
			seenOther := false
			for _, block := range concerns.Items(concerns.Object(message)["content"]) {
				if concerns.Text(concerns.Object(block)["type"]) == "tool_result" {
					if seenOther {
						t.Errorf("%s: a tool_result follows other content: %s", name, got)
					}
				} else {
					seenOther = true
				}
			}
		}
	}
	body, _ := concerns.WireObject([]byte(`{"model":"m","messages":` + cases["parallel then text"] + `}`))
	got, _, _ := ChatRequestToMessages(body, "m", false, true, false)
	text := string(got)
	if strings.Count(text, `"tool_result"`) != 2 || !strings.Contains(text, `"text":"next"`) {
		t.Fatalf("both results and the text must survive: %s", text)
	}
}

// A tool_choice with no answer is refused, never defaulted to a permissive
// "auto": "none" must stay none, and an unknown choice must not grant tools.
func TestToolChoiceHasNoPermissiveDefault(t *testing.T) {
	for _, choice := range []string{`{"type":"function"}`, `{"type":"allowed_tools"}`, `"whatever"`, `{"type":"tool"}`} {
		body, _ := concerns.WireObject([]byte(`{"model":"m","messages":[{"role":"user","content":"x"}],"tools":[{"type":"function","function":{"name":"t"}}],"tool_choice":` + choice + `}`))
		if _, _, err := ChatRequestToMessages(body, "m", false, true, false); err == nil {
			t.Errorf("Chat tool_choice %s must be refused", choice)
		}
		if _, err := MessagesRequestToChat([]byte(`{"model":"m","messages":[{"role":"user","content":"x"}],"tools":[{"name":"t"}],"tool_choice":`+choice+`}`), "m", false); err == nil {
			t.Errorf("Messages tool_choice %s must be refused", choice)
		}
	}
	for _, role := range []string{"developer", "robot"} {
		if _, err := MessagesRequestToChat([]byte(`{"model":"m","messages":[{"role":"`+role+`","content":"x"}]}`), "m", false); err == nil {
			t.Errorf("Messages role %q must not become the assistant", role)
		}
	}
	// A choice with no tools is not forwarded, matching the documented table.
	got, err := MessagesRequestToChat([]byte(`{"model":"m","messages":[{"role":"user","content":"x"}],"tools":[],"tool_choice":{"type":"any"}}`), "m", false)
	if err != nil || strings.Contains(string(got), "tool_choice") || strings.Contains(string(got), `"tools"`) {
		t.Errorf("%s %v", got, err)
	}
}

// A Chat tool message's part array becomes what a tool_result accepts.
func TestToolMessagePartsBecomeToolResultBlocks(t *testing.T) {
	body, _ := concerns.WireObject([]byte(`{"model":"m","messages":[{"role":"user","content":"q"},{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"x","arguments":"{}"}}]},{"role":"tool","tool_call_id":"a","content":[{"type":"text","text":"out"},{"type":"image_url","image_url":{"url":"data:image/png;base64,AAAA"}}]}]}`))
	got, _, err := ChatRequestToMessages(body, "m", false, true, false)
	if err != nil || strings.Contains(string(got), "image_url") || !strings.Contains(string(got), `"type":"image"`) || !strings.Contains(string(got), `"text":"out"`) {
		t.Fatalf("%s %v", got, err)
	}
	body, _ = concerns.WireObject([]byte(`{"model":"m","messages":[{"role":"user","content":"q"},{"role":"assistant","tool_calls":[{"id":"a","type":"function","function":{"name":"x","arguments":"{}"}}]},{"role":"tool","tool_call_id":"a","content":[{"type":"input_audio"}]}]}`))
	if _, _, err := ChatRequestToMessages(body, "m", false, true, false); err == nil {
		t.Fatal("an unsupported part in a tool message must be refused")
	}
}
