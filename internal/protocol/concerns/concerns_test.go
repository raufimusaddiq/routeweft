package concerns

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func decode(t *testing.T, text string) map[string]any {
	t.Helper()
	value, err := WireObject([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return value
}

func encode(t *testing.T, value any) string {
	t.Helper()
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

// Each row is one case of translator/concerns/finishReason.js at the pin.
func TestFinishReasonTables(t *testing.T) {
	toOpenAI := []struct{ format, reason, want string }{
		{"claude", "end_turn", "stop"}, {"claude", "max_tokens", "length"}, {"claude", "tool_use", "tool_calls"},
		{"claude", "stop_sequence", "stop"}, {"claude", "refusal", "content_filter"}, {"claude", "surprise", "stop"},
		{"commandcode", "stop", "stop"}, {"commandcode", "length", "length"}, {"commandcode", "tool-calls", "tool_calls"},
		{"commandcode", "tool_use", "tool_calls"}, {"commandcode", "content-filter", "content_filter"},
		{"commandcode", "error", "stop"}, {"commandcode", "other", "other"}, {"commandcode", "", "stop"},
		{"gemini", "STOP", "stop"}, {"gemini", "max_tokens", "length"}, {"gemini", "SAFETY", "content_filter"},
		{"gemini", "recitation", "content_filter"}, {"gemini", "BLOCKLIST", "content_filter"},
		{"gemini", "PROHIBITED_CONTENT", "content_filter"}, {"gemini", "OTHER", "stop"},
		{"kiro", "tool_calls", "tool_calls"}, {"kiro", "tool_use", "tool_calls"}, {"kiro", "length", "length"},
		{"ollama", "max_tokens", "length"}, {"ollama", "anything", "stop"},
		{"openai", "length", "length"}, {"openai", "", "stop"},
	}
	for _, c := range toOpenAI {
		if got := ToOpenAIFinish(c.reason, c.format); got != c.want {
			t.Errorf("ToOpenAIFinish(%q,%q)=%q want %q", c.reason, c.format, got, c.want)
		}
	}
	fromOpenAI := []struct{ format, reason, want string }{
		{"claude", "stop", "end_turn"}, {"claude", "length", "max_tokens"}, {"claude", "tool_calls", "tool_use"},
		{"claude", "content_filter", "refusal"}, {"claude", "function_call", "end_turn"}, {"claude", "", "end_turn"},
		{"gemini", "stop", "stop"},
	}
	for _, c := range fromOpenAI {
		if got := FromOpenAIFinish(c.reason, c.format); got != c.want {
			t.Errorf("FromOpenAIFinish(%q,%q)=%q want %q", c.reason, c.format, got, c.want)
		}
	}
}

// Every Claude stop reason must come back the same after a round trip, except
// stop_sequence which the pin collapses into end_turn.
func TestClaudeFinishReasonRoundTrip(t *testing.T) {
	for _, reason := range []string{"end_turn", "max_tokens", "tool_use", "refusal"} {
		if got := FromOpenAIFinish(ToOpenAIFinish(reason, "claude"), "claude"); got != reason {
			t.Errorf("%s round-tripped to %s", reason, got)
		}
	}
	if got := FromOpenAIFinish(ToOpenAIFinish("stop_sequence", "claude"), "claude"); got != "end_turn" {
		t.Errorf("stop_sequence round-tripped to %s", got)
	}
}

func TestEnsureToolCallIDsRepairsBothHalvesOfACall(t *testing.T) {
	body := decode(t, `{"messages":[
		{"role":"assistant","tool_calls":[{"id":"call.1|x","function":{"name":"lookup","arguments":{"q":"a"}}},{"function":{"name":"my.tool"}}]},
		{"role":"tool","tool_call_id":"call.1|x","content":"r"},
		{"role":"assistant","content":[{"type":"tool_use","id":"toolu 9","name":"w"}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu 9","content":"ok"},{"type":"tool_result","tool_use_id":"!!!","content":"x"}]}]}`)
	EnsureToolCallIDs(body)
	messages := Items(body["messages"])
	calls := Items(Object(messages[0])["tool_calls"])
	first, second := Object(calls[0]), Object(calls[1])
	if first["id"] != "call1x" || Object(messages[1])["tool_call_id"] != "call1x" {
		t.Fatalf("a stripped id must still match its result: %v / %v", first["id"], Object(messages[1])["tool_call_id"])
	}
	if second["id"] != "call_msg0_tc1_mytool" || second["type"] != "function" {
		t.Fatalf("a missing id is generated from position and name: %v", second)
	}
	if Object(first["function"])["arguments"] != `{"q":"a"}` {
		t.Fatalf("object arguments must become a JSON string: %v", Object(first["function"])["arguments"])
	}
	use := Object(Items(Object(messages[2])["content"])[0])
	results := Items(Object(messages[3])["content"])
	if use["id"] != "toolu9" || Object(results[0])["tool_use_id"] != "toolu9" {
		t.Fatalf("tool_use and tool_result ids must stay paired: %v %v", use["id"], Object(results[0])["tool_use_id"])
	}
	if Object(results[1])["tool_use_id"] != "call_msg3_tc1" {
		t.Fatalf("an unusable result id is regenerated: %v", Object(results[1])["tool_use_id"])
	}
}

func TestFixMissingToolResponses(t *testing.T) {
	body := decode(t, `{"messages":[
		{"role":"assistant","tool_calls":[{"id":"a"},{"id":"b"}]},
		{"role":"user","content":"hi"},
		{"role":"assistant","content":[{"type":"tool_use","id":"c"}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"c","content":"ok"}]},
		{"role":"assistant","tool_calls":[{"id":"d"}]}]}`)
	FixMissingToolResponses(body)
	var roles []string
	for _, raw := range Items(body["messages"]) {
		roles = append(roles, Text(Object(raw)["role"]))
	}
	want := []string{"assistant", "tool", "tool", "user", "assistant", "user", "assistant"}
	if !reflect.DeepEqual(roles, want) {
		t.Fatalf("roles %v want %v", roles, want)
	}
	inserted := Object(Items(body["messages"])[1])
	if inserted["tool_call_id"] != "a" || inserted["content"] != "" {
		t.Fatalf("inserted reply %v", inserted)
	}
}

func TestToolNameSanitizerAvoidsCollisionsAndMapsBack(t *testing.T) {
	names := NewToolNames([]string{"srv_tool", "srv.tool", "plain"})
	if got := names.Safe("plain"); got != "plain" {
		t.Fatalf("valid names are untouched: %q", got)
	}
	first := names.Safe("srv.tool")
	if first != "srv_tool_2" {
		t.Fatalf("srv.tool must not collide with the real srv_tool: %q", first)
	}
	if again := names.Safe("srv.tool"); again != first {
		t.Fatalf("the rewrite must be stable: %q", again)
	}
	if names.Original(first) != "srv.tool" || names.Original("plain") != "plain" || !names.Rewritten() {
		t.Fatal("rewritten names must map back and others pass through")
	}
	if got := NewToolNames(nil).Safe("..."); got != "___" {
		t.Fatalf("got %q", got)
	}
	long := NewToolNames(nil).Safe(strings.Repeat("a.", 100))
	if len(long) != 128 {
		t.Fatalf("length %d", len(long))
	}
}

func TestChatToolNamesCollectsDeclarationsHistoryAndChoice(t *testing.T) {
	body := decode(t, `{"tools":[{"type":"function","function":{"name":" a.b "}},{"name":"c"}],
		"messages":[{"role":"assistant","tool_calls":[{"function":{"name":"d"}}]}],"tool_choice":{"function":{"name":"e"}}}`)
	if got := ChatToolNames(body); !reflect.DeepEqual(got, []string{"a.b", "c", "d", "e"}) {
		t.Fatalf("got %v", got)
	}
}

func TestReasoningTextAcrossVendorShapes(t *testing.T) {
	cases := map[string]string{
		`{"reasoning_content":"a"}`: "a",
		`{"reasoning":"b"}`:         "b",
		`{"reasoning_details":[{"text":"c"},"d",{"content":"e"},{}]}`: "cde",
		`{"reasoning_content":"","reasoning":"f"}`:                    "f",
		`{"content":"x"}`: "",
	}
	for input, want := range cases {
		if got := ReasoningText(decode(t, input)); got != want {
			t.Errorf("%s -> %q want %q", input, got, want)
		}
	}
}

// The hub convention: the prompt includes cache, and Anthropic's separate cache
// fields fold in on the way in and are subtracted on the way out.
func TestClaudeUsageConvention(t *testing.T) {
	raw := decode(t, `{"input_tokens":10,"output_tokens":5,"cache_read_input_tokens":100,"cache_creation_input_tokens":20}`)
	usage := ClaudeUsage(raw)
	want := `{"completion_tokens":5,"prompt_tokens":130,"prompt_tokens_details":{"cache_creation_tokens":20,"cached_tokens":100},"total_tokens":135}`
	if got := encode(t, usage.OpenAI()); got != want {
		t.Fatalf("got  %s\nwant %s", got, want)
	}
	back := ClaudeUsageFromOpenAI(usage.OpenAI())
	if encode(t, back) != encode(t, raw) {
		t.Fatalf("round trip %s want %s", encode(t, back), encode(t, raw))
	}
	plain := ClaudeUsage(decode(t, `{"input_tokens":3,"output_tokens":4}`)).OpenAI()
	if encode(t, plain) != `{"completion_tokens":4,"prompt_tokens":3,"total_tokens":7}` {
		t.Fatalf("zero cache adds no details: %s", encode(t, plain))
	}
}

// Reasoning is a subset of completion, never added on top (usage.js:10-12,
// openai-to-claude.js:108).
func TestReasoningTokensStayInsideCompletion(t *testing.T) {
	usage := Usage{Prompt: 10, Completion: 30, Total: 40, Reasoning: 12}.OpenAI()
	if encode(t, usage) != `{"completion_tokens":30,"completion_tokens_details":{"reasoning_tokens":12},"prompt_tokens":10,"total_tokens":40}` {
		t.Fatal(encode(t, usage))
	}
	if got := ClaudeUsageFromOpenAI(usage); Int(got["output_tokens"]) != 30 {
		t.Fatalf("output must stay the reported completion: %v", got)
	}
}

func TestDataURIAndExtractText(t *testing.T) {
	if mediaType, data, ok := ParseDataURI("data:image/png;base64,AAA\nBBB"); !ok || mediaType != "image/png" || data != "AAA\nBBB" {
		t.Fatalf("%q %q %v", mediaType, data, ok)
	}
	if _, _, ok := ParseDataURI("https://x/y.png"); ok {
		t.Fatal("a URL is not a data URI")
	}
	parts := []any{map[string]any{"type": "text", "text": "a"}, map[string]any{"type": "image_url"}, map[string]any{"type": "text"}}
	if got := ExtractText(parts, "\n"); got != "a\n" {
		t.Fatalf("%q", got)
	}
}

// An id-less call gets an id, and an id-less result answers the oldest call
// still waiting, in both shapes alike.
func TestEnsureToolCallIDsFillsAbsentIDs(t *testing.T) {
	chat := decode(t, `{"messages":[
		{"role":"assistant","tool_calls":[{"function":{"name":"a"}},{"function":{"name":"b"}}]},
		{"role":"tool","content":"ra"},{"role":"tool","content":"rb"}]}`)
	messages := Items(chat["messages"])
	EnsureToolCallIDs(chat)
	calls := Items(Object(messages[0])["tool_calls"])
	first, second := Text(Object(calls[0])["id"]), Text(Object(calls[1])["id"])
	if first == "" || second == "" || first == second || Object(messages[1])["tool_call_id"] != first || Object(messages[2])["tool_call_id"] != second {
		t.Fatalf("chat shape: %v %v %v %v", first, second, Object(messages[1])["tool_call_id"], Object(messages[2])["tool_call_id"])
	}
	claude := decode(t, `{"messages":[
		{"role":"assistant","content":[{"type":"tool_use","name":"a","input":{}},{"type":"tool_use","name":"b","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","content":"ra"},{"type":"tool_result","content":"rb"}]}]}`)
	messages = Items(claude["messages"])
	EnsureToolCallIDs(claude)
	uses, results := Items(Object(messages[0])["content"]), Items(Object(messages[1])["content"])
	if Text(Object(uses[0])["id"]) == "" || Object(results[0])["tool_use_id"] != Object(uses[0])["id"] || Object(results[1])["tool_use_id"] != Object(uses[1])["id"] {
		t.Fatalf("messages shape: %v %v", uses, results)
	}
	FixMissingToolResponses(claude)
	if len(Items(claude["messages"])) != 2 {
		t.Fatalf("every call is answered, so nothing is inserted: %v", claude["messages"])
	}
}

// A non-string id is spelled once, as a string, so the call, the pairing set and
// the result can never hold different values.
func TestNonStringToolIDsPairAsStrings(t *testing.T) {
	chat := decode(t, `{"messages":[{"role":"assistant","tool_calls":[{"id":123,"function":{"name":"a"}}]},{"role":"tool","content":"r"}]}`)
	EnsureToolCallIDs(chat)
	messages := Items(chat["messages"])
	call := Object(Items(Object(messages[0])["tool_calls"])[0])
	if call["id"] != "123" || Object(messages[1])["tool_call_id"] != "123" {
		t.Fatalf("chat shape: %#v / %#v", call["id"], Object(messages[1])["tool_call_id"])
	}
	claude := decode(t, `{"messages":[{"role":"assistant","content":[{"type":"tool_use","id":4.5,"name":"a","input":{}}]},{"role":"user","content":[{"type":"tool_result","content":"r"}]}]}`)
	EnsureToolCallIDs(claude)
	messages = Items(claude["messages"])
	use := Object(Items(Object(messages[0])["content"])[0])
	result := Object(Items(Object(messages[1])["content"])[0])
	if _, isText := use["id"].(string); !isText || use["id"] == "" || result["tool_use_id"] != use["id"] {
		t.Fatalf("messages shape: %#v / %#v", use["id"], result["tool_use_id"])
	}
}

func TestResolveFinish(t *testing.T) {
	cases := []struct {
		reason string
		calls  int
		force  bool
		want   string
		fail   bool
	}{
		{"", 0, false, "stop", false}, {"", 2, false, "tool_calls", false},
		{"stop", 2, false, "stop", false}, {"stop", 2, true, "tool_calls", false},
		{"tool_calls", 1, false, "tool_calls", false}, {"tool_calls", 0, false, "", true},
		{"tool_calls", 0, true, "", true}, {"length", 0, true, "length", false},
	}
	for _, c := range cases {
		got, err := ResolveFinish(c.reason, c.calls, c.force)
		if got != c.want || (err != nil) != c.fail {
			t.Errorf("ResolveFinish(%q,%d,%v)=%q,%v", c.reason, c.calls, c.force, got, err)
		}
	}
}

// The SSE reader bounds one event, not just the whole stream.
func TestReadEventsBoundsOneEvent(t *testing.T) {
	huge := "data: " + strings.Repeat("x", 5<<20) + "\n\n"
	err := ReadEvents(context.Background(), strings.NewReader(huge), func(string, map[string]any) (bool, error) { return false, nil })
	if err == nil {
		t.Fatal("a single 5 MiB line must be refused")
	}
	piece := "data: {\"a\":\"" + strings.Repeat("y", 3<<20) + "\n"
	err = ReadEvents(context.Background(), strings.NewReader(piece+piece+"\n"), func(string, map[string]any) (bool, error) { return false, nil })
	if err == nil {
		t.Fatal("an event built of several lines must be bounded as a whole")
	}
}
