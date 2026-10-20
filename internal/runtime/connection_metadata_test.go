package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

func TestProviderSettingsPublishRotateAndRestore(t *testing.T) {
	ctx := context.Background()
	m, s := providerStore(t)
	sealer, err := credentials.NewSealer([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		provider, model string
		data            map[string]string
		want            routing.ProviderSettings
	}{
		{"azure", "gpt-4", map[string]string{"azureEndpoint": "https://fixture.openai.azure.com", "apiVersion": "2024-10-01-preview", "deployment": "fixture-deploy", "organization": "fixture-org"}, routing.ProviderSettings{AzureEndpoint: "https://fixture.openai.azure.com", APIVersion: "2024-10-01-preview", Deployment: "fixture-deploy", Organization: "fixture-org"}},
		{"xiaomi-tokenplan", "fixture-model", map[string]string{"region": "cn"}, routing.ProviderSettings{Region: "cn"}},
		{"grok-cli", "grok-4", map[string]string{"workspaceId": "fixture-workspace", "deviceId": "fixture-device", "agentId": "fixture-agent", "email": "fixture@example.invalid", "userId": "fixture-user"}, routing.ProviderSettings{WorkspaceID: "fixture-workspace", DeviceID: "fixture-device", AgentID: "fixture-agent", Email: "fixture@example.invalid", UserID: "fixture-user"}},
		{"zed", "fixture-model", map[string]string{"userId": "fixture-user", "systemId": "fixture-system", "organizationId": "fixture-org", "defaultOrganizationId": "fixture-default"}, routing.ProviderSettings{UserID: "fixture-user", SystemID: "fixture-system", OrganizationID: "fixture-org", DefaultOrganizationID: "fixture-default"}},
	}
	for _, tc := range cases {
		node, err := s.PutNode(ctx, credentials.Node{ID: tc.provider, Kind: credentials.NodeBuiltin, ProviderID: tc.provider, Name: tc.provider, BaseURL: "https://fixture.invalid", Transports: []string{"openai-chat"}})
		if err != nil {
			t.Fatal(err)
		}
		tc.data["clientSecret"] = "fixture-client-secret"
		tc.data["copilotToken"] = "fixture-copilot"
		tc.data["unknownFutureSecret"] = "fixture-unknown-secret"
		auth := credentials.AuthOAuth
		if tc.provider == "azure" || tc.provider == "xiaomi-tokenplan" {
			auth = credentials.AuthAPIKey
		}
		_, err = s.PutConnection(ctx, credentials.Connection{ID: tc.provider, NodeID: node.ID, Name: tc.provider, Identity: "fixture-identity", AuthKind: auth, Enabled: true, AccountID: "fixture-account", Secret: credentials.Secret{AccessToken: "fixture-token", RefreshToken: "fixture-refresh", IDToken: "fixture-id", Cookie: "fixture-cookie", ProviderData: tc.data}})
		if err != nil {
			t.Fatal(err)
		}
		// Catalog models are compiled through the same mutation path as operator models.
		if err := m.PutModel(ctx, Model{ProviderID: tc.provider, ID: tc.model, Name: tc.model}); err != nil {
			t.Fatal(err)
		}
	}
	before, _ := m.Load()
	for _, tc := range cases {
		ref, ok := before.AccountProvider(tc.provider+"/"+tc.model, tc.provider, "openai-chat", nil)
		if !ok || ref.Connection.Settings != tc.want || ref.Connection.Identity != "fixture-identity" || ref.Connection.AuthKind == "" || ref.Connection.AccountID != "fixture-account" {
			t.Fatalf("published %s: %+v selectable=%v", tc.provider, ref.Connection, ok)
		}
		data, err := json.Marshal(ref)
		if err != nil {
			t.Fatal(err)
		}
		var fields map[string]any
		if err := json.Unmarshal(data, &fields); err != nil {
			t.Fatal(err)
		}
		if _, exposed := fields["Connection"]; exposed {
			t.Fatal("ProviderRef JSON exposed compiled connection identity")
		}
		tc.data["region"] = "mutated-input"
	}
	for _, c := range before.providerConfig.Connections {
		if !c.Secret.Empty() || c.Secret.IDToken != "" || c.Secret.ProviderData != nil {
			t.Fatal("credential material reached snapshot")
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				ref, _ := before.AccountProvider("grok-cli/grok-4", "grok-cli", "openai-chat", nil)
				if ref.Connection.Settings.WorkspaceID != "fixture-workspace" {
					t.Error("old snapshot mutated during credential install")
				}
			}
		}()
	}
	rotated := credentials.Secret{AccessToken: "fixture-new-token", ProviderData: map[string]string{"workspaceId": "renewed-workspace", "clientSecret": "renewed-secret"}}
	if err := s.RotateSecret(ctx, "grok-cli", rotated); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	after, _ := m.Load()
	ref, _ := after.AccountProvider("grok-cli/grok-4", "grok-cli", "openai-chat", nil)
	if ref.Connection.Settings.WorkspaceID != "renewed-workspace" {
		t.Fatal("credential install left stale snapshot settings")
	}
	if _, err := m.db.ExecContext(ctx, `CREATE TRIGGER reject_metadata BEFORE UPDATE ON provider_connections BEGIN SELECT RAISE(ABORT,'fixture failure'); END`); err != nil {
		t.Fatal(err)
	}
	rotated.ProviderData["workspaceId"] = "failed-workspace"
	if err := s.RotateSecret(ctx, "grok-cli", rotated); err == nil {
		t.Fatal("accepted failed durable install")
	}
	unchanged, _ := m.Load()
	if unchanged != after {
		t.Fatal("failed commit published snapshot")
	}
	if _, err := m.db.ExecContext(ctx, `DROP TRIGGER reject_metadata`); err != nil {
		t.Fatal(err)
	}
	loaded, err := s.GetConnection(ctx, "grok-cli")
	if err != nil || loaded.Secret.ProviderData["workspaceId"] != "renewed-workspace" {
		t.Fatalf("failed write changed durable data: %v", err)
	}
	loaded.Secret.AccessToken = "fixture-token-only-renewal"
	if err := s.RotateSecret(ctx, "grok-cli", loaded.Secret); err != nil {
		t.Fatal(err)
	}
	stillCurrent, _ := m.Load()
	if stillCurrent != after {
		t.Fatal("token-only renewal recompiled routing configuration")
	}
	restored, err := NewManagerObserved(ctx, m.db, nil, sealer)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, _ := restored.Load()
	if err := m.db.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		got, ok := snapshot.AccountProvider(tc.provider+"/"+tc.model, tc.provider, "openai-chat", nil)
		want := tc.want
		if tc.provider == "grok-cli" {
			want = routing.ProviderSettings{WorkspaceID: "renewed-workspace"}
		}
		if !ok || got.Connection.Settings != want {
			t.Fatalf("restored %s: %+v", tc.provider, got.Connection)
		}
		if got.APIToken != "" {
			t.Fatal("snapshot returned a credential")
		}
	}
}

func BenchmarkAccountProviderMetadata(b *testing.B) {
	ctx := context.Background()
	db, err := sqlite.Open(ctx, filepath.Join(b.TempDir(), "metadata.sqlite"))
	if err != nil {
		b.Fatal(err)
	}
	defer db.Close()
	if err := migrations.NewRunner(db.DB()).Apply(ctx); err != nil {
		b.Fatal(err)
	}
	sealer, err := credentials.NewSealer([]byte("01234567890123456789012345678901"))
	if err != nil {
		b.Fatal(err)
	}
	s, err := credentials.NewStore(db.DB(), sealer)
	if err != nil {
		b.Fatal(err)
	}
	node, err := s.PutNode(ctx, credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: "xiaomi-mimo", Name: "Xiaomi"})
	if err != nil {
		b.Fatal(err)
	}
	_, err = s.PutConnection(ctx, credentials.Connection{ID: "fixture", NodeID: node.ID, Name: "Xiaomi", Identity: "fixture", AuthKind: credentials.AuthAPIKey, Enabled: true, Secret: credentials.Secret{AccessToken: "sk-fixture", ProviderData: map[string]string{"baseUrl": "https://fixture.invalid/v1", "uid": "fixture"}}})
	if err != nil {
		b.Fatal(err)
	}
	m, err := NewManagerObserved(ctx, db.DB(), nil, sealer)
	if err != nil {
		b.Fatal(err)
	}
	snapshot, _ := m.Load()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ref, ok := snapshot.AccountProvider("xiaomi-mimo/mimo-v2-flash", "fixture", "openai-chat", nil)
		if !ok || ref.ConnectionID != "fixture" {
			b.Fatal("missing compiled metadata")
		}
	}
}

func TestCredentialInstallWaitHonorsDeadline(t *testing.T) {
	ctx := context.Background()
	m, s := providerStore(t)
	node, err := s.PutNode(ctx, credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: "github", Name: "GitHub"})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := s.PutConnection(ctx, credentials.Connection{NodeID: node.ID, Name: "Fixture", Identity: "fixture", AuthKind: credentials.AuthOAuth, Enabled: true, Secret: credentials.Secret{AccessToken: "fixture-old"}})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := m.Load()
	entered, release := make(chan struct{}), make(chan struct{})
	finished := make(chan error, 1)
	go func() {
		_, err := m.Update(ctx, func(*Candidate) error { close(entered); <-release; return nil })
		finished <- err
	}()
	<-entered
	defer func() {
		close(release)
		if err := <-finished; err != nil {
			t.Error(err)
		}
	}()
	deadline, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	installed := make(chan error, 1)
	go func() {
		installed <- s.RotateSecret(deadline, connection.ID, credentials.Secret{AccessToken: "fixture-new"})
	}()
	select {
	case err := <-installed:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("renewal wait: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("renewal ignored its persistence deadline while queued")
	}
	after, _ := m.Load()
	if after != before {
		t.Fatal("timed-out install published metadata")
	}
	durable, err := s.GetConnection(ctx, connection.ID)
	if err != nil || durable.Secret.AccessToken != "fixture-old" {
		t.Fatalf("timed-out install changed durable credential: %v", err)
	}
}
