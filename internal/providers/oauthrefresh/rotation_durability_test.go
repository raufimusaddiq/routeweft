package oauthrefresh

import (
	"context"
	"crypto/rand"
	"net/http"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

// rotatingUpstream answers a token refresh with a new access and refresh token and
// records which refresh token each call presented, so a test can tell a retry from
// the durable secret (old token) from one that continued off a half-rotated state
// (new token).
type rotatingUpstream struct {
	mu        sync.Mutex
	presented []string
	body      string
}

func (u *rotatingUpstream) Do(request *http.Request) (*http.Response, error) {
	raw := make([]byte, 4096)
	n, _ := request.Body.Read(raw)
	u.mu.Lock()
	u.presented = append(u.presented, string(raw[:n]))
	u.mu.Unlock()
	return (&fakeClient{body: u.body}).Do(request)
}

func (u *rotatingUpstream) calls() []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.presented...)
}

// rotationFixture is a real SQLite credential store with one expired Kiro social
// connection, wired to the real registry through the real refresh resolver.
type rotationFixture struct {
	t        *testing.T
	store    *credentials.Store
	registry *credentials.Registry
	upstream *rotatingUpstream
	id       string
	rawExec  func(string) error
}

func newRotationFixture(t *testing.T) *rotationFixture {
	t.Helper()
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := migrations.NewRunner(db.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	key := make([]byte, credentials.MasterKeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	sealer, err := credentials.NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	store, err := credentials.NewStore(db.DB(), sealer)
	if err != nil {
		t.Fatal(err)
	}
	node, err := store.PutNode(ctx, credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: "kiro", Name: "kiro"})
	if err != nil {
		t.Fatal(err)
	}
	put, err := store.PutConnection(ctx, credentials.Connection{
		NodeID: node.ID, Name: "primary", AuthKind: credentials.AuthOAuth, Identity: "acct", Enabled: true,
		Secret: credentials.Secret{
			AccessToken: "old-access", RefreshToken: "old-refresh", Expiry: time.Now().Add(-time.Minute),
			ProviderData: map[string]string{"authMethod": "google", "profileArn": "arn:aws:codewhisperer:us-east-1:000000000000:profile/FIXTURE"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	// PutConnection echoes the write; the loaded row is what carries the provider id
	// the refresh resolver dispatches on, as at startup.
	connection, err := store.GetConnection(ctx, put.ID)
	if err != nil {
		t.Fatal(err)
	}
	upstream := &rotatingUpstream{body: `{"accessToken":"new-access","refreshToken":"new-refresh","expiresIn":3600}`}
	resolve, err := Resolver(upstream)
	if err != nil {
		t.Fatal(err)
	}
	registry := credentials.NewRegistry(store, nil)
	registry.SetExchangerResolver(resolve)
	registry.Register(connection, nil)
	return &rotationFixture{
		t: t, store: store, registry: registry, upstream: upstream, id: connection.ID,
		rawExec: func(statement string) error { _, err := db.DB().ExecContext(ctx, statement); return err },
	}
}

func (f *rotationFixture) durable() credentials.Secret {
	f.t.Helper()
	connection, err := f.store.GetConnection(context.Background(), f.id)
	if err != nil {
		f.t.Fatal(err)
	}
	return connection.Secret
}

// TestRotatedCredentialIsDurableAndKeepsProviderData proves the success half of the
// invariant for a credential imported with no client registration: the rotated pair
// is in the database, not only in memory, and the connection fields the exchange did
// not mention survive it.
func TestRotatedCredentialIsDurableAndKeepsProviderData(t *testing.T) {
	f := newRotationFixture(t)
	token, err := f.registry.Credential(context.Background(), f.id)
	if err != nil || token != "new-access" {
		t.Fatalf("token=%q err=%v", token, err)
	}
	durable := f.durable()
	if durable.AccessToken != "new-access" || durable.RefreshToken != "new-refresh" {
		t.Fatalf("rotation reached memory but not storage: %+v", durable)
	}
	if durable.ProviderData["authMethod"] != "google" || durable.ProviderData["profileArn"] == "" {
		t.Fatalf("rotation dropped connection fields: %+v", durable.ProviderData)
	}
	if durable.Expiry.Before(time.Now().Add(30 * time.Minute)) {
		t.Fatalf("rotation stored no fresh expiry: %v", durable.Expiry)
	}
	if calls := f.upstream.calls(); len(calls) != 1 || !strings.Contains(calls[0], "old-refresh") {
		t.Fatalf("upstream calls=%v", calls)
	}
}

// TestFailedPersistLeavesNoHalfRotatedConnection is the incident this guards: the
// provider rotated the token, the write failed, and the connection ends up holding a
// credential that exists nowhere durable. After a failed persist neither storage nor
// the served credential may move, and the next attempt must start again from the
// durable secret.
func TestFailedPersistLeavesNoHalfRotatedConnection(t *testing.T) {
	f := newRotationFixture(t)
	ctx := context.Background()
	if err := f.rawExec(`CREATE TRIGGER fixture_refuse_rotation BEFORE UPDATE OF secret_blob ON provider_connections BEGIN SELECT RAISE(ABORT, 'fixture: disk full'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.registry.Credential(ctx, f.id); err == nil || !strings.Contains(err.Error(), "persist rotated credential") {
		t.Fatalf("a failed persist must fail the refresh, got %v", err)
	}
	if durable := f.durable(); durable.AccessToken != "old-access" || durable.RefreshToken != "old-refresh" {
		t.Fatalf("storage changed although the write failed: %+v", durable)
	}
	// Nothing was published: asking again must go back to the provider with the
	// durable refresh token rather than serve, or continue from, the unsaved pair.
	if _, err := f.registry.Credential(ctx, f.id); err == nil {
		t.Fatal("the unsaved rotation was served after the failed persist")
	}
	for index, call := range f.upstream.calls() {
		if !strings.Contains(call, "old-refresh") || strings.Contains(call, "new-refresh") {
			t.Fatalf("attempt %d continued from an unsaved rotation: %s", index, call)
		}
	}

	if err := f.rawExec(`DROP TRIGGER fixture_refuse_rotation`); err != nil {
		t.Fatal(err)
	}
	token, err := f.registry.Credential(ctx, f.id)
	if err != nil || token != "new-access" {
		t.Fatalf("recovery token=%q err=%v", token, err)
	}
	if durable := f.durable(); durable.AccessToken != "new-access" || durable.RefreshToken != "new-refresh" {
		t.Fatalf("recovery did not persist: %+v", durable)
	}
}
