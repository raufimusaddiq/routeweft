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
// provider leaves it in the PR that lands its wire's legs. The last four have no translator
// leg at the oracle either, so closing them is an adapter decision, not a leg. (Qoder's
// conversion is in the pin's executor, not absent; the entry says where.)
var unreachableProviders = map[string]string{
	"cursor":         "#76 P02: legs, Connect-RPC transport (wire.CursorDo), the legacy ChatService path for image-bearing turns (wire/cursor_chat.go) and a hook Codec landed; no transport gap remains, and the provider is fenced in ingress.unwired (TestCursorAndQoderHaveAWholeIngressArmButStayFenced, PARITY 6.23)",
	"typesafe":       "no conversion leg exists at the pin: registry/typesafe.js:22 format \"systemone\" is not in translator/formats.js:2-15",
	"qoder":          "no translator leg at the pin: registry/qoder.js:19-26 declares no format, so the target format is \"openai\" (services/provider.js:142-151) and executors/qoder.js:210-335 builds the agent envelope from the Chat body, :426-597 unwraps its SSE; #203 ported both and #76 P02 part 3 binds them through a hook Codec + QoderDo, still fenced (PARITY 6.22, 6.23)",
	"perplexity-web": "#195 Perplexity Web: hook Codec serves every client wire (the pin converts in the executor, no hub leg); fenced in ingress.unwired until a supervised live check",
	"grok-web":       "#195 Grok Web: hook Codec serves every client wire (the pin converts in the executor, no hub leg); fenced in ingress.unwired until a supervised live check",
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
