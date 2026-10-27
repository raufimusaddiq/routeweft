// Package onboarding implements the provider connection flows LiteRouter offers,
// one implementation per flow shape rather than one per provider.
//
// The reference declares a `flowType` on each provider
// (src/lib/oauth/providers/*.js) and a handful of separate routes for token
// import, cookie and PAT connections. Those are the flow shapes here:
//
//	authorization_code_pkce  browser sign-in with PKCE, code or redirect pasted back
//	authorization_code       browser sign-in without PKCE
//	device_code              user enters a code, we poll the token endpoint
//	browser_token            browser returns a token, validated then stored
//	import_token             credential read from a local IDE install
//	manual_token             token/cookie/PAT pasted by the operator
//
// Each flow owns only what differs: the authorize URL, the exchange, and any
// post-exchange enrichment. Everything shared (state, PKCE, single-use states,
// durable save, credential publication) lives here once.
package onboarding

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

// FlowType names a flow shape.
type FlowType string

const (
	FlowAuthorizationCodePKCE FlowType = "authorization_code_pkce"
	FlowAuthorizationCode     FlowType = "authorization_code"
	FlowDeviceCode            FlowType = "device_code"
	FlowBrowserToken          FlowType = "browser_token"
	FlowImportToken           FlowType = "import_token"
	FlowManualToken           FlowType = "manual_token"
)

// Completion says what the operator hands back to finish a browser flow.
type Completion string

const (
	// CompletionCode is a bare authorization code (or Claude's "code#state").
	CompletionCode Completion = "code"
	// CompletionRedirectURL is a full redirect URL whose query holds the code,
	// used when the registered redirect points at a host that will not load.
	CompletionRedirectURL Completion = "redirect-url"
	// CompletionToken is a pasted long-lived token (browser token, PAT, cookie).
	CompletionToken Completion = "token"
	// CompletionCallback is a full callback URL carrying the credential itself
	// (Windsurf's access_token, Trae's refreshToken).
	CompletionCallback Completion = "callback"
)

// HTTPClient is the outbound client used by flows. The app passes a pooled,
// SSRF-guarded client so a sign-in cannot reach a private address.
type HTTPClient interface {
	Do(*http.Request) (*http.Response, error)
}

// Flow is one provider's onboarding implementation.
type Flow struct {
	// Provider is the catalog id this flow connects.
	Provider string
	// Type is the flow shape.
	Type FlowType
	// Completion tells the UI what to ask the operator to paste.
	Completion Completion
	// FixedPort, FixedHost and CallbackPath mirror the provider's registered
	// redirect. The host matters: Codex registers localhost:1455/auth/callback and
	// xAI registers 127.0.0.1:56121/callback, and a mismatch is rejected upstream.
	FixedPort    int
	FixedHost    string
	CallbackPath string
	// MetaKeys lists the operator input this flow reads from Params.Meta, so the
	// dashboard offers exactly those fields. Declared here rather than inferred by
	// the UI, and checked against the flow's actual reads by a test.
	MetaKeys []string
	// ImportMetaKeys narrows MetaKeys for the Import half of a flow that also has a
	// browser step (GitLab's PAT reads only baseUrl, not the OAuth application's
	// client id and secret), so the import method does not show fields it ignores.
	// Nil means the import reads MetaKeys.
	ImportMetaKeys []string
	// AuthKind is the credential mode this flow produces, so it can be checked
	// against the provider spec without performing a sign-in. Empty means the
	// default, AuthOAuth.
	AuthKind credentials.AuthKind
	// Bulk converts one item of a bulk-import payload into a connection. Present
	// only for providers whose reference exposes a bulk-import route (codex,
	// grok-cli); the endpoint refuses the rest rather than accepting a batch it
	// would silently drop.
	Bulk func(item map[string]any) (BulkAccount, error)
	// ResolvesIdentity means the flow reads the account identity from the provider
	// or the credential itself (a JWT claim, /api/v4/user, the account key), so the
	// operator is not asked for one. Without this the dashboard demanded a "stable
	// account identity", validated it, and then stored the flow's value instead.
	// Declare it only where a genuine credential always yields one; a best-effort
	// lookup that can come back empty still needs the operator's. An operator value
	// sent anyway stays the fallback, and a sign-in that resolves none is refused
	// before anything is saved.
	ResolvesIdentity bool
	// IdentityOptional means a credential that names no account is stored without
	// an identity rather than refused, as the reference stores a Codex connection
	// with no email (connectionsRepo.js:228-264 never dedups one). Such a
	// connection is never matched to another.
	IdentityOptional bool
	// InfersIdentity means the flow usually reads the account from the provider
	// (a user-info call, an id-token claim) but cannot promise to: the lookup is
	// best effort. The operator is not asked for an identity up front, as the
	// reference asks for none (OAuthModal.js), and a sign-in that comes back
	// without one is refused before anything is saved (oauth_identity_unresolved),
	// so the next attempt asks. ResolvesIdentity is the stronger promise and
	// implies this.
	InfersIdentity bool
	// RedirectSource says where the flow's redirect URI comes from. It is the
	// single declaration that decides whether the handler must resolve one, so a
	// flow cannot consume a URI the handler never builds, or demand one it discards.
	RedirectSource RedirectSource
	// AuthMethods is the closed set of Meta["authMethod"] values the flow signs in
	// with (Kiro's builder-id/idc device sign-in, its google/github browser
	// sign-in). The start refuses any other value instead of quietly signing in
	// with a method the operator did not choose. DefaultAuthMethod is used when
	// none is sent; with no default the operator must name one. Nil means the flow
	// reads no auth method.
	AuthMethods       []string
	DefaultAuthMethod string
	// Browser is a browser sign-in offered beside a device flow for the same
	// provider (Kiro's Google/GitHub login beside its AWS SSO device code). The
	// device endpoints drive this flow and the browser endpoints drive Browser, so
	// each endpoint family still has exactly one flow per provider.
	Browser *Flow
	// RenewsSameAccount means a browser sign-in of an account already connected
	// renews that row instead of adding a second, as every device sign-in does
	// (the reference's oauth duplicate rule, connectionsRepo.js:228-283).
	RenewsSameAccount bool

	// Authorize builds the browser URL. verifier is empty for flows without PKCE.
	Authorize func(ctx context.Context, p Params) (AuthRequest, error)
	// Exchange completes a browser/device flow.
	Exchange func(ctx context.Context, p Params) (Result, error)
	// Import handles manual_token/import_token flows that need no browser step.
	Import func(ctx context.Context, p Params) (Result, error)
	// Poll handles device_code flows.
	Poll func(ctx context.Context, p Params) (PollResult, error)
}

// Params is everything a flow step may need. Secrets never appear in logs and
// the verifier stays server-side.
type Params struct {
	RedirectURI string
	State       string
	Verifier    string
	Input       string
	// Meta carries operator-supplied provider parameters (GitLab baseUrl and
	// clientId, Kiro start URL and region).
	Meta map[string]string
	// Client is the outbound HTTP client.
	Client HTTPClient
	// Now is injectable for deterministic tests.
	Now func() time.Time
}

func (p Params) now() time.Time {
	if p.Now != nil {
		return p.Now()
	}
	return time.Now()
}

// BulkAccount is one parsed entry of a bulk import: what to store, under which
// label and identity.
type BulkAccount struct {
	Name     string
	Identity string
	AuthKind credentials.AuthKind
	Secret   credentials.Secret
	// ItemName is the item's own "name", the only label that replaces a matched
	// Codex connection's name: the reference spreads the item over the row
	// (connectionsRepo.js:283) and displayName is a separate field there.
	ItemName string
	// Disabled is an item that says isActive: false; any other item is stored or
	// updated enabled (bulk-import/route.js:103, connectionsRepo.js:308).
	Disabled bool
	// Carries names the credential keys the item sets, with or without a value:
	// only those replace a matched Codex connection's (connectionsRepo.js:283).
	Carries Carries
	// Priority is the item's numeric priority when HasPriority. A non-zero one
	// places a new connection (connectionsRepo.js:293-294); any one replaces a
	// matched connection's, and zero or null there sorts it last as the
	// reference's (priority || 999) does (connectionsRepo.js:159).
	Priority    int
	HasPriority bool
}

// Carries names which of a credential's optional keys an import sets, so that
// a matched Codex connection keeps the ones it leaves out and clears the ones it
// sets to nothing, as a spread does.
type Carries struct {
	RefreshToken, IDToken, Expiry bool
}

// CarriesAll is a credential that sets every key, as a sign-in does.
var CarriesAll = Carries{RefreshToken: true, IDToken: true, Expiry: true}

// AsksIdentityUpFront reports whether a sign-in must name its account before it
// starts: only when the flow neither resolves nor usually infers one.
func (f Flow) AsksIdentityUpFront() bool {
	return !f.ResolvesIdentity && !f.InfersIdentity
}

// ImportKeys is the operator input the flow's Import step reads.
func (f Flow) ImportKeys() []string {
	if f.ImportMetaKeys != nil {
		return f.ImportMetaKeys
	}
	return f.MetaKeys
}

// AuthMethodFor resolves the auth method a start asked for against the flow's
// closed set: a blank value takes the default, and a value outside the set, or a
// blank one with no default, is refused. A flow without AuthMethods accepts
// anything and resolves to "".
func (f Flow) AuthMethodFor(requested string) (string, bool) {
	if len(f.AuthMethods) == 0 {
		return "", true
	}
	requested = strings.TrimSpace(requested)
	if requested == "" {
		requested = f.DefaultAuthMethod
	}
	for _, method := range f.AuthMethods {
		if requested != "" && method == requested {
			return method, true
		}
	}
	return "", false
}

// browserFlow is a companion browser sign-in for Flow.Browser.
func browserFlow(flow Flow) *Flow {
	return &flow
}

// Startable reports whether the flow can start a browser or device sign-in.
func (f Flow) Startable() bool {
	return f.Authorize != nil
}

// RedirectSource is where a flow's redirect URI comes from.
type RedirectSource int

const (
	// RedirectFromOperator means the flow sends Params.RedirectURI, so the handler
	// must resolve it from the configured public origin.
	RedirectFromOperator RedirectSource = iota
	// RedirectPinned means the flow hardcodes the redirect the provider registered
	// (Claude's console page, Codex's localhost:1455, xAI's 127.0.0.1:56121). The
	// handler must not resolve one: the flow would discard it.
	RedirectPinned
	// RedirectUnused means the flow never sends a redirect at all: a device flow
	// shows a code and polls, Zed's native app returns to its own loopback port,
	// and an import flow has no browser step.
	RedirectUnused
)

// NeedsRedirectURI reports whether the handler must resolve a redirect URI for
// this flow. It is derived from the flow's own declaration rather than from its
// type, because the three cases do not line up with the six flow types: two
// authorization-code flows pin their redirect and one never uses one.
func (f Flow) NeedsRedirectURI() bool {
	return f.RedirectSource == RedirectFromOperator
}

// AuthRequest is the result of starting a flow.
type AuthRequest struct {
	// URL is the browser URL, empty for device_code until the device code arrives.
	URL string
	// DeviceCode is the provider-issued handle the poll step redeems. It is the
	// only way a device flow can complete, so it travels with the request and is
	// stored server-side; the browser only ever sees the state.
	DeviceCode string
	// UserCode and VerificationURL are the device-code display values.
	UserCode        string
	VerificationURL string
	// Interval is the device-code poll interval.
	Interval time.Duration
	// ExpiresIn bounds the device code lifetime.
	ExpiresIn time.Duration
	// Verifier overrides the caller-generated PKCE verifier when the flow mints
	// its own (Qoder's device handshake does).
	Verifier string
	// Extra is flow-private state the poll step needs (Kiro's client id/secret,
	// Qoder's machine id). It never leaves the server.
	Extra map[string]string
}

// Result is a completed sign-in, ready to persist.
type Result struct {
	Secret      credentials.Secret
	Identity    string
	DisplayName string
	// FallbackName labels the connection when neither the operator, the provider
	// nor an identity names it. Empty means the reference's "Account N".
	FallbackName string
	// AuthKind is the credential mode to store. Most flows store oauth; a PAT,
	// cookie or API-key import stores its own kind.
	AuthKind credentials.AuthKind
	// AfterSave is provider work the reference starts once a sign-in has its
	// tokens and does not wait for (Antigravity's Code Assist onboarding). The
	// handler runs it in the background after the connection is saved, on a
	// context the request's end does not cancel. It saves nothing.
	AfterSave func(context.Context)
}

// PollResult is one device-code poll outcome.
type PollResult struct {
	Done    bool
	Pending bool
	// SlowDown is a pending answer that also asks for a longer interval (RFC 8628
	// slow_down); the handler lengthens the attempt's interval.
	SlowDown bool
	Result   Result
	// Denied and Expired distinguish the two terminal rejections the reference
	// reports separately, so the dashboard can tell "you said no" from "the code
	// timed out". Pending is never an error.
	Denied  bool
	Expired bool
	// Err describes any other terminal failure.
	Err error
}

// HiddenProviders names flows implemented for a provider the product keeps hidden
// (docs/PROVIDER_BASELINE.md "Hidden reference providers"). The flow exists and is
// tested so the parity claim stays honest, but no catalog entry serves it, so no
// endpoint can reach it. Listing them here — rather than silently omitting the
// flow — keeps "hidden" a decision instead of an accident.
func HiddenProviders() map[string]bool {
	return map[string]bool{
		"trae":     true, // no tool-calling support in the reference
		"windsurf": true, // no tool-calling/transport support in the reference
	}
}

// Registry holds every implemented flow, keyed by provider id.
type Registry struct {
	flows map[string]Flow
}

// NewRegistry builds the flow registry from the built-in flow set.
func NewRegistry() *Registry {
	registry := &Registry{flows: make(map[string]Flow)}
	for _, flow := range builtinFlows() {
		registry.flows[flow.Provider] = flow
	}
	return registry
}

// Lookup returns one provider's flow.
func (r *Registry) Lookup(provider string) (Flow, bool) {
	if r == nil {
		return Flow{}, false
	}
	flow, ok := r.flows[provider]
	return flow, ok
}

// LookupBrowser returns the browser sign-in of one provider: its flow when that is
// a browser flow, or the browser sign-in offered beside its device flow.
func (r *Registry) LookupBrowser(provider string) (Flow, bool) {
	flow, ok := r.Lookup(provider)
	if !ok {
		return Flow{}, false
	}
	if flow.Type == FlowDeviceCode {
		if flow.Browser == nil {
			return Flow{}, false
		}
		return *flow.Browser, true
	}
	return flow, true
}

// Providers lists the providers with an implemented flow, sorted.
func (r *Registry) Providers() []string {
	if r == nil {
		return nil
	}
	ids := make([]string, 0, len(r.flows))
	for id := range r.flows {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

// BulkProviders lists every provider whose flow implements a bulk import, from
// both tables, so a test can cover each one rather than naming two by hand.
func BulkProviders() []string {
	seen := make(map[string]bool)
	for provider, flow := range ManualFlows() {
		if flow.Bulk != nil {
			seen[provider] = true
		}
	}
	registry := NewRegistry()
	for _, provider := range registry.Providers() {
		if flow, ok := registry.Lookup(provider); ok && flow.Bulk != nil {
			seen[provider] = true
		}
	}
	providers := make([]string, 0, len(seen))
	for provider := range seen {
		providers = append(providers, provider)
	}
	sort.Strings(providers)
	return providers
}

// OnboardingFlows reports each provider's implemented flow type, which the parity
// gate compares against the reference's declared flows.
func (r *Registry) OnboardingFlows() map[string]string {
	flows := make(map[string]string, len(r.flows))
	for id, flow := range r.flows {
		flows[id] = string(flow.Type)
	}
	return flows
}

// ErrRedirectOriginUnset means the provider needs the operator-facing origin and
// none is configured. Guessing one would send the provider a redirect_uri the
// operator's browser never reaches.
var ErrRedirectOriginUnset = errors.New("provider redirect origin is not configured; set the public origin")

// DefaultRedirectURI builds the redirect a provider registered. Providers whose
// client registration pins a loopback port use it; every other provider uses the
// configured public origin, and an unset origin is an error rather than a guess.
func DefaultRedirectURI(flow Flow, publicOrigin string) (string, error) {
	path := flow.CallbackPath
	if path == "" {
		path = "/callback"
	}
	if flow.FixedPort > 0 {
		host := flow.FixedHost
		if host == "" {
			host = "localhost"
		}
		return fmt.Sprintf("http://%s:%d%s", host, flow.FixedPort, path), nil
	}
	origin := strings.TrimSuffix(strings.TrimSpace(publicOrigin), "/")
	if origin == "" {
		return "", ErrRedirectOriginUnset
	}
	return origin + path, nil
}

// appendQuery sets query parameters on a URL, preserving existing ones.
func appendQuery(base string, values url.Values) (string, error) {
	parsed, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("invalid authorize endpoint: %w", err)
	}
	query := parsed.Query()
	for key, items := range values {
		for _, value := range items {
			query.Set(key, value)
		}
	}
	parsed.RawQuery = query.Encode()
	return parsed.String(), nil
}
