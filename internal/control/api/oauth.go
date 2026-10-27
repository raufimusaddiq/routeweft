package api

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/onboarding"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

const (
	oauthFlowTTL       = 10 * time.Minute
	oauthMaxPerSession = 8
	oauthMaxPending    = 256
	oauthMaxInput      = 4096
	// oauthMaxImportInput bounds one imported credential (#218). An import can be
	// a whole exported file (a Kiro CLIProxyAPI or IDE token JSON, a Xiaomi
	// auth.json, a full iFlow cookie string), which a Microsoft token pair alone
	// can push past the 4 KiB a pasted code is held to. 64 KiB is far past any
	// real credential file and still bounds what a flow parses.
	oauthMaxImportInput = 64 << 10
	oauthMaxNameLength  = 200
	oauthStateEntropy   = 32
	// oauthMaxBulkItems bounds one bulk import. Each accepted item costs a
	// connection write and, historically, a full snapshot recompile; without a cap
	// a 1 MiB paste is thousands of serial recompiles on the control plane. 100
	// accounts is far past any real paste of exported tokens and keeps the work
	// per request bounded.
	oauthMaxBulkItems = 100
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
	// extra is flow-private state the poll or exchange needs (Kiro's registered
	// client credentials, Qoder's machine id, Kimi's device id). It never leaves
	// the server and never reaches the browser.
	extra map[string]string
	// meta is the operator input supplied at start (GitLab's client id and base
	// URL, Kiro's region and start URL). Exchange and Import read it, so it has to
	// survive on the attempt; keeping only Extra dropped it between start and
	// callback.
	meta     map[string]string
	name     string
	identity string
	// identityOptional is the flow's IdentityOptional: a credential that names no
	// account is saved without an identity instead of refused.
	identityOptional bool
	// target is the connection a Reauthenticate started for (#215). The sign-in
	// then renews that connection's credential and nothing else: an answer for
	// another account is refused rather than added or written over another row.
	target    string
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
// newState mints one opaque sign-in state. It is minted before the flow runs so a
// flow that embeds {p.State} in its authorize URL writes the real value, rather
// than an empty one the handler then repairs on the returned URL.
func newState() (string, bool) {
	raw := make([]byte, oauthStateEntropy)
	if _, err := rand.Read(raw); err != nil {
		return "", false
	}
	return base64.RawURLEncoding.EncodeToString(raw), true
}

func (f *oauthFlows) begin(state string, flow oauthFlow) (time.Time, bool) {
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
		return time.Time{}, false
	}
	f.pending[state] = flow
	return flow.expiresAt, true
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

// cancel ends an attempt the operator abandoned, with the same session and
// provider binding as consume. An attempt a poll is saving right now is left to
// finish: its token is already issued, and dropping it mid-save would orphan it.
func (f *oauthFlows) cancel(state, sessionID, provider string) reserveResult {
	f.mu.Lock()
	defer f.mu.Unlock()
	flow, ok := f.pending[state]
	if !ok || !f.now().Before(flow.expiresAt) {
		return reserveInvalid
	}
	if subtle.ConstantTimeCompare([]byte(flow.sessionID), []byte(sessionID)) != 1 || flow.provider != provider {
		return reserveInvalid
	}
	if flow.completing {
		return reserveBusy
	}
	delete(f.pending, state)
	return reserveTaken
}

// finish ends an attempt for good.
func (f *oauthFlows) finish(state string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.pending, state)
}

// oauthProvider adapts one onboarding flow to the shared start/callback/poll
// machinery. The flow shapes and their per-provider implementations live in
// internal/providers/onboarding, so this layer owns only the HTTP contract:
// state, session binding, single use, durable save and publication.
type oauthProvider struct {
	flow onboarding.Flow
}

// OAuthClient is the HTTP client used for provider sign-in calls. A nil client
// uses the SSRF-protected default the handler was built with.
type OAuthClient interface {
	Do(*http.Request) (*http.Response, error)
}

// providerFor resolves the path provider to an implemented flow. A browser flow
// and a device flow are mutually exclusive per provider, as in the reference.
func (h *Handler) providerFor(id string, device bool) (onboarding.Flow, bool) {
	flow, known := h.oauthFlows.Lookup(id)
	if !known || (flow.Type == onboarding.FlowDeviceCode) != device {
		return onboarding.Flow{}, false
	}
	// A flow without the step this endpoint drives (cursor has import only) is
	// not offered by that endpoint. Refusing here keeps a nil step from becoming a
	// panic in the handler.
	if !flow.Startable() {
		return onboarding.Flow{}, false
	}
	return flow, true
}

// oauthProviderFor resolves the path provider to an implemented, catalog-backed
// flow. A provider that is not in the catalog, or whose catalog entry accepts no
// OAuth-style credential, is refused even if a flow exists for it.
func (h *Handler) oauthProviderFor(w http.ResponseWriter, id string, device bool) (oauthProvider, bool) {
	flow, ok := h.providerFor(id, device)
	spec, inCatalog := h.providerSpec(id)
	if !ok || !inCatalog || !specAllowsOAuth(spec) {
		message := "this provider does not offer browser sign-in"
		if device {
			message = "this provider does not offer device sign-in"
		}
		writeError(w, http.StatusNotFound, "oauth_unsupported", message)
		return oauthProvider{}, false
	}
	return oauthProvider{flow: flow}, true
}

// specAllowsOAuth reports whether the provider accepts an OAuth-style credential,
// including dual-auth providers whose default is an API key and cookie-auth
// providers whose sign-in yields a session.
func specAllowsOAuth(spec registry.Spec) bool {
	if spec.Auth == registry.AuthOAuth || spec.Auth == registry.AuthCookie {
		return true
	}
	for _, mode := range spec.AuthModes {
		if mode == registry.AuthOAuth || mode == registry.AuthCookie {
			return true
		}
	}
	return false
}

// onboardingAccount validates the operator's optional account name and identity
// for a sign-in or import. The name is a nickname: the reference starts a sign-in
// with neither (OAuthModal.js opens straight to the provider) and labels the
// connection from what the provider reports, so a blank name falls back to that
// and finally to the identity. The identity is required up front only when the
// flow cannot resolve one, rather than surfacing later as a misleading
// connection_save_failed from the store.
func onboardingAccount(w http.ResponseWriter, flow onboarding.Flow, rawName, rawIdentity string) (name, identity string, ok bool) {
	name, identity = strings.TrimSpace(rawName), strings.TrimSpace(rawIdentity)
	if len(name) > oauthMaxNameLength || len(identity) > oauthMaxNameLength {
		writeError(w, http.StatusBadRequest, "invalid_request", fmt.Sprintf("account name and identity must be at most %d characters", oauthMaxNameLength))
		return "", "", false
	}
	if flow.AsksIdentityUpFront() && identity == "" {
		writeError(w, http.StatusBadRequest, "invalid_request", "a stable account identity is required for this provider")
		return "", "", false
	}
	return name, identity, true
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
		// Meta carries operator-supplied provider parameters (GitLab's base URL and
		// client id, Kiro's region and start URL).
		Meta map[string]string `json:"meta"`
		// ConnectionID starts a Reauthenticate of that connection (#215). Its name
		// and identity are the connection's own, so none are read from the body.
		ConnectionID string `json:"connectionId"`
	}
	if !decodeJSON(w, r, 8<<10, &body) {
		return
	}
	session, ok := h.sessionFor(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "admin session is required")
		return
	}
	target := strings.TrimSpace(body.ConnectionID)
	if target != "" {
		connection, found, err := h.providerConnection(r.Context(), providerID, target)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "oauth_start_failed", "could not start sign-in")
			return
		}
		if !found {
			writeError(w, http.StatusNotFound, "connection_not_found", "no connection of this provider has that id")
			return
		}
		body.Name, body.Identity = connection.Name, connection.Identity
	}
	name, identity, ok := onboardingAccount(w, provider.flow, body.Name, body.Identity)
	if !ok {
		return
	}
	// Resolve the redirect only for flows that send one. Zed's native-app
	// handshake and device flows never do, and requiring a configured origin for
	// them refused a sign-in that would have worked.
	redirectURI, ok := h.flowRedirectURI(w, provider.flow)
	if !ok {
		return
	}
	verifier, err := newVerifier()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "oauth_start_failed", "could not start sign-in")
		return
	}
	state, ok := newState()
	if !ok {
		writeError(w, http.StatusInternalServerError, "oauth_start_failed", "could not start sign-in")
		return
	}
	request, err := provider.flow.Authorize(r.Context(), onboarding.Params{
		State:       state,
		RedirectURI: redirectURI,
		Verifier:    verifier,
		Meta:        body.Meta,
		Client:      h.opts.OAuthClient,
	})
	// A missing operator-supplied OAuth client is a documented precondition
	// (SPEC 19.1), so it answers with its own code instead of a generic 500.
	if errors.Is(err, onboarding.ErrGoogleClientUnset) {
		writeError(w, http.StatusServiceUnavailable, "oauth_google_client_unconfigured", err.Error())
		return
	}
	if errors.Is(err, onboarding.ErrGitLabClientIDRequired) {
		writeError(w, http.StatusBadRequest, "oauth_client_id_required", err.Error())
		return
	}
	if errors.Is(err, onboarding.ErrIflowClientUnset) {
		writeError(w, http.StatusServiceUnavailable, "oauth_iflow_client_unconfigured", err.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "oauth_start_failed", "could not start sign-in")
		return
	}
	// Qoder's handshake mints its own PKCE pair, so its verifier wins.
	if request.Verifier != "" {
		verifier = request.Verifier
	}
	expiresAt, ok := h.oauth.begin(state, oauthFlow{
		sessionID: session.ID, provider: providerID, verifier: verifier,
		name: name, identity: identity, identityOptional: provider.flow.IdentityOptional,
		extra: request.Extra, meta: body.Meta, target: target,
	})
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "oauth_busy", "too many sign-ins in progress; try again shortly")
		return
	}
	// The flow wrote {p.State} itself; the URL is not rewritten here, so there is
	// one source for the value rather than two that can disagree.
	authorizeURL := request.URL
	writeJSON(w, http.StatusOK, map[string]any{
		"state":           state,
		"authorizeUrl":    authorizeURL,
		"completion":      provider.flow.Completion,
		"flowType":        provider.flow.Type,
		"userCode":        request.UserCode,
		"verificationUrl": request.VerificationURL,
		"intervalSeconds": int(request.Interval.Seconds()),
		"expiresAt":       expiresAt,
	})
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
	// A callback-completion flow (zed, xiaomi-mimo) parses the pasted URL itself:
	// the credential is not a `code` query parameter but an encrypted payload the
	// flow decrypts. Handing it the extracted code would always be empty, so the
	// operator would be told to paste a URL the endpoint then rejected.
	if provider.flow.Completion == onboarding.CompletionCallback {
		// The same bounds parseOAuthInput applies still hold: a pasted callback is
		// operator input and must not be unbounded or carry control characters.
		raw := strings.TrimSpace(body.Input)
		if raw == "" || len(raw) > oauthMaxInput || strings.IndexFunc(raw, unicode.IsControl) >= 0 {
			writeError(w, http.StatusBadRequest, "oauth_code_missing", "no callback was found in what was pasted; check it and try again")
			return
		}
		code, denied, pastedState = raw, false, ""
	}
	if !denied && code == "" {
		// Nothing usable was pasted. Refuse before consuming so the operator can
		// paste again instead of restarting the single-use attempt.
		writeError(w, http.StatusBadRequest, "oauth_code_missing", "no authorization code was found in what was pasted; check it and try again")
		return
	}
	if pastedState != "" && subtle.ConstantTimeCompare([]byte(pastedState), []byte(body.State)) != 1 {
		// Output from a different attempt: refuse without consuming this one. It has
		// its own code because this attempt is still open; answering
		// oauth_state_invalid told the dashboard the attempt was gone. Both states are
		// the caller's own, so the distinction reveals nothing.
		writeError(w, http.StatusBadRequest, "oauth_state_mismatch", "this address is from a different sign-in attempt; paste the one from this attempt")
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
	// Resolve the redirect only for flows that send one. Zed's native-app
	// handshake and device flows never do, and requiring a configured origin for
	// them refused a sign-in that would have worked.
	redirectURI, ok := h.flowRedirectURI(w, provider.flow)
	if !ok {
		return
	}
	result, err := provider.flow.Exchange(r.Context(), onboarding.Params{
		RedirectURI: redirectURI,
		State:       body.State,
		Verifier:    flow.verifier,
		Input:       code,
		Meta:        mergeMeta(flow.meta, flow.extra),
		Client:      h.opts.OAuthClient,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, "oauth_exchange_failed", "the provider did not accept the authorization code; start again")
		return
	}
	if saved, _, ok := h.saveOnboardedConnection(w, r, providerID, flow, result); ok {
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
		// Meta carries operator-supplied provider parameters (GitLab's base URL and
		// client id, Kiro's region and start URL).
		Meta map[string]string `json:"meta"`
	}
	if !decodeJSON(w, r, 8<<10, &body) {
		return
	}
	name, identity, ok := onboardingAccount(w, provider.flow, body.Name, body.Identity)
	if !ok {
		return
	}
	session, ok := h.sessionFor(r)
	if !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "admin session is required")
		return
	}
	// A device flow never sends the operator to a redirect the provider registered:
	// it shows a code and polls. Requiring a public origin here would break device
	// sign-in for installs that never configured one.
	redirectURI, ok := h.flowRedirectURI(w, provider.flow)
	if !ok {
		return
	}
	state, ok := newState()
	if !ok {
		writeError(w, http.StatusInternalServerError, "oauth_start_failed", "could not start sign-in")
		return
	}
	device, err := provider.flow.Authorize(r.Context(), onboarding.Params{
		State:       state,
		RedirectURI: redirectURI,
		Meta:        body.Meta,
		Client:      h.opts.OAuthClient,
	})
	if errors.Is(err, onboarding.ErrRateLimited) {
		writeError(w, http.StatusTooManyRequests, "oauth_rate_limited", "the provider has too many pending sign-ins; wait a few minutes and try again")
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "oauth_start_failed", "the provider did not start a sign-in; try again shortly")
		return
	}
	expiresAt, ok := h.oauth.begin(state, oauthFlow{
		sessionID: session.ID, provider: providerID,
		deviceCode: device.DeviceCode, verifier: device.Verifier,
		extra: device.Extra, meta: body.Meta, name: name, identity: identity,
		identityOptional: provider.flow.IdentityOptional, ttl: device.ExpiresIn, nextPoll: h.oauth.now().Add(device.Interval),
	})
	if !ok {
		writeError(w, http.StatusServiceUnavailable, "oauth_busy", "too many sign-ins in progress; try again shortly")
		return
	}
	// The device handle stays server-side: the browser holds only the opaque
	// state, and the poll redeems it with the handle this attempt captured. The
	// operator needs the user code, not the handle.
	writeJSON(w, http.StatusOK, map[string]any{
		"state":           state,
		"flowType":        provider.flow.Type,
		"userCode":        device.UserCode,
		"verificationUrl": device.VerificationURL,
		"intervalSeconds": int(device.Interval / time.Second),
		"expiresAt":       expiresAt,
	})
}

// handleOAuthCancel ends a pending browser or device attempt so its state can no
// longer complete. Without it a dashboard "Cancel" only hid the form while the
// attempt stayed redeemable for the rest of its lifetime. Unknown, expired,
// replayed and foreign states answer oauth_state_invalid, as everywhere else.
func (h *Handler) handleOAuthCancel(w http.ResponseWriter, r *http.Request) {
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
	switch h.oauth.cancel(body.State, session.ID, r.PathValue("provider")) {
	case reserveTaken:
		writeJSON(w, http.StatusOK, map[string]any{"status": "cancelled"})
	case reserveBusy:
		writeError(w, http.StatusConflict, "oauth_completing", "the sign-in was approved and is being saved")
	default:
		writeError(w, http.StatusBadRequest, "oauth_state_invalid", "this sign-in attempt expired or was already used; start again")
	}
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
	outcome, err := provider.flow.Poll(r.Context(), onboarding.Params{
		State:    body.State,
		Verifier: flow.verifier,
		Input:    flow.deviceCode,
		Meta:     mergeMeta(flow.meta, flow.extra),
		Client:   h.opts.OAuthClient,
	})
	if err != nil {
		writeError(w, http.StatusBadGateway, "oauth_poll_failed", "the provider could not be reached; the sign-in is still open")
		return
	}
	// A terminal failure that is not a denial is the provider failing, not the
	// operator refusing. Collapsing the two would tell an operator "you said no"
	// when the provider errored.
	status := "pending"
	switch {
	case outcome.Expired:
		status = "expired"
	case outcome.Denied:
		status = "denied"
	case outcome.Err != nil:
		status = "failed"
	case outcome.Done:
		status = "approved"
	}
	switch status {
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
		saved, persisted, ok := h.saveOnboardedConnection(w, r, providerID, flow, outcome.Result)
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
	case "failed":
		// The attempt is burned: the provider rejected this authorization, so
		// polling again cannot help. This needs its own code because the dashboard
		// retries oauth_poll_failed, which is the retryable transport case above;
		// reusing it turned a terminal provider rejection into a stale
		// oauth_state_invalid message after five pointless retries.
		h.oauth.consume(body.State, session.ID, providerID)
		writeError(w, http.StatusBadGateway, "oauth_sign_in_rejected", "the provider rejected the sign-in; start again")
	default:
		writeJSON(w, http.StatusOK, map[string]any{"status": "pending"})
	}
}

// saveOAuthConnection persists a completed sign-in as a new enabled connection,
// last in routing order, and answers the request. Shared by both flows.
func (h *Handler) saveOnboardedConnection(w http.ResponseWriter, r *http.Request, providerID string, flow oauthFlow, result onboarding.Result) (saved map[string]any, persisted, ok bool) {
	saved, persisted, ok = h.saveOnboardedResult(w, r, providerID, flow, result)
	// Work the provider starts once the account is connected (Antigravity's Code
	// Assist onboarding) runs after the answer, as the reference's does not hold
	// up its save; the request ending does not cancel it.
	if ok && result.AfterSave != nil {
		go result.AfterSave(context.WithoutCancel(r.Context()))
	}
	return saved, persisted, ok
}

// saveOnboardedResult stores one sign-in result and reports what the save did.
func (h *Handler) saveOnboardedResult(w http.ResponseWriter, r *http.Request, providerID string, flow oauthFlow, result onboarding.Result) (saved map[string]any, persisted, ok bool) {
	if result.Secret.Empty() {
		writeError(w, http.StatusBadGateway, "oauth_exchange_failed", "the provider returned no usable credential; start again")
		return nil, persisted, false
	}
	// A provider may report its own identity and label (the reference stores both);
	// the operator-supplied values are the fallback so dedup always has a key.
	name, identity := strings.TrimSpace(result.DisplayName), strings.TrimSpace(result.Identity)
	if name == "" {
		name = flow.name
	}
	if identity == "" {
		identity = flow.identity
	}
	// Nothing to dedup on: refuse before saving, unless the flow stores such a
	// credential without an identity as the reference does (Codex).
	if identity == "" && !flow.identityOptional {
		writeError(w, http.StatusBadGateway, "oauth_identity_unresolved", "the provider did not report which account signed in; start again and enter its identity")
		return nil, persisted, false
	}
	if name == "" {
		name = identity
	}
	if name == "" {
		name = result.FallbackName
	}
	authKind := result.AuthKind
	if authKind == "" {
		authKind = credentials.AuthOAuth
	}
	// A flow may report its own mode (iflow's cookie import yields a cookie
	// connection), so the result is checked against the provider's declared modes
	// exactly as the manual form is. Without this, /import could store a connection
	// the PATCH path would refuse.
	spec, _ := h.providerSpec(providerID)
	if !authKindPermitted(spec, authKind) {
		writeError(w, http.StatusBadRequest, "invalid_auth_kind", "this provider does not accept the credential mode this sign-in produced")
		return nil, persisted, false
	}
	if providerID == codexProviderID {
		h.connectionMu.Lock()
		defer h.connectionMu.Unlock()
	}
	if flow.target != "" {
		return h.saveReauthenticated(w, r, providerID, flow.target, identity, result)
	}
	if providerID == codexProviderID && result.Secret.AuthType != credentials.AuthTypeAccessToken {
		match, found, err := h.codexMatch(r.Context(), identity, result.Secret.ProviderData)
		if err != nil {
			writeError(w, http.StatusBadRequest, "connection_save_failed", "provider connection could not be saved; check its identity")
			return nil, persisted, false
		}
		if found {
			// The matched row keeps its id, name, priority, enabled flag, proxy pool and
			// identity: a sign-in sends none of them (codex.js:48-66,
			// [provider]/[action]/route.js:471-479). It takes the new credential whole:
			// a reply without a refresh token or expires_in clears the stored ones.
			match.Secret = result.Secret
			connection, persisted, err := h.publishMergedConnection(r.Context(), match, func(existing, update credentials.Secret) credentials.Secret {
				return codexMergedSecret(existing, update, onboarding.CarriesAll)
			})
			if err != nil {
				if persisted {
					writeError(w, http.StatusInternalServerError, "connection_publish_failed", "connection saved but could not be published to the runtime")
				} else {
					writeError(w, http.StatusBadRequest, "connection_save_failed", "provider connection could not be saved; check its identity")
				}
				return nil, persisted, false
			}
			h.clearConnectionHealth(connection.ID)
			return onboardedJSON(connection, onboardedUpdated), true, true
		}
	}
	if name == "" {
		label, err := h.accountLabel(r.Context(), providerID)
		if err != nil {
			writeError(w, http.StatusBadRequest, "connection_save_failed", "provider connection could not be saved; check its identity")
			return nil, persisted, false
		}
		name = label
	}
	flow.name, flow.identity = name, identity
	connection, persisted, err := h.publishConnection(r.Context(), credentials.Connection{
		ProviderID: providerID, Name: flow.name, AuthKind: authKind, Identity: flow.identity,
		Secret: result.Secret, Enabled: true,
	})
	if err != nil {
		if persisted {
			writeError(w, http.StatusInternalServerError, "connection_publish_failed", "connection saved but could not be published to the runtime")
		} else {
			writeError(w, http.StatusBadRequest, "connection_save_failed", "provider connection could not be saved; check its identity")
		}
		return nil, persisted, false
	}
	return onboardedJSON(connection, onboardedAdded), true, true
}

// What a sign-in or import did, reported with the saved connection so the
// operator sees whether an account was added or an existing one renewed (#215).
const (
	// onboardedAdded: no stored connection was this account; a new one was made.
	onboardedAdded = "added"
	// onboardedUpdated: the account was already connected (the reference's
	// duplicate rule, codexMatch); that row took the new credential in place.
	onboardedUpdated = "updated"
	// onboardedReauthenticated: the operator renewed this connection on purpose.
	onboardedReauthenticated = "reauthenticated"
)

// onboardedJSON is the saved connection plus what the save did. Codex adds the
// ChatGPT plan the sign-in reported (chatgptPlanType, not a credential), so the
// account's plan is visible before any quota read.
func onboardedJSON(connection credentials.Connection, outcome string) map[string]any {
	saved := connectionJSON(connection)
	saved["outcome"] = outcome
	// An import may carry a credential that had already expired (#218).
	if expired, refreshable := importExpiry(connection.Secret, time.Now()); expired {
		saved["expired"], saved["refreshable"] = true, refreshable
	}
	if plan := connection.Secret.ProviderData["chatgptPlanType"]; plan != "" && connection.ProviderID == codexProviderID {
		saved["plan"] = plan
	}
	// A Code Assist provider is unusable without the project its sign-in
	// discovers (wire/cloudcode.go refuses a request without one), so the result
	// names it, or says none was found, rather than leaving that to the first
	// failed request (#216). The project id is a label, not a credential.
	if cloudCodeProviders[connection.ProviderID] {
		project := strings.TrimSpace(connection.Secret.ProviderData["projectId"])
		saved["project"] = project
		saved["projectMissing"] = project == ""
	}
	return saved
}

// cloudCodeProviders sign in with Google and serve through a Code Assist project.
var cloudCodeProviders = map[string]bool{"gemini-cli": true, "antigravity": true}

// providerConnection finds one of a provider's connections by id. A connection
// of another provider reads as absent, so a Reauthenticate cannot be pointed at it.
func (h *Handler) providerConnection(ctx context.Context, providerID, id string) (credentials.Connection, bool, error) {
	connections, err := h.opts.Credentials.ListConnections(ctx, providerID)
	if err != nil {
		return credentials.Connection{}, false, err
	}
	for _, connection := range connections {
		if connection.ID == id {
			return connection, true, nil
		}
	}
	return credentials.Connection{}, false, nil
}

// saveReauthenticated renews the target connection's credential with a sign-in
// result, and only when the result is that connection's account (sameAccount).
// Another account is refused and nothing is saved, so a Reauthenticate never
// adds a row or overwrites a different one. Name, priority, enabled state, proxy
// and identity are kept, as for a duplicate update.
func (h *Handler) saveReauthenticated(w http.ResponseWriter, r *http.Request, providerID, targetID, identity string, result onboarding.Result) (map[string]any, bool, bool) {
	target, found, err := h.providerConnection(r.Context(), providerID, targetID)
	if err != nil {
		writeError(w, http.StatusBadRequest, "connection_save_failed", "provider connection could not be saved; check its identity")
		return nil, false, false
	}
	if !found {
		writeError(w, http.StatusConflict, "connection_not_found", "the connection being reauthenticated no longer exists; nothing was saved")
		return nil, false, false
	}
	merge := mergeConnectionSecret
	if providerID == codexProviderID {
		merge = func(existing, update credentials.Secret) credentials.Secret {
			return codexMergedSecret(existing, update, onboarding.CarriesAll)
		}
	}
	if !sameAccount(providerID, target, identity, result) {
		writeError(w, http.StatusConflict, "oauth_account_mismatch", "a different account signed in; nothing was saved. Sign in as the account this connection belongs to, or use Add connection for a new one")
		return nil, false, false
	}
	target.Secret = result.Secret
	connection, persisted, err := h.publishMergedConnection(r.Context(), target, merge)
	if err != nil {
		if persisted {
			writeError(w, http.StatusInternalServerError, "connection_publish_failed", "connection saved but could not be published to the runtime")
		} else {
			writeError(w, http.StatusBadRequest, "connection_save_failed", "provider connection could not be saved; check its identity")
		}
		return nil, persisted, false
	}
	h.clearConnectionHealth(connection.ID)
	return onboardedJSON(connection, onboardedReauthenticated), true, true
}

// sameAccount reports whether a sign-in result is the target connection's
// account. identity is the save's identity, which falls back to the target's own
// when the provider reports none: such a provider cannot tell accounts apart, so
// the operator's choice of connection stands. Codex reports its account, so only
// what it reported counts. A ChatGPT account id on either side must be on both
// and equal (the reference keeps a different workspace distinct,
// connectionsRepo.js:244-247), and an email on both must be equal; with neither
// an account id nor two emails there is nothing to confirm. A bare access token
// (codexTokenOnly) is never taken as a Reauthenticate: it would turn the row into
// one that can no longer refresh or be matched.
func sameAccount(providerID string, target credentials.Connection, identity string, result onboarding.Result) bool {
	if providerID != codexProviderID {
		return identity != "" && identity == target.Identity
	}
	if result.Secret.AuthType == credentials.AuthTypeAccessToken {
		return false
	}
	reported := strings.TrimSpace(result.Identity)
	account, stored := result.Secret.ProviderData["chatgptAccountId"], target.Secret.ProviderData["chatgptAccountId"]
	if account != "" || stored != "" {
		if account != stored {
			return false
		}
		return reported == "" || target.Identity == "" || reported == target.Identity
	}
	return reported != "" && reported == target.Identity
}

// parseOAuthInput accepts what the operator can copy: a bare code, Claude's
// "code#state", or the whole redirect URL (or its query) from Codex, carrying a
// code or, for Kimchi, a token. It returns
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
		// A browser-token provider returns the credential as token=, which the
		// reference reads wherever code is absent (OAuthModal.js:713-733).
		code := query.Get("code")
		if code == "" {
			code = query.Get("token")
		}
		return cleanCode(code), false, query.Get("state")
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

// importFlowFor resolves the flow that implements a provider's import mode. It
// merges the two tables rather than preferring one: an import flow is a distinct
// auth mode, not an alternative to the browser flow.
//
// The manual table is checked first because that is where the import-only modes
// live (codex, grok-cli, iflow, kiro, xiaomi-mimo). A provider can be in both
// tables with different halves: codex's token import is manual while its bulk
// importer belongs to the browser flow. Returning on the first table hit left
// Bulk nil and made a documented endpoint answer 404, so the halves are merged.
func (h *Handler) importFlowFor(providerID string) (onboarding.Flow, bool) {
	var merged onboarding.Flow
	found := false
	if manual, ok := h.manualFlows[providerID]; ok && manual.Import != nil {
		merged = manual
		found = true
	}
	if flow, ok := h.oauthFlows.Lookup(providerID); ok {
		switch {
		case !found && flow.Import != nil:
			merged = flow
			found = true
		case found && merged.Bulk == nil && flow.Bulk != nil:
			// Only the bulk hook is taken: the manual entry owns Import, and the two
			// halves must not shadow each other.
			merged.Bulk = flow.Bulk
		}
	}
	return merged, found
}

// handleOAuthImport completes a manual-token, cookie, PAT or API-key connection.
// These flows have no browser step, so the operator supplies the credential and
// the flow validates it before it is stored.
func (h *Handler) handleOAuthImport(w http.ResponseWriter, r *http.Request) {
	if h.opts.Credentials == nil {
		writeError(w, http.StatusServiceUnavailable, "credentials_unavailable", "configure ROUTEWEFT_CREDENTIAL_KEY to manage provider connections")
		return
	}
	providerID := r.PathValue("provider")
	// A provider can offer both a browser flow and an import mode (codex,
	// grok-cli, iflow, kiro, xiaomi-mimo, cursor). importFlowFor consults both
	// tables, so a provider registered as a browser flow still resolves its import
	// mode.
	flow, ok := h.importFlowFor(providerID)
	spec, inCatalog := h.providerSpec(providerID)
	if !ok || !inCatalog || !specAllowsOAuth(spec) {
		writeError(w, http.StatusNotFound, "oauth_unsupported", "this provider has no import flow")
		return
	}
	var body struct {
		Name     string            `json:"name"`
		Identity string            `json:"identity"`
		Input    string            `json:"input"`
		Meta     map[string]string `json:"meta"`
	}
	// The input plus its JSON escaping (a pasted file's quotes and newlines), the
	// name, identity and meta.
	if !decodeJSON(w, r, 3*oauthMaxImportInput, &body) {
		return
	}
	// Same rule as /start and /device.
	name, identity, ok := onboardingAccount(w, flow, body.Name, body.Identity)
	if !ok {
		return
	}
	// An import payload becomes a header value or a form body, so a control
	// character must not reach it. A JSON document (an exported file, pasted or
	// chosen) is parsed by its flow, never sent as is, so its line breaks and tabs
	// are allowed; nothing else is.
	switch {
	case strings.TrimSpace(body.Input) == "":
		writeError(w, http.StatusBadRequest, "oauth_code_missing", "a credential to import is required")
		return
	case len(body.Input) > oauthMaxImportInput:
		writeError(w, http.StatusRequestEntityTooLarge, "import_too_large", fmt.Sprintf("the credential is larger than %d KiB; import only the account's own token or file", oauthMaxImportInput>>10))
		return
	case !importInputClean(body.Input):
		writeError(w, http.StatusBadRequest, "import_invalid", "the credential contains control characters; paste it exactly as it was issued")
		return
	}
	if _, ok := h.sessionFor(r); !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "admin session is required")
		return
	}
	result, err := flow.Import(r.Context(), onboarding.Params{
		Input:  body.Input,
		Meta:   body.Meta,
		Client: h.opts.OAuthClient,
	})
	// A missing field the operator must supply is a request problem with its own
	// code, not an upstream rejection.
	if errors.Is(err, onboarding.ErrCursorMachineIDRequired) {
		writeError(w, http.StatusBadRequest, "oauth_meta_required", err.Error())
		return
	}
	// A malformed paste or file is the operator's to correct, and its message is
	// fixed text; anything else is the provider's answer, reported without detail
	// (an invalid, expired or revoked credential).
	var inputErr *onboarding.InputError
	if errors.As(err, &inputErr) {
		writeError(w, http.StatusBadRequest, "import_invalid", inputErr.Error())
		return
	}
	if err != nil {
		writeError(w, http.StatusBadGateway, "oauth_exchange_failed", "the provider did not accept the credential; it may be invalid, expired or revoked")
		return
	}
	// The same account imported again updates its connection, as the reference's
	// insert does (importMatch). Codex keeps its own rule in saveOnboardedResult.
	if providerID != codexProviderID {
		h.connectionMu.Lock()
		defer h.connectionMu.Unlock()
		if h.saveImportedDuplicate(w, r, providerID, firstNonEmptyTrimmed(result.Identity, identity), result) {
			return
		}
	}
	flowState := oauthFlow{name: name, identity: identity, identityOptional: flow.IdentityOptional}
	if saved, _, ok := h.saveOnboardedConnection(w, r, providerID, flowState, result); ok {
		writeJSON(w, http.StatusOK, saved)
	}
}

// importInputClean reports whether an import payload is free of control
// characters, allowing a JSON document's line breaks and tabs.
func importInputClean(input string) bool {
	document := strings.HasPrefix(strings.TrimSpace(input), "{") || strings.HasPrefix(strings.TrimSpace(input), "[")
	return strings.IndexFunc(input, func(r rune) bool {
		if document && (r == '\n' || r == '\r' || r == '\t') {
			return false
		}
		return unicode.IsControl(r)
	}) < 0
}

// firstNonEmptyTrimmed is the first value that is not blank, trimmed.
func firstNonEmptyTrimmed(values ...string) string {
	for _, value := range values {
		if trimmed := strings.TrimSpace(value); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

// newVerifier mints one PKCE verifier.
func newVerifier() (string, error) {
	raw := make([]byte, oauthStateEntropy)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

// dashboardOrigin returns the configured operator-facing origin, which is the
// callback host for providers without a pinned loopback redirect.
//
// It deliberately does not derive the origin from the request: the value becomes
// a provider redirect_uri, so trusting Host or X-Forwarded-Proto would let a
// caller steer a sign-in callback to an origin of their choosing. An unset
// origin fails the sign-in instead.
func (h *Handler) dashboardOrigin() string {
	return strings.TrimSpace(h.opts.PublicOrigin)
}

// mergeMeta combines the operator input captured at start with the flow-private
// state the flow itself minted. The flow's own values win on a key collision,
// because they are the ones it must be able to trust (a private key it generated,
// a nonce it issued) and the operator cannot usefully override them.
func mergeMeta(operator, private map[string]string) map[string]string {
	if len(operator) == 0 && len(private) == 0 {
		return nil
	}
	merged := make(map[string]string, len(operator)+len(private))
	for key, value := range operator {
		merged[key] = value
	}
	for key, value := range private {
		merged[key] = value
	}
	return merged
}

// flowRedirectURI resolves the redirect a flow sends, and answers the request when
// it cannot. A flow that does not consume one (pinned or unused) gets an empty
// value rather than a 503: requiring an origin for a value the flow discards is
// what made claude, codex, xai, zed and every device provider fail to start on an
// install that never configured one.
func (h *Handler) flowRedirectURI(w http.ResponseWriter, flow onboarding.Flow) (string, bool) {
	redirectURI, err := onboarding.DefaultRedirectURI(flow, h.dashboardOrigin())
	if err != nil && !flow.NeedsRedirectURI() {
		return "", true
	}
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "oauth_redirect_unconfigured", "set the public origin before starting a sign-in")
		return "", false
	}
	return redirectURI, true
}

// handleOAuthBulkImport imports several account JSON objects in one call, mirroring
// the reference's /api/oauth/{codex,grok-cli}/bulk-import. The body accepts an
// array, a single object, or {accounts: [...]} because operators paste whatever
// their exporter produced.
//
// Items are processed serially: PutConnection reads and reorders within a
// transaction, so concurrent imports would race on priority assignment.
//
// No credential is echoed back. The response carries an index, a status and the
// saved connection id per item, so a partially failed batch still reports which
// entries landed.
func (h *Handler) handleOAuthBulkImport(w http.ResponseWriter, r *http.Request) {
	if h.opts.Credentials == nil {
		writeError(w, http.StatusServiceUnavailable, "credentials_unavailable", "configure ROUTEWEFT_CREDENTIAL_KEY to manage provider connections")
		return
	}
	providerID := r.PathValue("provider")
	flow, ok := h.importFlowFor(providerID)
	spec, inCatalog := h.providerSpec(providerID)
	if !ok || !inCatalog || !specAllowsOAuth(spec) || flow.Bulk == nil {
		writeError(w, http.StatusNotFound, "oauth_unsupported", "this provider has no bulk import flow")
		return
	}
	if _, ok := h.sessionFor(r); !ok {
		writeError(w, http.StatusUnauthorized, "unauthenticated", "admin session is required")
		return
	}
	raw, ok := decodeBulkBody(w, r)
	if !ok {
		return
	}
	type itemResult struct {
		Index int    `json:"index"`
		OK    bool   `json:"ok"`
		ID    string `json:"id,omitempty"`
		// Outcome is onboardedAdded or onboardedUpdated (a duplicate of a stored
		// account took the item in place), so a batch says which rows are new.
		Outcome string `json:"outcome,omitempty"`
		// Expired marks an item stored with a credential that had already expired
		// (#218); Refreshable says whether a refresh token came with it.
		Expired     bool   `json:"expired,omitempty"`
		Refreshable *bool  `json:"refreshable,omitempty"`
		Error       string `json:"error,omitempty"`
	}
	results := make([]itemResult, 0, len(raw))
	saved, failed := 0, 0
	for index, item := range raw {
		if item == nil {
			// The reference fails the item, not the paste (bulk-import/route.js:62-64).
			results = append(results, itemResult{Index: index, Error: "item is not an object"})
			failed++
			continue
		}
		account, err := flow.Bulk(item)
		if err != nil {
			results = append(results, itemResult{Index: index, Error: err.Error()})
			failed++
			continue
		}
		if account.Identity == "" && !flow.IdentityOptional {
			results = append(results, itemResult{Index: index, Error: "account has no email or identity claim"})
			failed++
			continue
		}
		if !authKindPermitted(spec, account.AuthKind) {
			results = append(results, itemResult{Index: index, Error: "credential mode is not accepted by this provider"})
			failed++
			continue
		}
		// The label falls back to the identity, and then to the reference's
		// "Account N", as it does for a bulk item that carries no display name.
		name := account.Name
		if name == "" {
			name = account.Identity
		}
		if name == "" {
			label, err := h.accountLabel(r.Context(), providerID)
			if err != nil {
				results = append(results, itemResult{Index: index, Error: bulkSaveError})
				failed++
				continue
			}
			name = label
		}
		if len(name) > oauthMaxNameLength || len(account.Identity) > oauthMaxNameLength {
			results = append(results, itemResult{Index: index, Error: "account name or identity is too long"})
			failed++
			continue
		}
		// Stored and registered per item, but the snapshot is recompiled once after
		// the loop: each refresh recompiles every provider, so doing it per item made
		// one paste cost thousands of recompiles.
		connection, updated, err := h.storeImportedConnection(r.Context(), providerID, name, account)
		if err != nil {
			// The store's own text names tables and constraints; the item gets a
			// fixed reason instead (#218).
			message := bulkSaveError
			if errors.Is(err, errImportConflict) {
				message = importConflictMessage
			}
			results = append(results, itemResult{Index: index, Error: message})
			failed++
			continue
		}
		outcome := onboardedAdded
		if updated {
			outcome = onboardedUpdated
		}
		item := itemResult{Index: index, OK: true, ID: connection.ID, Outcome: outcome}
		if expired, refreshable := importExpiry(account.Secret, time.Now()); expired {
			item.Expired, item.Refreshable = true, &refreshable
		}
		results = append(results, item)
		saved++
	}
	if saved > 0 {
		if err := h.publishBindings(r.Context()); err != nil {
			writeError(w, http.StatusInternalServerError, "connection_publish_failed", "connections were saved but the runtime could not be updated")
			return
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"total": len(raw), "success": saved, "failed": failed, "results": results})
}

// bulkObjects decodes each element as an account object, nil where it is not one.
func bulkObjects(elements []json.RawMessage) []map[string]any {
	items := make([]map[string]any, len(elements))
	for index, element := range elements {
		var object map[string]any
		if json.Unmarshal(element, &object) == nil {
			items[index] = object
		}
	}
	return items
}

// bulkSaveError is a bulk item's reason when the store refused it for any
// reason other than a duplicate.
const bulkSaveError = "the account could not be saved"

// decodeBulkBody normalizes the three accepted shapes into a slice of objects.
// An array element that is not an object is nil, so it fails alone.
func decodeBulkBody(w http.ResponseWriter, r *http.Request) ([]map[string]any, bool) {
	var body json.RawMessage
	if !decodeJSON(w, r, 1<<20, &body) {
		return nil, false
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "a JSON body is required")
		return nil, false
	}
	var items []map[string]any
	switch trimmed[0] {
	case '[':
		var elements []json.RawMessage
		if err := json.Unmarshal(trimmed, &elements); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the body must be a JSON array of account objects")
			return nil, false
		}
		items = bulkObjects(elements)
	case '{':
		var wrapped struct {
			Accounts json.RawMessage `json:"accounts"`
		}
		if err := json.Unmarshal(trimmed, &wrapped); err != nil {
			writeError(w, http.StatusBadRequest, "invalid_request", "the body must be a JSON account object")
			return nil, false
		}
		var elements []json.RawMessage
		// Only an array under accounts wraps a batch; anything else there is one
		// account object (the reference's Array.isArray(body.accounts)).
		if json.Unmarshal(wrapped.Accounts, &elements) == nil && elements != nil {
			items = bulkObjects(elements)
		} else {
			var single map[string]any
			if err := json.Unmarshal(trimmed, &single); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_request", "the body must be a JSON account object")
				return nil, false
			}
			items = []map[string]any{single}
		}
	default:
		writeError(w, http.StatusBadRequest, "invalid_request", "the body must be a JSON array or object")
		return nil, false
	}
	if len(items) == 0 {
		writeError(w, http.StatusBadRequest, "invalid_request", "no accounts provided")
		return nil, false
	}
	if len(items) > oauthMaxBulkItems {
		writeError(w, http.StatusRequestEntityTooLarge, "bulk_too_large", fmt.Sprintf("at most %d accounts may be imported at once", oauthMaxBulkItems))
		return nil, false
	}
	return items, true
}

// publishConnection is the single way a connection becomes durable and live: the
// store write, the runtime registry registration and the proxy-binding refresh
// happen together or not at all. Every path that creates a connection (PATCH, a
// single import, a bulk import) goes through it.
//
// Bulk import previously wrote the row without registering it, so the connection
// existed in SQLite while inference answered credentials_unavailable until some
// later quota refresh happened to load it. A second write path is how that
// happens; there is now one.
// persisted reports whether the row reached durable storage, which is what decides
// whether a sign-in attempt may be closed: a publish failure after the write must
// not discard a token the provider already issued.
func (h *Handler) publishConnection(ctx context.Context, connection credentials.Connection) (stored credentials.Connection, persisted bool, err error) {
	return h.publishMergedConnection(ctx, connection, mergeConnectionSecret)
}

// publishMergedConnection is publishConnection with the rule that combines an
// existing connection's stored secret with the update's: a Codex duplicate
// replaces its providerSpecificData whole (codexMergedSecret), every other edit
// merges it key by key (mergeConnectionSecret).
func (h *Handler) publishMergedConnection(ctx context.Context, connection credentials.Connection, merge func(existing, update credentials.Secret) credentials.Secret) (stored credentials.Connection, persisted bool, err error) {
	// A caller that already resolved its node keeps it: a Generic Provider node has
	// no built-in spec to look up, and a second resolution could disagree with the
	// first. Onboarding callers pass only the provider and get the built-in node.
	node := credentials.Node{ID: connection.NodeID, ProviderID: connection.ProviderID}
	if connection.NodeID == "" || connection.ProviderID == "" {
		resolved, err := h.builtinNode(ctx, connection.ProviderID)
		if err != nil {
			return credentials.Connection{}, false, err
		}
		node = resolved
	}
	connection.NodeID = node.ID
	if connection.ID == "" {
		// New connections go last in routing order, as the manual form does.
		existing, err := h.opts.Credentials.ListConnections(ctx, node.ProviderID)
		if err != nil {
			return credentials.Connection{}, false, err
		}
		connection.Priority = len(existing)
	}
	if connection.AuthKind == "" {
		connection.AuthKind = credentials.AuthOAuth
	}
	if connection.ID != "" {
		// An edit merges onto the credential as stored when it is written, never an
		// earlier read a refresh may since have rotated (updateConnection).
		stored, err = h.updateConnection(ctx, connection.ID, func(existing credentials.Connection) (credentials.Connection, error) {
			update := connection
			update.Secret = merge(existing.Secret, connection.Secret)
			return update, nil
		})
	} else {
		stored, err = h.opts.Credentials.PutConnection(ctx, connection)
		if err == nil {
			err = h.register(ctx, stored)
		}
	}
	if err != nil {
		return credentials.Connection{}, false, err
	}
	// PutConnection returns the metadata as given; the registry's exchanger lookup
	// keys on the provider.
	stored.ProviderID = node.ProviderID
	if h.opts.PoolBindings != nil {
		if _, err := h.opts.PoolBindings.RefreshPoolBindings(ctx); err != nil {
			return stored, true, err
		}
	}
	return stored, true, nil
}

// storeImportedConnection writes one imported credential and registers it with the
// runtime, without recompiling the snapshot. Bulk import calls it per item and
// publishes once; a single import goes through publishConnection, which does both.
// updated reports that a stored duplicate took the item instead of a new row.
func (h *Handler) storeImportedConnection(ctx context.Context, providerID, name string, account onboarding.BulkAccount) (_ credentials.Connection, updated bool, _ error) {
	// Held from before the node is resolved: builtinNode creates the node on
	// first use, and two first imports racing there would each create one. It
	// also keeps a concurrent import of the same account from inserting between
	// the duplicate lookup and the write.
	h.connectionMu.Lock()
	defer h.connectionMu.Unlock()
	node, err := h.builtinNode(ctx, providerID)
	if err != nil {
		return credentials.Connection{}, false, err
	}
	if providerID != codexProviderID {
		// The reference's insert updates the stored account (importMatch). The
		// bulk route sends neither a name, a priority nor isActive
		// (grok-cli/bulk-import/route.js:90-99), so those stay as stored.
		match, found, err := h.importMatch(ctx, providerID, account.Identity, account.AuthKind, account.Secret.ProviderData)
		if err != nil {
			return credentials.Connection{}, false, err
		}
		if found {
			stored, err := h.updateConnection(ctx, match.ID, func(existing credentials.Connection) (credentials.Connection, error) {
				existing.Secret = importedSecret(providerID, existing.Secret, account.Secret)
				return existing, nil
			})
			if err != nil {
				return credentials.Connection{}, false, err
			}
			stored.ProviderID = node.ProviderID
			h.clearConnectionHealth(stored.ID)
			return stored, true, nil
		}
	}
	if providerID == codexProviderID {
		match, found, err := h.codexMatch(ctx, account.Identity, account.Secret.ProviderData)
		if err != nil {
			return credentials.Connection{}, false, err
		}
		if found {
			// {...existing, ...item} (connectionsRepo.js:283): the keys of the
			// credential the item carries replace the stored ones, its own name
			// replaces the label, its priority the row's, and its isActive, true
			// unless it says false (bulk-import/route.js:103), sets enabled. The id,
			// proxy pool and identity are kept. The merge is onto the credential as
			// stored when it is written, so a refresh's rotated token is kept when
			// the item carries none (updateConnection).
			priority := account.Priority
			if account.HasPriority && priority == 0 {
				// The reference sorts a zero or null priority as 999, after the rest
				// of the pool (connectionsRepo.js:159).
				if priority, err = h.codexLastPriority(ctx, match.ID); err != nil {
					return credentials.Connection{}, false, err
				}
			}
			stored, err := h.updateConnection(ctx, match.ID, func(existing credentials.Connection) (credentials.Connection, error) {
				existing.Secret = codexMergedSecret(existing.Secret, account.Secret, account.Carries)
				if account.ItemName != "" {
					existing.Name = account.ItemName
				}
				if account.HasPriority {
					existing.Priority = priority
				}
				existing.Enabled = !account.Disabled
				return existing, nil
			})
			if err != nil {
				return credentials.Connection{}, false, err
			}
			stored.ProviderID = node.ProviderID
			h.clearConnectionHealth(stored.ID)
			return stored, true, nil
		}
	}
	existing, err := h.opts.Credentials.ListConnections(ctx, providerID)
	if err != nil {
		return credentials.Connection{}, false, err
	}
	priority := len(existing)
	if account.Priority != 0 {
		// An item's own non-zero priority places it (connectionsRepo.js:293-294).
		priority = account.Priority
	}
	stored, err := h.opts.Credentials.PutConnection(ctx, credentials.Connection{
		NodeID: node.ID, Name: name, Identity: account.Identity,
		AuthKind: account.AuthKind, Secret: account.Secret, Enabled: !account.Disabled,
		Priority: priority,
	})
	if err != nil {
		return credentials.Connection{}, false, err
	}
	stored.ProviderID = node.ProviderID
	if err := h.register(ctx, stored); err != nil {
		return credentials.Connection{}, false, err
	}
	return stored, false, nil
}

// accountLabel is the reference's label for a connection that names no account:
// "Account N", N being one more than the provider's connections
// (connectionsRepo.js:289-291).
func (h *Handler) accountLabel(ctx context.Context, providerID string) (string, error) {
	existing, err := h.opts.Credentials.ListConnections(ctx, providerID)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("Account %d", len(existing)+1), nil
}

// updateConnection changes one stored connection from its record as stored at
// the write, and publishes it to the runtime. With a credential registry, the read, change, durable write and publication are one
// step with refresh (Registry.Update), so a refresh can neither rotate the
// credential between the read and the write nor commit over the result.
// The registry seeds a connection it does not hold first (#252), so the store
// is read and written directly only without a registry, where no refresh runs.
func (h *Handler) updateConnection(ctx context.Context, id string, change func(credentials.Connection) (credentials.Connection, error)) (credentials.Connection, error) {
	if registry := h.opts.CredentialRegistry; registry != nil {
		stored, err := registry.Update(ctx, id, change)
		if !errors.Is(err, credentials.ErrNotFound) {
			return stored, err
		}
	}
	existing, err := h.opts.Credentials.GetConnection(ctx, id)
	if err != nil {
		return credentials.Connection{}, err
	}
	updated, err := change(existing)
	if err != nil {
		return credentials.Connection{}, err
	}
	stored, err := h.opts.Credentials.PutConnection(ctx, updated)
	if err != nil {
		return credentials.Connection{}, err
	}
	if err := h.register(ctx, stored); err != nil {
		return credentials.Connection{}, err
	}
	return stored, nil
}

// register publishes one stored connection to the runtime registry. It is separate
// from the store write so a batch can write many connections and publish the
// recompiled snapshot once. It seeds (Seed): a connection a Load seeded and a
// request refreshed meanwhile keeps that rotation (#252).
func (h *Handler) register(ctx context.Context, connection credentials.Connection) error {
	if h.opts.CredentialRegistry != nil {
		return h.opts.CredentialRegistry.Seed(ctx, connection)
	}
	return nil
}

// publishBindings refreshes the compiled snapshot after a batch. Every
// RefreshPoolBindings call recompiles the whole candidate snapshot, so a batch
// publishes once instead of once per connection.
func (h *Handler) publishBindings(ctx context.Context) error {
	if h.opts.PoolBindings == nil {
		return nil
	}
	_, err := h.opts.PoolBindings.RefreshPoolBindings(ctx)
	return err
}

// Nil provider data leaves all existing provider fields unchanged.
func mergeConnectionSecret(existing, update credentials.Secret) credentials.Secret {
	data := update.ProviderData
	if update.AuthType == "" {
		update.AuthType = existing.AuthType
	}
	if update.Empty() {
		// Same material rule as sealSecret: an ID token alone is a real update.
		idToken := update.IDToken
		update = existing
		if idToken != "" {
			update.IDToken = idToken
		}
	}
	if existing.ProviderData == nil && data == nil {
		update.ProviderData = nil
		return update
	}
	update.ProviderData = make(map[string]string, len(existing.ProviderData)+len(data))
	for key, value := range existing.ProviderData {
		update.ProviderData[key] = value
	}
	for key, value := range data {
		update.ProviderData[key] = value
	}
	return update
}
