package ingress

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

func TestCodexProviderHeadersCarryCLIFingerprint(t *testing.T) {
	provider := routing.ProviderRef{ProviderID: "codex", Protocol: "openai-responses", BaseURL: "https://chatgpt.com/backend-api", APIToken: "token"}
	headers := openAIProviderHeaders(provider)
	if headers.Get("Authorization") != "Bearer token" || headers.Get("originator") != "codex_cli_rs" || headers.Get("User-Agent") != "codex_cli_rs/0.154.0" {
		t.Fatalf("codex headers=%v", headers)
	}
	plain := openAIProviderHeaders(routing.ProviderRef{ProviderID: "openai", Protocol: "openai-chat", APIToken: "token"})
	if plain.Get("originator") != "" || plain.Get("Authorization") != "Bearer token" {
		t.Fatalf("openai headers=%v", plain)
	}
}

func TestAnthropicProviderHeaderPolicy(t *testing.T) {
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	keyed := anthropicHeaders(request, routing.ProviderRef{ProviderID: "anthropic", APIToken: "key"})
	if keyed.Get("X-Api-Key") != "key" || keyed.Get("Authorization") != "" || keyed.Get("Anthropic-Version") != "2023-06-01" || keyed.Get("Anthropic-Beta") == "" {
		t.Fatalf("anthropic headers=%v", keyed)
	}
	oauth := anthropicHeaders(request, routing.ProviderRef{ProviderID: "claude", APIToken: "token"})
	if oauth.Get("Authorization") != "Bearer token" || oauth.Get("X-Api-Key") != "" || oauth.Get("User-Agent") == "" {
		t.Fatalf("claude headers=%v", oauth)
	}
	clientOptIn := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	clientOptIn.Header.Set("Anthropic-Beta", "client-beta")
	clientOptIn.Header.Set("Anthropic-Version", "2099-01-01")
	forwarded := anthropicHeaders(clientOptIn, routing.ProviderRef{ProviderID: "anthropic", APIToken: "key"})
	if forwarded.Get("Anthropic-Beta") != "client-beta" || forwarded.Get("Anthropic-Version") != "2099-01-01" {
		t.Fatalf("client opt-in not forwarded: %v", forwarded)
	}
}
