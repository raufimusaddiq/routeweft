package api

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

// Spending a Codex reset credit is one explicit, confirmed action (#226): it
// needs confirm:true, it reaches Codex once per request, and an outcome that
// cannot be known is reported as such and never sent again.

func codexCreditsRig(t *testing.T) (*Handler, *http.ServeMux, *http.Cookie, string, *[]string) {
	t.Helper()
	h, mux, store, creds, cookie := newProvidersAPI(t, false, nil)
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	node, err := creds.PutNode(ctx, credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: "codex", Name: "Codex", Transports: []string{"openai-responses"}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := creds.PutConnection(ctx, credentials.Connection{NodeID: node.ID, Name: "fixture", Identity: "fixture", AuthKind: credentials.AuthOAuth, Enabled: true, Secret: credentials.Secret{AccessToken: "fixture-secret"}})
	if err != nil {
		t.Fatal(err)
	}
	forgotten := &[]string{}
	h.opts.QuotaForget = func(id string) { *forgotten = append(*forgotten, id) }
	return h, mux, cookie, "/admin/v1/quota/" + c.ID + "/codex-reset-credits", forgotten
}

// consumeServer answers the consume call through Go's real HTTP transport, so a
// replay by the transport itself would be counted too. It records each redeem id.
func consumeServer(t *testing.T, answer func(http.ResponseWriter)) (*http.Client, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var ids []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]string
		_ = json.NewDecoder(r.Body).Decode(&payload)
		mu.Lock()
		ids = append(ids, payload["redeem_request_id"])
		mu.Unlock()
		answer(w)
	}))
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	base := server.Client().Transport
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
		return base.RoundTrip(r)
	})}
	return client, func() []string { mu.Lock(); defer mu.Unlock(); return append([]string(nil), ids...) }
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCodexResetCreditSpendNeedsExplicitConfirmation(t *testing.T) {
	h, mux, cookie, path, _ := codexCreditsRig(t)
	calls := 0
	h.opts.OAuthClient = creditsHTTP(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"code":"reset","windows_reset":1}`))}, nil
	})
	for _, body := range []string{``, `{}`, `{"confirm":false}`, `{"confirm":"true"}`, `{"confirm":1}`, `{"redeem_request_id":"client-chosen"}`} {
		got := doJSON(t, mux, cookie, http.MethodPost, path, body)
		if got.Code != http.StatusBadRequest {
			t.Errorf("body %q: %d %s", body, got.Code, got.Body)
		}
	}
	if calls != 0 {
		t.Fatalf("a spend without confirm:true reached Codex %d times", calls)
	}
}

// Each confirmed request spends at most one credit under its own server id; a
// client cannot choose or reuse the id.
func TestCodexResetCreditOneSpendPerConfirmedRequest(t *testing.T) {
	h, mux, cookie, path, forgotten := codexCreditsRig(t)
	client, ids := consumeServer(t, func(w http.ResponseWriter) { io.WriteString(w, `{"code":"reset","windows_reset":1}`) })
	h.opts.OAuthClient = client
	for i := 0; i < 2; i++ {
		if got := doJSON(t, mux, cookie, http.MethodPost, path, `{"confirm":true,"redeem_request_id":"client-chosen"}`); got.Code != http.StatusOK {
			t.Fatalf("spend %d: %d %s", i, got.Code, got.Body)
		}
	}
	sent := ids()
	if len(sent) != 2 || sent[0] == sent[1] || sent[0] == "client-chosen" || sent[1] == "client-chosen" {
		t.Fatalf("redeem ids %v", sent)
	}
	if len(*forgotten) != 2 {
		t.Fatalf("cached quota forgotten %d times, want 2", len(*forgotten))
	}
}

// An outcome Codex did not report is a 502, sent once, never retried, and the
// cached balance is kept because nothing is known to have changed.
func TestCodexResetCreditAmbiguousOutcomeIsNeverRetried(t *testing.T) {
	for _, tc := range []struct {
		name   string
		answer func(http.ResponseWriter)
	}{
		{"connection dropped", func(w http.ResponseWriter) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				conn.Close()
			}
		}},
		{"server error", func(w http.ResponseWriter) { w.WriteHeader(http.StatusBadGateway) }},
		{"unknown code", func(w http.ResponseWriter) { io.WriteString(w, `{"code":"pending","windows_reset":0}`) }},
		{"not json", func(w http.ResponseWriter) { io.WriteString(w, `<html>gateway</html>`) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, mux, cookie, path, forgotten := codexCreditsRig(t)
			client, ids := consumeServer(t, tc.answer)
			h.opts.OAuthClient = client
			got := doJSON(t, mux, cookie, http.MethodPost, path, `{"confirm":true}`)
			if got.Code != http.StatusBadGateway {
				t.Fatalf("%d %s", got.Code, got.Body)
			}
			if n := len(ids()); n != 1 {
				t.Fatalf("consume sent %d times, want exactly 1", n)
			}
			if len(*forgotten) != 0 {
				t.Fatal("an unknown outcome dropped the cached balance")
			}
			if strings.Contains(got.Body.String(), "fixture-secret") {
				t.Fatal("credential in the reply")
			}
		})
	}
}

// A refusal is definite: nothing was spent, and its status says so.
func TestCodexResetCreditRefusalKeepsItsStatus(t *testing.T) {
	for _, tc := range []struct {
		upstream, want int
		code           string
	}{
		{http.StatusTooManyRequests, http.StatusTooManyRequests, ""},
		{http.StatusBadRequest, http.StatusBadRequest, ""},
		{http.StatusUnauthorized, http.StatusForbidden, "reauth_required"},
		{http.StatusForbidden, http.StatusForbidden, "reauth_required"},
	} {
		h, mux, cookie, path, forgotten := codexCreditsRig(t)
		client, ids := consumeServer(t, func(w http.ResponseWriter) {
			w.WriteHeader(tc.upstream)
			io.WriteString(w, `{"code":"rejected"}`)
		})
		h.opts.OAuthClient = client
		got := doJSON(t, mux, cookie, http.MethodPost, path, `{"confirm":true}`)
		if got.Code != tc.want || !strings.Contains(got.Body.String(), tc.code) || len(ids()) != 1 || len(*forgotten) != 0 {
			t.Errorf("upstream %d: %d %s (sent %d)", tc.upstream, got.Code, got.Body, len(ids()))
		}
	}
}

// retryRig is codexCreditsRig with a refreshable credential and a Codex that
// answers each consume by the bearer it carries.
func retryRig(t *testing.T, refreshToken, authType string, exchange func() (credentials.Secret, error), answer func(auth string, w http.ResponseWriter)) (*http.ServeMux, *http.Cookie, string, *[]string, *[][2]string, *int) {
	t.Helper()
	return retryRigCtx(t, refreshToken, authType, nil, func(context.Context) (credentials.Secret, error) { return exchange() }, answer)
}

// retryRigCtx hands the exchange the context the registry gives a real refresh,
// so a test can honor its deadline as an HTTP exchange does.
func retryRigCtx(t *testing.T, refreshToken, authType string, providerData map[string]string, exchange func(context.Context) (credentials.Secret, error), answer func(auth string, w http.ResponseWriter)) (*http.ServeMux, *http.Cookie, string, *[]string, *[][2]string, *int) {
	t.Helper()
	h, mux, store, creds, cookie := newProvidersAPI(t, false, nil)
	t.Cleanup(func() { store.Close() })
	ctx := context.Background()
	node, err := creds.PutNode(ctx, credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: "codex", Name: "Codex", Transports: []string{"openai-responses"}})
	if err != nil {
		t.Fatal(err)
	}
	c, err := creds.PutConnection(ctx, credentials.Connection{NodeID: node.ID, Name: "fixture", Identity: "fixture", AuthKind: credentials.AuthOAuth, Enabled: true, Secret: credentials.Secret{AccessToken: "stale-secret", RefreshToken: refreshToken, AuthType: authType, ProviderData: providerData}})
	if err != nil {
		t.Fatal(err)
	}
	exchanges := 0
	h.opts.CredentialRegistry.SetExchangerResolver(func(credentials.Connection) credentials.Exchanger {
		return func(ctx context.Context, _ credentials.Secret) (credentials.Secret, error) {
			exchanges++
			return exchange(ctx)
		}
	})
	forgotten := &[]string{}
	h.opts.QuotaForget = func(id string) { *forgotten = append(*forgotten, id) }
	var mu sync.Mutex
	seen := &[][2]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]string
		_ = json.NewDecoder(r.Body).Decode(&payload)
		mu.Lock()
		*seen = append(*seen, [2]string{payload["redeem_request_id"], r.Header.Get("Authorization")})
		mu.Unlock()
		answer(r.Header.Get("Authorization"), w)
	}))
	t.Cleanup(server.Close)
	target, _ := url.Parse(server.URL)
	h.opts.OAuthClient = &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		r = r.Clone(r.Context())
		r.URL.Scheme, r.URL.Host = target.Scheme, target.Host
		return server.Client().Transport.RoundTrip(r)
	})}
	return mux, cookie, "/admin/v1/quota/" + c.ID + "/codex-reset-credits", forgotten, seen, &exchanges
}

func freshSecret() (credentials.Secret, error) {
	return credentials.Secret{AccessToken: "fresh-secret", RefreshToken: "r2"}, nil
}

func rejectStale(auth string, w http.ResponseWriter) {
	if auth == "Bearer stale-secret" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	io.WriteString(w, `{"code":"reset","windows_reset":1}`)
}

// A credential refusal is definite, so the spend is replayed once with the same
// redeem id and a forced refresh (codex-reset-credits/route.js:136-147).
func TestCodexResetCreditRefusedCredentialRetriesOnceWithSameIDAndFreshToken(t *testing.T) {
	mux, cookie, path, forgotten, seen, exchanges := retryRig(t, "r1", "", freshSecret, rejectStale)
	got := doJSON(t, mux, cookie, http.MethodPost, path, `{"confirm":true}`)
	if got.Code != http.StatusOK {
		t.Fatalf("%d %s", got.Code, got.Body)
	}
	if len(*seen) != 2 || (*seen)[0][0] == "" || (*seen)[0][0] != (*seen)[1][0] || (*seen)[1][1] != "Bearer fresh-secret" || *exchanges != 1 {
		t.Fatalf("requests %v, exchanges %d", *seen, *exchanges)
	}
	if len(*forgotten) != 1 {
		t.Fatalf("cached quota forgotten %d times, want 1", len(*forgotten))
	}
	if strings.Contains(got.Body.String(), "fresh-secret") || strings.Contains(got.Body.String(), "stale-secret") {
		t.Fatal("credential in the reply")
	}
}

// The retry is one, never a loop: a second refusal is reported and nothing more
// is sent.
func TestCodexResetCreditSecondRefusalIsNotRetriedAgain(t *testing.T) {
	mux, cookie, path, forgotten, seen, exchanges := retryRig(t, "r1", "", freshSecret, func(_ string, w http.ResponseWriter) { w.WriteHeader(http.StatusUnauthorized) })
	got := doJSON(t, mux, cookie, http.MethodPost, path, `{"confirm":true}`)
	if got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), "reauth_required") || len(*seen) != 2 || *exchanges != 1 || len(*forgotten) != 0 {
		t.Fatalf("%d %s requests %v exchanges %d", got.Code, got.Body, *seen, *exchanges)
	}
}

// An ambiguous outcome on the retried attempt is as final as on the first.
func TestCodexResetCreditAmbiguousRetryOutcomeIsNotSentAgain(t *testing.T) {
	mux, cookie, path, forgotten, seen, _ := retryRig(t, "r1", "", freshSecret, func(auth string, w http.ResponseWriter) {
		if auth == "Bearer stale-secret" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.WriteHeader(http.StatusBadGateway)
	})
	got := doJSON(t, mux, cookie, http.MethodPost, path, `{"confirm":true}`)
	if got.Code != http.StatusBadGateway || len(*seen) != 2 || (*seen)[0][0] != (*seen)[1][0] || len(*forgotten) != 0 {
		t.Fatalf("%d %s requests %v", got.Code, got.Body, *seen)
	}
}

// With nothing to refresh with, or a refresh that fails, no second spend is sent.
func TestCodexResetCreditRefusalWithoutUsableRefreshIsNotRetried(t *testing.T) {
	for name, tc := range map[string]struct {
		refreshToken string
		exchange     func() (credentials.Secret, error)
		exchanges    int
	}{
		"no refresh token": {"", freshSecret, 0},
		"refresh fails":    {"r1", func() (credentials.Secret, error) { return credentials.Secret{}, io.ErrUnexpectedEOF }, 1},
	} {
		t.Run(name, func(t *testing.T) {
			mux, cookie, path, forgotten, seen, exchanges := retryRig(t, tc.refreshToken, "", tc.exchange, rejectStale)
			got := doJSON(t, mux, cookie, http.MethodPost, path, `{"confirm":true}`)
			if got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), "reauth_required") || len(*seen) != 1 || *exchanges != tc.exchanges || len(*forgotten) != 0 {
				t.Fatalf("%d %s requests %v exchanges %d", got.Code, got.Body, *seen, *exchanges)
			}
		})
	}
}

// The pin decides from the refusal's text, so a 4xx that reads as an expired
// credential is replayed once; a refusal that does not, and any 2xx or 5xx whose
// text merely matches (an unknown outcome), is sent once.
func TestCodexResetCreditRetryFollowsTheRefusalText(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
		sent   int
	}{
		{"400 message expired", 400, `{"message":"token expired"}`, 2},
		{"400 detail", 400, `{"detail":"Re-authorize the account"}`, 2},
		{"422 error", 422, `{"error":"Unauthorized"}`, 2},
		{"400 code", 400, `{"code":"authentication_failed"}`, 2},
		{"400 plain refusal", 400, `{"code":"rejected"}`, 1},
		{"429 limit", 429, `{"message":"slow down"}`, 1},
		{"502 matching text", 502, `{"message":"unauthorized"}`, 1},
		{"200 matching text", 200, `{"code":"pending","message":"credit expired"}`, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux, cookie, path, _, seen, exchanges := retryRig(t, "r1", "", freshSecret, func(auth string, w http.ResponseWriter) {
				if auth == "Bearer stale-secret" {
					w.WriteHeader(tc.status)
					io.WriteString(w, tc.body)
					return
				}
				io.WriteString(w, `{"code":"reset","windows_reset":1}`)
			})
			got := doJSON(t, mux, cookie, http.MethodPost, path, `{"confirm":true}`)
			if len(*seen) != tc.sent || *exchanges != tc.sent-1 {
				t.Fatalf("%d %s requests %v exchanges %d", got.Code, got.Body, *seen, *exchanges)
			}
			if tc.sent == 2 && (got.Code != http.StatusOK || (*seen)[0][0] != (*seen)[1][0]) {
				t.Fatalf("%d %s requests %v", got.Code, got.Body, *seen)
			}
		})
	}
}

// An access-token connection has no sign-in to renew (route.js:130,140), even if
// a refresh token is stored: its refusal is final.
func TestCodexResetCreditAccessTokenConnectionIsNeverRefreshed(t *testing.T) {
	mux, cookie, path, forgotten, seen, exchanges := retryRig(t, "r1", credentials.AuthTypeAccessToken, freshSecret, rejectStale)
	got := doJSON(t, mux, cookie, http.MethodPost, path, `{"confirm":true}`)
	if got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), "reauth_required") || len(*seen) != 1 || *exchanges != 0 || len(*forgotten) != 0 {
		t.Fatalf("%d %s requests %v exchanges %d", got.Code, got.Body, *seen, *exchanges)
	}
}

// Each leg has its own budget: a refresh that takes most of one budget does not
// leave the replay without time.
func TestCodexResetCreditRefreshAndReplayEachGetTheirOwnBudget(t *testing.T) {
	old := resetCreditLegTimeout
	resetCreditLegTimeout = 400 * time.Millisecond
	t.Cleanup(func() { resetCreditLegTimeout = old })
	slow := func(auth string, w http.ResponseWriter) {
		time.Sleep(250 * time.Millisecond)
		rejectStale(auth, w)
	}
	exchange := func() (credentials.Secret, error) {
		time.Sleep(250 * time.Millisecond)
		return freshSecret()
	}
	mux, cookie, path, _, seen, exchanges := retryRig(t, "r1", "", exchange, slow)
	got := doJSON(t, mux, cookie, http.MethodPost, path, `{"confirm":true}`)
	if got.Code != http.StatusOK || len(*seen) != 2 || *exchanges != 1 {
		t.Fatalf("%d %s requests %v exchanges %d", got.Code, got.Body, *seen, *exchanges)
	}
}

// A refresh that outlives its budget leaves the first refusal, which stays a
// definite 403 reauth_required and is not turned into an unknown outcome.
func TestCodexResetCreditSlowRefreshKeepsTheRefusalDefinite(t *testing.T) {
	old := resetCreditLegTimeout
	resetCreditLegTimeout = 200 * time.Millisecond
	t.Cleanup(func() { resetCreditLegTimeout = old })
	mux, cookie, path, forgotten, seen, _ := retryRigCtx(t, "r1", "", nil, func(ctx context.Context) (credentials.Secret, error) {
		select {
		case <-time.After(600 * time.Millisecond):
			return freshSecret()
		case <-ctx.Done():
			return credentials.Secret{}, ctx.Err()
		}
	}, rejectStale)
	got := doJSON(t, mux, cookie, http.MethodPost, path, `{"confirm":true}`)
	if got.Code != http.StatusForbidden || !strings.Contains(got.Body.String(), "reauth_required") || len(*seen) != 1 || len(*forgotten) != 0 {
		t.Fatalf("%d %s requests %v", got.Code, got.Body, *seen)
	}
}

// The access-token test is the sign-in match's (codexTokenOnly): a connection
// stored before its auth type was recorded, whose data says access_token, is
// refused as is even with a refresh token; a bulk item (auth type oauth) with the
// same data is a sign-in and does refresh, as the pin treats authType oauth.
func TestCodexResetCreditAccessTokenDetectionMatchesTheSignInMatch(t *testing.T) {
	data := map[string]string{"authMethod": "access_token"}
	for _, tc := range []struct {
		name     string
		authType string
		sent     int
	}{
		{"legacy access-token connection", "", 1},
		{"recorded access token", credentials.AuthTypeAccessToken, 1},
		{"bulk item with the same data", credentials.AuthTypeOAuth, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mux, cookie, path, _, seen, exchanges := retryRigCtx(t, "r1", tc.authType, data, func(context.Context) (credentials.Secret, error) { return freshSecret() }, rejectStale)
			got := doJSON(t, mux, cookie, http.MethodPost, path, `{"confirm":true}`)
			if len(*seen) != tc.sent || *exchanges != tc.sent-1 {
				t.Fatalf("%d %s requests %v exchanges %d", got.Code, got.Body, *seen, *exchanges)
			}
			if tc.sent == 1 && got.Code != http.StatusForbidden {
				t.Fatalf("%d %s", got.Code, got.Body)
			}
		})
	}
}
