package onboarding

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

// Fixtures below are the shapes of the pin's own payloads with invented values; no
// field here is a real credential.
const (
	fixtureProfileARN = "arn:aws:codewhisperer:us-east-1:000000000000:profile/FIXTURE"
	// Cursor: >= 50 characters and a UUID-shaped machine id (services/cursor.js:105-114).
	fixtureCursorLongToken = "fixture-cursor-token-0123456789-0123456789-0123456789"
	fixtureCursorMachine   = "0123abcd-0123-4abc-8def-0123456789ab"
)

func kiroImportFlow(t *testing.T) Flow {
	t.Helper()
	flow, ok := ManualFlows()["kiro"]
	if !ok || flow.Import == nil {
		t.Fatal("no kiro import flow")
	}
	return flow
}

// TestKiroRefreshPicksTheEndpointFromTheCredentialShape is the refresh branch table
// (tokenRefresh/providers.js:334-455). Before, a credential without a registered
// AWS client was refused outright, so a social or Microsoft login worked until its
// first expiry and then needed reauthorizing.
func TestKiroRefreshPicksTheEndpointFromTheCredentialShape(t *testing.T) {
	cases := []struct {
		name     string
		data     map[string]string
		response string
		wantURL  string
		wantBody []string
	}{
		{
			name:     "social",
			data:     map[string]string{"authMethod": "google"},
			response: `{"accessToken":"fixture-new-access","refreshToken":"fixture-new-refresh","expiresIn":1800,"profileArn":"` + fixtureProfileARN + `"}`,
			wantURL:  "https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken",
			wantBody: []string{`"refreshToken":"fixture-old-refresh"`},
		},
		{
			name:     "builder id outside idc stays in us-east-1",
			data:     map[string]string{"authMethod": "builder-id", "clientId": "c", "clientSecret": "s", "region": "eu-west-1"},
			response: `{"accessToken":"fixture-new-access","refreshToken":"fixture-new-refresh","expiresIn":1800}`,
			wantURL:  "https://oidc.us-east-1.amazonaws.com/token",
			wantBody: []string{`"grantType":"refresh_token"`, `"clientId":"c"`},
		},
		{
			name:     "idc uses its region",
			data:     map[string]string{"authMethod": "idc", "clientId": "c", "clientSecret": "s", "region": "eu-west-1"},
			response: `{"accessToken":"fixture-new-access","refreshToken":"fixture-new-refresh","expiresIn":1800}`,
			wantURL:  "https://oidc.eu-west-1.amazonaws.com/token",
		},
		{
			name: "external idp",
			data: map[string]string{
				"authMethod": "external_idp", "clientId": "fixture-client",
				"tokenEndpoint": "https://login.microsoftonline.com/fixture-tenant/oauth2/v2.0/token", "scope": "openid offline_access",
			},
			response: `{"access_token":"fixture-new-access","refresh_token":"fixture-new-refresh","expires_in":1800}`,
			wantURL:  "https://login.microsoftonline.com/fixture-tenant/oauth2/v2.0/token",
			wantBody: []string{"grant_type=refresh_token", "client_id=fixture-client", "refresh_token=fixture-old-refresh", "scope=openid+offline_access"},
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubClient{handler: func(*http.Request) (int, string) { return http.StatusOK, tc.response }}
			connection := credentials.Connection{
				ProviderID: "kiro", AuthKind: credentials.AuthOAuth,
				Secret: credentials.Secret{RefreshToken: "fixture-old-refresh", ProviderData: tc.data},
			}
			now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
			resolve, err := RefreshResolver(stub, func() time.Time { return now })
			if err != nil {
				t.Fatal(err)
			}
			next, err := resolve(connection)(context.Background(), connection.Secret)
			if err != nil {
				t.Fatal(err)
			}
			if got := stub.last().URL.String(); got != tc.wantURL {
				t.Fatalf("refresh went to %q, want %q", got, tc.wantURL)
			}
			for _, want := range tc.wantBody {
				if !strings.Contains(stub.bodies[0], want) {
					t.Fatalf("body %q lacks %q", stub.bodies[0], want)
				}
			}
			if next.AccessToken != "fixture-new-access" || next.RefreshToken != "fixture-new-refresh" {
				t.Fatalf("rotation not carried: %+v", next)
			}
			if !next.Expiry.Equal(now.Add(30 * time.Minute)) {
				t.Fatalf("expiry=%v, want the reported 1800s", next.Expiry)
			}
			for key, value := range tc.data {
				if tc.name == "external idp" && key == "tokenEndpoint" {
					continue
				}
				if next.ProviderData[key] != value {
					t.Fatalf("refresh dropped %s: %+v", key, next.ProviderData)
				}
			}
		})
	}
}

func TestKiroSocialRefreshDefaultsToOneHourAndFillsAMissingProfile(t *testing.T) {
	stub := &stubClient{handler: func(*http.Request) (int, string) {
		return http.StatusOK, `{"accessToken":"fixture-new-access","profileArn":"` + fixtureProfileARN + `"}`
	}}
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	secret := credentials.Secret{RefreshToken: "fixture-old-refresh", ProviderData: map[string]string{"authMethod": "github"}}
	next, err := refreshKiro(context.Background(), stub, func() time.Time { return now }, secret, secret.ProviderData)
	if err != nil {
		t.Fatal(err)
	}
	if !next.Expiry.Equal(now.Add(time.Hour)) || next.RefreshToken != "fixture-old-refresh" {
		t.Fatalf("secret=%+v", next)
	}
	if next.ProviderData["profileArn"] != fixtureProfileARN || next.ProviderData["authMethod"] != "github" {
		t.Fatalf("provider data=%+v", next.ProviderData)
	}
	if got := stub.last().Header.Get("User-Agent"); got != "kiro-cli/1.0.0" {
		t.Fatalf("user agent=%q", got)
	}

	// A profile the connection already has is never replaced by a reported one.
	secret.ProviderData["profileArn"] = "arn:aws:codewhisperer:us-east-1:111111111111:profile/KEEP"
	next, err = refreshKiro(context.Background(), stub, func() time.Time { return now }, secret, secret.ProviderData)
	if err != nil || !strings.HasSuffix(next.ProviderData["profileArn"], "/KEEP") {
		t.Fatalf("stored profile replaced: %+v err=%v", next.ProviderData, err)
	}
}

func TestKiroExternalIdpRefreshRefusesNonMicrosoftEndpoints(t *testing.T) {
	for _, endpoint := range []string{
		"http://login.microsoftonline.com/t/token",
		"https://evil.example.test/token",
		"https://login.microsoftonline.com.evil.example.test/token",
		"https://user:pass@login.microsoftonline.com/token",
		"https://login.microsoftonline.com:8443/token",
		"",
	} {
		stub := &stubClient{}
		secret := credentials.Secret{RefreshToken: "fixture-refresh", ProviderData: map[string]string{
			"authMethod": "external_idp", "clientId": "c", "scope": "s", "tokenEndpoint": endpoint,
		}}
		if _, err := refreshKiro(context.Background(), stub, time.Now, secret, secret.ProviderData); err == nil {
			t.Errorf("endpoint %q was accepted", endpoint)
		}
		if len(stub.requests) != 0 {
			t.Errorf("endpoint %q was contacted: the refresh token would have left the host", endpoint)
		}
	}
}

// TestKiroTokenImportProvesTheTokenByRefreshing mirrors kiro/import/route.js.
func TestKiroTokenImportProvesTheTokenByRefreshing(t *testing.T) {
	flow := kiroImportFlow(t)
	jwt := fixtureJWT(t, `{"email":"person@example.invalid"}`)
	social := &stubClient{handler: func(*http.Request) (int, string) {
		return http.StatusOK, `{"accessToken":"` + jwt + `","refreshToken":"fixture-rotated","expiresIn":900,"profileArn":"` + fixtureProfileARN + `"}`
	}}
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	result, err := flow.Import(context.Background(), Params{
		Input: `{"refreshToken":"fixture-pasted"}`, Client: social, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(social.last().URL.Path, "/refreshToken") || !strings.Contains(social.bodies[0], "fixture-pasted") {
		t.Fatalf("social import did not refresh with the pasted token: %s %s", social.last().URL, social.bodies[0])
	}
	secret := result.Secret
	if secret.RefreshToken != "fixture-rotated" || secret.AccessToken != jwt || !secret.Expiry.Equal(now.Add(15*time.Minute)) {
		t.Fatalf("secret=%+v", secret)
	}
	if result.Identity != "person@example.invalid" || result.AuthKind != credentials.AuthOAuth {
		t.Fatalf("result=%+v", result)
	}
	if secret.ProviderData["authMethod"] != "imported" || secret.ProviderData["profileArn"] != fixtureProfileARN {
		t.Fatalf("provider data=%+v", secret.ProviderData)
	}

	// An organization (IDC) token carries its client registration and refreshes in
	// its own region; the registration is stored so the next rotation can.
	idc := &stubClient{handler: func(*http.Request) (int, string) {
		return http.StatusOK, `{"accessToken":"fixture-access","refreshToken":"fixture-rotated","expiresIn":900}`
	}}
	result, err = flow.Import(context.Background(), Params{
		Input:  `{"refreshToken":"fixture-pasted","clientId":"fixture-client","clientSecret":"fixture-secret","region":"eu-west-1","profileArn":"` + fixtureProfileARN + `"}`,
		Client: idc, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if idc.last().URL.Host != "oidc.eu-west-1.amazonaws.com" {
		t.Fatalf("idc import refreshed at %s", idc.last().URL.Host)
	}
	data := result.Secret.ProviderData
	if data["authMethod"] != "idc" || data["clientId"] != "fixture-client" || data["clientSecret"] != "fixture-secret" || data["region"] != "eu-west-1" || data["profileArn"] != fixtureProfileARN {
		t.Fatalf("idc provider data=%+v", data)
	}

	// A rejected refresh stores nothing, and a missing token never reaches the network.
	rejected := &stubClient{handler: func(*http.Request) (int, string) { return http.StatusBadRequest, `{}` }}
	if _, err := flow.Import(context.Background(), Params{Input: `{"refreshToken":"fixture-dead"}`, Client: rejected}); err == nil {
		t.Fatal("an unrefreshable token was imported")
	}
	untouched := &stubClient{}
	if _, err := flow.Import(context.Background(), Params{Input: `{"clientId":"c"}`, Client: untouched}); err == nil || len(untouched.requests) != 0 {
		t.Fatalf("missing refresh token: err=%v requests=%d", err, len(untouched.requests))
	}
}

// cliProxyFixture is a CLIProxyAPI export for a Microsoft external_idp account
// (kiroExternalIdp.js:77-128).
func cliProxyFixture(t *testing.T, overrides string) string {
	t.Helper()
	jwt := fixtureJWT(t, `{"preferred_username":"person@example.invalid","exp":1893456000}`)
	return `{"auth_method":"external_idp","access_token":"` + jwt + `","refresh_token":"fixture-refresh","client_id":"fixture-client",` +
		`"token_endpoint":"https://login.microsoftonline.com/fixture-tenant/oauth2/v2.0/token","scopes":["openid"," offline_access",""],` +
		`"profile_arn":"` + fixtureProfileARN + `"` + overrides + `}`
}

func TestKiroCLIProxyImportNormalizesWithoutANetworkCall(t *testing.T) {
	flow := kiroImportFlow(t)
	stub := &stubClient{}
	result, err := flow.Import(context.Background(), Params{Input: cliProxyFixture(t, ""), Client: stub})
	if err != nil {
		t.Fatal(err)
	}
	if len(stub.requests) != 0 {
		t.Fatalf("the export already carries a token pair; %d requests were made", len(stub.requests))
	}
	data := result.Secret.ProviderData
	if data["authMethod"] != "external_idp" || data["scope"] != "openid offline_access" || data["clientId"] != "fixture-client" ||
		data["region"] != "us-east-1" || data["profileArn"] != fixtureProfileARN || !strings.HasPrefix(data["tokenEndpoint"], "https://login.microsoftonline.com/") {
		t.Fatalf("provider data=%+v", data)
	}
	if result.Identity != "person@example.invalid" {
		t.Fatalf("identity=%q", result.Identity)
	}
	// With no lifetime given the token's own exp claim is the expiry.
	if !result.Secret.Expiry.Equal(time.Unix(1893456000, 0)) {
		t.Fatalf("expiry=%v", result.Secret.Expiry)
	}
	// The imported credential refreshes through the external_idp branch.
	refresh := &stubClient{handler: func(*http.Request) (int, string) {
		return http.StatusOK, `{"access_token":"fixture-new","refresh_token":"fixture-new-refresh","expires_in":600}`
	}}
	connection := credentials.Connection{ProviderID: "kiro", AuthKind: credentials.AuthOAuth, Secret: result.Secret}
	next, err := mustResolver(t, refresh)(connection)(context.Background(), result.Secret)
	if err != nil || next.AccessToken != "fixture-new" {
		t.Fatalf("imported credential did not refresh: %+v err=%v", next, err)
	}
	if u, _ := url.Parse(refresh.last().URL.String()); u.Host != "login.microsoftonline.com" {
		t.Fatalf("refresh host=%s", u.Host)
	}
}

func TestKiroCLIProxyImportRefusesWhatTheReferenceRefuses(t *testing.T) {
	flow := kiroImportFlow(t)
	for name, overrides := range map[string]string{
		"other auth method":   `,"auth_method":"builder-id"`,
		"foreign endpoint":    `,"token_endpoint":"https://evil.example.test/token"`,
		"plain http endpoint": `,"token_endpoint":"http://login.microsoftonline.com/token"`,
		"no client id":        `,"client_id":""`,
		"no refresh token":    `,"refresh_token":""`,
		"no scopes":           `,"scopes":[]`,
		"no profile":          `,"profile_arn":""`,
	} {
		stub := &stubClient{}
		// A later duplicate key wins in encoding/json, which is what makes the
		// override a one-field mutation of the valid fixture.
		if _, err := flow.Import(context.Background(), Params{Input: cliProxyFixture(t, overrides), Client: stub}); err == nil {
			t.Errorf("%s: import succeeded", name)
		}
		if len(stub.requests) != 0 {
			t.Errorf("%s: made %d requests", name, len(stub.requests))
		}
	}
}

// TestKiroAPIKeyIsProvenAgainstTheCatalog mirrors services/kiro.js:299-350: a key is
// valid only if the Amazon Q catalog lists models for it.
func TestKiroAPIKeyIsProvenAgainstTheCatalog(t *testing.T) {
	flow := kiroImportFlow(t)
	ok := &stubClient{handler: func(*http.Request) (int, string) { return http.StatusOK, `{"models":[{"modelId":"fixture"}]}` }}
	result, err := flow.Import(context.Background(), Params{Input: "fixture-kiro-key", Meta: map[string]string{"region": "eu-central-1"}, Client: ok})
	if err != nil {
		t.Fatal(err)
	}
	request := ok.last()
	if request.Method != http.MethodGet || request.URL.Host != "q.eu-central-1.amazonaws.com" || request.URL.Path != "/ListAvailableModels" ||
		request.URL.Query().Get("origin") != "AI_EDITOR" || request.Header.Get("TokenType") != "API_KEY" ||
		request.Header.Get("Authorization") != "Bearer fixture-kiro-key" {
		t.Fatalf("catalog request %s %s %v", request.Method, request.URL, request.Header)
	}
	if result.AuthKind != credentials.AuthAPIKey || result.Secret.ProviderData["authMethod"] != "api_key" || result.Secret.ProviderData["region"] != "eu-central-1" {
		t.Fatalf("result=%+v", result)
	}
	for name, body := range map[string]string{"empty catalog": `{"models":[]}`, "no catalog": `{}`} {
		empty := &stubClient{handler: func(*http.Request) (int, string) { return http.StatusOK, body }}
		if _, err := flow.Import(context.Background(), Params{Input: "fixture-kiro-key", Client: empty}); err == nil {
			t.Errorf("%s: a key with no models was accepted", name)
		}
	}
}

// TestCursorImportValidatesShapeAndCallsNothing mirrors CursorService.validateImportToken.
func TestCursorImportValidatesShapeAndCallsNothing(t *testing.T) {
	flow := ManualFlows()["cursor"]
	stub := &stubClient{}
	jwt := fixtureJWT(t, `{"sub":"user_fixture","pad":"0123456789012345678901234567890123456789"}`)
	result, err := flow.Import(context.Background(), Params{Input: jwt, Meta: map[string]string{"machineId": fixtureCursorMachine}, Client: stub})
	if err != nil {
		t.Fatal(err)
	}
	if len(stub.requests) != 0 {
		t.Fatalf("cursor import made %d provider calls; the pin validates shape only", len(stub.requests))
	}
	if result.Identity != "user_fixture" || result.Secret.ProviderData["userId"] != "user_fixture" || result.Secret.ProviderData["machineId"] != fixtureCursorMachine {
		t.Fatalf("subject fallback: %+v", result)
	}
	if result.Secret.Expiry.IsZero() {
		t.Fatal("cursor tokens are assumed to last a day; no expiry stored")
	}
	for name, params := range map[string]Params{
		"short token":    {Input: "short", Meta: map[string]string{"machineId": fixtureCursorMachine}},
		"bad machine id": {Input: fixtureCursorLongToken, Meta: map[string]string{"machineId": "not-a-uuid"}},
	} {
		if _, err := flow.Import(context.Background(), params); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	if _, err := flow.Import(context.Background(), Params{Input: fixtureCursorLongToken}); err != ErrCursorMachineIDRequired {
		t.Fatalf("missing machine id: %v", err)
	}
}

// TestBulkImportKeepsAnUnknownExpiryUnknown pins the rule #173 left behind: an
// absent value reads as unknown, never as zero. now+0 marked every export without a
// lifetime as expired the moment it was imported.
func TestBulkImportKeepsAnUnknownExpiryUnknown(t *testing.T) {
	jwt := fixtureJWT(t, `{"email":"person@example.invalid","https://api.openai.com/auth":{"chatgpt_account_id":"acct-jwt","chatgpt_plan_type":"plus"}}`)
	for provider, bulk := range map[string]func(map[string]any) (BulkAccount, error){"codex": codexBulkAccount, "grok-cli": grokBulkAccount} {
		account, err := bulk(map[string]any{"accessToken": jwt, "refreshToken": "fixture-refresh", "idToken": jwt, "email": "person@example.invalid"})
		if err != nil {
			t.Fatal(provider, err)
		}
		if !account.Secret.Expiry.IsZero() {
			t.Errorf("%s: no lifetime was given but the expiry is %v", provider, account.Secret.Expiry)
		}
		if account.Secret.IDToken != jwt {
			t.Errorf("%s: the exported id token was dropped", provider)
		}
		dated, err := bulk(map[string]any{"accessToken": jwt, "expiresAt": "2030-01-02T03:04:05Z"})
		if err != nil || !dated.Secret.Expiry.Equal(time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)) {
			t.Errorf("%s: expiresAt not honored: %v %v", provider, dated.Secret.Expiry, err)
		}
		// The codex route reads camelCase only (bulk-import/route.js:72-99); the
		// grok-cli route takes either spelling.
		lifetime := map[string]any{"access_token": jwt, "expires_in": float64(600)}
		if provider == "codex" {
			lifetime = map[string]any{"accessToken": jwt, "expiresIn": float64(600)}
		}
		timed, err := bulk(lifetime)
		if err != nil || time.Until(timed.Secret.Expiry) < 9*time.Minute || time.Until(timed.Secret.Expiry) > 11*time.Minute {
			t.Errorf("%s: lifetime not honored: %v %v", provider, timed.Secret.Expiry, err)
		}
	}
}

func TestCodexBulkExportedAccountDataWinsOverTheJWT(t *testing.T) {
	jwt := fixtureJWT(t, `{"https://api.openai.com/auth":{"chatgpt_account_id":"acct-jwt","chatgpt_plan_type":"plus"}}`)
	account, err := codexBulkAccount(map[string]any{
		"accessToken":          jwt,
		"providerSpecificData": map[string]any{"chatgptAccountId": "acct-exported"},
	})
	if err != nil {
		t.Fatal(err)
	}
	data := account.Secret.ProviderData
	if data["chatgptAccountId"] != "acct-exported" || data["chatgptPlanType"] != "plus" {
		t.Fatalf("provider data=%+v", data)
	}
}

func TestXiaomiImportHoldsSealedAccountFields(t *testing.T) {
	flow := ManualFlows()["xiaomi-mimo"]
	models := &stubClient{handler: func(*http.Request) (int, string) { return http.StatusOK, `{"data":[{"id":"a"},{"id":"b"}]}` }}

	result, err := flow.Import(context.Background(), Params{
		Input:  `{"apiKey":"sk-fixture","uid":"1234","baseUrl":"https://mimo.example.test/v1/","mimoPassToken":"fixture-pass","mimoUserId":"fixture-user","mimoCUserId":"fixture-cuser"}`,
		Client: models,
	})
	if err != nil {
		t.Fatal(err)
	}
	data := result.Secret.ProviderData
	if result.Secret.AccessToken != "sk-fixture" || result.Identity != "1234@xiaomi" || data["baseUrl"] != "https://mimo.example.test/v1" || data["modelCount"] != "2" {
		t.Fatalf("result=%+v", result)
	}
	for key, want := range map[string]string{"mimoPassToken": "fixture-pass", "mimoUserId": "fixture-user", "mimoCUserId": "fixture-cuser"} {
		if data[key] != want {
			t.Fatalf("account field %s not retained", key)
		}
	}
	if got := models.last().Header.Get("X-Mimo-Source"); got != "mimocode-cli" || models.last().URL.String() != "https://mimo.example.test/v1/models" {
		t.Fatalf("validation request %s source=%q", models.last().URL, got)
	}

	authFile := &stubClient{handler: func(*http.Request) (int, string) { return http.StatusOK, `{"data":[]}` }}
	result, err = flow.Import(context.Background(), Params{
		Input:  `{"xiaomi":{"type":"api","key":"sk-from-file","metadata":{"uid":"99","base_url":"https://api.xiaomimimo.com/v1"}}}`,
		Client: authFile,
	})
	if err != nil || result.Secret.AccessToken != "sk-from-file" || result.Identity != "99@xiaomi" {
		t.Fatalf("auth.json import: %+v err=%v", result, err)
	}

	down := &stubClient{handler: func(*http.Request) (int, string) { return http.StatusServiceUnavailable, `{}` }}
	result, err = flow.Import(context.Background(), Params{Input: "sk-bare", Meta: map[string]string{"uid": "7"}, Client: down})
	if err != nil || result.Secret.AccessToken != "sk-bare" || result.Identity != "7@xiaomi" {
		t.Fatalf("bare key: %+v err=%v", result, err)
	}
	if count, has := result.Secret.ProviderData["modelCount"]; has {
		t.Fatalf("modelCount=%q recorded for a catalog that was never read", count)
	}

	for _, input := range []string{"not-a-key", `{"apiKey":"nope"}`, `{"xiaomi":{}}`, `{"broken`} {
		if _, err := flow.Import(context.Background(), Params{Input: input, Client: models}); err == nil {
			t.Errorf("%q was accepted", input)
		}
	}
}

// TestKiroImportAcceptsTheFilesTheAuditNames feeds the real file shapes: Kiro's
// kiro-auth-token.json (which carries an accessToken beside its refresh token) and a
// snake_case IDC payload. Both once misrouted: the first to the CLI-proxy parser, the
// second to the social refresh, which dropped both client secrets.
func TestKiroImportAcceptsTheFilesTheAuditNames(t *testing.T) {
	flow := kiroImportFlow(t)
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	refreshed := func() *stubClient {
		return &stubClient{handler: func(*http.Request) (int, string) {
			return http.StatusOK, `{"accessToken":"fixture-access","refreshToken":"fixture-rotated","expiresIn":900}`
		}}
	}

	// kiro-auth-token.json as the IDE writes it.
	file := refreshed()
	result, err := flow.Import(context.Background(), Params{
		Input:  `{"accessToken":"fixture-stale-access","refreshToken":"fixture-pasted","expiresAt":"2026-10-21T11:00:00.000Z","region":"us-east-1","profileArn":"` + fixtureProfileARN + `","authMethod":"social","provider":"Google"}`,
		Client: file, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatalf("kiro-auth-token.json was refused: %v", err)
	}
	if !strings.HasSuffix(file.last().URL.Path, "/refreshToken") || result.Secret.AccessToken != "fixture-access" || result.Secret.ProviderData["profileArn"] != fixtureProfileARN {
		t.Fatalf("file import: %+v url=%s", result.Secret, file.last().URL)
	}

	// The same file plus the client registration (IDC), in snake_case.
	idc := refreshed()
	result, err = flow.Import(context.Background(), Params{
		Input:  `{"refresh_token":"fixture-pasted","client_id":"fixture-client","client_secret":"fixture-secret","region":"eu-west-1"}`,
		Client: idc, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if idc.last().URL.Host != "oidc.eu-west-1.amazonaws.com" {
		t.Fatalf("snake_case idc refreshed at %s, not the regional endpoint", idc.last().URL.Host)
	}
	data := result.Secret.ProviderData
	if data["authMethod"] != "idc" || data["clientId"] != "fixture-client" || data["clientSecret"] != "fixture-secret" || data["region"] != "eu-west-1" {
		t.Fatalf("idc secrets dropped: %+v", data)
	}
}

// TestKiroExternalIdpRefreshWithoutExpiresInDoesNotKeepALapsedExpiry.
func TestKiroExternalIdpRefreshWithoutExpiresInDoesNotKeepALapsedExpiry(t *testing.T) {
	stub := &stubClient{handler: func(*http.Request) (int, string) {
		return http.StatusOK, `{"access_token":"fixture-new","refresh_token":"fixture-new-refresh"}`
	}}
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	secret := credentials.Secret{RefreshToken: "fixture-refresh", Expiry: now.Add(-time.Hour), ProviderData: map[string]string{
		"authMethod": "external_idp", "clientId": "c", "scope": "s", "tokenEndpoint": "https://login.microsoftonline.com/t/oauth2/v2.0/token",
	}}
	next, err := refreshKiro(context.Background(), stub, func() time.Time { return now }, secret, secret.ProviderData)
	if err != nil || !next.Expiry.Equal(now.Add(time.Hour)) {
		t.Fatalf("expiry=%v err=%v", next.Expiry, err)
	}
}

// TestIflowCookieImportKeepsTheKeyExpiry mirrors iflow/cookie/route.js:115-118.
func TestIflowCookieImportKeepsTheKeyExpiry(t *testing.T) {
	stub := &stubClient{handler: func(request *http.Request) (int, string) {
		if request.Method == http.MethodGet {
			return http.StatusOK, `{"success":true,"data":{"name":"fixture-key-name"}}`
		}
		return http.StatusOK, `{"success":true,"data":{"apiKey":"sk-fixture-iflow","name":"fixture-key-name","expireTime":"2026-12-01 00:00:00"}}`
	}}
	result, err := ManualFlows()["iflow"].Import(context.Background(), Params{Input: "BXAuth=fixture; tracking=drop-me", Client: stub})
	if err != nil {
		t.Fatal(err)
	}
	if result.Secret.Cookie != "BXAuth=fixture;" || result.Secret.ProviderData["expireTime"] != "2026-12-01 00:00:00" || result.Identity != "fixture-key-name" {
		t.Fatalf("result=%+v", result)
	}
}

func TestXiaomiImportRejectsCookieInjectionWithoutEcho(t *testing.T) {
	for _, field := range []string{"mimoPassToken", "mimoUserId", "mimoCUserId"} {
		input := `{"apiKey":"sk-fixture","` + field + `":"fixture-private; other=secret"}`
		_, err := ManualFlows()["xiaomi-mimo"].Import(context.Background(), Params{Input: input})
		if err == nil || strings.Contains(err.Error(), "fixture-private") || strings.Contains(err.Error(), "other=secret") {
			t.Fatal("invalid account value accepted or echoed")
		}
	}
}

// The codex bulk route reads camelCase keys only (bulk-import/route.js:72-99): an
// item with only access_token is "Missing accessToken", and a snake_case
// refresh_token beside a camelCase accessToken is never the refresh token.
func TestCodexBulkReadsCamelCaseOnly(t *testing.T) {
	jwt := fixtureJWT(t, `{"email":"person@example.invalid","https://api.openai.com/auth":{"chatgpt_account_id":"acct-jwt"}}`)
	if _, err := codexBulkAccount(map[string]any{"access_token": jwt}); err == nil || err.Error() != "missing accessToken" {
		t.Fatalf("snake_case access token: %v", err)
	}
	account, err := codexBulkAccount(map[string]any{"accessToken": jwt, "refresh_token": "snake", "id_token": jwt, "expires_in": float64(600)})
	if err != nil {
		t.Fatal(err)
	}
	if account.Secret.RefreshToken != "" || account.Secret.IDToken != "" || !account.Secret.Expiry.IsZero() || account.Carries != (Carries{}) {
		t.Fatalf("snake_case keys were read: %+v carries=%+v", account.Secret, account.Carries)
	}
}

// The route keeps an exported providerSpecificData as given
// (bulk-import/route.js:77,92-94): strings verbatim, empty included, other
// values as their JSON text, and the JWT backfills only a falsy account id or
// plan (:78-91).
func TestCodexBulkKeepsExportedProviderData(t *testing.T) {
	jwt := fixtureJWT(t, `{"https://api.openai.com/auth":{"chatgpt_account_id":"acct-jwt","chatgpt_plan_type":"plus"}}`)
	account, err := codexBulkAccount(map[string]any{
		"accessToken": jwt,
		"providerSpecificData": map[string]any{
			"chatgptAccountId": "", "workspaceId": " ws-1 ", "connectionProxyEnabled": true,
			"jwtExp": float64(1893456000), "nested": map[string]any{"a": float64(1)}, "gone": nil,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"chatgptAccountId": "acct-jwt", "chatgptPlanType": "plus", "workspaceId": " ws-1 ",
		"connectionProxyEnabled": "true", "jwtExp": "1893456000", "nested": `{"a":1}`,
	}
	if fmt.Sprint(account.Secret.ProviderData) != fmt.Sprint(want) {
		t.Fatalf("providerSpecificData=%v, want %v", account.Secret.ProviderData, want)
	}
}
