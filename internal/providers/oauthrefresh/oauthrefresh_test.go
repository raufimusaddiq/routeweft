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
	resolve := Resolver(nil)
	oauth := credentials.Connection{ProviderID: "claude", AuthKind: credentials.AuthOAuth, Secret: credentials.Secret{RefreshToken: "r"}}
	if resolve(oauth) == nil {
		t.Fatal("claude oauth connection got no exchanger")
	}
	oauth.ProviderID = "codex"
	if resolve(oauth) == nil {
		t.Fatal("codex oauth connection got no exchanger")
	}
	for name, connection := range map[string]credentials.Connection{
		"api key":          {ProviderID: "claude", AuthKind: credentials.AuthAPIKey, Secret: credentials.Secret{RefreshToken: "r"}},
		"no refresh token": {ProviderID: "claude", AuthKind: credentials.AuthOAuth, Secret: credentials.Secret{AccessToken: "a"}},
		"other provider":   {ProviderID: "openai", AuthKind: credentials.AuthOAuth, Secret: credentials.Secret{RefreshToken: "r"}},
	} {
		if resolve(connection) != nil {
			t.Errorf("%s: unexpected exchanger", name)
		}
	}
}

func TestExchangersRotateAndKeepIDToken(t *testing.T) {
	client := &fakeClient{body: `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`}
	for _, provider := range []string{"claude", "codex"} {
		exchange := Resolver(client)(credentials.Connection{ProviderID: provider, AuthKind: credentials.AuthOAuth, Secret: credentials.Secret{RefreshToken: "old-refresh"}})
		secret, err := exchange(context.Background(), credentials.Secret{RefreshToken: "old-refresh", IDToken: "kept-id"})
		if err != nil {
			t.Fatalf("%s: %v", provider, err)
		}
		if secret.AccessToken != "new-access" || secret.RefreshToken != "new-refresh" || secret.IDToken != "kept-id" || !secret.Expiry.After(time.Now()) || !strings.Contains(client.sent, "old-refresh") {
			t.Fatalf("%s: secret=%+v sent=%s", provider, secret, client.sent)
		}
	}
}
