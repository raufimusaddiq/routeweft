package quota

import (
	"context"
	"errors"
	"sync"
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
	// cookies holds cookie-only connections, which no usage reader can read.
	cookies map[string]string
}

func (f fakeCredentials) Secret(_ context.Context, connectionID string) (credentials.Secret, error) {
	if err := f.err[connectionID]; err != nil {
		return credentials.Secret{}, err
	}
	if f.known != nil && !f.known[connectionID] {
		return credentials.Secret{}, credentials.ErrNotFound
	}
	return credentials.Secret{AccessToken: f.values[connectionID], Cookie: f.cookies[connectionID]}, nil
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

// TestCookieOnlyConnectionIsNotConfigured proves a connection whose only credential
// is a session cookie is reported as "no usage reader" rather than as a failed
// read: no reader reads Credential.Cookie, and a failed read must not make an
// otherwise healthy account look broken.
func TestCookieOnlyConnectionIsNotConfigured(t *testing.T) {
	now := time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)
	connections := fakeConnections{byProvider: map[string][]credentials.Connection{
		"grok-web": {{ID: "cookie-only", ProviderID: "grok-web", Name: "Cookie", Enabled: true}},
	}}
	credentialsSource := fakeCredentials{cookies: map[string]string{"cookie-only": "session=fixture"}}
	observer := Observer{Clients: map[string]UsageClient{"grok-web": &fakeClient{state: State{}}}, Now: func() time.Time { return now }}
	service := NewService(connections, credentialsSource, observer)
	// ErrNotConfigured means "nothing to read", so the aggregate refresh must skip
	// it rather than report the provider as failed. Returning it here made one
	// cookie-only connection poison RefreshProvider and RefreshAll.
	if err := service.RefreshProvider(context.Background(), "grok-web"); err != nil {
		t.Fatalf("cookie-only connection err=%v, want nil (skipped)", err)
	}
	// Not a failed read, so it must not surface as one.
	if _, reported := service.LastError("cookie-only"); reported {
		t.Fatal("a provider with no usage endpoint was recorded as a read failure")
	}
	// The sentinel must still be what the connection reports, so a caller that asks
	// about one connection can tell "unreadable" from "healthy".
	if err := service.refreshConnection(context.Background(), connections.byProvider["grok-web"][0]); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("per-connection err=%v, want ErrNotConfigured", err)
	}
}

func TestLastStateIsolatedFromConcurrentReaders(t *testing.T) {
	now := time.Now()
	remaining := .5
	credits := 2.0
	client := &fakeClient{state: State{Windows: []Window{{Name: "session", Remaining: &remaining, ResetAt: &now}}, AvailableReset: &credits}}
	s := NewService(fakeConnections{}, fakeCredentials{}, Observer{Clients: map[string]UsageClient{"codex": client}})
	if _, err := s.observe(context.Background(), "codex", "a", Credential{}); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			state, ok := s.LastState("a")
			if !ok {
				t.Error("missing state")
				return
			}
			*state.Windows[0].Remaining = 0
			*state.Windows[0].ResetAt = time.Time{}
			*state.AvailableReset = 0
		}()
	}
	wg.Wait()
	state, _ := s.LastState("a")
	if *state.Windows[0].Remaining != .5 || state.Windows[0].ResetAt.IsZero() || *state.AvailableReset != 2 {
		t.Fatal("readers mutated cached state")
	}
}

func TestFailedReadKeepsLastGoodDetail(t *testing.T) {
	remaining := .5
	client := &fakeClient{state: State{Plan: "plus", Windows: []Window{{Name: "session", Remaining: &remaining}}}}
	s := NewService(fakeConnections{}, fakeCredentials{}, Observer{Clients: map[string]UsageClient{"codex": client}})
	if _, err := s.observe(context.Background(), "codex", "a", Credential{}); err != nil {
		t.Fatal(err)
	}
	client.err = errors.New("outage")
	if _, err := s.observe(context.Background(), "codex", "a", Credential{}); err == nil {
		t.Fatal("expected read error")
	}
	state, _ := s.LastState("a")
	if state.Plan != "plus" || len(state.Windows) != 1 {
		t.Fatalf("outage wiped last good detail: %+v", state)
	}
}

func TestForgetAndPruneDropCachedDetail(t *testing.T) {
	remaining := .5
	client := &fakeClient{state: State{Plan: "plus", Windows: []Window{{Name: "session", Remaining: &remaining}}}}
	conns := fakeConnections{byProvider: map[string][]credentials.Connection{"codex": {{ID: "a", ProviderID: "codex", Enabled: true, AuthKind: credentials.AuthOAuth}, {ID: "b", ProviderID: "codex", Enabled: true, AuthKind: credentials.AuthOAuth}}}}
	s := NewService(conns, fakeCredentials{values: map[string]string{"a": "x", "b": "x"}}, Observer{Clients: map[string]UsageClient{"codex": client}})
	if err := s.RefreshProvider(context.Background(), "codex"); err != nil {
		t.Fatal(err)
	}
	s.Forget("a")
	if _, ok := s.LastState("a"); ok {
		t.Fatal("Forget kept state")
	}
	conns.byProvider["codex"] = conns.byProvider["codex"][:1] // "b" removed
	if err := s.RefreshProvider(context.Background(), "codex"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.LastState("b"); ok {
		t.Fatal("removed connection still cached")
	}
	if _, ok := s.LastState("a"); !ok {
		t.Fatal("live connection not refreshed")
	}
}
