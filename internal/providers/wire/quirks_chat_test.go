package wire

import (
	"fmt"
	"strings"
	"testing"
)

func has(m map[string]any, key string) bool { _, ok := m[key]; return ok }

// GitHub Copilot: model x max_tokens x max_completion_tokens x reasoning_effort.
func TestGithubChatBodyCrossProduct(t *testing.T) {
	models := map[string]bool{"gpt-5": true, "gpt-5.2-codex": true, "o3-mini": true, "o1-preview": true, "o4-mini": true, "GPT-5-mini": true,
		"gpt-4o": false, "claude-sonnet-4.5": false, "gemini-2.5-pro": false, "o2-x": false}
	tokens := map[string]string{"absent": "", "number": `"max_tokens":100,`, "null": `"max_tokens":null,`}
	completions := map[string]string{"absent": "", "present": `"max_completion_tokens":7,`}
	efforts := map[string]string{"absent": "", "none": `"reasoning_effort":"none",`, "high": `"reasoning_effort":"high",`, "non-string": `"reasoning_effort":["none"],`}
	for model, completionModel := range models {
		for tn, tk := range tokens {
			for cn, cp := range completions {
				for en, ef := range efforts {
					t.Run(fmt.Sprintf("%s/%s/%s/%s", model, tn, cn, en), func(t *testing.T) {
						in := `{"model":"` + model + `",` + tk + cp + ef + `"messages":[{"role":"user","content":"hi"}]}`
						out := finalize(t, "github", chat, in)
						got := decode(t, out)
						if completionModel && tn != "absent" {
							if has(got, "max_tokens") {
								t.Fatalf("max_tokens kept for %s: %s", model, out)
							}
							want := any(float64(100))
							if tn == "null" {
								want = nil
							}
							if cn == "present" {
								want = float64(7) // an explicit max_completion_tokens is never overwritten
							}
							if value, ok := got["max_completion_tokens"]; !ok || value != want {
								t.Fatalf("max_completion_tokens %v (%v) want %v: %s", value, ok, want, out)
							}
						} else if tn != "absent" && !has(got, "max_tokens") {
							t.Fatalf("max_tokens moved for non-completion model %s: %s", model, out)
						}
						if !completionModel && cn == "present" && got["max_completion_tokens"] != float64(7) {
							t.Fatalf("client max_completion_tokens changed: %s", out)
						}
						// "none" is removed; every other state, including a non-string
						// value the client owns, is left exactly as sent.
						if wantEffort := en == "high" || en == "non-string"; has(got, "reasoning_effort") != wantEffort {
							t.Fatalf("reasoning_effort %s: present=%v: %s", en, has(got, "reasoning_effort"), out)
						}
						if !strings.Contains(out, `"hi"`) {
							t.Fatalf("user text lost: %s", out)
						}
					})
				}
			}
		}
	}
	// Only the Chat wire is touched.
	in := `{"model":"gpt-5","max_tokens":5,"reasoning_effort":"none","input":"x"}`
	for _, p := range []string{responses, messages} {
		if got := finalize(t, "github", p, in); got != in {
			t.Errorf("%s body changed: %s", p, got)
		}
	}
}

func TestGithubMessageContentCrossProduct(t *testing.T) {
	parts := map[string]string{
		"text": `{"type":"text","text":"T"}`, "empty text": `{"type":"text","text":""}`, "image": `{"type":"image_url","image_url":{"url":"u"}}`,
		"tool_result with content": `{"type":"tool_result","tool_use_id":"1","content":"RESULT"}`, "tool_use": `{"type":"tool_use","id":"1","name":"n","input":{}}`,
		"thinking with text": `{"type":"thinking","text":"THOUGHT"}`, "empty tool_result": `{"type":"tool_result","tool_use_id":"1","content":""}`,
		"unknown": `{"type":"mystery","a":1}`,
	}
	for name, part := range parts {
		for _, role := range []string{"user", "assistant", "tool"} {
			for _, extra := range []string{"", `,"tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]`} {
				t.Run(name+"/"+role+"/"+fmt.Sprint(extra != ""), func(t *testing.T) {
					in := `{"model":"gpt-4o","messages":[{"role":"` + role + `","content":[` + part + `,{"type":"text","text":"KEEP"}]` + extra + `}]}`
					out := finalize(t, "github", chat, in)
					got := decode(t, out)
					message := got["messages"].([]any)[0].(map[string]any)
					if message["role"] != role || (extra != "") != has(message, "tool_calls") {
						t.Fatalf("message envelope changed: %s", out)
					}
					content, _ := message["content"].([]any)
					kinds := map[string]bool{}
					for _, p := range content {
						kind := p.(map[string]any)["type"].(string)
						kinds[kind] = true
						if kind != "text" && kind != "image_url" {
							t.Fatalf("part type %q reached Copilot: %s", kind, out)
						}
						if text, ok := p.(map[string]any)["text"]; ok && text == "" {
							t.Fatalf("empty text part sent: %s", out)
						}
					}
					if !strings.Contains(out, "KEEP") {
						t.Fatalf("sibling text lost: %s", out)
					}
					switch name {
					case "tool_result with content":
						if !strings.Contains(out, "RESULT") {
							t.Fatalf("tool result text lost: %s", out)
						}
					case "thinking with text":
						if !strings.Contains(out, "THOUGHT") {
							t.Fatalf("thinking text lost: %s", out)
						}
					case "tool_use", "unknown":
						if !strings.Contains(out, `\"type\":\"`) {
							t.Fatalf("part not serialized as text: %s", out)
						}
					case "image":
						if !kinds["image_url"] {
							t.Fatalf("image part lost: %s", out)
						}
					}
				})
			}
		}
	}
	// A message whose every part is removed gets null content rather than an empty array.
	only := `{"model":"gpt-4o","messages":[{"role":"assistant","content":[{"type":"text","text":""}],"tool_calls":[{"id":"c"}]}]}`
	got := decode(t, finalize(t, "github", chat, only))
	if m := got["messages"].([]any)[0].(map[string]any); m["content"] != nil || !has(m, "content") || !has(m, "tool_calls") {
		t.Errorf("all-removed message: %v", m)
	}
	// String, null and absent content, and non-array messages, are untouched.
	for _, in := range []string{
		`{"model":"gpt-4o","messages":[{"role":"user","content":"s"},{"role":"assistant","content":null},{"role":"user"}]}`,
		`{"model":"gpt-4o"}`, `{"model":"gpt-4o","messages":"x"}`,
	} {
		if out := finalize(t, "github", chat, in); out != in {
			t.Errorf("changed %s -> %s", in, out)
		}
	}
}

// Kimchi: top-level system x messages state, model x reasoning fields, artifacts.
func TestKimchiSystemCrossMessages(t *testing.T) {
	systems := map[string]string{
		"string": `"system":"SYS"`, "parts": `"system":[{"type":"text","text":"SYS"},"SYS2"]`,
		"blank": `"system":"   "`, "null": `"system":null`, "empty array": `"system":[]`,
		"image part": `"system":[{"type":"text","text":"SYS"},{"type":"image","source":{}}]`,
		"object":     `"system":{"text":"SYS"}`, "number": `"system":3`,
	}
	messageSets := map[string]string{
		"user only":         `[{"role":"user","content":"USER"}]`,
		"system string":     `[{"role":"system","content":"EXISTING"},{"role":"user","content":"USER"}]`,
		"system parts":      `[{"role":"system","content":[{"type":"text","text":"EXISTING"}]},{"role":"user","content":"USER"}]`,
		"system null":       `[{"role":"system","content":null},{"role":"user","content":"USER"}]`,
		"system object":     `[{"role":"system","content":{"a":1}},{"role":"user","content":"USER"}]`,
		"empty":             `[]`,
		"not an array":      `"x"`,
		"second system too": `[{"role":"user","content":"USER"},{"role":"system","content":"EXISTING"}]`,
	}
	for sn, system := range systems {
		for mn, messages := range messageSets {
			t.Run(sn+"/"+mn, func(t *testing.T) {
				in := `{"model":"m",` + system + `,"messages":` + messages + `}`
				out := finalize(t, "kimchi", chat, in)
				got := decode(t, out)
				// Invariant: either nothing changed, or `system` is gone and every
				// piece of text it carried is in messages next to what was there.
				if out == in {
					return
				}
				if has(got, "system") {
					t.Fatalf("system kept on a rewritten body: %s", out)
				}
				for _, text := range []string{"SYS", "SYS2", "USER", "EXISTING"} {
					if strings.Contains(in, `"`+text) && !strings.Contains(out, text) && !(text == "SYS2" && sn != "parts") {
						t.Fatalf("%q lost\n in %s\nout %s", text, in, out)
					}
				}
				if _, ok := got["messages"].([]any); !ok && mn != "not an array" {
					t.Fatalf("messages is not an array: %s", out)
				}
			})
		}
	}
	// The merge itself, per the pin.
	same(t, finalize(t, "kimchi", chat, `{"model":"m","system":" SYS ","messages":[{"role":"user","content":"U"}]}`),
		`{"model":"m","messages":[{"role":"system","content":"SYS"},{"role":"user","content":"U"}]}`)
	same(t, finalize(t, "kimchi", chat, `{"model":"m","system":"SYS","messages":[{"role":"system","content":"E"}]}`),
		`{"model":"m","messages":[{"role":"system","content":"SYS\n\nE"}]}`)
	same(t, finalize(t, "kimchi", chat, `{"model":"m","system":[{"text":"A"},"B"],"messages":[{"role":"system","content":[{"type":"text","text":"E"}]}]}`),
		`{"model":"m","messages":[{"role":"system","content":[{"type":"text","text":"A\nB"},{"type":"text","text":"E"}]}]}`)
	// A body that needs nothing stays byte-identical.
	plain := `{"model":"m","messages":[{"role":"user","content":"U"}]}`
	if got := finalize(t, "kimchi", chat, plain); got != plain {
		t.Errorf("plain body changed: %s", got)
	}
}

func TestKimchiFieldsCrossModel(t *testing.T) {
	fields := `"anthropic_version":"x","anthropic_beta":"y","client_metadata":{},"mcp_servers":[],"stop_sequences":["s"],"thinking":{"type":"enabled"},"top_k":5,"reasoning_effort":"high","reasoning":{"effort":"high"},"temperature":0.2,`
	for model, anthropic := range map[string]bool{"claude-opus-4-6": true, "anthropic/claude-3": true, "vendor_claude-x": true, "kimi-k2.7": false, "declaude-x": false, "claudette": false} {
		t.Run(model, func(t *testing.T) {
			out := finalize(t, "kimchi", chat, `{"model":"`+model+`",`+fields+`"messages":[{"role":"user","content":"U"}]}`)
			got := decode(t, out)
			for _, key := range kimchiGatewayDrops {
				if has(got, key) {
					t.Errorf("%s survived: %s", key, out)
				}
			}
			if has(got, "reasoning_effort") == anthropic || has(got, "reasoning") == anthropic {
				t.Errorf("reasoning fields for anthropic=%v model: %s", anthropic, out)
			}
			if got["temperature"] != 0.2 {
				t.Errorf("unrelated field changed: %s", out)
			}
		})
	}
}

func TestKimchiArtifactsAndReasoningContent(t *testing.T) {
	long := strings.Repeat("r", 9)
	in := `{"model":"m","tools":[{"name":"t","cache_control":{"type":"ephemeral"}},{"name":"u"}],"messages":[` +
		`{"role":"user","cache_control":{"type":"ephemeral"},"content":[{"type":"text","text":"U","cache_control":{"type":"ephemeral"}},{"type":"thinking","thinking":"x","signature":"sig"}]},` +
		`{"role":"assistant","content":"A","reasoning_content":"` + long + `"},` +
		`{"role":"assistant","content":"B","reasoning_content":"` + strings.Repeat("r", 8) + `"},` +
		`{"role":"assistant","content":"C","reasoning_content":" "},` +
		`{"role":"user","content":"D","reasoning_content":"` + long + `"}]}`
	same(t, finalize(t, "kimchi", chat, in), `{"model":"m","tools":[{"name":"t"},{"name":"u"}],"messages":[`+
		`{"role":"user","content":[{"type":"text","text":"U"},{"type":"thinking","thinking":"x"}]},`+
		`{"role":"assistant","content":"A"},`+
		`{"role":"assistant","content":"B","reasoning_content":"`+strings.Repeat("r", 8)+`"},`+
		`{"role":"assistant","content":"C","reasoning_content":" "},`+
		`{"role":"user","content":"D","reasoning_content":"`+long+`"}]}`)
	// Only the Chat wire is touched.
	for _, p := range []string{responses, messages} {
		raw := `{"model":"m","system":"S","thinking":{},"messages":[]}`
		if got := finalize(t, "kimchi", p, raw); got != raw {
			t.Errorf("%s body changed: %s", p, got)
		}
	}
}
