package anthropic

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/raufimusaddiq/routeweft/compat/fixtures"
)

func TestMessagesFixturePreservesNativeBody(t *testing.T) {
	exchanges, err := fixtures.Load()
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, exchange := range exchanges {
		if exchange.Protocol != fixtures.Anthropic {
			continue
		}
		if exchange.Kind != fixtures.KindNonStreaming && exchange.Kind != fixtures.KindStreaming {
			continue
		}
		request, err := ParseMessagesRequest([]byte(exchange.Request.Body))
		if err != nil {
			t.Fatalf("%s: %v", exchange.ID, err)
		}
		body, err := request.MarshalBody("")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(body, []byte(exchange.Request.Body)) {
			t.Fatalf("%s body changed", exchange.ID)
		}
		if exchange.Kind == fixtures.KindNonStreaming {
			for _, field := range []string{"tools", "thinking", "system", "messages"} {
				if !bytes.Contains(body, []byte("\""+field+"\":")) {
					t.Errorf("fixture omitted %s", field)
				}
			}
			if !bytes.Contains(body, []byte("cache_control")) {
				t.Error("cache_control lost")
			}
		}
		seen++
	}
	if seen < 2 {
		t.Fatalf("Messages fixtures parsed: %d", seen)
	}
}

func TestMessagesModelRemapPreservesOtherFields(t *testing.T) {
	request, err := ParseMessagesRequest([]byte(`{"model":"alias","max_tokens":10,"messages":[{"role":"user","content":[{"type":"text","text":"x","cache_control":{"type":"ephemeral"}}]}],"thinking":{"type":"enabled","budget_tokens":3},"future":{"x":1}}`))
	if err != nil {
		t.Fatal(err)
	}
	body, err := request.MarshalBody("claude-target")
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	if string(fields["future"]) != `{"x":1}` || !bytes.Contains(fields["messages"], []byte("cache_control")) || !bytes.Contains(fields["thinking"], []byte("budget_tokens")) {
		t.Fatalf("fields lost: %s", body)
	}
}

func TestMessagesRejectsMalformedRequest(t *testing.T) {
	for _, body := range [][]byte{nil, []byte("[]"), []byte(`{"messages":[]}`), []byte(`{"model":"m"}`), []byte(`{"model":"m","messages":[]}`), []byte(`{"model":"m","messages":[{}],"stream":"true"}`), []byte(`{"model":"m","messages":[{}]} {}`)} {
		if _, err := ParseMessagesRequest(body); err == nil {
			t.Errorf("accepted %q", body)
		}
	}
}
