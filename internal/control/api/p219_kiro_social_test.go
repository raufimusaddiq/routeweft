package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/onboarding"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

// #219: Kiro's Google and GitHub sign-in (social-authorize/route.js,
// social-exchange/route.js) driven end to end against sanitized fixtures: the
// login URL, the pasted kiro:// address, denial, cancel, state, expiry, the
// duplicate rule, Reauthenticate, the profile ARN lookup and the refresh the saved
// credential takes. No network call leaves the fixture.

const kiroSocialARN = "arn:aws:codewhisperer:us-east-1:000000000000:profile/FIXTURE"

type kiroSocialFixture struct {
	mu sync.Mutex
	// reportARN makes the token exchange report a profile ARN; without it the
	// sign-in looks the profile up.
	reportARN bool
	// exchangeStatus is the token exchange's status (0 means 200).
	exchangeStatus int
	seen           []string
	bodies         []string
	auth           []string
}

func (f *kiroSocialFixture) Do(request *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body := ""
	if request.Body != nil {
		raw, _ := io.ReadAll(request.Body)
		body = string(raw)
	}
	raw := request.URL.String()
	f.seen, f.bodies, f.auth = append(f.seen, raw), append(f.bodies, body), append(f.auth, request.Header.Get("Authorization"))
	respond := func(status int, payload string) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(payload))}, nil
	}
	switch raw {
	case "https://prod.us-east-1.auth.desktop.kiro.dev/oauth/token":
		if f.exchangeStatus != 0 && f.exchangeStatus != 200 {
			return respond(f.exchangeStatus, `{"message":"invalid code"}`)
		}
		var sent struct {
			Code string `json:"code"`
		}
		_ = json.Unmarshal([]byte(body), &sent)
		arn := ""
		if f.reportARN {
			arn = fmt.Sprintf(`,"profileArn":%q`, kiroSocialARN)
		}
		// Kiro's social tokens are opaque, not JWTs: the account is not readable.
		return respond(200, fmt.Sprintf(`{"accessToken":"fixture-social-access-%s","refreshToken":"fixture-social-refresh-%s","expiresIn":1800%s}`, sent.Code, sent.Code, arn))
	case "https://codewhisperer.us-east-1.amazonaws.com/ListAvailableProfiles":
		return respond(200, `{"profiles":[{"arn":"  "},{"arn":"`+kiroSocialARN+`"}]}`)
	case "https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken":
		return respond(200, `{"accessToken":"fixture-social-access-rotated","refreshToken":"fixture-social-refresh-rotated","expiresIn":3600}`)
	}
	return respond(404, `{}`)
}

func (f *kiroSocialFixture) requests() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.seen...)
}

type kiroSocialRig struct {
	h       *Handler
	mux     *http.ServeMux
	cookie  *http.Cookie
	creds   *credentials.Store
	fixture *kiroSocialFixture
	now     *time.Time
}

func newKiroSocialRig(t *testing.T) *kiroSocialRig {
	t.Helper()
	fixture := &kiroSocialFixture{}
	h, mux, creds, cookie := newOnboardingAPI(t, fixture)
	h.opts.Providers = registry.Builtins()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	h.oauth.now = func() time.Time { return now }
	return &kiroSocialRig{h: h, mux: mux, cookie: cookie, creds: creds, fixture: fixture, now: &now}
}

type kiroSocialStart struct {
	State        string `json:"state"`
	AuthorizeURL string `json:"authorizeUrl"`
	Completion   string `json:"completion"`
}

func (r *kiroSocialRig) start(t *testing.T, body string) (int, kiroSocialStart, string) {
	t.Helper()
	recorder := doJSON(t, r.mux, r.cookie, http.MethodPost, "/admin/v1/oauth/kiro/start", body)
	var started kiroSocialStart
	_ = json.Unmarshal(recorder.Body.Bytes(), &started)
	return recorder.Code, started, recorder.Body.String()
}

func (r *kiroSocialRig) callback(t *testing.T, state, pasted string) (int, map[string]any, string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]string{"state": state, "input": pasted})
	recorder := doJSON(t, r.mux, r.cookie, http.MethodPost, "/admin/v1/oauth/kiro/callback", string(payload))
	var answer map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &answer)
	return recorder.Code, answer, recorder.Body.String()
}

// signIn runs one Google or GitHub sign-in to completion and returns the saved
// connection. code is the fixture authorization code, which the fixture folds
// into the tokens so two sign-ins hold different credentials.
func (r *kiroSocialRig) signIn(t *testing.T, method, identity, code string) map[string]any {
	t.Helper()
	status, started, body := r.start(t, fmt.Sprintf(`{"identity":%q,"meta":{"authMethod":%q}}`, identity, method))
	if status != http.StatusOK {
		t.Fatalf("%s start: %d %s", method, status, body)
	}
	status, answer, body := r.callback(t, started.State, "kiro://kiro.kiroAgent/authenticate-success?code="+code+"&state="+started.State)
	if status != http.StatusOK {
		t.Fatalf("%s callback: %d %s", method, status, body)
	}
	for _, secret := range []string{"fixture-social-access", "fixture-social-refresh"} {
		if strings.Contains(body, secret) {
			t.Fatalf("%s sign-in echoed a credential: %s", method, body)
		}
	}
	return answer
}

func (r *kiroSocialRig) stored(t *testing.T) []credentials.Connection {
	t.Helper()
	connections, err := r.creds.ListConnections(context.Background(), "kiro")
	if err != nil {
		t.Fatal(err)
	}
	return connections
}

// The dashboard is told about Google and GitHub as two real browser methods,
// each fixed to its auth method (no field to type it), beside the AWS SSO device
// method and the import, all from the tables the endpoints read.
func TestKiroSocialMethodsAreListedFromTheFlowTable(t *testing.T) {
	rig := newKiroSocialRig(t)
	recorder := doJSON(t, rig.mux, rig.cookie, http.MethodGet, "/admin/v1/providers?pageSize=200", "")
	var list struct {
		Items []struct {
			ID            string                  `json:"id"`
			SignInMethods []signInMethodReadModel `json:"signInMethods"`
		} `json:"items"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	var methods []signInMethodReadModel
	for _, item := range list.Items {
		if item.ID == "kiro" {
			methods = item.SignInMethods
		}
	}
	var got []string
	for _, method := range methods {
		got = append(got, method.Mode+":"+method.Variant)
		if method.Mode == "browser" {
			if method.Completion != "redirect-url" || len(method.MetaKeys) != 0 || method.ResolvesIdentity || method.InfersIdentity || method.RedirectURI != "" || method.LoopbackRedirect != "" {
				t.Errorf("kiro %s method %+v", method.Variant, method)
			}
		}
		if method.Mode == "device" && strings.Join(method.MetaKeys, ",") != "region,startUrl,authMethod" {
			t.Errorf("kiro device method keys %v", method.MetaKeys)
		}
	}
	if strings.Join(got, ",") != "device:,browser:google,browser:github,import:" {
		t.Fatalf("kiro methods %v", got)
	}
}

// Google and GitHub each start at Kiro's login page for their own idp with PKCE
// and the kiro:// redirect, and the pasted address completes the sign-in into an
// oauth connection that records which provider made it.
func TestKiroSocialSignInSucceeds(t *testing.T) {
	for _, tc := range []struct{ method, idp, label string }{{"google", "Google", "Google"}, {"github", "Github", "Github"}} {
		t.Run(tc.method, func(t *testing.T) {
			rig := newKiroSocialRig(t)
			rig.fixture.reportARN = true
			status, started, body := rig.start(t, `{"identity":"person@example.invalid","meta":{"authMethod":"`+tc.method+`"}}`)
			if status != http.StatusOK || started.Completion != "redirect-url" {
				t.Fatalf("start: %d %s", status, body)
			}
			login, err := url.Parse(started.AuthorizeURL)
			if err != nil {
				t.Fatal(err)
			}
			query := login.Query()
			if login.Host != "prod.us-east-1.auth.desktop.kiro.dev" || login.Path != "/login" || query.Get("idp") != tc.idp ||
				query.Get("redirect_uri") != "kiro://kiro.kiroAgent/authenticate-success" || query.Get("code_challenge_method") != "S256" ||
				query.Get("code_challenge") == "" || query.Get("state") != started.State || query.Get("prompt") != "select_account" {
				t.Fatalf("login URL %s", started.AuthorizeURL)
			}
			if len(rig.fixture.requests()) != 0 {
				t.Fatalf("start reached the network: %v", rig.fixture.requests())
			}
			status, answer, body := rig.callback(t, started.State, "kiro://kiro.kiroAgent/authenticate-success?code=one&state="+started.State)
			if status != http.StatusOK || answer["outcome"] != "added" || answer["identity"] != "person@example.invalid" || answer["authKind"] != "oauth" {
				t.Fatalf("callback: %d %s", status, body)
			}
			if strings.Contains(body, "fixture-social") {
				t.Fatalf("callback echoed a credential: %s", body)
			}
			rig.fixture.mu.Lock()
			exchange := rig.fixture.bodies[0]
			rig.fixture.mu.Unlock()
			for _, want := range []string{`"code":"one"`, `"code_verifier":"`, `"redirect_uri":"kiro://kiro.kiroAgent/authenticate-success"`} {
				if !strings.Contains(exchange, want) {
					t.Fatalf("exchange body %s lacks %s", exchange, want)
				}
			}
			stored := rig.stored(t)
			if len(stored) != 1 {
				t.Fatalf("stored %d connections", len(stored))
			}
			secret := stored[0].Secret
			data := secret.ProviderData
			// The exchange reported 1800 s; the handler's exchange runs on the wall clock.
			lifetime := time.Until(secret.Expiry)
			if secret.AccessToken != "fixture-social-access-one" || secret.RefreshToken != "fixture-social-refresh-one" ||
				lifetime < 29*time.Minute || lifetime > 31*time.Minute {
				t.Fatalf("secret %+v", secret)
			}
			if data["authMethod"] != tc.method || data["provider"] != tc.label || data["profileArn"] != kiroSocialARN || data["clientId"] != "" {
				t.Fatalf("provider data %+v", data)
			}
			// The reported ARN is used as is: no profile lookup.
			if got := rig.fixture.requests(); len(got) != 1 {
				t.Fatalf("requests %v", got)
			}
		})
	}
}

// A sign-in is never quietly turned into another method: a browser start that
// names no social provider, an AWS SSO method or anything else is refused before
// any network call, and so is a device start that names Google or GitHub.
func TestKiroSocialRefusesAnUnchosenOrForeignMethod(t *testing.T) {
	rig := newKiroSocialRig(t)
	for _, meta := range []string{`{}`, `{"authMethod":""}`, `{"authMethod":"builder-id"}`, `{"authMethod":"idc"}`, `{"authMethod":"gitlab"}`, `{"authMethod":"Google"}`} {
		status, _, body := rig.start(t, `{"identity":"person@example.invalid","meta":`+meta+`}`)
		if status != http.StatusBadRequest || !strings.Contains(body, "oauth_auth_method_invalid") {
			t.Errorf("browser start with %s: %d %s", meta, status, body)
		}
	}
	for _, meta := range []string{`{"authMethod":"google"}`, `{"authMethod":"github"}`, `{"authMethod":"social"}`} {
		recorder := doJSON(t, rig.mux, rig.cookie, http.MethodPost, "/admin/v1/oauth/kiro/device", `{"identity":"person@example.invalid","meta":`+meta+`}`)
		if recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "oauth_auth_method_invalid") {
			t.Errorf("device start with %s: %d %s", meta, recorder.Code, recorder.Body.String())
		}
	}
	if got := rig.fixture.requests(); len(got) != 0 {
		t.Fatalf("a refused start reached the network: %v", got)
	}
	if stored := rig.stored(t); len(stored) != 0 {
		t.Fatalf("a refused start saved %d connections", len(stored))
	}
	// Kiro does not report the account, so the identity is asked for up front.
	if status, _, body := rig.start(t, `{"meta":{"authMethod":"google"}}`); status != http.StatusBadRequest {
		t.Fatalf("start without identity: %d %s", status, body)
	}
}

// Denial, cancel, a pasted address from another attempt, an expired attempt, a
// replay and a rejected code each end or keep the attempt as the shared sign-in
// contract says, and none of them saves anything.
func TestKiroSocialFailuresSaveNothing(t *testing.T) {
	start := func(t *testing.T, rig *kiroSocialRig) kiroSocialStart {
		status, started, body := rig.start(t, `{"identity":"person@example.invalid","meta":{"authMethod":"github"}}`)
		if status != http.StatusOK {
			t.Fatalf("start: %d %s", status, body)
		}
		return started
	}
	expect := func(t *testing.T, rig *kiroSocialRig, state, pasted string, code int, errorCode string) {
		t.Helper()
		status, _, body := rig.callback(t, state, pasted)
		if status != code || !strings.Contains(body, errorCode) {
			t.Fatalf("paste %q: %d %s, want %d %s", pasted, status, body, code, errorCode)
		}
	}
	t.Run("denied", func(t *testing.T) {
		rig := newKiroSocialRig(t)
		started := start(t, rig)
		expect(t, rig, started.State, "kiro://kiro.kiroAgent/authenticate-success?error=access_denied&error_description=fixture&state="+started.State, http.StatusBadRequest, "oauth_denied")
		// Denial ends the attempt.
		expect(t, rig, started.State, "kiro://kiro.kiroAgent/authenticate-success?code=late&state="+started.State, http.StatusBadRequest, "oauth_state_invalid")
		if len(rig.fixture.requests()) != 0 || len(rig.stored(t)) != 0 {
			t.Fatal("a denial reached the provider or saved")
		}
	})
	t.Run("cancel", func(t *testing.T) {
		rig := newKiroSocialRig(t)
		started := start(t, rig)
		if recorder := doJSON(t, rig.mux, rig.cookie, http.MethodPost, "/admin/v1/oauth/kiro/cancel", `{"state":"`+started.State+`"}`); recorder.Code != http.StatusOK {
			t.Fatalf("cancel: %d %s", recorder.Code, recorder.Body.String())
		}
		expect(t, rig, started.State, "kiro://kiro.kiroAgent/authenticate-success?code=one&state="+started.State, http.StatusBadRequest, "oauth_state_invalid")
		if len(rig.fixture.requests()) != 0 || len(rig.stored(t)) != 0 {
			t.Fatal("a cancelled attempt reached the provider or saved")
		}
	})
	t.Run("state from another attempt", func(t *testing.T) {
		rig := newKiroSocialRig(t)
		started := start(t, rig)
		expect(t, rig, started.State, "kiro://kiro.kiroAgent/authenticate-success?code=one&state=fixture-other-state", http.StatusBadRequest, "oauth_state_mismatch")
		expect(t, rig, started.State, "kiro://kiro.kiroAgent/authenticate-success?state="+started.State, http.StatusBadRequest, "oauth_code_missing")
		// Both refusals keep the attempt: the right address still completes it.
		if status, _, body := rig.callback(t, started.State, "kiro://kiro.kiroAgent/authenticate-success?code=one&state="+started.State); status != http.StatusOK {
			t.Fatalf("the attempt was lost: %d %s", status, body)
		}
		expect(t, rig, started.State, "kiro://kiro.kiroAgent/authenticate-success?code=one&state="+started.State, http.StatusBadRequest, "oauth_state_invalid")
		if len(rig.stored(t)) != 1 {
			t.Fatal("replay changed the stored connections")
		}
	})
	t.Run("expired", func(t *testing.T) {
		rig := newKiroSocialRig(t)
		started := start(t, rig)
		*rig.now = rig.now.Add(oauthFlowTTL + time.Second)
		expect(t, rig, started.State, "kiro://kiro.kiroAgent/authenticate-success?code=one&state="+started.State, http.StatusBadRequest, "oauth_state_invalid")
		if len(rig.fixture.requests()) != 0 || len(rig.stored(t)) != 0 {
			t.Fatal("an expired attempt reached the provider or saved")
		}
	})
	t.Run("rejected code", func(t *testing.T) {
		rig := newKiroSocialRig(t)
		rig.fixture.exchangeStatus = http.StatusBadRequest
		started := start(t, rig)
		status, _, body := rig.callback(t, started.State, "kiro://kiro.kiroAgent/authenticate-success?code=one&state="+started.State)
		if status != http.StatusBadGateway || !strings.Contains(body, "oauth_exchange_failed") || strings.Contains(body, "invalid code") {
			t.Fatalf("rejected code: %d %s", status, body)
		}
		if len(rig.stored(t)) != 0 {
			t.Fatal("a rejected code saved")
		}
	})
}

// Signing the same account in again renews its row (outcome updated, id kept, new
// credential) as the reference's oauth duplicate rule does; another account is
// added beside it and left alone by the renewal.
func TestKiroSocialDuplicateIdentityRenewsItsRow(t *testing.T) {
	rig := newKiroSocialRig(t)
	rig.fixture.reportARN = true
	first := rig.signIn(t, "google", "person-a@example.invalid", "one")
	other := rig.signIn(t, "github", "person-b@example.invalid", "two")
	again := rig.signIn(t, "google", "person-a@example.invalid", "three")
	if first["outcome"] != "added" || other["outcome"] != "added" || again["outcome"] != "updated" || again["id"] != first["id"] || other["id"] == first["id"] {
		t.Fatalf("outcomes %v / %v / %v", first, other, again)
	}
	stored := rig.stored(t)
	if len(stored) != 2 {
		t.Fatalf("stored %d connections", len(stored))
	}
	for _, connection := range stored {
		switch connection.Identity {
		case "person-a@example.invalid":
			if connection.Secret.AccessToken != "fixture-social-access-three" || connection.Secret.ProviderData["authMethod"] != "google" {
				t.Fatalf("renewed row %+v", connection.Secret)
			}
		case "person-b@example.invalid":
			if connection.Secret.AccessToken != "fixture-social-access-two" || connection.Secret.ProviderData["authMethod"] != "github" {
				t.Fatalf("other account's row changed: %+v", connection.Secret)
			}
		default:
			t.Fatalf("unexpected identity %q", connection.Identity)
		}
	}
}

// Reauthenticate renews a social connection with the provider that made it,
// whatever the request names, and refuses a connection the social sign-in did not
// make (Builder ID) rather than rewriting it as a social credential.
func TestKiroSocialReauthenticateKeepsTheConnectionsMethod(t *testing.T) {
	rig := newKiroSocialRig(t)
	rig.fixture.reportARN = true
	saved := rig.signIn(t, "github", "person@example.invalid", "one")
	status, started, body := rig.start(t, `{"connectionId":"`+saved["id"].(string)+`","meta":{"authMethod":"google"}}`)
	if status != http.StatusOK {
		t.Fatalf("reauthenticate start: %d %s", status, body)
	}
	if login, _ := url.Parse(started.AuthorizeURL); login == nil || login.Query().Get("idp") != "Github" {
		t.Fatalf("reauthenticate switched provider: %s", started.AuthorizeURL)
	}
	status, answer, body := rig.callback(t, started.State, "kiro://kiro.kiroAgent/authenticate-success?code=two&state="+started.State)
	if status != http.StatusOK || answer["outcome"] != "reauthenticated" || answer["id"] != saved["id"] {
		t.Fatalf("reauthenticate: %d %s", status, body)
	}
	if stored := rig.stored(t); len(stored) != 1 || stored[0].Secret.AccessToken != "fixture-social-access-two" || stored[0].Secret.ProviderData["authMethod"] != "github" {
		t.Fatalf("stored %+v", stored)
	}

	// A Builder ID connection, as the device flow stores it.
	builder, _, err := rig.h.publishConnection(context.Background(), credentials.Connection{
		ProviderID: "kiro", Name: "builder", AuthKind: credentials.AuthOAuth, Identity: "builder@example.invalid", Enabled: true,
		Secret: credentials.Secret{AccessToken: "fixture-builder-access", RefreshToken: "fixture-builder-refresh", ProviderData: map[string]string{
			"authMethod": "builder-id", "clientId": "fixture-client", "clientSecret": "fixture-client-secret", "profileArn": kiroSocialARN,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	status, _, body = rig.start(t, `{"connectionId":"`+builder.ID+`","meta":{"authMethod":"google"}}`)
	if status != http.StatusBadRequest || !strings.Contains(body, "oauth_reauth_unsupported") {
		t.Fatalf("builder id reauthenticate: %d %s", status, body)
	}
}

// A sign-in whose exchange reports no profile ARN looks it up as the pin does
// (fetchKiroProfileArn), so the connection can serve before its first refresh;
// the saved credential then refreshes durably at Kiro's own service, keeping its
// method, its ARN and the rotated pair.
func TestKiroSocialProfileARNFallbackAndDurableRefresh(t *testing.T) {
	rig := newKiroSocialRig(t)
	rig.signIn(t, "google", "person@example.invalid", "one")
	requests := rig.fixture.requests()
	if len(requests) != 2 || requests[1] != "https://codewhisperer.us-east-1.amazonaws.com/ListAvailableProfiles" {
		t.Fatalf("requests %v", requests)
	}
	rig.fixture.mu.Lock()
	lookupAuth, lookupBody := rig.fixture.auth[1], rig.fixture.bodies[1]
	rig.fixture.mu.Unlock()
	if lookupAuth != "Bearer fixture-social-access-one" || lookupBody != `{"maxResults":10}` {
		t.Fatalf("lookup sent %q %q", lookupAuth, lookupBody)
	}
	stored := rig.stored(t)
	if len(stored) != 1 || stored[0].Secret.ProviderData["profileArn"] != kiroSocialARN {
		t.Fatalf("stored %+v", stored)
	}

	resolve, err := onboarding.RefreshResolver(rig.fixture, func() time.Time { return *rig.now })
	if err != nil {
		t.Fatal(err)
	}
	refresh := resolve(stored[0])
	if refresh == nil {
		t.Fatal("no refresher for a social connection")
	}
	next, err := refresh(context.Background(), stored[0].Secret)
	if err != nil {
		t.Fatal(err)
	}
	requests = rig.fixture.requests()
	if requests[len(requests)-1] != "https://prod.us-east-1.auth.desktop.kiro.dev/refreshToken" {
		t.Fatalf("refresh went to %v", requests)
	}
	if next.AccessToken != "fixture-social-access-rotated" || next.RefreshToken != "fixture-social-refresh-rotated" ||
		next.ProviderData["authMethod"] != "google" || next.ProviderData["profileArn"] != kiroSocialARN || !next.Expiry.Equal(rig.now.Add(time.Hour)) {
		t.Fatalf("refreshed %+v", next)
	}
}
