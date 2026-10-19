package runtime

import (
	"testing"
	"time"
)

func TestStateCursorCooldownAndQuotaAreSeparateFromSnapshot(t *testing.T) {
	state := NewState()
	if state.NextCursor("p") != 0 || state.NextCursor("p") != 1 {
		t.Fatal("provider cursor did not advance")
	}
	now := time.Now().UTC()
	state.Cooldown("account", now.Add(time.Minute))
	if _, ok := state.CooldownUntil("account", now); !ok {
		t.Fatal("active cooldown missing")
	}
	if _, ok := state.CooldownUntil("account", now.Add(2*time.Minute)); ok {
		t.Fatal("expired cooldown remained active")
	}
	remaining := 0.25
	state.ObserveQuota("account", QuotaObservation{Remaining: &remaining, ObservedAt: now})
	observation, ok := state.Quota("account")
	if !ok || observation.Remaining == nil || *observation.Remaining != remaining {
		t.Fatalf("quota %+v", observation)
	}
}
