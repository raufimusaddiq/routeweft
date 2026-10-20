// Package gemini owns Gemini GenerateContent wire adapters: the native request
// view the ingress route parses, and the conversion legs between a Chat body and
// Gemini's (PRD 5, SPEC 9, #119).
//
//   - ChatRequestToGemini is request openai-chat>gemini and, with Vertex set,
//     openai-chat>vertex.
//   - GeminiRequestToChat is request gemini>openai-chat.
//   - GeminiResponseToChat and GeminiStreamToChat are response gemini>openai-chat,
//     and serve vertex too.
//
// Every field of every leg is carried, not carried for a stated reason, or
// refused; docs/PARITY.md 6.6 is the table. Thought signatures are the one piece
// of state a leg keeps between requests, in Signatures, scoped to the client and
// connection they came from.
package gemini
