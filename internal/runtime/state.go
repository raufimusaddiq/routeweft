package runtime

import (
	"sync"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/providers/wire"
)

// RuntimeState contains mutable process-local routing and health state. It is
// never embedded in or published through RuntimeSnapshot.
type RuntimeState struct {
	mu        sync.Mutex
	cursors   map[string]uint64
	rotations map[string]rotation
	cooldowns map[string]time.Time
	quotas    map[string]QuotaObservation
	wires     map[string]string
	codecs    *wire.CodecState
}

// QuotaObservation is a memory-first provider/account quota observation.
type QuotaObservation struct {
	Remaining  *float64
	ResetAt    time.Time
	ObservedAt time.Time
	Err        string
}

func NewState() *RuntimeState {
	return &RuntimeState{cursors: make(map[string]uint64), rotations: make(map[string]rotation), cooldowns: make(map[string]time.Time), quotas: make(map[string]QuotaObservation), wires: make(map[string]string), codecs: wire.NewCodecState()}
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

// rotation is one Combo's round-robin position: the start index and how many
// consecutive requests have used it.
type rotation struct {
	index int
	uses  uint64
}

// ComboRotation returns the start index for a list of length models and
// advances the Combo's position. It is the oracle's getRotatedModels state
// machine (open-sse/services/combo.js:208-237): the stored index is re-reduced by
// the current length, and it moves on only once sticky consecutive requests have
// used it, so a list that grows or shrinks between requests (an adapter model
// appearing) keeps the position the oracle would keep. A sticky limit below 1 is 1.
func (s *RuntimeState) ComboRotation(key string, length int, sticky uint64) int {
	if length <= 0 {
		return 0
	}
	if sticky < 1 {
		sticky = 1
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.rotations[key]
	current := state.index % length
	if state.uses+1 >= sticky {
		s.rotations[key] = rotation{index: (current + 1) % length}
	} else {
		s.rotations[key] = rotation{index: current, uses: state.uses + 1}
	}
	return current
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

func (s *RuntimeState) ClearCooldown(account string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cooldowns, account)
}

// ForgetAccount drops every process-local record kept for one account:
// cooldown and the latest quota observation. A deleted connection's ID is never
// reused, so this is hygiene and not correctness, but without it the maps grow
// by one entry per deleted connection for the life of the process.
func (s *RuntimeState) ForgetAccount(account string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.cooldowns, account)
	delete(s.quotas, account)
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

// PreferWire records that a provider's model is served on protocol after its
// first-choice wire refused it. The record lives as long as the process: it is
// never expired or cleared, and it is shared by every connection of the provider
// (the pin's GithubExecutor.knownCodexModels, executors/github.js:19,159, on an
// executor instance cached once per process, executors/index.js:78-86).
func (s *RuntimeState) PreferWire(provider, model, protocol string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wires[provider+"|"+model] = protocol
}

// PreferredWire returns the wire PreferWire recorded for the provider's model,
// or empty.
func (s *RuntimeState) PreferredWire(provider, model string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.wires[provider+"|"+model]
}

// CodecState is the state provider codecs keep between requests (Perplexity's
// account-scoped thread cache, #195). Like PreferWire it is process-local, bounded
// and never part of a snapshot.
func (s *RuntimeState) CodecState() *wire.CodecState { return s.codecs }
