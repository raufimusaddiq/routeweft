package routing

import "time"

// AttemptChain walks a bounded candidate list, advancing only on
// fallback-classified failures (SPEC §14-§15, PRD-ROUTE-003).
type AttemptChain struct {
	selection Selection
	budget    AttemptBudget
	used      int
}

// NewAttemptChain bounds one request to exactly the eligible candidate list.
func NewAttemptChain(selection Selection) AttemptChain {
	budget := AttemptBudget{Max: len(selection.Candidates)}
	return AttemptChain{selection: selection, budget: budget}
}

// Next returns the next candidate attempt. It reports false when the candidate
// list or the attempt budget is exhausted.
func (c *AttemptChain) Next() (Account, bool) {
	if !c.budget.Allow(c.used) || c.used >= len(c.selection.Candidates) {
		return Account{}, false
	}
	account := c.selection.Candidates[c.used]
	c.used++
	return account, true
}

// Used reports how many attempts have been consumed.
func (c *AttemptChain) Used() int { return c.used }

// CooldownDeadline returns the deadline for a classification, honoring
// Retry-After over generic backoff and any trusted reset timestamp.
func CooldownDeadline(classification Classification, now time.Time, defaultBackoff time.Duration) time.Time {
	if reset, ok := parseResetTimestamp(classification.ResetAt); ok && reset.After(now) {
		return reset
	}
	if classification.RetryAfterSeconds > 0 {
		return now.Add(time.Duration(classification.RetryAfterSeconds) * time.Second)
	}
	if classification.AllowsFallback() || classification.Outcome == OutcomeQuotaLock {
		return now.Add(defaultBackoff)
	}
	return time.Time{}
}

func parseResetTimestamp(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}
