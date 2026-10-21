package wire

import (
	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// openCodeGoTransport lists the pin's Responses-only models (registry/opencode-go.js:41,72-74).
func openCodeGoTransport(model string) string {
	switch model {
	case "gpt-5.6-luna", "grok-4.6", "muse-spark-1.2-contributor", "muse-spark-1.3-contributor":
		return openaiadapter.ResponsesProtocol
	}
	return ""
}

func openCodeGoStream(provider routing.ProviderRef, body Body) bool {
	if !openCodeGoForcesStream(provider) {
		return false
	}
	return body.set("stream", true)
}

func openCodeGoForcesStream(provider routing.ProviderRef) bool {
	return provider.Protocol == openaiadapter.ResponsesProtocol && openCodeGoTransport(provider.UpstreamModel) == provider.Protocol
}
