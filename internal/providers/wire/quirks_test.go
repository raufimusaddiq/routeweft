package wire

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

const (
	chat      = "openai-chat"
	responses = "openai-responses"
	messages  = "anthropic-messages"
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
			`{"model":"m","input":[{"type":"message","role":"system","content":[{"type":"input_text","text":"a"},{"type":"image"},{"type":"text","text":"b"}]},{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}]}`,
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
