// Package commandcode holds the conversion legs between OpenAI Chat and the
// CommandCode provider wire (translator/request/openai-to-commandcode.js,
// translator/response/commandcode-to-openai.js).
//
// The legs convert bodies and NDJSON streams. They do not speak to CommandCode:
// the headers, the forced stream, and the pre-stream error inspection and retry
// of the pin's executor (executors/commandcode.js) belong to the provider wire,
// which is not part of these legs. Until it is bound no provider is routed here
// (internal/ingress).
package commandcode

// Protocol is the wire name, the registry's TransportCommandCode.
const Protocol = "commandcode"
