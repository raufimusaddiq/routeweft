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
	for _, tc := range []struct {
		outcome Outcome
		delay   time.Duration
	}{
		{OutcomeAuthRefreshRequired, 2 * time.Minute},
		{OutcomeRetrySameAccount, time.Second},
	} {
		classification := Classification{Outcome: tc.outcome}
		if !classification.AllowsFallback() || !CooldownDeadline(classification, now, time.Minute).Equal(now.Add(tc.delay)) {
			t.Fatalf("missing cooldown for %s", tc.outcome)
		}
		classification.RetryAfterSeconds = 15
		if !CooldownDeadline(classification, now, time.Minute).Equal(now.Add(15 * time.Second)) {
			t.Fatalf("Retry-After lost for %s", tc.outcome)
		}
	}
}

func TestClassifyErrorRefinesNormalizedKinds(t *testing.T) {
	cases := []struct {
		kind    string
		status  int
		outcome Outcome
	}{
		{"auth", http.StatusUnauthorized, OutcomeAuthRefreshRequired},
		{"quota", http.StatusForbidden, OutcomeQuotaLock},
		{"rate-limited", http.StatusTooManyRequests, OutcomeQuotaLock},
		{"overloaded", http.StatusServiceUnavailable, OutcomeFallbackAccount},
		{"context-length", http.StatusBadRequest, OutcomeTerminalClientError},
		{"invalid-input", http.StatusBadRequest, OutcomeTerminalClientError},
		{"not-found", http.StatusNotFound, OutcomeFallbackProvider},
	}
	for _, tc := range cases {
		if got := ClassifyError(tc.kind, tc.status); got.Outcome != tc.outcome {
			t.Fatalf("kind=%q outcome=%q want=%q", tc.kind, got.Outcome, tc.outcome)
		}
	}
	if got := ClassifyError("unknown", http.StatusBadGateway); got.Outcome != OutcomeFallbackAccount {
		t.Fatalf("unknown 5xx outcome=%q", got.Outcome)
	}
	if got := ClassifyError("", http.StatusBadRequest); got.Outcome != "" {
		t.Fatalf("empty kind should not classify, got %q", got.Outcome)
	}
}

// 402 is out-of-credit: the pin cools the account for two minutes and falls
// back (errorConfig.js:77, accountFallback.js:35-46).
func TestClassifyStatusPaymentRequiredFallsBackAndCools(t *testing.T) {
	got := ClassifyStatus(http.StatusPaymentRequired, nil)
	if got.Outcome != OutcomeFallbackAccount || !got.AllowsFallback() || got.RetryAfterSeconds != 120 {
		t.Fatalf("402 = %+v, want fallback-account for 120s", got)
	}
	header := http.Header{}
	header.Set("Retry-After", "45")
	if got := ClassifyStatus(http.StatusPaymentRequired, header); got.RetryAfterSeconds != 45 {
		t.Fatalf("402 with Retry-After = %+v, want 45s", got)
	}
}

// A 402 body that parses as a request fault (DeepSeek's wears invalid_request_error)
// must not end the chain: the status rule wins over those kinds only.
func TestClassifyErrorPaymentRequiredBeatsRequestScopedKinds(t *testing.T) {
	for _, kind := range []string{"invalid-input", "context-length"} {
		if got := ClassifyError(kind, http.StatusPaymentRequired); got.Outcome != OutcomeFallbackAccount || !got.AllowsFallback() {
			t.Errorf("402 %s = %+v, want fallback-account", kind, got)
		}
		if got := ClassifyError(kind, http.StatusBadRequest); got.Outcome != OutcomeTerminalClientError {
			t.Errorf("400 %s = %+v, must stay terminal", kind, got)
		}
	}
}
