package credentials

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

// #220: the validation lifecycle is stored beside the connection, the expiry
// column follows every new secret, and refresh rotations of several accounts at
// once survive a restart with their own credentials.

func TestValidationStatusLifecycle(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "claude")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthAPIKey, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "fixture-status-key"}})
	if err != nil {
		t.Fatal(err)
	}
	status, err := store.GetConnectionStatus(ctx, connection.ID)
	if err != nil || status.TestStatus != StatusUntested || status.LastTestedAt != "" || status.ModelsAvailable != nil {
		t.Fatalf("new: %+v err=%v", status, err)
	}
	at := time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC)
	if err := store.RecordValidation(ctx, connection.ID, Validation{Error: "provider rejected credentials", At: at}); err != nil {
		t.Fatal(err)
	}
	status, _ = store.GetConnectionStatus(ctx, connection.ID)
	if status.TestStatus != StatusFailed || status.LastError != "provider rejected credentials" || status.LastErrorAt != "2026-10-27T08:00:00.000Z" || status.LastTestedAt != status.LastErrorAt {
		t.Fatalf("failed: %+v", status)
	}
	if err := store.RecordValidation(ctx, connection.ID, Validation{OK: true, ModelsAvailable: 3, At: at.Add(time.Minute)}); err != nil {
		t.Fatal(err)
	}
	status, _ = store.GetConnectionStatus(ctx, connection.ID)
	if status.TestStatus != StatusActive || status.LastError != "" || status.LastErrorAt != "" || status.ModelsAvailable == nil || *status.ModelsAvailable != 3 || status.LastTestedAt != "2026-10-27T08:01:00.000Z" {
		t.Fatalf("passed: %+v", status)
	}
	// A metadata write keeps the status; it is not part of the configuration.
	if err := store.SetConnectionEnabled(ctx, connection.ID, false); err != nil {
		t.Fatal(err)
	}
	if status, _ = store.GetConnectionStatus(ctx, connection.ID); status.TestStatus != StatusActive {
		t.Fatalf("after toggle: %+v", status)
	}
	if err := store.MarkCredentialUntested(ctx, connection.ID, OriginManual); err != nil {
		t.Fatal(err)
	}
	status, _ = store.GetConnectionStatus(ctx, connection.ID)
	if status.TestStatus != StatusUntested || status.AuthOrigin != OriginManual || status.LastTestedAt != "" || status.ModelsAvailable != nil {
		t.Fatalf("replaced: %+v", status)
	}
	if err := store.RecordValidation(ctx, connection.ID, Validation{Error: "boom"}); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkCredentialAccepted(ctx, connection.ID, OriginOAuth); err != nil {
		t.Fatal(err)
	}
	status, _ = store.GetConnectionStatus(ctx, connection.ID)
	if status.TestStatus != StatusActive || status.AuthOrigin != OriginOAuth || status.LastError != "" || status.LastTestedAt == "" {
		t.Fatalf("accepted: %+v", status)
	}
	for _, write := range []error{
		store.RecordValidation(ctx, "missing", Validation{OK: true}),
		store.MarkCredentialAccepted(ctx, "missing", OriginOAuth),
		store.MarkCredentialUntested(ctx, "missing", OriginManual),
	} {
		if !errors.Is(write, ErrNotFound) {
			t.Fatalf("missing connection: %v", write)
		}
	}
	if _, err := store.GetConnectionStatus(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing status: %v", err)
	}
	if _, err := store.db.ExecContext(ctx, "UPDATE provider_connections SET test_status='unknown' WHERE id=?", connection.ID); err == nil {
		t.Fatal("test_status accepted a value outside untested/active/failed")
	}
}

func TestExpiryColumnFollowsTheSealedSecret(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "claude")
	first := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "fixture-a0", RefreshToken: "fixture-r0", Expiry: first}})
	if err != nil {
		t.Fatal(err)
	}
	expiry := func() string {
		status, err := store.GetConnectionStatus(ctx, connection.ID)
		if err != nil {
			t.Fatal(err)
		}
		return status.ExpiresAt
	}
	if got := expiry(); got != "2026-11-01T00:00:00.000Z" {
		t.Fatalf("created expiry=%q", got)
	}
	// An edit without credential material keeps the stored secret and its expiry.
	connection.Secret = Secret{}
	connection.Name = "renamed"
	if _, err := store.PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	if got := expiry(); got != "2026-11-01T00:00:00.000Z" {
		t.Fatalf("metadata edit expiry=%q", got)
	}
	if err := store.RotateSecret(ctx, connection.ID, Secret{AccessToken: "fixture-a1", RefreshToken: "fixture-r1", Expiry: first.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if got := expiry(); got != "2026-11-01T01:00:00.000Z" {
		t.Fatalf("rotated expiry=%q", got)
	}
	// A secret without an expiry clears it.
	if err := store.RotateSecret(ctx, connection.ID, Secret{AccessToken: "fixture-a2"}); err != nil {
		t.Fatal(err)
	}
	if got := expiry(); got != "" {
		t.Fatalf("no-expiry secret kept %q", got)
	}
}

// Two accounts refresh at once; each keeps its own rotation, and a new process
// on the same database serves the rotated credentials and reports their expiry.
func TestConcurrentRefreshRotationsSurviveRestart(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "routeweft.sqlite")
	sealer := testSealer(t)
	open := func() (*sqlite.Store, *Store) {
		t.Helper()
		db, err := sqlite.Open(ctx, path)
		if err != nil {
			t.Fatal(err)
		}
		if err := migrations.NewRunner(db.DB()).Apply(ctx); err != nil {
			t.Fatal(err)
		}
		store, err := NewStore(db.DB(), sealer)
		if err != nil {
			t.Fatal(err)
		}
		return db, store
	}
	db, store := open()
	node := seedNode(t, store, "claude")
	expiry := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Millisecond)
	registry := NewRegistry(store, nil)
	ids := make([]string, 2)
	for i := range ids {
		connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: fmt.Sprint("account-", i), AuthKind: AuthOAuth, Identity: fmt.Sprint("acct-", i), Enabled: true, Secret: Secret{AccessToken: fmt.Sprint("fixture-stale-", i), RefreshToken: fmt.Sprint("fixture-r0-", i), Expiry: time.Now().Add(-time.Minute)}})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = connection.ID
		account := i
		registry.Register(connection, func(_ context.Context, current Secret) (Secret, error) {
			if current.RefreshToken != fmt.Sprint("fixture-r0-", account) {
				return Secret{}, fmt.Errorf("account %d refreshed with another account's token", account)
			}
			return Secret{AccessToken: fmt.Sprint("fixture-fresh-", account), RefreshToken: fmt.Sprint("fixture-r1-", account), Expiry: expiry}, nil
		})
	}
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for round := 0; round < 4; round++ {
		for i, id := range ids {
			wg.Add(1)
			go func(account int, id string) {
				defer wg.Done()
				token, err := registry.Credential(ctx, id)
				if err == nil && token != fmt.Sprint("fixture-fresh-", account) {
					err = fmt.Errorf("account %d served another token", account)
				}
				if err != nil {
					errs <- err
				}
			}(i, id)
		}
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, restarted := open()
	defer reopened.Close()
	fresh := NewRegistry(restarted, nil)
	if _, err := fresh.Load(ctx, "claude"); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		token, err := fresh.Credential(ctx, id)
		if err != nil || token != fmt.Sprint("fixture-fresh-", i) {
			t.Fatalf("account %d after restart: err=%v rotated=%v", i, err, token == fmt.Sprint("fixture-fresh-", i))
		}
		stored, err := restarted.GetConnection(ctx, id)
		if err != nil || stored.Secret.RefreshToken != fmt.Sprint("fixture-r1-", i) {
			t.Fatalf("account %d refresh token not durable: err=%v", i, err)
		}
		status, err := restarted.GetConnectionStatus(ctx, id)
		if err != nil || status.ExpiresAt != statusTime(expiry) {
			t.Fatalf("account %d expiry after restart: %+v err=%v", i, status, err)
		}
	}
}
