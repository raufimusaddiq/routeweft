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
	if err := store.RecordValidation(ctx, connection.ID, 0, Validation{Error: "provider rejected credentials", At: at}); err != nil {
		t.Fatal(err)
	}
	status, _ = store.GetConnectionStatus(ctx, connection.ID)
	if status.TestStatus != StatusFailed || status.LastError != "provider rejected credentials" || status.LastErrorAt != "2026-10-27T08:00:00.000Z" || status.LastTestedAt != status.LastErrorAt {
		t.Fatalf("failed: %+v", status)
	}
	if err := store.RecordValidation(ctx, connection.ID, 0, Validation{OK: true, ModelsAvailable: 3, At: at.Add(time.Minute)}); err != nil {
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
	// A save with a new secret records its lifecycle in the same write and
	// clears the results that described the old one.
	save := func(secret Secret, lifecycle Lifecycle) {
		t.Helper()
		update := connection
		update.Secret, update.Lifecycle = secret, lifecycle
		if _, err := store.PutConnection(ctx, update); err != nil {
			t.Fatal(err)
		}
	}
	save(Secret{AccessToken: "fixture-status-key-2"}, Lifecycle{Status: StatusUntested, Origin: OriginManual})
	status, _ = store.GetConnectionStatus(ctx, connection.ID)
	if status.TestStatus != StatusUntested || status.AuthOrigin != OriginManual || status.LastTestedAt != "" || status.ModelsAvailable != nil {
		t.Fatalf("replaced: %+v", status)
	}
	stored, _ := store.GetConnection(ctx, connection.ID)
	if err := store.RecordValidation(ctx, connection.ID, stored.CredentialRevision, Validation{Error: "boom"}); err != nil {
		t.Fatal(err)
	}
	// An empty status keeps the stored lifecycle (a soft-validated duplicate),
	// and a save without a new secret never changes it, whatever it names.
	save(Secret{AccessToken: "fixture-status-key-3"}, Lifecycle{Origin: OriginImport})
	save(Secret{}, Lifecycle{Status: StatusActive, Origin: OriginOAuth})
	status, _ = store.GetConnectionStatus(ctx, connection.ID)
	if status.TestStatus != StatusFailed || status.LastError != "boom" || status.AuthOrigin != OriginImport {
		t.Fatalf("kept: %+v", status)
	}
	save(Secret{AccessToken: "fixture-status-key-4"}, Lifecycle{Status: StatusActive, Origin: OriginOAuth})
	status, _ = store.GetConnectionStatus(ctx, connection.ID)
	if status.TestStatus != StatusActive || status.AuthOrigin != OriginOAuth || status.LastError != "" || status.LastTestedAt != "" {
		t.Fatalf("accepted: %+v", status)
	}
	bad := connection
	bad.Secret, bad.Lifecycle = Secret{AccessToken: "fixture-status-key-5"}, Lifecycle{Status: StatusFailed}
	if _, err := store.PutConnection(ctx, bad); err == nil {
		t.Fatal("a save recorded a failed status")
	}
	if err := store.RecordValidation(ctx, "missing", 0, Validation{OK: true}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing connection: %v", err)
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

// A test reads the credential with its revision; a relink that lands before the
// test finishes makes the store refuse the old result, so the replacement keeps
// its own status. A metadata edit or a refresh rotation is the same credential.
func TestValidationIsRefusedForAReplacedCredential(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "claude")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthAPIKey, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "fixture-old"}})
	if err != nil {
		t.Fatal(err)
	}
	revision := func() int64 {
		t.Helper()
		stored, err := store.GetConnection(ctx, connection.ID)
		if err != nil {
			t.Fatal(err)
		}
		return stored.CredentialRevision
	}
	tested := revision()
	// Neither a metadata edit nor a refresh rotation replaces the credential.
	metadata := connection
	metadata.Secret, metadata.Name = Secret{}, "renamed"
	if _, err := store.PutConnection(ctx, metadata); err != nil {
		t.Fatal(err)
	}
	if err := store.RotateSecret(ctx, connection.ID, Secret{AccessToken: "fixture-old-rotated"}); err != nil {
		t.Fatal(err)
	}
	if got := revision(); got != tested {
		t.Fatalf("revision moved without a replacement: %d -> %d", tested, got)
	}
	// The relink.
	replaced := connection
	replaced.Secret, replaced.Lifecycle = Secret{AccessToken: "fixture-new"}, Lifecycle{Status: StatusUntested, Origin: OriginManual}
	if _, err := store.PutConnection(ctx, replaced); err != nil {
		t.Fatal(err)
	}
	for _, result := range []Validation{{OK: true, ModelsAvailable: 4}, {Error: "provider rejected credentials"}} {
		if err := store.RecordValidation(ctx, connection.ID, tested, result); !errors.Is(err, ErrCredentialChanged) {
			t.Fatalf("stale result %+v: %v", result, err)
		}
	}
	status, err := store.GetConnectionStatus(ctx, connection.ID)
	if err != nil || status.TestStatus != StatusUntested || status.LastTestedAt != "" || status.LastError != "" || status.ModelsAvailable != nil {
		t.Fatalf("replacement status overwritten: %+v err=%v", status, err)
	}
	if err := store.RecordValidation(ctx, connection.ID, revision(), Validation{OK: true}); err != nil {
		t.Fatalf("current revision: %v", err)
	}
}
