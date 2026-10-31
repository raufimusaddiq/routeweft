package wire

import (
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// #228: the base a connection is dialled at, and the host Kimchi's catalog is read from.

func TestLocalOllamaDialsTheConnectionsHostBeforeTheCatalogDefault(t *testing.T) {
	const catalog = "http://localhost:11434"
	own := routing.ProviderSettings{BaseURL: " http://10.1.2.3:11434// "}
	// config/providers.js:7-10: trim, drop one trailing slash, default only when blank.
	if got := NodeBase("ollama-local", catalog, catalog, own); got != "http://10.1.2.3:11434" {
		t.Fatalf("connection host = %q", got)
	}
	if got := NodeBase("ollama-local", catalog, catalog, routing.ProviderSettings{BaseURL: "  "}); got != catalog {
		t.Fatalf("blank host = %q", got)
	}
	// Every other provider keeps the node's own base ahead of a connection's baseUrl.
	if got := NodeBase("openai", "https://api.openai.com/v1", "", own); got != "https://api.openai.com/v1" {
		t.Fatalf("another provider = %q", got)
	}
	if got := NodeBase("azure", "", "", routing.ProviderSettings{AzureEndpoint: "https://r.openai.azure.com", BaseURL: "https://x"}); got != "https://r.openai.azure.com" {
		t.Fatalf("azure = %q", got)
	}
}

func TestKimchiListingReadsTheConnectionsEndpoint(t *testing.T) {
	list := func(endpoint string) Listing {
		t.Helper()
		listing, err := kimchiListing(routing.ProviderRef{Connection: routing.ConnectionMetadata{Settings: routing.ProviderSettings{KimchiEndpoint: endpoint}}})
		if err != nil {
			t.Fatal(err)
		}
		return listing
	}
	// kimchiModels.js:17-24,144: blank means the default host; trailing slashes are dropped.
	for endpoint, want := range map[string]string{
		"":                                 "https://llm.kimchi.dev/v1/models/metadata",
		"   ":                              "https://llm.kimchi.dev/v1/models/metadata",
		"https://kimchi.example.test":      "https://kimchi.example.test/v1/models/metadata",
		" https://kimchi.example.test/// ": "https://kimchi.example.test/v1/models/metadata",
		"https://kimchi.example.test/eu":   "https://kimchi.example.test/eu/v1/models/metadata",
	} {
		got := list(endpoint)
		if got.URL != want || got.Query != "include_in_cli=true" || got.Headers["User-Agent"] != "kimchi/0.1.40" {
			t.Errorf("endpoint %q: %+v, want %s", endpoint, got, want)
		}
	}
}
