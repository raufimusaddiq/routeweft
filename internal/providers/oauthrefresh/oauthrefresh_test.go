package oauthrefresh

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

type fakeClient struct {
	body string
	sent string
}

func (f *fakeClient) Do(request *http.Request) (*http.Response, error) {
	raw, _ := io.ReadAll(request.Body)
	f.sent = string(raw)
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(f.body)), Header: http.Header{}}, nil
}

func TestResolverOnlyRefreshesOAuthConnectionsWithRefreshToken(t *testing.T) {
	// A real client, because a resolver without one cannot refresh anything: the
	// test that passed a nil client was asserting a resolver that never worked.
	resolve, err := Resolver(&fakeClient{})
	if err != nil {
		t.Fatal(err)
	}
	oauth := credentials.Connection{ProviderID: "claude", AuthKind: credentials.AuthOAuth, Secret: credentials.Secret{RefreshToken: "r"}}
	if resolve(oauth) == nil {
		t.Fatal("claude oauth connection got no exchanger")
	}
	// Every provider the reference refreshes must resolve, not just the first two.
	for _, provider := range []string{"codex", "xai", "grok-cli", "gemini-cli", "antigravity", "iflow", "kimi", "cline", "clinepass", "kiro", "codebuddy-cn", "codebuddy-intl", "trae"} {
		oauth.ProviderID = provider
		if resolve(oauth) == nil {
			t.Errorf("%s oauth connection got no exchanger", provider)
		}
	}
	// GitHub refreshes the Copilot token without a refresh token.
	if resolve(credentials.Connection{ProviderID: "github", AuthKind: credentials.AuthOAuth, Secret: credentials.Secret{AccessToken: "a"}}) == nil {
		t.Error("github connection got no copilot-token exchanger")
	}
	for name, connection := range map[string]credentials.Connection{
		"api key": {ProviderID: "claude", AuthKind: credentials.AuthAPIKey, Secret: credentials.Secret{RefreshToken: "r"}},
		// A provider whose credential does not rotate keeps no exchanger.
		"no refresh grant":    {ProviderID: "openai", AuthKind: credentials.AuthOAuth, Secret: credentials.Secret{RefreshToken: "r"}},
		"long-lived seat key": {ProviderID: "windsurf", AuthKind: credentials.AuthAPIKey, Secret: credentials.Secret{AccessToken: "sk-ws-fixture"}},
	} {
		if resolve(connection) != nil {
			t.Errorf("%s: unexpected exchanger", name)
		}
	}
}

func TestExchangersRotateAndKeepIDToken(t *testing.T) {
	client := &fakeClient{body: `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`}
	for _, provider := range []string{"claude", "codex"} {
		resolve, err := Resolver(client)
		if err != nil {
			t.Fatal(err)
		}
		exchange := resolve(credentials.Connection{ProviderID: provider, AuthKind: credentials.AuthOAuth, Secret: credentials.Secret{RefreshToken: "old-refresh"}})
		secret, err := exchange(context.Background(), credentials.Secret{RefreshToken: "old-refresh", IDToken: "kept-id"})
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		if secret.AccessToken != "new-access" || secret.RefreshToken != "new-refresh" || secret.IDToken != "kept-id" || !secret.Expiry.After(time.Now()) || !strings.Contains(client.sent, "old-refresh") {
			t.Fatalf("%s: secret=%+v sent=%s", provider, secret, client.sent)
		}
	}
}
