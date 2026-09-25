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
	// known mirrors a memory-first registry: only seeded ids are readable.
	known map[string]bool
}

func (f fakeCredentials) Credential(_ context.Context, connectionID string) (string, error) {
	if err := f.err[connectionID]; err != nil {
		return "", err
	}
	if f.known != nil && !f.known[connectionID] {
		return "", credentials.ErrNotFound
	}
	return f.values[connectionID], nil
}

type fakeSeeder struct {
	connections fakeConnections
	credentials *fakeCredentials
	loaded      []string
}

func (f *fakeSeeder) Load(_ context.Context, providerID string) ([]credentials.Connection, error) {
	f.loaded = append(f.loaded, providerID)
	connections, err := f.connections.ListConnections(context.Background(), providerID)
	if err != nil {
		return nil, err
	}
	if f.credentials.known == nil {
		f.credentials.known = make(map[string]bool)
	}
	for _, connection := range connections {
		f.credentials.known[connection.ID] = true
	}
	return connections, nil
}

// TestServiceSeedsRegistryBeforeReading proves the fix for the review finding:
// without seeding, every credential lookup returns ErrNotFound and quota reads
// fail; with a seeder, the accounts become readable and publish real usage.
func TestServiceSeedsRegistryBeforeReading(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	reset := now.Add(time.Hour)
	connections := fakeConnections{byProvider: map[string][]credentials.Connection{
		"codex": {{ID: "conn-1", ProviderID: "codex", Enabled: true}},
	}}
	credential := &fakeCredentials{values: map[string]string{"conn-1": "token"}, known: map[string]bool{}}
	client := &fakeClient{state: State{Windows: []Window{{Name: "weekly", Remaining: ptr(0.3), ResetAt: &reset}}}}
	state := runtime.NewState()
	observer := Observer{Clients: map[string]UsageClient{"codex": client}, Now: func() time.Time { return now }, Publisher: state}

	// Without a seeder the registry is empty and the read fails.
	unseeded := NewService(connections, credential, observer)
	if err := unseeded.RefreshProvider(context.Background(), "codex"); err == nil {
		t.Fatal("expected an unseeded registry to fail the credential lookup")
	}
	if client.calls != 0 {
		t.Fatalf("usage client ran without a resolved credential: calls=%d", client.calls)
	}

	// With a seeder the account is registered and real usage is published.
	state2 := runtime.NewState()
	client2 := &fakeClient{state: State{Windows: []Window{{Name: "weekly", Remaining: ptr(0.3), ResetAt: &reset}}}}
	seeder := &fakeSeeder{connections: connections, credentials: credential}
	seeded := NewService(connections, credential, Observer{Clients: map[string]UsageClient{"codex": client2}, Now: func() time.Time { return now }, Publisher: state2}).WithSeeder(seeder)
	if err := seeded.RefreshProvider(context.Background(), "codex"); err != nil {
		t.Fatal(err)
	}
	if len(seeder.loaded) != 1 || seeder.loaded[0] != "codex" {
		t.Fatalf("seeder loaded=%v", seeder.loaded)
	}
	observation, ok := state2.Quota("conn-1")
	if !ok || observation.Remaining == nil || *observation.Remaining != 0.3 {
		t.Fatalf("observation=%+v ok=%v", observation, ok)
	}
	if client2.calls != 1 {
		t.Fatalf("usage calls=%d", client2.calls)
	}
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
