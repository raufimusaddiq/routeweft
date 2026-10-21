package ingress

import (
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

// The registry's readiness is an explicit declaration; this ties it to what the
// gateway can actually do. A provider no converted client wire reaches must be
// declared unavailable, and an unavailable one must be unreachable, so adding a
// leg (or an unwired entry) without updating readiness fails here.
func TestReadinessMatchesIngressReachability(t *testing.T) {
	for _, spec := range registry.Builtins() {
		unreachable := false
		for _, id := range []string{"openai-chat", "openai-responses", "anthropic-messages"} {
			if !reaches(id, spec.Transports) {
				unreachable = true
			}
		}
		_, tracked := unreachableProviders[spec.ID]
		declared := spec.Readiness.Status == registry.ProviderUnavailable
		if unreachable != declared || tracked != declared {
			t.Errorf("%s: unreachable=%v tracked=%v readiness=%q; declare it in registry.notReady and unreachableProviders together",
				spec.ID, unreachable, tracked, spec.Readiness.Status)
		}
	}
}
