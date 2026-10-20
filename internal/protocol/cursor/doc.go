// Package cursor holds the conversion legs between OpenAI Chat and the Cursor
// provider wire (translator/request/openai-to-cursor.js,
// translator/response/cursor-to-openai.js).
//
// The legs convert Chat bodies. They do not speak to Cursor: the pin's executor
// (executors/cursor.js) encodes the request as Connect-RPC protobuf over HTTP/2
// and decodes the reply into Chat chunks, and that transport is not part of
// these legs. Until it exists no provider is routed here (internal/ingress).
package cursor

// Protocol is the wire name, the registry's TransportCursor.
const Protocol = "cursor"
