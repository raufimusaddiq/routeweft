// Package hub declares OpenAI Chat Completions as the conversion hub (BDR-025)
// and which conversion legs Routeweft implements.
//
// A leg converts one wire format into another. Request legs convert a client
// body into the provider's body; response legs convert provider output into the
// client's format. A conversion uses a registered direct leg when there is one
// and otherwise pivots through Chat, exactly like LiteRouter's
// translator/index.js. The set of legs here is checked against the pinned
// oracle's translator directory by internal/parity.
package hub

import (
	"github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/protocol/commandcode"
	"github.com/raufimusaddiq/routeweft/internal/protocol/cursor"
	"github.com/raufimusaddiq/routeweft/internal/protocol/gemini"
	"github.com/raufimusaddiq/routeweft/internal/protocol/kiro"
	"github.com/raufimusaddiq/routeweft/internal/protocol/ollama"
	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
)

// Hub is the pivot format.
const Hub = openai.ChatProtocol

// Kind separates the two directions a format can be converted in.
type Kind uint8

const (
	// Request converts a client request into the provider's request.
	Request Kind = iota + 1
	// Response converts provider output (body or stream) into the client format.
	Response
)

func (k Kind) String() string {
	switch k {
	case Request:
		return "request"
	case Response:
		return "response"
	}
	return "unknown"
}

// Leg is one implemented conversion: a body in From becomes a body in To.
type Leg struct {
	From, To string
	Kind     Kind
}

// legs lists every implemented conversion. Chat and Responses are converted
// directly in both directions (translate*.go in internal/protocol/openai), and
// so are Chat and Messages (internal/protocol/anthropic). Ollama is a provider
// wire only: the pin has a Chat>Ollama request leg and an Ollama>Chat response
// leg and nothing the other way, so a client never speaks it through the hub.
// Cursor and CommandCode are provider wires of the same kind. Gemini and Vertex
// are too: a Chat client reaches them through the request leg and reads the
// response leg, while the pin's gemini>Chat request leg serves a Gemini client
// and has no response leg back, so it is not routable on its own (the pin
// registers no openai>gemini response leg). Gemini CLI and Antigravity are the
// same wires in the Cloud Code envelope: both are provider wires, and
// Antigravity alone has the response leg a client of its own would read. Kiro is
// a provider wire with a Chat request leg and a Chat response leg; its two
// Messages legs are PR 4c, until which a Messages client would pivot through
// Chat where the pin has a direct pair, so ingress keeps Kiro unwired.
var legs = []Leg{
	{anthropic.MessagesProtocol, openai.ChatProtocol, Request},
	{openai.ChatProtocol, anthropic.MessagesProtocol, Request},
	{anthropic.MessagesProtocol, openai.ChatProtocol, Response},
	{openai.ChatProtocol, anthropic.MessagesProtocol, Response},
	{openai.ChatProtocol, openai.ResponsesProtocol, Request},
	{openai.ResponsesProtocol, openai.ChatProtocol, Request},
	{openai.ResponsesProtocol, openai.ChatProtocol, Response},
	{openai.ChatProtocol, openai.ResponsesProtocol, Response},
	{openai.ChatProtocol, ollama.Protocol, Request},
	{ollama.Protocol, openai.ChatProtocol, Response},
	{openai.ChatProtocol, cursor.Protocol, Request},
	{cursor.Protocol, openai.ChatProtocol, Response},
	{openai.ChatProtocol, commandcode.Protocol, Request},
	{commandcode.Protocol, openai.ChatProtocol, Response},
	{openai.ChatProtocol, gemini.Protocol, Request},
	{openai.ChatProtocol, gemini.VertexProtocol, Request},
	{gemini.Protocol, openai.ChatProtocol, Request},
	{gemini.Protocol, openai.ChatProtocol, Response},
	{gemini.VertexProtocol, openai.ChatProtocol, Response},
	{openai.ChatProtocol, gemini.GeminiCLIProtocol, Request},
	{gemini.GeminiCLIProtocol, openai.ChatProtocol, Request},
	{gemini.GeminiCLIProtocol, openai.ChatProtocol, Response},
	{openai.ChatProtocol, gemini.AntigravityProtocol, Request},
	{gemini.AntigravityProtocol, openai.ChatProtocol, Request},
	{gemini.AntigravityProtocol, openai.ChatProtocol, Response},
	{openai.ChatProtocol, gemini.AntigravityProtocol, Response},
	{openai.ChatProtocol, kiro.Protocol, Request},
	{kiro.Protocol, openai.ChatProtocol, Response},
}

// Legs returns a copy of the implemented legs.
func Legs() []Leg { return append([]Leg(nil), legs...) }

// Has reports whether a leg is implemented exactly as declared.
func Has(from, to string, kind Kind) bool {
	for _, leg := range legs {
		if leg.From == from && leg.To == to && leg.Kind == kind {
			return true
		}
	}
	return false
}

// Route returns the legs that convert from one format to another in one
// direction: nil when the formats are equal, the direct leg when one exists,
// otherwise from→Hub→to. ok is false when a needed leg is not implemented.
func Route(from, to string, kind Kind) (route []Leg, ok bool) {
	if from == to {
		return nil, true
	}
	if Has(from, to, kind) {
		return []Leg{{from, to, kind}}, true
	}
	if from == Hub || to == Hub {
		return nil, false
	}
	if Has(from, Hub, kind) && Has(Hub, to, kind) {
		return []Leg{{from, Hub, kind}, {Hub, to, kind}}, true
	}
	return nil, false
}

// CanTranslate reports whether a client in source format can be served by a
// provider speaking target: the request must convert source→target and the
// response must convert target→source.
func CanTranslate(source, target string) bool {
	if _, ok := Route(source, target, Request); !ok {
		return false
	}
	_, ok := Route(target, source, Response)
	return ok
}
