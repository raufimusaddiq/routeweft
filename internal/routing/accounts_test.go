package routing

import (
	"net/http"
	"testing"
	"time"
)

func testAccounts() []Account {
	return []Account{{ID: "c", Priority: 3, Enabled: true}, {ID: "a", Priority: 1, Enabled: true}, {ID: "off", Priority: 2, Enabled: false}, {ID: "b", Priority: 2, Enabled: true}}
}

func TestSelectFillFirstRespectsPriorityAndFilters(t *testing.T) {
	selection := Select(SelectOptions{Strategy: StrategyFillFirst, Accounts: testAccounts(), Excluded: map[string]struct{}{"b": {}}, Eligible: func(a Account) bool { return a.ID != "c" }})
	if len(selection.Candidates) != 1 || selection.Candidates[0].ID != "a" {
		t.Fatalf("selection %+v", selection)
	}
}

func TestSelectRoundRobinRotatesStablePriorityOrder(t *testing.T) {
	for cursor, want := range []string{"a", "b", "c"} {
		got := Select(SelectOptions{Strategy: StrategyRoundRobin, Accounts: testAccounts(), Cursor: uint64(cursor)})
		if len(got.Candidates) != 3 || got.Candidates[0].ID != want {
			t.Fatalf("cursor %d selection %+v", cursor, got)
		}
	}
}

func TestStickyLimitHasFiniteProgression(t *testing.T) {
	if StickyRotations(0) != 1 || StickyRotations(4) != 4 {
		t.Fatal("invalid sticky limit handling")
	}
}

func TestStickyRoundRobinAdvancesOnlyAfterLimit(t *testing.T) {
	options := SelectOptions{Strategy: StrategyStickyRR, Accounts: testAccounts(), StickyLimit: 3}
	for cursor, want := range []string{"a", "a", "a", "b", "b", "b", "c", "c", "c", "a"} {
		options.Cursor = uint64(cursor)
		got := Select(options)
		if got.Candidates[0].ID != want {
			t.Fatalf("cursor %d first %s want %s", cursor, got.Candidates[0].ID, want)
		}
	}
}

func TestAttemptChainIsBoundedAndDoesNotRepeatExcludedCandidates(t *testing.T) {
	selection := Select(SelectOptions{Strategy: StrategyFillFirst, Accounts: testAccounts(), Excluded: map[string]struct{}{"b": {}}})
	chain := NewAttemptChain(selection)
	var got []string
	for {
		account, ok := chain.Next()
		if !ok {
			break
		}
		got = append(got, account.ID)
	}
	if len(got) != 2 || got[0] != "a" || got[1] != "c" || chain.Used() != 2 {
		t.Fatalf("attempts %v", got)
	}
	if _, ok := chain.Next(); ok {
		t.Fatal("attempt budget overflow")
	}
}

func TestClassifyStatusSeparatesClientFailuresAndQuota(t *testing.T) {
	if got := ClassifyStatus(http.StatusBadRequest, nil); got.Outcome != OutcomeTerminalClientError || got.AllowsFallback() {
		t.Fatalf("client classification %+v", got)
	}
	header := http.Header{"Retry-After": []string{"120"}}
	if got := ClassifyStatus(http.StatusTooManyRequests, header); got.Outcome != OutcomeQuotaLock || got.RetryAfterSeconds != 120 {
		t.Fatalf("quota classification %+v", got)
	}
	if got := ClassifyStatus(http.StatusBadGateway, nil); got.Outcome != OutcomeFallbackAccount || !got.AllowsFallback() {
		t.Fatalf("5xx classification %+v", got)
	}
}

func TestCooldownDeadlineHonorsResetAndRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	reset := now.Add(3 * time.Hour)
	got := CooldownDeadline(Classification{Outcome: OutcomeQuotaLock, RetryAfterSeconds: 20, ResetAt: reset.Format(time.RFC3339)}, now, time.Minute)
	if !got.Equal(reset) {
		t.Fatalf("reset deadline %s", got)
	}
	got = CooldownDeadline(Classification{Outcome: OutcomeQuotaLock, RetryAfterSeconds: 20}, now, time.Minute)
	if !got.Equal(now.Add(20 * time.Second)) {
		t.Fatalf("retry deadline %s", got)
	}
}
