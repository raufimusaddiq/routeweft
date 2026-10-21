package wire

import (
	"reflect"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

func grokOut(t *testing.T, body string) map[string]any {
	t.Helper()
	return decodeBody(t, finalize(t, "grok-cli", responses, body))
}

const (
	nativeReasoning = "rs_6f1d1c1e-0a52-4b8e-9f3a-5d2c7b8a9e10"
	nativeMessage   = "msg_6f1d1c1e-0a52-4b8e-9f3a-5d2c7b8a9e11"
)

func TestGrokCLIBodyIsStreamedNotStoredAndListsOnlyItsFields(t *testing.T) {
	out := grokOut(t, `{"model":"grok-build","input":"hi","stream":false,"store":true,"temperature":0.3,"top_p":0.9,"max_output_tokens":99,
		"parallel_tool_calls":true,"metadata":{"k":"v"},"text":{"verbosity":"low"},"prompt_cache_key":"p",
		"max_tokens":5,"user":"u","stream_options":{"include_usage":true},"previous_response_id":"resp_1","service_tier":"auto","instructions":"i"}`)
	if out["stream"] != true || out["store"] != false {
		t.Fatalf("stream=%v store=%v", out["stream"], out["store"])
	}
	for _, kept := range []string{"temperature", "top_p", "max_output_tokens", "parallel_tool_calls", "metadata", "text", "prompt_cache_key", "instructions"} {
		if _, present := out[kept]; !present {
			t.Errorf("%s was dropped", kept)
		}
	}
	for _, dropped := range []string{"max_tokens", "user", "stream_options", "previous_response_id", "service_tier"} {
		if _, present := out[dropped]; present {
			t.Errorf("%s was sent", dropped)
		}
	}
	if _, has := out["instructions"]; !has {
		t.Error("instructions are the client's and stay")
	}
}

func TestGrokCLIInputBecomesATurnAndKeepsSystemTurns(t *testing.T) {
	for body, want := range map[string]any{
		`{"model":"m","input":"hello"}`: []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "hello"}}}},
		`{"model":"m","input":[]}`:      []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "..."}}}},
		`{"model":"m"}`:                 []any{map[string]any{"type": "message", "role": "user", "content": "..."}},
		`{"model":"m","messages":[{"role":"system","content":"s"},{"role":"user","content":[{"type":"text","text":"t"}]}]}`: []any{
			map[string]any{"type": "message", "role": "system", "content": "s"},
			map[string]any{"type": "message", "role": "user", "content": `[{"text":"t","type":"text"}]`},
		},
		`{"model":"m","input":[{"role":"system","content":"s"}]}`: []any{map[string]any{"role": "system", "content": "s"}},
	} {
		out := grokOut(t, body)
		if !reflect.DeepEqual(out["input"], want) {
			t.Errorf("%s:\n got %v\nwant %v", body, out["input"], want)
		}
		if _, present := out["messages"]; present {
			t.Errorf("%s: messages were sent", body)
		}
	}
}

func TestGrokCLIInputItemRepair(t *testing.T) {
	out := grokOut(t, `{"model":"m","input":[
		{"role":"user","content":"u","internal_chat_message_metadata_passthrough":{"a":1}},
		{"type":"reasoning","id":"`+nativeReasoning+`","encrypted_content":"enc","summary":[]},
		{"type":"reasoning","id":"rs_not_native","encrypted_content":"enc"},
		{"type":"reasoning","id":"`+nativeReasoning+`"},
		{"type":"custom_tool_call","call_id":"c1","name":" patch ","input":"*** Begin"},
		{"type":"custom_tool_call_output","call_id":"c1","output":{"ok":true}},
		{"type":"function_call","id":"fc_old","call_id":"c2","name":"f","arguments":{"a":1},"status":"completed"},
		{"type":"function_call_output","call_id":"c2","output":"done"},
		{"type":"function_call_output","call_id":"orphan","output":"x"},
		{"type":"function_call","call_id":"c3","name":"","arguments":"{}"},
		{"type":"message","id":"`+nativeMessage+`","role":"assistant","content":[]},
		{"type":"message","id":"msg_other","role":"assistant","content":[]}]}`)
	got := out["input"].([]any)
	want := []any{
		map[string]any{"role": "user", "content": "u"},
		map[string]any{"type": "reasoning", "id": nativeReasoning, "encrypted_content": "enc", "summary": []any{}},
		map[string]any{"type": "function_call", "call_id": "c1", "name": "patch", "arguments": `{"input":"*** Begin"}`},
		map[string]any{"type": "function_call_output", "call_id": "c1", "output": `{"ok":true}`},
		map[string]any{"type": "function_call", "call_id": "c2", "name": "f", "arguments": `{"a":1}`, "status": "completed"},
		map[string]any{"type": "function_call_output", "call_id": "c2", "output": "done"},
		map[string]any{"type": "message", "id": nativeMessage, "role": "assistant", "content": []any{}},
		map[string]any{"type": "message", "role": "assistant", "content": []any{}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("input\n got %v\nwant %v", got, want)
	}
}

func TestGrokCLIToolsAreFlattenedAndHostedToolsKept(t *testing.T) {
	out := grokOut(t, `{"model":"m","input":"x","tool_choice":{"type":"function","function":{"name":"f"}},"tools":[
		{"type":"function","function":{"name":"f","description":"d","parameters":{"type":"object"}}},
		{"type":"custom","name":"patch","description":"freeform"},
		{"type":"custom"},
		{"function":{"name":"typeless"}},
		{"name":"bare"},
		{"type":"web_search"},{"type":"x_search"},
		{"type":"namespace","name":"ns"},
		{"type":"function","name":" "},
		7]}`)
	var names []string
	for _, tool := range out["tools"].([]any) {
		object := tool.(map[string]any)
		names = append(names, object["type"].(string)+":"+asString(object["name"]))
	}
	if want := []string{"function:f", "function:patch", "function:typeless", "function:bare", "web_search:", "x_search:", "function:ns"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("tools %v, want %v", names, want)
	}
	// A typed tool that carries a name is read as a function in another shape, a
	// namespace included (:261-269); a typed tool with no name is dropped.
	patch := out["tools"].([]any)[1].(map[string]any)
	if !reflect.DeepEqual(patch["parameters"], map[string]any{"type": "object", "properties": map[string]any{"input": map[string]any{"type": "string"}}, "required": []any{"input"}}) {
		t.Fatalf("a freeform tool takes one string input: %v", patch["parameters"])
	}
	if !reflect.DeepEqual(out["tool_choice"], map[string]any{"type": "function", "name": "f"}) {
		t.Fatalf("tool_choice %v", out["tool_choice"])
	}
}

func TestGrokCLIToolChoiceAndEmptyTools(t *testing.T) {
	for body, wantChoice := range map[string]any{
		`{"model":"m","input":"x","tool_choice":{"type":"function","name":"gone"},"tools":[{"type":"function","name":"here"}]}`: nil,
		`{"model":"m","input":"x","tool_choice":{"type":"web_search"},"tools":[{"type":"web_search"}]}`:                         map[string]any{"type": "web_search"},
		`{"model":"m","input":"x","tool_choice":{"type":"web_search"},"tools":[{"type":"function","name":"f"}]}`:                nil,
		`{"model":"m","input":"x","tool_choice":{"type":"function","name":"f"},"tools":[]}`:                                     nil,
		`{"model":"m","input":"x","tool_choice":{"type":"function","name":"f"}}`:                                                nil,
		`{"model":"m","input":"x","tool_choice":{"type":"function","name":"f"},"tools":[{"type":"made_up"}]}`:                   nil,
	} {
		out := grokOut(t, body)
		if !reflect.DeepEqual(out["tool_choice"], wantChoice) {
			t.Errorf("%s: tool_choice %v, want %v", body, out["tool_choice"], wantChoice)
		}
		if _, hasTools := out["tools"]; hasTools && strings.Contains(body, `"tools":[]`) {
			t.Errorf("%s: empty tools were sent", body)
		}
	}
}

func TestGrokCLIReasoning(t *testing.T) {
	for name, test := range map[string]struct {
		body    string
		model   string
		effort  any
		summary string
	}{
		"4.5 default":           {`{"model":"grok-4.5","input":"x"}`, "grok-4.5", "high", "concise"},
		"4.5 effort suffix":     {`{"model":"grok-4.5-low","input":"x"}`, "grok-4.5", "low", "concise"},
		"4.5 reasoning_effort":  {`{"model":"grok-4.5-low","input":"x","reasoning_effort":"max"}`, "grok-4.5", "xhigh", "concise"},
		"4.5 explicit":          {`{"model":"grok-4.5","input":"x","reasoning":{"effort":"medium","summary":"detailed"}}`, "grok-4.5", "medium", "detailed"},
		"4.5 unknown level":     {`{"model":"grok-4.5","input":"x","reasoning":{"effort":"warp"}}`, "grok-4.5", "high", "concise"},
		"build takes no effort": {`{"model":"grok-build","input":"x","reasoning_effort":"low"}`, "grok-build", nil, "concise"},
		"build explicit effort": {`{"model":"grok-build","input":"x","reasoning":{"effort":"high"}}`, "grok-build", nil, "concise"},
		"4.50 is not 4.5":       {`{"model":"grok-4.50","input":"x"}`, "grok-4.50", nil, "concise"},
	} {
		t.Run(name, func(t *testing.T) {
			out := grokOut(t, test.body)
			reasoning := out["reasoning"].(map[string]any)
			if out["model"] != test.model || reasoning["effort"] != test.effort || reasoning["summary"] != test.summary {
				t.Fatalf("model=%v reasoning=%v", out["model"], reasoning)
			}
			if !reflect.DeepEqual(out["include"], []any{"reasoning.encrypted_content"}) {
				t.Fatalf("include %v", out["include"])
			}
			if _, present := out["reasoning_effort"]; present {
				t.Error("reasoning_effort was sent")
			}
		})
	}
	out := grokOut(t, `{"model":"grok-4.5","input":"x","reasoning":{"effort":"none"},"include":["a","reasoning.encrypted_content"]}`)
	if out["reasoning"].(map[string]any)["effort"] != "high" {
		// none is not a Grok level (:124-129): it normalizes like any unknown value.
		t.Fatalf("reasoning %v", out["reasoning"])
	}
	if !reflect.DeepEqual(out["include"], []any{"a", "reasoning.encrypted_content"}) {
		t.Fatalf("an include the client sent is kept, once: %v", out["include"])
	}
}

func TestGrokCLIBodyIsIdempotentAndFailsOpen(t *testing.T) {
	provider := routing.ProviderRef{ProviderID: "grok-cli", Protocol: responses}
	body := `{"model":"grok-4.5-high","input":[{"role":"user","content":"u"},{"type":"custom_tool_call","call_id":"c","name":"n","input":"i"}],"tools":[{"type":"function","function":{"name":"f"}}],"tool_choice":"auto"}`
	once := FinalizeBody(provider, []byte(body))
	same(t, string(FinalizeBody(provider, once)), string(once))
	for _, raw := range []string{``, `nope`, `[]`} {
		if got := string(FinalizeBody(provider, []byte(raw))); got != raw {
			t.Errorf("%q changed to %q", raw, got)
		}
	}
	if got := string(FinalizeBody(routing.ProviderRef{ProviderID: "grok-cli", Protocol: "openai-chat"}, []byte(`{"messages":[]}`))); got != `{"messages":[]}` {
		t.Errorf("a Chat body was rewritten: %s", got)
	}
}

func TestGrokCLIHeadersCarryTheCLIIdentityAndSession(t *testing.T) {
	provider := routing.ProviderRef{ProviderID: "grok-cli", Protocol: responses, ConnectionID: "acct-1"}
	provider.Connection.Settings = routing.ProviderSettings{Email: "a@example.test", UserID: "u-1", DeviceID: "dev-1"}
	body := []byte(`{"model":"grok-4.5","prompt_cache_key":"thread-9","input":[
		{"role":"user","content":"1"},{"type":"message","role":"assistant","content":"a"},
		{"type":"message","role":"user","content":"2"},{"type":"function_call_output","role":"user","call_id":"c","output":"x"}]}`)
	headers, err := grokCLIHeaders(provider, body, "https://cli-chat-proxy.grok.com/v1/responses")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"User-Agent":               "grok-shell/" + grokCLIVersion + " (linux; x86_64)",
		"Accept":                   "text/event-stream",
		"x-grok-client-identifier": "grok-shell",
		"x-grok-client-version":    grokCLIVersion,
		"x-grok-session-id":        "thread-9",
		"x-grok-conv-id":           "thread-9",
		"x-grok-turn-idx":          "2",
		"x-grok-agent-id":          "dev-1",
		"x-grok-model-override":    "grok-4.5",
		"x-email":                  "a@example.test",
		"x-userid":                 "u-1",
	} {
		if headers[name] != want {
			t.Errorf("%s = %q, want %q", name, headers[name], want)
		}
	}
	if len(headers["x-grok-req-id"]) != 36 {
		t.Errorf("x-grok-req-id %q", headers["x-grok-req-id"])
	}
	again, _ := grokCLIHeaders(provider, body, "")
	if again["x-grok-req-id"] == headers["x-grok-req-id"] {
		t.Error("two requests share a request id")
	}
}

func TestGrokCLIHeadersDoNotInventWhatTheConnectionLacks(t *testing.T) {
	headers, _ := grokCLIHeaders(routing.ProviderRef{ProviderID: "grok-cli", ConnectionID: "acct-1"}, []byte(`{}`), "")
	for _, name := range []string{"x-email", "x-userid", "x-grok-agent-id", "x-grok-model-override"} {
		if _, present := headers[name]; present {
			t.Errorf("%s = %q was sent without a source", name, headers[name])
		}
	}
	if headers["x-grok-turn-idx"] != "1" || headers["x-grok-session-id"] != connectionSession(routing.ProviderRef{ProviderID: "grok-cli", ConnectionID: "acct-1"}) {
		t.Errorf("%v", headers)
	}
}
