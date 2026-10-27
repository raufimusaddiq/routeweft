package onboarding

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"
)

// TestBrowserFlowsInferIdentityAsTheReferenceDoes pins which browser flows read
// the account from the provider when they can (#216). The reference asks for no
// identity before any of them (OAuthModal.js opens the provider page directly);
// these learn one best effort, so the operator is not asked up front. Claude
// reports none (claude.js:52), so it still asks.
func TestBrowserFlowsInferIdentityAsTheReferenceDoes(t *testing.T) {
	registry := NewRegistry()
	var inferred []string
	for _, provider := range registry.Providers() {
		flow, _ := registry.Lookup(provider)
		if flow.InfersIdentity {
			inferred = append(inferred, provider)
			if flow.ResolvesIdentity {
				t.Errorf("%s declares both InfersIdentity and ResolvesIdentity", provider)
			}
			if flow.AsksIdentityUpFront() {
				t.Errorf("%s infers its identity but still asks for one", provider)
			}
		}
	}
	sort.Strings(inferred)
	want := []string{"antigravity", "cline", "clinepass", "gemini-cli", "kimchi", "xai", "xiaomi-mimo", "zed"}
	if !reflect.DeepEqual(inferred, want) {
		t.Fatalf("InfersIdentity flows = %v, want %v", inferred, want)
	}
	if !flowFor(t, "claude").AsksIdentityUpFront() {
		t.Fatal("claude reports no account, so it must ask for one")
	}
}

// TestClineGatewayExchangeReadsTheWorkOSAnswer pins the exchange fallback to the
// reference's reading (cline.js:32-48, clinepass.js:32-48): camelCase fields under
// data or at the top level, the email under data.userInfo, an hour when no
// expiresAt is reported. A snake_case answer carries no token the reference reads.
func TestClineGatewayExchangeReadsTheWorkOSAnswer(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, provider, answer, identity string
		expiry                           time.Time
		fails                            bool
	}{
		{"nested", "cline", `{"data":{"accessToken":"wos-access","refreshToken":"wos-refresh","expiresAt":"2026-10-27T18:00:00Z","userInfo":{"email":"dev@example.test"}}}`, "dev@example.test", time.Date(2026, 10, 27, 18, 0, 0, 0, time.UTC), false},
		{"top level", "clinepass", `{"accessToken":"wos-access","refreshToken":"wos-refresh"}`, "", now.Add(time.Hour), false},
		{"snake case", "cline", `{"access_token":"wos-access","refresh_token":"wos-refresh"}`, "", time.Time{}, true},
	} {
		stub := &stubClient{handler: func(*http.Request) (int, string) { return http.StatusOK, tc.answer }}
		result, err := flowFor(t, tc.provider).Exchange(context.Background(), Params{
			Client: stub, Input: "not-a-bundle", RedirectURI: "https://routeweft.example.test/callback", Now: func() time.Time { return now },
		})
		if tc.fails {
			if err == nil {
				t.Fatalf("%s: a snake_case answer was accepted", tc.name)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if result.Secret.AccessToken != "wos-access" || result.Secret.RefreshToken != "wos-refresh" {
			t.Fatalf("%s: tokens not read", tc.name)
		}
		if strings.HasPrefix(result.Secret.AccessToken, "workos:") {
			t.Fatalf("%s: the stored token carries the wire prefix", tc.name)
		}
		if result.Identity != tc.identity || !result.Secret.Expiry.Equal(tc.expiry) {
			t.Fatalf("%s: identity=%q expiry=%v", tc.name, result.Identity, result.Secret.Expiry)
		}
		var body map[string]string
		_ = json.Unmarshal([]byte(stub.bodies[0]), &body)
		if stub.requests[0].URL.String() != "https://api.cline.bot/api/v1/auth/token" || body["client_type"] != "extension" || body["grant_type"] != "authorization_code" {
			t.Fatalf("%s: exchange request %s %v", tc.name, stub.requests[0].URL, body)
		}
	}
}

// TestClineBundleKeepsNamesAndAcceptsURLSafeBase64 pins the bundle branch
// (cline.js:15-31,51-59): Node's decoder reads either alphabet, the names land in
// provider data, and a bundle without expiresAt lasts an hour.
func TestClineBundleKeepsNamesAndAcceptsURLSafeBase64(t *testing.T) {
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	// The payload is chosen so its standard encoding holds '+' and '/'.
	bundle := `{"accessToken":"eyJ>>>?","refreshToken":"r???","email":"dev@example.test","firstName":"Ada","lastName":"L"}`
	encoded := base64.URLEncoding.EncodeToString([]byte(bundle))
	if !strings.ContainsAny(encoded, "-_") {
		t.Fatalf("fixture %q is not URL-safe specific", encoded)
	}
	result, err := flowFor(t, "clinepass").Exchange(context.Background(), Params{Input: strings.TrimRight(encoded, "="), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	if result.Secret.AccessToken != "eyJ>>>?" || result.Identity != "dev@example.test" || result.DisplayName != "Ada L" {
		t.Fatalf("bundle read as %+v", result)
	}
	if !reflect.DeepEqual(result.Secret.ProviderData, map[string]string{"firstName": "Ada", "lastName": "L"}) {
		t.Fatalf("provider data %v", result.Secret.ProviderData)
	}
	if !result.Secret.Expiry.Equal(now.Add(time.Hour)) {
		t.Fatalf("expiry %v", result.Secret.Expiry)
	}
}

// TestAntigravityOnboardsTheAccountAfterSave pins the project bootstrap
// (antigravity.js:78-124): with a project, the result carries background work
// that asks onboardUser for the default tier until it reports done; without one,
// or for gemini-cli, there is none.
func TestAntigravityOnboardsTheAccountAfterSave(t *testing.T) {
	t.Setenv(GoogleClientIDEnv, "fixture-client-id")
	t.Setenv(GoogleClientSecretEnv, "fixture-client-secret")
	previous := antigravityOnboardDelay
	antigravityOnboardDelay = time.Millisecond
	t.Cleanup(func() { antigravityOnboardDelay = previous })
	signIn := func(provider, load string) Result {
		stub := &stubClient{handler: func(request *http.Request) (int, string) {
			switch {
			case strings.HasSuffix(request.URL.Path, "/token"):
				return http.StatusOK, `{"access_token":"fixture-access","expires_in":3600}`
			case strings.HasSuffix(request.URL.Path, ":loadCodeAssist"):
				return http.StatusOK, load
			}
			return http.StatusOK, `{"email":"fixture@example.test"}`
		}}
		result, err := flowFor(t, provider).Exchange(context.Background(), Params{Client: stub, Input: "fixture-code", RedirectURI: "https://routeweft.example.test/callback"})
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		return result
	}
	withProject := `{"cloudaicompanionProject":{"id":"fixture-project"},"allowedTiers":[{"id":"free-tier"},{"id":"standard-tier","isDefault":true}]}`
	if signIn("antigravity", `{}`).AfterSave != nil {
		t.Fatal("antigravity without a project onboards nothing (antigravity.js:104)")
	}
	if signIn("gemini-cli", withProject).AfterSave != nil {
		t.Fatal("gemini-cli has no onboarding step in the reference")
	}
	result := signIn("antigravity", withProject)
	if result.AfterSave == nil || result.Secret.ProviderData["projectId"] != "fixture-project" {
		t.Fatalf("antigravity with a project: afterSave=%v data=%v", result.AfterSave != nil, result.Secret.ProviderData)
	}
	// AfterSave captured the sign-in's client; drive one that answers not done,
	// then done, through the same entry point.
	answers := []string{`{"done":false}`, `{"done":true}`, `{"done":true}`}
	onboard := &stubClient{handler: func(*http.Request) (int, string) {
		answer := answers[0]
		answers = answers[1:]
		return http.StatusOK, answer
	}}
	onboardAntigravityUser(context.Background(), onboard, "fixture-access", "standard-tier")
	if len(onboard.requests) != 2 {
		t.Fatalf("onboardUser called %d times, want 2 (stop at done)", len(onboard.requests))
	}
	request := onboard.requests[0]
	if request.URL.String() != "https://cloudcode-pa.googleapis.com/v1internal:onboardUser" || request.Header.Get("Authorization") != "Bearer fixture-access" || request.Header.Get("x-request-source") != "local" {
		t.Fatalf("onboardUser request %s %v", request.URL, request.Header)
	}
	var body struct {
		TierID   string         `json:"tierId"`
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(onboard.bodies[0]), &body); err != nil || body.TierID != "standard-tier" || body.Metadata == nil {
		t.Fatalf("onboardUser body %s", onboard.bodies[0])
	}
	// A provider that never says done is asked at most the reference's ten times.
	never := &stubClient{handler: func(*http.Request) (int, string) { return http.StatusServiceUnavailable, `{}` }}
	onboardAntigravityUser(context.Background(), never, "fixture-access", "legacy-tier")
	if len(never.requests) != antigravityOnboardAttempts {
		t.Fatalf("onboardUser retried %d times, want %d", len(never.requests), antigravityOnboardAttempts)
	}
	// Without a default tier the reference asks for legacy-tier.
	if _, tier := cloudCodeProject(context.Background(), &stubClient{handler: func(*http.Request) (int, string) {
		return http.StatusOK, `{"cloudaicompanionProject":"p"}`
	}}, "fixture-access", true); tier != "legacy-tier" {
		t.Fatalf("default tier %q", tier)
	}
}
