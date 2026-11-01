package runtime

import (
	"context"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

// The catalog id is what an operator selects; the provider's API gets the
// registry's upstream id (providerModels.js getModelUpstreamId, chatCore.js:126).
// DeepSeek answers 400 to "deepseek-v4.1-flash" and Blackbox only knows its
// prefixed ids. Ids a wire hook reads by suffix are left whole for the hook.
func TestServingRouteCarriesTheRegistrysUpstreamModelID(t *testing.T) {
	ctx := context.Background()
	m, s := providerStore(t)
	for _, provider := range []string{"blackbox", "deepseek", "cerebras"} {
		node, err := s.PutNode(ctx, credentials.Node{ID: provider, Kind: credentials.NodeBuiltin, ProviderID: provider, Name: provider, Transports: []string{"openai-chat"}, BaseURL: "https://upstream.example/v1"})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.PutConnection(ctx, credentials.Connection{ID: provider + "-acct", NodeID: node.ID, Name: provider, Identity: provider, AuthKind: credentials.AuthAPIKey, Enabled: true, Secret: credentials.Secret{AccessToken: "k"}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, model := range []Model{
		{ProviderID: "blackbox", ID: "claude-fable-5", Name: "x"},
		{ProviderID: "blackbox", ID: "not-in-the-table", Name: "x"},
		{ProviderID: "deepseek", ID: "deepseek-v4.1-flash", Name: "x"},
		{ProviderID: "deepseek", ID: "deepseek-v4-pro-max", Name: "x"},
		{ProviderID: "cerebras", ID: "gpt-5.4", Name: "x"},
	} {
		if err := m.PutModel(ctx, model); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, _ := m.Load()
	for _, tc := range []struct{ provider, model, want string }{
		{"blackbox", "claude-fable-5", "blackboxai/anthropic/claude-fable-5"},
		{"blackbox", "not-in-the-table", "not-in-the-table"},
		{"deepseek", "deepseek-v4.1-flash", "deepseek-flash"},
		// A hook-owned alias keeps its id: the sanitizer reads it.
		{"deepseek", "deepseek-v4-pro-max", "deepseek-v4-pro-max"},
		// The same catalog id on another provider is not rewritten.
		{"cerebras", "gpt-5.4", "gpt-5.4"},
	} {
		ref, ok := snapshot.AccountProvider(tc.provider+"/"+tc.model, tc.provider+"-acct", "openai-chat", nil)
		if !ok || ref.UpstreamModel != tc.want {
			t.Errorf("%s/%s: ok=%v upstream=%q, want %q", tc.provider, tc.model, ok, ref.UpstreamModel, tc.want)
		}
	}
}

// Telemetry reads the catalog id: a rewritten route names it, an unrewritten
// route leaves it empty so RecordedModel falls back to the wire id.
func TestRewrittenRouteKeepsTheCatalogIDForTelemetry(t *testing.T) {
	ctx := context.Background()
	m, s := providerStore(t)
	node, err := s.PutNode(ctx, credentials.Node{ID: "blackbox", Kind: credentials.NodeBuiltin, ProviderID: "blackbox", Name: "b", Transports: []string{"openai-chat"}, BaseURL: "https://upstream.example/v1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutConnection(ctx, credentials.Connection{ID: "b-acct", NodeID: node.ID, Name: "b", Identity: "b", AuthKind: credentials.AuthAPIKey, Enabled: true, Secret: credentials.Secret{AccessToken: "k"}}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"gpt-5.5", "plain"} {
		if err := m.PutModel(ctx, Model{ProviderID: "blackbox", ID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, _ := m.Load()
	for model, want := range map[string]string{"gpt-5.5": "gpt-5.5", "plain": "plain"} {
		ref, ok := snapshot.AccountProvider("blackbox/"+model, "b-acct", "openai-chat", nil)
		if !ok || ref.RecordedModel() != want {
			t.Errorf("%s: ok=%v recorded=%q, want %q", model, ok, ref.RecordedModel(), want)
		}
	}
	ref, _ := snapshot.AccountProvider("blackbox/gpt-5.5", "b-acct", "openai-chat", nil)
	if ref.UpstreamModel != "blackboxai/openai/gpt-5.5" || ref.CatalogModel != "gpt-5.5" {
		t.Errorf("route = %+v", ref)
	}
}
