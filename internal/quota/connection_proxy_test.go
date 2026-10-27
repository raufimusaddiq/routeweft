package quota

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/transport"
)

type usageFunc func(context.Context, Credential) (State, error)

func (f usageFunc) Usage(ctx context.Context, credential Credential) (State, error) {
	return f(ctx, credential)
}

// Usage reads and auto-pings leave through the connection's proxy, as the pin
// resolves it for both (usage/[connectionId]/route.js:159-185,
// quotaAutoPing.js:198-212): their outbound work names the connection.
func TestQuotaWorkCarriesTheConnection(t *testing.T) {
	credential := Credential{ConnectionID: "conn-quota", AccessToken: "fixture"}
	for _, tc := range []struct {
		name string
		run  func(t *testing.T) string
	}{
		{"usage read", func(t *testing.T) string {
			var seen string
			observer := Observer{Clients: map[string]UsageClient{"codex": usageFunc(func(ctx context.Context, _ Credential) (State, error) {
				seen = transport.ConnectionFrom(ctx)
				return State{}, nil
			})}}
			if _, err := observer.Observe(context.Background(), "codex", "acct", credential); err != nil {
				t.Fatal(err)
			}
			return seen
		}},
		{"scheduled usage refresh", func(t *testing.T) string {
			var seen string
			usage := usageFunc(func(ctx context.Context, _ Credential) (State, error) {
				seen = transport.ConnectionFrom(ctx)
				return State{}, nil
			})
			service := NewService(fakeConnections{byProvider: map[string][]credentials.Connection{"codex": {{ID: credential.ConnectionID, ProviderID: "codex", Enabled: true, AuthKind: credentials.AuthOAuth}}}}, fakeCredentials{values: map[string]string{credential.ConnectionID: "fixture"}}, Observer{Clients: map[string]UsageClient{"codex": usage}})
			if err := service.RefreshProvider(context.Background(), "codex"); err != nil {
				t.Fatal(err)
			}
			return seen
		}},
		{"auto-ping", func(t *testing.T) string {
			var seen string
			client := pingClient(func(r *http.Request) (*http.Response, error) {
				seen = transport.ConnectionFrom(r.Context())
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("data: {}\n\n"))}, nil
			})
			if err := SendPing(context.Background(), client, "codex", credential); err != nil {
				t.Fatal(err)
			}
			return seen
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.run(t); got != credential.ConnectionID {
				t.Fatalf("went out for %q, want %q", got, credential.ConnectionID)
			}
		})
	}
}
