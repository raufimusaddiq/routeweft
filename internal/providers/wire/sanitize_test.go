package wire

import (
	"strings"
	"testing"

	anthropicadapter "github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// Each case is one rule of the pin's finalize step (default.js:106,109): where
// it fires, and where the same body is left byte-for-byte alone.
func TestStripRulesApplyAtFinalize(t *testing.T) {
	msgs := `"messages":[{"role":"user","content":"hi"}]`
	cases := []struct {
		name, provider, protocol, in, want string
	}{
		{"claude temperature, chat", "openrouter", chat, `{"model":"claude-opus-4","temperature":0.5,` + msgs + `}`, `{"model":"claude-opus-4",` + msgs + `}`},
		{"claude temperature, a native Messages body no translator touched", "claude", messages, `{"model":"claude-sonnet-4-5","temperature":1,` + msgs + `}`, `{"model":"claude-sonnet-4-5",` + msgs + `}`},
		{"github gpt-5.4 temperature", "github", chat, `{"model":"gpt-5.4","temperature":0.5,` + msgs + `}`, `{"model":"gpt-5.4",` + msgs + `}`},
		{"github gpt-5.4 rule is github's", "openai", chat, `{"model":"gpt-5.4","temperature":0.5,` + msgs + `}`, ``},
		{"github claude thinking", "github", chat, `{"model":"claude-haiku-4.5","thinking":{"type":"enabled"},"reasoning_effort":"high",` + msgs + `}`, `{"model":"claude-haiku-4.5",` + msgs + `}`},
		{"github claude 4.6 keeps thinking", "github", chat, `{"model":"claude-opus-4.6","thinking":{"type":"enabled"},"reasoning_effort":"high",` + msgs + `}`, `{"model":"claude-opus-4.6","thinking":{"type":"enabled"},"reasoning_effort":"high",` + msgs + `}`},
		{"thinking rule is github's", "openrouter", chat, `{"model":"claude-haiku-4.5","thinking":{"type":"enabled"},` + msgs + `}`, `{"model":"claude-haiku-4.5","thinking":{"type":"enabled"},` + msgs + `}`},
		{"cloudflare flattens parts", "cloudflare-ai", chat,
			`{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"u"}},{"type":"text","text":"b"}]},{"role":"user","content":"s"}]}`,
			`{"model":"m","messages":[{"role":"user","content":"ab"},{"role":"user","content":"s"}]}`},
		{"flatten is cloudflare's", "openai", chat, `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"a"}]}]}`, ``},
		{"flatten never rewrites Messages blocks", "cloudflare-ai", messages, `{"model":"m","messages":[{"role":"user","content":[{"type":"tool_result","tool_use_id":"t","content":"x"}]}]}`, ``},
		{"mimo preview flattens", "xiaomi-mimo", chat, `{"model":"mimo-v2-preview","messages":[{"role":"user","content":[{"type":"text","text":"a"}]}]}`, `{"model":"mimo-v2-preview","messages":[{"role":"user","content":"a"}]}`},
		{"mimo cloud model keeps parts", "xiaomi-mimo", chat, `{"model":"mimo-v2-omni","messages":[{"role":"user","content":[{"type":"text","text":"a"}]}]}`, ``},
		{"ark kimi cap", "volcengine-ark", chat, `{"model":"Kimi-K2.7-Code","max_tokens":262144,"max_completion_tokens":40000,"max_output_tokens":100,` + msgs + `}`, `{"model":"Kimi-K2.7-Code","max_tokens":32768,"max_completion_tokens":32768,"max_output_tokens":100,` + msgs + `}`},
		{"ark kimi under the cap", "volcengine-ark", chat, `{"model":"kimi-k2","max_tokens":4096,` + msgs + `}`, ``},
		{"kimi cap is ark's", "openrouter", chat, `{"model":"kimi-k2","max_tokens":262144,` + msgs + `}`, ``},
		{"ark glm-5 clamps to the catalogued ceiling", "volcengine-ark", chat, `{"model":"glm-5.1","max_tokens":200000,"max_completion_tokens":128001,"max_output_tokens":100,` + msgs + `}`, `{"model":"glm-5.1","max_tokens":128000,"max_completion_tokens":128000,"max_output_tokens":100,` + msgs + `}`},
		{"ark glm-5 under the ceiling", "volcengine-ark", chat, `{"model":"glm-5.1","max_tokens":128000,` + msgs + `}`, ``},
		{"glm-5 clamp is ark's", "openrouter", chat, `{"model":"glm-5.1","max_tokens":200000,` + msgs + `}`, ``},
		{"groq drops echoed reasoning", "groq", chat, `{"model":"m","messages":[{"role":"assistant","content":"a","reasoning":"r","reasoning_details":[1],"reasoning_content":"c"},{"role":"user","content":"u","reasoning":"keep"}]}`, `{"model":"m","messages":[{"role":"assistant","content":"a"},{"role":"user","content":"u","reasoning":"keep"}]}`},
		{"mistral drops echoed reasoning", "mistral", chat, `{"model":"m","messages":[{"role":"assistant","content":"a","reasoning":"r"}]}`, `{"model":"m","messages":[{"role":"assistant","content":"a"}]}`},
		{"cerebras drops echoed reasoning", "cerebras", chat, `{"model":"m","messages":[{"role":"assistant","content":"a","reasoning":"r"}]}`, `{"model":"m","messages":[{"role":"assistant","content":"a"}]}`},
		{"echo drop is not for others", "openrouter", chat, `{"model":"m","messages":[{"role":"assistant","content":"a","reasoning":"r"}]}`, ``},
		{"opencode-go deepseek response_format", "opencode-go", chat, `{"model":"deepseek-v4.1-flash","response_format":{"type":"json_object"},` + msgs + `}`, `{"model":"deepseek-v4.1-flash",` + msgs + `}`},
		{"opencode-go other family keeps it", "opencode-go", chat, `{"model":"glm-5","response_format":{"type":"json_object"},` + msgs + `}`, ``},
		{"no model, no strip", "openrouter", chat, `{"temperature":0.5,` + msgs + `}`, ``},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := finalize(t, c.provider, c.protocol, c.in)
			if c.want == "" {
				if got != c.in {
					t.Errorf("body changed: %s", got)
				}
				return
			}
			same(t, got, c.want)
		})
	}
}

func TestReasoningInjectionAppliesAtFinalize(t *testing.T) {
	const assistant = `{"role":"assistant","content":"a"`
	toolCalls := `,"tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]`
	cases := []struct {
		name, provider, protocol, in, want string
	}{
		{"registry provider, every assistant turn", "deepseek", chat, `{"model":"deepseek-chat","messages":[` + assistant + `},{"role":"user","content":"u"}]}`, `{"model":"deepseek-chat","messages":[` + assistant + `,"reasoning_content":" "},{"role":"user","content":"u"}]}`},
		{"minimax", "minimax", chat, `{"model":"MiniMax-M2","messages":[` + assistant + `}]}`, `{"model":"MiniMax-M2","messages":[` + assistant + `,"reasoning_content":" "}]}`},
		{"minimax-cn", "minimax-cn", chat, `{"model":"MiniMax-M2","messages":[` + assistant + `}]}`, `{"model":"MiniMax-M2","messages":[` + assistant + `,"reasoning_content":" "}]}`},
		{"opencode (which also forces a stream)", "opencode", chat, `{"model":"m","messages":[` + assistant + `}]}`, `{"model":"m","stream":true,"messages":[` + assistant + `,"reasoning_content":" "}]}`},
		{"deepseek model on another provider", "openrouter", chat, `{"model":"deepseek-r1","messages":[` + assistant + `}]}`, `{"model":"deepseek-r1","messages":[` + assistant + `,"reasoning_content":" "}]}`},
		{"kimi model needs a tool call", "openrouter", chat, `{"model":"kimi-k2","messages":[` + assistant + `},{"role":"assistant","content":"b"` + toolCalls + `}]}`, `{"model":"kimi-k2","messages":[` + assistant + `},{"role":"assistant","content":"b"` + toolCalls + `,"reasoning_content":" "}]}`},
		{"empty and null reasoning are filled", "deepseek", chat, `{"model":"m","messages":[{"role":"assistant","reasoning_content":""},{"role":"assistant","reasoning_content":null}]}`, `{"model":"m","messages":[{"role":"assistant","reasoning_content":" "},{"role":"assistant","reasoning_content":" "}]}`},
		{"real reasoning is kept", "deepseek", chat, `{"model":"m","messages":[{"role":"assistant","reasoning_content":"why"}]}`, ``},
		{"a model with no rule gets none", "openrouter", chat, `{"model":"gpt-4o","messages":[` + assistant + `}]}`, ``},
		{"not a Chat body", "deepseek", messages, `{"model":"deepseek-chat","messages":[` + assistant + `}]}`, ``},
		{"deepseek-v4-pro-max", "deepseek", chat, `{"model":"deepseek-v4-pro-max","extra_body":{"a":1,"thinking":{"budget":3}},"reasoning_effort":"low","messages":[{"role":"user","content":"u"}]}`, `{"model":"deepseek-v4-pro","extra_body":{"a":1,"thinking":{"budget":3,"type":"enabled"}},"reasoning_effort":"max","messages":[{"role":"user","content":"u"}]}`},
		{"deepseek-v4-pro-none", "deepseek", chat, `{"model":"deepseek-v4-pro-none","reasoning_effort":"high","messages":[{"role":"user","content":"u"}]}`, `{"model":"deepseek-v4-pro","extra_body":{"thinking":{"type":"disabled"}},"messages":[{"role":"user","content":"u"}]}`},
		{"alias is deepseek's", "openrouter", chat, `{"model":"deepseek-v4-pro-max","messages":[{"role":"user","content":"u"}]}`, ``},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := finalize(t, c.provider, c.protocol, c.in)
			if c.want == "" {
				if got != c.in {
					t.Errorf("body changed: %s", got)
				}
				return
			}
			same(t, got, c.want)
		})
	}
}

// Providers whose pin executor does not run DefaultExecutor.transformRequest are
// untouched; github strips but never injects, opencode injects but never strips.
func TestSanitizeScopePerProvider(t *testing.T) {
	in := `{"model":"claude-deepseek","temperature":1,"messages":[{"role":"assistant","content":"a"}]}`
	for id := range noSanitize {
		got := finalize(t, id, chat, in)
		if id == "zed" {
			same(t, got, `{"model":"claude-deepseek","temperature":1,"messages":[{"role":"assistant","content":"a"}],"stream":true}`)
		} else if got != in {
			t.Errorf("%s sanitized: %s", id, got)
		}
	}
	github := decode(t, finalize(t, "github", chat, in))
	if has(github, "temperature") || strings.Contains(finalize(t, "github", chat, in), "reasoning_content") {
		t.Errorf("github must strip and not inject: %v", github)
	}
	opencode := finalize(t, "opencode", chat, in)
	if !strings.Contains(opencode, `"temperature":1`) || !strings.Contains(opencode, "reasoning_content") {
		t.Errorf("opencode must inject and not strip: %s", opencode)
	}
}

// A body is finalized whichever leg built it, and finalizing it again changes
// nothing, so a probe replaying a served body does not double-apply a rule.
func TestSanitizeIsIdempotentAndLegIndependent(t *testing.T) {
	for _, c := range []struct{ id, in string }{
		{"deepseek", `{"model":"deepseek-v4-pro-max","temperature":1,"messages":[{"role":"assistant","content":"a"}]}`},
		{"mistral", `{"model":"deepseek-r1","messages":[{"role":"assistant","content":"a","reasoning":"r"}]}`},
		{"volcengine-ark", `{"model":"kimi-k2","max_tokens":999999,"messages":[]}`},
		{"github", `{"model":"claude-haiku-4.5","thinking":{},"messages":[]}`},
	} {
		once := finalize(t, c.id, chat, c.in)
		if once == c.in {
			t.Errorf("%s: finalize did nothing to %s", c.id, c.in)
		}
		if twice := finalize(t, c.id, chat, once); twice != once {
			t.Errorf("%s: second finalize changed the body:\n%s\n%s", c.id, once, twice)
		}
	}
}

// The translator does not sanitise: a body a conversion leg built still carries
// what the strip rules and injection act on until FinalizeBody. This fails if a
// rule is moved back into a leg, where a body from another leg would miss it.
func TestTranslatorLegsLeaveSanitationToFinalize(t *testing.T) {
	in := []byte(`{"model":"claude-sonnet-4-5","max_tokens":10,"temperature":0.3,"messages":[{"role":"user","content":"u"},{"role":"assistant","content":"a"},{"role":"user","content":"v"}]}`)
	converted, err := anthropicadapter.MessagesRequestToChat(in, "deepseek-chat", false)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(converted), `"temperature"`) || strings.Contains(string(converted), "reasoning_content") {
		t.Fatalf("the Messages->Chat leg already sanitised: %s", converted)
	}
	out := string(FinalizeBody(routing.ProviderRef{ProviderID: "deepseek", Protocol: openaiadapter.ChatProtocol}, converted))
	if !strings.Contains(out, `"reasoning_content":" "`) {
		t.Errorf("finalize did not inject on a converted body: %s", out)
	}
}

// preserveCacheControl is translator-time (translator/index.js:152): finalize
// neither strips cache_control nor adds it back, for a provider that keeps it
// and for one that does not alike.
func TestFinalizeDoesNotApplyTranslatorTimeCacheControl(t *testing.T) {
	in := `{"model":"qwen3-coder-plus","messages":[{"role":"user","content":[{"type":"text","text":"a","cache_control":{"type":"ephemeral"}}]}]}`
	for _, id := range []string{"alicode", "openrouter", "deepseek"} {
		if got := finalize(t, id, chat, in); got != in {
			t.Errorf("%s: finalize touched cache_control: %s", id, got)
		}
	}
	// And the converse: a body a leg stripped is not given cache_control back.
	stripped := `{"model":"qwen3-coder-plus","messages":[{"role":"user","content":[{"type":"text","text":"a"}]}]}`
	if got := finalize(t, "alicode", chat, stripped); got != stripped {
		t.Errorf("finalize changed a stripped body: %s", got)
	}
}
