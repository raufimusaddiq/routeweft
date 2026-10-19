package runtime

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

func TestProbeModelPreservesNativeIDsAndRejectsDisabledAliases(t *testing.T) {
	models, aliases, disabled := compileCatalog([]Model{
		{ProviderID: "openai", ID: "gpt-5.4"},
		{ProviderID: "openrouter", ID: "openai/gpt-5.4"},
		{ProviderID: "openrouter", ID: "raw-name"},
		{ProviderID: "openrouter", ID: "real"},
		{ProviderID: "openrouter", ID: "paused-target"},
		{ProviderID: "meta-llama", ID: "Llama-3-8B"},
	}, map[string]ModelRef{
		"friendly": {ProviderID: "openrouter", ModelID: "real"},
		"paused":   {ProviderID: "openrouter", ModelID: "paused-target"},
		"outside":  {ProviderID: "openai", ModelID: "gpt-5.4"},
		"raw-name": {ProviderID: "openai", ModelID: "gpt-5.4"},
	}, map[string]struct{}{modelKey("openrouter", "paused-target"): {}})
	s := &RuntimeSnapshot{models: models, aliases: aliases, disabledModels: disabled}
	for _, tc := range []struct{ requested, want, failure string }{
		{"openai/gpt-5.4", "openai/gpt-5.4", ""},
		{"meta-llama/Llama-3-8B", "meta-llama/Llama-3-8B", ""},
		{"raw-name", "raw-name", ""},
		{"unknown-native", "unknown-native", ""},
		{"gpt-5.4", "gpt-5.4", ""},
		{"openrouter/real", "real", ""},
		{"friendly", "real", ""},
		{"openrouter/friendly", "real", ""},
		{"outside", "", "different provider"},
		{"paused", "", "disabled"},
		{"openrouter/paused", "", "disabled"},
		{"paused-target", "", "disabled"},
	} {
		t.Run(tc.requested, func(t *testing.T) {
			got, err := s.ProbeModel("openrouter", tc.requested)
			if got != tc.want || tc.failure == "" && err != nil || tc.failure != "" && (err == nil || !strings.Contains(err.Error(), tc.failure)) {
				t.Fatalf("model=%q error=%v", got, err)
			}
		})
	}
}

func TestSystemOneSeedsAndCloudflareAccountEndpoints(t *testing.T) {
	m, s := providerStore(t)
	ctx := context.Background()
	for _, provider := range []string{"typesafe", "cloudflare-ai"} {
		node, err := s.PutNode(ctx, credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: provider, Name: provider})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.PutConnection(ctx, credentials.Connection{ID: provider, NodeID: node.ID, Name: provider, Identity: provider, AuthKind: credentials.AuthNone, Enabled: true, AccountID: "account-a"}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, _ := m.Load()
	for _, tc := range []struct{ model, account, source, endpoint string }{
		{"typesafe/jev-latest", "typesafe", "systemone", ""},
		{"cloudflare-ai/@cf/cloudflare/clef", "cloudflare-ai", "systemone", "account-a/ai/run/@cf/cloudflare/clef"},
		{"cloudflare-ai/@cf/cloudflare/clef-flash", "cloudflare-ai", "systemone", "account-a/ai/run/@cf/cloudflare/clef-flash"},
	} {
		got, ok := snapshot.AccountProvider(tc.model, tc.account, tc.source, nil)
		if !ok || got.Protocol != tc.source || got.Endpoint != tc.endpoint {
			t.Fatalf("provider=%+v ok=%v expected=%+v", got, ok, tc)
		}
	}
	if _, ok := snapshot.AccountProvider("cloudflare-ai/@cf/cloudflare/clef", "cloudflare-ai", "openai-chat", nil); ok {
		t.Fatal("typed model selected as chat")
	}
	connection, err := s.GetConnection(ctx, "cloudflare-ai")
	if err != nil {
		t.Fatal(err)
	}
	connection.AccountID = "account-b"
	if _, err := s.PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	current, _ := m.Load()
	old, _ := snapshot.AccountProvider("cloudflare-ai/@cf/cloudflare/clef", "cloudflare-ai", "systemone", nil)
	updated, _ := current.AccountProvider("cloudflare-ai/@cf/cloudflare/clef", "cloudflare-ai", "systemone", nil)
	if old.Endpoint != "account-a/ai/run/@cf/cloudflare/clef" || updated.Endpoint != "account-b/ai/run/@cf/cloudflare/clef" {
		t.Fatalf("snapshot account metadata mutated: %s / %s", old.Endpoint, updated.Endpoint)
	}
	connection.AccountID = ""
	if _, err := s.PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	current, _ = m.Load()
	if _, ok := current.AccountProvider("cloudflare-ai/@cf/cloudflare/clef", "cloudflare-ai", "systemone", nil); ok {
		t.Fatal("unconfigured Cloudflare account selected")
	}
}

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

func TestProviderSeedsRemainEphemeralAcrossWritesAndRestart(t *testing.T) {
	m, s := providerStore(t)
	ctx := context.Background()
	if _, err := s.PutNode(ctx, credentials.Node{ID: "n", Kind: credentials.NodeBuiltin, ProviderID: "openai", Name: "OpenAI"}); err != nil {
		t.Fatal(err)
	}
	if _, err := m.SetSettings(ctx, map[string]string{"providerStrategy": "round-robin"}, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := m.RefreshPoolBindings(ctx); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := m.db.QueryRowContext(ctx, "SELECT count(*) FROM provider_models").Scan(&rows); err != nil || rows != 0 {
		t.Fatalf("persisted seeds=%d err=%v", rows, err)
	}
	restarted, err := NewManager(ctx, m.db)
	if err != nil {
		t.Fatal(err)
	}
	for _, manager := range []*Manager{m, restarted} {
		snapshot, _ := manager.Load()
		if model, ok := snapshot.ServingModel("openai/gpt-4o"); !ok || model.Source != "seed" {
			t.Fatalf("seed model=%+v ok=%v", model, ok)
		}
	}
	if err := m.PutModel(ctx, Model{ProviderID: "openai", ID: "gpt-4o", Name: "Explicit override"}); err != nil {
		t.Fatal(err)
	}
	restarted, err = NewManager(ctx, m.db)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := restarted.Load()
	if model, ok := snapshot.ServingModel("openai/gpt-4o"); !ok || model.Source != "custom" || model.Name != "Explicit override" {
		t.Fatalf("custom override=%+v ok=%v", model, ok)
	}
}

func TestProviderMissingDurableRowAbortsPublication(t *testing.T) {
	for _, operation := range []string{"enable", "priority", "proxy", "delete", "move", "node-delete"} {
		t.Run(operation, func(t *testing.T) {
			m, s := providerStore(t)
			ctx := context.Background()
			if _, err := s.PutNode(ctx, credentials.Node{ID: "n", Kind: credentials.NodeGeneric, ProviderID: "test", Name: "Test", BaseURL: "https://example.com", Transports: []string{"openai-chat"}}); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if _, err := s.PutConnection(ctx, credentials.Connection{ID: fmt.Sprint(i), NodeID: "n", Name: fmt.Sprint(i), Identity: fmt.Sprint(i), AuthKind: credentials.AuthNone, Enabled: true, Priority: i}); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := m.Load()
			query := "DELETE FROM provider_connections WHERE id='0'"
			if operation == "node-delete" {
				query = "DELETE FROM provider_nodes WHERE id='n'"
			}
			if _, err := m.db.ExecContext(ctx, query); err != nil {
				t.Fatal(err)
			}
			var err error
			switch operation {
			case "enable":
				err = s.SetConnectionEnabled(ctx, "0", false)
			case "priority":
				err = s.SetConnectionPriority(ctx, "0", 3)
			case "proxy":
				err = s.SetConnectionProxy(ctx, "0", "")
			case "delete":
				err = s.DeleteConnection(ctx, "0")
			case "move":
				err = s.MoveConnection(ctx, "1", -1)
			case "node-delete":
				err = s.DeleteGenericNode(ctx, "n")
			}
			if !errors.Is(err, credentials.ErrNotFound) {
				t.Fatalf("missing durable row: %v", err)
			}
			after, _ := m.Load()
			if after != before {
				t.Fatal("failed write published snapshot/revision")
			}
			var revision uint64
			if err := m.db.QueryRowContext(ctx, "SELECT value FROM meta WHERE key='config_revision'").Scan(&revision); err != nil || revision != before.ConfigRevision() {
				t.Fatalf("durable revision=%d err=%v", revision, err)
			}
			if operation == "move" {
				var priority int
				if err := m.db.QueryRowContext(ctx, "SELECT priority FROM provider_connections WHERE id='1'").Scan(&priority); err != nil || priority != 1 {
					t.Fatalf("partial reorder not rolled back: priority=%d err=%v", priority, err)
				}
			}
		})
	}
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
	ref, ok := before.AccountProvider("openai/gpt-4o", "c", "openai-responses", nil)
	if !ok || ref.BaseURL != "https://api.openai.com/v1" || ref.Protocol != "openai-responses" || ref.APIToken != "" {
		t.Fatalf("compiled ref %+v ok=%v", ref, ok)
	}
	if !before.providerConfig.Connections[0].Secret.Empty() {
		t.Fatal("snapshot retained credential material")
	}
	if _, ok := before.AccountProvider("openai/gpt-4o", "c", "anthropic-messages", nil); ok {
		t.Fatal("unsupported native transport is eligible")
	}
	ref, ok = before.AccountProvider("openai/gpt-4o", "c", "external-protocol", func(source, target string) bool {
		return source == "external-protocol" && target == "openai-responses"
	})
	if !ok || ref.Protocol != "openai-responses" {
		t.Fatal("runtime did not use the caller's protocol policy")
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
	if _, ok := after.AccountProvider("openai/gpt-4o", "c", "openai-chat", nil); ok {
		t.Fatal("disabled connection remains eligible")
	}
	if _, ok := before.AccountProvider("openai/gpt-4o", "c", "openai-chat", nil); !ok {
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
					ref, found := view.AccountProvider("test/model", item.ID, "openai-chat", nil)
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

// A snapshot must hand the dispatcher the per-transport endpoint of a provider
// whose upstream path differs from the shared default, so base + endpoint is the
// URL the oracle's registry declares (internal/parity TestEveryOracleTransportURLMatches).
func TestSnapshotCarriesTransportEndpointsForPathDriftProviders(t *testing.T) {
	m, s := providerStore(t)
	ctx := context.Background()
	cases := []struct{ provider, model, source, endpoint string }{
		{"kilocode", "anthropic/claude-sonnet-4-20250514", "openai-chat", "openrouter/chat/completions"},
		{"cline", "anthropic/claude-opus-4.7", "openai-chat", "api/v1/chat/completions"},
		{"clinepass", "cline-pass/glm-5.2", "openai-chat", "api/v1/chat/completions"},
		{"mimo-free", "mimo-auto", "openai-chat", "api/free-ai/openai/chat"},
		{"mmf", "mimo-auto", "openai-chat", "api/free-ai/openai/chat"},
		{"deepseek", "deepseek-chat", "anthropic-messages", "anthropic/v1/messages"},
		{"deepseek", "deepseek-chat", "openai-chat", ""},
	}
	seen := map[string]bool{}
	for _, tc := range cases {
		if err := m.PutModel(ctx, Model{ProviderID: tc.provider, ID: tc.model, Kind: "llm"}); err != nil {
			t.Fatal(err)
		}
		if seen[tc.provider] {
			continue
		}
		seen[tc.provider] = true
		node, err := s.PutNode(ctx, credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: tc.provider, Name: tc.provider})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.PutConnection(ctx, credentials.Connection{ID: tc.provider, NodeID: node.ID, Name: tc.provider, Identity: tc.provider, AuthKind: credentials.AuthNone, Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, _ := m.Load()
	for _, tc := range cases {
		got, ok := snapshot.AccountProvider(tc.provider+"/"+tc.model, tc.provider, tc.source, nil)
		if !ok || got.Protocol != tc.source || got.Endpoint != tc.endpoint {
			t.Fatalf("%s %s: provider=%+v ok=%v, want endpoint %q", tc.provider, tc.source, got, ok, tc.endpoint)
		}
	}
}
