package openai

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/compat/fixtures"
)

func TestParseResponsesFixturesPreservesNativeBytes(t *testing.T) {
	exchanges, err := fixtures.Load()
	if err != nil {
		t.Fatal(err)
	}
	seen := 0
	for _, exchange := range exchanges {
		if exchange.Protocol != fixtures.OpenAIResponses || exchange.Kind != fixtures.KindNonStreaming {
			continue
		}
		request, err := ParseResponsesRequest([]byte(exchange.Request.Body), false)
		if err != nil {
			t.Fatalf("%s: %v", exchange.ID, err)
		}
		outbound, err := request.MarshalBody("")
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(outbound, []byte(exchange.Request.Body)) {
			t.Fatalf("%s native body changed", exchange.ID)
		}
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(outbound, &fields); err != nil {
			t.Fatal(err)
		}
		for _, retained := range []string{"tools", "parallel_tool_calls", "reasoning", "input", "store"} {
			if _, ok := fields[retained]; !ok {
				t.Fatalf("%s dropped retained field %q", exchange.ID, retained)
			}
		}
		seen++
	}
	if seen == 0 {
		t.Fatal("no native Responses fixture found")
	}
}

func TestCompactStripsInternalFlagAndKeepsFields(t *testing.T) {
	body := []byte(`{"model":"codex-mini","_compact":true,"input":[{"type":"message","role":"user","content":[{"type":"input_text","text":"x"}]}],"tools":[],"future":{"n":1}}`)
	request, err := ParseResponsesRequest(body, true)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(request.Raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["_compact"]; ok {
		t.Fatal("compact marker leaked into upstream body")
	}
	if string(fields["future"]) != `{"n":1}` {
		t.Fatalf("unknown field lost: %s", fields["future"])
	}
	if request.Stream {
		t.Fatal("compact request unexpectedly streamed")
	}
}

func TestParseResponsesRejectsInvalidBodies(t *testing.T) {
	bodies := [][]byte{nil, []byte("null"), []byte("[]"), []byte(`{"input":[]}`), []byte(`{"model":"m"}`), []byte(`{"model":"m","input":null}`), []byte(`{"model":"m","input":[],"stream":"yes"}`), []byte(`{"model":"m","input":[]} {}`)}
	for _, body := range bodies {
		if _, err := ParseResponsesRequest(body, false); err == nil {
			t.Errorf("accepted invalid body %q", body)
		}
	}
	request, err := ParseResponsesRequest([]byte(`{"model":"m","input":[],"stream":true}`), false)
	if err != nil || !request.Stream {
		t.Fatalf("valid stream request rejected: %v", err)
	}
}

func TestResponsesFixtureTerminalEvent(t *testing.T) {
	exchanges, err := fixtures.Load()
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, exchange := range exchanges {
		if exchange.Protocol != fixtures.OpenAIResponses || exchange.Kind != fixtures.KindStreaming {
			continue
		}
		if count := strings.Count(exchange.Response.Body, "event: response.completed"); count != 1 {
			t.Fatalf("%s has %d terminal events", exchange.ID, count)
		}
		found = true
	}
	if !found {
		t.Fatal("no Responses streaming fixture found")
	}
}
