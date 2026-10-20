package kiro

import (
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

func messagesWith(t *testing.T, body string, options RequestOptions) (map[string]any, *concerns.ToolNames, RequestInfo, error) {
	t.Helper()
	parsed, err := concerns.WireObject([]byte(body))
	if err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	if options.Model == "" {
		options.Model = "claude-sonnet-4.5"
	}
	options.NewConversationID = fixedID
	out, names, info, err := MessagesRequestToKiro(parsed, options)
	if err != nil {
		return nil, nil, info, err
	}
	result, err := concerns.WireObject(out)
	if err != nil {
		t.Fatal(err)
	}
	return result, names, info, nil
}

func messages(t *testing.T, body string) map[string]any {
	t.Helper()
	result, _, _, err := messagesWith(t, body, RequestOptions{})
	if err != nil {
		t.Fatalf("%s: %v", body, err)
	}
	return result
}

func messagesRefused(t *testing.T, body string) string {
	t.Helper()
	_, _, _, err := messagesWith(t, body, RequestOptions{})
	if err == nil {
		t.Fatalf("expected a refusal: %s", body)
	}
	if !strings.Contains(err.Error(), "unsupported: ") {
		t.Fatalf("not an unsupported error: %v", err)
	}
	return err.Error()
}

const userMessage = `{"role":"user","content":"hi"}`

func ask(fields string) string {
	return `{"messages":[` + userMessage + `]` + fields + `}`
}

func TestMessagesBasicConversation(t *testing.T) {
	out := messages(t, `{"model":"m","max_tokens":256,"temperature":0,"top_p":0.5,"stream":true,"system":"be brief","messages":[
		{"role":"user","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]},
		{"role":"assistant","content":[{"type":"text","text":"x"},{"type":"text","text":"y"}]},
		{"role":"user","content":"c"}]}`)
	state := concerns.Object(out["conversationState"])
	if state["conversationId"] != "conversation-1" || state["chatTriggerType"] != "MANUAL" {
		t.Fatalf("%v", state)
	}
	// The system text goes in front of the first user turn, as the pin does
	// (claude-to-kiro.js:257-270), and is not wrapped in the Chat leg's tags.
	if got := at(state, "history.0.userInputMessage.content"); got != "be brief\n\na\n\nb" {
		t.Fatalf("first turn %q", got)
	}
	if got := at(state, "history.1.assistantResponseMessage.content"); got != "x\ny" {
		t.Fatalf("assistant %q", got)
	}
	if got := at(state, "currentMessage.userInputMessage.content"); got != "c" {
		t.Fatalf("current %q", got)
	}
	if at(state, "currentMessage.userInputMessage.origin") != "AI_EDITOR" || at(state, "currentMessage.userInputMessage.modelId") != "claude-sonnet-4.5" {
		t.Fatalf("%v", state["currentMessage"])
	}
	config := concerns.Object(out["inferenceConfig"])
	if len(config) != 3 || concerns.JSONText(config["maxTokens"]) != "256" || concerns.JSONText(config["temperature"]) != "0" || concerns.JSONText(config["topP"]) != "0.5" {
		t.Fatalf("%v", config)
	}
	if strings.Contains(concerns.JSONText(out), "Current time") {
		t.Fatalf("the pin's clock line is not sent: %v", out)
	}
}

func TestMessagesSystemForms(t *testing.T) {
	out := messages(t, `{"system":[{"type":"text","text":"one","cache_control":{"type":"ephemeral","ttl":"1h"}},{"type":"text","text":"two"}],"messages":[`+userMessage+`]}`)
	if got := at(out, "conversationState.currentMessage.userInputMessage.content"); got != "one\ntwo\n\nhi" {
		t.Fatalf("%q", got)
	}
	for name, system := range map[string]string{
		"image block":   `[{"type":"image"}]`,
		"number":        `5`,
		"non-text":      `[{"type":"text","text":5}]`,
		"citations":     `[{"type":"text","text":"x","citations":[]}]`,
		"bad cache":     `[{"type":"text","text":"x","cache_control":{"type":"persistent"}}]`,
		"bad cache ttl": `[{"type":"text","text":"x","cache_control":{"type":"ephemeral","ttl":"2h"}}]`,
	} {
		t.Run(name, func(t *testing.T) { messagesRefused(t, ask(`,"system":`+system)) })
	}
}

func TestMessagesToolRoundTrip(t *testing.T) {
	out, names, _, err := messagesWith(t, `{"max_tokens":10,"tools":[
		{"name":"a.b","description":"d","input_schema":{"type":"object","properties":{"x":{"type":"string"}},"required":["x"]},"cache_control":{"type":"ephemeral"}},
		{"name":"plain","input_schema":{"type":"object"}}],
		"tool_choice":{"type":"auto"},
		"messages":[{"role":"user","content":"go"},
		{"role":"assistant","content":[{"type":"text","text":"calling"},{"type":"tool_use","id":"toolu_1","name":"a.b","input":{"x":"1"}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"toolu_1","content":[{"type":"text","text":"r1"},{"type":"text","text":"r2"}]},{"type":"text","text":"thanks"}]}]}`, RequestOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if at(out, "conversationState.history.1.assistantResponseMessage.toolUses.0.name") != "a_b" || at(out, "conversationState.history.1.assistantResponseMessage.toolUses.0.toolUseId") != "toolu_1" {
		t.Fatalf("%v", out)
	}
	current := "conversationState.currentMessage.userInputMessage."
	if at(out, current+"content") != "thanks" || at(out, current+"userInputMessageContext.toolResults.0.content.0.text") != "r1\nr2" || at(out, current+"userInputMessageContext.toolResults.0.status") != "success" {
		t.Fatalf("%v", out)
	}
	if at(out, current+"userInputMessageContext.tools.0.toolSpecification.name") != "a_b" || at(out, current+"userInputMessageContext.tools.1.toolSpecification.description") != "Tool: plain" {
		t.Fatalf("%v", out)
	}
	if names.Original("a_b") != "a.b" {
		t.Fatalf("the name is not put back: %v", names)
	}
	// A turn that only answers tools carries the placeholder text.
	out = messages(t, `{"tools":[{"name":"t","input_schema":{}}],"messages":[`+userMessage+`,
		{"role":"assistant","content":[{"type":"tool_use","id":"u","name":"t","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"u","content":"boom","is_error":true}]}]}`)
	if at(out, current+"content") != "Tool results provided." || at(out, current+"userInputMessageContext.toolResults.0.status") != "error" || at(out, "conversationState.history.1.assistantResponseMessage.content") != "..." {
		t.Fatalf("%v", out)
	}
}

func TestMessagesImages(t *testing.T) {
	out := messages(t, `{"messages":[{"role":"user","content":[{"type":"text","text":"look"},{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGk="}}]}]}`)
	if at(out, "conversationState.currentMessage.userInputMessage.images.0.format") != "png" || at(out, "conversationState.currentMessage.userInputMessage.images.0.source.bytes") != "aGk=" {
		t.Fatalf("%v", out)
	}
	for name, block := range map[string]string{
		"url":              `{"type":"image","source":{"type":"url","url":"https://x/y.png"}}`,
		"file":             `{"type":"image","source":{"type":"file","file_id":"f"}}`,
		"not an image":     `{"type":"image","source":{"type":"base64","media_type":"application/pdf","data":"aGk="}}`,
		"no media type":    `{"type":"image","source":{"type":"base64","data":"aGk="}}`,
		"bad base64":       `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"!!"}}`,
		"extra source key": `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGk=","x":1}}`,
		"document":         `{"type":"document","source":{"type":"base64","media_type":"application/pdf","data":"aGk="}}`,
		"search result":    `{"type":"search_result","source":"s","title":"t","content":[]}`,
	} {
		t.Run(name, func(t *testing.T) {
			messagesRefused(t, `{"messages":[{"role":"user","content":[{"type":"text","text":"hi"},`+block+`]}]}`)
		})
	}
	// An image alone is not a turn Kiro can answer, and the pin invents text for it.
	messagesRefused(t, `{"messages":[{"role":"user","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGk="}}]}]}`)
	// Nor can an image ride in a tool result (the pin moves it to the user turn).
	messagesRefused(t, `{"messages":[`+userMessage+`,{"role":"assistant","content":[{"type":"tool_use","id":"u","name":"t","input":{}}]},
		{"role":"user","content":[{"type":"tool_result","tool_use_id":"u","content":[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGk="}}]}]}],
		"tools":[{"name":"t","input_schema":{}}]}`)
}

func TestMessagesRefusals(t *testing.T) {
	for name, fields := range map[string]string{
		"top_k":                 `,"top_k":5`,
		"stop_sequences":        `,"stop_sequences":["x"]`,
		"tool_choice any":       `,"tool_choice":{"type":"any"}`,
		"tool_choice tool":      `,"tool_choice":{"type":"tool","name":"t"}`,
		"tool_choice none":      `,"tool_choice":{"type":"none"}`,
		"tool_choice string":    `,"tool_choice":"auto"`,
		"no parallel":           `,"tool_choice":{"type":"auto","disable_parallel_tool_use":true}`,
		"mcp_servers":           `,"mcp_servers":[]`,
		"container":             `,"container":"c"`,
		"context_management":    `,"context_management":{}`,
		"structured output":     `,"output_config":{"format":{"type":"json_schema","schema":{}}}`,
		"effort and format":     `,"output_config":{"effort":"high","format":{}}`,
		"thinking display":      `,"thinking":{"type":"enabled","budget_tokens":2048,"display":"omitted"}`,
		"unknown field":         `,"anything":1`,
		"chat field":            `,"reasoning_effort":"high"`,
		"bad cache":             `,"cache_control":{"type":"x"}`,
		"max_tokens zero":       `,"max_tokens":0`,
		"max_tokens fractional": `,"max_tokens":1.5`,
		"temperature string":    `,"temperature":"hot"`,
		"unknown model suffix":  ``,
	} {
		t.Run(name, func(t *testing.T) {
			if name == "unknown model suffix" {
				if _, _, _, err := messagesWith(t, ask(``), RequestOptions{Model: "m(bogus)"}); err == nil {
					t.Fatal("expected a refusal")
				}
				return
			}
			messagesRefused(t, ask(fields))
		})
	}
	// Identities are accepted: an empty stop list and an explicit auto choice, a
	// cache marker, and the advisory fields.
	messages(t, ask(`,"stop_sequences":[],"tool_choice":{"type":"auto"},"cache_control":{"type":"ephemeral"},"metadata":{"user_id":"u"},"service_tier":"auto","stream":false,"top_k":null`))
}

func TestMessagesBlockRefusals(t *testing.T) {
	assistant := func(block string) string {
		return `{"messages":[` + userMessage + `,{"role":"assistant","content":[` + block + `]},{"role":"user","content":"more"}],"tools":[{"name":"t","input_schema":{}}]}`
	}
	for name, block := range map[string]string{
		"server tool use":      `{"type":"server_tool_use","id":"s","name":"web_search","input":{}}`,
		"web search result":    `{"type":"web_search_tool_result","tool_use_id":"s","content":[]}`,
		"mcp tool use":         `{"type":"mcp_tool_use","id":"s","name":"n","server_name":"x","input":{}}`,
		"input not object":     `{"type":"tool_use","id":"u","name":"t","input":[1]}`,
		"input string":         `{"type":"tool_use","id":"u","name":"t","input":"x"}`,
		"no id":                `{"type":"tool_use","name":"t","input":{}}`,
		"id unusable":          `{"type":"tool_use","id":"!!","name":"t","input":{}}`,
		"caller":               `{"type":"tool_use","id":"u","name":"t","input":{},"caller":{"type":"direct"}}`,
		"undeclared tool":      `{"type":"tool_use","id":"u","name":"other","input":{}}`,
		"thinking junk key":    `{"type":"thinking","thinking":"x","signature":"s","extra":1}`,
		"citations on text":    `{"type":"text","text":"x","citations":[]}`,
		"unanswered call":      `{"type":"tool_use","id":"u","name":"t","input":{}}`,
		"image from the model": `{"type":"image","source":{"type":"base64","media_type":"image/png","data":"aGk="}}`,
	} {
		t.Run(name, func(t *testing.T) { messagesRefused(t, assistant(block)) })
	}
	for name, body := range map[string]string{
		"system role":       `{"messages":[{"role":"system","content":"x"},` + userMessage + `]}`,
		"unknown role":      `{"messages":[{"role":"tool","content":"x"}]}`,
		"message extra key": `{"messages":[{"role":"user","content":"x","name":"n"}]}`,
		"content number":    `{"messages":[{"role":"user","content":5}]}`,
		"block not object":  `{"messages":[{"role":"user","content":["x"]}]}`,
		"empty list":        `{"messages":[]}`,
		"prefill":           `{"messages":[` + userMessage + `,{"role":"assistant","content":"The answer is"}]}`,
		"starts assistant":  `{"messages":[{"role":"assistant","content":"x"},` + userMessage + `]}`,
		"empty user":        `{"messages":[{"role":"user","content":""}]}`,
		"orphan result":     `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"u","content":"x"}]}]}`,
		"result no id":      `{"messages":[{"role":"user","content":[{"type":"tool_result","content":"x"}]}]}`,
		"is_error string":   `{"messages":[{"role":"user","content":[{"type":"text","text":"x"},{"type":"tool_result","tool_use_id":"u","is_error":"yes"}]}]}`,
		"result list junk":  `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"u","content":[{"type":"document"}]}]}]}`,
		"result number":     `{"messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"u","content":5}]}]}`,
	} {
		t.Run(name, func(t *testing.T) { messagesRefused(t, body) })
	}
}

func TestMessagesToolRefusals(t *testing.T) {
	for name, tools := range map[string]string{
		"server tool":     `[{"type":"web_search_20250305","name":"web_search"}]`,
		"computer":        `[{"type":"computer_20250124","name":"computer","display_width_px":1,"display_height_px":1}]`,
		"no name":         `[{"input_schema":{}}]`,
		"twice":           `[{"name":"t","input_schema":{}},{"name":"t","input_schema":{}}]`,
		"strict":          `[{"name":"t","input_schema":{},"strict":true}]`,
		"defer loading":   `[{"name":"t","input_schema":{},"defer_loading":true}]`,
		"examples":        `[{"name":"t","input_schema":{},"input_examples":[{}]}]`,
		"schema not obj":  `[{"name":"t","input_schema":{"type":"array"}}]`,
		"extra props sch": `[{"name":"t","input_schema":{"type":"object","additionalProperties":{"type":"string"}}}]`,
		"cache control":   `[{"name":"t","input_schema":{},"cache_control":{"type":"x"}}]`,
		"collide":         `[{"name":"a.b","input_schema":{}},{"name":"a_b","input_schema":{}}]`,
		"not a list":      `{"name":"t"}`,
		"long desc":       `[{"name":"t","description":"` + strings.Repeat("x", 10238) + `","input_schema":{}}]`,
	} {
		t.Run(name, func(t *testing.T) { messagesRefused(t, ask(`,"tools":`+tools)) })
	}
	_, _, info, err := messagesWith(t, ask(`,"tools":[{"name":"t","strict":false,"input_schema":{"type":"object","additionalProperties":false}}]`), RequestOptions{})
	if err != nil || info.Normalized.AdditionalPropertiesFalse != 1 {
		t.Fatalf("%v %+v", err, info.Normalized)
	}
}

func TestMessagesThinkingBlocksAreAdvisory(t *testing.T) {
	out := messages(t, `{"messages":[`+userMessage+`,
		{"role":"assistant","content":[{"type":"thinking","thinking":"hm","signature":"sig"},{"type":"redacted_thinking","data":"x"},{"type":"text","text":"ok"}]},
		{"role":"user","content":"more"}]}`)
	if got := at(out, "conversationState.history.1.assistantResponseMessage.content"); got != "ok" || strings.Contains(concerns.JSONText(out), "sig") {
		t.Fatalf("%v", out)
	}
}

func TestMessagesThinkingIntent(t *testing.T) {
	prefix := func(out map[string]any) string {
		return concerns.Text(at(out, "conversationState.currentMessage.userInputMessage.content"))
	}
	for name, test := range map[string]struct {
		body, beta string
		model      string
		on         bool
		budget     string
	}{
		"nothing":                {ask(``), "", "claude-sonnet-4.5", false, ""},
		"body thinking":          {ask(`,"thinking":{"type":"enabled","budget_tokens":2048}`), "", "claude-sonnet-4.5", true, "2048"},
		"effort":                 {ask(`,"output_config":{"effort":"low"}`), "", "claude-sonnet-4.5", true, "1024"},
		"header":                 {ask(``), "interleaved-thinking-2025-05-14", "claude-sonnet-4.5", true, "16000"},
		"header among others":    {ask(``), "claude-code-20250219, Interleaved-Thinking-2025-05-14", "claude-sonnet-4.5", true, "16000"},
		"header, other beta":     {ask(``), "claude-code-20250219", "claude-sonnet-4.5", false, ""},
		"disabled beats header":  {ask(`,"thinking":{"type":"disabled"}`), "interleaved-thinking-2025-05-14", "claude-sonnet-4.5", false, ""},
		"budget beats header":    {ask(`,"thinking":{"type":"enabled","budget_tokens":4096}`), "interleaved-thinking-2025-05-14", "claude-sonnet-4.5", true, "4096"},
		"effort beats header":    {ask(`,"output_config":{"effort":"high"}`), "interleaved-thinking-2025-05-14", "claude-sonnet-4.5", true, "24576"},
		"suffix disables header": {ask(``), "interleaved-thinking-2025-05-14", "claude-sonnet-4.5(none)", false, ""},
		"model suffix":           {ask(``), "", "claude-sonnet-4.5-thinking", true, "16000"},
		"tag in system blocks":   {`{"system":[{"type":"text","text":"x"},{"type":"text","text":"<thinking_mode>interleaved</thinking_mode>"}],"messages":[{"role":"user","content":"hi"}]}`, "", "claude-sonnet-4.5", true, "16000"},
		"tag in system string":   {`{"system":"<thinking_mode>enabled</thinking_mode>","messages":[{"role":"user","content":"hi"}]}`, "", "claude-sonnet-4.5", true, "16000"},
		"tag in user text":       {`{"messages":[{"role":"user","content":"<thinking_mode>enabled</thinking_mode> hi"}]}`, "", "claude-sonnet-4.5", true, "16000"},
	} {
		t.Run(name, func(t *testing.T) {
			out, _, info, err := messagesWith(t, test.body, RequestOptions{AnthropicBeta: test.beta, Model: test.model})
			if err != nil {
				t.Fatal(err)
			}
			if info.ThinkingTags != test.on {
				t.Fatalf("ThinkingTags %v, want %v: %s", info.ThinkingTags, test.on, prefix(out))
			}
			if got := strings.Contains(prefix(out), "<thinking_mode>"); got != test.on {
				t.Fatalf("prefix present %v, want %v: %s", got, test.on, prefix(out))
			}
			if test.on && !strings.Contains(prefix(out), "<max_thinking_length>"+test.budget+"</max_thinking_length>") {
				t.Fatalf("budget %s not in %s", test.budget, prefix(out))
			}
		})
	}
	// The header is a request the client made of this gateway; it is never forwarded.
	out, _, _, _ := messagesWith(t, ask(``), RequestOptions{AnthropicBeta: "interleaved-thinking-2025-05-14"})
	if strings.Contains(strings.ToLower(concerns.JSONText(out)), "anthropic-beta") {
		t.Fatalf("%v", out)
	}
	// The Chat leg does not read it: a Chat client has no such header, and a
	// caller that sets the option on a Chat conversion gets no thinking.
	chat, _, _, err := convertWith(t, wrap(``), RequestOptions{AnthropicBeta: "interleaved-thinking-2025-05-14"})
	if err != nil || strings.Contains(concerns.JSONText(chat), "<thinking_mode>") {
		t.Fatalf("%v %v", err, chat)
	}
}

func TestMessagesSystemOrderWithThinkingAndAgentic(t *testing.T) {
	out, _, _, err := messagesWith(t, ask(`,"system":"SYS","thinking":{"type":"enabled","budget_tokens":2048}`), RequestOptions{Model: "claude-sonnet-4.5-agentic"})
	if err != nil {
		t.Fatal(err)
	}
	content := concerns.Text(at(out, "conversationState.currentMessage.userInputMessage.content"))
	thinking, agentic, system, hi := strings.Index(content, "<thinking_mode>"), strings.Index(content, "chunk"), strings.Index(content, "SYS"), strings.LastIndex(content, "\n\nhi")
	if thinking != 0 || agentic < 0 || !(thinking < agentic && agentic < system && system < hi) {
		t.Fatalf("order %d %d %d %d in %q", thinking, agentic, system, hi, content)
	}
}

// Nothing is kept between requests: each gets its own conversation, so no client's
// conversation can be continued by another, and there is no signature to scope.
func TestMessagesKeepNoStateBetweenRequests(t *testing.T) {
	body := ask(`,"system":"same","thinking":{"type":"enabled","budget_tokens":2048}`)
	parsed, _ := concerns.WireObject([]byte(body))
	ids := map[string]bool{}
	for i := 0; i < 5; i++ {
		out, _, _, err := MessagesRequestToKiro(parsed, RequestOptions{Model: "claude-sonnet-4.5"})
		if err != nil {
			t.Fatal(err)
		}
		decoded, _ := concerns.WireObject(out)
		id := concerns.Text(at(decoded, "conversationState.conversationId"))
		if id == "" || ids[id] {
			t.Fatalf("conversation id %q repeats", id)
		}
		ids[id] = true
	}
	// The request is not mutated: a second conversion of the same body is the same.
	first, _, _, _ := MessagesRequestToKiro(parsed, RequestOptions{Model: "m", NewConversationID: fixedID})
	second, _, _, _ := MessagesRequestToKiro(parsed, RequestOptions{Model: "m", NewConversationID: fixedID})
	if string(first) != string(second) {
		t.Fatalf("%s\n%s", first, second)
	}
}
