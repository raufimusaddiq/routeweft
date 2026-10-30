package api

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/adminauth"
	"github.com/raufimusaddiq/routeweft/internal/clientip"
	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
	"github.com/raufimusaddiq/routeweft/internal/store/storetest"
)

// #283 / #281 (SPEC 12.3): with ROUTEWEFT_TRUSTED_PROXIES set, the login and
// password-change throttle keys on the client a trusted proxy vouches for, and on
// nothing a peer that is not a trusted proxy says.

// proxyAPI returns a mounted control API trusting the given proxies.
func proxyAPI(t *testing.T, trusted string) *http.ServeMux {
	t.Helper()
	prefixes, err := clientip.ParseTrusted(trusted)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	dbPath := filepath.Join(t.TempDir(), "routeweft.sqlite")
	storetest.Seed(t, dbPath)
	store, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	if err := migrations.NewRunner(store.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := bootstrapOperator(ctx, store.DB()); err != nil {
		t.Fatal(err)
	}
	handler := New(Options{Accounts: adminauth.NewStore(store.DB()), Sessions: adminauth.NewSessionManager(time.Hour), Settings: &fakeSettings{values: map[string]string{}}, TrustedProxies: prefixes})
	// A frozen clock so no bucket window expires mid-test.
	handler.throttler.now = func() time.Time { return time.Unix(100, 0) }
	mux := http.NewServeMux()
	handler.Attach(mux)
	return mux
}

func loginFrom(mux *http.ServeMux, peer, forwarded, password string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/admin/v1/auth/login", strings.NewReader(fmt.Sprintf(`{"username":"operator","password":%q}`, password)))
	request.RemoteAddr = peer
	if forwarded != "" {
		request.Header.Set("X-Forwarded-For", forwarded)
	}
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	return recorder
}

func TestTrustedProxyGivesEachClientItsOwnLoginBucket(t *testing.T) {
	mux := proxyAPI(t, "10.0.0.0/8")
	const proxy = "10.0.0.2:5000"
	// Client A fails until locked. Each attempt also forges a different leftmost
	// identity; the proxy appended the real address on the right, which is the one
	// that counts, so the forgery cannot spread the failures across buckets.
	for i := 0; i < loginMaxFailures; i++ {
		if got := loginFrom(mux, proxy, fmt.Sprintf("192.0.2.%d, 198.51.100.1", i+1), "wrong").Code; got != http.StatusUnauthorized {
			t.Fatalf("attempt %d status=%d want 401", i, got)
		}
	}
	for _, forged := range []string{"198.51.100.1", "192.0.2.99, 198.51.100.1"} {
		recorder := loginFrom(mux, proxy, forged, "s3cret")
		if recorder.Code != http.StatusTooManyRequests || recorder.Header().Get("Retry-After") == "" {
			t.Fatalf("client A (%q) status=%d want 429 with Retry-After", forged, recorder.Code)
		}
	}
	// Client B behind the same proxy is a different bucket and still logs in: the
	// shared-bucket lockout of RUNBOOK 7 is gone.
	if got := loginFrom(mux, proxy, "198.51.100.2", "s3cret").Code; got != http.StatusOK {
		t.Fatalf("client B behind the same proxy status=%d want 200", got)
	}
	// The proxy's own address is not client A's bucket either.
	if got := loginFrom(mux, proxy, "", "s3cret").Code; got != http.StatusOK {
		t.Fatalf("request with no forwarded identity status=%d want 200 (the proxy's own bucket)", got)
	}
}

// The security property of #281: a peer that is not a trusted proxy cannot pick
// its throttle bucket, whether to escape its own lockout or to land failures in a
// victim's bucket, even while a trusted proxy is configured.
func TestUntrustedPeerCannotChooseItsThrottleBucket(t *testing.T) {
	mux := proxyAPI(t, "10.0.0.0/8")
	const attacker = "203.0.113.7:4444"
	// Rotating the claimed identity, including the victim's and a trusted proxy's.
	claims := []string{"192.0.2.1", "192.0.2.2", "198.51.100.2", "10.0.0.2", "192.0.2.5, 198.51.100.2", "192.0.2.6", "192.0.2.7", "192.0.2.8"}
	if len(claims) != loginMaxFailures {
		t.Fatalf("test needs %d claims", loginMaxFailures)
	}
	for i, claim := range claims {
		if got := loginFrom(mux, attacker, claim, "wrong").Code; got != http.StatusUnauthorized {
			t.Fatalf("attempt %d status=%d want 401", i, got)
		}
	}
	// Rotation did not escape: the attacker's own socket bucket is full.
	for _, claim := range []string{"192.0.2.100", "198.51.100.2", ""} {
		if got := loginFrom(mux, attacker, claim, "s3cret").Code; got != http.StatusTooManyRequests {
			t.Fatalf("attacker claiming %q escaped its lockout: status=%d", claim, got)
		}
	}
	// And the failures were not charged to the victim named in the claims, who
	// reaches the server through the trusted proxy.
	if got := loginFrom(mux, "10.0.0.2:5000", "198.51.100.2", "s3cret").Code; got != http.StatusOK {
		t.Fatalf("victim framed by an untrusted peer: status=%d want 200", got)
	}
}

func TestMalformedForwardedIdentityFromATrustedProxyUsesTheProxyBucket(t *testing.T) {
	mux := proxyAPI(t, "10.0.0.0/8")
	const proxy = "10.0.0.2:5000"
	for i := 0; i < loginMaxFailures; i++ {
		loginFrom(mux, proxy, fmt.Sprintf("not-an-ip-%d", i), "wrong")
	}
	// Garbage cannot pick a fresh bucket each time: it all lands on the proxy's.
	if got := loginFrom(mux, proxy, "still-not-an-ip", "s3cret").Code; got != http.StatusTooManyRequests {
		t.Fatalf("garbage identity escaped: status=%d", got)
	}
	// A well-formed client behind the proxy is unaffected.
	if got := loginFrom(mux, proxy, "198.51.100.3", "s3cret").Code; got != http.StatusOK {
		t.Fatalf("well-formed client status=%d want 200", got)
	}
}

// Password change shares the bucket with login and uses the same key, so a client
// cannot use it to guess the current password past a lockout.
func TestPasswordChangeThrottleUsesTheClientBehindATrustedProxy(t *testing.T) {
	mux := proxyAPI(t, "10.0.0.0/8")
	const proxy = "10.0.0.2:5000"
	login := loginFrom(mux, proxy, "198.51.100.9", "s3cret")
	if login.Code != http.StatusOK || len(login.Result().Cookies()) == 0 {
		t.Fatalf("login status=%d", login.Code)
	}
	cookie := login.Result().Cookies()[0]
	change := func(forwarded, current string) int {
		request := httptest.NewRequest(http.MethodPost, "/admin/v1/auth/password", strings.NewReader(fmt.Sprintf(`{"currentPassword":%q,"newPassword":"another-password"}`, current)))
		request.RemoteAddr = proxy
		request.Header.Set("X-Forwarded-For", forwarded)
		request.AddCookie(cookie)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		return recorder.Code
	}
	for i := 0; i < loginMaxFailures; i++ {
		if got := change("198.51.100.9", "wrong"); got != http.StatusUnauthorized {
			t.Fatalf("attempt %d status=%d want 401", i, got)
		}
	}
	if got := change("198.51.100.9", "s3cret"); got != http.StatusTooManyRequests {
		t.Fatalf("locked client status=%d want 429", got)
	}
	// A different client behind the proxy is not locked by it.
	if got := change("198.51.100.10", "wrong"); got != http.StatusUnauthorized {
		t.Fatalf("other client status=%d want 401 (its own bucket)", got)
	}
}

// Password policy (#281). The oracle enforces none beyond "a value was sent": the
// server only tests newPassword for truthiness (`src/app/api/settings/route.js:45`)
// and the profile page only checks the confirmation matches (`profile/page.js:169`).
// Routeweft matches: any non-empty password is accepted, an empty one is refused,
// and the current password is always required once one is set (route.js:50-62).
func TestPasswordPolicyMatchesTheOracleNoMinimumLength(t *testing.T) {
	mux := proxyAPI(t, "")
	login := loginFrom(mux, "203.0.113.7:1", "", "s3cret")
	cookie := login.Result().Cookies()[0]
	change := func(current, next string) int {
		request := httptest.NewRequest(http.MethodPost, "/admin/v1/auth/password", strings.NewReader(fmt.Sprintf(`{"currentPassword":%q,"newPassword":%q}`, current, next)))
		request.RemoteAddr = "203.0.113.7:1"
		request.AddCookie(cookie)
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		return recorder.Code
	}
	if got := change("s3cret", ""); got != http.StatusBadRequest {
		t.Fatalf("empty new password status=%d want 400", got)
	}
	if got := change("", "x"); got != http.StatusUnauthorized {
		t.Fatalf("missing current password status=%d want 401", got)
	}
	if got := change("s3cret", "x"); got != http.StatusNoContent {
		t.Fatalf("one-character new password status=%d want 204 (the oracle has no minimum)", got)
	}
	if got := loginFrom(mux, "203.0.113.7:2", "", "x").Code; got != http.StatusOK {
		t.Fatalf("login with the new one-character password status=%d", got)
	}
}
