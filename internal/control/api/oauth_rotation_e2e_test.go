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

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/oauthrefresh"
)

// #231 (O03): token rotation through the real sign-in handler and the real sealed store. The mock
// provider answers every exchange with a fresh pair whose expiry is inside the refresh lead, so the
// next read refreshes; the bodies it receives prove which token each refresh presented.
type rotatingEndpoint struct {
	mu        sync.Mutex
	presented []string
}

func (e *rotatingEndpoint) Do(request *http.Request) (*http.Response, error) {
	var raw []byte
	if request.Body != nil {
		raw, _ = io.ReadAll(request.Body)
	}
	e.mu.Lock()
	e.presented = append(e.presented, string(raw))
	call := len(e.presented)
	e.mu.Unlock()
	body := fmt.Sprintf(`{"access_token":"access-%d","refresh_token":"refresh-%d","expires_in":5}`, call, call)
	return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{}}, nil
}

// refreshTokens lists the refresh_token each exchange presented. Claude's grant is JSON
// (onboarding/refresh.go grantClaude); a form grant is read too.
func (e *rotatingEndpoint) refreshTokens(t *testing.T) []string {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	tokens := make([]string, len(e.presented))
	for i, body := range e.presented {
		var document map[string]string
		if json.Unmarshal([]byte(body), &document) == nil && document["refresh_token"] != "" {
			tokens[i] = document["refresh_token"]
			continue
		}
		values, err := url.ParseQuery(body)
		if err != nil {
			t.Fatalf("exchange %d body %q: %v", i+1, body, err)
		}
		tokens[i] = values.Get("refresh_token")
	}
	return tokens
}

func TestOAuthTokenRotationIsUsedAndSurvivesARestart(t *testing.T) {
	ctx := context.Background()
	endpoint := &rotatingEndpoint{}
	h, mux, store, cookie := newOAuthAPI(t, &oauthExchange{})
	h.opts.OAuthClient = endpoint
	resolver, err := oauthrefresh.Resolver(endpoint)
	if err != nil {
		t.Fatal(err)
	}
	h.opts.CredentialRegistry.SetExchangerResolver(resolver)

	flow := startFlow(t, mux, cookie, "claude")
	if recorder := callback(t, mux, cookie, "claude", flow.State, oauthTestCode); recorder.Code != http.StatusOK {
		t.Fatalf("callback status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	connections, err := store.ListConnections(ctx, "claude")
	if err != nil || len(connections) != 1 {
		t.Fatalf("connections=%d err=%v", len(connections), err)
	}
	id := connections[0].ID

	// Each read is inside the lead, so each read refreshes: the second refresh must present the
	// token the first one rotated to, never the one the sign-in stored.
	if _, err := h.opts.CredentialRegistry.Secret(ctx, id); err != nil {
		t.Fatal(err)
	}
	second, err := h.opts.CredentialRegistry.Secret(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if second.AccessToken != "access-3" {
		t.Fatalf("second read served %q", second.AccessToken)
	}
	tokens := endpoint.refreshTokens(t)
	if got := tokens[1]; got != "refresh-1" {
		t.Fatalf("first refresh presented %q, want the sign-in token", got)
	}
	if got := tokens[2]; got != "refresh-2" {
		t.Fatalf("second refresh presented %q, want the rotated token", got)
	}

	// A fresh registry over the same store is a restart: it must continue from the durable
	// rotation, not from the token the sign-in wrote.
	restarted := credentials.NewRegistry(store, nil)
	restarted.SetExchangerResolver(resolver)
	connection, err := store.GetConnection(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	restarted.Register(connection, nil)
	if _, err := restarted.Secret(ctx, id); err != nil {
		t.Fatal(err)
	}
	tokens = endpoint.refreshTokens(t)
	if got := tokens[3]; got != "refresh-3" {
		t.Fatalf("refresh after restart presented %q, want the last durable rotation", got)
	}
	stored, err := store.GetConnection(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Secret.RefreshToken != "refresh-4" {
		t.Fatalf("stored refresh token %q is not the last rotation", stored.Secret.RefreshToken)
	}
}
