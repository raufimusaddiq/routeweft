package ingress

import (
	"fmt"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/protocol/cursor"
	"github.com/raufimusaddiq/routeweft/internal/protocol/hub"
	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
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
	want := map[string]bool{cursor.Protocol: true, "grok-web": true, "perplexity-web": true}
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

// Cursor's legs are declared in the hub and wire.CursorDo can send, but the
// ingress translators have no cursor arm. Lifting the fence alone would make
// Cursor accounts candidates whose every attempt fails at the translation, so
// enablement is not the only thing missing.
func TestCursorFenceIsNotItsOnlyGap(t *testing.T) {
	liftFence(t, cursor.Protocol)
	for _, source := range clientWires {
		if !hub.CanTranslate(source, cursor.Protocol) || !CanTranslate(source, cursor.Protocol) {
			t.Fatalf("%s -> cursor legs are no longer declared", source)
		}
	}
	chat, err := openai.ParseChatRequest([]byte(`{"model":"claude-4.5-sonnet","messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	responses, err := openai.ParseResponsesRequest([]byte(`{"model":"claude-4.5-sonnet","input":"hi"}`), false)
	if err != nil {
		t.Fatal(err)
	}
	messages, err := anthropic.ParseMessagesRequest([]byte(`{"model":"claude-4.5-sonnet","max_tokens":5,"messages":[{"role":"user","content":"hi"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	const bound = "cursor now has an ingress arm: prove the whole path from every client wire, then update registry.notReady, unreachableProviders and PARITY 6.23"
	if _, err := TranslateChat(chat, cursor.Protocol); err == nil {
		t.Error("Chat: " + bound)
	}
	if _, err := TranslateResponses(responses, cursor.Protocol); err == nil {
		t.Error("Responses: " + bound)
	}
	if _, err := TranslateMessages(messages, cursor.Protocol); err == nil {
		t.Error("Messages: " + bound)
	}
}

// Qoder's request and answer halves are ported (#203), but no hook Codec hands
// them to dispatch, so no client wire reaches it even with no fence at all.
func TestQoderHasNoDispatchBindingYet(t *testing.T) {
	if wire.HasCodec("qoder") {
		t.Fatal("qoder has a hook Codec: prove the whole path, keep it fenced until the owner approves, and update registry.notReady, unreachableProviders and PARITY 6.23")
	}
	for _, source := range clientWires {
		if CanTranslate(source, "qoder") {
			t.Errorf("%s reaches qoder", source)
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
