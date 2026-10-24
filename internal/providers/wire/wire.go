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
	"time"

	anthropicprovider "github.com/raufimusaddiq/routeweft/internal/providers/anthropic"
	claudeprovider "github.com/raufimusaddiq/routeweft/internal/providers/claude"
	clineprovider "github.com/raufimusaddiq/routeweft/internal/providers/cline"
	"github.com/raufimusaddiq/routeweft/internal/providers/oauthheaders"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// Hook declares the provider-specific parts of a serving request. Every field
// is optional; a provider with no Hook is served with only its bearer credential.
type Hook struct {
	// RequestHeaders signs the final body and URL, after all other headers are applied.
	RequestHeaders func(routing.ProviderRef, []byte, string) (map[string]string, error)
	// ClientRequestHeaders derives headers from the client's own request and the
	// body before provider normalization, for an identity the provider reads from
	// the client's session (opencode-go). It runs after RequestHeaders and wins.
	ClientRequestHeaders func(routing.ProviderRef, http.Header, []byte) map[string]string
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
	// NoOpenAIBearer leaves the bearer Authorization header off Chat and
	// Responses requests, for a provider whose credential there travels in another
	// header that OpenAIHeaders sets (Azure's api-key). It does not touch the
	// Messages credential, which BearerMessages and MessagesHeaders own.
	NoOpenAIBearer bool
	// Base is the host a model listing is requested from, when it is not the
	// route's base (a Token Plan region).
	Base func(routing.ProviderRef) string
	// Unlisted marks a provider with no model listing: the reference never lists
	// its models, and a URL we cannot verify is not invented for it.
	Unlisted bool
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
	// FinalizeAccount injects settings from the selected connection into the final body.
	FinalizeAccount func(routing.ProviderRef, Body) bool
	// ForceStream marks a gateway that only answers streams; ForcesStream applies model restrictions.
	// Its finalizer sets
	// stream:true on the request; a client that did not ask to stream gets the
	// SSE folded back to one JSON body before any consumer reads it
	// (internal/ingress dispatchAttempts, FoldForcedStream).
	ForceStream bool
	// ForceStreamFor restricts forced streaming to selected routes.
	ForceStreamFor func(routing.ProviderRef) bool
	// ForcedTransport restricts a model to one wire; empty keeps normal selection.
	ForcedTransport func(model string) string
	// StripEmptyToolCalls removes an empty tool_calls array from the deltas of a
	// Chat stream relayed to the client (ResponseStream).
	StripEmptyToolCalls bool
	// ResponsesFunctionToolsOnly marks a Responses endpoint that takes function
	// tools only (kenari): Finalize declares custom tools as functions and the
	// response is rewritten back (ResponsesCustomToolNames, ResponsesStream,
	// RewriteResponsesBody).
	ResponsesFunctionToolsOnly bool
	// ClassifyError reads a provider's own error body on a non-2xx response and
	// reports how Routeweft should account for it. It never changes the status or
	// body the client sees.
	ClassifyError func(status int, body []byte, now time.Time) (routing.Classification, bool)
	// RequireCredential prevents unauthenticated dispatch when an empty credential
	// would otherwise produce a misleading upstream error.
	RequireCredential bool
	// Compact rewrites the body of a request to the provider's compact endpoint,
	// in place of Finalize and FinalizeAccount: the compact endpoint takes a
	// smaller document than the streaming one and must not be forced to stream.
	Compact Finalize
	// ClientHeaders names client request headers that are forwarded to the
	// provider unchanged when the client sent them (a beta opt-in the provider
	// reads, not an identity or a credential).
	ClientHeaders []string
	// Do sends the request in place of client.Do, for a provider whose exchange is
	// not one request and one reply (Cursor's Connect stream, which the client
	// answers mid-turn). It returns an ordinary response for the caller to classify.
	Do func(*http.Client, *http.Request) (*http.Response, error)
}

var hooks = map[string]Hook{
	"codex":      codexHook,
	"grok-cli":   grokCLIHook,
	"gemini-cli": {ClassifyError: geminiCLIRetryInfo},
	// The gateway requires SSE even for buffered clients (registry/api-airforce.js:25-32).
	"api-airforce": {ForceStream: true, Finalize: codebuddyStream, OpenAIHeaders: func(routing.ProviderRef) map[string]string {
		return map[string]string{"HTTP-Referer": "https://endpoint-proxy.local", "X-Title": "Endpoint Proxy"}
	}},
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
	"kenari":           {Finalize: chain(responsesFunctionToolsOnly, foldResponsesSystem), ResponsesFunctionToolsOnly: true},
	"azure":            {NoOpenAIBearer: true, OpenAIHeaders: azureHeaders, Target: azureTarget, Unlisted: true},
	"xiaomi-tokenplan": {Target: tokenplanTarget, Base: tokenplanBase},
	"glm":              {Target: betaQuery},
	"kimi":             {OpenAIHeaders: kimiDeviceHeaders, MessagesHeaders: kimiDeviceHeaders, Target: betaQuery},
	"kilocode":         {OpenAIHeaders: kilocodeOrgHeaders},
	"qoder":            {RequestHeaders: qoderHeaders, RequireCredential: true},
	"iflow":            {OpenAIHeaders: iflowHeaders, RequireCredential: true},
	"kiro":             {NoOpenAIBearer: true, OpenAIHeaders: kiroHeaders, Target: kiroTarget, FinalizeAccount: kiroBody, RequireCredential: true},
	"github":           githubHook,
	"kimchi":           {Finalize: kimchiBody},
	"cline":            clineHook,
	"clinepass":        clineHook,
	"codebuddy-cn":     codebuddyCN,
	"codebuddy-intl":   codebuddyIntl,
	"zed":              {ForceStream: true, Finalize: codebuddyStream},
	"opencode-go":      {ForceStream: true, ForceStreamFor: openCodeGoForcesStream, ForcedTransport: openCodeGoTransport, FinalizeAccount: openCodeGoFinalize, RequestHeaders: openCodeGoSessionFallback, ClientRequestHeaders: openCodeGoSession},
	"opencode":         {Target: opencodeTarget, OpenAIHeaders: opencodeHeaders, ForceStream: true, Finalize: codebuddyStream},
	"vertex-partner":   {Target: vertexPartnerTarget},
	"cloudflare-ai":    {Target: cloudflareTarget},
	"commandcode":      commandCodeHook,
	"cursor":           {RequestHeaders: cursorHeaders, Target: cursorTarget, ForceStream: true, RequireCredential: true, Do: CursorDo},
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
	if provider.APIToken != "" && !hook.NoOpenAIBearer {
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

// Send performs the request through the provider's own exchange when it has one.
func Send(provider routing.ProviderRef, client *http.Client, request *http.Request) (*http.Response, error) {
	if do := For(provider.ProviderID).Do; do != nil {
		return do(client, request)
	}
	return client.Do(request)
}

// ForcesStream reports whether the selected provider/model route only answers streams.
func ForcesStream(provider routing.ProviderRef) bool {
	hook := For(provider.ProviderID)
	return hook.ForceStream && (hook.ForceStreamFor == nil || hook.ForceStreamFor(provider))
}

// ForcedTransport returns the model's required wire, or empty for normal selection.
func ForcedTransport(providerID, model string) string {
	if forced := For(providerID).ForcedTransport; forced != nil {
		return forced(model)
	}
	return ""
}

// RequiresCredential reports whether a request to provider must carry a
// credential, so a caller can refuse it instead of sending an unsigned one.
func RequiresCredential(providerID string) bool { return For(providerID).RequireCredential }

// ClassifyError reports the provider's own account of a failed response, when it
// has one for this body.
func ClassifyError(providerID string, status int, body []byte, now time.Time) (routing.Classification, bool) {
	if classify := For(providerID).ClassifyError; classify != nil {
		return classify(status, body, now)
	}
	return routing.Classification{}, false
}
