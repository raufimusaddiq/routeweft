package ingress

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/protocol/hub"
	kiroadapter "github.com/raufimusaddiq/routeweft/internal/protocol/kiro"
	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
)

// Kiro's Chat legs exist and are reachable from the hub, but nothing may route a
// request to Kiro until the wire (#135) and the Messages legs (PR 4c) exist: a
// Messages client would pivot through Chat where the pin has a direct pair.
func TestKiroLegsExistButKiroStaysUnwired(t *testing.T) {
	if !hub.Has(openai.ChatProtocol, kiroadapter.Protocol, hub.Request) || !hub.Has(kiroadapter.Protocol, openai.ChatProtocol, hub.Response) {
		t.Fatal("the Chat legs are not declared")
	}
	if hub.Has(anthropic.MessagesProtocol, kiroadapter.Protocol, hub.Request) || hub.Has(kiroadapter.Protocol, anthropic.MessagesProtocol, hub.Response) {
		t.Fatal("the Messages legs are PR 4c")
	}
	for _, client := range []string{openai.ChatProtocol, openai.ResponsesProtocol, anthropic.MessagesProtocol} {
		if CanTranslate(client, kiroadapter.Protocol) {
			t.Errorf("%s client can reach the unwired Kiro wire", client)
		}
	}
}

func TestChatTranslationForKiro(t *testing.T) {
	request, err := openai.ParseChatRequest([]byte(`{"model":"claude-sonnet-4.5","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"function","function":{"name":"a.b"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	translation, err := TranslateChat(request, kiroadapter.Protocol)
	if err != nil {
		t.Fatal(err)
	}
	if translation.Endpoint != "generateAssistantResponse" || !strings.Contains(string(translation.Body), `"name":"a_b"`) {
		t.Fatalf("%s %s", translation.Endpoint, translation.Body)
	}
	// The names the request leg rewrote are put back on the way out.
	frames := kiroToolTurn()
	var out bytes.Buffer
	if err := translation.Stream(context.Background(), bytes.NewReader(frames), &out); err != nil || !strings.Contains(out.String(), `"name":"a.b"`) {
		t.Fatalf("%v %s", err, out.String())
	}
	body, err := translation.Response(frames)
	if err != nil || !strings.Contains(string(body), `"name":"a.b"`) {
		t.Fatalf("%v %s", err, body)
	}
	// A request the wire cannot carry is refused before anything is sent.
	bad, _ := openai.ParseChatRequest([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}],"stop":["x"]}`))
	if _, err := TranslateChat(bad, kiroadapter.Protocol); err == nil {
		t.Fatal("expected a refusal")
	}
}
