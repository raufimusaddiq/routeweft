package claude

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestWithIdentityIsFirstOnceAndKeepsTheRest(t *testing.T) {
	identity := map[string]any{"type": "text", "text": IdentityPrompt}
	kept := map[string]any{"type": "text", "text": "x", "cache_control": map[string]any{"type": "ephemeral"}}
	cases := map[string][]any{
		`{"model":"m"}`:         {identity},
		`{"system":"be brief"}`: {identity, map[string]any{"type": "text", "text": "be brief"}},
		`{"system":[{"type":"text","text":"x","cache_control":{"type":"ephemeral"}}]}`: {identity, kept},
	}
	for in, want := range cases {
		got, err := WithIdentity([]byte(in))
		if err != nil {
			t.Fatal(err)
		}
		var decoded map[string]any
		_ = json.Unmarshal(got, &decoded)
		if !reflect.DeepEqual(decoded["system"], want) {
			t.Errorf("%s -> %s", in, got)
		}
		again, _ := WithIdentity(got)
		if string(again) != string(got) {
			t.Errorf("a second application must change nothing: %s", again)
		}
	}
}
