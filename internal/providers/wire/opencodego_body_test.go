package wire

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

func openCodeGoFinal(model, protocol, body string) string {
	return string(FinalizeBodyAt(routing.ProviderRef{ProviderID: "opencode-go", Protocol: protocol, UpstreamModel: model}, "responses", []byte(body)))
}

// Sanitized shape of a Responses turn that replays a prior one (E/opencode-go.js:102-111).
const openCodeGoReplay = `{"model":"x","previous_response_id":"resp_1","future_field":{"keep":[1,2.50,"<&>"]},
"input":[
 {"type":"reasoning","id":"rs_1","encrypted_content":"opaque","summary":[]},
 {"type":"message","role":"assistant","id":"msg_1","encrypted_content":"opaque","content":[{"type":"output_text","text":"hi"}]},
 {"type":"function_call","call_id":"c1","name":"f","arguments":"{}","reasoning_encrypted_content":"opaque"},
 {"type":"item_reference","id":"rs_9"},
 {"role":"system","content":"be brief"},
 {"role":"user","content":"ping","extra":{"n":1.0}}]}`

func TestOpenCodeGoDropsPreviousResponseIDOnEveryWire(t *testing.T) {
	for _, model := range []string{"gpt-5.6-luna", "grok-4.6", "deepseek-v4-pro", "glm-5.3"} {
		for _, protocol := range []string{chat, responses, "anthropic-messages"} {
			got := decodeBody(t, openCodeGoFinal(model, protocol, `{"previous_response_id":"resp_1","future_field":"keep"}`))
			if _, has := got["previous_response_id"]; has || got["future_field"] != "keep" {
				t.Fatalf("%s/%s: %v", model, protocol, got)
			}
		}
	}
}

func TestOpenCodeGoStripsReasoningItemsAndEncryptedContent(t *testing.T) {
	// grok-4.6 is Responses-only but not Luna: only the reasoning strip applies.
	got := decodeBody(t, openCodeGoFinal("grok-4.6", responses, openCodeGoReplay))
	input := got["input"].([]any)
	if len(input) != 5 {
		t.Fatalf("reasoning item not the only drop: %v", input)
	}
	for i, item := range input {
		object := item.(map[string]any)
		if object["type"] == "reasoning" {
			t.Fatalf("reasoning item %d kept", i)
		}
		for _, field := range []string{"encrypted_content", "reasoning_encrypted_content"} {
			if _, has := object[field]; has {
				t.Fatalf("%s kept on %d", field, i)
			}
		}
	}
	// Not Luna: ids, item references and system roles are left as the client sent them.
	if input[0].(map[string]any)["id"] != "msg_1" || input[2].(map[string]any)["type"] != "item_reference" || input[3].(map[string]any)["role"] != "system" {
		t.Fatalf("non-Luna model lost Luna rewrites: %v", input)
	}
	if got["future_field"].(map[string]any)["keep"].([]any)[2] != "<&>" {
		t.Fatalf("unknown field changed: %v", got["future_field"])
	}
	// A chat-wire body for the same model is not touched beyond previous_response_id.
	if out := openCodeGoFinal("grok-4.6", chat, `{"input":[{"type":"reasoning"}],"max_tokens":5}`); !strings.Contains(out, `"reasoning"`) || !strings.Contains(out, `"max_tokens":5`) {
		t.Fatalf("chat wire rewritten: %s", out)
	}
}

func TestOpenCodeGoStripKeepsUntouchedItemsByteForByte(t *testing.T) {
	body := `{"model":"x","input":[{"type":"reasoning","id":"rs_1"},{"role":"user","content":"a  b","n":1.50},"raw string",null]}`
	var parsed Body
	if err := json.Unmarshal([]byte(openCodeGoFinal("grok-4.6", responses, body)), &parsed); err != nil {
		t.Fatal(err)
	}
	var items []json.RawMessage
	_ = json.Unmarshal(parsed["input"], &items)
	want := []string{`{"role":"user","content":"a  b","n":1.50}`, `"raw string"`, `null`}
	if len(items) != 3 || string(items[0]) != want[0] || string(items[1]) != want[1] || string(items[2]) != want[2] {
		t.Fatalf("items rewritten: %s", parsed["input"])
	}
	// Nothing to strip: the body is returned as received, byte for byte.
	same := `{"model":"x", "input":[{"role":"user","content":"ping"}], "store":false, "stream":true}`
	if got := openCodeGoFinal("grok-4.6", responses, same); got != same {
		t.Fatalf("rewritten: %s", got)
	}
}

func TestOpenCodeGoNormalizesTokensAndReasoning(t *testing.T) {
	for _, tc := range []struct {
		name, in string
		want     map[string]any
	}{
		{"max_tokens", `{"max_tokens":64}`, map[string]any{"max_output_tokens": 64.0}},
		{"completion wins over max_tokens", `{"max_tokens":64,"max_completion_tokens":32}`, map[string]any{"max_output_tokens": 32.0}},
		{"existing max_output_tokens wins", `{"max_output_tokens":8,"max_tokens":64,"max_completion_tokens":32}`, map[string]any{"max_output_tokens": 8.0}},
		{"effort", `{"reasoning_effort":"high"}`, map[string]any{"reasoning": map[string]any{"effort": "high", "summary": "auto"}}},
		{"effort does not replace reasoning", `{"reasoning_effort":"high","reasoning":{"effort":"low"}}`, map[string]any{"reasoning": map[string]any{"effort": "low", "summary": "auto"}}},
		{"summary kept", `{"reasoning":{"effort":"low","summary":"detailed"}}`, map[string]any{"reasoning": map[string]any{"effort": "low", "summary": "detailed"}}},
		{"empty summary", `{"reasoning":{"summary":""}}`, map[string]any{"reasoning": map[string]any{"summary": "auto"}}},
		{"null reasoning left", `{"reasoning":null,"reasoning_effort":"low"}`, map[string]any{"reasoning": nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := decodeBody(t, openCodeGoFinal("grok-4.6", responses, tc.in))
			for _, gone := range []string{"max_tokens", "max_completion_tokens", "reasoning_effort"} {
				if _, has := got[gone]; has {
					t.Fatalf("%s kept: %v", gone, got)
				}
			}
			for key, want := range tc.want {
				if !reflect.DeepEqual(got[key], want) {
					t.Fatalf("%s: got %v want %v", key, got[key], want)
				}
			}
			if got["store"] != false || got["stream"] != true {
				t.Fatalf("store/stream: %v", got)
			}
		})
	}
	// A model that is not Responses-only keeps its Chat names.
	got := decodeBody(t, openCodeGoFinal("glm-5.3", chat, `{"max_tokens":64,"reasoning_effort":"high"}`))
	if got["max_tokens"] != 64.0 || got["reasoning_effort"] != "high" || got["max_output_tokens"] != nil || got["store"] != nil {
		t.Fatalf("chat model rewritten: %v", got)
	}
}

func TestOpenCodeGoLunaOmissionsAndInputRewrites(t *testing.T) {
	in := `{"model":"x","stream_options":{"include_usage":true},"temperature":0.2,"top_p":1,"frequency_penalty":0,"presence_penalty":0,"logprobs":true,"top_logprobs":2,"n":1,"seed":7,"max_tokens":9,"max_completion_tokens":9,"max_output_tokens":9,"user":"u","prompt_cache_retention":"24h","metadata":{"a":"b"},"safety_identifier":"s","previous_response_id":"resp_1","prompt_cache_key":"keep","text":{"verbosity":"low"},` + openCodeGoReplay[strings.Index(openCodeGoReplay, `"future_field"`):]
	got := decodeBody(t, openCodeGoFinal("gpt-5.6-luna", responses, in))
	for _, field := range openCodeGoLunaOmit {
		if _, has := got[field]; has {
			t.Fatalf("%s kept", field)
		}
	}
	if got["prompt_cache_key"] != "keep" || got["text"] == nil || got["future_field"] == nil || got["store"] != false || got["stream"] != true {
		t.Fatalf("lost a field Luna accepts: %v", got)
	}
	input := got["input"].([]any)
	if len(input) != 4 {
		t.Fatalf("want reasoning and item_reference dropped: %v", input)
	}
	if _, has := input[0].(map[string]any)["id"]; has {
		t.Fatalf("server id kept: %v", input[0])
	}
	if input[2].(map[string]any)["role"] != "developer" || input[3].(map[string]any)["role"] != "user" {
		t.Fatalf("roles: %v", input)
	}
	// grok-4.6 keeps the sampling fields Luna drops.
	if out := decodeBody(t, openCodeGoFinal("grok-4.6", responses, `{"temperature":1,"seed":3,"input":[]}`)); out["temperature"] != 1.0 || out["seed"] != 3.0 {
		t.Fatalf("grok-4.6 lost sampling fields: %v", out)
	}
	// String items that name a server id go; other strings stay.
	out := decodeBody(t, openCodeGoFinal("gpt-5.6-luna", responses, `{"input":["rs_abc","hello",{"type":"message","role":"system","content":"s"},{"type":"message","role":"user","id":"user_1","content":"u"}]}`))
	items := out["input"].([]any)
	if len(items) != 3 || items[0] != "hello" || items[1].(map[string]any)["role"] != "developer" || items[2].(map[string]any)["id"] != "user_1" {
		t.Fatalf("string and id handling: %v", items)
	}
}

func TestOpenCodeGoDropsResponseFormatForDeepseekOnly(t *testing.T) {
	in := `{"model":"%s","response_format":{"type":"json_object"},"messages":[]}`
	for _, tc := range []struct {
		provider, model string
		dropped         bool
	}{
		{"opencode-go", "deepseek-v4.1-flash", true},
		{"opencode-go", "DeepSeek-V4-Pro", true},
		{"opencode-go", "glm-5.3", false},
		{"opencode-go", "kimi-k2.6", false},
		{"opencode", "deepseek-v4.1-flash", false},
		{"deepseek", "deepseek-v4.1-flash", false},
	} {
		body := strings.Replace(in, "%s", tc.model, 1)
		got := decodeBody(t, string(FinalizeBodyAt(routing.ProviderRef{ProviderID: tc.provider, Protocol: chat, UpstreamModel: tc.model}, "chat/completions", []byte(body))))
		if _, has := got["response_format"]; has == tc.dropped {
			t.Fatalf("%s/%s: %v", tc.provider, tc.model, got)
		}
	}
}
