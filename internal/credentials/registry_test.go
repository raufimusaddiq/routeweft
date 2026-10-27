package credentials

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func seedNode(t *testing.T, store *Store, providerID string) Node {
	t.Helper()
	node, err := store.PutNode(context.Background(), Node{Kind: NodeBuiltin, ProviderID: providerID, Name: providerID})
	if err != nil {
		t.Fatal(err)
	}
	return node
}

func TestSecretReturnsFreshWithoutRefresh(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "codex")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "static", Expiry: time.Now().Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(store, nil)
	registry.Register(connection, func(context.Context, Secret) (Secret, error) {
		t.Fatal("refresh should not run for a fresh token")
		return Secret{}, nil
	})
	credential, err := registry.Credential(ctx, connection.ID)
	if err != nil || credential != "static" {
		t.Fatalf("credential=%q err=%v", credential, err)
	}
}

func TestExpiredTokenRefreshesSingleflightAndPersists(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "claude")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "stale", RefreshToken: "r0", Expiry: time.Now().Add(-time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int64
	registry := NewRegistry(store, nil)
	registry.Register(connection, func(_ context.Context, current Secret) (Secret, error) {
		calls.Add(1)
		if current.RefreshToken != "r0" {
			t.Errorf("refresh token=%q", current.RefreshToken)
		}
		time.Sleep(20 * time.Millisecond)
		return Secret{AccessToken: "fresh", Expiry: time.Now().Add(time.Hour)}, nil
	})
	var wg sync.WaitGroup
	results := make([]string, 8)
	errs := make([]error, 8)
	for i := range results {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			results[index], errs[index] = registry.Credential(ctx, connection.ID)
		}(i)
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatalf("refresh calls=%d, want 1", calls.Load())
	}
	for i := range results {
		if errs[i] != nil || results[i] != "fresh" {
			t.Fatalf("call %d credential=%q err=%v", i, results[i], errs[i])
		}
	}
	// The rotated token must be durable so a restart keeps working.
	loaded, err := store.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Secret.AccessToken != "fresh" || loaded.Secret.RefreshToken != "r0" {
		t.Fatalf("durable secret=%+v", loaded.Secret)
	}
}

func TestRefreshKeepsRotatedTokenAndRejectsEmptyResult(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "github")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Secret: Secret{AccessToken: "stale", RefreshToken: "r0", Expiry: time.Now().Add(-time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(store, nil)
	registry.Register(connection, func(context.Context, Secret) (Secret, error) {
		return Secret{AccessToken: "only-access", Expiry: time.Now().Add(time.Hour)}, nil
	})
	secret, err := registry.Secret(ctx, connection.ID)
	if err != nil || secret.RefreshToken != "r0" || secret.AccessToken != "only-access" {
		t.Fatalf("secret=%+v err=%v", secret, err)
	}
	// An empty refresh payload must fail without destroying the durable secret.
	expired := connection
	expired.Secret = Secret{AccessToken: "stale", RefreshToken: "r0", Expiry: time.Now().Add(-time.Minute)}
	registry.Register(expired, func(context.Context, Secret) (Secret, error) { return Secret{}, nil })
	if _, err := registry.Secret(ctx, connection.ID); err == nil {
		t.Fatal("empty refresh payload was accepted")
	}
	loaded, err := store.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Secret.AccessToken != "only-access" {
		t.Fatalf("failed refresh destroyed the durable credential: %+v", loaded.Secret)
	}
}

func TestProviderErrorLeavesDurableCredentialIntact(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "xai")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Secret: Secret{AccessToken: "stale", RefreshToken: "r0", Expiry: time.Now().Add(-time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(store, nil)
	wantErr := errors.New("upstream refresh failed")
	registry.Register(connection, func(context.Context, Secret) (Secret, error) { return Secret{}, wantErr })
	if _, err := registry.Credential(ctx, connection.ID); !errors.Is(err, wantErr) {
		t.Fatalf("err=%v", err)
	}
	loaded, err := store.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Secret.RefreshToken != "r0" || loaded.Secret.AccessToken != "stale" {
		t.Fatalf("failed refresh mutated durable state: %+v", loaded.Secret)
	}
}

func TestImportValidatesIdentityAndUsesDurablePath(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "kimi")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthAPIKey, Identity: "acct-a", Secret: Secret{AccessToken: "old"}})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(store, nil)
	registry.Register(connection, nil)
	if err := registry.Import(ctx, connection.ID, "acct-b", Secret{AccessToken: "new"}); err == nil {
		t.Fatal("mismatched identity import was accepted")
	}
	if err := registry.Import(ctx, connection.ID, "acct-a", Secret{}); err == nil {
		t.Fatal("empty import was accepted")
	}
	if err := registry.Import(ctx, connection.ID, "acct-a", Secret{AccessToken: "imported", RefreshToken: "ir"}); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Secret.AccessToken != "imported" || loaded.Secret.RefreshToken != "ir" {
		t.Fatalf("secret=%+v", loaded.Secret)
	}
	credential, err := registry.Credential(ctx, connection.ID)
	if err != nil || credential != "imported" {
		t.Fatalf("credential=%q err=%v", credential, err)
	}
	var count int
	if err := store.db.QueryRowContext(ctx, "SELECT COUNT(*) FROM credential_events WHERE connection_id=? AND event='import'", connection.ID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("import audit rows=%d", count)
	}
}

func TestUnknownConnectionAndForget(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	registry := NewRegistry(store, nil)
	if _, err := registry.Credential(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing err=%v", err)
	}
	node := seedNode(t, store, "zed")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthCookie, Identity: "acct", Secret: Secret{Cookie: "session=fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	registry.Register(connection, nil)
	credential, err := registry.Credential(ctx, connection.ID)
	if err != nil || !strings.Contains(credential, "session=") {
		t.Fatalf("cookie credential=%q err=%v", credential, err)
	}
	registry.Forget(connection.ID)
	if _, err := registry.Credential(ctx, connection.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("after forget err=%v", err)
	}
}

func TestLoadSeedsProviderConnections(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "groq")
	for _, name := range []string{"a", "b"} {
		if _, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: name, AuthKind: AuthAPIKey, Identity: name, Enabled: true, Secret: Secret{AccessToken: "k-" + name}}); err != nil {
			t.Fatal(err)
		}
	}
	registry := NewRegistry(store, nil)
	connections, err := registry.Load(ctx, "groq")
	if err != nil {
		t.Fatal(err)
	}
	if len(connections) != 2 {
		t.Fatalf("loaded %d connections", len(connections))
	}
	credential, err := registry.Credential(ctx, connections[0].ID)
	if err != nil || !strings.HasPrefix(credential, "k-") {
		t.Fatalf("credential=%q err=%v", credential, err)
	}
}

func TestNamedCredentialSelectionAfterRefresh(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "fixture-provider")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, ProviderID: "fixture-provider", AuthKind: AuthOAuth, Name: "primary", Identity: "fixture", Secret: Secret{AccessToken: "fixture-access", Expiry: time.Now().Add(-time.Hour), ProviderData: map[string]string{"wireToken": "fixture-stale"}}})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(store, nil)
	registry.SetCredentialNameResolver(func(c Connection) (string, string) {
		if c.ProviderID != "fixture-provider" {
			t.Fatal("wrong provider identity")
		}
		return "wireToken", "wireTokenExpiresAt"
	})
	registry.Register(connection, func(_ context.Context, current Secret) (Secret, error) {
		current.ProviderData = map[string]string{"wireToken": "fixture-fresh", "wireTokenExpiresAt": fmt.Sprint(time.Now().Add(time.Hour).Unix())}
		current.Expiry = time.Now().Add(time.Hour)
		return current, nil
	})
	got, err := registry.Credential(ctx, connection.ID)
	if err != nil || got != "fixture-fresh" {
		t.Fatalf("named credential not selected after refresh: %v", err)
	}
	secret, err := registry.Secret(ctx, connection.ID)
	if err != nil || secret.AccessToken != "fixture-access" {
		t.Fatalf("primary credential changed: %v", err)
	}
	for _, tc := range []struct {
		secret Secret
		want   string
	}{
		{Secret{AccessToken: "fallback", ProviderData: map[string]string{"wireToken": ""}}, "fixture-fresh"},
		{Secret{AccessToken: "fallback"}, "fixture-fresh"},
		{Secret{Cookie: "fixture-cookie"}, "fixture-fresh"},
		{Secret{AccessToken: "fallback", ProviderData: map[string]string{"wireToken": "fixture-imported", "wireTokenExpiresAt": fmt.Sprint(time.Now().Add(time.Hour).Unix())}}, "fixture-imported"},
	} {
		if err := registry.Import(ctx, connection.ID, connection.Identity, tc.secret); err != nil {
			t.Fatal(err)
		}
		got, err := registry.Credential(ctx, connection.ID)
		if err != nil || got != tc.want {
			t.Fatalf("credential selection failed: %v", err)
		}
	}
}

func TestNamedCredentialExpiryRefreshesSingleflight(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2026, 10, 20, 0, 0, 0, 0, time.UTC)
	future := fmt.Sprint(now.Add(time.Hour).Unix())
	for _, tc := range []struct {
		name, token, expires string
		refresh              bool
	}{
		{"fresh", "fixture-old", future, false},
		{"near expiry", "fixture-old", fmt.Sprint(now.Add(30 * time.Second).Unix()), true},
		{"refresh boundary", "fixture-old", fmt.Sprint(now.Add(RefreshLead).Unix()), true},
		{"expired", "fixture-old", fmt.Sprint(now.Add(-time.Second).Unix()), true},
		{"missing expiry", "fixture-old", "", true},
		{"invalid expiry", "fixture-old", "invalid", true},
		{"zero expiry", "fixture-old", "0", true},
		{"missing token", "", future, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, _ := testStore(t)
			node := seedNode(t, store, "fixture-provider")
			connection, err := store.PutConnection(ctx, Connection{
				NodeID: node.ID, ProviderID: "fixture-provider", Name: "primary", Identity: "fixture", AuthKind: AuthOAuth,
				Secret: Secret{AccessToken: "fixture-primary", ProviderData: map[string]string{"wireToken": tc.token, "wireTokenExpiresAt": tc.expires}},
			})
			if err != nil {
				t.Fatal(err)
			}
			registry := NewRegistry(store, func() time.Time { return now })
			registry.SetCredentialNameResolver(func(Connection) (string, string) { return "wireToken", "wireTokenExpiresAt" })
			var remints atomic.Int32
			registry.Register(connection, func(_ context.Context, current Secret) (Secret, error) {
				remints.Add(1)
				current.ProviderData = map[string]string{"wireToken": "fixture-fresh", "wireTokenExpiresAt": future}
				return current, nil
			})
			var wg sync.WaitGroup
			results := make([]string, 8)
			failures := make([]error, len(results))
			for i := range results {
				wg.Add(1)
				go func(i int) { defer wg.Done(); results[i], failures[i] = registry.Credential(ctx, connection.ID) }(i)
			}
			wg.Wait()
			want, calls := tc.token, int32(0)
			if tc.refresh {
				want, calls = "fixture-fresh", 1
			}
			if remints.Load() != calls {
				t.Fatalf("remints=%d, want %d", remints.Load(), calls)
			}
			for i := range results {
				if failures[i] != nil || results[i] != want {
					t.Fatalf("caller %d selected the wrong credential: %v", i, failures[i])
				}
			}
			saved, err := store.GetConnection(ctx, connection.ID)
			if err != nil {
				t.Fatal(err)
			}
			if saved.Secret.ProviderData["wireToken"] != want || saved.Secret.AccessToken != "fixture-primary" || !saved.Secret.Expiry.IsZero() {
				t.Fatal("named-token renewal did not preserve the primary token and its absent expiry")
			}
		})
	}
}

func BenchmarkCredential(b *testing.B) {
	now := time.Now()
	for _, named := range []bool{false, true} {
		name := "primary"
		if named {
			name = "named-expiry"
		}
		b.Run(name, func(b *testing.B) {
			registry := NewRegistry(nil, func() time.Time { return now })
			want := "fixture-primary"
			if named {
				registry.SetCredentialNameResolver(func(Connection) (string, string) { return "wireToken", "wireTokenExpiresAt" })
				want = "fixture-wire"
			}
			registry.Register(Connection{ID: "fixture", Secret: Secret{
				AccessToken:  "fixture-primary",
				ProviderData: map[string]string{"wireToken": "fixture-wire", "wireTokenExpiresAt": fmt.Sprint(now.Add(time.Hour).Unix())},
			}}, nil)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				got, err := registry.Credential(context.Background(), "fixture")
				if err != nil || got != want {
					b.Fatal("credential selection failed", err)
				}
			}
		})
	}
}

func TestRefreshAndImportCarryProviderDataForward(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "azure")
	data := map[string]string{"azureEndpoint": "https://example.openai.azure.com", "region": "eastus"}
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "stale", RefreshToken: "r0", Expiry: time.Now().Add(-time.Minute), ProviderData: data}})
	if err != nil {
		t.Fatal(err)
	}
	if connection.Settings.AzureEndpoint != data["azureEndpoint"] {
		t.Fatalf("fixture settings=%+v", connection.Settings)
	}
	registry := NewRegistry(store, nil)
	// The exchanger constructs a Secret without provider data, as real ones do.
	registry.Register(connection, func(context.Context, Secret) (Secret, error) {
		return Secret{AccessToken: "fresh", Expiry: time.Now().Add(time.Hour), ProviderData: map[string]string{"region": "westus"}}, nil
	})
	if _, err := registry.Secret(ctx, connection.ID); err != nil {
		t.Fatal(err)
	}
	check := func(stage, token, region string) {
		t.Helper()
		loaded, err := store.GetConnection(ctx, connection.ID)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Secret.AccessToken != token || loaded.Secret.ProviderData["azureEndpoint"] != data["azureEndpoint"] || loaded.Secret.ProviderData["region"] != region {
			t.Fatalf("%s: durable secret=%+v", stage, loaded.Secret)
		}
		if loaded.Settings.AzureEndpoint != data["azureEndpoint"] || loaded.Settings.Region != region {
			t.Fatalf("%s: projected settings=%+v", stage, loaded.Settings)
		}
	}
	check("refresh", "fresh", "westus")
	if err := registry.Import(ctx, connection.ID, "acct", Secret{AccessToken: "imported"}); err != nil {
		t.Fatal(err)
	}
	check("import", "imported", "westus")
}

// How a Codex connection was signed in (Secret.AuthType, #241) survives a
// refresh and an import, whose exchangers and payloads never carry it.
func TestRefreshAndImportCarryAuthTypeForward(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "codex")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "stale", RefreshToken: "r0", Expiry: time.Now().Add(-time.Minute), AuthType: AuthTypeOAuth}})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(store, nil)
	registry.Register(connection, func(context.Context, Secret) (Secret, error) {
		return Secret{AccessToken: "fresh", Expiry: time.Now().Add(time.Hour)}, nil
	})
	if _, err := registry.Secret(ctx, connection.ID); err != nil {
		t.Fatal(err)
	}
	check := func(stage, token string) {
		t.Helper()
		loaded, err := store.GetConnection(ctx, connection.ID)
		if err != nil {
			t.Fatal(err)
		}
		if loaded.Secret.AccessToken != token || loaded.Secret.AuthType != AuthTypeOAuth {
			t.Fatalf("%s: durable secret token=%q authType=%q", stage, loaded.Secret.AccessToken, loaded.Secret.AuthType)
		}
	}
	check("refresh", "fresh")
	if err := registry.Import(ctx, connection.ID, "acct", Secret{AccessToken: "imported", ProviderData: map[string]string{}}); err != nil {
		t.Fatal(err)
	}
	check("import", "imported")
}

// An update that lands while a refresh is exchanging waits for the refresh to
// commit and merges onto its rotated credential: the rotated refresh token is
// kept, and memory and storage agree afterwards (#241).
func TestUpdateWaitsForRefreshInFlight(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "codex")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "a0", RefreshToken: "r0", Expiry: time.Now().Add(-time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(store, nil)
	entered, release := make(chan struct{}), make(chan struct{})
	registry.Register(connection, func(_ context.Context, current Secret) (Secret, error) {
		close(entered)
		<-release
		return Secret{AccessToken: "a1", RefreshToken: "r1", Expiry: time.Now().Add(time.Hour)}, nil
	})
	refreshed := make(chan error, 1)
	go func() {
		_, err := registry.Secret(ctx, connection.ID)
		refreshed <- err
	}()
	<-entered
	updated := make(chan error, 1)
	go func() {
		_, err := registry.Update(ctx, connection.ID, func(stored Connection) (Connection, error) {
			stored.Secret.AccessToken = "imported"
			stored.Name = "renamed"
			return stored, nil
		})
		updated <- err
	}()
	select {
	case err := <-updated:
		t.Fatalf("update did not wait for the refresh in flight: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-refreshed; err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if err := <-updated; err != nil {
		t.Fatalf("update: %v", err)
	}
	loaded, err := store.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Name != "renamed" || loaded.Secret.AccessToken != "imported" || loaded.Secret.RefreshToken != "r1" {
		t.Fatalf("durable after update: name=%q access=%q refresh=%q", loaded.Name, loaded.Secret.AccessToken, loaded.Secret.RefreshToken)
	}
	memory, err := registry.Secret(ctx, connection.ID)
	if err != nil || memory.AccessToken != "imported" || memory.RefreshToken != "r1" {
		t.Fatalf("memory after update: %+v %v", memory, err)
	}
}

// A refresh that would start while an update is between its read and its
// write waits for the update and gets the credential it published; no exchange
// spends the refresh token the update read (#241).
func TestRefreshWaitsForUpdate(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "codex")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "a0", RefreshToken: "r0", Expiry: time.Now().Add(-time.Minute)}})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(store, nil)
	var exchanges atomic.Int32
	registry.Register(connection, func(context.Context, Secret) (Secret, error) {
		exchanges.Add(1)
		return Secret{AccessToken: "a1", RefreshToken: "r1", Expiry: time.Now().Add(time.Hour)}, nil
	})
	inChange, release := make(chan struct{}), make(chan struct{})
	updated := make(chan error, 1)
	go func() {
		_, err := registry.Update(ctx, connection.ID, func(stored Connection) (Connection, error) {
			close(inChange)
			<-release
			stored.Secret = Secret{AccessToken: "imported", RefreshToken: "r-imported", Expiry: time.Now().Add(time.Hour)}
			return stored, nil
		})
		updated <- err
	}()
	<-inChange
	got := make(chan Secret, 1)
	go func() {
		secret, _ := registry.Secret(ctx, connection.ID)
		got <- secret
	}()
	select {
	case secret := <-got:
		t.Fatalf("refresh did not wait for the update: %+v", secret)
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	if err := <-updated; err != nil {
		t.Fatal(err)
	}
	if secret := <-got; secret.AccessToken != "imported" || exchanges.Load() != 0 {
		t.Fatalf("waiter got %+v after %d exchanges", secret, exchanges.Load())
	}
	loaded, err := store.GetConnection(ctx, connection.ID)
	if err != nil || loaded.Secret.RefreshToken != "r-imported" {
		t.Fatalf("durable after update: %+v %v", loaded.Secret, err)
	}
}

// Requests that find an expired credential while an update holds the refresh
// slot check it again when the update is done: whether the update succeeded but
// left the token expired (an edit that does not touch it, an import of an
// expired one) or failed and left the old one, they refresh it, once for all of
// them, and none is handed the expired credential (#241).
func TestRefreshWaitersRecheckAfterUpdate(t *testing.T) {
	for name, change := range map[string]func(Connection) (Connection, error){
		"update keeps the expired token": func(stored Connection) (Connection, error) {
			stored.Name = "renamed"
			return stored, nil
		},
		"update stores another expired token": func(stored Connection) (Connection, error) {
			stored.Secret.AccessToken = "imported-expired"
			stored.Secret.Expiry = time.Now().Add(-time.Hour)
			return stored, nil
		},
		"update fails": func(Connection) (Connection, error) {
			return Connection{}, errors.New("change refused")
		},
	} {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()
			store, _ := testStore(t)
			node := seedNode(t, store, "codex")
			connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "expired", RefreshToken: "r0", Expiry: time.Now().Add(-time.Minute)}})
			if err != nil {
				t.Fatal(err)
			}
			registry := NewRegistry(store, nil)
			var exchanges atomic.Int32
			registry.Register(connection, func(_ context.Context, current Secret) (Secret, error) {
				exchanges.Add(1)
				return Secret{AccessToken: "fresh", RefreshToken: "r1", Expiry: time.Now().Add(time.Hour)}, nil
			})
			inChange, release := make(chan struct{}), make(chan struct{})
			updated := make(chan error, 1)
			go func() {
				_, err := registry.Update(ctx, connection.ID, func(stored Connection) (Connection, error) {
					close(inChange)
					<-release
					return change(stored)
				})
				updated <- err
			}()
			<-inChange
			const waiters = 4
			got := make(chan Secret, waiters)
			for i := 0; i < waiters; i++ {
				go func() {
					secret, err := registry.Secret(ctx, connection.ID)
					if err != nil {
						t.Errorf("secret: %v", err)
					}
					got <- secret
				}()
			}
			time.Sleep(50 * time.Millisecond)
			if exchanges.Load() != 0 {
				t.Fatal("a refresh started while the update held the slot")
			}
			close(release)
			<-updated
			for i := 0; i < waiters; i++ {
				if secret := <-got; secret.AccessToken != "fresh" {
					t.Fatalf("waiter got access %q, want the refreshed credential", secret.AccessToken)
				}
			}
			if n := exchanges.Load(); n != 1 {
				t.Fatalf("exchanges=%d, want one shared refresh", n)
			}
			loaded, err := store.GetConnection(ctx, connection.ID)
			if err != nil || loaded.Secret.AccessToken != "fresh" || loaded.Secret.RefreshToken != "r1" {
				t.Fatalf("durable after refresh: %+v %v", loaded.Secret, err)
			}
		})
	}
}
