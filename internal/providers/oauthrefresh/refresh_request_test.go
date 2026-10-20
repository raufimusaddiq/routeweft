package oauthrefresh

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

// recordingClient answers every refresh call and records that one was made.
type recordingClient struct {
	calls []string
	body  string
}

func (r *recordingClient) Do(request *http.Request) (*http.Response, error) {
	r.calls = append(r.calls, request.URL.String())
	if request.Body != nil {
		raw, _ := io.ReadAll(request.Body)
		r.body = string(raw)
	}
	url := request.URL.String()
	body := `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`
	switch {
	case strings.Contains(url, "copilot_internal"):
		// GitHub re-mints the Copilot token; the OAuth token itself does not rotate.
		body = `{"token":"new-access","expires_at":1893456000}`
	case strings.Contains(url, "amazonaws.com"):
		// AWS SSO OIDC reports camelCase.
		body = `{"accessToken":"new-access","refreshToken":"new-refresh","expiresIn":3600}`
	case strings.Contains(url, "plugin/auth/token/refresh"):
		// CodeBuddy wraps the token in a code envelope.
		body = `{"code":0,"data":{"accessToken":"new-access","refreshToken":"new-refresh","expiresIn":3600}}`
	case strings.Contains(url, "trae"):
		body = `{"Result":{"AccessToken":"new-access","RefreshToken":"new-refresh"}}`
	case strings.Contains(url, "auth/refresh"):
		// Cline wraps its token under data.
		body = `{"data":{"accessToken":"new-access","refreshToken":"new-refresh","expiresAt":"2027-01-01T00:00:00Z"}}`
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}, nil
}

// TestResolverIssuesARealRefreshRequest is the test the old one should have been.
// Asserting resolve(...) != nil passed while every refresh was dead, because the
// resolver was handed a nil client and the token request failed per call. This
// asserts an HTTP request actually leaves, for every provider with a grant.
func TestResolverIssuesARealRefreshRequest(t *testing.T) {
	for _, provider := range []string{
		"claude", "codex", "xai", "grok-cli", "gemini-cli", "antigravity", "iflow",
		"github", "kimi", "cline", "clinepass", "kiro", "codebuddy-cn", "codebuddy-intl", "trae",
	} {
		provider := provider
		t.Run(provider, func(t *testing.T) {
			// Provider-supplied client credentials the flow needs are supplied the way
			// the operator would, so the test exercises the refresh, not a precondition.
			t.Setenv("ROUTEWEFT_GOOGLE_OAUTH_CLIENT_ID", "fixture-google-id")
			t.Setenv("ROUTEWEFT_GOOGLE_OAUTH_CLIENT_SECRET", "fixture-google-secret")
			t.Setenv("ROUTEWEFT_IFLOW_OAUTH_CLIENT_ID", "fixture-iflow-id")
			t.Setenv("ROUTEWEFT_IFLOW_OAUTH_CLIENT_SECRET", "fixture-iflow-secret")

			client := &recordingClient{}
			connection := credentials.Connection{
				ProviderID: provider,
				AuthKind:   credentials.AuthOAuth,
				Secret: credentials.Secret{
					RefreshToken: "fixture-refresh",
					ProviderData: map[string]string{
						"clientId": "fixture-client", "clientSecret": "fixture-secret",
						"region": "us-east-1",
					},
				},
			}
			resolve, err := Resolver(client)
			if err != nil {
				t.Fatal(err)
			}
			exchange := resolve(connection)
			if exchange == nil {
				t.Fatalf("%s resolved no exchanger", provider)
			}
			next, err := exchange(context.Background(), connection.Secret)
			if err != nil {
				t.Fatalf("%s refresh failed: %v", provider, err)
			}
			if len(client.calls) == 0 {
				t.Fatalf("%s refresh issued no HTTP request; the client was nil", provider)
			}
			// Cline's gateway requires the workos: scheme prefix on refreshed tokens.
			if !strings.HasSuffix(next.AccessToken, "new-access") {
				t.Fatalf("%s secret=%+v", provider, next)
			}
		})
	}
}

// TestNilClientFailsAtConstruction proves a nil client is rejected where it is
// supplied, not per refresh call. The per-request failure was invisible because
// the resolver still looked healthy.
func TestNilClientFailsAtConstruction(t *testing.T) {
	// An error, not a panic: a wiring mistake is the caller's to report, not a
	// crash in whatever imports the package.
	if _, err := Resolver(nil); !errors.Is(err, ErrNoClient) {
		t.Fatalf("Resolver(nil) err=%v, want ErrNoClient", err)
	}
}
