package body

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/transforms"
	"github.com/raufimusaddiq/routeweft/internal/transforms/caveman"
	"github.com/raufimusaddiq/routeweft/internal/transforms/ponytail"
	"github.com/raufimusaddiq/routeweft/internal/transforms/rtk"
)

func TestWireFixtures(t *testing.T) {
	raw, err := os.ReadFile("testdata/wire.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Protocol        string
		Input, Expected json.RawMessage
	}
	if err := json.Unmarshal(raw, &fixtures); err != nil {
		t.Fatal(err)
	}
	pipeline := transforms.Pipeline{Steps: []transforms.Transform{rtk.Transform{Enabled: true}, caveman.Transform{Enabled: true, Level: "lite"}, ponytail.Transform{Enabled: true, Level: "lite"}}}
	for _, fixture := range fixtures {
		t.Run(fixture.Protocol, func(t *testing.T) {
			actual, err := Run(context.Background(), fixture.Protocol, fixture.Input, pipeline)
			if err != nil {
				t.Fatal(err)
			}
			var got, want any
			json.Unmarshal(actual, &got)
			json.Unmarshal(fixture.Expected, &want)
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %s\nwant %s", actual, fixture.Expected)
			}
			again, err := Run(context.Background(), fixture.Protocol, actual, pipeline)
			if err != nil || string(again) != string(actual) {
				t.Fatalf("not idempotent: %s / %s, %v", actual, again, err)
			}
		})
	}
}

func TestUnchangedNativeBodyRetainsBytesAndNumbers(t *testing.T) {
	raw := []byte(`{ "future": 9007199254740993, "messages":[{"role":"user","content":"hi"}] }`)
	p := Compile(map[string]string{"rtkEnabled": "true"})
	got, err := p.Run(context.Background(), "openai-chat", raw, false)
	if err != nil || string(got) != string(raw) {
		t.Fatalf("unchanged body reencoded: %s, %v", got, err)
	}
	p = Compile(map[string]string{"cavemanEnabled": "true"})
	got, err = p.Run(context.Background(), "openai-chat", raw, false)
	if err != nil || !strings.Contains(string(got), "9007199254740993") {
		t.Fatalf("unknown number rounded: %s, %v", got, err)
	}
	for _, protocol := range []string{"systemone", "future-protocol"} {
		got, err = p.Run(context.Background(), protocol, raw, false)
		if err != nil || string(got) != string(raw) {
			t.Fatalf("opaque %s body changed: %s, %v", protocol, got, err)
		}
	}
}

func TestUnknownPromptRepresentationsFailOpenWithoutReplacingFields(t *testing.T) {
	p := Compile(map[string]string{"cavemanEnabled": "true", "ponytailEnabled": "true"})
	for _, tc := range []struct{ protocol, raw string }{
		{"openai-responses", `{ "instructions":[{"type":"future","data":"keep"}], "input":"hi" }`},
		{"anthropic-messages", `{ "system":{"future":"keep"}, "messages":[{"role":"user","content":"hi"}] }`},
		{"gemini", `{ "systemInstruction":{"parts":{"future":"keep"}}, "contents":[] }`},
		{"gemini", `{ "systemInstruction":[{"future":"keep"}], "contents":[] }`},
	} {
		got, err := p.Run(context.Background(), tc.protocol, []byte(tc.raw), false)
		if err != nil || string(got) != tc.raw {
			t.Errorf("unsupported %s prompt changed: %s, %v", tc.protocol, got, err)
		}
	}
}
