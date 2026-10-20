package wire

import (
	"encoding/json"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

var codebuddyIDs = []string{"codebuddy-cn", "codebuddy-intl"}

func eachCodeBuddy(t *testing.T, run func(t *testing.T, id string)) {
	t.Helper()
	for _, id := range codebuddyIDs {
		t.Run(id, func(t *testing.T) { run(t, id) })
	}
}

func TestCodeBuddyHooksForceStreamAndSendNoIdentity(t *testing.T) {
	eachCodeBuddy(t, func(t *testing.T, id string) {
		if !ForcesStream(id) {
			t.Fatal("not a stream-only provider")
		}
		headers := OpenAIHeaders(routing.ProviderRef{ProviderID: id, APIToken: "tok"})
		if len(headers) != 1 || headers.Get("Authorization") != "Bearer tok" {
			t.Fatalf("a CodeBuddy request carries only its credential, got %v", headers)
		}
		for _, name := range []string{"User-Agent", "X-Product", "X-IDE-Type", "X-IDE-Name", "X-Requested-With", "X-Codebuddy-Request"} {
			if headers.Get(name) != "" {
				t.Fatalf("%s sent: %v", name, headers)
			}
		}
	})
	if ForcesStream("openai") || ForcesStream("codex") {
		t.Fatal("forced stream leaked to another provider")
	}
}

func TestCodeBuddyStreamIsForcedWhateverTheClientSent(t *testing.T) {
	for _, stream := range []string{``, `"stream":false,`, `"stream":null,`, `"stream":"false",`, `"stream":0,`, `"stream":"",`, `"stream":[],`, `"stream":{},`, `"stream":true,`} {
		eachCodeBuddy(t, func(t *testing.T, id string) {
			out := finalize(t, id, chat, `{`+stream+`"model":"glm-5.2","temperature":0,"top_p":0,"presence_penalty":0,"stop":"","messages":[{"role":"user","content":"hi"}]}`)
			got := decode(t, out)
			if got["stream"] != true {
				t.Fatalf("stream %v: %s", got["stream"], out)
			}
			// Explicit zeros and empties are values and survive.
			if got["temperature"] != float64(0) || got["top_p"] != float64(0) || got["presence_penalty"] != float64(0) || got["stop"] != "" {
				t.Fatalf("a zero or empty value was lost: %s", out)
			}
		})
	}
	// A stream:true body no quirk changes is returned byte for byte.
	in := `{"model":"glm-5.2","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	if out := finalize(t, "codebuddy-cn", chat, in); out != in {
		t.Fatalf("an unchanged body was re-encoded: %s", out)
	}
}

func TestCodeBuddyQuirksAreScopedToChat(t *testing.T) {
	in := `{"model":"glm-5.2","reasoning_effort":"high","messages":[{"role":"developer","content":"x"}]}`
	for _, protocol := range []string{"openai-responses", "anthropic-messages", "gemini"} {
		if out := finalize(t, "codebuddy-intl", protocol, in); out != in {
			t.Fatalf("%s body rewritten: %s", protocol, out)
		}
	}
}

// reasoning_effort crossed with reasoning_summary: absent, null, empty, wrong
// type, populated, and the two disabling words in both cases.
func TestCodeBuddyReasoningCrossProduct(t *testing.T) {
	type outcome struct{ effort, summary string } // "" = absent; "RAW:" = the raw JSON
	efforts := []struct {
		name, json string
		// without a client summary, and with one
		bare, withSummary outcome
	}{
		{"absent", ``, outcome{"", ""}, outcome{"", `RAW:"concise"`}},
		{"null", `"reasoning_effort":null,`, outcome{"RAW:null", ""}, outcome{"RAW:null", `RAW:"concise"`}},
		{"empty", `"reasoning_effort":"",`, outcome{`RAW:""`, ""}, outcome{`RAW:""`, `RAW:"concise"`}},
		{"blank", `"reasoning_effort":"  ",`, outcome{`RAW:"  "`, ""}, outcome{`RAW:"  "`, `RAW:"concise"`}},
		{"high", `"reasoning_effort":"high",`, outcome{`RAW:"high"`, `RAW:"auto"`}, outcome{`RAW:"high"`, `RAW:"concise"`}},
		{"xhigh", `"reasoning_effort":"xhigh",`, outcome{`RAW:"xhigh"`, `RAW:"auto"`}, outcome{`RAW:"xhigh"`, `RAW:"concise"`}},
		{"none", `"reasoning_effort":"none",`, outcome{"", ""}, outcome{"", `RAW:"concise"`}},
		{"off", `"reasoning_effort":"off",`, outcome{"", ""}, outcome{"", `RAW:"concise"`}},
		{"None", `"reasoning_effort":"None",`, outcome{"", ""}, outcome{"", `RAW:"concise"`}},
		{"padded off", `"reasoning_effort":" OFF ",`, outcome{"", ""}, outcome{"", `RAW:"concise"`}},
		// A value that is not a string is the gateway's to judge: untouched, and no
		// summary is added to it.
		{"number", `"reasoning_effort":3,`, outcome{"RAW:3", ""}, outcome{"RAW:3", `RAW:"concise"`}},
		{"zero", `"reasoning_effort":0,`, outcome{"RAW:0", ""}, outcome{"RAW:0", `RAW:"concise"`}},
		{"true", `"reasoning_effort":true,`, outcome{"RAW:true", ""}, outcome{"RAW:true", `RAW:"concise"`}},
		{"false", `"reasoning_effort":false,`, outcome{"RAW:false", ""}, outcome{"RAW:false", `RAW:"concise"`}},
		{"object", `"reasoning_effort":{"x":1},`, outcome{`RAW:{"x":1}`, ""}, outcome{`RAW:{"x":1}`, `RAW:"concise"`}},
		{"array", `"reasoning_effort":["high"],`, outcome{`RAW:["high"]`, ""}, outcome{`RAW:["high"]`, `RAW:"concise"`}},
	}
	summaries := []struct {
		name, json string
		keep       string // the raw value that must survive when the effort is set
	}{
		{"bare", ``, ""},
		{"explicit concise", `"reasoning_summary":"concise",`, `"concise"`},
	}
	for _, e := range efforts {
		for i, s := range summaries {
			want := e.bare
			if i == 1 {
				want = e.withSummary
			}
			eachCodeBuddy(t, func(t *testing.T, id string) {
				t.Run(e.name+"/"+s.name, func(t *testing.T) {
					out := finalize(t, id, chat, `{"model":"glm-5.2",`+e.json+s.json+`"messages":[{"role":"user","content":"hi"}]}`)
					var raw map[string]json.RawMessage
					if err := json.Unmarshal([]byte(out), &raw); err != nil {
						t.Fatal(err)
					}
					check := func(field string, want string) {
						got, present := raw[field]
						switch {
						case want == "" && present:
							t.Fatalf("%s present (%s): %s", field, got, out)
						case want != "" && !present:
							t.Fatalf("%s missing, want %s: %s", field, strings.TrimPrefix(want, "RAW:"), out)
						case want != "" && string(got) != strings.TrimPrefix(want, "RAW:"):
							t.Fatalf("%s = %s want %s: %s", field, got, strings.TrimPrefix(want, "RAW:"), out)
						}
					}
					check("reasoning_effort", want.effort)
					check("reasoning_summary", want.summary)
				})
			})
		}
	}
}

func TestCodeBuddyReasoningSummaryKeepsEveryExplicitValue(t *testing.T) {
	for _, summary := range []string{`null`, `""`, `"detailed"`, `0`, `false`, `{}`, `[]`} {
		eachCodeBuddy(t, func(t *testing.T, id string) {
			out := finalize(t, id, chat, `{"model":"glm-5.2","reasoning_effort":"high","reasoning_summary":`+summary+`,"messages":[{"role":"user","content":"hi"}]}`)
			var raw map[string]json.RawMessage
			_ = json.Unmarshal([]byte(out), &raw)
			if string(raw["reasoning_summary"]) != summary {
				t.Fatalf("explicit reasoning_summary %s became %s", summary, raw["reasoning_summary"])
			}
		})
	}
}

func TestCodeBuddyIntlMessages(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"user string becomes a typed block",
			`[{"role":"user","content":"hi"}]`,
			`[{"role":"user","content":[{"type":"text","text":"hi"}]}]`},
		{"empty user string is still a block",
			`[{"role":"user","content":""}]`,
			`[{"role":"user","content":[{"text":"","type":"text"}]}]`},
		{"caller system message stays and is not replaced",
			`[{"role":"system","content":"Be terse."},{"role":"user","content":"hi"}]`,
			`[{"role":"system","content":"Be terse."},{"role":"user","content":[{"type":"text","text":"hi"}]}]`},
		{"developer maps to system, other fields kept",
			`[{"role":"developer","content":"Be terse.","name":"ops"},{"role":"user","content":"hi"}]`,
			`[{"content":"Be terse.","name":"ops","role":"system"},{"role":"user","content":[{"type":"text","text":"hi"}]}]`},
		{"system parts array kept verbatim",
			`[{"role":"system","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]},{"role":"user","content":"hi"}]`,
			`[{"role":"system","content":[{"type":"text","text":"a"},{"type":"text","text":"b"}]},{"role":"user","content":[{"type":"text","text":"hi"}]}]`},
		{"later system messages lead, in order, none dropped",
			`[{"role":"user","content":"hi"},{"role":"system","content":"s1"},{"role":"assistant","content":"ok"},{"role":"developer","content":"s2"}]`,
			`[{"role":"system","content":"s1"},{"content":"s2","role":"system"},{"role":"user","content":[{"type":"text","text":"hi"}]},{"role":"assistant","content":"ok"}]`},
		{"user array content, assistant, tool pass through",
			`[{"role":"user","content":[{"type":"text","text":"hi"}]},{"role":"assistant","content":null,"tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c","content":"r"}]`,
			`[{"role":"user","content":[{"type":"text","text":"hi"}]},{"role":"assistant","content":null,"tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}]},{"role":"tool","tool_call_id":"c","content":"r"}]`},
		{"user with null or number content untouched",
			`[{"role":"user","content":null},{"role":"user","content":7}]`,
			`[{"role":"user","content":null},{"role":"user","content":7}]`},
		{"non-object elements stay where they are",
			`[null,"x",3,{"role":"user","content":"hi"}]`,
			`[null,"x",3,{"role":"user","content":[{"type":"text","text":"hi"}]}]`},
		{"role missing or not a string is not a system message",
			`[{"content":"a"},{"role":7,"content":"b"}]`,
			`[{"content":"a"},{"role":7,"content":"b"}]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out := finalize(t, "codebuddy-intl", chat, `{"model":"glm-5.2","stream":true,"messages":`+tc.in+`}`)
			var got struct{ Messages json.RawMessage }
			if err := json.Unmarshal([]byte(out), &got); err != nil {
				t.Fatal(err)
			}
			if !jsonEqual(t, string(got.Messages), tc.want) {
				t.Fatalf("messages\n got %s\nwant %s", got.Messages, tc.want)
			}
		})
	}
}

// messages that is not an array is never replaced by a fabricated one; the other
// quirks still run.
func TestCodeBuddyIntlNeverFabricatesMessages(t *testing.T) {
	for _, messages := range []string{`"messages":null,`, `"messages":"hi",`, `"messages":{"role":"user"},`, `"messages":7,`, `"messages":false,`, `"messages":"",`, ``} {
		out := finalize(t, "codebuddy-intl", chat, `{`+messages+`"model":"glm-5.2","reasoning_effort":"high"}`)
		var raw map[string]json.RawMessage
		_ = json.Unmarshal([]byte(out), &raw)
		got, present := raw["messages"]
		want := strings.TrimSuffix(strings.TrimPrefix(messages, `"messages":`), ",")
		if messages == "" && present || messages != "" && string(got) != want {
			t.Fatalf("messages = %s (present=%v) from %q: %s", got, present, messages, out)
		}
		if string(raw["stream"]) != "true" || string(raw["reasoning_summary"]) != `"auto"` {
			t.Fatalf("one unmappable field disabled the unrelated quirks: %s", out)
		}
	}
	// An empty array stays empty: no one-message body is made up.
	if out := finalize(t, "codebuddy-intl", chat, `{"model":"m","stream":true,"messages":[]}`); !strings.Contains(out, `"messages":[]`) {
		t.Fatalf("empty messages changed: %s", out)
	}
}

func TestCodeBuddyCNLeavesSystemPromptsVerbatim(t *testing.T) {
	long := strings.Repeat("You are Claude Code, Anthropic's official CLI. ", 100) // over 2000 chars and an agent pattern
	for _, system := range []string{`"` + long + `"`, `"You are Cursor"`, `[{"type":"text","text":"<Role>x</Role>"}]`} {
		in := `{"model":"glm-5.2","stream":true,"messages":[{"role":"system","content":` + system + `},{"role":"user","content":"hi"}]}`
		if out := finalize(t, "codebuddy-cn", chat, in); out != in {
			t.Fatalf("a CN system prompt was rewritten:\n%s", out)
		}
	}
	// CN also keeps developer and plain user string content as sent.
	in := `{"model":"glm-5.2","stream":true,"messages":[{"role":"developer","content":"d"},{"role":"user","content":"hi"}]}`
	if out := finalize(t, "codebuddy-cn", chat, in); out != in {
		t.Fatalf("CN messages rewritten: %s", out)
	}
}

func TestCodeBuddyReasoningPlaceholder(t *testing.T) {
	assistant := func(extra string) string { return `{"role":"assistant",` + extra + `"content":"a"}` }
	calls := `"tool_calls":[{"id":"c","type":"function","function":{"name":"f","arguments":"{}"}}],`
	cases := []struct {
		name, model, message string
		injected             bool
	}{
		{"deepseek plain assistant", "deepseek-v4-pro", assistant(``), true},
		{"deepseek with tool calls", "deepseek-v4.1-flash", assistant(calls), true},
		{"deepseek upper case", "DeepSeek-V4", assistant(``), true},
		{"kimi with tool calls", "kimi-k2.7", assistant(calls), true},
		{"kimi plain assistant", "kimi-k3-1", assistant(``), false},
		{"kimi empty tool_calls", "kimi-k2.6", assistant(`"tool_calls":[],`), false},
		{"kimi null tool_calls", "kimi-k2.6", assistant(`"tool_calls":null,`), false},
		{"kimi tool_calls wrong type", "kimi-k2.6", assistant(`"tool_calls":"x",`), false},
		{"glm never", "glm-5.2", assistant(``), false},
		{"not at the start of the id", "my-kimi-clone", assistant(calls), false},
		{"existing text kept", "deepseek-v4-pro", assistant(`"reasoning_content":"why",`), false},
		{"null reasoning filled", "deepseek-v4-pro", assistant(`"reasoning_content":null,`), true},
		{"empty reasoning filled", "deepseek-v4-pro", assistant(`"reasoning_content":"",`), true},
		{"reasoning of another type left to the gateway", "deepseek-v4-pro", assistant(`"reasoning_content":{"x":1},`), false},
		{"user message never", "deepseek-v4-pro", `{"role":"user","content":"u"}`, false},
	}
	for _, tc := range cases {
		eachCodeBuddy(t, func(t *testing.T, id string) {
			t.Run(tc.name, func(t *testing.T) {
				in := `{"model":"` + tc.model + `","stream":true,"messages":[` + tc.message + `]}`
				out := finalize(t, id, chat, in)
				var got struct {
					Messages []map[string]json.RawMessage
				}
				if err := json.Unmarshal([]byte(out), &got); err != nil {
					t.Fatal(err)
				}
				var before struct {
					Messages []map[string]json.RawMessage
				}
				_ = json.Unmarshal([]byte(in), &before)
				value, present := got.Messages[0]["reasoning_content"]
				switch {
				case tc.injected && string(value) != `" "`:
					t.Fatalf("placeholder missing: %s", out)
				case !tc.injected && string(value) != string(before.Messages[0]["reasoning_content"]):
					t.Fatalf("reasoning_content changed to %s (present=%v): %s", value, present, out)
				}
			})
		})
	}
}

// The placeholder is generic finalize behaviour (sanitize.go), chosen by model
// or by the provider's registry rule, not by the CodeBuddy hooks: a model that
// needs no placeholder gets none from any provider.
func TestPlaceholderIsNotCodeBuddySpecific(t *testing.T) {
	in := `{"model":"gpt-4o","messages":[{"role":"assistant","content":"a"}]}`
	for _, id := range []string{"kimi", "openai", "mistral", "codebuddy-cn", "codebuddy-intl"} {
		if out := finalize(t, id, chat, in); strings.Contains(out, "reasoning_content") {
			t.Fatalf("%s gained a placeholder: %s", id, out)
		}
	}
}

func TestCodeBuddyFrequencyLimit(t *testing.T) {
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	at := func(s string) string { return mustTime(t, s).UTC().Format(time.RFC3339) }
	cases := []struct {
		name, body string
		limit      bool
		resetAt    string
	}{
		{"cn code and text, default zone", `{"code":6004,"message":"当前模型超出频率限制，请于 2026-09-28 14:30:00 后重试"}`, true, at("2026-09-28T14:30:00+08:00")},
		{"intl text only, UTC+0", `{"code":11000,"msg":"frequency limit exceeded, retry after 2026-09-28 12:00:00 UTC+0"}`, true, at("2026-09-28T12:00:00+00:00")},
		{"zone with minutes", `{"code":6004,"message":"frequency limit until 2026-09-28 12:00:00 UTC+05:30"}`, true, at("2026-09-28T12:00:00+05:30")},
		{"zone with one-digit hour and minutes", `{"code":6004,"message":"until 2026-09-28 12:00:00 UTC+5:30"}`, true, at("2026-09-28T12:00:00+05:30")},
		{"zone hours only", `{"code":6004,"message":"until 2026-09-28 12:00:00 UTC+8"}`, true, at("2026-09-28T12:00:00+08:00")},
		{"negative zone is not read as +08:00", `{"code":6004,"message":"until 2026-09-28 12:00:00 UTC-5"}`, true, at("2026-09-28T12:00:00-05:00")},
		{"zone out of range gives no reset", `{"code":6004,"message":"until 2026-09-28 12:00:00 UTC+99"}`, true, ""},
		{"minutes out of range gives no reset", `{"code":6004,"message":"until 2026-09-28 12:00:00 UTC+05:99"}`, true, ""},
		{"impossible date gives no reset", `{"code":6004,"message":"until 2026-13-45 12:00:00"}`, true, ""},
		{"reset in the past is ignored", `{"code":6004,"message":"until 2026-09-27 12:00:00 UTC+0"}`, true, ""},
		{"no timestamp", `{"code":6004}`, true, ""},
		{"code as a string", `{"code":"6004"}`, true, ""},
		{"code as a float", `{"code":6004.0}`, true, ""},
		{"quota word in the message", `{"code":1,"message":"月度限额已用完"}`, true, ""},
		{"error.message", `{"error":{"message":"Frequency Limit reached"}}`, true, ""},
		{"not a limit: stream rejection", `{"code":11101,"message":"Non-stream chat request is currently not supported"}`, false, ""},
		{"other code", `{"code":6003,"message":"nope"}`, false, ""},
		{"code as the wrong type", `{"code":[6004]}`, false, ""},
		{"code null", `{"code":null,"message":"ok"}`, false, ""},
		{"not JSON", `not JSON`, false, ""},
		{"empty body", ``, false, ""},
		{"JSON array", `[6004]`, false, ""},
		{"JSON null", `null`, false, ""},
		{"truncated JSON", `{"code":6004,"message":"freq`, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := ClassifyError("codebuddy-cn", 400, []byte(tc.body), now)
			if ok != tc.limit {
				t.Fatalf("limit=%v want %v", ok, tc.limit)
			}
			if !ok {
				return
			}
			if got.Outcome != routing.OutcomeQuotaLock || got.ResetAt != tc.resetAt {
				t.Fatalf("got %+v want reset %q", got, tc.resetAt)
			}
		})
	}
}

// A non-string message at every JSON type: the pin's .match throws on these and
// the limit is missed. Code 6004 alone is a limit, and no type may panic or
// invent a reset.
func TestCodeBuddyFrequencyLimitMessageOfEveryType(t *testing.T) {
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	for _, field := range []string{"msg", "message"} {
		for name, value := range map[string]string{
			"object": `{"a":1}`, "array": `["x"]`, "number": `42`, "zero": `0`, "null": `null`, "true": `true`, "false": `false`, "empty string": `""`,
		} {
			t.Run(field+"/"+name, func(t *testing.T) {
				got, ok := ClassifyError("codebuddy-intl", 400, []byte(`{"code":6004,"`+field+`":`+value+`}`), now)
				if !ok || got.Outcome != routing.OutcomeQuotaLock || got.ResetAt != "" {
					t.Fatalf("got %+v ok=%v", got, ok)
				}
				// Without the code, the same non-text message is not a limit.
				if _, ok := ClassifyError("codebuddy-intl", 400, []byte(`{"code":1,"`+field+`":`+value+`}`), now); ok {
					t.Fatal("a non-text message was read as a limit")
				}
			})
		}
	}
	// error.message of every type, and error itself not an object.
	for name, value := range map[string]string{"object": `{"a":1}`, "array": `[]`, "number": `1`, "null": `null`, "bool": `true`} {
		if got, ok := ClassifyError("codebuddy-cn", 400, []byte(`{"code":6004,"error":{"message":`+value+`}}`), now); !ok || got.ResetAt != "" {
			t.Fatalf("error.message %s: %+v %v", name, got, ok)
		}
	}
	for name, value := range map[string]string{"string": `"x"`, "number": `1`, "array": `[]`, "null": `null`} {
		if _, ok := ClassifyError("codebuddy-cn", 400, []byte(`{"code":6004,"error":`+value+`}`), now); !ok {
			t.Fatalf("error as %s hid the code", name)
		}
	}
	// The first non-empty string wins; a non-string earlier field does not hide a later one.
	got, ok := ClassifyError("codebuddy-cn", 400, []byte(`{"msg":{"x":1},"message":"until 2026-09-28 12:00:00 UTC+0"}`), now)
	if ok {
		t.Fatalf("no code and no limit text, got %+v", got)
	}
	got, ok = ClassifyError("codebuddy-cn", 400, []byte(`{"code":6004,"msg":{"x":1},"message":"frequency limit until 2026-09-28 12:00:00 UTC+0"}`), now)
	if !ok || got.ResetAt != "2026-09-28T12:00:00Z" {
		t.Fatalf("got %+v %v", got, ok)
	}
}

func TestClassifyErrorIsScopedToItsProvider(t *testing.T) {
	if _, ok := ClassifyError("openai", 400, []byte(`{"code":6004}`), time.Now()); ok {
		t.Fatal("another provider's error was read as a CodeBuddy limit")
	}
}

func TestStripEmptyToolCalls(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"empty array removed", `data: {"choices":[{"index":0,"delta":{"content":"x","tool_calls":[]}}]}`, `data: {"choices":[{"delta":{"content":"x"},"index":0}]}`},
		{"empty array with spaces", `data: {"choices":[{"index":0,"delta":{"tool_calls": [ ]}}]}`, `data: {"choices":[{"delta":{},"index":0}]}`},
		{"every choice", `data: {"choices":[{"delta":{"tool_calls":[]}},{"delta":{"tool_calls":[]}}]}`, `data: {"choices":[{"delta":{}},{"delta":{}}]}`},
		{"one choice of two", `data: {"choices":[{"delta":{"tool_calls":[]}},{"delta":{"tool_calls":[{"index":0}]}}]}`, `data: {"choices":[{"delta":{}},{"delta":{"tool_calls":[{"index":0}]}}]}`},
		{"populated kept", `data: {"choices":[{"delta":{"tool_calls":[{"index":0}]}}]}`, ``},
		{"null kept", `data: {"choices":[{"delta":{"tool_calls":null}}]}`, ``},
		{"object kept", `data: {"choices":[{"delta":{"tool_calls":{}}}]}`, ``},
		{"string kept", `data: {"choices":[{"delta":{"tool_calls":"[]"}}]}`, ``},
		{"not a delta", `data: {"choices":[{"message":{"tool_calls":[]}}]}`, ``},
		{"content that mentions it", `data: {"choices":[{"delta":{"content":"\"tool_calls\":[]"}}]}`, ``},
		{"no choices", `data: {"tool_calls":[]}`, ``},
		{"choices wrong type", `data: {"choices":{"tool_calls":[]}}`, ``},
		{"delta wrong type", `data: {"choices":[{"delta":"tool_calls"}]}`, ``},
		{"malformed", `data: {"choices":[{"delta":{"tool_calls":[]}`, ``},
		{"done", `data: [DONE]`, ``},
		{"not a data line", `: "tool_calls":[]`, ``},
		{"event line", `event: "tool_calls"`, ``},
		{"blank", ``, ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			want := tc.want
			if want == "" {
				want = tc.in
			}
			for _, ending := range []string{"\n", "\r\n", ""} {
				if got := string(stripEmptyToolCalls([]byte(tc.in + ending))); got != want+ending {
					t.Fatalf("ending %q\n got %q\nwant %q", ending, got, want+ending)
				}
			}
		})
	}
}

type countingCloser struct {
	io.Reader
	closed bool
}

func (c *countingCloser) Close() error { c.closed = true; return nil }

func TestResponseStreamRewritesLinesAndPassesTheRest(t *testing.T) {
	stream := "data: {\"choices\":[{\"delta\":{\"content\":\"a\",\"tool_calls\":[]}}]}\n\n" +
		": keepalive\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0}]}}]}\n\n" +
		"data: [DONE]\n\n" +
		"data: tail without a newline"
	source := &countingCloser{Reader: strings.NewReader(stream)}
	wrapped := ResponseStream("codebuddy-cn", source)
	out, err := io.ReadAll(wrapped)
	if err != nil {
		t.Fatal(err)
	}
	want := "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}\n\n" +
		": keepalive\n\n" +
		"data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0}]}}]}\n\n" +
		"data: [DONE]\n\n" +
		"data: tail without a newline"
	if string(out) != want {
		t.Fatalf("got %q\nwant %q", out, want)
	}
	if wrapped.Close(); !source.closed {
		t.Fatal("closing the wrapper did not close the upstream body")
	}
	// A provider with no rewrite gets its body back untouched.
	plain := &countingCloser{Reader: strings.NewReader("x")}
	if ResponseStream("openai", plain) != io.ReadCloser(plain) {
		t.Fatal("a provider with no stream rewrite was wrapped")
	}
}

func TestResponseStreamRelaysAnOversizedLineUnchanged(t *testing.T) {
	long := "data: {\"choices\":[{\"delta\":{\"content\":\"" + strings.Repeat("x", 3*maxStreamLine/2) + "\",\"tool_calls\":[]}}]}\n"
	out, err := io.ReadAll(ResponseStream("codebuddy-intl", &countingCloser{Reader: strings.NewReader(long + "data: [DONE]\n")}))
	if err != nil || string(out) != long+"data: [DONE]\n" {
		t.Fatalf("oversized line altered or read failed: %v (%d bytes)", err, len(out))
	}
}

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	moment, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatal(err)
	}
	return moment
}

func jsonEqual(t *testing.T, a, b string) bool {
	t.Helper()
	var x, y any
	if json.Unmarshal([]byte(a), &x) != nil || json.Unmarshal([]byte(b), &y) != nil {
		t.Fatalf("not JSON: %s / %s", a, b)
	}
	left, _ := json.Marshal(x)
	right, _ := json.Marshal(y)
	return string(left) == string(right)
}

func TestResponseStreamRewritesLinesLargerThanItsReadBuffer(t *testing.T) {
	line := `data: {"choices":[{"delta":{"content":"` + strings.Repeat("x", 64<<10) + `","tool_calls":[]}}]}` + "\n"
	out, err := io.ReadAll(ResponseStream("codebuddy-cn", &countingCloser{Reader: strings.NewReader(line)}))
	if err != nil || strings.Contains(string(out), "tool_calls") || !strings.Contains(string(out), strings.Repeat("x", 64<<10)) {
		t.Fatalf("line was lost or not rewritten: %v (%d bytes)", err, len(out))
	}
}

func TestCodeBuddyRequestGuardsPreserveOpaqueValues(t *testing.T) {
	for _, field := range []string{"messages", "reasoning_effort", "reasoning_summary", "stream_options"} {
		for _, value := range []string{`null`, `""`, `0`, `false`, `{}`, `[]`, `"` + strings.Repeat("x", 1<<20) + `"`} {
			for _, id := range codebuddyIDs {
				out := finalize(t, id, chat, `{"model":"glm-5.2","stream":true,"`+field+`":`+value+`}`)
				var fields Body
				if err := json.Unmarshal([]byte(out), &fields); err != nil || string(fields[field]) != value {
					t.Fatalf("%s changed %s (%d bytes): %v", id, field, len(value), err)
				}
			}
		}
	}
	for _, raw := range []string{"", `null`, `[]`, `{`, `{"messages":[`, `{"reasoning_effort":"high"`, `{"messages":tru}`} {
		for _, id := range codebuddyIDs {
			if out := finalize(t, id, chat, raw); out != raw {
				t.Fatalf("%s rewrote malformed body %q: %q", id, raw, out)
			}
		}
	}
}

func TestCodeBuddyResetRejectsPartialOrInvalidZones(t *testing.T) {
	for _, zone := range []string{"UTC+14:01", "UTC+999", "UTC+12345", "UTC+05:300", "UTC+foo", "UTC+", "UTC"} {
		if reset, ok := codebuddyResetTime("until 2026-09-28 12:00:00 " + zone); ok {
			t.Fatalf("invented reset %v for %q", reset, zone)
		}
	}
	if reset, ok := codebuddyResetTime("until 2026-09-28 12:00:00 utc-5"); !ok || !reset.Equal(mustTime(t, "2026-09-28T17:00:00Z")) {
		t.Fatalf("lowercase zone: %v %v", reset, ok)
	}
}

func TestCodeBuddyPlaceholderPreservesEveryWrongType(t *testing.T) {
	for _, field := range []string{"model", "reasoning_content", "tool_calls", "role"} {
		for _, value := range []string{`null`, `""`, `0`, `false`, `{}`, `[]`} {
			body := Body{"model": json.RawMessage(`"kimi-k2.7"`), "stream": json.RawMessage(`true`)}
			message := Body{"role": json.RawMessage(`"assistant"`), "reasoning_content": json.RawMessage(`"existing"`), "tool_calls": json.RawMessage(`[{"id":"c"}]`)}
			if field == "model" {
				body[field] = json.RawMessage(value)
			} else {
				message[field] = json.RawMessage(value)
			}
			body["messages"], _ = json.Marshal([]Body{message})
			in, _ := json.Marshal(body)
			out := finalize(t, "codebuddy-cn", chat, string(in))
			want := in
			if field == "reasoning_content" && (value == `null` || value == `""`) {
				message[field] = json.RawMessage(`" "`)
				body["messages"], _ = json.Marshal([]Body{message})
				want, _ = json.Marshal(body)
			}
			if out != string(want) {
				t.Fatalf("%s=%s changed opaque history: %s", field, value, out)
			}
		}
	}
}
