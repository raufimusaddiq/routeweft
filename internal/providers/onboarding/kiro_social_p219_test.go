package onboarding

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

// #219: Kiro's Google/GitHub sign-in sits beside its AWS SSO device flow. The
// device endpoints keep the device flow (the parity gate still reads device_code
// for kiro) and the browser endpoints reach the social flow.
func TestKiroOffersItsSocialSignInBesideTheDeviceFlow(t *testing.T) {
	registry := NewRegistry()
	device, ok := registry.Lookup("kiro")
	if !ok || device.Type != FlowDeviceCode || registry.OnboardingFlows()["kiro"] != string(FlowDeviceCode) {
		t.Fatalf("kiro device flow: %+v", device.Type)
	}
	social, ok := registry.LookupBrowser("kiro")
	if !ok || social.Type != FlowAuthorizationCodePKCE || social.Completion != CompletionRedirectURL || social.RedirectSource != RedirectPinned || !social.RenewsSameAccount {
		t.Fatalf("kiro social flow: %+v", social)
	}
	if social.AsksIdentityUpFront() != device.AsksIdentityUpFront() || !social.AsksIdentityUpFront() {
		t.Fatal("kiro tokens are opaque, so both sign-ins ask for the account")
	}
	// A provider with only a device flow has no browser sign-in, and a browser
	// provider's browser sign-in is its own flow.
	if _, ok := registry.LookupBrowser("github"); ok {
		t.Fatal("github gained a browser sign-in")
	}
	if claude, ok := registry.LookupBrowser("claude"); !ok || claude.Provider != "claude" {
		t.Fatal("claude lost its browser sign-in")
	}
}

func TestKiroAuthMethodsAreClosedSets(t *testing.T) {
	device, _ := NewRegistry().Lookup("kiro")
	social, _ := NewRegistry().LookupBrowser("kiro")
	for _, tc := range []struct {
		flow      Flow
		requested string
		want      string
		ok        bool
	}{
		{device, "", "builder-id", true},
		{device, " idc ", "idc", true},
		{device, "google", "", false},
		{device, "IDC", "", false},
		{social, "google", "google", true},
		{social, "github", "github", true},
		{social, "", "", false},
		{social, "builder-id", "", false},
		{social, "Github", "", false},
		{Flow{}, "anything", "", true},
	} {
		if got, ok := tc.flow.AuthMethodFor(tc.requested); got != tc.want || ok != tc.ok {
			t.Errorf("%s AuthMethodFor(%q) = %q, %v", tc.flow.Type, tc.requested, got, ok)
		}
	}
}

// The flows refuse a foreign method themselves too, without a network call, so
// a caller that skipped the handler's check still cannot change the method.
func TestKiroFlowsRefuseAForeignMethodWithoutANetworkCall(t *testing.T) {
	stub := &stubClient{}
	device, _ := NewRegistry().Lookup("kiro")
	if _, err := device.Authorize(context.Background(), Params{Client: stub, Meta: map[string]string{"authMethod": "google"}}); !errors.Is(err, ErrKiroDeviceMethod) {
		t.Fatalf("device with google: %v", err)
	}
	social, _ := NewRegistry().LookupBrowser("kiro")
	if _, err := social.Authorize(context.Background(), Params{Client: stub, Verifier: "v", State: "s"}); !errors.Is(err, ErrKiroSocialMethod) {
		t.Fatalf("social without a method: %v", err)
	}
	if _, err := social.Exchange(context.Background(), Params{Client: stub, Input: "code", Meta: map[string]string{"authMethod": "idc"}}); !errors.Is(err, ErrKiroSocialMethod) {
		t.Fatalf("social exchange as idc: %v", err)
	}
	if len(stub.requests) != 0 {
		t.Fatalf("a refused method reached %v", stub.requests[0].URL)
	}
}

// The login URL carries the S256 challenge of the verifier the exchange sends.
func TestKiroSocialLoginCarriesTheVerifiersChallenge(t *testing.T) {
	social, _ := NewRegistry().LookupBrowser("kiro")
	request, err := social.Authorize(context.Background(), Params{Verifier: "fixture-verifier", State: "fixture-state", Meta: map[string]string{"authMethod": "github"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"idp=Github", "code_challenge=" + challengeFor("fixture-verifier"), "state=fixture-state", "redirect_uri=kiro%3A%2F%2Fkiro.kiroAgent%2Fauthenticate-success"} {
		if !strings.Contains(request.URL, want) {
			t.Fatalf("login URL %s lacks %s", request.URL, want)
		}
	}
}

// An exchange without expiresIn lasts the pin's one hour, and a failed profile
// lookup leaves the connection without an ARN rather than failing the sign-in.
func TestKiroSocialExchangeDefaultsAndBestEffortLookup(t *testing.T) {
	stub := &stubClient{handler: func(request *http.Request) (int, string) {
		if strings.HasSuffix(request.URL.Path, "/ListAvailableProfiles") {
			return http.StatusForbidden, `{}`
		}
		return http.StatusOK, `{"accessToken":"fixture-access","refreshToken":"fixture-refresh"}`
	}}
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	social, _ := NewRegistry().LookupBrowser("kiro")
	result, err := social.Exchange(context.Background(), Params{Client: stub, Input: "code", Verifier: "v", Now: func() time.Time { return now }, Meta: map[string]string{"authMethod": "google"}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.Secret.Expiry.Equal(now.Add(time.Hour)) || result.Secret.ProviderData["profileArn"] != "" || result.Secret.ProviderData["authMethod"] != "google" {
		t.Fatalf("result %+v", result.Secret)
	}
	if len(stub.requests) != 2 {
		t.Fatalf("requests %d", len(stub.requests))
	}
	// A reply without an access token saves nothing.
	empty := &stubClient{handler: func(*http.Request) (int, string) { return http.StatusOK, `{"refreshToken":"fixture-refresh"}` }}
	if _, err := social.Exchange(context.Background(), Params{Client: empty, Input: "code", Meta: map[string]string{"authMethod": "google"}}); err == nil {
		t.Fatal("an exchange without a token succeeded")
	}
}

// A Builder ID approval without a profile ARN looks it up, as the pin's device
// poll does (providers/index.js:175-179).
func TestKiroDeviceApprovalLooksUpAMissingProfileARN(t *testing.T) {
	const arn = "arn:aws:codewhisperer:us-east-1:000000000000:profile/FIXTURE"
	stub := &stubClient{handler: func(request *http.Request) (int, string) {
		if request.URL.String() == kiroProfilesEndpoint {
			return http.StatusOK, `{"profiles":[{"arn":"` + arn + `"}]}`
		}
		return http.StatusOK, `{"accessToken":"fixture-access","refreshToken":"fixture-refresh","expiresIn":3600}`
	}}
	device, _ := NewRegistry().Lookup("kiro")
	result, err := device.Poll(context.Background(), Params{Client: stub, Input: "fixture-device", Meta: map[string]string{"clientId": "c", "clientSecret": "s"}})
	if err != nil || !result.Done {
		t.Fatalf("poll: %+v %v", result, err)
	}
	if result.Result.Secret.ProviderData["profileArn"] != arn || stub.last().Header.Get("Authorization") != "Bearer fixture-access" {
		t.Fatalf("provider data %+v", result.Result.Secret.ProviderData)
	}
}

// The refresh records a missing ARN by lookup (resolveKiroProfileArnPatch) and
// never replaces or looks up one already stored.
func TestKiroRefreshProfileARNFallback(t *testing.T) {
	const arn = "arn:aws:codewhisperer:us-east-1:000000000000:profile/FIXTURE"
	stub := &stubClient{handler: func(request *http.Request) (int, string) {
		if request.URL.String() == kiroProfilesEndpoint {
			return http.StatusOK, `{"profiles":[{"arn":"` + arn + `"}]}`
		}
		return http.StatusOK, `{"accessToken":"fixture-access","refreshToken":"fixture-refresh","expiresIn":3600}`
	}}
	now := func() time.Time { return time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC) }
	next, err := refreshKiro(context.Background(), stub, now, credentials.Secret{RefreshToken: "r", ProviderData: map[string]string{"authMethod": "github"}}, map[string]string{"authMethod": "github"})
	if err != nil || next.ProviderData["profileArn"] != arn || next.ProviderData["authMethod"] != "github" || len(stub.requests) != 2 {
		t.Fatalf("refresh without an ARN: %+v %v (%d requests)", next.ProviderData, err, len(stub.requests))
	}
	stub.requests = nil
	stored := map[string]string{"authMethod": "github", "profileArn": "arn:aws:codewhisperer:us-east-1:000000000000:profile/KEPT"}
	next, err = refreshKiro(context.Background(), stub, now, credentials.Secret{RefreshToken: "r", ProviderData: stored}, stored)
	if err != nil || next.ProviderData["profileArn"] != stored["profileArn"] || len(stub.requests) != 1 {
		t.Fatalf("refresh with an ARN: %+v %v (%d requests)", next.ProviderData, err, len(stub.requests))
	}
}
