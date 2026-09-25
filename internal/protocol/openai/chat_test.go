package openai

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/raufimusaddiq/routeweft/compat/fixtures"
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
