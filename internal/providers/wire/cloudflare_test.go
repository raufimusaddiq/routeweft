package wire

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

func cloudflareRef(protocol, model, account string) routing.ProviderRef {
	provider := ref("cloudflare-ai", protocol, "https://api.cloudflare.com/client/v4/accounts", routing.ProviderSettings{})
	provider.UpstreamModel = model
	provider.Connection.AccountID = account
	return provider
}

// open-sse/providers/registry/cloudflare-ai.js:26,30.
func TestCloudflareTargetURLs(t *testing.T) {
	for _, tc := range []struct{ protocol, model, want string }{
		{chat, "@cf/meta/llama-3.3-70b-instruct-fp8-fast", "https://api.cloudflare.com/client/v4/accounts/acct1/ai/v1/chat/completions"},
		{"systemone", "@cf/cloudflare/clef", "https://api.cloudflare.com/client/v4/accounts/acct1/ai/run/@cf/cloudflare/clef"},
		{"systemone", "@cf/cloudflare/clef-flash", "https://api.cloudflare.com/client/v4/accounts/acct1/ai/run/@cf/cloudflare/clef-flash"},
	} {
		target, err := ResolveTarget(cloudflareRef(tc.protocol, tc.model, " acct1 "), "ignored")
		if err != nil {
			t.Fatalf("%+v: %v", tc, err)
		}
		u, err := target.URL(func(raw string) (*url.URL, error) { return url.Parse(raw) })
		if err != nil || u.String() != tc.want || u.RawQuery != "" {
			t.Errorf("%+v: url %v %v", tc, u, err)
		}
	}
}

func TestCloudflareTargetFailsClosed(t *testing.T) {
	for _, account := range []string{"", "  "} {
		_, err := ResolveTarget(cloudflareRef(chat, "@cf/meta/llama-3.2-1b-instruct", account), "")
		var refused *TargetError
		if !errors.As(err, &refused) || !strings.Contains(err.Error(), "account ID") {
			t.Errorf("account %q: %v", account, err)
		}
	}
	for _, account := range []string{".", "..", "a/b", "a%2fb", "a?b", "a#b", "a b", "a\\b"} {
		if got, err := ResolveTarget(cloudflareRef(chat, "@cf/meta/llama-3.2-1b-instruct", account), ""); err == nil {
			t.Errorf("unsafe account %q accepted: %+v", account, got)
		}
	}
	for _, tc := range []struct{ protocol, model string }{
		{"systemone", "@cf/meta/llama-3.2-1b-instruct"}, {"systemone", "../escape"},
		{chat, "@cf/cloudflare/clef"}, {"openai-responses", "@cf/cloudflare/clef"}, {"anthropic-messages", "@cf/meta/llama-3.2-1b-instruct"},
	} {
		if got, err := ResolveTarget(cloudflareRef(tc.protocol, tc.model, "acct1"), ""); err == nil {
			t.Errorf("unsupported pair %+v accepted: %+v", tc, got)
		}
	}
}

// Auth is the pin's default Bearer (executors/default.js resolveAuthDescriptor).
func TestCloudflareAuthIsBearer(t *testing.T) {
	headers := OpenAIHeaders(cloudflareRef(chat, "@cf/meta/llama-3.2-1b-instruct", "acct1"))
	if headers.Get("Authorization") != "Bearer tok" {
		t.Fatalf("headers %v", headers)
	}
}
