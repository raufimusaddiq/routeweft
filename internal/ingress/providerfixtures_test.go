package ingress

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/compat/fixtures"
	"github.com/raufimusaddiq/routeweft/compat/mockupstream"
	anthropicadapter "github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/providers/oauthheaders"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// providerResolver returns a fixed provider identity for one provider id.
func providerResolver(providerID, protocol, baseURL string) ProviderResolver {
	return func(*runtime.RuntimeSnapshot, string) (routing.ProviderRef, bool) {
		return routing.ProviderRef{ProviderID: providerID, Protocol: protocol, BaseURL: baseURL, APIToken: "provider-credential"}, true
	}
}

func providerFixtureServer(t *testing.T, protocol fixtures.Protocol) *mockupstream.Server {
	t.Helper()
	server, err := mockupstream.Start(compatFixtures(t, protocol))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

// providerFixtureServerFor starts a mock upstream that serves only the named
// Chat fixtures, so same-path fixtures cannot shadow each other.
func providerFixtureServerFor(t *testing.T, protocol fixtures.Protocol, ids ...string) *mockupstream.Server {
	t.Helper()
	want := make(map[string]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	var selected []fixtures.Exchange
	for _, exchange := range compatFixtures(t, protocol) {
		if want[exchange.ID] {
			selected = append(selected, exchange)
		}
	}
	server, err := mockupstream.Start(selected)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Close() })
	return server
}

// TestProviderKimiChatNativeReplaysFixture proves a dual-auth multi-transport
// provider round-trips the shared OpenAI Chat adapter byte-for-byte.
func TestProviderKimiChatNativeReplaysFixture(t *testing.T) {
	exchange := compatFixture(t, fixtures.OpenAIChat, "provider-kimi-chat-nonstreaming")
	server := providerFixtureServerFor(t, fixtures.OpenAIChat, "provider-kimi-chat-nonstreaming")
	mux, key := keyedHandler(t, Options{AllowPrivateUpstreams: true, ProviderResolver: providerResolver("kimi", openaiadapter.ChatProtocol, server.URL())})
	recorder := httptest.NewRecorder()
	bearer(mux, key).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(exchange.Request.Body)))
	if recorder.Code != http.StatusOK || recorder.Body.String() != exchange.Response.Body {
		t.Fatalf("kimi status %d body %s", recorder.Code, recorder.Body.String())
	}
	seen := server.Requests()
	if len(seen) != 1 || seen[0].Body != exchange.Request.Body {
		t.Fatalf("kimi upstream %+v", seen)
	}
}

// TestProviderXiaomiMimoMessagesNativeUsesProviderPath proves a source-matching
// Messages route posts to the provider-specific Messages path (PR34 resolution).
func TestProviderXiaomiMimoMessagesNativeUsesProviderPath(t *testing.T) {
	exchange := compatFixture(t, fixtures.Anthropic, "provider-xiaomi-mimo-messages-nonstreaming")
	server := providerFixtureServer(t, fixtures.Anthropic)
	mux, key := keyedHandler(t, Options{AllowPrivateUpstreams: true,
		EndpointFor: func(providerID, transport string) (string, bool) {
			if providerID == "xiaomi-mimo" && transport == "anthropic-messages" {
				return "anthropic/v1/messages", true
			}
			return "", false
		},
		ProviderResolver: providerResolver("xiaomi-mimo", anthropicadapter.MessagesProtocol, server.URL()),
	})
	recorder := httptest.NewRecorder()
	bearer(mux, key).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(exchange.Request.Body)))
	if recorder.Code != http.StatusOK || recorder.Body.String() != exchange.Response.Body {
		t.Fatalf("mimo status %d body %s", recorder.Code, recorder.Body.String())
	}
	seen := server.Requests()
	if len(seen) != 1 || seen[0].Path != "/anthropic/v1/messages" || seen[0].Headers.Get("X-Api-Key") != "provider-credential" {
		t.Fatalf("mimo upstream %+v", seen)
	}
}

// TestGitHubCopilotChatSendsFingerprint proves the OAuth-specialized header
// policy reaches the real outbound request for the Copilot chat path.
func TestGitHubCopilotChatSendsFingerprint(t *testing.T) {
	exchange := compatFixture(t, fixtures.OpenAIChat, "provider-github-copilot-chat-nonstreaming")
	server := providerFixtureServerFor(t, fixtures.OpenAIChat, "provider-github-copilot-chat-nonstreaming")
	mux, key := keyedHandler(t, Options{AllowPrivateUpstreams: true, ProviderResolver: providerResolver("github", openaiadapter.ChatProtocol, server.URL())})
	recorder := httptest.NewRecorder()
	bearer(mux, key).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(exchange.Request.Body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("copilot status %d", recorder.Code)
	}
	seen := server.Requests()
	if len(seen) != 1 {
		t.Fatalf("copilot upstream %+v", seen)
	}
	if got := seen[0].Headers.Get("copilot-integration-id"); got != oauthheaders.CopilotIntegrationID {
		t.Fatalf("copilot fingerprint missing: %q", got)
	}
	if seen[0].Headers.Get("Authorization") != "Bearer provider-credential" {
		t.Fatalf("copilot credential header wrong: %q", seen[0].Headers.Get("Authorization"))
	}
	// The client credential must never be forwarded.
	if seen[0].Headers.Get("X-Api-Key") != "" {
		t.Fatal("copilot request leaked an x-api-key header")
	}
}

// TestClineEnvelopeFixtureReachesUpstream asserts the cline referer/title
// headers are applied; envelope unwrapping is a later provider-module slice.
func TestClineEnvelopeFixtureReachesUpstream(t *testing.T) {
	exchange := compatFixture(t, fixtures.OpenAIChat, "provider-cline-envelope-nonstreaming")
	server := providerFixtureServerFor(t, fixtures.OpenAIChat, "provider-cline-envelope-nonstreaming")
	mux, key := keyedHandler(t, Options{AllowPrivateUpstreams: true,
		EndpointFor: func(providerID, transport string) (string, bool) {
			if providerID == "cline" && transport == "openai-chat" {
				return "api/v1/chat/completions", true
			}
			return "", false
		},
		ProviderResolver: providerResolver("cline", openaiadapter.ChatProtocol, server.URL()),
	})
	recorder := httptest.NewRecorder()
	bearer(mux, key).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(exchange.Request.Body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("cline status %d", recorder.Code)
	}
	seen := server.Requests()
	if len(seen) != 1 || seen[0].Path != "/api/v1/chat/completions" || seen[0].Headers.Get("X-Title") != "Cline" || seen[0].Headers.Get("HTTP-Referer") != "https://cline.bot" {
		t.Fatalf("cline upstream %+v", seen)
	}
	// The recorded envelope body is forwarded verbatim on the native path.
	if recorder.Body.String() != exchange.Response.Body {
		t.Fatalf("cline body %s", recorder.Body.String())
	}
}

// TestOpenRouterPassthroughStreamRelaysBytes proves a dynamic passthrough
// gateway's vendor-prefixed streaming response is relayed uncompressed and
// terminates exactly once.
func TestOpenRouterPassthroughStreamRelaysBytes(t *testing.T) {
	exchange := compatFixture(t, fixtures.OpenAIChat, "provider-openrouter-passthrough-streaming")
	server := providerFixtureServerFor(t, fixtures.OpenAIChat, "provider-openrouter-passthrough-streaming")
	mux, key := keyedHandler(t, Options{AllowPrivateUpstreams: true, ProviderResolver: providerResolver("openrouter", openaiadapter.ChatProtocol, server.URL())})
	recorder := httptest.NewRecorder()
	bearer(mux, key).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(exchange.Request.Body)))
	if recorder.Code != http.StatusOK {
		t.Fatalf("openrouter status %d", recorder.Code)
	}
	body := recorder.Body.String()
	if strings.Count(body, exchange.Terminal) != 1 {
		t.Fatalf("terminal marker count != 1: %q", body)
	}
	if seen := server.Requests(); len(seen) != 1 || !strings.Contains(seen[0].Body, "anthropic/claude-sonnet-4") {
		t.Fatalf("openrouter upstream %+v", seen)
	}
}

// TestProviderFixturesCoverExpectedShapes guards the fixture oracle against
// accidental deletion of the provider-level coverage added here.

// TestCodeBuddyCNUsageFixtureReplays proves the provider-specific completion
// path and usage-reporting provider round-trip through the shared adapter.
func TestCodeBuddyCNUsageFixtureReplays(t *testing.T) {
	exchange := compatFixture(t, fixtures.OpenAIChat, "provider-codebuddy-cn-usage-nonstreaming")
	server := providerFixtureServerFor(t, fixtures.OpenAIChat, "provider-codebuddy-cn-usage-nonstreaming")
	mux, key := keyedHandler(t, Options{AllowPrivateUpstreams: true,
		EndpointFor: func(providerID, transport string) (string, bool) {
			if providerID == "codebuddy-cn" && transport == "openai-chat" {
				return "v2/chat/completions", true
			}
			return "", false
		},
		ProviderResolver: providerResolver("codebuddy-cn", openaiadapter.ChatProtocol, server.URL()),
	})
	recorder := httptest.NewRecorder()
	bearer(mux, key).ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(exchange.Request.Body)))
	if recorder.Code != http.StatusOK || recorder.Body.String() != exchange.Response.Body {
		t.Fatalf("codebuddy status %d body %s", recorder.Code, recorder.Body.String())
	}
	seen := server.Requests()
	if len(seen) != 1 || seen[0].Path != "/v2/chat/completions" || seen[0].Headers.Get("User-Agent") != "CLI/2.108.1 CodeBuddy/2.108.1" {
		t.Fatalf("codebuddy upstream %+v", seen)
	}
}

func TestProviderFixturesCoverExpectedShapes(t *testing.T) {
	all, err := fixtures.Load()
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]fixtures.Protocol{
		"provider-kimi-chat-nonstreaming":            fixtures.OpenAIChat,
		"provider-xiaomi-mimo-messages-nonstreaming": fixtures.Anthropic,
		"provider-github-copilot-chat-nonstreaming":  fixtures.OpenAIChat,
		"provider-cline-envelope-nonstreaming":       fixtures.OpenAIChat,
		"provider-codebuddy-cn-usage-nonstreaming":   fixtures.OpenAIChat,
		"provider-openrouter-passthrough-streaming":  fixtures.OpenAIChat,
	}
	present := map[string]fixtures.Protocol{}
	for _, exchange := range all {
		present[exchange.ID] = exchange.Protocol
	}
	for id, protocol := range want {
		if present[id] != protocol {
			t.Errorf("fixture %s missing or wrong protocol (got %q)", id, present[id])
		}
	}
}
