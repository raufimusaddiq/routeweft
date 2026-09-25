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

func TestOAuthSpecializedProviderHeaders(t *testing.T) {
	github := openAIProviderHeaders(routing.ProviderRef{ProviderID: "github", Protocol: "openai-chat", APIToken: "token"})
	if github.Get("Authorization") != "Bearer token" || github.Get("copilot-integration-id") != "vscode-chat" || github.Get("editor-version") != "vscode/1.110.0" {
		t.Fatalf("github headers=%v", github)
	}
	cline := openAIProviderHeaders(routing.ProviderRef{ProviderID: "cline", Protocol: "openai-chat", APIToken: "token"})
	if cline.Get("HTTP-Referer") != "https://cline.bot" || cline.Get("X-Title") != "Cline" {
		t.Fatalf("cline headers=%v", cline)
	}
	if iflow := openAIProviderHeaders(routing.ProviderRef{ProviderID: "iflow", Protocol: "openai-chat", APIToken: "token"}); iflow.Get("User-Agent") != "iFlow-Cli" {
		t.Fatalf("iflow headers=%v", iflow)
	}
	plain := openAIProviderHeaders(routing.ProviderRef{ProviderID: "mistral", Protocol: "openai-chat", APIToken: "token"})
	if plain.Get("copilot-integration-id") != "" || plain.Get("User-Agent") != "" {
		t.Fatalf("unexpected fingerprint on plain provider: %v", plain)
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
	// GitHub Copilot's native Messages route must authenticate as bearer and
	// carry the Copilot fingerprint, not x-api-key.
	github := anthropicHeaders(request, routing.ProviderRef{ProviderID: "github", APIToken: "copilot-token"})
	if github.Get("Authorization") != "Bearer copilot-token" || github.Get("X-Api-Key") != "" || github.Get("copilot-integration-id") != "vscode-chat" || github.Get("editor-version") != "vscode/1.110.0" {
		t.Fatalf("github messages headers=%v", github)
	}
	clientOptIn := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	clientOptIn.Header.Set("Anthropic-Beta", "client-beta")
	clientOptIn.Header.Set("Anthropic-Version", "2099-01-01")
	forwarded := anthropicHeaders(clientOptIn, routing.ProviderRef{ProviderID: "anthropic", APIToken: "key"})
	if forwarded.Get("Anthropic-Beta") != "client-beta" || forwarded.Get("Anthropic-Version") != "2099-01-01" {
		t.Fatalf("client opt-in not forwarded: %v", forwarded)
	}
}
