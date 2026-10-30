package app

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/quota"
	"github.com/raufimusaddiq/routeweft/internal/store/storetest"
)

type quotaFixture struct{ reset time.Time }

func (f quotaFixture) Usage(context.Context, quota.Credential) (quota.State, error) {
	remaining := 1.0
	return quota.State{Windows: []quota.Window{{Name: "session (5h)", Remaining: &remaining, ResetAt: &f.reset}}}, nil
}

func TestQuotaAutoPingProductionPersistence(t *testing.T) {
	ctx := context.Background()
	key := make([]byte, 32)
	app := New(Config{Listen: ":0", DataDir: storetest.DataDir(t), CredentialKey: key}, nil)
	if err := app.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	defer app.store.Close()
	if app.autoPing == nil {
		t.Fatal("missing auto-ping worker")
	}
	node, err := app.creds.PutNode(ctx, credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: "claude", Name: "Claude", Transports: []string{"anthropic-messages"}})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := app.creds.PutConnection(ctx, credentials.Connection{NodeID: node.ID, Name: "fixture", Identity: "fixture", Enabled: true, AuthKind: credentials.AuthOAuth, Secret: credentials.Secret{AccessToken: "fixture"}})
	if err != nil {
		t.Fatal(err)
	}
	app.quota.Observer.Clients["claude"] = quotaFixture{reset: time.Now().Add(-time.Second)}
	calls := 0
	app.autoPing.Send = func(context.Context, string, quota.Credential) error { calls++; return nil }
	if err := app.autoPing.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("warming defaults on")
	}
	settings, _ := json.Marshal(map[string]any{"connections": map[string]bool{conn.ID: true}})
	if _, err := app.runtime.SetSettings(ctx, map[string]string{"claudeAutoPing": string(settings)}, nil); err != nil {
		t.Fatal(err)
	}
	if err := app.autoPing.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("calls=%d", calls)
	}
	var stored string
	if err := app.store.DB().QueryRow("SELECT value FROM settings WHERE key=?", quota.PingHistorySetting).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var history map[string]quota.PingRecord
	if json.Unmarshal([]byte(stored), &history) != nil || history[conn.ID].Reset == "" {
		t.Fatalf("history=%s", stored)
	}
	restarted := &quota.AutoPinger{Service: app.quota, Settings: app.runtime.Settings, Save: app.autoPing.Save, Send: app.autoPing.Send}
	if err := restarted.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatal("persistent history did not suppress restart")
	}
}
