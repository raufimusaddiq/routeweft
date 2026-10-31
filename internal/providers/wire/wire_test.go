package wire

import (
	"bytes"
	"net/http"
	"reflect"
	"sort"
	"testing"

	anthropicprovider "github.com/raufimusaddiq/routeweft/internal/providers/anthropic"
	claudeprovider "github.com/raufimusaddiq/routeweft/internal/providers/claude"
	clineprovider "github.com/raufimusaddiq/routeweft/internal/providers/cline"
	codexprovider "github.com/raufimusaddiq/routeweft/internal/providers/codex"
	"github.com/raufimusaddiq/routeweft/internal/providers/oauthheaders"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// The functions below are the ingress branches this package replaced, frozen
// verbatim from main at a6de333 (internal/ingress/ingress.go and openai.go). They
// are the oracle for the refactor: the seam must return exactly what they did,
// for every provider id, so a behaviour change cannot hide inside the move.

func legacyAnthropicHeaders(clientVersion, clientBeta string, provider routing.ProviderRef) http.Header {
	headers := http.Header{}
	bearer := provider.ProviderID == "claude" || provider.ProviderID == "github"
	if provider.APIToken != "" {
		if bearer {
			headers.Set("Authorization", "Bearer "+provider.APIToken)
		} else {
			headers.Set("X-Api-Key", provider.APIToken)
		}
	}
	if provider.ProviderID == "claude" {
		for name, value := range claudeprovider.Headers() {
			headers.Set(name, value)
		}
	}
	for name, value := range oauthheaders.Headers(provider.ProviderID) {
		headers.Set(name, value)
	}
	if clientVersion != "" {
		headers.Set("Anthropic-Version", clientVersion)
	} else {
		headers.Set("Anthropic-Version", anthropicprovider.Version)
	}
	if clientBeta != "" {
		headers.Set("Anthropic-Beta", clientBeta)
	} else if provider.ProviderID == "anthropic" {
		headers.Set("Anthropic-Beta", anthropicprovider.Beta)
	}
	return headers
}

func legacyOpenAIProviderHeaders(provider routing.ProviderRef) http.Header {
	headers := http.Header{}
	if provider.APIToken != "" {
		headers.Set("Authorization", "Bearer "+provider.APIToken)
	}
	if provider.ProviderID == "codex" {
		for name, value := range codexprovider.Headers() {
			headers.Set(name, value)
		}
	}
	for name, value := range oauthheaders.Headers(provider.ProviderID) {
		headers.Set(name, value)
	}
	if clineprovider.Applies(provider.ProviderID) {
		for name, value := range clineprovider.Headers(provider.APIToken) {
			headers.Set(name, value)
		}
	}
	return headers
}

func providerIDs() []string {
	ids := []string{"", "unknown-provider", "openai-compatible-x"}
	for _, spec := range registry.Builtins() {
		ids = append(ids, spec.ID)
	}
	return ids
}

var tokens = []string{
	"", "sk-plain", "clp_pass-key", "workos:already", "WorkOS:Upper",
	"eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.sig",
}

// providers whose headers PR-C changed on purpose; headers_test.go pins their new
// values. Every other provider must still match the frozen legacy result.
// iflow is added by #115 PR-E: every request now carries a per-request HMAC
// signature (iflow_test.go pins it), so it cannot match a frozen header set.
// commandcode carries the pin's version, environment and session headers
// (commandcode_test.go pins them).
// API.airforce adds its registry referer/title pair (airforce_wire_test.go).
// github adds the pin's per-request x-request-id (github_test.go).
// vertex and vertex-partner send no bearer: the credential is a JSON document or a key that VertexDo places (vertex_test.go).
var headersChanged = map[string]bool{"azure": true, "api-airforce": true, "kimi": true, "kilocode": true, "iflow": true, "kiro": true, "commandcode": true, "opencode": true, "github": true, "codebuddy-cn": true, "codebuddy-intl": true, "vertex": true, "vertex-partner": true, "grok-web": true, "perplexity-web": true}

func TestOpenAIHeadersMatchLegacyForEveryProvider(t *testing.T) {
	for _, id := range providerIDs() {
		if headersChanged[id] {
			continue
		}
		for _, token := range tokens {
			ref := routing.ProviderRef{ProviderID: id, APIToken: token}
			if got, want := OpenAIHeaders(ref), legacyOpenAIProviderHeaders(ref); !reflect.DeepEqual(got, want) {
				t.Errorf("%s token %q: openai headers\n got %v\nwant %v", id, token, got, want)
			}
		}
	}
}

func TestMessagesHeadersMatchLegacyForEveryProvider(t *testing.T) {
	for _, id := range providerIDs() {
		if headersChanged[id] && id != "kiro" {
			continue
		}
		for _, token := range tokens {
			for _, version := range []string{"", "2024-01-01"} {
				for _, beta := range []string{"", "tools-2024"} {
					ref := routing.ProviderRef{ProviderID: id, APIToken: token}
					if got, want := MessagesHeaders(ref, version, beta), legacyAnthropicHeaders(version, beta, ref); !reflect.DeepEqual(got, want) {
						t.Errorf("%s token %q version %q beta %q: messages headers\n got %v\nwant %v", id, token, version, beta, got, want)
					}
				}
			}
		}
	}
}

func TestUnwrapResponseMatchesLegacyForEveryProvider(t *testing.T) {
	bodies := [][]byte{
		[]byte(`{"success":true,"data":{"id":"x","usage":{"total_tokens":3}}}`),
		[]byte(`{"success":false,"data":{"id":"x"}}`),
		[]byte(`{"id":"bare"}`), []byte(`not json`), nil,
	}
	for _, id := range providerIDs() {
		unwrap := UnwrapResponse(id)
		if (unwrap != nil) != clineprovider.Applies(id) {
			t.Errorf("%s: unwrap present=%v, legacy applies=%v", id, unwrap != nil, clineprovider.Applies(id))
			continue
		}
		for _, body := range bodies {
			want, _ := clineprovider.UnwrapEnvelope(body)
			if !clineprovider.Applies(id) {
				want = body
			}
			got := body
			if unwrap != nil {
				got = unwrap(body)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s: unwrap %q got %q want %q", id, body, got, want)
			}
		}
	}
}

func TestPrepareMessagesMatchesLegacyForEveryProvider(t *testing.T) {
	body := []byte(`{"model":"m","max_tokens":1,"messages":[{"role":"user","content":"hi"}]}`)
	for _, id := range providerIDs() {
		want := body
		var wantErr error
		if id == "claude" {
			want, wantErr = claudeprovider.WithIdentity(body)
		}
		got, err := PrepareMessages(id, body)
		if (err == nil) != (wantErr == nil) || !bytes.Equal(got, want) {
			t.Errorf("%s: prepare got %q/%v want %q/%v", id, got, err, want, wantErr)
		}
	}
}

// The hook list is closed: the six branches ingress used to hold (PR-A) plus the
// registry-quirk providers. A new hook is a deliberate change to this list.
func TestRegisteredHooksAreExactlyTheDeclaredProviders(t *testing.T) {
	got := Registered()
	sort.Strings(got)
	want := []string{"anthropic", "antigravity", "api-airforce", "azure", "cerebras", "claude", "cline", "clinepass", "cloudflare-ai", "codebuddy-cn", "codebuddy-intl", "codex", "commandcode", "cursor", "deepseek", "gemini-cli", "github", "glm", "grok-cli", "grok-web", "iflow", "kenari", "kilocode", "kimchi", "kimi", "kiro", "mimo-free", "minimax", "minimax-cn", "mistral", "mmf", "opencode", "opencode-go", "perplexity-web", "qoder", "vertex", "vertex-partner", "xiaomi-tokenplan", "zed"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("registered hooks %v, want %v", got, want)
	}
}
