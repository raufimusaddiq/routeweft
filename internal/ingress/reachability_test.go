package ingress

import (
	"sort"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

// unreachableProviders are the providers a Chat, Responses or Messages client
// cannot reach, because the only wire they speak has no conversion leg yet. It
// is tracked work for #119, not an exclusion: the list only shrinks, and each
// provider leaves it in the PR that lands its wire's legs. The last four have no
// leg at the oracle either, so closing them is an adapter decision, not a leg.
var unreachableProviders = map[string]string{
	"antigravity":    "#119 Antigravity: legs landed, URL, auth and project id are the wire hook's (ingress.unwired)",
	"gemini-cli":     "#119 Gemini CLI: legs landed, URL, auth and project id are the wire hook's (ingress.unwired)",
	"vertex":         "#119 Gemini family: legs landed, URL and auth are #115 PR-C's (ingress.unwired)",
	"kiro":           "#119 Kiro",
	"cursor":         "#119 Cursor: legs landed, protobuf/h2 transport not built (ingress.unwired)",
	"commandcode":    "#119 CommandCode: legs landed, wire hook not bound (ingress.unwired)",
	"typesafe":       "no conversion leg exists at the pin: registry/typesafe.js:22 format \"systemone\" is not in translator/formats.js:2-15",
	"qoder":          "no conversion leg exists at the pin: registry/qoder.js:19-26 declares no format; its wire is executor-level (shared/qoder/sse.js)",
	"perplexity-web": "no conversion leg exists at the pin: registry/perplexity-web.js:21 format \"perplexity-web\" is not in translator/formats.js:2-15",
	"grok-web":       "no conversion leg exists at the pin: registry/grok-web.js:21 format \"grok-web\" is not in translator/formats.js:2-15",
}

// reaches reports whether a client in the given wire can be served by a provider
// speaking transports, natively or through the hub (runtime.AccountProvider's rule).
func reaches(client string, transports []registry.Protocol) bool {
	for _, transport := range transports {
		if string(transport) == client || CanTranslate(client, string(transport)) {
			return true
		}
	}
	return false
}

// Every built-in provider is reachable from every client wire the gateway
// converts, except those tracked above. This is the per-provider measure of the
// conversion work: a leg that lands but leaves a provider unroutable fails here.
func TestEveryProviderIsReachableOrTracked(t *testing.T) {
	clients := []string{openai.ChatProtocol, openai.ResponsesProtocol, anthropic.MessagesProtocol}
	reachable := map[string][]string{}
	found := map[string]string{}
	for _, spec := range registry.Builtins() {
		ok := true
		for _, client := range clients {
			if reaches(client, spec.Transports) {
				reachable[client] = append(reachable[client], spec.ID)
			} else {
				ok = false
			}
		}
		if !ok {
			found[spec.ID] = "unreachable"
		}
	}
	for id := range found {
		if _, tracked := unreachableProviders[id]; !tracked {
			t.Errorf("provider %s is unreachable from a converted client wire and is not tracked", id)
		}
	}
	for id, why := range unreachableProviders {
		if _, still := found[id]; !still {
			t.Errorf("provider %s is now reachable; remove it from unreachableProviders (%s)", id, why)
		}
	}
	for _, client := range clients {
		sort.Strings(reachable[client])
		t.Logf("%s clients reach %d of %d providers", client, len(reachable[client]), len(registry.Builtins()))
	}
	var missing []string
	for id := range unreachableProviders {
		missing = append(missing, id)
	}
	sort.Strings(missing)
	t.Logf("not reachable yet: %s", strings.Join(missing, ", "))
}
