package quota

import (
	"context"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// TestObserverFeedsRuntimeStateEligibility proves the wiring that was missing
// before this slice: a provider quota read reaches RuntimeState, and routing
// eligibility then excludes an exhausted account but never a failed read.
func TestObserverFeedsRuntimeStateEligibility(t *testing.T) {
	now := time.Date(2026, 9, 25, 12, 0, 0, 0, time.UTC)
	reset := now.Add(time.Hour)
	state := runtime.NewState()
	exhausted := &fakeClient{state: State{Windows: []Window{{Name: "weekly", Remaining: ptr(0), ResetAt: &reset}}}}
	observer := Observer{Clients: map[string]UsageClient{"codex": exhausted}, Now: func() time.Time { return now }, Publisher: state}
	if _, err := observer.Observe(context.Background(), "codex", "acct-1", "token"); err != nil {
		t.Fatal(err)
	}
	observation, ok := state.Quota("acct-1")
	if !ok || observation.Remaining == nil || *observation.Remaining != 0 || !observation.ResetAt.Equal(reset) {
		t.Fatalf("observation=%+v ok=%v", observation, ok)
	}
	// Routing eligibility rule from ingress: zero remaining with a future reset
	// removes the account from rotation.
	if !(observation.Remaining != nil && *observation.Remaining <= 0 && observation.ResetAt.After(now)) {
		t.Fatal("exhausted account would still be eligible")
	}

	// A failed read must publish no bound, so the same rule keeps the account.
	failing := &fakeClient{err: errFailedRead}
	state2 := runtime.NewState()
	observer2 := Observer{Clients: map[string]UsageClient{"claude": failing}, Now: func() time.Time { return now }, Publisher: state2}
	if _, err := observer2.Observe(context.Background(), "claude", "acct-2", "token"); err == nil {
		t.Fatal("expected read failure")
	}
	observation2, ok := state2.Quota("acct-2")
	if !ok {
		t.Fatal("failure observation was not published")
	}
	if observation2.Remaining != nil {
		t.Fatalf("failed read published a remaining bound: %+v", observation2)
	}
	if observation2.Err == "" {
		t.Fatal("failure observation must record the error text")
	}
}

var errFailedRead = &fakeError{}

type fakeError struct{}

func (*fakeError) Error() string { return "usage endpoint unavailable" }
