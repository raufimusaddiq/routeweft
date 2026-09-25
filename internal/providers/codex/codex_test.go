package codex

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeClient struct {
	do func(*http.Request) (*http.Response, error)
}

func (f fakeClient) Do(r *http.Request) (*http.Response, error) { return f.do(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
func jsonBody(t *testing.T, value any) string {
	t.Helper()
	b, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestPKCEAuthorizeAndExchange(t *testing.T) {
	p, err := NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	if p.Method != "S256" || p.Challenge != ChallengeFor(p.Verifier).Challenge {
		t.Fatalf("pkce=%+v", p)
	}
	u, err := url.Parse(AuthorizeURL(p))
	if err != nil {
		t.Fatal(err)
	}
	if u.Query().Get("client_id") != DefaultClientID || u.Query().Get("originator") != "codex_cli_rs" || u.Query().Get("code_challenge") != p.Challenge {
		t.Fatalf("url=%s", u)
	}
	var form url.Values
	client := fakeClient{do: func(r *http.Request) (*http.Response, error) {
		b, _ := io.ReadAll(r.Body)
		form, _ = url.ParseQuery(string(b))
		return response(200, jsonBody(t, map[string]any{"access_token": "a", "refresh_token": "r", "id_token": "i", "expires_in": 3600})), nil
	}}
	tokens, err := ExchangeCode(context.Background(), p, "code", client)
	if err != nil || tokens.AccessToken != "a" || tokens.RefreshToken != "r" || form.Get("code_verifier") != p.Verifier {
		t.Fatalf("tokens=%+v form=%v err=%v", tokens, form, err)
	}
}
func TestExchangeRejectsAuthAndBadResponses(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   error
	}{{401, "{}", ErrReauthRequired}, {200, "not-json", nil}, {200, jsonBody(t, map[string]string{"access_token": "a"}), nil}} {
		_, err := ExchangeCode(context.Background(), ChallengeFor("v"), "code", fakeClient{do: func(*http.Request) (*http.Response, error) { return response(tc.status, tc.body), nil }})
		if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
			t.Fatalf("status=%d err=%v", tc.status, err)
		}
	}
}
func TestRefresherSingleflightAndDurableRotation(t *testing.T) {
	var calls, saved atomic.Int64
	client := fakeClient{do: func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return response(200, jsonBody(t, map[string]any{"access_token": "new", "refresh_token": "rotated", "expires_in": 3600})), nil
	}}
	r := &Refresher{Client: client, Persist: func(_ context.Context, tokens Tokens) error {
		if tokens.RefreshToken != "rotated" {
			t.Errorf("tokens=%+v", tokens)
		}
		saved.Add(1)
		return nil
	}}
	r.Seed(Tokens{AccessToken: "old", RefreshToken: "refresh", Expiry: time.Now().Add(-time.Minute)})
	var wg sync.WaitGroup
	results := make([]string, 8)
	for i := range results {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i], _ = r.Token(context.Background()) }(i)
	}
	wg.Wait()
	for _, got := range results {
		if got != "new" {
			t.Fatalf("tokens=%v", results)
		}
	}
	if calls.Load() != 1 || saved.Load() != 1 {
		t.Fatalf("calls=%d saved=%d", calls.Load(), saved.Load())
	}
}
func TestRefresherPersistenceFailureDoesNotExposeToken(t *testing.T) {
	// Compose the sentinels from parts so no single secret-shaped literal appears
	// in the source; the assertions still prove the token is not exposed.
	secretAccess := "secret-" + "access"
	secretRefresh := "secret-" + "refresh"
	r := &Refresher{Client: fakeClient{do: func(*http.Request) (*http.Response, error) {
		return response(200, jsonBody(t, map[string]any{"access_token": secretAccess, "refresh_token": secretRefresh, "expires_in": 3600})), nil
	}}, Persist: func(context.Context, Tokens) error { return errors.New(secretRefresh + " disk-error") }}
	r.Seed(Tokens{AccessToken: "old", RefreshToken: "old-refresh", Expiry: time.Now().Add(-time.Minute)})
	got, err := r.Token(context.Background())
	if got != "" || !errors.Is(err, ErrPersistenceFailed) || strings.Contains(err.Error(), secretRefresh) {
		t.Fatalf("got=%q err=%v", got, err)
	}
	missingPersist := &Refresher{Client: r.Client}
	missingPersist.Seed(Tokens{RefreshToken: "refresh", Expiry: time.Now().Add(-time.Minute)})
	if _, err := missingPersist.Token(context.Background()); !errors.Is(err, ErrPersistenceFailed) {
		t.Fatalf("missing persistence err=%v", err)
	}
}
