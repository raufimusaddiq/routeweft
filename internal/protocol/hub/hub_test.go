package hub

import (
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/protocol/commandcode"
	"github.com/raufimusaddiq/routeweft/internal/protocol/cursor"
	"github.com/raufimusaddiq/routeweft/internal/protocol/gemini"
	"github.com/raufimusaddiq/routeweft/internal/protocol/ollama"
	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/protocol/systemone"
)

// The implemented pairs are exactly those reachable through the Chat hub among
// Chat, Responses and Messages, plus Ollama, Cursor and CommandCode as provider
// wires (the pin has a Chat>X request leg and an X>Chat response leg and no
// client side);
// Gemini and System One have no legs yet (#119 tracks them), so they must stay
// unreachable rather than appear to work.
func TestCanTranslateCoversChatResponsesMessages(t *testing.T) {
	converted := map[string]bool{openai.ChatProtocol: true, openai.ResponsesProtocol: true, anthropic.MessagesProtocol: true}
	protocols := []string{openai.ChatProtocol, openai.ResponsesProtocol, anthropic.MessagesProtocol, gemini.Protocol, gemini.VertexProtocol, ollama.Protocol, cursor.Protocol, commandcode.Protocol, systemone.Protocol}
	providerWires := map[string]bool{gemini.Protocol: true, gemini.VertexProtocol: true, ollama.Protocol: true, cursor.Protocol: true, commandcode.Protocol: true}
	for _, source := range protocols {
		for _, target := range protocols {
			want := source != target && converted[source] && (converted[target] || providerWires[target])
			got := CanTranslate(source, target)
			if source == target {
				got = false
			}
			if got != want {
				t.Errorf("CanTranslate(%s,%s)=%v want %v", source, target, got, want)
			}
		}
	}
}

func TestRoutePivotsThroughHubAndPrefersDirectLegs(t *testing.T) {
	saved := legs
	defer func() { legs = saved }()
	legs = []Leg{
		{"a", Hub, Request}, {Hub, "b", Request},
		{"a", "b", Request},
		{"c", Hub, Request}, {Hub, "d", Request},
	}
	if route, ok := Route("a", "b", Request); !ok || len(route) != 1 || route[0] != (Leg{"a", "b", Request}) {
		t.Fatalf("a direct leg must win over the pivot: %v %v", route, ok)
	}
	if route, ok := Route("c", "d", Request); !ok || len(route) != 2 || route[0].To != Hub || route[1].From != Hub {
		t.Fatalf("c->d must pivot through the hub: %v %v", route, ok)
	}
	if _, ok := Route("d", "c", Request); ok {
		t.Fatal("a missing leg must not be assumed")
	}
	if route, ok := Route("x", "x", Request); !ok || route != nil {
		t.Fatalf("equal formats need no conversion: %v %v", route, ok)
	}
	if _, ok := Route("c", Hub, Response); ok {
		t.Fatal("request legs must not satisfy a response conversion")
	}
}
