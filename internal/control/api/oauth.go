package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/claude"
	"github.com/raufimusaddiq/routeweft/internal/providers/codex"
	"github.com/raufimusaddiq/routeweft/internal/providers/kilocode"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

const (
	oauthFlowTTL       = 10 * time.Minute
	oauthMaxPerSession = 8
	oauthMaxPending    = 256
	oauthMaxInput      = 4096
	oauthMaxNameLength = 200
	oauthStateEntropy  = 32
	// oauthMinPollInterval bounds upstream polling per attempt regardless of how
	// fast the browser asks.
	oauthMinPollInterval = 2 * time.Second
	oauthInputPasteCode  = "code"
	oauthInputPasteURL   = "redirect-url"
)

// oauthFlow is one pending authorization attempt. The verifier never leaves the
// server; the browser only ever holds the opaque state.
type oauthFlow struct {
	sessionID string
	provider  string
	verifier  string
	// deviceCode is set for device-authorization attempts. Browser requests
	// carry only the opaque state. A provider whose protocol uses one code for
	// both roles (Kilo Code) must also show it to the operator, so the code is
	// never treated as hidden from them; it is kept out of poll responses and
	// logs.
	deviceCode string
	nextPoll   time.Time
	// ttl, when set, caps the attempt's lifetime below oauthFlowTTL (a device
	// code that expires sooner than the flow must not outlive its code).
	ttl time.Duration
	// completing marks an approved attempt whose connection is being saved, so a
	// concurrent poll cannot save it twice.
	completing bool
	name       string
	identity   string
	expiresAt  time.Time
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
	ttl := oauthFlowTTL
	if flow.ttl > 0 && flow.ttl < ttl {
		ttl = flow.ttl
	}
	flow.expiresAt = now.Add(ttl)
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

// claimPoll admits one upstream poll for a device attempt. A poll that arrives
// before the provider's interval has elapsed is throttled so a fast browser loop
// cannot hammer the provider, and an unknown, expired, foreign-session or
// foreign-provider state is indistinguishable to the caller, as in consume.
func (f *oauthFlows) claimPoll(state, sessionID, provider string, interval time.Duration) (flow oauthFlow, found, throttled bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	flow, ok := f.pending[state]
	if !ok || flow.deviceCode == "" {
		return oauthFlow{}, false, false
	}
	now := f.now()
	if !now.Before(flow.expiresAt) {
		delete(f.pending, state)
		return oauthFlow{}, false, false
	}
	if subtle.ConstantTimeCompare([]byte(flow.sessionID), []byte(sessionID)) != 1 || flow.provider != provider {
		return oauthFlow{}, false, false
	}
	if flow.completing || now.Before(flow.nextPoll) {
		return flow, true, true
	}
	flow.nextPoll = now.Add(interval)
	f.pending[state] = flow
	return flow, true, false
}

// reserveResult says why reserve did or did not take the attempt.
type reserveResult int

const (
	reserveTaken reserveResult = iota
	// reserveBusy: another poll is saving this attempt right now.
	reserveBusy
	// reserveInvalid: unknown, expired or not this session's or provider's.
	reserveInvalid
)

// reserve takes the exclusive right to save an approved attempt. It reports
// reserveInvalid for an unknown, expired or foreign state and reserveBusy for an
// attempt another poll is already completing. The attempt stays pending until
// finish, so a failed save does not lose a token that was already issued
// upstream; release hands the attempt back for another poll.
func (f *oauthFlows) reserve(state, sessionID, provider string) (oauthFlow, reserveResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	flow, ok := f.pending[state]
	if !ok || !f.now().Before(flow.expiresAt) {
		return oauthFlow{}, reserveInvalid
	}
	if subtle.ConstantTimeCompare([]byte(flow.sessionID), []byte(sessionID)) != 1 || flow.provider != provider {
		return oauthFlow{}, reserveInvalid
	}
	if flow.completing {
		return oauthFlow{}, reserveBusy
	}
	flow.completing = true
	f.pending[state] = flow
	return flow, reserveTaken
}

// release reopens a reserved attempt after a save that persisted nothing.
func (f *oauthFlows) release(state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if flow, ok := f.pending[state]; ok {
		flow.completing = false
		f.pending[state] = flow
	}
}

// finish ends an attempt for good.
func (f *oauthFlows) finish(state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.pending, state)
}

// oauthProvider adapts one provider's engine to the shared start/callback flow.
// Claude and Codex differ in PKCE type, authorize parameters and token-request
// encoding; those differences stay inside the engines.
//
// A provider offers either the browser authorization-code flow (authorize and
// exchange) or the device-authorization flow (device), never both.
type oauthProvider struct {
	completion string
	authorize  func() (verifier, authorizeURL string, err error)
	exchange   func(ctx context.Context, client OAuthClient, verifier, code string) (credentials.Secret, error)
	device     *deviceProvider
}

// deviceProvider is one provider's device-authorization engine. poll reports an
// outcome for a single check; it never blocks waiting for the operator.
type deviceProvider struct {
	request func(ctx context.Context, client OAuthClient) (deviceStart, error)
	poll    func(ctx context.Context, client OAuthClient, deviceCode string) (devicePoll, error)
}

type deviceStart struct {
	deviceCode, userCode, verificationURL string
	interval, expiresIn                   time.Duration
}

type devicePoll struct {
	outcome string // "pending", "approved", "denied" or "expired"
	secret  credentials.Secret
}

// OAuthClient is the HTTP client used for token exchange. A nil client uses the
// engines' SSRF-protected default.
type OAuthClient interface {
	Do(*http.Request) (*http.Response, error)
}

var oauthProviders = map[string]oauthProvider{
	"kilocode": {device: &deviceProvider{
		request: func(ctx context.Context, client OAuthClient) (deviceStart, error) {
			device, err := kilocode.RequestDevice(ctx, kilocode.Config{}, client)
			return deviceStart{deviceCode: device.Code, userCode: device.Code, verificationURL: device.VerificationURL, interval: device.Interval, expiresIn: device.ExpiresIn}, err
		},
		poll: func(ctx context.Context, client OAuthClient, deviceCode string) (devicePoll, error) {
			poll, err := kilocode.PollDevice(ctx, kilocode.Config{}, deviceCode, client)
			// The approved token is a non-expiring bearer with no refresh token.
			return devicePoll{outcome: string(poll.Outcome), secret: credentials.Secret{AccessToken: poll.Token}}, err
		},
	}},
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
func (h *Handler) oauthProviderFor(w http.ResponseWriter, id string, device bool) (oauthProvider, bool) {
	provider, known := oauthProviders[id]
	spec, inCatalog := h.providerSpec(id)
	if !known || !inCatalog || !specAllowsOAuth(spec) || (provider.device != nil) != device {
		message := "this provider does not offer browser sign-in"
		if device {
			message = "this provider does not offer device sign-in"
		}
		writeError(w, http.StatusNotFound, "oauth_unsupported", message)
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
	provider, ok := h.oauthProviderFor(w, providerID, false)
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
	provider, ok := h.oauthProviderFor(w, providerID, false)
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
	if saved, _, ok := h.saveOAuthConnection(w, r, providerID, flow, secret); ok {
		writeJSON(w, http.StatusOK, saved)
	}
}

// handleOAuthDeviceStart begins a device authorization and returns what the
// operator must do: open the verification URL and confirm the code. The device
// code stays server-side; the browser holds only the opaque state.
func (h *Handler) handleOAuthDeviceStart(w http.ResponseWriter, r *http.Request) {
	if h.opts.Credentials == nil {
		writeError(w, http.StatusServiceUnavailable, "credentials_unavailable", "configure ROUTEWEFT_CREDENTIAL_KEY to manage provider connections")
		return
	}
	providerID := r.PathValue("provider")
	provider, ok := h.oauthProviderFor(w, providerID, true)
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
	device, err := provider.device.request(r.Context(), h.opts.OAuthClient)
	if errors.Is(err, kilocode.ErrRateLimited) {
		writeError(w, http.StatusTooManyRequests, "oauth_rate_limited", "the provider has too many pending sign-ins; wait a few minutes and try again")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "oauth_start_failed", "the provider did not start a sign-in; try again shortly")
		return
	}
	state, expiresAt, ok := h.oauth.begin(oauthFlow{sessionID: session.ID, provider: providerID, deviceCode: device.deviceCode, name: name, identity: identity, ttl: device.expiresIn, nextPoll: h.oauth.now().Add(device.interval)})
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "oauth_busy", "too many sign-ins in progress; try again shortly")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"state": state, "userCode": device.userCode, "verificationUrl": device.verificationURL, "intervalSeconds": int(device.interval / time.Second), "expiresAt": expiresAt})
}

// handleOAuthDevicePoll checks a device authorization once. Pending answers 200
// so the dashboard keeps polling; approval redeems the attempt exactly once and
// stores the connection; denial and expiry end the attempt.
func (h *Handler) handleOAuthDevicePoll(w http.ResponseWriter, r *http.Request) {
	if h.opts.Credentials == nil {
		writeError(w, http.StatusServiceUnavailable, "credentials_unavailable", "configure ROUTEWEFT_CREDENTIAL_KEY to manage provider connections")
		return
	}
	providerID := r.PathValue("provider")
	provider, ok := h.oauthProviderFor(w, providerID, true)
	if !ok {
		return
	}
	var body struct {
		State string `json:"state"`
	}
	if !decodeJSON(w, r, 4<<10, &body) {
		return
	}
	session, ok := h.sessionFor(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "admin session is required")
		return
	}
	flow, found, throttled := h.oauth.claimPoll(body.State, session.ID, providerID, oauthMinPollInterval)
	if !found {
		writeError(w, http.StatusBadRequest, "oauth_state_invalid", "this sign-in attempt expired or was already used; start again")
		return
	}
	if throttled {
		writeJSON(w, http.StatusOK, map[string]any{"status": "pending"})
		return
	}
	poll, err := provider.device.poll(r.Context(), h.opts.OAuthClient, flow.deviceCode)
	if err != nil {
		writeError(w, http.StatusBadGateway, "oauth_poll_failed", "the provider could not be reached; the sign-in is still open")
		return
	}
	switch poll.outcome {
	case "approved":
		// Reserve, save, then finish: of two concurrent approved polls only one
		// saves, and a save that persisted nothing leaves the attempt open so the
		// token already issued upstream is not lost.
		flow, reserved := h.oauth.reserve(body.State, session.ID, providerID)
		if reserved == reserveBusy {
			// Lost the race to a poll that is saving this same approval; the
			// dashboard keeps polling and sees the connection.
			writeJSON(w, http.StatusOK, map[string]any{"status": "pending"})
			return
		}
		if reserved != reserveTaken {
			writeError(w, http.StatusBadRequest, "oauth_state_invalid", "this sign-in attempt expired or was already used; start again")
			return
		}
		saved, persisted, ok := h.saveOAuthConnection(w, r, providerID, flow, poll.secret)
		if persisted {
			h.oauth.finish(body.State)
		} else {
			h.oauth.release(body.State)
		}
		if ok {
			writeJSON(w, http.StatusOK, map[string]any{"status": "connected", "connection": saved})
		}
	case "denied":
		h.oauth.consume(body.State, session.ID, providerID)
		writeError(w, http.StatusBadRequest, "oauth_denied", "the provider did not approve the sign-in; start again")
	case "expired":
		h.oauth.consume(body.State, session.ID, providerID)
		writeError(w, http.StatusBadRequest, "oauth_expired", "the sign-in code expired; start again")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"status": "pending"})
	}
}

// saveOAuthConnection persists a completed sign-in as a new enabled connection,
// last in routing order, and answers the request. Shared by both flows.
func (h *Handler) saveOAuthConnection(w http.ResponseWriter, r *http.Request, providerID string, flow oauthFlow, secret credentials.Secret) (saved map[string]any, persisted, ok bool) {
	node, err := h.builtinNode(r.Context(), providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "connection_save_failed", "provider connection could not be saved")
		return nil, persisted, false
	}
	// New connections go last in routing order, as the manual form does.
	existing, err := h.opts.Credentials.ListConnections(r.Context(), providerID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "connection_save_failed", "provider connection could not be saved")
		return nil, persisted, false
	}
	priority := len(existing)
	connection, err := h.opts.Credentials.PutConnection(r.Context(), credentials.Connection{NodeID: node.ID, Name: flow.name, AuthKind: credentials.AuthOAuth, Identity: flow.identity, Secret: secret, Enabled: true, Priority: priority})
	if err != nil {
		writeError(w, http.StatusBadRequest, "connection_save_failed", "provider connection could not be saved; check its identity")
		return nil, persisted, false
	}
	// PutConnection returns the metadata as given; the registry's exchanger lookup
	// keys on the provider.
	persisted = true
	connection.ProviderID = node.ProviderID
	if h.opts.CredentialRegistry != nil {
		h.opts.CredentialRegistry.Register(connection, nil)
	}
	if h.opts.PoolBindings != nil {
		if _, err := h.opts.PoolBindings.RefreshPoolBindings(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "connection_publish_failed", "connection saved but runtime proxy bindings could not be refreshed")
			return nil, persisted, false
		}
	}
	return connectionJSON(connection), true, true
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
