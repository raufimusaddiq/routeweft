package quota

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

type fakeConnections struct {
	byProvider map[string][]credentials.Connection
	err        error
}

func (f fakeConnections) ListConnections(_ context.Context, providerID string) ([]credentials.Connection, error) {
	if f.err != nil {
		return nil, f.err
	}
	return f.byProvider[providerID], nil
}

type fakeCredentials struct {
	values map[string]string
	err    map[string]error
}

func (f fakeCredentials) Credential(_ context.Context, connectionID string) (string, error) {
	if err := f.err[connectionID]; err != nil {
		return "", err
	}
	return f.values[connectionID], nil
}

// TestServiceRefreshPublishesEachEnabledConnection proves the production service
// reads every enabled connection for a provider and publishes to RuntimeState,
// while skipping disabled accounts and providers with no usage client.
func TestServiceRefreshPublishesEachEnabledConnection(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	reset := now.Add(time.Hour)
	connections := fakeConnections{byProvider: map[string][]credentials.Connection{
		"codex": {
			{ID: "c1", ProviderID: "codex", Enabled: true},
			{ID: "c2", ProviderID: "codex", Enabled: false},
			{ID: "c3", ProviderID: "codex", Enabled: true},
		},
	}}
	credential := fakeCredentials{values: map[string]string{"c1": "token-1", "c2": "token-2", "c3": "token-3"}}
	client := &fakeClient{state: State{Windows: []Window{{Name: "weekly", Remaining: ptr(0.4), ResetAt: &reset}}}}
	state := runtime.NewState()
	service := NewService(connections, credential, Observer{Clients: map[string]UsageClient{"codex": client}, Now: func() time.Time { return now }, Publisher: state})
	if err := service.RefreshProvider(context.Background(), "codex"); err != nil {
		t.Fatal(err)
	}
	if _, ok := state.Quota("c2"); ok {
		t.Fatal("disabled connection was refreshed")
	}
	for _, id := range []string{"c1", "c3"} {
		observation, ok := state.Quota(id)
		if !ok || observation.Remaining == nil || *observation.Remaining != 0.4 {
			t.Fatalf("%s observation=%+v ok=%v", id, observation, ok)
		}
	}
	if client.calls != 2 {
		t.Fatalf("usage calls=%d, want 2", client.calls)
	}
	if service.LastRefresh().IsZero() {
		t.Fatal("last refresh was not recorded")
	}
}

// TestServiceIsolatesPerAccountFailures proves one account's failure does not
// stop the others and is recorded as an error observation, not exhaustion.
func TestServiceIsolatesPerAccountFailures(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	connections := fakeConnections{byProvider: map[string][]credentials.Connection{
		"claude": {{ID: "good", ProviderID: "claude", Enabled: true}, {ID: "bad", ProviderID: "claude", Enabled: true}},
	}}
	credential := fakeCredentials{
		values: map[string]string{"good": "token"},
		err:    map[string]error{"bad": errors.New("credential refresh required")},
	}
	client := &fakeClient{state: State{Windows: []Window{{Name: "session", Remaining: ptr(0.5)}}}}
	state := runtime.NewState()
	service := NewService(connections, credential, Observer{Clients: map[string]UsageClient{"claude": client}, Now: func() time.Time { return now }, Publisher: state})
	if err := service.RefreshProvider(context.Background(), "claude"); err == nil {
		t.Fatal("expected the failure to surface")
	}
	if _, ok := state.Quota("good"); !ok {
		t.Fatal("a healthy account was skipped after a failure")
	}
	if _, ok := service.LastError("bad"); !ok {
		t.Fatal("failure was not recorded")
	}
	if _, ok := service.LastError("good"); ok {
		t.Fatal("healthy account recorded an error")
	}
}

func TestServiceSkipsProvidersWithoutUsageClient(t *testing.T) {
	connections := fakeConnections{byProvider: map[string][]credentials.Connection{
		"blackbox": {{ID: "b1", ProviderID: "blackbox", Enabled: true}},
	}}
	client := &fakeClient{}
	service := NewService(connections, fakeCredentials{}, Observer{Clients: map[string]UsageClient{"codex": client}})
	if err := service.RefreshProvider(context.Background(), "blackbox"); err != nil {
		t.Fatal(err)
	}
	if client.calls != 0 {
		t.Fatalf("usage client was called for a provider with no registered client")
	}
}

func TestServiceRunStopsOnContextCancel(t *testing.T) {
	client := &fakeClient{}
	service := NewService(fakeConnections{}, fakeCredentials{}, Observer{Clients: map[string]UsageClient{"codex": client}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { service.Run(ctx, 10*time.Millisecond); close(done) }()
	time.Sleep(30 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not stop after context cancellation")
	}
}
