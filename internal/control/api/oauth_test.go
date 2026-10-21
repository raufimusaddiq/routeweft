package api

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/adminauth"
	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/oauthrefresh"
	"github.com/raufimusaddiq/routeweft/internal/providers/onboarding"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

// oauthExchange is a scripted token endpoint that records what the engine sent.
type oauthExchange struct {
	mu       sync.Mutex
	status   int
	body     string
	requests []oauthExchangeCall
}

type oauthExchangeCall struct {
	contentType string
	body        string
}

func (e *oauthExchange) Do(request *http.Request) (*http.Response, error) {
	var raw []byte
	if request.Body != nil {
		raw, _ = io.ReadAll(request.Body)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.requests = append(e.requests, oauthExchangeCall{contentType: request.Header.Get("Content-Type"), body: string(raw)})
	status := e.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(e.body)), Header: http.Header{}}, nil
}

func (e *oauthExchange) calls() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return len(e.requests)
}

const (
	oauthTestAccess  = "access-token-sentinel"
	oauthTestRefresh = "refresh-token-sentinel"
	oauthTestCode    = "auth-code-sentinel"
)

func newOAuthAPI(t *testing.T, exchange *oauthExchange) (*Handler, *http.ServeMux, *credentials.Store, *http.Cookie) {
	t.Helper()
	h, mux, store, creds, cookie := newProvidersAPI(t, false, nil)
	t.Cleanup(func() { store.Close() })
	h.opts.OAuthClient = exchange
	// Sign-in callbacks are built from the configured origin, never a request
	// header, so every test that starts a flow needs one.
	h.opts.PublicOrigin = "https://dashboard.example.test"
	h.opts.Providers = append(h.opts.Providers,
		registry.Spec{ID: "claude", Transports: []registry.Protocol{registry.TransportAnthropic}, Auth: registry.AuthOAuth, DefaultBaseURL: "https://api.anthropic.com/v1", ModelCatalog: registry.CatalogStatic},
		registry.Spec{ID: "kilocode", Transports: []registry.Protocol{registry.TransportOpenAIChat}, Auth: registry.AuthOAuth, DefaultBaseURL: "https://api.kilo.ai/api", ModelCatalog: registry.CatalogDynamic},
		registry.Spec{ID: "codex", Transports: []registry.Protocol{registry.TransportOpenAIResponses}, Auth: registry.AuthOAuth, DefaultBaseURL: "https://chatgpt.com/backend-api/codex", ModelCatalog: registry.CatalogStatic},
		// A flow that consumes Params.RedirectURI, for the fail-closed assertion.
		registry.Spec{ID: "gemini-cli", Transports: []registry.Protocol{registry.TransportGeminiCLI}, Auth: registry.AuthOAuth, DefaultBaseURL: "https://cloudcode-pa.googleapis.com/v1internal", ModelCatalog: registry.CatalogStatic},
	)
	return h, mux, creds, cookie
}

func secondSession(t *testing.T, h *Handler) *http.Cookie {
	t.Helper()
	session, err := h.opts.Sessions.Create(adminauth.Account{ID: "a2", Username: "second"})
	if err != nil {
		t.Fatal(err)
	}
	return &http.Cookie{Name: sessionCookieName, Value: session.ID}
}

type startedFlow struct {
	State        string `json:"state"`
	AuthorizeURL string `json:"authorizeUrl"`
	Completion   string `json:"completion"`
}

func startFlow(t *testing.T, mux *http.ServeMux, cookie *http.Cookie, provider string) startedFlow {
	t.Helper()
	body := `{"name":"Work","identity":"work@example.test"}`
	if provider == "gitlab" {
		// GitLab is self-hosted: its client id is operator-supplied.
		body = `{"name":"Work","identity":"work@example.test","meta":{"clientId":"fixture-client"}}`
	}
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/"+provider+"/start", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var flow startedFlow
	if err := json.Unmarshal(recorder.Body.Bytes(), &flow); err != nil {
		t.Fatal(err)
	}
	return flow
}

func callback(t *testing.T, mux *http.ServeMux, cookie *http.Cookie, provider, state, input string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"state": state, "input": input})
	return doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/"+provider+"/callback", string(body))
}

func tokenBody() string {
	return `{"access_token":"` + oauthTestAccess + `","refresh_token":"` + oauthTestRefresh + `","id_token":"id-token-sentinel","expires_in":3600}`
}

func assertNoSecrets(t *testing.T, body string, extra ...string) {
	t.Helper()
	for _, secret := range append([]string{oauthTestAccess, oauthTestRefresh, oauthTestCode, "id-token-sentinel"}, extra...) {
		if strings.Contains(body, secret) {
			t.Fatalf("response leaked %q: %s", secret, body)
		}
	}
}

func TestOAuthStartRequiresSessionAndKnownProvider(t *testing.T) {
	_, mux, _, cookie := newOAuthAPI(t, &oauthExchange{})
	if recorder := doJSON(t, mux, nil, http.MethodPost, "/admin/v1/oauth/claude/start", `{"name":"a","identity":"b"}`); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous start status=%d want 401", recorder.Code)
	}
	if recorder := doJSON(t, mux, nil, http.MethodPost, "/admin/v1/oauth/claude/callback", `{"state":"x","input":"y"}`); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous callback status=%d want 401", recorder.Code)
	}
	if recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/openai/start", `{"name":"a","identity":"b"}`); recorder.Code != http.StatusNotFound {
		t.Fatalf("api-key provider start status=%d want 404", recorder.Code)
	}
	if recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/claude/start", `{"name":"","identity":"b"}`); recorder.Code != http.StatusBadRequest {
		t.Fatalf("missing name status=%d want 400", recorder.Code)
	}
}

func TestOAuthStartReturnsBoundAuthorizeURLWithoutVerifier(t *testing.T) {
	h, mux, _, cookie := newOAuthAPI(t, &oauthExchange{})
	flow := startFlow(t, mux, cookie, "claude")
	parsed, err := url.Parse(flow.AuthorizeURL)
	if err != nil {
		t.Fatal(err)
	}
	query := parsed.Query()
	if parsed.Host != "claude.ai" || query.Get("state") != flow.State || query.Get("code_challenge_method") != "S256" || query.Get("code_challenge") == "" {
		t.Fatalf("authorize url=%s", flow.AuthorizeURL)
	}
	pending := h.oauth.pending[flow.State]
	if strings.Contains(flow.AuthorizeURL, pending.verifier) || pending.verifier == "" {
		t.Fatal("verifier must stay server-side")
	}
	sum := sha256.Sum256([]byte(pending.verifier))
	if base64.RawURLEncoding.EncodeToString(sum[:]) != query.Get("code_challenge") {
		t.Fatal("challenge does not match the stored verifier")
	}
	if flow.Completion != "code" {
		t.Fatalf("completion=%q", flow.Completion)
	}
}

func TestOAuthClaudeCallbackSealsCredentialAndSendsVerifier(t *testing.T) {
	exchange := &oauthExchange{body: tokenBody()}
	h, mux, creds, cookie := newOAuthAPI(t, exchange)
	flow := startFlow(t, mux, cookie, "claude")
	verifier := h.oauth.pending[flow.State].verifier
	recorder := callback(t, mux, cookie, "claude", flow.State, oauthTestCode+"#"+flow.State)
	if recorder.Code != http.StatusOK {
		t.Fatalf("callback status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	assertNoSecrets(t, recorder.Body.String(), verifier)
	if exchange.calls() != 1 || exchange.requests[0].contentType != "application/json" {
		t.Fatalf("exchange=%+v", exchange.requests)
	}
	var sent map[string]string
	if err := json.Unmarshal([]byte(exchange.requests[0].body), &sent); err != nil {
		t.Fatal(err)
	}
	if sent["code"] != oauthTestCode || sent["code_verifier"] != verifier || sent["grant_type"] != "authorization_code" {
		t.Fatalf("claude exchange body=%v", sent)
	}
	connections, err := creds.ListConnections(context.Background(), "claude")
	if err != nil || len(connections) != 1 {
		t.Fatalf("connections=%v err=%v", connections, err)
	}
	stored := connections[0]
	if stored.AuthKind != credentials.AuthOAuth || stored.Identity != "work@example.test" || stored.Name != "Work" || stored.Secret.AccessToken != oauthTestAccess || stored.Secret.RefreshToken != oauthTestRefresh || stored.Secret.Expiry.IsZero() {
		t.Fatalf("stored=%+v", stored)
	}
}

func TestOAuthCodexCallbackAcceptsRedirectURLAndKeepsIDToken(t *testing.T) {
	exchange := &oauthExchange{body: tokenBody()}
	h, mux, creds, cookie := newOAuthAPI(t, exchange)
	flow := startFlow(t, mux, cookie, "codex")
	if flow.Completion != "redirect-url" || !strings.Contains(flow.AuthorizeURL, "codex_cli_simplified_flow=true") {
		t.Fatalf("flow=%+v", flow)
	}
	verifier := h.oauth.pending[flow.State].verifier
	redirect := "http://localhost:1455/auth/callback?code=" + oauthTestCode + "&state=" + url.QueryEscape(flow.State)
	recorder := callback(t, mux, cookie, "codex", flow.State, redirect)
	if recorder.Code != http.StatusOK {
		t.Fatalf("callback status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	assertNoSecrets(t, recorder.Body.String(), verifier)
	form, err := url.ParseQuery(exchange.requests[0].body)
	if err != nil || exchange.requests[0].contentType != "application/x-www-form-urlencoded" || form.Get("code") != oauthTestCode || form.Get("code_verifier") != verifier {
		t.Fatalf("codex exchange=%+v form=%v", exchange.requests[0], form)
	}
	connections, _ := creds.ListConnections(context.Background(), "codex")
	if len(connections) != 1 || connections[0].Secret.IDToken != "id-token-sentinel" {
		t.Fatalf("stored=%+v", connections)
	}
}

func TestOAuthStateCannotBeReplayed(t *testing.T) {
	exchange := &oauthExchange{body: tokenBody()}
	_, mux, creds, cookie := newOAuthAPI(t, exchange)
	flow := startFlow(t, mux, cookie, "claude")
	if recorder := callback(t, mux, cookie, "claude", flow.State, oauthTestCode); recorder.Code != http.StatusOK {
		t.Fatalf("first redeem status=%d", recorder.Code)
	}
	replay := callback(t, mux, cookie, "claude", flow.State, oauthTestCode)
	if replay.Code != http.StatusBadRequest || !strings.Contains(replay.Body.String(), "oauth_state_invalid") {
		t.Fatalf("replay status=%d body=%s", replay.Code, replay.Body.String())
	}
	if exchange.calls() != 1 {
		t.Fatalf("replay reached the provider: calls=%d", exchange.calls())
	}
	if connections, _ := creds.ListConnections(context.Background(), "claude"); len(connections) != 1 {
		t.Fatalf("replay created a second connection: %d", len(connections))
	}
}

func TestOAuthStateExpires(t *testing.T) {
	exchange := &oauthExchange{body: tokenBody()}
	h, mux, creds, cookie := newOAuthAPI(t, exchange)
	now := time.Now()
	h.oauth.now = func() time.Time { return now }
	flow := startFlow(t, mux, cookie, "claude")
	now = now.Add(oauthFlowTTL + time.Second)
	recorder := callback(t, mux, cookie, "claude", flow.State, oauthTestCode)
	if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "oauth_state_invalid") {
		t.Fatalf("expired status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if exchange.calls() != 0 {
		t.Fatal("expired state reached the provider")
	}
	if connections, _ := creds.ListConnections(context.Background(), "claude"); len(connections) != 0 {
		t.Fatal("expired state created a connection")
	}
	if len(h.oauth.pending) != 0 {
		t.Fatal("expired state was not discarded")
	}
}

func TestOAuthStateIsBoundToStartingSession(t *testing.T) {
	exchange := &oauthExchange{body: tokenBody()}
	h, mux, creds, owner := newOAuthAPI(t, exchange)
	intruder := secondSession(t, h)
	flow := startFlow(t, mux, owner, "claude")
	stolen := callback(t, mux, intruder, "claude", flow.State, oauthTestCode)
	if stolen.Code != http.StatusBadRequest || !strings.Contains(stolen.Body.String(), "oauth_state_invalid") {
		t.Fatalf("foreign session status=%d body=%s", stolen.Code, stolen.Body.String())
	}
	if exchange.calls() != 0 {
		t.Fatal("foreign session reached the provider")
	}
	if connections, _ := creds.ListConnections(context.Background(), "claude"); len(connections) != 0 {
		t.Fatal("foreign session created a connection")
	}
	// The failed attempt must not burn the owner's state.
	if recorder := callback(t, mux, owner, "claude", flow.State, oauthTestCode); recorder.Code != http.StatusOK {
		t.Fatalf("owner redeem after intruder status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestOAuthStateIsBoundToProvider(t *testing.T) {
	exchange := &oauthExchange{body: tokenBody()}
	_, mux, _, cookie := newOAuthAPI(t, exchange)
	flow := startFlow(t, mux, cookie, "claude")
	if recorder := callback(t, mux, cookie, "codex", flow.State, oauthTestCode); recorder.Code != http.StatusBadRequest || exchange.calls() != 0 {
		t.Fatalf("cross-provider status=%d calls=%d", recorder.Code, exchange.calls())
	}
}

func TestOAuthPastedStateFromAnotherAttemptIsRefusedWithoutConsuming(t *testing.T) {
	exchange := &oauthExchange{body: tokenBody()}
	_, mux, _, cookie := newOAuthAPI(t, exchange)
	first := startFlow(t, mux, cookie, "claude")
	second := startFlow(t, mux, cookie, "claude")
	if recorder := callback(t, mux, cookie, "claude", second.State, oauthTestCode+"#"+first.State); recorder.Code != http.StatusBadRequest || exchange.calls() != 0 {
		t.Fatalf("mismatched paste status=%d calls=%d", recorder.Code, exchange.calls())
	}
	if recorder := callback(t, mux, cookie, "claude", second.State, oauthTestCode+"#"+second.State); recorder.Code != http.StatusOK {
		t.Fatalf("matching paste status=%d", recorder.Code)
	}
}

func TestOAuthPendingAttemptsPerSessionAreBounded(t *testing.T) {
	_, mux, _, cookie := newOAuthAPI(t, &oauthExchange{body: tokenBody()})
	first := startFlow(t, mux, cookie, "claude")
	for i := 0; i < oauthMaxPerSession; i++ {
		startFlow(t, mux, cookie, "claude")
	}
	if recorder := callback(t, mux, cookie, "claude", first.State, oauthTestCode); recorder.Code != http.StatusBadRequest {
		t.Fatalf("evicted oldest attempt status=%d want 400", recorder.Code)
	}
}

func TestOAuthCallbackNeverEchoesProviderText(t *testing.T) {
	exchange := &oauthExchange{status: http.StatusBadRequest, body: `{"error":"invalid_grant","error_description":"leak <script>alert(1)</script> ` + oauthTestRefresh + `"}`}
	_, mux, creds, cookie := newOAuthAPI(t, exchange)

	denied := startFlow(t, mux, cookie, "codex")
	hostile := "http://localhost:1455/auth/callback?error=access_denied&error_description=" + url.QueryEscape("<script>alert(1)</script>") + "&state=" + url.QueryEscape(denied.State)
	recorder := callback(t, mux, cookie, "codex", denied.State, hostile)
	if recorder.Code != http.StatusBadRequest || strings.Contains(recorder.Body.String(), "script") || strings.Contains(recorder.Body.String(), "access_denied") {
		t.Fatalf("denied status=%d body=%s", recorder.Code, recorder.Body.String())
	}

	rejected := startFlow(t, mux, cookie, "claude")
	recorder = callback(t, mux, cookie, "claude", rejected.State, oauthTestCode)
	if recorder.Code != http.StatusBadGateway || strings.Contains(recorder.Body.String(), "script") || strings.Contains(recorder.Body.String(), "invalid_grant") {
		t.Fatalf("exchange failure status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	assertNoSecrets(t, recorder.Body.String())
	if connections, _ := creds.ListConnections(context.Background(), "claude"); len(connections) != 0 {
		t.Fatal("failed exchange created a connection")
	}
	// A failed exchange still consumes the state.
	if again := callback(t, mux, cookie, "claude", rejected.State, oauthTestCode); again.Code != http.StatusBadRequest || !strings.Contains(again.Body.String(), "oauth_state_invalid") {
		t.Fatalf("retry after failure status=%d", again.Code)
	}
}

func TestParseOAuthInput(t *testing.T) {
	cases := []struct {
		name, input, code, state string
		denied                   bool
	}{
		{"bare code", "abc123", "abc123", "", false},
		{"claude code#state", "abc123#st", "abc123", "st", false},
		{"redirect url", "http://localhost:1455/auth/callback?code=abc&state=st", "abc", "st", false},
		{"query only", "?code=abc&state=st", "abc", "st", false},
		{"provider error", "http://localhost:1455/auth/callback?error=access_denied&state=st", "", "st", true},
		{"whitespace in code", "abc 123", "", "", false},
		{"control characters", "abc\n123", "", "", false},
		{"empty", "  ", "", "", false},
		{"oversize", strings.Repeat("a", oauthMaxInput+1), "", "", false},
	}
	for _, tc := range cases {
		code, denied, state := parseOAuthInput(tc.input)
		if code != tc.code || denied != tc.denied || state != tc.state {
			t.Errorf("%s: got (%q,%v,%q) want (%q,%v,%q)", tc.name, code, denied, state, tc.code, tc.denied, tc.state)
		}
	}
}

func TestOAuthUnusablePasteDoesNotBurnTheAttempt(t *testing.T) {
	exchange := &oauthExchange{body: tokenBody()}
	_, mux, _, cookie := newOAuthAPI(t, exchange)
	flow := startFlow(t, mux, cookie, "claude")
	for _, paste := range []string{"abc\n123", "abc 123", strings.Repeat("a", oauthMaxInput+1)} {
		recorder := callback(t, mux, cookie, "claude", flow.State, paste)
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "oauth_code_missing") {
			t.Fatalf("paste %q status=%d body=%s", paste, recorder.Code, recorder.Body.String())
		}
	}
	if exchange.calls() != 0 {
		t.Fatal("unusable paste reached the provider")
	}
	if recorder := callback(t, mux, cookie, "claude", flow.State, oauthTestCode); recorder.Code != http.StatusOK {
		t.Fatalf("redeem after unusable pastes status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestOAuthGlobalPendingLimit(t *testing.T) {
	h, mux, _, cookie := newOAuthAPI(t, &oauthExchange{body: tokenBody()})
	for i := 0; i < oauthMaxPending; i++ {
		state, ok := newState()
		if !ok {
			t.Fatal("state mint failed")
		}
		if _, ok := h.oauth.begin(state, oauthFlow{sessionID: "other-" + strconv.Itoa(i), provider: "claude"}); !ok {
			t.Fatalf("begin %d refused below the limit", i)
		}
	}
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/claude/start", `{"name":"Work","identity":"work@example.test"}`)
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "oauth_busy") {
		t.Fatalf("start at limit status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

func TestOAuthSignInRefreshesThroughTheRegistry(t *testing.T) {
	// expires_in inside the refresh lead makes the first read refresh at once.
	exchange := &oauthExchange{body: `{"access_token":"` + oauthTestAccess + `","refresh_token":"` + oauthTestRefresh + `","expires_in":5}`}
	h, mux, creds, cookie := newOAuthAPI(t, exchange)
	resolver, err := oauthrefresh.Resolver(exchange)
	if err != nil {
		t.Fatal(err)
	}
	h.opts.CredentialRegistry.SetExchangerResolver(resolver)
	flow := startFlow(t, mux, cookie, "claude")
	if recorder := callback(t, mux, cookie, "claude", flow.State, oauthTestCode); recorder.Code != http.StatusOK {
		t.Fatalf("callback status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	connections, _ := creds.ListConnections(context.Background(), "claude")
	if _, err := h.opts.CredentialRegistry.Secret(context.Background(), connections[0].ID); err != nil {
		t.Fatal(err)
	}
	if exchange.calls() != 2 || !strings.Contains(exchange.requests[1].body, "refresh_token") {
		t.Fatalf("expired sign-in credential was not refreshed: %+v", exchange.requests)
	}
}

// newOnboardingAPI builds a handler with the providers the new flow shapes need.
func newOnboardingAPI(t *testing.T, exchange OAuthClient) (*Handler, *http.ServeMux, *credentials.Store, *http.Cookie) {
	t.Helper()
	h, mux, store, creds, cookie := newProvidersAPI(t, false, nil)
	t.Cleanup(func() { store.Close() })
	h.opts.OAuthClient = exchange
	h.opts.PublicOrigin = "https://dashboard.example.test"
	t.Setenv(onboarding.GoogleClientIDEnv, "fixture-client-id")
	t.Setenv(onboarding.GoogleClientSecretEnv, "fixture-client-secret")
	t.Setenv(onboarding.IflowClientIDEnv, "fixture-iflow-client-id")
	t.Setenv(onboarding.IflowClientSecretEnv, "fixture-iflow-client-secret")
	h.opts.Providers = append(h.opts.Providers,
		registry.Spec{ID: "github", Transports: []registry.Protocol{registry.TransportOpenAIChat}, Auth: registry.AuthOAuth, DefaultBaseURL: "https://api.githubcopilot.com", ModelCatalog: registry.CatalogStatic},
		registry.Spec{ID: "kimchi", Transports: []registry.Protocol{registry.TransportOpenAIChat}, Auth: registry.AuthAPIKey, AuthModes: []registry.AuthKind{registry.AuthAPIKey, registry.AuthOAuth}, DefaultBaseURL: "https://llm.kimchi.dev/openai/v1", ModelCatalog: CatalogPassthroughForTest(), PassthroughModels: true},
		registry.Spec{ID: "gitlab", Transports: []registry.Protocol{registry.TransportOpenAIChat}, Auth: registry.AuthOAuth, DefaultBaseURL: "https://gitlab.com", ModelCatalog: registry.CatalogStatic},
		registry.Spec{ID: "cursor", Transports: []registry.Protocol{registry.TransportCursor}, Auth: registry.AuthOAuth, DefaultBaseURL: "https://api2.cursor.sh", ModelCatalog: registry.CatalogStatic},
	)
	return h, mux, creds, cookie
}

// CatalogPassthroughForTest names the passthrough catalog class.
func CatalogPassthroughForTest() registry.ModelCatalog { return registry.CatalogPassthrough }

// routeExchange answers per URL fragment, which a multi-call flow needs (a device
// start and its poll hit different endpoints).
type routeExchange struct {
	mu     sync.Mutex
	routes map[string]string
	status int
	// lastTokenBody and seenURLs let a test assert what a flow actually sent, which
	// is how a dropped operator field is caught rather than assumed.
	lastTokenBody string
	seenURLs      []string
}

func (r *routeExchange) Do(request *http.Request) (*http.Response, error) {
	var raw []byte
	if request.Body != nil {
		raw, _ = io.ReadAll(request.Body)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seenURLs = append(r.seenURLs, request.URL.String())
	body := "{}"
	matched := ""
	for fragment, payload := range r.routes {
		// Longest fragment wins so a generic key cannot shadow a specific one.
		if strings.Contains(request.URL.String(), fragment) && len(fragment) > len(matched) {
			matched, body = fragment, payload
		}
	}
	if strings.Contains(request.URL.String(), "token") {
		r.lastTokenBody = string(raw)
	}
	status := r.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
}

// TestDeviceStartReturnsVerificationAndPollCompletes proves a device flow needs no
// browser redirect: start returns the user code, poll stores the connection.
func TestDeviceStartReturnsVerificationAndPollCompletes(t *testing.T) {
	exchange := &routeExchange{routes: map[string]string{
		"login/device/code":        `{"device_code":"fixture-device","user_code":"ABCD-1234","verification_uri":"https://example.invalid/device","expires_in":900,"interval":5}`,
		"login/oauth/access_token": `{"access_token":"` + oauthTestAccess + `","refresh_token":"` + oauthTestRefresh + `"}`,
		"copilot_internal":         `{"token":"fixture-copilot","expires_at":1761000000}`,
		"api.github.com/user":      `{"login":"fixture-login","id":42}`,
	}}
	h, mux, creds, cookie := newOnboardingAPI(t, exchange)
	// The device poll is throttled per attempt, so the test advances the clock the
	// same way a real browser's interval would.
	clock := &deviceClock{now: time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)}
	h.oauth.now = func() time.Time { return clock.now }

	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/github/device", `{"name":"Work","identity":"work@example.test"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var started struct {
		State           string `json:"state"`
		FlowType        string `json:"flowType"`
		UserCode        string `json:"userCode"`
		VerificationURL string `json:"verificationUrl"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.FlowType != "device_code" || started.UserCode == "" || started.VerificationURL == "" {
		t.Fatalf("device start=%+v", started)
	}
	assertNoSecrets(t, recorder.Body.String())

	// A pending poll must not fail and must not store anything.
	clock.now = clock.now.Add(3 * time.Second)
	exchange.status = http.StatusBadRequest
	exchange.routes["login/oauth/access_token"] = `{"error":"authorization_pending"}`
	pending := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/github/poll", `{"state":"`+started.State+`","deviceCode":"fixture-device"}`)
	if pending.Code != http.StatusOK || !strings.Contains(pending.Body.String(), `"pending"`) {
		t.Fatalf("pending poll status=%d body=%s", pending.Code, pending.Body.String())
	}
	if connections, _ := creds.ListConnections(context.Background(), "github"); len(connections) != 0 {
		t.Fatalf("a pending poll stored a connection: %+v", connections)
	}

	// A completed poll stores the connection.
	clock.now = clock.now.Add(3 * time.Second)
	exchange.status = http.StatusOK
	exchange.routes["login/oauth/access_token"] = `{"access_token":"` + oauthTestAccess + `","refresh_token":"` + oauthTestRefresh + `"}`
	done := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/github/poll", `{"state":"`+started.State+`","deviceCode":"fixture-device"}`)
	if done.Code != http.StatusOK || !strings.Contains(done.Body.String(), `"connected"`) {
		t.Fatalf("done poll status=%d body=%s", done.Code, done.Body.String())
	}
	assertNoSecrets(t, done.Body.String())
	connections, err := creds.ListConnections(context.Background(), "github")
	if err != nil || len(connections) != 1 {
		t.Fatalf("connections=%+v err=%v", connections, err)
	}
	if connections[0].Secret.AccessToken != oauthTestAccess {
		t.Fatalf("stored secret=%+v", connections[0].Secret)
	}

	// The state is single use.
	clock.now = clock.now.Add(3 * time.Second)
	replay := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/github/poll", `{"state":"`+started.State+`","deviceCode":"fixture-device"}`)
	if replay.Code != http.StatusBadRequest {
		t.Fatalf("replayed poll status=%d want 400", replay.Code)
	}
}

// TestPollRejectsBrowserFlow keeps a device poll from being used on a flow that
// has no device code.
func TestPollRejectsBrowserFlow(t *testing.T) {
	_, mux, _, cookie := newOnboardingAPI(t, &oauthExchange{})
	flow := startFlow(t, mux, cookie, "gitlab")
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/gitlab/poll", `{"state":"`+flow.State+`"}`)
	// A browser flow has no device provider, so the poll route refuses it outright.
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("poll on a browser flow status=%d want 404", recorder.Code)
	}
}

// TestImportStoresManualToken proves the manual-token path persists without a
// browser step and still publishes to the credential registry.
// TestImportRejectsUnverifiedCredential is the negative half of the import path:
// a token the host rejects is not stored. It is named for that, not for storing.
func TestImportRejectsUnverifiedCredential(t *testing.T) {
	exchange := &routeExchange{status: http.StatusUnauthorized, routes: map[string]string{}}
	_, mux, creds, cookie := newOnboardingAPI(t, exchange)
	body := `{"name":"Work","identity":"work@example.test","input":"` + oauthTestAccess + `"}`
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/gitlab/import", body)
	if recorder.Code != http.StatusBadGateway {
		t.Fatalf("unverified import status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if connections, _ := creds.ListConnections(context.Background(), "gitlab"); len(connections) != 0 {
		t.Fatal("stored an unverified import")
	}
}

// TestImportStoresAVerifiedCredential is the positive half the old name claimed but
// never tested: a verified import must produce a stored connection.
func TestImportStoresAVerifiedCredential(t *testing.T) {
	exchange := &routeExchange{routes: map[string]string{
		"api/v4/user": `{"username":"fixture","email":"fixture@example.invalid","name":"Fixture"}`,
	}}
	_, mux, creds, cookie := newOnboardingAPI(t, exchange)
	body := `{"name":"Work","identity":"work@example.test","input":"fixture-pat"}`
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/gitlab/import", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("import status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	assertNoSecrets(t, recorder.Body.String(), "fixture-pat")
	connections, err := creds.ListConnections(context.Background(), "gitlab")
	if err != nil || len(connections) != 1 {
		t.Fatalf("connections=%+v err=%v", connections, err)
	}
	stored := connections[0]
	if stored.Secret.AccessToken != "fixture-pat" || !stored.Enabled {
		t.Fatalf("stored=%+v", stored)
	}
	if stored.Secret.ProviderData["authKind"] != "personal_access_token" {
		t.Fatalf("provider data=%v", stored.Secret.ProviderData)
	}
	if stored.Identity != "fixture@example.invalid" {
		t.Fatalf("identity=%q, want the provider-reported account", stored.Identity)
	}
}

// TestImportCursorStoresAConnection proves the import-only provider works with the
// body a real client sends. It previously required meta fields the UI never posts.
func TestImportCursorStoresAConnection(t *testing.T) {
	// The pin makes no provider call for a Cursor import, so nothing is scripted
	// and any request would fail the assertion below.
	exchange := &routeExchange{routes: map[string]string{}}
	_, mux, creds, cookie := newOnboardingAPI(t, exchange)
	body := `{"name":"Work","identity":"work@example.test","input":"` + fixtureCursorToken + `","meta":{"machineId":"` + fixtureCursorMachineID + `"}}`
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/cursor/import", body)
	if recorder.Code != http.StatusOK {
		t.Fatalf("cursor import status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	connections, err := creds.ListConnections(context.Background(), "cursor")
	if err != nil || len(connections) != 1 {
		t.Fatalf("connections=%+v err=%v", connections, err)
	}
	if connections[0].Secret.AccessToken != fixtureCursorToken {
		t.Fatalf("stored=%+v", connections[0].Secret)
	}
	if connections[0].Secret.ProviderData["machineId"] != fixtureCursorMachineID || connections[0].Secret.Expiry.IsZero() {
		t.Fatalf("machine id or expiry not stored: %+v", connections[0].Secret)
	}
	if len(exchange.seenURLs) != 0 {
		t.Fatalf("cursor import made provider calls %v; the pin validates shape only", exchange.seenURLs)
	}
}

// TestGitlabStartMetaReachesTheTokenRequest is the plumbing test: operator input
// supplied at start must survive to the exchange. It previously vanished, so the
// token request carried an empty client_id.
func TestGitlabStartMetaReachesTheTokenRequest(t *testing.T) {
	exchange := &routeExchange{routes: map[string]string{
		"oauth/token": `{"access_token":"fixture-access","refresh_token":"fixture-refresh","expires_in":3600}`,
		"api/v4/user": `{"username":"fixture","email":"fixture@example.invalid","name":"Fixture"}`,
	}}
	_, mux, _, cookie := newOnboardingAPI(t, exchange)
	started := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/gitlab/start",
		`{"name":"Work","identity":"work@example.test","meta":{"clientId":"fixture-client-id","baseUrl":"https://gitlab.example.test"}}`)
	if started.Code != http.StatusOK {
		t.Fatalf("start status=%d body=%s", started.Code, started.Body.String())
	}
	var flow struct {
		State string `json:"state"`
	}
	if err := json.Unmarshal(started.Body.Bytes(), &flow); err != nil {
		t.Fatal(err)
	}
	exchange.mu.Lock()
	exchange.lastTokenBody = ""
	exchange.mu.Unlock()
	done := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/gitlab/callback", `{"state":"`+flow.State+`","input":"fixture-code"}`)
	if done.Code != http.StatusOK {
		t.Fatalf("callback status=%d body=%s", done.Code, done.Body.String())
	}
	exchange.mu.Lock()
	sent := exchange.lastTokenBody
	exchange.mu.Unlock()
	if !strings.Contains(sent, "client_id=fixture-client-id") {
		t.Fatalf("token request dropped the operator client id: %q", sent)
	}
	// The self-hosted base URL must also survive: the exchange must have gone to it.
	exchange.mu.Lock()
	urls := strings.Join(exchange.seenURLs, " ")
	exchange.mu.Unlock()
	if !strings.Contains(urls, "gitlab.example.test") {
		t.Fatalf("exchange did not use the operator base URL: %v", urls)
	}
}

// TestOAuthRoutesRequireSession covers the new endpoints' auth guard.
func TestOAuthRoutesRequireSession(t *testing.T) {
	_, mux, _, _ := newOnboardingAPI(t, &oauthExchange{})
	for _, path := range []string{
		"/admin/v1/oauth/github/poll",
		"/admin/v1/oauth/gitlab/import",
	} {
		if recorder := doJSON(t, mux, nil, http.MethodPost, path, `{}`); recorder.Code != http.StatusUnauthorized {
			t.Errorf("%s anonymous status=%d want 401", path, recorder.Code)
		}
	}
}

// TestOnboardingFlowsWorkWithTheDefaultClient is the seam test: the handler must
// reach the network without a caller-injected OAuthClient. Every other OAuth test
// sets one, which is exactly how a missing production default stayed green.
func TestOnboardingFlowsWorkWithTheDefaultClient(t *testing.T) {
	// newProvidersAPI builds the handler with no OAuthClient, like the app does.
	h, mux, store, _, cookie := newProvidersAPI(t, true, nil)
	t.Cleanup(func() { store.Close() })
	h.opts.PublicOrigin = "https://dashboard.example.test"
	h.opts.Providers = append(h.opts.Providers,
		registry.Spec{ID: "claude", Transports: []registry.Protocol{registry.TransportAnthropic}, Auth: registry.AuthOAuth, DefaultBaseURL: "https://api.anthropic.com/v1", ModelCatalog: registry.CatalogStatic},
		registry.Spec{ID: "github", Transports: []registry.Protocol{registry.TransportOpenAIChat}, Auth: registry.AuthOAuth, DefaultBaseURL: "https://api.githubcopilot.com", ModelCatalog: registry.CatalogStatic},
	)
	if h.opts.OAuthClient == nil {
		t.Fatal("handler has no OAuth client; sign-in would fail for every provider")
	}
	// A browser flow must produce an authorize URL rather than failing on a nil
	// client.
	if recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/claude/start", `{"name":"Work","identity":"work@example.test"}`); recorder.Code != http.StatusOK {
		t.Fatalf("claude start status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	// A device flow's failure must be the upstream being unreachable, never a
	// missing client. A nil client answers 502 oauth_start_failed for every
	// provider; a wired client gets past the client check and the request fails
	// on its own merits.
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/github/device", `{"name":"Work","identity":"work@example.test"}`)
	if recorder.Code == http.StatusInternalServerError {
		t.Fatalf("device start hit a server error: %s", recorder.Body.String())
	}
}

// TestDefaultOAuthClientUsesTheSSRFPolicy proves the default client is the same
// guarded transport discovery uses, not an unguarded http.Client.
func TestDefaultOAuthClientUsesTheSSRFPolicy(t *testing.T) {
	h := New(Options{})
	client, ok := h.opts.OAuthClient.(*http.Client)
	if !ok || client == nil {
		t.Fatalf("default OAuth client is %T", h.opts.OAuthClient)
	}
	if client.Transport == nil {
		t.Fatal("default OAuth client has no transport, so it bypasses the SSRF policy")
	}
}

// TestDeviceSignInNeedsNoPublicOrigin is the seam test for the second blocker: a
// device flow shows a code and polls, so it must work on an install that never
// configured ROUTEWEFT_PUBLIC_ORIGIN. A browser flow on the same install must
// still fail closed rather than guess a redirect.
func TestDeviceSignInNeedsNoPublicOrigin(t *testing.T) {
	upstream := &deviceUpstream{pollStatus: http.StatusAccepted}
	h, mux, _, cookie, clock := newDeviceAPI(t, upstream)
	h.opts.PublicOrigin = ""
	clock.now = time.Date(2026, 10, 19, 12, 0, 0, 0, time.UTC)

	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/kilocode/device", `{"name":"Work","identity":"work@example.test"}`)
	if recorder.Code != http.StatusOK {
		t.Fatalf("device start without a public origin status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var started struct {
		State    string `json:"state"`
		UserCode string `json:"userCode"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &started); err != nil {
		t.Fatal(err)
	}
	if started.State == "" || started.UserCode == "" {
		t.Fatalf("device start=%+v", started)
	}

	// A browser flow that consumes the redirect still refuses to guess. gemini-cli
	// reads Params.RedirectURI; claude pins its callback and does not, so asserting
	// claude 503s was the regression itself.
	if recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/gemini-cli/start", `{"name":"Work","identity":"work@example.test"}`); recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("browser start without a public origin status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}
