package credentials

import (
	"context"
	"errors"
	"testing"
	"time"
)

func forceRig(t *testing.T, refreshToken string) (*Registry, string, *int) {
	t.Helper()
	store, _ := testStore(t)
	node := seedNode(t, store, "codex")
	ctx := context.Background()
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "old", RefreshToken: refreshToken, Expiry: time.Now().Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(store, nil)
	exchanges := 0
	registry.Register(connection, func(context.Context, Secret) (Secret, error) {
		exchanges++
		return Secret{AccessToken: "new", RefreshToken: "r2", Expiry: time.Now().Add(time.Hour)}, nil
	})
	return registry, connection.ID, &exchanges
}

// A token the provider refused is replaced although its expiry has not passed.
func TestForceRefreshReplacesARefusedUnexpiredToken(t *testing.T) {
	registry, id, exchanges := forceRig(t, "r1")
	secret, err := registry.ForceRefresh(context.Background(), id, "old")
	if err != nil || secret.AccessToken != "new" || *exchanges != 1 {
		t.Fatalf("%+v %v exchanges %d", secret, err, *exchanges)
	}
}

// Once another caller has rotated past the refused token, the newer credential
// is used and the refresh token is not spent a second time.
func TestForceRefreshDoesNotRotateTwice(t *testing.T) {
	registry, id, exchanges := forceRig(t, "r1")
	if _, err := registry.ForceRefresh(context.Background(), id, "old"); err != nil {
		t.Fatal(err)
	}
	secret, err := registry.ForceRefresh(context.Background(), id, "old")
	if err != nil || secret.AccessToken != "new" || *exchanges != 1 {
		t.Fatalf("%+v %v exchanges %d", secret, err, *exchanges)
	}
}

func TestForceRefreshWithoutRefreshTokenFails(t *testing.T) {
	registry, id, exchanges := forceRig(t, "")
	if _, err := registry.ForceRefresh(context.Background(), id, "old"); !errors.Is(err, ErrNoRefreshToken) || *exchanges != 0 {
		t.Fatalf("%v exchanges %d", err, *exchanges)
	}
}

func forceRigWith(t *testing.T, exchange func(Secret) (Secret, error)) (*Registry, string, *int) {
	t.Helper()
	store, _ := testStore(t)
	node := seedNode(t, store, "codex")
	connection, err := store.PutConnection(context.Background(), Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "old", RefreshToken: "r1", Expiry: time.Now().Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(store, nil)
	exchanges := 0
	registry.Register(connection, func(_ context.Context, current Secret) (Secret, error) {
		exchanges++
		return exchange(current)
	})
	return registry, connection.ID, &exchanges
}

func markRejected(t *testing.T, registry *Registry, id, token string) {
	t.Helper()
	current, err := registry.lookup(id)
	if err != nil {
		t.Fatal(err)
	}
	current.mu.Lock()
	current.rejected = token
	current.mu.Unlock()
}

func rejectedOf(t *testing.T, registry *Registry, id string) string {
	t.Helper()
	current, _ := registry.lookup(id)
	current.mu.Lock()
	defer current.mu.Unlock()
	return current.rejected
}

// A forced refresh that fails is one attempt: later reads take the ordinary
// expiry path and do not exchange again.
func TestFailedForceRefreshDoesNotExchangeOnEveryRead(t *testing.T) {
	registry, id, exchanges := forceRigWith(t, func(Secret) (Secret, error) { return Secret{}, errors.New("provider down") })
	if _, err := registry.ForceRefresh(context.Background(), id, "old"); err == nil {
		t.Fatal("forced refresh should fail")
	}
	for i := 0; i < 3; i++ {
		secret, err := registry.Secret(context.Background(), id)
		if err != nil || secret.AccessToken != "old" {
			t.Fatalf("read %d: %+v %v", i, secret, err)
		}
	}
	if *exchanges != 1 || rejectedOf(t, registry, id) != "" {
		t.Fatalf("exchanges %d, marker %q", *exchanges, rejectedOf(t, registry, id))
	}
}

// An exchange that hands back the same access token still commits a valid
// credential, so the marker must not keep it invalid.
func TestForceRefreshReturningTheSameTokenIsServedWithoutFurtherExchanges(t *testing.T) {
	registry, id, exchanges := forceRigWith(t, func(current Secret) (Secret, error) {
		return Secret{AccessToken: current.AccessToken, RefreshToken: "r2", Expiry: time.Now().Add(time.Hour)}, nil
	})
	if secret, err := registry.ForceRefresh(context.Background(), id, "old"); err != nil || secret.AccessToken != "old" {
		t.Fatalf("%+v %v", secret, err)
	}
	for i := 0; i < 3; i++ {
		if _, err := registry.Secret(context.Background(), id); err != nil {
			t.Fatal(err)
		}
	}
	if *exchanges != 1 || rejectedOf(t, registry, id) != "" {
		t.Fatalf("exchanges %d, marker %q", *exchanges, rejectedOf(t, registry, id))
	}
}

// An operator re-import or edit that installs the very token that was refused
// is served as installed, not rotated away.
func TestInstallClearsTheRefusedMarkerEvenForTheSameToken(t *testing.T) {
	never := func(Secret) (Secret, error) { return Secret{}, errors.New("must not be called") }
	t.Run("import", func(t *testing.T) {
		registry, id, exchanges := forceRigWith(t, never)
		markRejected(t, registry, id, "old")
		if err := registry.Import(context.Background(), id, "acct", Secret{AccessToken: "old", RefreshToken: "r1", Expiry: time.Now().Add(time.Hour)}); err != nil {
			t.Fatal(err)
		}
		if secret, err := registry.Secret(context.Background(), id); err != nil || secret.AccessToken != "old" || *exchanges != 0 {
			t.Fatalf("%+v %v exchanges %d", secret, err, *exchanges)
		}
	})
	t.Run("update", func(t *testing.T) {
		registry, id, exchanges := forceRigWith(t, never)
		markRejected(t, registry, id, "old")
		if _, err := registry.Update(context.Background(), id, func(c Connection) (Connection, error) { c.Name = "renamed"; return c, nil }); err != nil {
			t.Fatal(err)
		}
		if secret, err := registry.Secret(context.Background(), id); err != nil || secret.AccessToken != "old" || *exchanges != 0 {
			t.Fatalf("%+v %v exchanges %d", secret, err, *exchanges)
		}
	})
}

// Register installs a credential that differs from the one held, which clears the
// marker; one equal to it changes nothing, so the refusal still stands and the
// next read refreshes (a refresh in flight must not be superseded, #252).
func TestRegisterClearsTheRefusedMarkerOnlyForADifferentCredential(t *testing.T) {
	exchange := func(Secret) (Secret, error) {
		return Secret{AccessToken: "new", RefreshToken: "r2", Expiry: time.Now().Add(time.Hour)}, nil
	}
	calls := 0
	exchanger := func(_ context.Context, current Secret) (Secret, error) { calls++; return exchange(current) }
	same, id, _ := forceRigWith(t, exchange)
	markRejected(t, same, id, "old")
	stored, _ := same.lookup(id)
	connection := stored.connection
	same.Register(connection, exchanger)
	if rejectedOf(t, same, id) != "old" {
		t.Fatal("re-registering the identical credential cleared the refusal")
	}
	if secret, err := same.Secret(context.Background(), id); err != nil || secret.AccessToken != "new" || calls != 1 {
		t.Fatalf("%+v %v exchanges %d", secret, err, calls)
	}

	calls = 0
	other, id, _ := forceRigWith(t, exchange)
	markRejected(t, other, id, "old")
	stored, _ = other.lookup(id)
	connection = stored.connection
	connection.Secret = Secret{AccessToken: "installed", RefreshToken: "r3", Expiry: time.Now().Add(time.Hour)}
	other.Register(connection, exchanger)
	if secret, err := other.Secret(context.Background(), id); err != nil || secret.AccessToken != "installed" || calls != 0 || rejectedOf(t, other, id) != "" {
		t.Fatalf("%+v %v exchanges %d marker %q", secret, err, calls, rejectedOf(t, other, id))
	}
}
