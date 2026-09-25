// Package routing owns provider/account selection, strategies and fallback.
package routing

import (
	"errors"
	"sort"
	"strings"
)

// Strategy is a supported base account-selection strategy (PRD-ROUTE-002).
type Strategy string

const (
	StrategyFillFirst  Strategy = "fill-first"
	StrategyRoundRobin Strategy = "round-robin"
	StrategyStickyRR   Strategy = "sticky-round-robin"
)

// ParseStrategy validates operator input and defaults empty to fill-first.
func ParseStrategy(raw string) (Strategy, error) {
	switch Strategy(strings.TrimSpace(raw)) {
	case "", StrategyFillFirst:
		return StrategyFillFirst, nil
	case StrategyRoundRobin:
		return StrategyRoundRobin, nil
	case StrategyStickyRR:
		return StrategyStickyRR, nil
	default:
		return "", errors.New("unsupported strategy")
	}
}

// Account is one configured provider connection/account candidate.
type Account struct {
	ID       string
	Priority int
	Enabled  bool
}

// Selection describes the ordered candidate list for one request attempt plus
// the chosen sticky starting index.
type Selection struct {
	Candidates []Account
	Start      int
}

// SelectOptions carries the request-time inputs for account selection.
type SelectOptions struct {
	Provider    string
	Strategy    Strategy
	StickyLimit uint64
	Accounts    []Account
	// Excluded accounts already failed inside the current retry chain.
	Excluded map[string]struct{}
	// Eligible reports whether an account is not in cooldown/quota lock.
	Eligible func(Account) bool
	// Cursor is the provider-local round-robin cursor from RuntimeState.
	Cursor uint64
}

// Select returns enabled, eligible accounts ordered by strategy. Fill-first
// keeps configured priority order; RR and sticky RR rotate the starting account
// while preserving relative order after it.
func Select(options SelectOptions) Selection {
	candidates := make([]Account, 0, len(options.Accounts))
	for _, account := range options.Accounts {
		if !account.Enabled || account.ID == "" {
			continue
		}
		if _, excluded := options.Excluded[account.ID]; excluded {
			continue
		}
		if options.Eligible != nil && !options.Eligible(account) {
			continue
		}
		candidates = append(candidates, account)
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Priority < candidates[j].Priority })
	if len(candidates) == 0 {
		return Selection{}
	}
	if options.Strategy == StrategyFillFirst {
		return Selection{Candidates: candidates}
	}
	start := 0
	if options.Strategy == StrategyRoundRobin {
		start = int(options.Cursor % uint64(len(candidates)))
	} else if options.Strategy == StrategyStickyRR {
		limit := StickyRotations(options.StickyLimit)
		start = int((options.Cursor / limit) % uint64(len(candidates)))
	}
	rotated := make([]Account, 0, len(candidates))
	rotated = append(rotated, candidates[start:]...)
	rotated = append(rotated, candidates[:start]...)
	return Selection{Candidates: rotated, Start: start}
}

// StickyRotations converts a sticky request limit into the number of requests
// served before advancing the sticky starting candidate.
func StickyRotations(limit uint64) uint64 {
	if limit == 0 {
		return 1
	}
	return limit
}
