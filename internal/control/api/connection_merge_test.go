package api

import (
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
)

func TestMergeConnectionSecretKeepsTokensAndAppliesIDTokenOnlyUpdate(t *testing.T) {
	existing := credentials.Secret{AccessToken: "a", RefreshToken: "r", ProviderData: map[string]string{"region": "eastus"}}
	merged := mergeConnectionSecret(existing, credentials.Secret{IDToken: "id-new"})
	if merged.IDToken != "id-new" || merged.AccessToken != "a" || merged.RefreshToken != "r" || merged.ProviderData["region"] != "eastus" {
		t.Fatalf("id-token-only update was discarded or lost tokens/metadata: %+v", merged)
	}
}
