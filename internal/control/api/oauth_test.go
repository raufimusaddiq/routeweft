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
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/adminauth"
	"github.com/raufimusaddiq/routeweft/internal/credentials"
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
	raw, _ := io.ReadAll(request.Body)
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
	h.opts.Providers = append(h.opts.Providers,
		registry.Spec{ID: "claude", Transports: []registry.Protocol{registry.TransportAnthropic}, Auth: registry.AuthOAuth, DefaultBaseURL: "https://api.anthropic.com/v1", ModelCatalog: registry.CatalogStatic},
		registry.Spec{ID: "codex", Transports: []registry.Protocol{registry.TransportOpenAIResponses}, Auth: registry.AuthOAuth, DefaultBaseURL: "https://chatgpt.com/backend-api/codex", ModelCatalog: registry.CatalogStatic},
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
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/"+provider+"/start", `{"name":"Work","identity":"work@example.test"}`)
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
