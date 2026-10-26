package migrations

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

func TestFreshMigrationAndRestart(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := NewRunner(store.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := NewRunner(store.DB()).Apply(ctx); err != nil {
		t.Fatalf("migration not idempotent: %v", err)
	}
	version, err := NewRunner(store.DB()).Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != LatestVersion() {
		t.Fatalf("version %d, want %d", version, LatestVersion())
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := sqlite.Open(ctx, store.Path())
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if err := NewRunner(reopened.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	required := []string{"meta", "settings", "provider_nodes", "provider_connections", "combos", "usage_events", "request_details", "provider_models"}
	for _, name := range required {
		var count int
		if err := reopened.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name=?", name).Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("table %q missing after restart", name)
		}
	}
}

func TestV7ModelKindUpgrade(t *testing.T) {
	ctx := context.Background()
	s, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := NewRunner(s.DB(), v1, v2, v3, v4, v5, v6).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO provider_models(provider_id,model_id,capabilities) VALUES('provider','model','["vision"]')`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO api_keys(id,name,key_hash,key_prefix,access_restricted,access_allow) VALUES('k1','restricted','synthetic-hash','rw_test',1,'["provider/model"]')`); err != nil {
		t.Fatal(err)
	}
	if err := NewRunner(s.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	var kind, caps string
	if err := s.DB().QueryRowContext(ctx, "SELECT kind,capabilities FROM provider_models").Scan(&kind, &caps); err != nil || kind != "" || caps != `["vision"]` {
		t.Fatalf("upgrade metadata: %q %q %v", kind, caps, err)
	}
	if _, err := s.DB().ExecContext(ctx, "UPDATE provider_models SET kind='image'"); err == nil {
		t.Fatal("excluded media kind accepted")
	}
	var restricted bool
	var allow string
	if err := s.DB().QueryRowContext(ctx, "SELECT access_restricted,access_allow FROM api_keys WHERE id='k1'").Scan(&restricted, &allow); err != nil || !restricted || allow != `["provider/model"]` {
		t.Fatalf("v7 changed v6 key policy: %v %q %v", restricted, allow, err)
	}
}

func TestV1DatabaseUpgradesWithoutLosingCatalogRecords(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := NewRunner(store.DB(), v1).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := store.DB().ExecContext(ctx, `INSERT INTO settings(key,value) VALUES('requireApiKey','true');
INSERT INTO api_keys(id,name,key_hash,key_prefix) VALUES('k1','operator key','hash-only','rw_demo');
INSERT INTO custom_models(id,provider_id,model_id,display_name) VALUES('m1','provider','model','Model');`); err != nil {
		t.Fatal(err)
	}
	if err := NewRunner(store.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	version, err := NewRunner(store.DB()).Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != LatestVersion() {
		t.Fatalf("version %d, want %d", version, LatestVersion())
	}
	var keyName, modelName string
	if err := store.DB().QueryRowContext(ctx, "SELECT name FROM api_keys WHERE id='k1'").Scan(&keyName); err != nil {
		t.Fatal(err)
	}
	if err := store.DB().QueryRowContext(ctx, "SELECT display_name FROM custom_models WHERE id='m1'").Scan(&modelName); err != nil {
		t.Fatal(err)
	}
	if keyName != "operator key" || modelName != "Model" {
		t.Fatalf("upgraded records key=%q model=%q", keyName, modelName)
	}
}

func TestV4ConnectionAccountIDUpgradePreservesRecords(t *testing.T) {
	ctx := context.Background()
	s, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := NewRunner(s.DB(), v1, v2, v3, v4).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO provider_nodes(id,kind,provider_id,name) VALUES('n','builtin','typesafe','TypeSafe');
INSERT INTO provider_connections(id,node_id,name,auth_kind,identity,secret_blob) VALUES('c','n','Account','api-key','account','sealed-fixture');`); err != nil {
		t.Fatal(err)
	}
	if err := NewRunner(s.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	var account, blob string
	if err := s.DB().QueryRowContext(ctx, "SELECT account_id,secret_blob FROM provider_connections WHERE id='c'").Scan(&account, &blob); err != nil || account != "" || blob != "sealed-fixture" {
		t.Fatalf("upgrade changed existing connection: account=%q blob=%q err=%v", account, blob, err)
	}
}

func TestMigrationFailureRollsBackSchemaAndVersion(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	broken := NewRunner(store.DB(), Migration{Version: 1, Name: "bad", SQL: "CREATE TABLE partial (id INTEGER); INVALID SQL"})
	if err := broken.Apply(ctx); err == nil {
		t.Fatal("expected migration failure")
	}
	var count int
	if err := store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='partial'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("failed migration left partial schema")
	}
	version, err := broken.Version(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if version != 0 {
		t.Fatalf("failed migration recorded version %d", version)
	}
}

func TestMigrationRunnerRejectsMissingVersion(t *testing.T) {
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	runner := NewRunner(store.DB(), Migration{Version: 2, Name: "gap", SQL: "CREATE TABLE should_not_exist (id INTEGER)"})
	if err := runner.Apply(context.Background()); err == nil {
		t.Fatal("expected non-contiguous migration list to fail")
	}
	var count int
	if err := store.DB().QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='should_not_exist'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("invalid migration list changed schema")
	}
}

func TestVersionTableCheckDoesNotCreateTable(t *testing.T) {
	store, err := sqlite.Open(context.Background(), filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	exists, err := NewRunner(store.DB()).HasVersionTable(context.Background())
	if err != nil || exists {
		t.Fatalf("exists=%v err=%v, want false without error", exists, err)
	}
	var count int
	if err := store.DB().QueryRow("SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatal("read-only version check created the migration table")
	}
}

func TestV6KeyAccessUpgradeKeepsExistingKeysUnrestricted(t *testing.T) {
	ctx := context.Background()
	s, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := NewRunner(s.DB(), v1, v2, v3, v4, v5).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO api_keys(id,name,key_hash,key_prefix) VALUES('k1','existing','h','rw_x')`); err != nil {
		t.Fatal(err)
	}
	if err := NewRunner(s.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	var restricted int
	var allow string
	if err := s.DB().QueryRowContext(ctx, `SELECT access_restricted,access_allow FROM api_keys WHERE id='k1'`).Scan(&restricted, &allow); err != nil {
		t.Fatal(err)
	}
	if restricted != 0 || allow != "[]" {
		t.Fatalf("existing key became restricted=%d allow=%s; upgrade must not narrow access", restricted, allow)
	}
}

func TestV8MimoFreeBaseURLUpgrade(t *testing.T) {
	ctx := context.Background()
	s, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := NewRunner(s.DB(), v1, v2, v3, v4, v5, v6, v7).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	const old = "https://api.xiaomimimo.com/api/free-ai/openai/chat"
	for _, row := range [][3]string{
		{"n1", "mimo-free", old},
		{"n2", "mmf", old},
		{"n3", "mmf", "https://proxy.example.test/mimo"},
		{"n4", "openai", old},
	} {
		if _, err := s.DB().ExecContext(ctx, `INSERT INTO provider_nodes(id,kind,provider_id,name,base_url) VALUES(?, 'builtin', ?, ?, ?)`, row[0], row[1], row[0], row[2]); err != nil {
			t.Fatal(err)
		}
	}
	if err := NewRunner(s.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"n1": "https://api.xiaomimimo.com", "n2": "https://api.xiaomimimo.com", "n3": "https://proxy.example.test/mimo", "n4": old}
	for id, base := range want {
		var got string
		if err := s.DB().QueryRowContext(ctx, "SELECT base_url FROM provider_nodes WHERE id=?", id).Scan(&got); err != nil || got != base {
			t.Fatalf("node %s base_url %q, want %q (%v)", id, got, base, err)
		}
	}
}

// v11 keeps every stored connection, including one whose identity is a ChatGPT
// account id from the pre-#239 Codex fallback, and then lets identity be absent:
// absent identities never collide, present ones stay unique per node.
func TestV11KeepsConnectionsAndMakesIdentityOptional(t *testing.T) {
	ctx := context.Background()
	s, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := NewRunner(s.DB(), v1, v2, v3, v4, v5, v6, v7, v8, v9, v10).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := s.DB().ExecContext(ctx, `INSERT INTO proxy_pools(id,name) VALUES('pool-1','pool');
INSERT INTO provider_nodes(id,kind,provider_id,name) VALUES('node-codex','builtin','codex','Codex');
INSERT INTO provider_connections(id,node_id,name,auth_kind,identity,secret_blob,enabled,priority,proxy_pool_id,created_at,updated_at,account_id)
VALUES('conn-legacy','node-codex','legacy','oauth','acct-legacy','sealed-blob',0,3,'pool-1','2026-01-01T00:00:00.000Z','2026-01-02T00:00:00.000Z','acct-header');`); err != nil {
		t.Fatal(err)
	}
	if err := NewRunner(s.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	var name, kind, identity, blob, pool, created, updated, account string
	var enabled, priority int
	if err := s.DB().QueryRowContext(ctx, `SELECT name,auth_kind,identity,secret_blob,enabled,priority,proxy_pool_id,created_at,updated_at,account_id FROM provider_connections WHERE id='conn-legacy'`).
		Scan(&name, &kind, &identity, &blob, &enabled, &priority, &pool, &created, &updated, &account); err != nil {
		t.Fatal(err)
	}
	if name != "legacy" || kind != "oauth" || identity != "acct-legacy" || blob != "sealed-blob" || enabled != 0 || priority != 3 || pool != "pool-1" ||
		created != "2026-01-01T00:00:00.000Z" || updated != "2026-01-02T00:00:00.000Z" || account != "acct-header" {
		t.Fatalf("v11 rewrote the stored connection: %q %q %q %q %d %d %q %q %q %q", name, kind, identity, blob, enabled, priority, pool, created, updated, account)
	}
	insert := `INSERT INTO provider_connections(id,node_id,name,auth_kind,identity) VALUES(?,?,?,?,?)`
	for _, id := range []string{"conn-a", "conn-b"} {
		if _, err := s.DB().ExecContext(ctx, insert, id, "node-codex", "Account", "oauth", nil); err != nil {
			t.Fatalf("connection without identity refused: %v", err)
		}
	}
	if _, err := s.DB().ExecContext(ctx, insert, "conn-dup", "node-codex", "dup", "oauth", "acct-legacy"); err == nil {
		t.Fatal("a duplicate identity on the same node was accepted")
	}
	if _, err := s.DB().ExecContext(ctx, insert, "conn-blank", "node-codex", "blank", "oauth", ""); err == nil {
		t.Fatal("an empty identity was accepted alongside NULL")
	}
	if _, err := s.DB().ExecContext(ctx, `DELETE FROM provider_nodes WHERE id='node-codex'`); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := s.DB().QueryRowContext(ctx, `SELECT COUNT(*) FROM provider_connections`).Scan(&left); err != nil || left != 0 {
		t.Fatalf("node delete no longer cascades: %d %v", left, err)
	}
}
