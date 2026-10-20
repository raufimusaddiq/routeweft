package wire

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	anthropicadapter "github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

const (
	chat      = openaiadapter.ChatProtocol
	responses = openaiadapter.ResponsesProtocol
	messages  = anthropicadapter.MessagesProtocol
)

func finalize(t *testing.T, id, protocol, body string) string {
	t.Helper()
	return string(FinalizeBody(routing.ProviderRef{ProviderID: id, Protocol: protocol}, []byte(body)))
}

func same(t *testing.T, got, want string) {
	t.Helper()
	var g, w any
	if json.Unmarshal([]byte(got), &g) != nil || json.Unmarshal([]byte(want), &w) != nil {
		t.Fatalf("not json: got %s want %s", got, want)
	}
	if !reflect.DeepEqual(g, w) {
		t.Errorf("got  %s\nwant %s", got, want)
	}
}

func TestDropClientMetadata(t *testing.T) {
	in := `{"model":"m","client_metadata":{"a":1},"messages":[],"future_field":{"x":[1,2]}}`
	for _, id := range []string{"cerebras", "mistral"} {
		same(t, finalize(t, id, chat, in), `{"model":"m","messages":[],"future_field":{"x":[1,2]}}`)
	}
	// Other chat providers keep it.
	if got := finalize(t, "openai", chat, in); got != in {
		t.Errorf("openai body changed: %s", got)
	}
}

func TestMinimaxDropsOutputConfigAndTypesTools(t *testing.T) {
	in := `{"model":"m","output_config":{"effort":"high"},"tools":[{"name":"a","input_schema":{}},{"name":"b","type":"custom"},{"type":"web_search_20250305","name":"s"}]}`
	for _, id := range []string{"minimax", "minimax-cn"} {
		same(t, finalize(t, id, messages, in), `{"model":"m","tools":[{"name":"a","input_schema":{},"type":"custom"},{"name":"b","type":"custom"},{"type":"web_search_20250305","name":"s"}]}`)
		// Not a Messages request: untouched.
		if got := finalize(t, id, chat, in); got != in {
			t.Errorf("%s chat body changed: %s", id, got)
		}
	}
	// A provider without the quirk must not gain "custom" (DeepSeek answers 400).
	if got := finalize(t, "glm", messages, in); got != in {
		t.Errorf("glm body changed: %s", got)
	}
}

func TestDeepseekKeepsOnlySupportedToolTypes(t *testing.T) {
	in := `{"model":"m","tool_choice":{"type":"auto"},"tools":[{"name":"f","input_schema":{}},{"name":"c","type":"custom","input_schema":{}},{"type":"web_search_20260209","name":"w"},{"type":"bash_20250124","name":"b"}]}`
	same(t, finalize(t, "deepseek", messages, in), `{"model":"m","tool_choice":{"type":"auto"},"tools":[{"name":"f","input_schema":{}},{"type":"web_search_20260209","name":"w"}]}`)
	// Everything unsupported: tools and tool_choice go together.
	only := `{"model":"m","tool_choice":{"type":"auto"},"tools":[{"name":"c","type":"custom"}]}`
	same(t, finalize(t, "deepseek", messages, only), `{"model":"m"}`)
	// DeepSeek's Chat transport is unaffected.
	if got := finalize(t, "deepseek", chat, in); got != in {
		t.Errorf("deepseek chat body changed: %s", got)
	}
	// Nothing to drop: byte-identical.
	ok := `{"model":"m","tools":[{"name":"f"}]}`
	if got := finalize(t, "deepseek", messages, ok); got != ok {
		t.Errorf("unchanged body was re-encoded: %s", got)
	}
}

func TestKenariFoldsSystemIntoFirstUserTurn(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"string user content",
			`{"model":"m","instructions":"  be brief ","input":[{"type":"message","role":"system","content":"sys"},{"type":"message","role":"user","content":"hi"}]}`,
			`{"model":"m","input":[{"type":"message","role":"user","content":"be brief\n\nsys\n\nhi"}]}`},
		{"part user content and part system",
			`{"model":"m","input":[{"type":"message","role":"system","content":[{"type":"input_text","text":"a"},{"type":"text","text":"b"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`,
			`{"model":"m","input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"a\nb\n\n"},{"type":"input_text","text":"hi"}]}]}`},
		{"empty system message is removed",
			`{"model":"m","input":[{"type":"message","role":"system","content":""},{"type":"message","role":"user","content":"hi"}]}`,
			`{"model":"m","input":[{"type":"message","role":"user","content":"hi"}]}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) { same(t, finalize(t, "kenari", responses, c.in), c.want) })
	}
	// instructions alone are folded too (default.js:32), system message or not.
	same(t, finalize(t, "kenari", responses, `{"model":"m","instructions":"keep","input":[{"type":"message","role":"user","content":"hi"}]}`),
		`{"model":"m","input":[{"type":"message","role":"user","content":"keep\n\nhi"}]}`)
	plain := `{"model":"m","input":[{"type":"message","role":"user","content":"hi"}]}`
	if got := finalize(t, "kenari", responses, plain); got != plain {
		t.Errorf("nothing to fold: body changed: %s", got)
	}
	// Chat and Messages bodies have no `input`; untouched.
	for _, p := range []string{chat, messages} {
		in := `{"model":"m","messages":[{"role":"system","content":"s"}]}`
		if got := finalize(t, "kenari", p, in); got != in {
			t.Errorf("%s body changed: %s", p, got)
		}
	}
	// Divergence from the pin: nowhere to fold means the body is left alone, not
	// stripped of its system text.
	orphan := `{"model":"m","instructions":"keep","input":[{"type":"message","role":"system","content":"only system"}]}`
	if got := finalize(t, "kenari", responses, orphan); got != orphan {
		t.Errorf("system text was dropped: %s", got)
	}
}

func TestFinalizeIsFailOpenAndIdempotent(t *testing.T) {
	for _, id := range []string{"cerebras", "minimax", "deepseek", "kenari"} {
		for _, bad := range []string{"", "not json", "[1,2]", "null", `{"tools":"x","input":7}`} {
			if got := finalize(t, id, messages, bad); got != bad {
				t.Errorf("%s: %q changed to %q", id, bad, got)
			}
		}
	}
	in := `{"output_config":{},"tools":[{"name":"a"}]}`
	once := FinalizeBody(routing.ProviderRef{ProviderID: "minimax", Protocol: messages}, []byte(in))
	twice := FinalizeBody(routing.ProviderRef{ProviderID: "minimax", Protocol: messages}, once)
	if !bytes.Equal(once, twice) {
		t.Errorf("not idempotent: %s then %s", once, twice)
	}
}

// Providers with no quirk never have their body re-encoded.
func TestFinalizeLeavesEveryOtherProviderByteIdentical(t *testing.T) {
	in := []byte(`{ "model": "m",  "client_metadata": 1, "output_config": {}, "tools": [{"name":"a"}], "input": [{"type":"message","role":"system","content":"s"}] }`)
	quirky := map[string]bool{}
	for _, id := range Registered() {
		if For(id).Finalize != nil {
			quirky[id] = true
		}
	}
	for _, id := range providerIDs() {
		if quirky[id] {
			continue
		}
		for _, p := range []string{chat, responses, messages} {
			if got := FinalizeBody(routing.ProviderRef{ProviderID: id, Protocol: p}, in); !bytes.Equal(got, in) {
				t.Errorf("%s/%s: body changed", id, p)
			}
		}
	}
}

// Every legal Responses `input` shape (openai/responses.go accepts any non-null
// value) either folds or comes back byte-identical, and a body the quirk changes
// never carries a system role, `instructions`, or a null/empty `input`.
func TestKenariFoldEnumeratesEveryInputShape(t *testing.T) {
	const user = `{"type":"message","role":"user","content":"hi"}`
	cases := []struct {
		name, in string
		want     string // empty: must come back byte-identical
	}{
		{"string input, instructions", `{"model":"m","instructions":"sys","input":"hi"}`,
			`{"model":"m","input":[{"type":"message","role":"user","content":"sys\n\nhi"}]}`},
		{"string input, blank instructions", `{"model":"m","instructions":"  ","input":"hi"}`, ""},
		{"string input, no instructions", `{"model":"m","input":"hi"}`, ""},
		{"empty string input, instructions", `{"model":"m","instructions":"sys","input":""}`,
			`{"model":"m","input":[{"type":"message","role":"user","content":"sys\n\n"}]}`},
		{"array, user only", `{"model":"m","input":[` + user + `]}`, ""},
		{"array, instructions only", `{"model":"m","instructions":"sys","input":[` + user + `]}`,
			`{"model":"m","input":[{"type":"message","role":"user","content":"sys\n\nhi"}]}`},
		{"array, system then user", `{"model":"m","input":[{"type":"message","role":"system","content":"s"},` + user + `]}`,
			`{"model":"m","input":[{"type":"message","role":"user","content":"s\n\nhi"}]}`},
		{"array, typeless system and user",
			`{"model":"m","input":[{"role":"system","content":"s"},{"role":"user","content":"hi"}]}`,
			`{"model":"m","input":[{"role":"user","content":"s\n\nhi"}]}`},
		{"array with non-message items", `{"model":"m","instructions":"sys","input":[{"type":"function_call_output","call_id":"c","output":"o"},` + user + `]}`,
			`{"model":"m","input":[{"type":"function_call_output","call_id":"c","output":"o"},{"type":"message","role":"user","content":"sys\n\nhi"}]}`},
		{"array, textless system plus user", `{"model":"m","input":[{"type":"message","role":"system","content":""},` + user + `]}`,
			`{"model":"m","input":[` + user + `]}`},
		{"empty array", `{"model":"m","input":[]}`, ""},
		{"empty array, instructions", `{"model":"m","instructions":"sys","input":[]}`, ""},
		{"only textless system", `{"model":"m","input":[{"type":"message","role":"system","content":""}]}`, ""},
		{"only system with text", `{"model":"m","input":[{"type":"message","role":"system","content":"s"}]}`, ""},
		{"system and non-message, no user", `{"model":"m","input":[{"type":"message","role":"system","content":"s"},{"type":"reasoning","id":"r"}]}`, ""},
		{"assistant only, instructions", `{"model":"m","instructions":"sys","input":[{"type":"message","role":"assistant","content":"a"}]}`, ""},
		{"object input", `{"model":"m","instructions":"sys","input":{"a":1}}`, ""},
		{"number input", `{"model":"m","instructions":"sys","input":7}`, ""},
		{"null input", `{"model":"m","instructions":"sys","input":null}`, ""},
		{"no input", `{"model":"m","instructions":"sys"}`, ""},
		{"non-string instructions", `{"model":"m","instructions":[1],"input":"hi"}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := finalize(t, "kenari", responses, c.in)
			if c.want == "" {
				if got != c.in {
					t.Fatalf("expected byte-identical\n got %s\n  in %s", got, c.in)
				}
				return
			}
			same(t, got, c.want)
			var out struct {
				Instructions json.RawMessage   `json:"instructions"`
				Input        []json.RawMessage `json:"input"`
			}
			if err := json.Unmarshal([]byte(got), &out); err != nil || len(out.Input) == 0 || out.Instructions != nil {
				t.Fatalf("invalid result %s (%v)", got, err)
			}
			if bytes.Contains([]byte(got), []byte(`"role":"system"`)) {
				t.Fatalf("system role survived: %s", got)
			}
		})
	}
}

// quirk × body-state cross products. Each quirk removes or rewrites a field; the
// bug class is a quirk that changes one half of a pair (tools/tool_choice,
// instructions/input, text/content) and leaves the other half dangling or loses
// what was in it. These tables cross every field the quirk can touch with every
// state the body can be in and check an invariant on each combination.

func decode(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatalf("invalid json %q: %v", raw, err)
	}
	return out
}

func TestDeepseekToolsCrossToolChoice(t *testing.T) {
	const (
		fn  = `{"name":"f","input_schema":{}}`
		web = `{"type":"web_search_20250305","name":"w"}`
		bad = `{"name":"c","type":"custom","input_schema":{}}`
	)
	toolSets := map[string]string{
		"none dropped": `[` + fn + `,` + web + `]`,
		"some dropped": `[` + web + `,` + bad + `,` + fn + `]`,
		"all dropped":  `[` + bad + `]`,
		"empty":        `[]`,
	}
	choices := map[string]string{
		"absent": "", "null": `"tool_choice":null,`,
		"auto": `"tool_choice":{"type":"auto"},`, "any": `"tool_choice":{"type":"any"},`,
		"none": `"tool_choice":{"type":"none"},`, "string": `"tool_choice":"auto",`,
		"names dropped":  `"tool_choice":{"type":"tool","name":"c"},`,
		"names kept web": `"tool_choice":{"type":"tool","name":"w"},`,
		"names kept fn":  `"tool_choice":{"type":"tool","name":"f"},`,
		"names unknown":  `"tool_choice":{"type":"tool","name":"zzz"},`,
	}
	for setName, tools := range toolSets {
		for choiceName, choice := range choices {
			t.Run(setName+"/"+choiceName, func(t *testing.T) {
				in := `{"model":"m",` + choice + `"tools":` + tools + `}`
				out := finalize(t, "deepseek", messages, in)
				if setName == "none dropped" || setName == "empty" {
					if out != in {
						t.Fatalf("nothing to drop, body changed: %s", out)
					}
					return
				}
				got := decode(t, out)
				if setName == "all dropped" {
					if _, ok := got["tools"]; ok {
						t.Fatalf("emptied tools stayed: %s", out)
					}
					if _, ok := got["tool_choice"]; ok {
						t.Fatalf("tool_choice outlived its tools: %s", out)
					}
					return
				}
				var names []string
				for _, tool := range got["tools"].([]any) {
					names = append(names, tool.(map[string]any)["name"].(string))
				}
				if kind, _ := tool(got["tool_choice"])["type"].(string); kind == "tool" {
					name, _ := tool(got["tool_choice"])["name"].(string)
					if name != "zzz" && !contains(names, name) {
						t.Fatalf("tool_choice names %q, tools are %v: %s", name, names, out)
					}
				}
				for _, n := range names {
					if n == "c" {
						t.Fatalf("custom tool survived: %s", out)
					}
				}
				// "auto", "any", "none" and tools that survive are left in place.
				if choiceName == "auto" || choiceName == "any" || choiceName == "none" || choiceName == "names kept web" || choiceName == "names kept fn" {
					if _, ok := got["tool_choice"]; !ok {
						t.Fatalf("tool_choice %s was dropped though it names nothing removed: %s", choiceName, out)
					}
				}
			})
		}
	}
}

func tool(v any) map[string]any {
	m, _ := v.(map[string]any)
	if m == nil {
		return map[string]any{}
	}
	return m
}

func TestMinimaxToolTypeCrossOutputConfig(t *testing.T) {
	types := map[string]string{
		"missing": `{"name":"t"}`, "null": `{"name":"t","type":null}`, "empty": `{"name":"t","type":""}`,
		"custom": `{"name":"t","type":"custom"}`, "web": `{"name":"t","type":"web_search_20250305"}`, "number": `{"name":"t","type":5}`,
	}
	for typeName, tl := range types {
		for _, config := range []string{"", `"output_config":{"effort":"high"},`} {
			for _, protocol := range []string{messages, chat, responses} {
				for _, id := range []string{"minimax", "minimax-cn"} {
					t.Run(fmt.Sprintf("%s/%s/config=%t/%s", id, typeName, config != "", protocol), func(t *testing.T) {
						in := `{"model":"m",` + config + `"tools":[` + tl + `]}`
						out := finalize(t, id, protocol, in)
						if protocol != messages {
							if out != in {
								t.Fatalf("not a Messages request, body changed: %s", out)
							}
							return
						}
						got := decode(t, out)
						if _, ok := got["output_config"]; ok {
							t.Fatalf("output_config survived: %s", out)
						}
						kind := tool(got["tools"].([]any)[0])["type"]
						switch typeName {
						case "missing", "null", "empty":
							if kind != "custom" {
								t.Fatalf("typeless tool not typed: %s", out)
							}
						case "web":
							if kind != "web_search_20250305" {
								t.Fatalf("typed tool changed: %s", out)
							}
						case "number":
							if kind != float64(5) {
								t.Fatalf("client's non-string type changed: %s", out)
							}
						}
						if again := finalize(t, id, protocol, out); again != out {
							t.Fatalf("not idempotent: %s then %s", out, again)
						}
					})
				}
			}
		}
	}
}

// Whatever the fold does, no text the client sent may vanish: either the body
// comes back byte-identical, or it is valid, carries no system role or
// `instructions`, a non-empty `input`, and every original text string.
func TestKenariFoldCrossProductLosesNothing(t *testing.T) {
	instructions := map[string]string{
		"absent": "", "null": `"instructions":null,`, "empty": `"instructions":"",`, "blank": `"instructions":"  ",`,
		"text": `"instructions":"INSTR",`, "array": `"instructions":["INSTR"],`, "number": `"instructions":7,`,
	}
	userContent := map[string]string{
		"string": `"content":"USER"`, "parts": `"content":[{"type":"input_text","text":"USER"}]`,
		"object": `"content":{"type":"input_text","text":"USER"}`, "number": `"content":5`,
		"null": `"content":null`, "missing": ``,
	}
	systemContent := map[string]string{
		"string": `"content":"SYS"`, "parts": `"content":[{"type":"input_text","text":"SYS"},{"type":"text","text":"SYS2"}]`,
		"image part":     `"content":[{"type":"input_text","text":"SYS"},{"type":"input_image","image_url":"x"}]`,
		"text-less part": `"content":[{"type":"input_text"}]`,
		"object":         `"content":{"type":"input_text","text":"SYS"}`, "number": `"content":3`,
		"null": `"content":null`, "empty string": `"content":""`,
	}
	message := func(role, content, typ string) string {
		fields := []string{}
		if typ != "" {
			fields = append(fields, `"type":"`+typ+`"`)
		}
		fields = append(fields, `"role":"`+role+`"`)
		if content != "" {
			fields = append(fields, content)
		}
		return "{" + strings.Join(fields, ",") + "}"
	}
	var cases []string
	for ins, insField := range instructions {
		for uc, ucField := range userContent {
			for sc, scField := range systemContent {
				for _, typ := range []string{"message", ""} {
					for _, order := range []string{"sys-user", "user-sys", "user-only", "sys-only"} {
						var items []string
						switch order {
						case "sys-user":
							items = []string{message("system", scField, typ), message("user", ucField, typ)}
						case "user-sys":
							items = []string{message("user", ucField, typ), message("system", scField, typ)}
						case "user-only":
							items = []string{message("user", ucField, typ)}
						case "sys-only":
							items = []string{message("system", scField, typ)}
						}
						cases = append(cases, `{"model":"m",`+insField+`"input":[`+strings.Join(items, ",")+`]}`)
						_ = ins + uc + sc
					}
				}
			}
		}
	}
	changed := 0
	for _, in := range cases {
		out := finalize(t, "kenari", responses, in)
		if out == in {
			continue
		}
		changed++
		got := decode(t, out)
		input, ok := got["input"].([]any)
		if !ok || len(input) == 0 {
			t.Fatalf("empty or invalid input\n in %s\nout %s", in, out)
		}
		if value, ok := got["instructions"]; ok && value != nil && strings.TrimSpace(fmt.Sprint(value)) != "" { // a null or blank value is not an instruction
			t.Fatalf("instructions survived\n in %s\nout %s", in, out)
		}
		if strings.Contains(out, `"role":"system"`) {
			t.Fatalf("system role survived\n in %s\nout %s", in, out)
		}
		for _, text := range []string{"INSTR", "SYS", "SYS2", "USER"} {
			if strings.Contains(in, text) && !strings.Contains(out, text) {
				t.Fatalf("%q was lost\n in %s\nout %s", text, in, out)
			}
		}
	}
	if changed == 0 || changed == len(cases) {
		t.Fatalf("the fold changed %d of %d bodies; the table is not exercising both outcomes", changed, len(cases))
	}
	t.Logf("%d bodies, %d folded, %d left byte-identical", len(cases), changed, len(cases)-changed)
}
