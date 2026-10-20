// Package wire is the provider hook seam for the inference path: the one place
// that says which provider-specific header, body and response behaviour applies
// to a serving request. The ingress handlers ask this package instead of
// branching on a provider id, so a provider's quirk is added by registering a
// Hook, not by editing the handlers.
//
// The seam is a pure relocation of behaviour that previously lived as
// ProviderID comparisons in internal/ingress. It adds no provider behaviour.
package wire

import (
	"net/http"

	anthropicprovider "github.com/raufimusaddiq/routeweft/internal/providers/anthropic"
	claudeprovider "github.com/raufimusaddiq/routeweft/internal/providers/claude"
	clineprovider "github.com/raufimusaddiq/routeweft/internal/providers/cline"
	codexprovider "github.com/raufimusaddiq/routeweft/internal/providers/codex"
	"github.com/raufimusaddiq/routeweft/internal/providers/oauthheaders"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// Hook declares the provider-specific parts of a serving request. Every field
// is optional; a provider with no Hook is served with only its bearer credential.
type Hook struct {
	// BearerMessages sends the credential on a native Messages request as a
	// bearer token instead of x-api-key.
	BearerMessages bool
	// OpenAIHeaders are identity headers for Chat and Responses requests, applied
	// after the credential and before the shared OAuth identity table.
	OpenAIHeaders func(routing.ProviderRef) map[string]string
	// OpenAIOverride headers are applied last on Chat and Responses requests and
	// may replace the credential header.
	OpenAIOverride func(routing.ProviderRef) map[string]string
	// MessagesHeaders are identity headers for Messages requests, applied after
	// the credential and before the shared OAuth identity table.
	MessagesHeaders func(routing.ProviderRef) map[string]string
	// NoBearer leaves the Authorization credential header off Chat and Responses
	// requests, for a provider whose credential travels in another header.
	NoBearer bool
	// Target resolves where the request goes when that depends on the
	// connection, the model or the wire (a deployment path, a region, a query).
	Target func(routing.ProviderRef) (Target, error)
	// PrepareMessages rewrites a Messages body produced by a conversion leg.
	PrepareMessages func(body []byte) ([]byte, error)
	// DefaultMessagesBeta is sent when the client supplied no anthropic-beta.
	DefaultMessagesBeta string
	// UnwrapResponse removes a gateway envelope from a non-stream 2xx body.
	UnwrapResponse func(body []byte) []byte
	// Finalize is the provider's last rewrite of the outbound body (registry quirks).
	Finalize Finalize
}

var hooks = map[string]Hook{
	"codex": {OpenAIHeaders: func(routing.ProviderRef) map[string]string { return codexprovider.Headers() }},
	"claude": {
		BearerMessages:  true,
		MessagesHeaders: func(routing.ProviderRef) map[string]string { return claudeprovider.Headers() },
		PrepareMessages: claudeprovider.WithIdentity,
		Target:          betaQuery,
	},
	"anthropic":        {DefaultMessagesBeta: anthropicprovider.Beta},
	"cerebras":         {Finalize: dropClientMetadata},
	"mistral":          {Finalize: dropClientMetadata},
	"minimax":          minimaxHook,
	"minimax-cn":       minimaxHook,
	"deepseek":         {Finalize: supportedToolTypes("web_search_20250305", "web_search_20260209")},
	"kenari":           {Finalize: foldResponsesSystem},
	"azure":            {NoBearer: true, OpenAIHeaders: azureHeaders, Target: azureTarget},
	"xiaomi-tokenplan": {Target: tokenplanTarget},
	"glm":              {Target: betaQuery},
	"kimi":             {OpenAIHeaders: kimiDeviceHeaders, MessagesHeaders: kimiDeviceHeaders, Target: betaQuery},
	"kilocode":         {OpenAIHeaders: kilocodeOrgHeaders},
	"github":           githubHook,
	"kimchi":           {Finalize: kimchiBody},
	"cline":            clineHook,
	"clinepass":        clineHook,
}

var minimaxHook = Hook{Finalize: chain(dropOutputConfig, requireClaudeToolType), Target: betaQuery}

var githubHook = Hook{BearerMessages: true, Finalize: githubChatBody}

var clineHook = Hook{
	OpenAIOverride: func(provider routing.ProviderRef) map[string]string { return clineprovider.Headers(provider.APIToken) },
	UnwrapResponse: func(body []byte) []byte {
		unwrapped, _ := clineprovider.UnwrapEnvelope(body)
		return unwrapped
	},
}

// For returns the hook registered for a provider id; the zero Hook if none.
func For(providerID string) Hook { return hooks[providerID] }

// Registered lists the provider ids that have a hook, for tests.
func Registered() []string {
	ids := make([]string, 0, len(hooks))
	for id := range hooks {
		ids = append(ids, id)
	}
	return ids
}

// OpenAIHeaders returns the credential and identity headers for a Chat or
// Responses request to provider.
func OpenAIHeaders(provider routing.ProviderRef) http.Header {
	headers := http.Header{}
	hook := For(provider.ProviderID)
	if provider.APIToken != "" && !hook.NoBearer {
		headers.Set("Authorization", "Bearer "+provider.APIToken)
	}
	if hook.OpenAIHeaders != nil {
		set(headers, hook.OpenAIHeaders(provider))
	}
	set(headers, oauthheaders.Headers(provider.ProviderID))
	if hook.OpenAIOverride != nil {
		set(headers, hook.OpenAIOverride(provider))
	}
	return headers
}

// MessagesHeaders returns the credential, identity and version headers for a
// native Messages request. clientVersion and clientBeta are the client's own
// Anthropic-Version and Anthropic-Beta values, forwarded when present.
func MessagesHeaders(provider routing.ProviderRef, clientVersion, clientBeta string) http.Header {
	headers := http.Header{}
	hook := For(provider.ProviderID)
	if provider.APIToken != "" {
		if hook.BearerMessages {
			headers.Set("Authorization", "Bearer "+provider.APIToken)
		} else {
			headers.Set("X-Api-Key", provider.APIToken)
		}
	}
	if hook.MessagesHeaders != nil {
		set(headers, hook.MessagesHeaders(provider))
	}
	set(headers, oauthheaders.Headers(provider.ProviderID))
	if clientVersion != "" {
		headers.Set("Anthropic-Version", clientVersion)
	} else {
		headers.Set("Anthropic-Version", anthropicprovider.Version)
	}
	if clientBeta != "" {
		headers.Set("Anthropic-Beta", clientBeta)
	} else if hook.DefaultMessagesBeta != "" {
		headers.Set("Anthropic-Beta", hook.DefaultMessagesBeta)
	}
	return headers
}

// PrepareMessages applies a provider's rewrite to a converted Messages body.
func PrepareMessages(providerID string, body []byte) ([]byte, error) {
	if prepare := For(providerID).PrepareMessages; prepare != nil {
		return prepare(body)
	}
	return body, nil
}

// UnwrapResponse reports the provider's envelope remover, or nil.
func UnwrapResponse(providerID string) func([]byte) []byte { return For(providerID).UnwrapResponse }

func set(headers http.Header, values map[string]string) {
	for name, value := range values {
		headers.Set(name, value)
	}
}
