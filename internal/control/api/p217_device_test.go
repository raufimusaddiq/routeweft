package api

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

// #217: device approval journeys for every device method. Each provider's start
// and poll endpoints are scripted from sanitized fixtures, and the poll answer is
// one of a small set of verdicts the test switches between, so every method is
// driven through pending, slow_down, transient errors, expiry, denial, cancel,
// approval and a second sign-in of the same account without a network call.

// deviceVerdict is what the scripted provider answers a poll with.
type deviceVerdict string

const (
	verdictPending   deviceVerdict = "pending"
	verdictSlowDown  deviceVerdict = "slow_down"
	verdictTransient deviceVerdict = "transient"
	verdictExpired   deviceVerdict = "expired"
	verdictDenied    deviceVerdict = "denied"
	verdictApproved  deviceVerdict = "approved"
)

// deviceMethods are the device providers the reference's OAuthModal lists
// (OAuthModal.js:288-297, less the kimi-coding alias). rfc names the ones whose
// token endpoint speaks RFC 8628 errors (slow_down, expired_token,
// access_denied); kilo signals expiry and denial by status; CodeBuddy and Qoder
// have neither and end at the attempt's deadline.
var deviceMethods = []struct {
	provider string
	rfc      bool
	// statusVerdicts: expiry and denial are reported (RFC names or Kilo's 410/403).
	statusVerdicts bool
	// reportsIdentity: the approval names the account, so two accounts are told
	// apart without the operator's identity.
	reportsIdentity bool
}{
	{provider: "github", rfc: true, statusVerdicts: true, reportsIdentity: true},
	{provider: "grok-cli", rfc: true, statusVerdicts: true, reportsIdentity: true},
	{provider: "kimi", rfc: true, statusVerdicts: true},
	{provider: "kiro", rfc: true, statusVerdicts: true, reportsIdentity: true},
	{provider: "kilocode", statusVerdicts: true, reportsIdentity: true},
	{provider: "codebuddy-cn"},
	{provider: "codebuddy-intl"},
	{provider: "qoder", reportsIdentity: true},
}

// deviceFixture scripts every device provider's endpoints.
type deviceFixture struct {
	mu      sync.Mutex
	verdict deviceVerdict
	// account selects which fixture account approves ("a" or "b").
	account string
	// copilotStatus is the Copilot token mint's status (0 means 200).
	copilotStatus int
	// interval is the poll interval the start reports, in seconds (0 means 5).
	interval int
	seen     []string
	headers  []http.Header
}

func (f *deviceFixture) set(verdict deviceVerdict, account string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.verdict, f.account = verdict, account
}

// polls counts requests to a provider's poll endpoint.
func (f *deviceFixture) polls(provider string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, seen := range f.seen {
		if isPollURL(provider, seen) {
			n++
		}
	}
	return n
}

func isPollURL(provider, raw string) bool {
	switch provider {
	case "github":
		return strings.HasPrefix(raw, "https://github.com/login/oauth/access_token")
	case "grok-cli":
		return strings.HasPrefix(raw, "https://auth.x.ai/oauth2/token")
	case "kimi":
		return strings.HasPrefix(raw, "https://auth.kimi.com/api/oauth/token")
	case "kiro":
		return strings.HasPrefix(raw, "https://oidc.us-east-1.amazonaws.com/token")
	case "kilocode":
		return strings.HasPrefix(raw, "https://api.kilo.ai/api/device-auth/codes/")
	case "codebuddy-cn":
		return strings.HasPrefix(raw, "https://copilot.tencent.com/v2/plugin/auth/token")
	case "codebuddy-intl":
		return strings.HasPrefix(raw, "https://www.codebuddy.ai/v2/plugin/auth/token")
	case "qoder":
		return strings.HasPrefix(raw, "https://openapi.qoder.sh/api/v1/deviceToken/poll")
	}
	return false
}

func fixtureJWT(claims map[string]any) string {
	payload, _ := json.Marshal(claims)
	return "eyJhbGciOiJub25lIn0." + base64.RawURLEncoding.EncodeToString(payload) + ".sig"
}

func (f *deviceFixture) Do(request *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	raw := request.URL.String()
	f.seen = append(f.seen, raw)
	f.headers = append(f.headers, request.Header.Clone())
	respond := func(status int, body string) (*http.Response, error) {
		return &http.Response{StatusCode: status, Header: http.Header{"Content-Type": []string{"application/json"}}, Body: io.NopCloser(strings.NewReader(body))}, nil
	}
	account := f.account
	if account == "" {
		account = "a"
	}
	email := "fixture-" + account + "@example.test"
	interval := f.interval
	if interval == 0 {
		interval = 5
	}
	token := "fixture-access-" + account
	// Start endpoints.
	switch {
	case raw == "https://github.com/login/device/code",
		raw == "https://auth.x.ai/oauth2/device/code",
		raw == "https://auth.kimi.com/api/oauth/device_authorization":
		return respond(200, fmt.Sprintf(`{"device_code":"fixture-device","user_code":"WXYZ-1234","verification_uri":"https://example.invalid/device","expires_in":600,"interval":%d}`, interval))
	case raw == "https://oidc.us-east-1.amazonaws.com/client/register":
		return respond(200, `{"clientId":"fixture-client","clientSecret":"fixture-client-secret"}`)
	case raw == "https://oidc.us-east-1.amazonaws.com/device_authorization":
		return respond(200, fmt.Sprintf(`{"deviceCode":"fixture-device","userCode":"WXYZ-1234","verificationUri":"https://example.invalid/device","verificationUriComplete":"https://example.invalid/device?user_code=WXYZ-1234","expiresIn":600,"interval":%d}`, interval))
	case raw == "https://api.kilo.ai/api/device-auth/codes" && request.Method == http.MethodPost:
		return respond(200, `{"code":"KILO-1234","verificationUrl":"https://example.invalid/kilo?code=KILO-1234","expiresIn":600}`)
	case strings.HasPrefix(raw, "https://copilot.tencent.com/v2/plugin/auth/state"),
		strings.HasPrefix(raw, "https://www.codebuddy.ai/v2/plugin/auth/state"):
		return respond(200, `{"code":0,"data":{"state":"fixture-state","authUrl":"https://example.invalid/codebuddy"}}`)
	}
	// Post-approval lookups.
	switch {
	case raw == "https://api.github.com/user":
		return respond(200, fmt.Sprintf(`{"login":"fixture-%s","email":%q,"id":7}`, account, email))
	case raw == "https://api.github.com/copilot_internal/v2/token":
		if f.copilotStatus != 0 && f.copilotStatus != 200 {
			return respond(f.copilotStatus, `{"message":"no copilot"}`)
		}
		return respond(200, `{"token":"fixture-copilot-`+account+`","expires_at":4102444800}`)
	case raw == "https://cli-chat-proxy.grok.com/v1/user":
		return respond(200, fmt.Sprintf(`{"email":%q,"userId":"u-%s"}`, email, account))
	case raw == "https://api.kilo.ai/api/profile":
		return respond(200, `{"organizations":[{"id":"org-`+account+`"}]}`)
	case raw == "https://openapi.qoder.sh/api/v1/userinfo":
		return respond(200, fmt.Sprintf(`{"name":"Fixture","email":%q}`, email))
	}
	provider := ""
	for _, method := range deviceMethods {
		if isPollURL(method.provider, raw) {
			provider = method.provider
		}
	}
	if provider == "" {
		return respond(404, `{}`)
	}
	if f.verdict == verdictTransient {
		return respond(503, `<html>upstream unavailable</html>`)
	}
	switch provider {
	case "kilocode":
		switch f.verdict {
		case verdictExpired:
			return respond(http.StatusGone, ``)
		case verdictDenied:
			return respond(http.StatusForbidden, ``)
		case verdictApproved:
			return respond(200, fmt.Sprintf(`{"status":"approved","token":%q,"userEmail":%q}`, token, email))
		}
		return respond(http.StatusAccepted, ``)
	case "codebuddy-cn", "codebuddy-intl":
		if f.verdict == verdictApproved {
			return respond(200, fmt.Sprintf(`{"code":0,"data":{"accessToken":%q,"refreshToken":"fixture-refresh-%s"}}`, token, account))
		}
		return respond(200, `{"code":11217,"msg":"retry"}`)
	case "qoder":
		if f.verdict == verdictApproved {
			return respond(200, fmt.Sprintf(`{"accessToken":%q,"refreshToken":"fixture-refresh","expireTime":4102444800000,"status":"success","userId":"u-%s"}`, token, account))
		}
		return respond(200, `{"status":"pending"}`)
	}
	// RFC 8628 providers.
	rfcError := map[deviceVerdict]string{verdictPending: "authorization_pending", verdictSlowDown: "slow_down", verdictExpired: "expired_token", verdictDenied: "access_denied"}
	if code, ok := rfcError[f.verdict]; ok {
		return respond(400, `{"error":"`+code+`"}`)
	}
	if provider == "kiro" {
		return respond(200, fmt.Sprintf(`{"accessToken":%q,"refreshToken":"fixture-refresh","expiresIn":3600}`, fixtureJWT(map[string]any{"email": email, "sub": account})))
	}
	if provider == "grok-cli" {
		return respond(200, fmt.Sprintf(`{"access_token":%q,"refresh_token":"fixture-refresh","expires_in":3600}`, token))
	}
	return respond(200, fmt.Sprintf(`{"access_token":%q,"refresh_token":"fixture-refresh","token_type":"bearer","expires_in":3600}`, token))
}

type deviceRig struct {
	h       *Handler
	cookie  *http.Cookie
	fixture *deviceFixture
	now     *time.Time
	creds   *credentials.Store
	mux     *http.ServeMux
}

func newDeviceRig(t *testing.T) *deviceRig {
	t.Helper()
	fixture := &deviceFixture{verdict: verdictPending}
	h, mux, creds, cookie := newOnboardingAPI(t, fixture)
	h.opts.Providers = registry.Builtins()
	now := time.Date(2026, 10, 27, 12, 0, 0, 0, time.UTC)
	h.oauth.now = func() time.Time { return now }
	return &deviceRig{h: h, cookie: cookie, fixture: fixture, now: &now, creds: creds, mux: mux}
}

func (r *deviceRig) advance(d time.Duration) { *r.now = r.now.Add(d) }

type deviceStart struct {
	State           string `json:"state"`
	UserCode        string `json:"userCode"`
	VerificationURL string `json:"verificationUrl"`
	IntervalSeconds int    `json:"intervalSeconds"`
	ExpiresAt       string `json:"expiresAt"`
}

func (r *deviceRig) start(t *testing.T, provider, identity string) deviceStart {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"name": "", "identity": identity})
	recorder := doJSON(t, r.mux, r.cookie, http.MethodPost, "/admin/v1/oauth/"+provider+"/device", string(body))
	if recorder.Code != http.StatusOK {
		t.Fatalf("%s device start: %d %s", provider, recorder.Code, recorder.Body.String())
	}
	var started deviceStart
	if err := json.Unmarshal(recorder.Body.Bytes(), &started); err != nil || started.State == "" {
		t.Fatalf("%s device start body=%s", provider, recorder.Body.String())
	}
	return started
}

type devicePollAnswer struct {
	code            int
	status          string
	errorCode       string
	intervalSeconds int
	connection      map[string]any
	body            string
}

func (r *deviceRig) poll(t *testing.T, provider, state string) devicePollAnswer {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"state": state})
	recorder := doJSON(t, r.mux, r.cookie, http.MethodPost, "/admin/v1/oauth/"+provider+"/poll", string(body))
	var payload struct {
		Status          string         `json:"status"`
		IntervalSeconds int            `json:"intervalSeconds"`
		Connection      map[string]any `json:"connection"`
		Error           struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	_ = json.Unmarshal(recorder.Body.Bytes(), &payload)
	return devicePollAnswer{code: recorder.Code, status: payload.Status, errorCode: payload.Error.Code, intervalSeconds: payload.IntervalSeconds, connection: payload.Connection, body: recorder.Body.String()}
}

// The operator identity each method's start sends. A method that reports its
// account is started without one where it may be (Kilo), and with the account's
// own address otherwise, so the stored identity is the account's either way.
func identityFor(provider, account string) string {
	if provider == "kilocode" {
		return ""
	}
	return "fixture-" + account + "@example.test"
}

// No method polls forever: a provider that answers pending for good is polled at
// most once per interval, and the attempt ends at its deadline (the code's
// expires_in, else the flow TTL, else the method's own 5 minutes) with
// oauth_state_invalid and no further upstream call.
func TestDeviceMethodsEndAtTheirDeadline(t *testing.T) {
	for _, method := range deviceMethods {
		t.Run(method.provider, func(t *testing.T) {
			rig := newDeviceRig(t)
			started := rig.start(t, method.provider, identityFor(method.provider, "a"))
			deadline, err := time.Parse(time.RFC3339Nano, started.ExpiresAt)
			if err != nil {
				t.Fatalf("expiresAt %q: %v", started.ExpiresAt, err)
			}
			if limit := rig.now.Add(oauthFlowTTL); deadline.After(limit) || !deadline.After(*rig.now) {
				t.Fatalf("deadline %s outside (now, now+TTL]", deadline)
			}
			interval := time.Duration(started.IntervalSeconds) * time.Second
			if interval < oauthMinPollInterval {
				t.Fatalf("interval %s below the floor", interval)
			}
			maxPolls := int(deadline.Sub(*rig.now)/interval) + 1
			ended := false
			for step := 0; step < maxPolls+2; step++ {
				rig.advance(interval)
				answer := rig.poll(t, method.provider, started.State)
				if answer.code == http.StatusBadRequest && answer.errorCode == "oauth_state_invalid" {
					ended = true
					break
				}
				if answer.code != http.StatusOK || answer.status != "pending" {
					t.Fatalf("step %d: %d %s", step, answer.code, answer.body)
				}
			}
			if !ended {
				t.Fatal("the attempt was still pending past its deadline")
			}
			if polls := rig.fixture.polls(method.provider); polls > maxPolls {
				t.Fatalf("%d upstream polls, at most %d fit the deadline", polls, maxPolls)
			}
			before := rig.fixture.polls(method.provider)
			rig.advance(interval)
			if answer := rig.poll(t, method.provider, started.State); answer.errorCode != "oauth_state_invalid" || rig.fixture.polls(method.provider) != before {
				t.Fatalf("poll after the deadline: %s, upstream polls %d -> %d", answer.body, before, rig.fixture.polls(method.provider))
			}
		})
	}
}

// slow_down lengthens the interval by five seconds up to thirty (RFC 8628 3.5,
// OAuthModal.js:184-186), the pending answer says so, and a poll before the new
// interval never reaches the provider.
func TestDeviceSlowDownLengthensTheInterval(t *testing.T) {
	for _, method := range deviceMethods {
		if !method.rfc {
			continue
		}
		t.Run(method.provider, func(t *testing.T) {
			rig := newDeviceRig(t)
			started := rig.start(t, method.provider, identityFor(method.provider, "a"))
			rig.fixture.set(verdictSlowDown, "a")
			interval := time.Duration(started.IntervalSeconds) * time.Second
			want := started.IntervalSeconds
			for round := 0; round < 6; round++ {
				rig.advance(interval)
				polls := rig.fixture.polls(method.provider)
				answer := rig.poll(t, method.provider, started.State)
				want += 5
				if want > 30 {
					want = 30
				}
				if answer.code != http.StatusOK || answer.status != "pending" || answer.intervalSeconds != want || rig.fixture.polls(method.provider) != polls+1 {
					t.Fatalf("round %d: %s want interval %d", round, answer.body, want)
				}
				interval = time.Duration(answer.intervalSeconds) * time.Second
				// Just short of the new interval: throttled, the provider is not asked.
				rig.advance(interval - time.Second)
				if early := rig.poll(t, method.provider, started.State); early.status != "pending" || early.intervalSeconds != want || rig.fixture.polls(method.provider) != polls+1 {
					t.Fatalf("round %d early poll reached upstream: %s", round, early.body)
				}
				rig.advance(-(interval - time.Second))
			}
			// The attempt is still alive and finishes normally.
			rig.fixture.set(verdictApproved, "a")
			rig.advance(interval)
			if answer := rig.poll(t, method.provider, started.State); answer.status != "connected" {
				t.Fatalf("approval after slow_down: %s", answer.body)
			}
		})
	}
}

// The 30 s cap never lowers a provider's own interval: one that asks for 45 s
// gets 50 s after a slow_down (RFC 8628 3.5: five seconds more), then 55 s, and
// a poll before that interval never reaches it.
func TestDeviceSlowDownNeverShortensALongProviderInterval(t *testing.T) {
	for _, method := range deviceMethods {
		if !method.rfc {
			continue
		}
		t.Run(method.provider, func(t *testing.T) {
			rig := newDeviceRig(t)
			rig.fixture.interval = 45
			started := rig.start(t, method.provider, identityFor(method.provider, "a"))
			if started.IntervalSeconds != 45 {
				t.Fatalf("start interval %d, want the provider's 45", started.IntervalSeconds)
			}
			rig.fixture.set(verdictSlowDown, "a")
			rig.advance(45 * time.Second)
			for round, want := range []int{50, 55} {
				polls := rig.fixture.polls(method.provider)
				if answer := rig.poll(t, method.provider, started.State); answer.status != "pending" || answer.intervalSeconds != want || rig.fixture.polls(method.provider) != polls+1 {
					t.Fatalf("round %d slow_down on a long interval: %s, want %d", round, answer.body, want)
				}
				// 45 s later (the provider's own interval) is still too soon.
				rig.advance(45 * time.Second)
				if answer := rig.poll(t, method.provider, started.State); answer.intervalSeconds != want || rig.fixture.polls(method.provider) != polls+1 {
					t.Fatalf("round %d: a poll before %d s reached the provider: %s", round, want, answer.body)
				}
				rig.advance(time.Duration(want-45) * time.Second)
			}
		})
	}
}

func TestSlowedIntervalOnlyLengthens(t *testing.T) {
	cases := []struct{ current, base, want time.Duration }{
		{0, 0, 7 * time.Second},
		{time.Second, time.Second, 7 * time.Second},
		{5 * time.Second, 5 * time.Second, 10 * time.Second},
		{28 * time.Second, 5 * time.Second, 30 * time.Second},
		{30 * time.Second, 5 * time.Second, 30 * time.Second},
		{30 * time.Second, 30 * time.Second, 30 * time.Second},
		{45 * time.Second, 45 * time.Second, 50 * time.Second},
		{50 * time.Second, 45 * time.Second, 55 * time.Second},
	}
	for _, c := range cases {
		got := slowedInterval(c.current, c.base)
		if got != c.want || got < pollInterval(c.current) || got < pollInterval(c.base) {
			t.Fatalf("slowedInterval(%s, base %s) = %s, want %s", c.current, c.base, got, c.want)
		}
	}
}

// A provider that is briefly unavailable (HTTP 503 with an HTML body) is a
// retryable oauth_poll_failed: the attempt stays open and finishes once the
// provider answers.
func TestDeviceTransientErrorKeepsTheAttemptOpen(t *testing.T) {
	for _, method := range deviceMethods {
		t.Run(method.provider, func(t *testing.T) {
			rig := newDeviceRig(t)
			started := rig.start(t, method.provider, identityFor(method.provider, "a"))
			interval := time.Duration(started.IntervalSeconds) * time.Second
			rig.fixture.set(verdictTransient, "a")
			for i := 0; i < 3; i++ {
				rig.advance(interval)
				answer := rig.poll(t, method.provider, started.State)
				if answer.code != http.StatusBadGateway || answer.errorCode != "oauth_poll_failed" {
					t.Fatalf("transient %d: %s", i, answer.body)
				}
				if strings.Contains(answer.body, "upstream unavailable") {
					t.Fatal("the provider's error body reached the browser")
				}
			}
			rig.fixture.set(verdictApproved, "a")
			rig.advance(interval)
			if answer := rig.poll(t, method.provider, started.State); answer.status != "connected" {
				t.Fatalf("approval after transient errors: %s", answer.body)
			}
		})
	}
}

// expired_token and access_denied (Kilo: 410 and 403) end the attempt with their
// own codes, save nothing, and no later poll reaches the provider.
func TestDeviceExpiryAndDenialEndTheAttempt(t *testing.T) {
	for _, method := range deviceMethods {
		if !method.statusVerdicts {
			continue
		}
		for verdict, code := range map[deviceVerdict]string{verdictExpired: "oauth_expired", verdictDenied: "oauth_denied"} {
			t.Run(method.provider+"/"+string(verdict), func(t *testing.T) {
				rig := newDeviceRig(t)
				started := rig.start(t, method.provider, identityFor(method.provider, "a"))
				interval := time.Duration(started.IntervalSeconds) * time.Second
				rig.fixture.set(verdict, "a")
				rig.advance(interval)
				if answer := rig.poll(t, method.provider, started.State); answer.code != http.StatusBadRequest || answer.errorCode != code {
					t.Fatalf("%s: %s", verdict, answer.body)
				}
				polls := rig.fixture.polls(method.provider)
				rig.fixture.set(verdictApproved, "a")
				rig.advance(interval)
				if answer := rig.poll(t, method.provider, started.State); answer.errorCode != "oauth_state_invalid" || rig.fixture.polls(method.provider) != polls {
					t.Fatalf("attempt survived %s: %s", verdict, answer.body)
				}
				if connections, _ := rig.creds.ListConnections(context.Background(), method.provider); len(connections) != 0 {
					t.Fatalf("%s saved %d connections", verdict, len(connections))
				}
			})
		}
	}
}

// Cancel ends every method's attempt: it is confirmed, a later approval is never
// polled for or saved.
func TestDeviceCancelEndsEveryMethod(t *testing.T) {
	for _, method := range deviceMethods {
		t.Run(method.provider, func(t *testing.T) {
			rig := newDeviceRig(t)
			started := rig.start(t, method.provider, identityFor(method.provider, "a"))
			body, _ := json.Marshal(map[string]string{"state": started.State})
			if recorder := doJSON(t, rig.mux, rig.cookie, http.MethodPost, "/admin/v1/oauth/"+method.provider+"/cancel", string(body)); recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"cancelled"`) {
				t.Fatalf("cancel: %d %s", recorder.Code, recorder.Body.String())
			}
			rig.fixture.set(verdictApproved, "a")
			rig.advance(time.Duration(started.IntervalSeconds) * time.Second)
			if answer := rig.poll(t, method.provider, started.State); answer.errorCode != "oauth_state_invalid" || rig.fixture.polls(method.provider) != 0 {
				t.Fatalf("poll after cancel: %s polls=%d", answer.body, rig.fixture.polls(method.provider))
			}
			if connections, _ := rig.creds.ListConnections(context.Background(), method.provider); len(connections) != 0 {
				t.Fatalf("cancelled attempt saved %d connections", len(connections))
			}
		})
	}
}

// approve runs one sign-in of the given account to completion.
func (r *deviceRig) approve(t *testing.T, provider, account string) map[string]any {
	t.Helper()
	r.fixture.set(verdictPending, account)
	started := r.start(t, provider, identityFor(provider, account))
	r.fixture.set(verdictApproved, account)
	r.advance(time.Duration(started.IntervalSeconds) * time.Second)
	answer := r.poll(t, provider, started.State)
	if answer.code != http.StatusOK || answer.status != "connected" || answer.connection == nil {
		t.Fatalf("%s approval of %s: %s", provider, account, answer.body)
	}
	for _, secret := range []string{"fixture-access-" + account, "fixture-refresh", "fixture-device", "fixture-client-secret", "fixture-copilot-" + account} {
		if strings.Contains(answer.body, secret) {
			t.Fatalf("%s approval echoed a credential", provider)
		}
	}
	return answer.connection
}

// Two accounts stay two connections, each with its own credential; signing the
// first in again renews its row in place (outcome updated, name and priority
// kept) as the reference's oauth duplicate rule does (connectionsRepo.js:228-283),
// and the second account's row is untouched.
func TestDeviceTwoAccountsStayIsolatedAndADuplicateRenewsItsRow(t *testing.T) {
	for _, method := range deviceMethods {
		t.Run(method.provider, func(t *testing.T) {
			rig := newDeviceRig(t)
			first := rig.approve(t, method.provider, "a")
			second := rig.approve(t, method.provider, "b")
			if first["outcome"] != "added" || second["outcome"] != "added" || first["id"] == second["id"] {
				t.Fatalf("two accounts: %v / %v", first, second)
			}
			if first["identity"] != "fixture-a@example.test" || second["identity"] != "fixture-b@example.test" {
				t.Fatalf("identities %v / %v", first["identity"], second["identity"])
			}
			ctx := context.Background()
			stored, _ := rig.creds.ListConnections(ctx, method.provider)
			byID := map[string]credentials.Connection{}
			for _, connection := range stored {
				byID[connection.ID] = connection
			}
			secondBefore := byID[second["id"].(string)]
			// Rename the first so a renewal that rewrote the name would show.
			firstRow := byID[first["id"].(string)]
			firstRow.Name = "Renamed by operator"
			firstRow.Secret = credentials.Secret{}
			if _, err := rig.creds.PutConnection(ctx, firstRow); err != nil {
				t.Fatal(err)
			}
			again := rig.approve(t, method.provider, "a")
			if again["outcome"] != "updated" || again["id"] != first["id"] || again["name"] != "Renamed by operator" {
				t.Fatalf("duplicate sign-in: %v", again)
			}
			stored, _ = rig.creds.ListConnections(ctx, method.provider)
			if len(stored) != 2 {
				t.Fatalf("duplicate sign-in left %d connections", len(stored))
			}
			for _, connection := range stored {
				if connection.ID == secondBefore.ID {
					if connection.Secret.AccessToken != secondBefore.Secret.AccessToken || connection.Name != secondBefore.Name || connection.Priority != secondBefore.Priority {
						t.Fatal("the other account's connection changed")
					}
					continue
				}
				if connection.Priority != firstRow.Priority {
					t.Fatalf("renewal moved the row: priority %d -> %d", firstRow.Priority, connection.Priority)
				}
			}
		})
	}
}

// Methods whose approval names no account (Kimi, CodeBuddy) ask the operator
// for one up front; the others, which usually learn it, ask only where the
// approval cannot be relied on to name it.
func TestDeviceJourneysAskIdentityOnlyWhereNoneIsReported(t *testing.T) {
	rig := newDeviceRig(t)
	for _, provider := range []string{"kimi", "codebuddy-cn", "codebuddy-intl"} {
		if recorder := doJSON(t, rig.mux, rig.cookie, http.MethodPost, "/admin/v1/oauth/"+provider+"/device", `{"name":"","identity":""}`); recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s without identity: %d %s", provider, recorder.Code, recorder.Body.String())
		}
	}
	if recorder := doJSON(t, rig.mux, rig.cookie, http.MethodPost, "/admin/v1/oauth/kilocode/device", `{"name":"","identity":""}`); recorder.Code != http.StatusOK {
		t.Fatalf("kilocode without identity: %d %s", recorder.Code, recorder.Body.String())
	}
}

// CodeBuddy CN and international are separate providers: each signs in against
// its own host and X-Domain, the same address on both makes two connections on
// two nodes, and neither poll ever reaches the other region.
func TestCodeBuddyRegionsStaySeparate(t *testing.T) {
	rig := newDeviceRig(t)
	cn := rig.approve(t, "codebuddy-cn", "a")
	intl := rig.approve(t, "codebuddy-intl", "a")
	if cn["id"] == intl["id"] || cn["nodeId"] == intl["nodeId"] || cn["providerId"] != "codebuddy-cn" || intl["providerId"] != "codebuddy-intl" {
		t.Fatalf("regions share a connection: %v / %v", cn, intl)
	}
	if cn["outcome"] != "added" || intl["outcome"] != "added" {
		t.Fatalf("the other region's sign-in was taken as a duplicate: %v / %v", cn["outcome"], intl["outcome"])
	}
	rig.fixture.mu.Lock()
	defer rig.fixture.mu.Unlock()
	for i, seen := range rig.fixture.seen {
		domain := rig.fixture.headers[i].Get("X-Domain")
		switch {
		case strings.HasPrefix(seen, "https://copilot.tencent.com/"):
			if domain != "copilot.tencent.com" {
				t.Fatalf("CN request %s sent X-Domain %q", seen, domain)
			}
		case strings.HasPrefix(seen, "https://www.codebuddy.ai/"):
			if domain != "www.codebuddy.ai" {
				t.Fatalf("intl request %s sent X-Domain %q", seen, domain)
			}
		default:
			t.Fatalf("CodeBuddy sign-in reached %s", seen)
		}
	}
	ctx := context.Background()
	for _, provider := range []string{"codebuddy-cn", "codebuddy-intl"} {
		stored, _ := rig.creds.ListConnections(ctx, provider)
		if len(stored) != 1 || stored[0].Secret.Expiry.IsZero() {
			t.Fatalf("%s stored %+v; a reply without expiresIn lasts a day (codebuddy-cn.js:75)", provider, stored)
		}
	}
}

// The Copilot token is minted at approval. An account without Copilot
// entitlement (403 from copilot_internal/v2/token) is still connected, as the
// reference stores it (github.js:56-65), and the result says the Copilot token
// is missing; an entitled one stores the token sealed and never echoes it.
func TestGitHubCopilotEntitlementIsReported(t *testing.T) {
	for _, status := range []int{200, http.StatusForbidden} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			rig := newDeviceRig(t)
			rig.fixture.copilotStatus = status
			connection := rig.approve(t, "github", "a")
			missing, ok := connection["copilotMissing"].(bool)
			if !ok || missing != (status != 200) {
				t.Fatalf("copilotMissing=%v for status %d", connection["copilotMissing"], status)
			}
			stored, _ := rig.creds.ListConnections(context.Background(), "github")
			if len(stored) != 1 {
				t.Fatalf("stored %d", len(stored))
			}
			data := stored[0].Secret.ProviderData
			if (data["copilotToken"] != "") != (status == 200) || data["githubLogin"] != "fixture-a" {
				t.Fatalf("provider data %v", data)
			}
		})
	}
}

// Storing a Qoder credential never makes Qoder served: after a device sign-in
// the connection is saved and enabled, and the build's readiness still says
// unavailable and not served (registry/readiness.go, PARITY 6.23).
func TestQoderCredentialDoesNotImplyServingReadiness(t *testing.T) {
	rig := newDeviceRig(t)
	connection := rig.approve(t, "qoder", "a")
	if connection["enabled"] != true || connection["credentialConfigured"] != true {
		t.Fatalf("qoder connection %v", connection)
	}
	items := readinessItems(t, rig.h, rig.mux)
	qoder, ok := items["qoder"]
	if !ok {
		t.Fatal("qoder missing from the provider read model")
	}
	if qoder.EnabledAccounts != 1 || qoder.Readiness.Status != "unavailable" || qoder.Readiness.Served == nil || *qoder.Readiness.Served {
		t.Fatalf("qoder readiness with a stored credential: %+v", qoder)
	}
	if spec, _ := rig.h.providerSpec("qoder"); spec.Readiness.Served() {
		t.Fatal("the qoder spec reads as served")
	}
}
