package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/claude"
	"github.com/raufimusaddiq/routeweft/internal/providers/codex"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

const (
	oauthFlowTTL        = 10 * time.Minute
	oauthMaxPerSession  = 8
	oauthMaxPending     = 256
	oauthMaxInput       = 4096
	oauthMaxNameLength  = 200
	oauthStateEntropy   = 32
	oauthInputPasteCode = "code"
	oauthInputPasteURL  = "redirect-url"
)

// oauthFlow is one pending authorization attempt. The verifier never leaves the
// server; the browser only ever holds the opaque state.
type oauthFlow struct {
	sessionID string
	provider  string
	verifier  string
	name      string
	identity  string
	expiresAt time.Time
}

// oauthFlows holds pending attempts in process memory, like admin sessions: a
// restart abandons in-flight sign-ins and the operator starts again.
type oauthFlows struct {
	mu      sync.Mutex
	pending map[string]oauthFlow
	now     func() time.Time
}

func newOAuthFlows() *oauthFlows {
	return &oauthFlows{pending: make(map[string]oauthFlow), now: time.Now}
}

// begin registers a flow bound to sessionID and returns its state token. Expired
// entries are swept first; a session over its limit loses its oldest attempt.
func (f *oauthFlows) begin(flow oauthFlow) (string, time.Time, bool) {
	raw := make([]byte, oauthStateEntropy)
	if _, err := rand.Read(raw); err != nil {
		return "", time.Time{}, false
	}
	state := base64.RawURLEncoding.EncodeToString(raw)
	f.mu.Lock()
	defer f.mu.Unlock()
	now := f.now()
	flow.expiresAt = now.Add(oauthFlowTTL)
	oldest, owned := "", 0
	for key, existing := range f.pending {
		if !now.Before(existing.expiresAt) {
			delete(f.pending, key)
			continue
		}
		if existing.sessionID == flow.sessionID {
			owned++
			if oldest == "" || existing.expiresAt.Before(f.pending[oldest].expiresAt) {
				oldest = key
			}
		}
	}
	if owned >= oauthMaxPerSession {
		delete(f.pending, oldest)
	}
	if len(f.pending) >= oauthMaxPending {
		return "", time.Time{}, false
	}
	f.pending[state] = flow
	return state, flow.expiresAt, true
}

// consume redeems a state exactly once. A state that is unknown, expired, bound
// to another session or issued for another provider is indistinguishable to the
// caller, and a foreign session never burns the owner's attempt.
func (f *oauthFlows) consume(state, sessionID, provider string) (oauthFlow, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	flow, ok := f.pending[state]
	if !ok {
		return oauthFlow{}, false
	}
	if !f.now().Before(flow.expiresAt) {
		delete(f.pending, state)
		return oauthFlow{}, false
	}
	if subtle.ConstantTimeCompare([]byte(flow.sessionID), []byte(sessionID)) != 1 || flow.provider != provider {
		return oauthFlow{}, false
	}
	delete(f.pending, state)
	return flow, true
}

// oauthProvider adapts one provider's engine to the shared start/callback flow.
// Claude and Codex differ in PKCE type, authorize parameters and token-request
// encoding; those differences stay inside the engines.
type oauthProvider struct {
	completion string
	authorize  func() (verifier, authorizeURL string, err error)
	exchange   func(ctx context.Context, client OAuthClient, verifier, code string) (credentials.Secret, error)
}

// OAuthClient is the HTTP client used for token exchange. A nil client uses the
// engines' SSRF-protected default.
type OAuthClient interface {
	Do(*http.Request) (*http.Response, error)
}

var oauthProviders = map[string]oauthProvider{
	"claude": {
		completion: oauthInputPasteCode,
		authorize: func() (string, string, error) {
			pkce, err := claude.NewPKCE()
			return pkce.Verifier, claude.AuthorizeURL(pkce), err
		},
		exchange: func(ctx context.Context, client OAuthClient, verifier, code string) (credentials.Secret, error) {
			tokens, err := claude.ExchangeCode(ctx, claude.ChallengeFor(verifier), code, client)
			return credentials.Secret{AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, Expiry: tokens.Expiry}, err
		},
	},
	"codex": {
		completion: oauthInputPasteURL,
		authorize: func() (string, string, error) {
			pkce, err := codex.NewPKCE()
			return pkce.Verifier, codex.AuthorizeURL(pkce), err
		},
		exchange: func(ctx context.Context, client OAuthClient, verifier, code string) (credentials.Secret, error) {
			tokens, err := codex.ExchangeCode(ctx, codex.ChallengeFor(verifier), code, client)
			return credentials.Secret{AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken, IDToken: tokens.IDToken, Expiry: tokens.Expiry}, err
		},
	},
}

// oauthProviderFor resolves the path provider to an OAuth-capable catalog entry.
func (h *Handler) oauthProviderFor(w http.ResponseWriter, id string) (oauthProvider, bool) {
	provider, known := oauthProviders[id]
	spec, inCatalog := h.providerSpec(id)
	if !known || !inCatalog || !specAllowsOAuth(spec) {
		writeError(w, http.StatusNotFound, "oauth_unsupported", "this provider does not offer browser sign-in")
		return oauthProvider{}, false
	}
	return provider, true
}

func specAllowsOAuth(spec registry.Spec) bool {
	if spec.Auth == registry.AuthOAuth {
		return true
	}
	for _, mode := range spec.AuthModes {
		if mode == registry.AuthOAuth {
			return true
		}
	}
	return false
}

// handleOAuthStart mints PKCE and a session-bound state, and returns the
// provider authorize URL. Nothing is persisted until the callback succeeds.
func (h *Handler) handleOAuthStart(w http.ResponseWriter, r *http.Request) {
	if h.opts.Credentials == nil {
		writeError(w, http.StatusServiceUnavailable, "credentials_unavailable", "configure ROUTEWEFT_CREDENTIAL_KEY to manage provider connections")
		return
	}
	providerID := r.PathValue("provider")
	provider, ok := h.oauthProviderFor(w, providerID)
	if !ok {
		return
	}
	var body struct {
		Name     string `json:"name"`
		Identity string `json:"identity"`
	}
	if !decodeJSON(w, r, 8<<10, &body) {
		return
	}
	name, identity := strings.TrimSpace(body.Name), strings.TrimSpace(body.Identity)
	if name == "" || identity == "" || len(name) > oauthMaxNameLength || len(identity) > oauthMaxNameLength {
		writeError(w, http.StatusBadRequest, "invalid_request", "account name and stable account identity are required")
		return
	}
	session, ok := h.sessionFor(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "admin session is required")
		return
	}
	verifier, authorizeURL, err := provider.authorize()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "oauth_start_failed", "could not start sign-in")
		return
	}
	state, expiresAt, ok := h.oauth.begin(oauthFlow{sessionID: session.ID, provider: providerID, verifier: verifier, name: name, identity: identity})
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "oauth_busy", "too many sign-ins in progress; try again shortly")
		return
	}
	parsed, err := url.Parse(authorizeURL)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "oauth_start_failed", "could not start sign-in")
		return
	}
	query := parsed.Query()
	query.Set("state", state)
	parsed.RawQuery = query.Encode()
	writeJSON(w, http.StatusOK, map[string]any{"state": state, "authorizeUrl": parsed.String(), "completion": provider.completion, "expiresAt": expiresAt})
}

// handleOAuthCallback completes a sign-in. The provider-registered redirect URIs
// point at the provider's own page (Claude) or the operator's loopback (Codex),
// never at Routeweft, so the operator hands the result back here from the
// authenticated dashboard. Responses never carry provider text or token data.
func (h *Handler) handleOAuthCallback(w http.ResponseWriter, r *http.Request) {
	if h.opts.Credentials == nil {
		writeError(w, http.StatusServiceUnavailable, "credentials_unavailable", "configure ROUTEWEFT_CREDENTIAL_KEY to manage provider connections")
		return
	}
	providerID := r.PathValue("provider")
	provider, ok := h.oauthProviderFor(w, providerID)
	if !ok {
		return
	}
	var body struct {
		State string `json:"state"`
		Input string `json:"input"`
	}
	if !decodeJSON(w, r, 16<<10, &body) {
		return
	}
	session, ok := h.sessionFor(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "admin session is required")
		return
	}
	code, denied, pastedState := parseOAuthInput(body.Input)
	if !denied && code == "" {
		// Nothing usable was pasted. Refuse before consuming so the operator can
		// paste again instead of restarting the single-use attempt.
		writeError(w, http.StatusBadRequest, "oauth_code_missing", "no authorization code was found in what was pasted; check it and try again")
		return
	}
	if pastedState != "" && subtle.ConstantTimeCompare([]byte(pastedState), []byte(body.State)) != 1 {
		// Output from a different attempt: refuse without consuming this one.
		writeError(w, http.StatusBadRequest, "oauth_state_invalid", "this sign-in attempt expired or was already used; start again")
		return
	}
	flow, ok := h.oauth.consume(body.State, session.ID, providerID)
	if !ok {
		writeError(w, http.StatusBadRequest, "oauth_state_invalid", "this sign-in attempt expired or was already used; start again")
		return
	}
	if denied {
		writeError(w, http.StatusBadRequest, "oauth_denied", "the provider did not approve the sign-in; start again")
		return
	}
	secret, err := provider.exchange(r.Context(), h.opts.OAuthClient, flow.verifier, code)
	if err != nil {
		writeError(w, http.StatusBadGateway, "oauth_exchange_failed", "the provider did not accept the authorization code; start again")
		return
	}
	node, err := h.builtinNode(r.Context(), providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "connection_save_failed", "provider connection could not be saved")
		return
	}
	// New connections go last in routing order, as the manual form does.
	existing, err := h.opts.Credentials.ListConnections(r.Context(), providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "connection_save_failed", "provider connection could not be saved")
		return
	}
	priority := len(existing)
	connection, err := h.opts.Credentials.PutConnection(r.Context(), credentials.Connection{NodeID: node.ID, Name: flow.name, AuthKind: credentials.AuthOAuth, Identity: flow.identity, Secret: secret, Enabled: true, Priority: priority})
	if err != nil {
		writeError(w, http.StatusBadRequest, "connection_save_failed", "provider connection could not be saved; check its identity")
		return
	}
	// PutConnection returns the metadata as given; the registry's exchanger lookup
	// keys on the provider.
	connection.ProviderID = node.ProviderID
	if h.opts.CredentialRegistry != nil {
		h.opts.CredentialRegistry.Register(connection, nil)
	}
	if h.opts.PoolBindings != nil {
		if _, err := h.opts.PoolBindings.RefreshPoolBindings(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "connection_publish_failed", "connection saved but runtime proxy bindings could not be refreshed")
			return
		}
	}
	writeJSON(w, http.StatusOK, connectionJSON(connection))
}

// parseOAuthInput accepts what the operator can copy: a bare code, Claude's
// "code#state", or the whole redirect URL (or its query) from Codex. It returns
// only values to act on; provider error text is reduced to the denied flag.
func parseOAuthInput(input string) (code string, denied bool, state string) {
	input = strings.TrimSpace(input)
	if input == "" || len(input) > oauthMaxInput || strings.IndexFunc(input, unicode.IsControl) >= 0 {
		return "", false, ""
	}
	if strings.Contains(input, "://") || strings.HasPrefix(input, "?") || strings.Contains(input, "code=") || strings.Contains(input, "error=") {
		raw := input
		if parsed, err := url.Parse(input); err == nil && parsed.Scheme != "" {
			raw = parsed.RawQuery
		}
		query, err := url.ParseQuery(strings.TrimPrefix(raw, "?"))
		if err != nil {
			return "", false, ""
		}
		if query.Get("error") != "" {
			return "", true, query.Get("state")
		}
		return cleanCode(query.Get("code")), false, query.Get("state")
	}
	if head, tail, found := strings.Cut(input, "#"); found {
		return cleanCode(head), false, tail
	}
	return cleanCode(input), false, ""
}

func cleanCode(code string) string {
	if code == "" || strings.IndexFunc(code, unicode.IsSpace) >= 0 {
		return ""
	}
	return code
}
