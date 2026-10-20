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
// so are Chat and Messages (internal/protocol/anthropic).
var legs = []Leg{
	{anthropic.MessagesProtocol, openai.ChatProtocol, Request},
	{openai.ChatProtocol, anthropic.MessagesProtocol, Request},
	{anthropic.MessagesProtocol, openai.ChatProtocol, Response},
	{openai.ChatProtocol, anthropic.MessagesProtocol, Response},
	{openai.ChatProtocol, openai.ResponsesProtocol, Request},
	{openai.ResponsesProtocol, openai.ChatProtocol, Request},
	{openai.ResponsesProtocol, openai.ChatProtocol, Response},
	{openai.ChatProtocol, openai.ResponsesProtocol, Response},
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
