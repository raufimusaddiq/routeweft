package wire

import (
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// The pin declares forceStream on these registry entries and nowhere else
// (registry/api-airforce.js:32, codebuddy-cn.js:23, codebuddy-intl.js:25,
// codex.js:40, commandcode.js:24, grok-cli.js:46, openai.js:28, opencode.js:20,
// zed.js:27, and the gpt-5.6-luna model of opencode-go.js:41). Every one is
// accounted for here, so a provider is neither forced by accident nor left out
// without a reason. A reason names why a provider the pin forces is not forced
// by this hook.
var pinForcedStream = map[string]string{
	"api-airforce":   "",
	"codebuddy-cn":   "",
	"codebuddy-intl": "",
	"codex":          "",
	"grok-cli":       "",
	"opencode":       "",
	"commandcode":    "its stream is forced and inspected by CommandCodeDo on the commandcode wire, which is unwired",
	"openai": "the fold refuses a stream field it cannot carry (logprobs, annotations), which api.openai.com " +
		"answers on a non-stream request; forcing it would turn a request that works into a 502",
	"zed":         "",
	"opencode-go": "",
}

func TestForcedStreamProvidersAreExactlyAccountedFor(t *testing.T) {
	for id, reason := range pinForcedStream {
		provider := routing.ProviderRef{ProviderID: id}
		if id == "opencode-go" {
			provider.Protocol, provider.UpstreamModel = responses, "gpt-5.6-luna"
		}
		forced := ForcesStream(provider)
		switch {
		case reason == "" && !forced:
			t.Errorf("%s: the pin forces its stream and nothing here does", id)
		case reason != "" && forced:
			t.Errorf("%s is forced although it is recorded as not forced: %s", id, reason)
		}
	}
	for _, id := range Registered() {
		if _, accounted := pinForcedStream[id]; !accounted && For(id).ForceStream && id != "cursor" {
			// Cursor's Connect exchange is a stream by construction (wire/cursor.go).
			t.Errorf("%s forces its stream and the pin does not", id)
		}
	}
}

// A forced Responses provider rewrites only Responses bodies: the Chat and
// Messages bodies a conversion leg produced for another provider pass untouched.
func TestResponsesRewritesLeaveOtherWiresAlone(t *testing.T) {
	for _, id := range []string{"codex", "grok-cli"} {
		for _, protocol := range []string{chat, "anthropic-messages", "gemini"} {
			if got := finalize(t, id, protocol, `{"model":"m","messages":[],"stream":false}`); got != `{"model":"m","messages":[],"stream":false}` {
				t.Errorf("%s on %s: %s", id, protocol, got)
			}
		}
	}
}
