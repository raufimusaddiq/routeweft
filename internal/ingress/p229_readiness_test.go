package ingress

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/cursor"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/providers/wire"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// #229 pins what each of the five unavailable providers still lacks, so a
// readiness reason cannot claim less (or more) than the code does. Each test
// fails when the gap closes, which is the moment registry.notReady,
// unreachableProviders and PARITY 6.23 must be updated together.

// The fence is a routability decision: nothing leaves it without the owner's
// approval, and nothing joins it silently either.
func TestUnwiredIsExactlyTheOwnerFencedSet(t *testing.T) {
	want := map[string]bool{cursor.Protocol: true, "qoder": true, "grok-web": true, "perplexity-web": true}
	if len(unwired) != len(want) {
		t.Fatalf("ingress.unwired = %v, want %v; a routability change needs the owner's approval (#229)", unwired, want)
	}
	for id := range want {
		if !unwired[id] {
			t.Fatalf("%s left ingress.unwired; a routability change needs the owner's approval (#229)", id)
		}
	}
	for _, id := range []string{"cursor", "qoder", "typesafe", "grok-web", "perplexity-web"} {
		spec, ok := lookupBuiltin(id)
		if !ok {
			t.Fatalf("%s is not a built-in provider; providers are never removed (#229)", id)
		}
		if spec.Readiness.Status != registry.ProviderUnavailable {
			t.Errorf("%s readiness = %q; promotion needs the owner's approval and serving evidence (#229)", id, spec.Readiness.Status)
		}
	}
}

// Cursor and Qoder have a hook Codec (#76 P02 part 3), so every client wire is
// convertible once the fence is lifted. The fence is not the only gap any more:
// readiness stays unavailable by owner decision (#229), and nothing routes while
// the entry is present.
func TestCursorAndQoderHaveAWholeIngressArmButStayFenced(t *testing.T) {
	for _, target := range []string{cursor.Protocol, "qoder"} {
		if !wire.HasCodec(target) {
			t.Fatalf("%s has no hook Codec", target)
		}
		for _, source := range clientWires {
			if CanTranslate(source, target) {
				t.Fatalf("%s is routable from %s while fenced", target, source)
			}
		}
	}
	liftFence(t, cursor.Protocol, "qoder")
	for _, target := range []string{cursor.Protocol, "qoder"} {
		for _, source := range clientWires {
			if !CanTranslate(source, target) {
				t.Errorf("%s is not reachable from %s through its codec", target, source)
			}
		}
	}
	var chat map[string]any
	if err := json.Unmarshal([]byte(`{"model":"m","messages":[{"role":"user","content":"hi"}]}`), &chat); err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{cursor.Protocol, "qoder"} {
		codec, ok, err := wire.CodecFor(routing.ProviderRef{ProviderID: target}, nil, chat, "m")
		if !ok || err != nil || codec.Endpoint == "" || len(codec.Body) == 0 || codec.Stream == nil {
			t.Errorf("%s codec: ok=%v err=%v codec=%+v", target, ok, err, codec)
		}
	}
}

func lookupBuiltin(id string) (registry.Spec, bool) {
	for _, spec := range registry.Builtins() {
		if spec.ID == id {
			return spec, true
		}
	}
	return registry.Spec{}, false
}

// Perplexity Web's readiness reason says it is served end to end through its
// Codec. #195 proved that for grok-web from every client wire, but for
// perplexity-web only for buffered Chat and Messages; this is the same proof,
// with the fence lifted for this test only.
func TestPerplexityWebIsServedToEveryClientWire(t *testing.T) {
	liftFence(t, "perplexity-web")
	for _, client := range clientWires {
		for _, stream := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/stream=%v", client, stream), func(t *testing.T) {
				up := newWebUpstream(t, 200, func(int) string { return pplxFixture("thread-1", "pong") })
				s := newWebServing(t, routing.ProviderRef{ProviderID: "perplexity-web", Protocol: "perplexity-web", BaseURL: up.URL, UpstreamModel: "pplx-auto", APIToken: "fixture-session", ConnectionID: "conn-a"}, runtime.NewState())
				w := s.call(client, stream)
				if w.Code != 200 || !strings.Contains(w.Body.String(), "pong") {
					t.Fatalf("client got %d: %s", w.Code, w.Body)
				}
				got := up.seen[0]
				if got.path != "/rest/sse/perplexity_ask" || got.cookie != "__Secure-next-auth.session-token=fixture-session" || got.auth != "" || !strings.Contains(got.agent, "Chrome/130") {
					t.Errorf("upstream saw %+v", got)
				}
				if query, _ := got.body["query_str"].(string); !strings.Contains(query, "ping") {
					t.Errorf("envelope %v", got.body)
				}
				outcomes := s.outcomes()
				if len(outcomes) != 1 || outcomes[0].Status != 200 || outcomes[0].ProviderID != "perplexity-web" {
					t.Fatalf("outcomes %+v", outcomes)
				}
				if !outcomes[0].UsageUnobserved || outcomes[0].InputTokens != 0 || outcomes[0].OutputTokens != 0 {
					t.Errorf("an estimate was recorded as measured usage: %+v", outcomes[0])
				}
			})
		}
	}
}
