package credentials

import (
	"context"
	"database/sql"
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
	// A replacement naming no status (a soft-validated duplicate) still clears
	// the old credential's result, and its failure is not the new one's; a
	// save without a new secret never changes the lifecycle, whatever it names.
	save(Secret{AccessToken: "fixture-status-key-3"}, Lifecycle{Origin: OriginImport})
	save(Secret{}, Lifecycle{Status: StatusActive, Origin: OriginOAuth})
	status, _ = store.GetConnectionStatus(ctx, connection.ID)
	if status.TestStatus != StatusUntested || status.LastError != "" || status.LastTestedAt != "" || status.AuthOrigin != OriginImport {
		t.Fatalf("replaced without a status: %+v", status)
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
	// An API edit merges the stored secret back in, so it reseals the same
	// credential; provider settings that are not credentials may change with
	// it. Neither is a replacement, even when the save names a status.
	resealed := connection
	resealed.Name = "resealed"
	resealed.Secret = Secret{AccessToken: "fixture-old-rotated", ProviderData: map[string]string{"region": "eu", "projectId": "proj-2"}}
	resealed.Lifecycle = Lifecycle{Status: StatusUntested, Origin: OriginManual}
	if err := store.RecordValidation(ctx, connection.ID, tested, Validation{OK: true, ModelsAvailable: 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.PutConnection(ctx, resealed); err != nil {
		t.Fatal(err)
	}
	if got := revision(); got != tested {
		t.Fatalf("revision moved without a replacement: %d -> %d", tested, got)
	}
	if status, _ := store.GetConnectionStatus(ctx, connection.ID); status.TestStatus != StatusActive || status.ModelsAvailable == nil || status.AuthOrigin == OriginManual {
		t.Fatalf("a reseal reset the lifecycle: %+v", status)
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

// A provider that accepts the stored credential again (a reauthentication or
// validated reimport returning the same token) makes it active and clears its
// last error, as the reference's activation does (connectionsRepo.js:64-75);
// the last test it describes stays. Typing the same credential in again is not
// a replacement and leaves the lifecycle alone.
func TestSameCredentialAcceptedAgainIsActive(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "claude")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "fixture-same", RefreshToken: "fixture-same-r"}, Lifecycle: Lifecycle{Status: StatusActive, Origin: OriginOAuth}})
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := store.GetConnection(ctx, connection.ID)
	if err := store.RecordValidation(ctx, connection.ID, stored.CredentialRevision, Validation{OK: true, ModelsAvailable: 5}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordValidation(ctx, connection.ID, stored.CredentialRevision, Validation{Error: "provider rejected credentials"}); err != nil {
		t.Fatal(err)
	}
	same := connection
	same.Lifecycle = Lifecycle{Status: StatusUntested, Origin: OriginManual}
	if _, err := store.PutConnection(ctx, same); err != nil {
		t.Fatal(err)
	}
	if status, _ := store.GetConnectionStatus(ctx, connection.ID); status.TestStatus != StatusFailed || status.AuthOrigin != OriginOAuth {
		t.Fatalf("same credential typed again changed the lifecycle: %+v", status)
	}
	same.Lifecycle = Lifecycle{Status: StatusActive, Origin: OriginDevice}
	if _, err := store.PutConnection(ctx, same); err != nil {
		t.Fatal(err)
	}
	status, _ := store.GetConnectionStatus(ctx, connection.ID)
	if status.TestStatus != StatusActive || status.LastError != "" || status.LastErrorAt != "" || status.AuthOrigin != OriginDevice || status.LastTestedAt == "" || status.ModelsAvailable == nil {
		t.Fatalf("same credential accepted again: %+v", status)
	}
	if after, _ := store.GetConnection(ctx, connection.ID); after.CredentialRevision != stored.CredentialRevision {
		t.Fatalf("same credential counted as a replacement: %d -> %d", stored.CredentialRevision, after.CredentialRevision)
	}
}

// A connection stored before credential_digest existed has none. Its first
// save that reseals the same credential is not a replacement; its first real
// replacement still is.
func TestUpgradedRowWithoutDigest(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "claude")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "legacy", AuthKind: AuthAPIKey, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "fixture-legacy"}})
	if err != nil {
		t.Fatal(err)
	}
	upgrade := func() {
		t.Helper()
		if _, err := store.db.ExecContext(ctx, "UPDATE provider_connections SET credential_digest=NULL WHERE id=?", connection.ID); err != nil {
			t.Fatal(err)
		}
	}
	revision := func() int64 {
		t.Helper()
		stored, err := store.GetConnection(ctx, connection.ID)
		if err != nil {
			t.Fatal(err)
		}
		return stored.CredentialRevision
	}
	upgrade()
	before := revision()
	renamed := connection
	renamed.Name = "renamed"
	renamed.Lifecycle = Lifecycle{Status: StatusUntested, Origin: OriginManual}
	if _, err := store.PutConnection(ctx, renamed); err != nil {
		t.Fatal(err)
	}
	if got := revision(); got != before {
		t.Fatalf("reseal of an upgraded row counted as a replacement: %d -> %d", before, got)
	}
	var digest sql.NullString
	if err := store.db.QueryRowContext(ctx, "SELECT credential_digest FROM provider_connections WHERE id=?", connection.ID).Scan(&digest); err != nil || !digest.Valid {
		t.Fatalf("digest not initialized: %v %v", digest, err)
	}
	upgrade()
	replaced := connection
	replaced.Secret = Secret{AccessToken: "fixture-replacement"}
	if _, err := store.PutConnection(ctx, replaced); err != nil {
		t.Fatal(err)
	}
	if got := revision(); got != before+1 {
		t.Fatalf("replacement of an upgraded row: revision %d -> %d", before, got)
	}
}

// Every replacement clears the last test, error and model count, which describe
// the credential it replaced, even when the save names no status. A failed
// status is the old credential's and becomes untested; an active one is kept,
// as the reference keeps existing.testStatus (xiaomi-mimo/api-key/route.js:79).
func TestReplacementNamingNoStatusClearsTheOldResult(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "xiaomi-mimo")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "mimo", AuthKind: AuthAPIKey, Identity: "7@xiaomi", Enabled: true, Secret: Secret{AccessToken: "fixture-mimo-1"}, Lifecycle: Lifecycle{Status: StatusActive, Origin: OriginImport}})
	if err != nil {
		t.Fatal(err)
	}
	replace := func(key string) ConnectionStatus {
		t.Helper()
		update := connection
		update.Secret, update.Lifecycle = Secret{AccessToken: key}, Lifecycle{Origin: OriginImport}
		if _, err := store.PutConnection(ctx, update); err != nil {
			t.Fatal(err)
		}
		status, err := store.GetConnectionStatus(ctx, connection.ID)
		if err != nil {
			t.Fatal(err)
		}
		return status
	}
	record := func(v Validation) {
		t.Helper()
		stored, _ := store.GetConnection(ctx, connection.ID)
		if err := store.RecordValidation(ctx, connection.ID, stored.CredentialRevision, v); err != nil {
			t.Fatal(err)
		}
	}
	record(Validation{Error: "provider rejected credentials", ModelsAvailable: 4})
	before, _ := store.GetConnection(ctx, connection.ID)
	status := replace("fixture-mimo-2")
	if status.TestStatus != StatusUntested || status.LastError != "" || status.LastErrorAt != "" || status.LastTestedAt != "" || status.ModelsAvailable != nil {
		t.Fatalf("replacement of a failed credential: %+v", status)
	}
	if after, _ := store.GetConnection(ctx, connection.ID); after.CredentialRevision != before.CredentialRevision+1 {
		t.Fatalf("revision %d -> %d", before.CredentialRevision, after.CredentialRevision)
	}
	record(Validation{OK: true, ModelsAvailable: 6})
	status = replace("fixture-mimo-3")
	if status.TestStatus != StatusActive || status.LastTestedAt != "" || status.ModelsAvailable != nil {
		t.Fatalf("replacement of an active credential: %+v", status)
	}
	// Resealing the same credential is not a replacement and clears nothing.
	record(Validation{OK: true, ModelsAvailable: 2})
	record(Validation{Error: "provider rejected credentials"})
	status = replace("fixture-mimo-3")
	if status.TestStatus != StatusFailed || status.LastError == "" || status.ModelsAvailable == nil {
		t.Fatalf("reseal cleared the result: %+v", status)
	}
}

// A failed check keeps the last confirmed model count: the failed probe's
// listing is not a count of the models the credential can use.
func TestFailedCheckKeepsTheConfirmedModelCount(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node := seedNode(t, store, "claude")
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthAPIKey, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "fixture-count"}})
	if err != nil {
		t.Fatal(err)
	}
	stored, _ := store.GetConnection(ctx, connection.ID)
	if err := store.RecordValidation(ctx, connection.ID, stored.CredentialRevision, Validation{Error: "listing failed", ModelsAvailable: 0}); err != nil {
		t.Fatal(err)
	}
	if status, _ := store.GetConnectionStatus(ctx, connection.ID); status.TestStatus != StatusFailed || status.ModelsAvailable != nil {
		t.Fatalf("failure before any pass recorded a count: %+v", status)
	}
	if err := store.RecordValidation(ctx, connection.ID, stored.CredentialRevision, Validation{OK: true, ModelsAvailable: 7}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordValidation(ctx, connection.ID, stored.CredentialRevision, Validation{Error: "provider rejected credentials", ModelsAvailable: 0}); err != nil {
		t.Fatal(err)
	}
	status, _ := store.GetConnectionStatus(ctx, connection.ID)
	if status.TestStatus != StatusFailed || status.LastError != "provider rejected credentials" || status.ModelsAvailable == nil || *status.ModelsAvailable != 7 {
		t.Fatalf("failure overwrote the confirmed count: %+v", status)
	}
}
