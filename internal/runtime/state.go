package runtime

import (
	"sync"
	"time"
)

// RuntimeState contains mutable process-local routing and health state. It is
// never embedded in or published through RuntimeSnapshot.
type RuntimeState struct {
	mu        sync.Mutex
	cursors   map[string]uint64
	cooldowns map[string]time.Time
	quotas    map[string]QuotaObservation
}

// QuotaObservation is a memory-first provider/account quota observation.
type QuotaObservation struct {
	Remaining  *float64
	ResetAt    time.Time
	ObservedAt time.Time
	Err        string
}

func NewState() *RuntimeState {
	return &RuntimeState{cursors: make(map[string]uint64), cooldowns: make(map[string]time.Time), quotas: make(map[string]QuotaObservation)}
}

// NextCursor atomically returns a provider-local monotonically increasing RR
// cursor. The lock covers only one map operation; no network or storage work.
func (s *RuntimeState) NextCursor(provider string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	value := s.cursors[provider]
	s.cursors[provider] = value + 1
	return value
}

// Cursor returns the current value without advancing it.
func (s *RuntimeState) Cursor(key string) uint64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cursors[key]
}

// CooldownUntil returns the active cooldown deadline, if any.
func (s *RuntimeState) CooldownUntil(account string, now time.Time) (time.Time, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	until, ok := s.cooldowns[account]
	if ok && !now.Before(until) {
		delete(s.cooldowns, account)
		return time.Time{}, false
	}
	return until, ok
}

// Cooldown marks an account unavailable until the supplied deadline.
func (s *RuntimeState) Cooldown(account string, until time.Time) {
	if account == "" || until.IsZero() {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if old := s.cooldowns[account]; until.After(old) {
		s.cooldowns[account] = until
	}
}

// ObserveQuota replaces one account's latest quota observation.
func (s *RuntimeState) ObserveQuota(account string, observation QuotaObservation) {
	if account == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.quotas[account] = observation
}

// ObserveQuotaResult is the quota-package publisher contract: one normalized
// observation per account, memory-first, with no synchronous persistence
// (SPEC §6, PRD-QUOTA-001).
func (s *RuntimeState) ObserveQuotaResult(account string, remaining *float64, resetAt time.Time, observedAt time.Time, errText string) {
	s.ObserveQuota(account, QuotaObservation{Remaining: remaining, ResetAt: resetAt, ObservedAt: observedAt, Err: errText})
}

// Quota returns the latest account observation.
func (s *RuntimeState) Quota(account string) (QuotaObservation, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.quotas[account]
	return value, ok
}
