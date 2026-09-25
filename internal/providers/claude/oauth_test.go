package claude

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

func TestPKCEAuthorizeAndExchange(t *testing.T) {
	pkce, err := NewPKCE()
	if err != nil {
		t.Fatal(err)
	}
	if pkce.Method != "S256" || pkce.Challenge != ChallengeFor(pkce.Verifier).Challenge {
		t.Fatalf("pkce=%+v", pkce)
	}
	parsed, err := url.Parse(AuthorizeURL(pkce))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Query().Get("client_id") != DefaultClientID || parsed.Query().Get("code_challenge") != pkce.Challenge || parsed.Query().Get("code_challenge_method") != "S256" {
		t.Fatalf("url=%s", parsed)
	}
	var sent map[string]string
	client := fakeClient{do: func(r *http.Request) (*http.Response, error) {
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("content-type=%s", r.Header.Get("Content-Type"))
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &sent)
		return response(200, `{"access_token":"a","refresh_token":"r","expires_in":3600}`), nil
	}}
	tokens, err := ExchangeCode(context.Background(), pkce, "code", client)
	if err != nil || tokens.AccessToken != "a" || tokens.RefreshToken != "r" || sent["code_verifier"] != pkce.Verifier || sent["grant_type"] != "authorization_code" {
		t.Fatalf("tokens=%+v sent=%v err=%v", tokens, sent, err)
	}
}

func TestExchangeRejectsAuthAndBadResponses(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
		want   error
	}{{401, "{}", ErrReauthRequired}, {200, "not-json", nil}, {200, `{"access_token":"a"}`, nil}} {
		_, err := ExchangeCode(context.Background(), ChallengeFor("v"), "code", fakeClient{do: func(*http.Request) (*http.Response, error) { return response(tc.status, tc.body), nil }})
		if err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
			t.Fatalf("status=%d err=%v", tc.status, err)
		}
	}
}

func TestRefresherSingleflightDurableRotation(t *testing.T) {
	var calls, saved atomic.Int64
	client := fakeClient{do: func(r *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), "refresh_token") {
			t.Errorf("body=%s", body)
		}
		calls.Add(1)
		time.Sleep(20 * time.Millisecond)
		return response(200, `{"access_token":"new","refresh_token":"rotated","expires_in":3600}`), nil
	}}
	r := &Refresher{Client: client, Persist: func(_ context.Context, tokens Tokens) error {
		if tokens.RefreshToken != "rotated" {
			t.Errorf("tokens=%+v", tokens)
		}
		saved.Add(1)
		return nil
	}}
	r.Seed(Tokens{AccessToken: "old", RefreshToken: "refresh", Expiry: time.Now().Add(-time.Minute)})
	results := make([]string, 8)
	var wg sync.WaitGroup
	for i := range results {
		wg.Add(1)
		go func(i int) { defer wg.Done(); results[i], _ = r.Token(context.Background()) }(i)
	}
	wg.Wait()
	for _, got := range results {
		if got != "new" {
			t.Fatalf("results=%v", results)
		}
	}
	if calls.Load() != 1 || saved.Load() != 1 {
		t.Fatalf("calls=%d saved=%d", calls.Load(), saved.Load())
	}
}

func TestRefresherPersistenceFailureDoesNotExposeToken(t *testing.T) {
	secretAccess := "secret-" + "access"
	secretRefresh := "secret-" + "refresh"
	r := &Refresher{Client: fakeClient{do: func(*http.Request) (*http.Response, error) {
		return response(200, `{"access_token":"`+secretAccess+`","refresh_token":"`+secretRefresh+`","expires_in":3600}`), nil
	}}, Persist: func(context.Context, Tokens) error { return errors.New(secretRefresh + " disk-error") }}
	r.Seed(Tokens{AccessToken: "old", RefreshToken: "old-refresh", Expiry: time.Now().Add(-time.Minute)})
	got, err := r.Token(context.Background())
	if got != "" || !errors.Is(err, ErrPersistenceFailed) || strings.Contains(err.Error(), secretRefresh) {
		t.Fatalf("got=%q err=%v", got, err)
	}
	missing := &Refresher{Client: r.Client}
	missing.Seed(Tokens{RefreshToken: "refresh", Expiry: time.Now().Add(-time.Minute)})
	if _, err := missing.Token(context.Background()); !errors.Is(err, ErrPersistenceFailed) {
		t.Fatalf("missing persistence err=%v", err)
	}
}
