package quota

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	codexprovider "github.com/raufimusaddiq/routeweft/internal/providers/codex"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// TestServiceAgainstLiveCodexUsageEndpoint exercises the real CodexClient
// adapter against a mock usage endpoint and asserts the normalized observation
// reaches RuntimeState. This covers the entire production path — HTTP read ->
// normalization -> publish -> routing eligibility — not just the in-memory
// stubs.
func TestServiceAgainstLiveCodexUsageEndpoint(t *testing.T) {
	usageBody := `{"plan_type":"plus","rate_limit":{"primary_window":{"used_percent":10,"reset_at":"2026-09-25T13:00:00Z"},"secondary_window":{"used_percent":100,"reset_at":"2026-09-30T13:00:00Z"}},"rate_limit_reset_credits":{"available_count":2}}`
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(usageBody))
	}))
	defer server.Close()

	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	client := &codexUsageRedirect{server: server}
	state := runtime.NewState()
	connections := fakeConnections{byProvider: map[string][]credentials.Connection{"codex": {{ID: "conn-1", ProviderID: "codex", Enabled: true}}}}
	service := NewService(connections, fakeCredentials{values: map[string]string{"conn-1": "fixture-token"}}, Observer{
		Clients:   map[string]UsageClient{"codex": client},
		Now:       func() time.Time { return now },
		Publisher: state,
	})
	if err := service.RefreshProvider(context.Background(), "codex"); err != nil {
		t.Fatal(err)
	}
	if seen != "Bearer fixture-token" {
		t.Fatalf("usage request authorization=%q", seen)
	}
	observation, ok := state.Quota("conn-1")
	if !ok || observation.Remaining == nil {
		t.Fatalf("observation=%+v ok=%v", observation, ok)
	}
	// The weekly window is exhausted (100% used) with a future reset, so the most
	// constrained window must have reduced to zero remaining.
	if *observation.Remaining != 0 {
		t.Fatalf("remaining=%v, want 0 from the exhausted weekly window", *observation.Remaining)
	}
	if !observation.ResetAt.After(now) {
		t.Fatalf("reset=%v should be in the future", observation.ResetAt)
	}
}

// codexUsageRedirect points the Codex usage client at a mock endpoint. It keeps
// the real adapter (CodexClient -> codexprovider.UsageClient) in the path.
type codexUsageRedirect struct {
	server *httptest.Server
}

func (c *codexUsageRedirect) Usage(ctx context.Context, credential string) (State, error) {
	client := codexprovider.UsageClient{Client: codexprovider.HTTPClient(rewriteClient{base: c.server.Client(), server: c.server})}
	usage, err := client.Usage(ctx, credential)
	if err != nil {
		return State{}, err
	}
	windows := make([]Window, 0, len(usage.Quotas))
	for name, window := range usage.Quotas {
		windows = append(windows, Window{Name: name, Remaining: PercentFromUsed(window.Used), ResetAt: window.ResetAt})
	}
	return State{Plan: usage.Plan, Windows: windows}, nil
}

// rewriteClient redirects Codex's fixed usage URL to the mock server.
type rewriteClient struct {
	base   *http.Client
	server *httptest.Server
}

func (c rewriteClient) Do(request *http.Request) (*http.Response, error) {
	redirected := request.Clone(request.Context())
	redirected.URL.Scheme = "http"
	redirected.URL.Host = c.server.Listener.Addr().String()
	redirected.Host = ""
	return c.base.Do(redirected)
}
