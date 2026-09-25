package credentials

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestResolveForAccountUsesNodeIdentityAndLiveCredential(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node, err := store.PutNode(ctx, Node{Kind: NodeGeneric, ProviderID: "corp-llm", Name: "Corp", Prefix: "corp", BaseURL: "https://llm.example:8443/v1", Transports: []string{"openai-chat", "anthropic-messages"}})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthAPIKey, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "live-key"}})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(store, nil)
	registry.Register(connection, nil)
	ref, err := (Resolver{Store: store, Registry: registry}).ResolveForAccount(ctx, connection.ID, "corp/model-1")
	if err != nil {
		t.Fatal(err)
	}
	if ref.ProviderID != "corp-llm" || ref.Protocol != "openai-chat" || ref.BaseURL != "https://llm.example:8443/v1" || ref.APIToken != "live-key" || ref.ConnectionID != connection.ID || ref.UpstreamModel != "corp/model-1" {
		t.Fatalf("ref=%+v", ref)
	}
}

func TestResolveForAccountFallsBackToCompiledIdentity(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node, err := store.PutNode(ctx, Node{Kind: NodeBuiltin, ProviderID: "codex", Name: "Codex"})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthOAuth, Identity: "acct", Enabled: true, Secret: Secret{AccessToken: "live", Expiry: time.Now().Add(time.Hour)}})
	if err != nil {
		t.Fatal(err)
	}
	registry := NewRegistry(store, nil)
	registry.Register(connection, nil)
	fallback := func(providerID string) (string, string, bool) {
		if providerID != "codex" {
			return "", "", false
		}
		return "openai-responses", "https://chatgpt.com/backend-api/codex", true
	}
	ref, err := (Resolver{Store: store, Registry: registry, Fallback: fallback}).ResolveForAccount(ctx, connection.ID, "gpt-5.3-codex")
	if err != nil {
		t.Fatal(err)
	}
	if ref.Protocol != "openai-responses" || ref.BaseURL != "https://chatgpt.com/backend-api/codex" {
		t.Fatalf("ref=%+v", ref)
	}
}

func TestResolveForAccountRejectsDisabledAndUnknown(t *testing.T) {
	ctx := context.Background()
	store, _ := testStore(t)
	node, err := store.PutNode(ctx, Node{Kind: NodeBuiltin, ProviderID: "groq", Name: "Groq"})
	if err != nil {
		t.Fatal(err)
	}
	connection, err := store.PutConnection(ctx, Connection{NodeID: node.ID, Name: "primary", AuthKind: AuthAPIKey, Identity: "acct", Enabled: false, Secret: Secret{AccessToken: "k"}})
	if err != nil {
		t.Fatal(err)
	}
	resolver := Resolver{Store: store, Registry: NewRegistry(store, nil)}
	if _, err := resolver.ResolveForAccount(ctx, connection.ID, "m"); err == nil {
		t.Fatal("disabled connection was resolvable")
	}
	if _, err := resolver.ResolveForAccount(ctx, "missing", "m"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown connection err=%v", err)
	}
	if _, err := (Resolver{}).ResolveForAccount(ctx, "x", "m"); err == nil {
		t.Fatal("unconfigured resolver accepted a call")
	}
}
