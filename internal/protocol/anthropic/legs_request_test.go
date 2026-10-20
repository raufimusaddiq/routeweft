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
// MCP-style tool names are rewritten for the upstream. A developer message is
// dropped: the pin's block builder handles only tool, user and assistant
// (openai-to-claude.js:204-299) and only system feeds the system prompt.
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
	    {"type":"text","text":"You are Claude Code, Anthropic's official CLI for Claude."},
	    {"type":"text","text":"Be brief.\nYou must respond with valid JSON. Respond ONLY with a JSON object, no other text.","cache_control":{"type":"ephemeral","ttl":"1h"}}
	  ],
	  "messages":[
	    {"role":"user","content":[{"type":"text","text":"hi"},{"type":"text","text":"again"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"AAAA"}},{"type":"image","source":{"type":"url","url":"https://example.invalid/x.png"}}]},
	    {"role":"assistant","content":[{"type":"thinking","thinking":"plan"},{"type":"text","text":"calling"},{"type":"tool_use","id":"call_1","name":"srv_lookup","input":{"q":"x"}}]},
	    {"role":"user","content":[{"type":"tool_result","tool_use_id":"call_1","content":"result"}]},
	    {"role":"assistant","content":[{"type":"text","text":"done","cache_control":{"type":"ephemeral"}}]}
	  ],
	  "tools":[{"name":"srv_lookup","description":"Look up","input_schema":{"type":"object","properties":{"q":{"type":"string"}}},"cache_control":{"type":"ephemeral","ttl":"1h"}}],
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
		`"whatever"`:                   `{"type":"auto"}`,
		`{"type":"any"}`:               `{"type":"any"}`,
		`{"type":"function"}`:          `{"type":"auto"}`,
		`{"type":"tool","name":"a.b"}`: `{"type":"tool","name":"a_b"}`,
		`{"function":{"name":"a.b"}}`:  `{"type":"tool","name":"a_b"}`,
	}
	for choice, want := range cases {
		body, _ := concerns.WireObject([]byte(`{"model":"m","messages":[{"role":"user","content":"x"}],"tool_choice":` + choice + `}`))
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
