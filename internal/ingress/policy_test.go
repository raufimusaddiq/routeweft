package ingress

import (
	"context"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func TestPlanProviderAttemptsUsesOverridesAndCooldownEligibility(t *testing.T) {
	manager := newManager(t)
	if _, err := manager.Update(context.Background(), func(candidate *runtime.Candidate) error {
		candidate.Set("providerStrategies", `{"p":"round-robin"}`)
		candidate.Set("stickyRoundRobinLimit", "2")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	state := runtime.NewState()
	state.Cooldown("b", time.Now().Add(time.Minute))
	handler := New(manager, Options{State: state, Candidates: func(*runtime.RuntimeSnapshot, string) ([]routing.Account, bool) {
		return []routing.Account{{ID: "a", Enabled: true}, {ID: "b", Enabled: true}, {ID: "c", Enabled: true}}, true
	}})
	candidates, ok := handler.PlanProviderAttempts(mustSnapshot(t, manager), "p", "model-x", time.Now())
	if !ok || len(candidates) != 2 || candidates[0].ID != "a" || candidates[1].ID != "c" {
		t.Fatalf("eligible candidates %+v", candidates)
	}
	if handler.StrategyFor(mustSnapshot(t, manager), "p") != routing.StrategyRoundRobin {
		t.Fatal("provider override was ignored")
	}
	if handler.StickyLimit(mustSnapshot(t, manager)) != 2 {
		t.Fatal("compiled sticky limit was ignored")
	}
}

func mustSnapshot(t *testing.T, manager interface {
	Load() (*runtime.RuntimeSnapshot, error)
}) *runtime.RuntimeSnapshot {
	t.Helper()
	snapshot, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}
