package wire

import (
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

func TestGitHubForcedTransportIsClaudeByName(t *testing.T) {
	for model, want := range map[string]string{
		"claude-haiku-4.5": "anthropic-messages", "Claude-Opus-4.6": "anthropic-messages", "gpt-5.4": "", "gemini-2.5-pro": "", "grok-code-fast-1": "",
	} {
		if got := ForcedTransport("github", model); got != want {
			t.Errorf("%s: %q, want %q", model, got, want)
		}
	}
	if ForcedTransport("openrouter", "claude-haiku-4.5") != "" {
		t.Error("another provider's Claude model is routed by github's rule")
	}
}

func TestGitHubForcesStreamForClaudeMessagesOnly(t *testing.T) {
	for _, tc := range []struct {
		protocol, model string
		want            bool
	}{
		{"anthropic-messages", "claude-haiku-4.5", true},
		{"openai-chat", "claude-haiku-4.5", false},
		{"anthropic-messages", "gpt-5.4", false},
		{chat, "gpt-5.4", false},
	} {
		provider := routing.ProviderRef{ProviderID: "github", Protocol: tc.protocol, UpstreamModel: tc.model}
		if got := ForcesStream(provider); got != tc.want {
			t.Errorf("%s %s: %v", tc.protocol, tc.model, got)
		}
		body := finalizeFor(t, provider, `{"model":"m","stream":false}`)
		if forced := strings.Contains(body, `"stream":true`); forced != tc.want {
			t.Errorf("%s %s: body %s", tc.protocol, tc.model, body)
		}
	}
	other := routing.ProviderRef{ProviderID: "openrouter", Protocol: "anthropic-messages", UpstreamModel: "claude-haiku-4.5"}
	if ForcesStream(other) || finalizeFor(t, other, `{"model":"m","stream":false}`) != `{"model":"m","stream":false}` {
		t.Error("another provider is forced to stream")
	}
}

func finalizeFor(t *testing.T, provider routing.ProviderRef, body string) string {
	t.Helper()
	return string(FinalizeBody(provider, []byte(body)))
}

const copilotRefusal = `{"error":{"message":"model is not accessible via the /chat/completions endpoint"}}`

func TestGitHubEscalationMatrix(t *testing.T) {
	cases := []struct {
		name     string
		provider routing.ProviderRef
		status   int
		body     string
		want     bool
	}{
		{"not accessible", routing.ProviderRef{ProviderID: "github", Protocol: chat, UpstreamModel: "gpt-5.4"}, 400, copilotRefusal, true},
		{"not supported", routing.ProviderRef{ProviderID: "github", Protocol: chat, UpstreamModel: "gpt-5.2-codex"}, 400, `The requested model is not supported`, true},
		{"grok", routing.ProviderRef{ProviderID: "github", Protocol: chat, UpstreamModel: "grok-code-fast-1"}, 400, copilotRefusal, true},
		{"other 400", routing.ProviderRef{ProviderID: "github", Protocol: chat, UpstreamModel: "gpt-5.4"}, 400, `{"error":"bad"}`, false},
		{"case differs", routing.ProviderRef{ProviderID: "github", Protocol: chat, UpstreamModel: "gpt-5.4"}, 400, `the requested model is not supported`, false},
		{"not 400", routing.ProviderRef{ProviderID: "github", Protocol: chat, UpstreamModel: "gpt-5.4"}, 404, copilotRefusal, false},
		{"gemini", routing.ProviderRef{ProviderID: "github", Protocol: chat, UpstreamModel: "gemini-2.5-pro"}, 400, copilotRefusal, false},
		{"claude", routing.ProviderRef{ProviderID: "github", Protocol: chat, UpstreamModel: "claude-haiku-4.5"}, 400, copilotRefusal, false},
		{"already on responses", routing.ProviderRef{ProviderID: "github", Protocol: "openai-responses", UpstreamModel: "gpt-5.4"}, 400, copilotRefusal, false},
		{"messages wire", routing.ProviderRef{ProviderID: "github", Protocol: "anthropic-messages", UpstreamModel: "gpt-5.4"}, 400, copilotRefusal, false},
		{"other provider", routing.ProviderRef{ProviderID: "openai", Protocol: chat, UpstreamModel: "gpt-5.4"}, 400, copilotRefusal, false},
	}
	for _, tc := range cases {
		protocol, endpoint := Escalation(tc.provider, tc.status, []byte(tc.body))
		if got := protocol != ""; got != tc.want || (tc.want && (protocol != "openai-responses" || endpoint != "responses")) {
			t.Errorf("%s: %q %q", tc.name, protocol, endpoint)
		}
	}
}

// The replay's own refusal does not match again: Escalate is asked once per wire
// and the wire it names is not the one it fires on.
func TestGitHubEscalationTargetDoesNotEscalateAgain(t *testing.T) {
	first := routing.ProviderRef{ProviderID: "github", Protocol: chat, UpstreamModel: "gpt-5.4"}
	protocol, endpoint := Escalation(first, 400, []byte(copilotRefusal))
	first.Protocol, first.Endpoint = protocol, endpoint
	if again, _ := Escalation(first, 400, []byte(copilotRefusal)); again != "" {
		t.Fatalf("a second escalation to %q", again)
	}
}

func TestGitHubRequestIDsAreFreshAndGitHubOnly(t *testing.T) {
	ref := routing.ProviderRef{ProviderID: "github", APIToken: "t"}
	seen := map[string]bool{}
	for i := 0; i < 20; i++ {
		for _, id := range []string{OpenAIHeaders(ref).Get("X-Request-Id"), MessagesHeaders(ref, "", "").Get("X-Request-Id")} {
			if len(id) != 36 || seen[id] {
				t.Fatalf("request id %q (seen %v)", id, seen[id])
			}
			seen[id] = true
		}
	}
	if OpenAIHeaders(routing.ProviderRef{ProviderID: "openai", APIToken: "t"}).Get("X-Request-Id") != "" {
		t.Error("another provider sends the request id")
	}
}

// The sanitizer's github rules apply to the body of each wire a github request
// can now travel on, and to nothing else.
func TestGitHubSanitizerRulesReachEveryWire(t *testing.T) {
	for _, tc := range []struct {
		name, provider, protocol, model, in, want string
	}{
		{"gpt-5.4 temperature on responses", "github", "openai-responses", "gpt-5.4", `{"model":"gpt-5.4","temperature":0.5,"input":"x"}`, `{"input":"x","model":"gpt-5.4"}`},
		{"gpt-5.2 keeps temperature on responses", "github", "openai-responses", "gpt-5.2", `{"model":"gpt-5.2","temperature":0.5,"input":"x"}`, ``},
		{"claude haiku thinking on messages", "github", "anthropic-messages", "claude-haiku-4.5", `{"model":"claude-haiku-4.5","thinking":{"type":"enabled"},"stream":true,"messages":[]}`, `{"messages":[],"model":"claude-haiku-4.5","stream":true}`},
		{"claude 4.6 thinking kept on messages", "github", "anthropic-messages", "claude-opus-4.6", `{"model":"claude-opus-4.6","thinking":{"type":"enabled"},"stream":true,"messages":[]}`, ``},
		{"openai provider keeps both", "openai", "openai-responses", "gpt-5.4", `{"model":"gpt-5.4","temperature":0.5,"input":"x"}`, ``},
		{"openrouter claude keeps thinking", "openrouter", "anthropic-messages", "claude-haiku-4.5", `{"model":"claude-haiku-4.5","thinking":{"type":"enabled"},"messages":[]}`, ``},
	} {
		provider := routing.ProviderRef{ProviderID: tc.provider, Protocol: tc.protocol, UpstreamModel: tc.model}
		got := finalizeFor(t, provider, tc.in)
		want := tc.want
		if want == "" {
			want = tc.in
		}
		if got != want {
			t.Errorf("%s\n got %s\nwant %s", tc.name, got, want)
		}
	}
}
