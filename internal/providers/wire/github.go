package wire

import (
	"regexp"
	"strings"

	"github.com/google/uuid"

	anthropicadapter "github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// ---- GitHub Copilot transport selection (open-sse/executors/github.js) ----

// githubClaudeID is the pin's Claude test: a name pattern, not a registry field,
// because Copilot's live catalog lists claude-* variants ahead of the static
// registry (github.js:22-30).
var githubClaudeID = regexp.MustCompile(`(?i)claude`)

// githubTransport sends every Claude model to Copilot's Anthropic-native
// /v1/messages, the one endpoint that reports prompt-cache tokens
// (github.js:126-133). Other models keep normal selection.
func githubTransport(model string) string {
	if githubClaudeID.MatchString(model) {
		return anthropicadapter.MessagesProtocol
	}
	return ""
}

// githubForcesStream: executeWithMessagesEndpoint translates with stream=true
// whatever the client asked for (github.js:259-263), so a non-stream client gets
// the SSE folded back to one body (internal/ingress foldForcedStream). Only the
// Messages wire is forced; a Claude id that reached another wire is left alone.
func githubForcesStream(provider routing.ProviderRef) bool {
	return provider.Protocol == anthropicadapter.MessagesProtocol && githubTransport(provider.UpstreamModel) == provider.Protocol
}

func githubStream(provider routing.ProviderRef, body Body) bool {
	return githubForcesStream(provider) && body.set("stream", true)
}

// githubRequestID: every Copilot request carries a fresh x-request-id
// (github.js:47). It is minted per prepared request, so an escalated replay on
// another wire has its own, as the pin's second buildHeaders call does.
func githubRequestID(routing.ProviderRef) map[string]string {
	return map[string]string{"x-request-id": uuid.NewString()}
}

// The two refusals Copilot gives a model its /chat/completions endpoint does not
// serve (github.js:157). Matched on the body as the pin does, case-sensitively.
var githubChatRefusals = []string{
	"not accessible via the /chat/completions endpoint",
	"The requested model is not supported",
}

// githubEscalate is the pin's one 400-triggered replay of a Chat request on
// /responses (github.js:154-161). It fires only for a Chat request whose 400
// names one of the two refusals, and never for a Gemini or Claude id: /responses
// serves OpenAI models only, so escalating them would swap the real Chat error
// for a misleading "does not support Responses API" (github.js:113-121, #1062).
//
// The hook decides nothing about bounds: internal/ingress replays at most once
// per attempt, and a provider already on Responses never matches here, so a 400
// that survives the replay is the answer.
//
// Divergence: the pin remembers the model in knownCodexModels and sends its later
// requests straight to /responses (github.js:19,137-140). Routeweft keeps no such
// cache; every request tries Chat first, so a model that needs /responses costs
// one extra round trip per request instead of one per process.
func githubEscalate(provider routing.ProviderRef, status int, body []byte) (protocol, endpoint string) {
	if status != 400 || provider.Protocol != openaiadapter.ChatProtocol {
		return "", ""
	}
	model := strings.ToLower(provider.UpstreamModel)
	if strings.Contains(model, "gemini") || strings.Contains(model, "claude") {
		return "", ""
	}
	text := string(body)
	for _, refusal := range githubChatRefusals {
		if strings.Contains(text, refusal) {
			return openaiadapter.ResponsesProtocol, githubResponsesEndpoint
		}
	}
	return "", ""
}

// githubResponsesEndpoint is registry github's TransportEndpoints[responses]
// (config.responsesUrl in the pin); a test pins them together.
const githubResponsesEndpoint = "responses"
