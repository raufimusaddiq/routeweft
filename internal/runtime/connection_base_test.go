package runtime

import (
	"context"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

// A node with no base URL of its own (Azure OpenAI declares none) is served from
// the base the connection names; a connection that names none is not selectable;
// a node that has a base keeps it whatever a connection says.
func TestConnectionSuppliesTheBaseURLOnlyWhenTheNodeHasNone(t *testing.T) {
	ctx := context.Background()
	m, s := providerStore(t)
	cases := []struct {
		name, provider, nodeBase string
		data                     map[string]string
		wantBase                 string
		selectable               bool
	}{
		{"azure endpoint", "azure", "", map[string]string{"azureEndpoint": "https://acme.openai.azure.com"}, "https://acme.openai.azure.com", true},
		{"azure baseUrl", "azure", "", map[string]string{"baseUrl": "https://gw.example/azure"}, "https://gw.example/azure", true},
		{"azure both", "azure", "", map[string]string{"baseUrl": "https://gw.example/azure", "azureEndpoint": "https://acme.openai.azure.com"}, "https://acme.openai.azure.com", true},
		{"azure none", "azure", "", map[string]string{"apiVersion": "2025-01-01"}, "", false},
		{"node base wins", "cerebras", "", map[string]string{"baseUrl": "https://evil.example", "azureEndpoint": "https://evil.example"}, "https://api.cerebras.ai/v1", true},
	}
	for _, c := range cases {
		node, err := s.PutNode(ctx, credentials.Node{ID: c.name, Kind: credentials.NodeBuiltin, ProviderID: c.provider, Name: c.name, BaseURL: c.nodeBase, Transports: []string{"openai-chat"}})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := s.PutConnection(ctx, credentials.Connection{ID: c.name, NodeID: node.ID, Name: c.name, Identity: c.name, AuthKind: credentials.AuthAPIKey, Enabled: true, Secret: credentials.Secret{AccessToken: "k", ProviderData: c.data}}); err != nil {
			t.Fatal(err)
		}
		if err := m.PutModel(ctx, Model{ProviderID: c.provider, ID: "m", Name: "m"}); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, _ := m.Load()
	for _, c := range cases {
		ref, ok := snapshot.AccountProvider(c.provider+"/m", c.name, "openai-chat", nil)
		if ok != c.selectable || ref.BaseURL != c.wantBase {
			t.Errorf("%s: selectable=%v base=%q, want selectable=%v base=%q", c.name, ok, ref.BaseURL, c.selectable, c.wantBase)
		}
	}
}

// Kilo stores its organization as orgId; it must reach the serving route.
func TestKiloOrgIDReachesTheRoute(t *testing.T) {
	ctx := context.Background()
	m, s := providerStore(t)
	node, err := s.PutNode(ctx, credentials.Node{ID: "kilocode", Kind: credentials.NodeBuiltin, ProviderID: "kilocode", Name: "Kilo", BaseURL: "https://api.kilo.ai/api", Transports: []string{"openai-chat"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.PutConnection(ctx, credentials.Connection{ID: "k", NodeID: node.ID, Name: "k", Identity: "id", AuthKind: credentials.AuthOAuth, Enabled: true, Secret: credentials.Secret{AccessToken: "t", ProviderData: map[string]string{"orgId": "org-42"}}}); err != nil {
		t.Fatal(err)
	}
	if err := m.PutModel(ctx, Model{ProviderID: "kilocode", ID: "m", Name: "m"}); err != nil {
		t.Fatal(err)
	}
	snapshot, _ := m.Load()
	ref, ok := snapshot.AccountProvider("kilocode/m", "k", "openai-chat", nil)
	if !ok || ref.Connection.Settings.OrgID != "org-42" {
		t.Fatalf("kilocode route: ok=%v settings=%+v", ok, ref.Connection.Settings)
	}
}
