package openai

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/compat/fixtures"
	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

func TestParseChatFixturesAndPreserveUnknownFields(t *testing.T) {
	exchanges, err := fixtures.Load()
	if err != nil {
		t.Fatal(err)
	}
	seen := map[fixtures.Kind]bool{}
	for _, exchange := range exchanges {
		if exchange.Protocol != fixtures.OpenAIChat {
			continue
		}
		switch exchange.Kind {
		case fixtures.KindNonStreaming, fixtures.KindStreaming, fixtures.KindCancellation:
		default:
			continue
		}
		request, err := ParseChatRequest([]byte(exchange.Request.Body))
		if err != nil {
			t.Fatalf("%s: %v", exchange.ID, err)
		}
		body, err := request.MarshalBody("")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(body, []byte(exchange.Request.Body)) {
			t.Fatalf("%s native body changed", exchange.ID)
		}
		seen[exchange.Kind] = true
	}
	for _, kind := range []fixtures.Kind{fixtures.KindNonStreaming, fixtures.KindStreaming, fixtures.KindCancellation} {
		if !seen[kind] {
			t.Errorf("no Chat Completions fixture for %s", kind)
		}
	}
}

func TestNativeModelRemapChangesOnlyModelValue(t *testing.T) {
	raw := []byte("{\"model\":\"client-alias\",\"messages\":[{\"role\":\"user\",\"content\":\"x\"}],\"future\":{\"n\":1},\"stream\":false}")
	request, err := ParseChatRequest(raw)
	if err != nil {
		t.Fatal(err)
	}
	outbound, err := request.MarshalBody("upstream-model")
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(outbound, &fields); err != nil {
		t.Fatal(err)
	}
	var model string
	if err := json.Unmarshal(fields["model"], &model); err != nil {
		t.Fatal(err)
	}
	if model != "upstream-model" {
		t.Fatalf("model = %q", model)
	}
	if string(fields["future"]) != `{"n":1}` {
		t.Fatalf("unknown field lost: %s", fields["future"])
	}
	if string(fields["messages"]) != `[{"role":"user","content":"x"}]` {
		t.Fatalf("messages changed: %s", fields["messages"])
	}
}

func TestParseChatRequestRejectsInvalidBodies(t *testing.T) {
	bodies := [][]byte{nil, []byte("null"), []byte("[]"), []byte(`{"model":"m","messages":{}}`), []byte(`{"model":"m","messages":null}`), []byte(`{"model":"m","messages":[]}`), []byte(`{"model":"m","messages":[]} trailing`), []byte(`{"model":"m","messages":[]} {}`), []byte(`{"messages":[]}`), []byte(`{"model":"m"}`)}
	for _, body := range bodies {
		if _, err := ParseChatRequest(body); err == nil {
			t.Errorf("accepted invalid body %q", body)
		}
	}
}

func TestValidateStreamTerminal(t *testing.T) {
	if err := ValidateStreamTerminal("data: {\"choices\":[]}\n\ndata: [DONE]\n\n"); err != nil {
		t.Fatal(err)
	}
	for _, body := range []string{"data: [DONE]\n\ndata: {\"x\":1}\n\n", "data: [DONE]\n\ndata: [DONE]\n\n", "data: [DONE]\n"} {
		if err := ValidateStreamTerminal(body); err == nil {
			t.Errorf("accepted malformed terminal stream %q", body)
		}
	}
}

func TestFilterChatBody(t *testing.T) {
	build := func() map[string]any {
		body, _ := concerns.WireObject([]byte(`{"messages":[
		  {"role":"developer","content":"d"},
		  {"role":"user","content":[{"type":"thinking","thinking":"x"},{"type":"text","text":"a","cache_control":{"type":"ephemeral"},"signature":"s"},{"type":"tool_use","id":"t"}]},
		  {"role":"user","content":"  "},
		  {"role":"user","content":[{"type":"text","text":" "}]},
		  {"role":"assistant","content":"","tool_calls":[{"id":"c"}]},
		  {"role":"tool","tool_call_id":"c","content":""}],
		  "tools":[{"name":"cl","description":"D","input_schema":{"type":"object"}},{"functionDeclarations":[{"name":"g","parameters":{"type":"object"}}]},{"type":"function","function":{"name":"ok"}}],
		  "tool_choice":{"type":"any"}}`))
		return body
	}
	for _, keep := range []bool{false, true} {
		body := build()
		FilterChatBody(body, keep)
		encoded, _ := json.Marshal(body)
		text := string(encoded)
		if strings.Contains(text, "thinking") || strings.Contains(text, "tool_use") || strings.Contains(text, "signature") {
			t.Errorf("keep=%v: non-OpenAI blocks survived: %s", keep, text)
		}
		if has := strings.Contains(text, "cache_control"); has != keep {
			t.Errorf("keep=%v but cache_control present=%v", keep, has)
		}
		messages := concerns.Items(body["messages"])
		if len(messages) != 4 || concerns.Text(concerns.Object(messages[0])["role"]) != "system" {
			t.Errorf("keep=%v: developer must become system and empty turns go, tool turns stay: %s", keep, text)
		}
		tools := concerns.Items(body["tools"])
		if len(tools) != 3 || concerns.Text(concerns.Object(concerns.Object(tools[0])["function"])["name"]) != "cl" || concerns.Text(concerns.Object(concerns.Object(tools[1])["function"])["name"]) != "g" {
			t.Errorf("keep=%v: tools not normalised: %s", keep, text)
		}
		if body["tool_choice"] != "required" {
			t.Errorf("keep=%v: tool_choice %v", keep, body["tool_choice"])
		}
	}
	empty, _ := concerns.WireObject([]byte(`{"messages":[{"role":"user","content":"a"}],"tools":[]}`))
	FilterChatBody(empty, false)
	if _, present := empty["tools"]; present {
		t.Error("an empty tools array must be removed")
	}
}
