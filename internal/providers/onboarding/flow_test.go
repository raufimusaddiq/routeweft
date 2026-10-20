package onboarding

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

// stubClient answers every sign-in call from a scripted handler and records the
// requests, so a flow's wire contract is asserted without a network.
type stubClient struct {
	handler  func(*http.Request) (int, string)
	requests []*http.Request
	bodies   []string
}

func (s *stubClient) Do(request *http.Request) (*http.Response, error) {
	body := ""
	if request.Body != nil {
		raw, _ := io.ReadAll(request.Body)
		body = string(raw)
	}
	s.requests = append(s.requests, request)
	s.bodies = append(s.bodies, body)
	status, payload := http.StatusOK, "{}"
	if s.handler != nil {
		status, payload = s.handler(request)
	}
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(payload)),
	}, nil
}

func (s *stubClient) last() *http.Request {
	if len(s.requests) == 0 {
		return nil
	}
	return s.requests[len(s.requests)-1]
}

func flowFor(t *testing.T, provider string) Flow {
	t.Helper()
	flow, ok := NewRegistry().Lookup(provider)
	if !ok {
		t.Fatalf("no flow for %q", provider)
	}
	return flow
}

// TestEveryOracleFlowIsImplemented is the drift guard: the reference declares 22
// provider flows plus the Xiaomi route handshake, and none may be dropped.
func TestEveryOracleFlowIsImplemented(t *testing.T) {
	implemented := NewRegistry().OnboardingFlows()
	for _, provider := range []string{
		"antigravity", "claude", "cline", "clinepass", "codebuddy-cn", "codebuddy-intl",
		"codex", "cursor", "gemini-cli", "github", "gitlab", "grok-cli", "iflow",
		"kilocode", "kimchi", "kimi", "kiro", "qoder", "trae", "windsurf", "xai",
		"xiaomi-mimo", "zed",
	} {
		if _, ok := implemented[provider]; !ok {
			t.Errorf("reference offers onboarding for %q but no flow is implemented", provider)
		}
	}
}

// TestFlowTypesMatchReference pins each provider to the flow shape the reference
// declares, so a provider cannot be quietly downgraded to a paste box.
func TestFlowTypesMatchReference(t *testing.T) {
	want := map[string]FlowType{
		"claude":         FlowAuthorizationCodePKCE,
		"codex":          FlowAuthorizationCodePKCE,
		"xai":            FlowAuthorizationCodePKCE,
		"gitlab":         FlowAuthorizationCodePKCE,
		"gemini-cli":     FlowAuthorizationCode,
		"antigravity":    FlowAuthorizationCode,
		"iflow":          FlowAuthorizationCode,
		"cline":          FlowAuthorizationCode,
		"clinepass":      FlowAuthorizationCode,
		"trae":           FlowAuthorizationCode,
		"windsurf":       FlowAuthorizationCode,
		"zed":            FlowAuthorizationCode,
		"xiaomi-mimo":    FlowAuthorizationCode,
		"grok-cli":       FlowDeviceCode,
		"github":         FlowDeviceCode,
		"kiro":           FlowDeviceCode,
		"kimi":           FlowDeviceCode,
		"kilocode":       FlowDeviceCode,
		"codebuddy-cn":   FlowDeviceCode,
		"codebuddy-intl": FlowDeviceCode,
		"qoder":          FlowDeviceCode,
		"kimchi":         FlowBrowserToken,
		"cursor":         FlowImportToken,
	}
	registry := NewRegistry()
	for provider, flowType := range want {
		flow, ok := registry.Lookup(provider)
		if !ok {
			t.Errorf("missing flow for %q", provider)
			continue
		}
		if flow.Type != flowType {
			t.Errorf("%s flow type = %q, reference uses %q", provider, flow.Type, flowType)
		}
	}
}

// TestClaudePasteCodeState proves Claude's "code#state" paste is split, and the
// JSON token body is sent as the reference does.
func TestClaudePasteCodeState(t *testing.T) {
	stub := &stubClient{handler: func(*http.Request) (int, string) {
		return http.StatusOK, `{"access_token":"fixture-access","refresh_token":"fixture-refresh","expires_in":3600}`
	}}
	flow := flowFor(t, "claude")
	result, err := flow.Exchange(context.Background(), Params{
		Verifier: "fixture-verifier",
		Input:    "fixture-code#fixture-state",
		Client:   stub,
		Now:      func() time.Time { return time.Unix(1000, 0) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Secret.AccessToken != "fixture-access" || result.Secret.RefreshToken != "fixture-refresh" {
		t.Fatalf("secret=%+v", result.Secret)
	}
	if result.AuthKind != credentials.AuthOAuth {
		t.Fatalf("auth kind=%q", result.AuthKind)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(stub.bodies[0]), &body); err != nil {
		t.Fatalf("claude token body is not JSON: %q", stub.bodies[0])
	}
	if body["code"] != "fixture-code" || body["state"] != "fixture-state" || body["code_verifier"] != "fixture-verifier" {
		t.Fatalf("token body=%v", body)
	}
}

// TestCodexCapturesAccountID proves the ChatGPT account id and plan are persisted
// with the connection, which routing and quota both need.
func TestCodexCapturesAccountID(t *testing.T) {
	// A JWT with the OpenAI auth claims; the signature is irrelevant because the
	// claims are only used for display and routing.
	claims := urlSafeEncode([]byte(`{"https://api.openai.com/auth":{"chatgpt_account_id":"fixture-account","chatgpt_plan_type":"plus"},"https://api.openai.com/profile":{"email":"fixture@example.invalid"}}`))
	idToken := "header." + claims + ".signature"
	stub := &stubClient{handler: func(*http.Request) (int, string) {
		return http.StatusOK, `{"access_token":"fixture-access","refresh_token":"fixture-refresh","id_token":"` + idToken + `","expires_in":3600}`
	}}
	result, err := flowFor(t, "codex").Exchange(context.Background(), Params{
		Verifier: "fixture-verifier",
		Input:    "fixture-code",
		Client:   stub,
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Secret.ProviderData["chatgptAccountId"] != "fixture-account" {
		t.Fatalf("provider data=%v", result.Secret.ProviderData)
	}
	if result.Secret.ProviderData["chatgptPlanType"] != "plus" {
		t.Fatalf("provider data=%v", result.Secret.ProviderData)
	}
	if result.Identity != "fixture@example.invalid" {
		t.Fatalf("identity=%q", result.Identity)
	}
}

// TestDeviceFlowPendingIsNotFailure proves a pending poll is a normal state.
func TestDeviceFlowPendingIsNotFailure(t *testing.T) {
	stub := &stubClient{handler: func(*http.Request) (int, string) {
		return http.StatusBadRequest, `{"error":"authorization_pending"}`
	}}
	outcome, err := flowFor(t, "github").Poll(context.Background(), Params{Input: "fixture-device", Client: stub})
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Pending || outcome.Err != nil || outcome.Done {
		t.Fatalf("outcome=%+v", outcome)
	}
}

// TestGitHubCapturesCopilotToken proves the post-exchange step persists the token
// inference actually sends.
func TestGitHubCapturesCopilotToken(t *testing.T) {
	stub := &stubClient{handler: func(request *http.Request) (int, string) {
		switch {
		case strings.Contains(request.URL.Path, "access_token"):
			return http.StatusOK, `{"access_token":"fixture-github"}`
		case strings.Contains(request.URL.Path, "copilot_internal"):
			return http.StatusOK, `{"token":"fixture-copilot","expires_at":1761000000}`
		default:
			return http.StatusOK, `{"login":"fixture-login","id":42}`
		}
	}}
	outcome, err := flowFor(t, "github").Poll(context.Background(), Params{Input: "fixture-device", Client: stub})
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Done {
		t.Fatalf("outcome=%+v", outcome)
	}
	if outcome.Result.Secret.ProviderData["copilotToken"] != "fixture-copilot" {
		t.Fatalf("provider data=%v", outcome.Result.Secret.ProviderData)
	}
	if outcome.Result.Identity != "fixture-login" {
		t.Fatalf("identity=%q", outcome.Result.Identity)
	}
}

// TestCodeBuddyPendingCodeIsNotFailure keeps the provider's retry code out of the
// error path.
func TestCodeBuddyPendingCodeIsNotFailure(t *testing.T) {
	stub := &stubClient{handler: func(*http.Request) (int, string) {
		return http.StatusOK, `{"code":11217}`
	}}
	outcome, err := flowFor(t, "codebuddy-cn").Poll(context.Background(), Params{Input: "fixture-state", Client: stub})
	if err != nil {
		t.Fatal(err)
	}
	if !outcome.Pending {
		t.Fatalf("outcome=%+v", outcome)
	}
}

// TestKiroDeviceFlowRegistersClient proves the AWS SSO client registration is
// performed before the device authorization and its credentials are retained.
func TestKiroDeviceFlowRegistersClient(t *testing.T) {
	stub := &stubClient{handler: func(request *http.Request) (int, string) {
		switch {
		case strings.Contains(request.URL.Path, "client/register"):
			return http.StatusOK, `{"clientId":"fixture-client","clientSecret":"fixture-secret"}`
		case strings.Contains(request.URL.Path, "device_authorization"):
			return http.StatusOK, `{"deviceCode":"fixture-device","userCode":"ABCD-1234","verificationUri":"https://example.invalid","expiresIn":600,"interval":5}`
		default:
			return http.StatusOK, `{}`
		}
	}}
	request, err := flowFor(t, "kiro").Authorize(context.Background(), Params{Client: stub})
	if err != nil {
		t.Fatal(err)
	}
	if request.Extra["clientId"] != "fixture-client" || request.Extra["clientSecret"] != "fixture-secret" {
		t.Fatalf("extra=%v", request.Extra)
	}
	if request.UserCode != "ABCD-1234" {
		t.Fatalf("user code=%q", request.UserCode)
	}
}

// TestKiroRejectsUnsafeRegion keeps a region value from becoming an SSRF vector.
func TestKiroRejectsUnsafeRegion(t *testing.T) {
	stub := &stubClient{}
	_, err := flowFor(t, "kiro").Authorize(context.Background(), Params{
		Client: stub,
		Meta:   map[string]string{"region": "evil.example.com/"},
	})
	if err == nil {
		t.Fatal("accepted an unsafe AWS region")
	}
	if len(stub.requests) != 0 {
		t.Fatal("sent a request with an unsafe region")
	}
}

// TestIflowRequiresAPIKey proves a sign-in that returns no API key fails rather
// than storing an unusable connection.
func TestIflowRequiresAPIKey(t *testing.T) {
	stub := &stubClient{handler: func(request *http.Request) (int, string) {
		if strings.Contains(request.URL.Path, "getUserInfo") {
			return http.StatusOK, `{"success":true,"data":{"apiKey":"","email":"fixture@example.invalid"}}`
		}
		return http.StatusOK, `{"access_token":"fixture","refresh_token":"fixture"}`
	}}
	if _, err := flowFor(t, "iflow").Exchange(context.Background(), Params{Input: "fixture", Client: stub}); err == nil {
		t.Fatal("accepted an iflow sign-in with no API key")
	}
}

// TestClineDecodesTokenBundle proves the extension's base64 code path is read
// before the exchange fallback.
func TestClineDecodesTokenBundle(t *testing.T) {
	bundle := base64Std(`{"accessToken":"fixture-cline","refreshToken":"fixture-refresh","email":"fixture@example.invalid"}`)
	stub := &stubClient{}
	result, err := flowFor(t, "cline").Exchange(context.Background(), Params{Input: bundle, Client: stub})
	if err != nil {
		t.Fatal(err)
	}
	if result.Secret.AccessToken != "fixture-cline" {
		t.Fatalf("secret=%+v", result.Secret)
	}
	if len(stub.requests) != 0 {
		t.Fatal("exchanged a code that was already a token bundle")
	}
}

// TestKimchiRejectsInvalidToken proves validation happens before storage.
func TestKimchiRejectsInvalidToken(t *testing.T) {
	stub := &stubClient{handler: func(*http.Request) (int, string) {
		return http.StatusUnauthorized, `{}`
	}}
	if _, err := flowFor(t, "kimchi").Exchange(context.Background(), Params{Input: "fixture-token", Client: stub}); err == nil {
		t.Fatal("stored an unvalidated kimchi token")
	}
}

// TestGitlabPATRequiresVerification proves a PAT that the host rejects is not
// stored.
func TestGitlabPATRequiresVerification(t *testing.T) {
	stub := &stubClient{handler: func(*http.Request) (int, string) {
		return http.StatusUnauthorized, `{}`
	}}
	if _, err := flowFor(t, "gitlab").Import(context.Background(), Params{Input: "fixture-pat", Client: stub}); err == nil {
		t.Fatal("stored an unverified GitLab PAT")
	}
	verified := &stubClient{handler: func(*http.Request) (int, string) {
		return http.StatusOK, `{"username":"fixture","email":"fixture@example.invalid","name":"Fixture"}`
	}}
	result, err := flowFor(t, "gitlab").Import(context.Background(), Params{Input: "fixture-pat", Client: verified})
	if err != nil {
		t.Fatal(err)
	}
	if result.Secret.ProviderData["authKind"] != "personal_access_token" {
		t.Fatalf("provider data=%v", result.Secret.ProviderData)
	}
}

// TestZedCallbackDecrypts proves the RSA native-app handshake round-trips.
func TestZedCallbackDecrypts(t *testing.T) {
	privateKey, publicKey := rsaKeyPair(t)
	payload := encryptZedToken(t, publicKey, "fixture-zed-token")
	stub := &stubClient{handler: func(*http.Request) (int, string) {
		return http.StatusOK, `{"email":"fixture@example.invalid","name":"Fixture"}`
	}}
	result, err := flowFor(t, "zed").Exchange(context.Background(), Params{
		Input:  "http://127.0.0.1:58443/?user_id=fixture-user&access_token=" + url.QueryEscape(payload),
		Client: stub,
		Meta:   map[string]string{"privateKey": privateKey, "systemId": "fixture-system"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Secret.AccessToken != "fixture-zed-token" {
		t.Fatalf("secret=%+v", result.Secret)
	}
	if result.Secret.ProviderData["userId"] != "fixture-user" {
		t.Fatalf("provider data=%v", result.Secret.ProviderData)
	}
}

// TestXiaomiCallbackDecrypts proves the ECDH/AES-GCM handshake round-trips.
func TestXiaomiCallbackDecrypts(t *testing.T) {
	publicKey, privateKey, err := newX25519KeyPair()
	if err != nil {
		t.Fatal(err)
	}
	payload := encryptXiaomiPayload(t, publicKey, `{"uid":"fixture-uid","sk":"fixture-sk","url":"https://api.xiaomimimo.com/v1"}`)
	result, err := flowFor(t, "xiaomi-mimo").Exchange(context.Background(), Params{
		Input:  "http://localhost:1234/?u=" + url.QueryEscape(payload),
		Client: &stubClient{},
		Meta:   map[string]string{"privateKey": privateKey},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.Secret.AccessToken != "fixture-sk" {
		t.Fatalf("secret=%+v", result.Secret)
	}
	if result.Identity != "fixture-uid@xiaomi" {
		t.Fatalf("identity=%q", result.Identity)
	}
}

// TestManualFlowsCoverReferenceRoutes proves every reference onboarding route that
// is an auth mode has a manual flow.
func TestManualFlowsCoverReferenceRoutes(t *testing.T) {
	flows := ManualFlows()
	for provider, route := range map[string]string{
		"codex":       "codex/import-token",
		"grok-cli":    "grok-cli/bulk-import",
		"iflow":       "iflow/cookie",
		"kiro":        "kiro/api-key",
		"xiaomi-mimo": "xiaomi-mimo/api-key",
		"cursor":      "cursor/import",
	} {
		flow, ok := flows[provider]
		if !ok {
			t.Errorf("reference route %s has no manual flow for %q", route, provider)
			continue
		}
		if flow.Import == nil {
			t.Errorf("%s manual flow has no Import step", provider)
		}
	}
}

// TestRedirectURIsMatchRegisteredValues pins the providers whose client
// registration fixes the callback.
func TestRedirectURIsMatchRegisteredValues(t *testing.T) {
	registry := NewRegistry()
	codex, _ := registry.Lookup("codex")
	if got, err := DefaultRedirectURI(codex, "https://dashboard.invalid"); err != nil || got != "http://localhost:1455/auth/callback" {
		t.Fatalf("codex redirect=%q err=%v", got, err)
	}
	xai, _ := registry.Lookup("xai")
	if got, err := DefaultRedirectURI(xai, "https://dashboard.invalid"); err != nil || got != "http://127.0.0.1:56121/callback" {
		t.Fatalf("xai redirect=%q err=%v", got, err)
	}
	claude, _ := registry.Lookup("claude")
	if got, err := DefaultRedirectURI(claude, "https://dashboard.invalid"); err != nil || got != "https://dashboard.invalid/callback" {
		t.Fatalf("claude redirect=%q err=%v", got, err)
	}
	// An unset origin is an error, never a guessed loopback default: the provider
	// would send the browser somewhere the operator's dashboard does not answer.
	if _, err := DefaultRedirectURI(claude, ""); err == nil {
		t.Fatal("an unset redirect origin was accepted")
	}
}

// TestRefreshCoversReferenceTable proves every provider the reference can refresh
// resolves a refresher, and that a long-lived credential does not.
func TestRefreshCoversReferenceTable(t *testing.T) {
	resolve, err := RefreshResolver(&stubClient{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, provider := range []string{
		"claude", "codex", "xai", "grok-cli", "gemini-cli", "antigravity", "iflow",
		"github", "kimi", "cline", "clinepass", "kiro", "codebuddy-cn", "codebuddy-intl", "trae",
	} {
		connection := credentials.Connection{
			ProviderID: provider,
			AuthKind:   credentials.AuthOAuth,
			Secret:     credentials.Secret{RefreshToken: "fixture-refresh"},
		}
		if resolve(connection) == nil {
			t.Errorf("%s has a refresh grant in the reference but resolves none", provider)
		}
	}
	// Long-lived credentials must not pretend to rotate.
	for _, provider := range []string{"cursor", "kilocode", "zed", "windsurf", "openai", "deepseek"} {
		connection := credentials.Connection{
			ProviderID: provider,
			AuthKind:   credentials.AuthOAuth,
			Secret:     credentials.Secret{RefreshToken: "fixture-refresh"},
		}
		if resolve(connection) != nil {
			t.Errorf("%s has no refresh grant but resolved a refresher", provider)
		}
	}
}

// TestRefreshKeepsRotatedRefreshToken proves a rotation is carried forward: losing
// it would revoke the session on the next refresh.
func TestRefreshKeepsRotatedRefreshToken(t *testing.T) {
	stub := &stubClient{handler: func(*http.Request) (int, string) {
		return http.StatusOK, `{"access_token":"fixture-new-access","refresh_token":"fixture-new-refresh","expires_in":3600}`
	}}
	refresher := mustResolver(t, stub)(credentials.Connection{
		ProviderID: "claude", AuthKind: credentials.AuthOAuth,
		Secret: credentials.Secret{RefreshToken: "fixture-old-refresh"},
	})
	next, err := refresher(context.Background(), credentials.Secret{
		RefreshToken: "fixture-old-refresh", IDToken: "fixture-id", ProviderData: map[string]string{"keep": "me"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if next.AccessToken != "fixture-new-access" || next.RefreshToken != "fixture-new-refresh" {
		t.Fatalf("secret=%+v", next)
	}
	if next.IDToken != "fixture-id" || next.ProviderData["keep"] != "me" {
		t.Fatalf("refresh dropped unrelated credential fields: %+v", next)
	}
	if next.Expiry.IsZero() {
		t.Fatal("refresh dropped the expiry, so the next refresh would never be proactive")
	}
}

// TestRefreshWithoutRotationKeepsOldToken proves a provider that omits a new
// refresh token does not lose the existing one.
func TestRefreshWithoutRotationKeepsOldToken(t *testing.T) {
	stub := &stubClient{handler: func(*http.Request) (int, string) {
		return http.StatusOK, `{"access_token":"fixture-new-access"}`
	}}
	refresher := mustResolver(t, stub)(credentials.Connection{
		ProviderID: "xai", AuthKind: credentials.AuthOAuth,
		Secret: credentials.Secret{RefreshToken: "fixture-old-refresh"},
	})
	next, err := refresher(context.Background(), credentials.Secret{RefreshToken: "fixture-old-refresh"})
	if err != nil {
		t.Fatal(err)
	}
	if next.RefreshToken != "fixture-old-refresh" {
		t.Fatalf("refresh token was dropped: %+v", next)
	}
}

// TestRefreshRejectionIsAnError proves a rejected refresh surfaces as an error so
// the caller can ask the operator to reauthorize, instead of silently reusing a
// dead token.
func TestRefreshRejectionIsAnError(t *testing.T) {
	stub := &stubClient{handler: func(*http.Request) (int, string) {
		return http.StatusBadRequest, `{"error":"invalid_grant"}`
	}}
	refresher := mustResolver(t, stub)(credentials.Connection{
		ProviderID: "codex", AuthKind: credentials.AuthOAuth,
		Secret: credentials.Secret{RefreshToken: "fixture-dead"},
	})
	if _, err := refresher(context.Background(), credentials.Secret{RefreshToken: "fixture-dead"}); err == nil {
		t.Fatal("a rejected refresh returned no error")
	}
}

// TestKiroRefreshUsesStoredClientCredentials proves the AWS SSO client identity
// captured at sign-in is what refresh uses, and an unsafe region is refused.
func TestKiroRefreshUsesStoredClientCredentials(t *testing.T) {
	stub := &stubClient{handler: func(*http.Request) (int, string) {
		return http.StatusOK, `{"accessToken":"fixture-access","refreshToken":"fixture-refresh","expiresIn":3600}`
	}}
	connection := credentials.Connection{
		ProviderID: "kiro", AuthKind: credentials.AuthOAuth,
		Secret: credentials.Secret{
			RefreshToken: "fixture-refresh",
			ProviderData: map[string]string{"clientId": "fixture-client", "clientSecret": "fixture-secret", "authMethod": "idc", "region": "eu-west-1"},
		},
	}
	next, err := mustResolver(t, stub)(connection)(context.Background(), connection.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if next.AccessToken != "fixture-access" {
		t.Fatalf("secret=%+v", next)
	}
	if !strings.Contains(stub.last().URL.String(), "oidc.eu-west-1.amazonaws.com") {
		t.Fatalf("refresh used %q", stub.last().URL.String())
	}

	connection.Secret.ProviderData["region"] = "evil.example.com/"
	if _, err := mustResolver(t, stub)(connection)(context.Background(), connection.Secret); err == nil {
		t.Fatal("accepted an unsafe AWS region on refresh")
	}
}

// TestGitHubRefreshRemintsCopilotToken proves the Copilot token, which inference
// actually sends, is refreshed even when GitHub issues no new access token.
func TestGitHubRefreshRemintsCopilotToken(t *testing.T) {
	stub := &stubClient{handler: func(request *http.Request) (int, string) {
		if strings.Contains(request.URL.Path, "copilot_internal") {
			return http.StatusOK, `{"token":"fixture-copilot","expires_at":1761000000}`
		}
		return http.StatusOK, `{}`
	}}
	connection := credentials.Connection{
		ProviderID: "github", AuthKind: credentials.AuthOAuth,
		Secret: credentials.Secret{AccessToken: "fixture-github"},
	}
	next, err := mustResolver(t, stub)(connection)(context.Background(), connection.Secret)
	if err != nil {
		t.Fatal(err)
	}
	if next.ProviderData["copilotToken"] != "fixture-copilot" || next.ProviderData["copilotTokenExpiresAt"] != "1761000000" {
		t.Fatal("Copilot renewal lost its own token/expiry")
	}
	if !next.Expiry.IsZero() {
		t.Fatal("Copilot renewal overwrote primary GitHub expiry")
	}
}

// TestRedirectSourceMatchesWhatTheFlowConsumes is the cross-check the endpoint test
// cannot be: it verifies the declaration against the flow's actual behaviour, so a
// wrong declaration fails instead of being self-consistent.
//
// Each startable flow is given a sentinel redirect URI. A flow declared
// RedirectFromOperator must put it in the sign-in URL; a flow declared
// RedirectPinned or RedirectUnused must not — it hardcodes its own or uses none,
// and the handler must therefore not demand an origin for it.
func TestRedirectSourceMatchesWhatTheFlowConsumes(t *testing.T) {
	const sentinel = "https://sentinel.example.test/callback"
	// Providers whose OAuth client the operator must supply (SPEC 19.1) and
	// GitLab's self-hosted application id: the same input production requires.
	t.Setenv(GoogleClientIDEnv, "fixture-client-id")
	t.Setenv(GoogleClientSecretEnv, "fixture-client-secret")
	t.Setenv(IflowClientIDEnv, "fixture-iflow-client-id")
	t.Setenv(IflowClientSecretEnv, "fixture-iflow-client-secret")
	meta := map[string]string{"clientId": "fixture-gitlab-client"}
	for _, flow := range builtinFlows() {
		flow := flow
		if flow.Authorize == nil {
			continue
		}
		t.Run(flow.Provider, func(t *testing.T) {
			request, err := flow.Authorize(context.Background(), Params{
				RedirectURI: sentinel,
				Verifier:    "fixture-verifier",
				State:       "fixture-state",
				Meta:        meta,
				// A generic upstream: the handshakes that need a shaped reply
				// (codebuddy's state, trae's guidance, kiro's registration) get one.
				Client: &stubClient{handler: func(request *http.Request) (int, string) {
					url := request.URL.String()
					switch {
					case strings.Contains(url, "plugin/auth/state"):
						return http.StatusOK, `{"code":0,"data":{"state":"s","authUrl":"https://example.invalid/auth"}}`
					case strings.Contains(url, "GetLoginGuidance"):
						return http.StatusOK, `{"Result":{"LoginHost":"https://login.example.invalid"}}`
					case strings.Contains(url, "client/register"):
						return http.StatusOK, `{"clientId":"c","clientSecret":"s"}`
					case strings.Contains(url, "device_authorization"), strings.Contains(url, "device/code"):
						return http.StatusOK, `{"deviceCode":"d","device_code":"d","userCode":"U","verificationUri":"https://example.invalid","expiresIn":600,"interval":5}`
					}
					return http.StatusOK, "{}"
				}},
			})
			if err != nil {
				t.Fatalf("authorize: %v", err)
			}
			// A device flow returns no URL; it cannot consume a redirect.
			consumed := strings.Contains(request.URL, "sentinel.example.test")
			switch flow.RedirectSource {
			case RedirectFromOperator:
				if !consumed {
					t.Fatalf("%s is declared RedirectFromOperator but its sign-in URL carries no operator redirect: %s", flow.Provider, request.URL)
				}
			case RedirectPinned, RedirectUnused:
				if consumed {
					t.Fatalf("%s is declared %v but sends the operator redirect, which the handler would not resolve: %s",
						flow.Provider, flow.RedirectSource, request.URL)
				}
			}
			if flow.NeedsRedirectURI() != consumed {
				t.Fatalf("%s NeedsRedirectURI()=%v but the flow %s consume the redirect",
					flow.Provider, flow.NeedsRedirectURI(), map[bool]string{true: "does", false: "does not"}[consumed])
			}
		})
	}
}

// TestMetaKeysMatchWhatTheFlowReads enforces one invariant for every flow: the set
// of keys a flow reads from Params.Meta is a subset of the MetaKeys it declares.
//
// The dashboard renders exactly the declared keys, so an undeclared read is a field
// the operator cannot fill — gitlab's clientSecret was exactly that, reachable only
// through the raw API. Reads are extracted from the flow source rather than listed
// here, so a new read cannot slip past the check the way a hardcoded provider list
// let this one.
func TestMetaKeysMatchWhatTheFlowReads(t *testing.T) {
	// Flow-private keys a flow mints itself and carries in AuthRequest.Extra. They
	// are never operator input, so they are not declared as fields.
	// Globally flow-private: minted by the flow and carried in AuthRequest.Extra.
	private := map[string]bool{"nonce": true, "privateKey": true, "deviceId": true, "systemId": true}
	// Per-provider flow-private keys: kiro's client credentials are minted by its
	// own AWS SSO registration, while gitlab's are operator input for a confidential
	// OAuth application. Privacy is a property of the flow, not of the key name.
	privateByProvider := map[string]map[string]bool{
		"kiro":   {"clientId": true, "clientSecret": true},
		"cursor": {"accessToken": true},
		// Qoder mints its machine id during the device handshake.
		"qoder": {"machineId": true},
	}

	reads := metaReadsByProvider(t)
	for _, flow := range builtinFlows() {
		flow := flow
		t.Run(flow.Provider, func(t *testing.T) {
			declared := make(map[string]bool, len(flow.MetaKeys))
			for _, key := range flow.MetaKeys {
				declared[key] = true
				if private[key] || privateByProvider[flow.Provider][key] {
					t.Errorf("%s declares flow-private key %q as operator input", flow.Provider, key)
				}
			}
			own := privateByProvider[flow.Provider]
			for key := range reads[flow.Provider] {
				if private[key] || own[key] || declared[key] {
					continue
				}
				t.Errorf("%s reads Meta[%q] but does not declare it, so the dashboard cannot offer the field", flow.Provider, key)
			}
		})
	}
}

// metaReadsByProvider extracts every Meta key each provider's source reads. The
// provider is the function that builds the flow (claudeFlow, gitlabFlow, …), which
// is how the source groups the reads.
func metaReadsByProvider(t *testing.T) map[string]map[string]bool {
	t.Helper()
	dir := filepath.Join(repoRoot(t), "internal", "providers", "onboarding")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	readPattern := regexp.MustCompile(`p\.Meta\["([a-zA-Z]+)"\]`)
	fallbackPattern := regexp.MustCompile(`metaOr\(p, "([a-zA-Z]+)"`)
	funcPattern := regexp.MustCompile(`(?m)^func (\w+)Flow\(`)
	result := make(map[string]map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".go") || strings.HasSuffix(entry.Name(), "_test.go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		bounds := funcPattern.FindAllStringSubmatchIndex(source, -1)
		for i, match := range bounds {
			name := source[match[2]:match[3]]
			end := len(source)
			if i+1 < len(bounds) {
				end = bounds[i+1][0]
			}
			body := source[match[0]:end]
			keys := make(map[string]bool)
			for _, m := range readPattern.FindAllStringSubmatch(body, -1) {
				keys[m[1]] = true
			}
			for _, m := range fallbackPattern.FindAllStringSubmatch(body, -1) {
				keys[m[1]] = true
			}
			if len(keys) > 0 {
				result[name] = keys
			}
		}
	}
	return result
}

// TestFlowAuthKindIsPermittedByItsProviderSpec is the check that would have caught
// iflow: its cookie import returns AuthCookie while the provider declared only
// AuthOAuth, so /import stored a connection the PATCH path then refused.
//
// Every flow's declared credential mode must be one its provider spec permits, for
// all flows, not one example. The provider spec is the contract both the manual form
// and the import path validate against.
func TestFlowAuthKindIsPermittedByItsProviderSpec(t *testing.T) {
	specs := make(map[string]registry.Spec)
	for _, spec := range registry.Builtins() {
		specs[spec.ID] = spec
	}
	permitted := func(spec registry.Spec, kind credentials.AuthKind) bool {
		allowed := spec.AuthModes
		if len(allowed) == 0 {
			allowed = []registry.AuthKind{spec.Auth}
		}
		for _, item := range allowed {
			if string(item) == string(kind) {
				return true
			}
		}
		return false
	}
	// The mode a flow produces, declared next to the flow so it can be checked
	// without performing a sign-in.
	for _, flow := range builtinFlows() {
		flow := flow
		if flow.AuthKind == "" {
			continue
		}
		t.Run(flow.Provider, func(t *testing.T) {
			spec, ok := specs[flow.Provider]
			if !ok {
				// A hidden provider has no spec to check against.
				if !HiddenProviders()[flow.Provider] {
					t.Fatalf("%q has no provider spec", flow.Provider)
				}
				return
			}
			if !permitted(spec, flow.AuthKind) {
				t.Fatalf("%s produces %q but its spec permits %v; declare the mode or change the flow",
					flow.Provider, flow.AuthKind, spec.AuthModes)
			}
		})
	}
	// Import flows are a separate table and produce their own mode.
	for provider, flow := range ManualFlows() {
		provider, flow := provider, flow
		if flow.AuthKind == "" {
			continue
		}
		t.Run("import/"+provider, func(t *testing.T) {
			spec, ok := specs[provider]
			if !ok {
				t.Fatalf("%q has no provider spec", provider)
			}
			if !permitted(spec, flow.AuthKind) {
				t.Fatalf("%s import produces %q but its spec permits %v; declare the mode or change the flow",
					provider, flow.AuthKind, spec.AuthModes)
			}
		})
	}
}

// TestResolvesIdentityFlowsReturnOne keeps the flag honest. The flag tells the
// dashboard not to ask for an identity, so a flow that sets it must resolve one
// from the provider or the credential; otherwise the connection is stored with an
// empty identity and dedup has no key.
func TestResolvesIdentityFlowsReturnOne(t *testing.T) {
	// The imports that declare the flag, with a credential that resolves an identity
	// without a live provider.
	cases := map[string]Params{
		"iflow": {Input: "BXAuth=fixture"},
		"cursor": {
			Input: fixtureJWT(t, `{"email":"resolved@example.invalid"}`),
			Meta:  map[string]string{"machineId": "fixture-machine"},
		},
		"gitlab": {Input: "fixture-pat", Meta: map[string]string{"baseUrl": "https://gitlab.example.invalid"}},
	}
	// The providers answer with a resolved identity; the point is that the flow
	// reads it rather than trusting the operator.
	upstream := &stubClient{handler: func(request *http.Request) (int, string) {
		switch {
		case strings.Contains(request.URL.Path, "/user"):
			return http.StatusOK, `{"username":"resolved","email":"resolved@example.invalid"}`
		case strings.Contains(request.URL.Host, "iflow"):
			return http.StatusOK, `{"success":true,"data":{"apiKey":"fixture-key","name":"fixture","email":"resolved@example.invalid"}}`
		}
		return http.StatusOK, `{"email":"resolved@example.invalid"}`
	}}
	checked := 0
	// A provider's import flow can live in either table (gitlab's is the browser
	// flow's PAT import), so both are consulted, as the endpoint does.
	all := make(map[string]Flow, len(ManualFlows()))
	for provider, flow := range ManualFlows() {
		all[provider] = flow
	}
	registry := NewRegistry()
	for _, provider := range registry.Providers() {
		if flow, ok := registry.Lookup(provider); ok {
			// The manual entry owns Import; the browser flow only supplies Bulk, the
			// same merge importFlowFor performs.
			if existing, exists := all[provider]; exists {
				existing.Bulk = flow.Bulk
				all[provider] = existing
				continue
			}
			all[provider] = flow
		}
	}
	for provider, params := range cases {
		flow, ok := all[provider]
		if !ok {
			t.Fatalf("%s has no import flow", provider)
		}
		if !flow.ResolvesIdentity {
			t.Fatalf("%s does not declare ResolvesIdentity but is listed here", provider)
		}
		if flow.Import == nil {
			t.Fatalf("%s declares ResolvesIdentity but has no Import", provider)
		}
		params := params
		params.Client = upstream
		result, err := flow.Import(context.Background(), params)
		if err != nil {
			t.Fatalf("%s declares ResolvesIdentity but its import failed: %v", provider, err)
		}
		if strings.TrimSpace(result.Identity) == "" {
			t.Fatalf("%s declares ResolvesIdentity but returned no identity", provider)
		}
		checked++
	}
	// Every flow that declares the flag must be exercised above, so a new one
	// cannot claim the capability without a test.
	for provider, flow := range ManualFlows() {
		if flow.ResolvesIdentity {
			if _, ok := cases[provider]; !ok {
				t.Errorf("%s declares ResolvesIdentity but has no case here", provider)
			}
		}
	}
	for _, flow := range builtinFlows() {
		if flow.ResolvesIdentity {
			if _, ok := cases[flow.Provider]; !ok {
				t.Errorf("%s declares ResolvesIdentity but has no case here", flow.Provider)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no flow was checked")
	}
}

// fixtureJWT builds an unsigned token whose payload is the supplied claims. The
// flows decode claims without verifying, because the token was just received over
// TLS from the provider.
func fixtureJWT(t *testing.T, claims string) string {
	t.Helper()
	encode := func(raw []byte) string { return base64.RawURLEncoding.EncodeToString(raw) }
	return encode([]byte(`{"alg":"none"}`)) + "." + encode([]byte(claims)) + ".signature"
}

// mustResolver installs the refresh resolver for a test, failing on a wiring error
// the same way the app does at startup.
func mustResolver(t *testing.T, client HTTPClient) func(credentials.Connection) Refresher {
	t.Helper()
	resolve, err := RefreshResolver(client, nil)
	if err != nil {
		t.Fatal(err)
	}
	return resolve
}

func TestGitHubMissingExpiryBoundsSequentialMints(t *testing.T) {
	for _, response := range []struct{ name, body string }{
		{"absent", `{"token":"fixture-copilot"}`},
		{"unparseable", `{"token":"fixture-copilot","expires_at":"invalid"}`},
		{"null", `{"token":"fixture-copilot","expires_at":null}`},
		{"zero", `{"token":"fixture-copilot","expires_at":0}`},
		{"numeric prefix", `{"token":"fixture-copilot","expires_at":"123garbage"}`},
	} {
		for _, legacy := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/legacy=%v", response.name, legacy), func(t *testing.T) {
				ctx := context.Background()
				now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
				clock := func() time.Time { return now }
				mints := 0
				client := &stubClient{handler: func(r *http.Request) (int, string) {
					switch r.URL.String() {
					case "https://github.com/login/oauth/access_token":
						return 200, `{"access_token":"fixture-github","token_type":"bearer","scope":"read:user"}`
					case "https://api.github.com/user":
						return 200, `{"login":"fixture-user","id":1}`
					case "https://api.github.com/copilot_internal/v2/token":
						mints++
						return 200, response.body
					default:
						t.Fatalf("unexpected request %s", r.URL)
						return 500, `{}`
					}
				}}
				outcome, err := flowFor(t, "github").Poll(ctx, Params{Input: "fixture-device", Client: client, Now: clock})
				if err != nil || outcome.Err != nil || !outcome.Done {
					t.Fatalf("onboarding: %v, %+v", err, outcome)
				}
				secret := outcome.Result.Secret
				if !secret.Expiry.IsZero() {
					t.Fatal("GitHub OAuth response has no expires_in")
				}
				assertExpiry := func(s credentials.Secret) {
					t.Helper()
					seconds, err := strconv.ParseInt(s.ProviderData["copilotTokenExpiresAt"], 10, 64)
					if err != nil || seconds != now.Add(10*credentials.RefreshLead).Unix() || !s.Expiry.IsZero() {
						t.Fatalf("incorrect token expiries: named=%q primary=%v", s.ProviderData["copilotTokenExpiresAt"], s.Expiry)
					}
				}
				assertExpiry(secret)
				if legacy {
					delete(secret.ProviderData, "copilotTokenExpiresAt")
				}
				connection := credentials.Connection{ID: "fixture", ProviderID: "github", AuthKind: outcome.Result.AuthKind, Secret: secret}
				r := credentials.NewRegistry(nil, clock)
				r.SetCredentialNameResolver(func(credentials.Connection) (string, string) { return "copilotToken", "copilotTokenExpiresAt" })
				r.Register(connection, credentials.Exchanger(refreshFor(connection, client, clock)))
				serve := func(wantMints int) {
					t.Helper()
					for i := 0; i < 20; i++ {
						token, err := r.Credential(ctx, connection.ID)
						if err != nil || token != "fixture-copilot" {
							t.Fatalf("request %d: token selection failed: %v", i, err)
						}
					}
					if mints != wantMints {
						t.Fatalf("20 sequential requests: mints=%d want=%d", mints, wantMints)
					}
					s, err := r.Secret(ctx, connection.ID)
					if err != nil || s.AccessToken != "fixture-github" {
						t.Fatalf("primary token: %v", err)
					}
					assertExpiry(s)
				}
				want := 1
				if legacy {
					want++
				}
				serve(want)
				now = now.Add(9 * time.Minute)
				serve(want + 1)
			})
		}
	}
}
