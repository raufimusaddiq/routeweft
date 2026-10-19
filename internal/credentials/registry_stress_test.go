package credentials

import (
	"context"

	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestOAuthRefreshStressSingleflightUnderLoad hammers one expiring OAuth
// connection from many callers across repeated refresh cycles and proves the
// registry collapses each cycle to exactly one provider exchange, returns one
// consistent token to every caller, and leaves memory equal to durable state
// (PRD-AUTH-003, SPEC §19; SPEC §28.2 OAuth singleflight).
func TestOAuthRefreshStressSingleflightUnderLoad(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "claude")
	start := time.Now()
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "stale", RefreshToken: "r0", Expiry: start.Add(-time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}

	const callers = 24
	const cycles = 20
	var exchanges atomic.Int64
	var current tokenSource
	current.Store("stale")
	registry := NewRegistry(store, func() time.Time { return time.Now() })
	registry.Register(connection, func(_ context.Context, stored Secret) (Secret, error) {
		n := exchanges.Add(1)
		// Each exchange still sees the durable refresh token; the access token
		// rotates once per cycle.
		if stored.RefreshToken != "r0" {
			t.Errorf("exchange %d saw refresh token %q", n, stored.RefreshToken)
		}
		token := fmt.Sprintf("token-%d", n)
		current.Store(token)
		return Secret{AccessToken: token, RefreshToken: "r0", Expiry: time.Now().Add(time.Hour)}, nil
	})

	// Force an expiry before each cycle so callers see a refresh-worthy token and
	// race through the singleflight gate together.
	expire := func() {
		entry, ok := registry.entries[connection.ID]
		if !ok {
			t.Fatal("connection missing from registry")
		}
		entry.mu.Lock()
		entry.current.Expiry = time.Now().Add(-time.Second)
		entry.mu.Unlock()
	}

	for cycle := 0; cycle < cycles; cycle++ {
		expire()
		before := exchanges.Load()
		var wg sync.WaitGroup
		results := make([]string, callers)
		errs := make([]error, callers)
		for i := 0; i < callers; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				results[i], errs[i] = registry.Credential(ctx, connection.ID)
			}(i)
		}
		wg.Wait()
		if got := exchanges.Load() - before; got != 1 {
			t.Fatalf("cycle %d performed %d exchanges, want 1", cycle, got)
		}
		want := current.Load()
		for i := range results {
			if errs[i] != nil {
				t.Fatalf("cycle %d caller %d err=%v", cycle, i, errs[i])
			}
			if results[i] != want {
				t.Fatalf("cycle %d caller %d token=%q, want %q", cycle, i, results[i], want)
			}
		}
	}

	// Durable state must match the last published token after the stress run.
	loaded, err := store.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Secret.AccessToken != current.Load() || loaded.Secret.RefreshToken != "r0" {
		t.Fatalf("durable secret drifted: %+v want token %q", loaded.Secret, current.Load())
	}
}

// TestOAuthRefreshStressImportNeverLosesNewestCredential runs refreshes and
// concurrent operator imports against one connection and proves the newest
// install always wins in both memory and durable storage, never a stale
// exchange result (SPEC §35; SPEC §19 generation check).
func TestOAuthRefreshStressImportNeverLosesNewestCredential(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "codex")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "stale", RefreshToken: "r0", Expiry: time.Now().Add(-time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(store, nil)
	registry.Register(connection, func(context.Context, Secret) (Secret, error) {
		time.Sleep(time.Millisecond)
		return Secret{AccessToken: "rotated", RefreshToken: "r0", Expiry: time.Now().Add(time.Hour)}, nil
	})

	const rounds = 40
	for i := 0; i < rounds; i++ {
		imported := fmt.Sprintf("imported-%d", i)
		var wg sync.WaitGroup
		wg.Add(2)
		go func() {
			defer wg.Done()
			// Reset to expiring so the next Credential triggers a refresh race.
			entry := registry.entries[connection.ID]
			entry.mu.Lock()
			entry.current.Expiry = time.Now().Add(-time.Second)
			entry.mu.Unlock()
			_, _ = registry.Credential(ctx, connection.ID)
		}()
		go func() {
			defer wg.Done()
			if err := registry.Import(ctx, connection.ID, "acct", Secret{AccessToken: imported, RefreshToken: "r0", Expiry: time.Now().Add(time.Hour)}); err != nil {
				t.Errorf("import %d: %v", i, err)
			}
		}()
		wg.Wait()

		// Whatever order won, memory and durable storage must agree and must hold a
		// valid, non-stale credential; the registry promises they never diverge.
		secret, err := registry.Secret(ctx, connection.ID)
		if err != nil {
			t.Fatalf("round %d secret: %v", i, err)
		}
		loaded, err := store.GetConnection(ctx, connection.ID)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Secret.AccessToken != secret.AccessToken {
			t.Fatalf("round %d memory token %q != durable token %q", i, secret.AccessToken, loaded.Secret.AccessToken)
		}
		if secret.AccessToken != imported && secret.AccessToken != "rotated" {
			t.Fatalf("round %d unexpected token %q", i, secret.AccessToken)
		}
	}
}

// TestOAuthRefreshStressSupersededServesNewerImport proves that when an import
// lands during an in-flight exchange, callers waiting on that exchange receive
// the newer imported credential rather than an error or the stale rotate result.
func TestOAuthRefreshStressSupersededServesNewerImport(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "github")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "stale", RefreshToken: "r0", Expiry: time.Now().Add(-time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	exchangeStarted := make(chan struct{})
	releaseExchange := make(chan struct{})
	registry := NewRegistry(store, nil)
	registry.Register(connection, func(context.Context, Secret) (Secret, error) {
		close(exchangeStarted)
		<-releaseExchange
		return Secret{AccessToken: "rotated", RefreshToken: "r0", Expiry: time.Now().Add(time.Hour)}, nil
	})

	var got string
	var gotErr error
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		got, gotErr = registry.Credential(ctx, connection.ID)
	}()
	<-exchangeStarted
	if err := registry.Import(ctx, connection.ID, "acct", Secret{AccessToken: "imported", RefreshToken: "r0", Expiry: time.Now().Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	close(releaseExchange)
	wg.Wait()
	if gotErr != nil {
		t.Fatalf("superseded caller err=%v", gotErr)
	}
	if got != "imported" {
		t.Fatalf("superseded caller got %q, want imported", got)
	}
	loaded, err := store.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Secret.AccessToken != "imported" {
		t.Fatalf("durable token %q, want imported", loaded.Secret.AccessToken)
	}
}

// tokenSource is a tiny atomic string holder for asserting the current published
// access token across cycles.
type tokenSource struct {
	mu sync.Mutex
	v  string
}

func (t *tokenSource) Store(v string) { t.mu.Lock(); t.v = v; t.mu.Unlock() }
func (t *tokenSource) Load() string   { t.mu.Lock(); defer t.mu.Unlock(); return t.v }
