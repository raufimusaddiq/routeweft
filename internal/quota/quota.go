package quota

import (
	"errors"
	"math"
	"strings"
	"time"
)

// Status is the routing-relevant quota state (PRD-ROUTE-004). Unknown and Error
// are deliberately not exhaustion, so a failed read can never remove an account
// from rotation (PROVIDER_BASELINE §8).
type Status string

const (
	StatusAvailable Status = "available"
	StatusExhausted Status = "exhausted"
	StatusCooldown  Status = "cooldown"
	StatusUnknown   Status = "unknown"
	StatusError     Status = "error"
)

// Window is one normalized quota window. Remaining is the fraction still
// available in [0,1]; it is nil when the provider did not report a bound (for
// example an unlimited plan), which is Unknown rather than exhausted.
type Window struct {
	Name      string
	Remaining *float64
	ResetAt   *time.Time
	Unlimited bool
}

// State is one account's normalized quota snapshot.
type State struct {
	Status     Status
	Plan       string
	Windows    []Window
	ObservedAt time.Time
	Err        string
}

// PercentFromUsed converts a 0-100 used percentage into a nil-safe remaining
// fraction clamped to [0,1].
func PercentFromUsed(used float64) *float64 {
	if math.IsNaN(used) || math.IsInf(used, 0) {
		return nil
	}
	remaining := (100 - clamp(used)) / 100
	return &remaining
}

// RemainingPercent converts a remaining percentage into a remaining fraction.
func RemainingPercent(remaining float64) *float64 {
	if math.IsNaN(remaining) || math.IsInf(remaining, 0) {
		return nil
	}
	value := clamp(remaining) / 100
	return &value
}

// DeriveStatus classifies a snapshot from its windows at the given time.
// Exhausted requires a known zero remaining with a reset still in the future or
// unspecified; a window with no bound stays available/unknown, not exhausted.
func (s State) DeriveStatus(now time.Time) Status {
	if s.Status == StatusError {
		return StatusError
	}
	exhausted := false
	for _, window := range s.Windows {
		if window.Unlimited || window.Remaining == nil {
			continue
		}
		if *window.Remaining > 0 {
			continue
		}
		// A zero-remaining window whose reset has already passed no longer
		// constrains routing; the next read will refresh the real state.
		if window.ResetAt != nil && !window.ResetAt.After(now) {
			continue
		}
		exhausted = true
	}
	if exhausted {
		return StatusExhausted
	}
	if len(s.Windows) == 0 {
		return StatusUnknown
	}
	return StatusAvailable
}

// Exhausted reports whether the snapshot should keep the account out of
// selection at the given time.
func (s State) Exhausted(now time.Time) bool {
	return s.DeriveStatus(now) == StatusExhausted
}

// ErrNotConfigured means no provider usage client exists for the account.
var ErrNotConfigured = errors.New("provider does not expose a usage/quota endpoint")

func clamp(value float64) float64 {
	if value < 0 {
		return 0
	}
	if value > 100 {
		return 100
	}
	return value
}

// normalizeName keeps window labels stable and non-empty so UI/state keys do not
// shift between reads.
func normalizeName(name string) string {
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "quota"
	}
	return trimmed
}
