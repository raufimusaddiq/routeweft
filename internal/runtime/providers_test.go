package runtime

import (
	"context"
	"database/sql"
	"fmt"
	"sync"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

func providerStore(t *testing.T) (*Manager, *credentials.Store) {
	t.Helper()
	m, db := newTestManager(t)
	t.Cleanup(func() { db.Close() })
	sealer, err := credentials.NewSealer([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := credentials.NewStore(db.DB(), sealer)
	if err != nil {
		t.Fatal(err)
	}
	s.SetPublisher(m.UpdateProviders)
	return m, s
}

func TestProviderSnapshotCompileCommitPublish(t *testing.T) {
	m, s := providerStore(t)
	ctx := context.Background()
	if _, err := s.PutNode(ctx, credentials.Node{ID: "n", Kind: credentials.NodeBuiltin, ProviderID: "openai", Name: "OpenAI"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutConnection(ctx, credentials.Connection{ID: "c", NodeID: "n", Name: "First", Identity: "synthetic", AuthKind: credentials.AuthAPIKey, Enabled: true, Secret: credentials.Secret{AccessToken: "synthetic-secret"}}); err != nil {
		t.Fatal(err)
	}
	before, _ := m.Load()
	ref, ok := before.AccountProvider("openai/gpt-4o", "c", "openai-responses")
	if !ok || ref.BaseURL != "https://api.openai.com/v1" || ref.Protocol != "openai-responses" || ref.APIToken != "" {
		t.Fatalf("compiled ref %+v ok=%v", ref, ok)
	}
	if !before.providerConfig.Connections[0].Secret.Empty() {
		t.Fatal("snapshot retained credential material")
	}
	if _, ok := before.AccountProvider("openai/gpt-4o", "c", "anthropic-messages"); ok {
		t.Fatal("unsupported native transport is eligible")
	}
	if _, err := m.db.ExecContext(ctx, `CREATE TRIGGER fail_connection BEFORE UPDATE ON provider_connections BEGIN SELECT RAISE(ABORT,'synthetic failure'); END`); err != nil {
		t.Fatal(err)
	}
	if err := s.SetConnectionEnabled(ctx, "c", false); err == nil {
		t.Fatal("failed write accepted")
	}
	after, _ := m.Load()
	if after != before {
		t.Fatal("failed commit published configuration")
	}
	connection, err := s.GetConnection(ctx, "c")
	if err != nil || !connection.Enabled || connection.Secret.AccessToken != "synthetic-secret" {
		t.Fatal("failed commit changed durable connection")
	}
	if _, err := m.db.ExecContext(ctx, "DROP TRIGGER fail_connection"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetConnectionEnabled(ctx, "c", false); err != nil {
		t.Fatal(err)
	}
	after, _ = m.Load()
	if _, ok := after.AccountProvider("openai/gpt-4o", "c", "openai-chat"); ok {
		t.Fatal("disabled connection remains eligible")
	}
	if _, ok := before.AccountProvider("openai/gpt-4o", "c", "openai-chat"); !ok {
		t.Fatal("old snapshot mutated")
	}
	called := false
	err = m.UpdateProviders(ctx, func(config *credentials.Configuration) error {
		config.Connections = append(config.Connections, credentials.Connection{ID: "bad", NodeID: "missing"})
		return nil
	}, func(_ *sql.Tx) error { called = true; return nil })
	if err == nil || called {
		t.Fatal("invalid candidate reached persistence")
	}
}

func TestProviderMetadataUpdatesAndConcurrentSnapshots(t *testing.T) {
	m, s := providerStore(t)
	ctx := context.Background()
	_, err := s.PutNode(ctx, credentials.Node{ID: "n", Kind: credentials.NodeGeneric, ProviderID: "test", Name: "Test", BaseURL: "https://example.com/v1", Transports: []string{"openai-chat"}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		_, err = s.PutConnection(ctx, credentials.Connection{ID: fmt.Sprint(i), NodeID: "n", Name: fmt.Sprint(i), Identity: fmt.Sprint(i), AuthKind: credentials.AuthNone, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := m.PutModel(ctx, Model{ProviderID: "test", ID: "model"}); err != nil {
		t.Fatal(err)
	}
	if err := s.MoveConnection(ctx, "1", -1); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := m.Load()
	accounts, ok := snapshot.ProviderCandidates("test/model")
	if !ok || len(accounts) != 2 || accounts[0].ID != "1" {
		t.Fatalf("order %+v", accounts)
	}
	accounts[0].Enabled = false
	accounts, _ = snapshot.ProviderCandidates("test/model")
	if !accounts[0].Enabled {
		t.Fatal("caller mutated snapshot accounts")
	}
	for _, move := range []struct {
		id        string
		direction int
	}{{"1", -1}, {"0", 1}} {
		if err := s.MoveConnection(ctx, move.id, move.direction); err != nil {
			t.Fatal(err)
		}
		after, _ := m.Load()
		if after != snapshot {
			t.Fatal("boundary move published a new snapshot/revision")
		}
	}
	var group sync.WaitGroup
	for i := 0; i < 4; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			for j := 0; j < 100; j++ {
				view, _ := m.Load()
				items, ok := view.ProviderCandidates("test/model")
				if !ok || len(items) != 2 {
					t.Error("incoherent candidates")
				}
				for _, item := range items {
					ref, found := view.AccountProvider("test/model", item.ID, "openai-chat")
					if item.Enabled && (!found || ref.BaseURL != "https://example.com/v1") {
						t.Error("incoherent provider")
					}
				}
			}
		}()
	}
	for i := 0; i < 12; i++ {
		if err := s.SetConnectionEnabled(ctx, "0", i%2 == 0); err != nil {
			t.Fatal(err)
		}
	}
	group.Wait()
	if err := s.DeleteGenericNode(ctx, "n"); err != nil {
		t.Fatal(err)
	}
	view, _ := m.Load()
	items, _ := view.ProviderCandidates("test/model")
	if len(items) != 0 {
		t.Fatal("deleted node left accounts active")
	}
}

func TestProviderMutationDoesNotResurrectDeletedProxyBinding(t *testing.T) {
	m, s := providerStore(t)
	ctx := context.Background()
	if _, err := m.db.ExecContext(ctx, `INSERT INTO proxy_pools(id,name) VALUES('p','test')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutNode(ctx, credentials.Node{ID: "n", Kind: credentials.NodeBuiltin, ProviderID: "openai", Name: "OpenAI"}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutConnection(ctx, credentials.Connection{ID: "c", NodeID: "n", Name: "First", Identity: "synthetic", AuthKind: credentials.AuthNone, Enabled: true, ProxyPoolID: "p"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.db.ExecContext(ctx, "DELETE FROM proxy_pools WHERE id='p'"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RefreshPoolBindings(ctx); err != nil {
		t.Fatal(err)
	}
	if err := s.SetConnectionPriority(ctx, "c", 1); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := m.Load()
	if snapshot.ConnectionPool("c") != "" || snapshot.providerConfig.Connections[0].ProxyPoolID != "" {
		t.Fatal("provider update resurrected deleted proxy binding")
	}
}
