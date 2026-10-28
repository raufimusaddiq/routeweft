package quota

import (
	"encoding/json"
	"errors"
	"math"
	"strings"
	"time"
)

// Status is the routing-relevant quota state (PRD-ROUTE-004). Unknown and Error
// are deliberately not exhaustion, so a failed read can never remove an account
// from rotation (PROVIDER_BASELINE §8). Cooldown is not derived from quota data:
// it is owned by routing classification (Retry-After / upstream status) and
// lives in RuntimeState alongside the quota observation.
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
// example an unlimited plan), which is Unknown rather than exhausted. A credit
// balance window carries Credit instead of a fraction: it is never unlimited,
// and its Remaining stays nil so one currency's balance cannot exhaust an
// account that holds credit in another.
type Window struct {
	Name string `json:"name"`
	// DisplayName is the provider's own label for the window where it reports
	// one; Name stays the stable key.
	DisplayName string     `json:"displayName,omitempty"`
	Remaining   *float64   `json:"remaining,omitempty"`
	ResetAt     *time.Time `json:"resetAt,omitempty"`
	Unlimited   bool       `json:"unlimited"`
	Credit      *Credit    `json:"credit,omitempty"`
	// ObservedAt is set only on a window a reader served from its own cache:
	// it is when that window was read upstream, earlier than the State's
	// ObservedAt, so a cached value is never presented as fresh.
	ObservedAt *time.Time `json:"observedAt,omitempty"`
	// Count is the absolute allowance where the provider reports one in its own
	// unit (GitHub Copilot entitlement/remaining). Each amount is nil when not
	// reported; a zero total is a reported zero, never unlimited.
	Count *Count `json:"count,omitempty"`
	// Scope is the provider's own scoping of the window (Claude weekly_scoped
	// limits name a model), kept so the label is not the only record of it.
	Scope *Scope `json:"scope,omitempty"`
	// LimitReached is the provider's own limit-reached flag for the window's
	// group (Codex). It is metadata: routing reads Remaining, as the
	// reference does.
	LimitReached bool `json:"limitReached,omitempty"`
	// Recurring is the provider's own refill flag where it reports one
	// (CodeBuddy): true resets at ResetAt, false expires there for good. nil is
	// not reported.
	Recurring *bool `json:"recurring,omitempty"`
	// Limit is the provider's own description of the window's limit (GLM type,
	// unit and number), kept as sent so the label is not the only record of it.
	Limit *Limit `json:"limit,omitempty"`
}

// Count is an absolute allowance. Amounts are in the provider's unit.
type Count struct {
	Used      *float64 `json:"used,omitempty"`
	Total     *float64 `json:"total,omitempty"`
	Remaining *float64 `json:"remaining,omitempty"`
}

// Scope is a provider-reported window scope.
type Scope struct {
	Kind  string `json:"kind,omitempty"`
	Model string `json:"model,omitempty"`
}

func (c *Count) clone() *Count {
	if c == nil {
		return nil
	}
	copied := *c
	for _, field := range []**float64{&copied.Used, &copied.Total, &copied.Remaining} {
		if *field != nil {
			value := **field
			*field = &value
		}
	}
	return &copied
}

// Limit is a provider-reported limit description. Unit and Number are the JSON
// values the provider sent, which are not always numbers.
type Limit struct {
	Type   string          `json:"type,omitempty"`
	Unit   json.RawMessage `json:"unit,omitempty"`
	Number json.RawMessage `json:"number,omitempty"`
}

func (l *Limit) clone() *Limit {
	if l == nil {
		return nil
	}
	copied := *l
	copied.Unit = append(json.RawMessage(nil), l.Unit...)
	copied.Number = append(json.RawMessage(nil), l.Number...)
	return &copied
}

func (s *Scope) clone() *Scope {
	if s == nil {
		return nil
	}
	copied := *s
	return &copied
}

// CreditState classifies one reported credit balance. Missing and malformed are
// not zero: they mean the amount was not observed.
type CreditState string

const (
	CreditPositive  CreditState = "positive"
	CreditZero      CreditState = "zero"
	CreditNegative  CreditState = "negative"
	CreditMissing   CreditState = "missing"
	CreditMalformed CreditState = "malformed"
)

// Credit is a prepaid balance in one currency. Amounts in different currencies
// are never summed or compared; each currency is its own window. Total is nil
// unless State is positive, zero or negative.
type Credit struct {
	Currency string      `json:"currency"`
	State    CreditState `json:"state"`
	Total    *float64    `json:"total,omitempty"`
	Granted  *float64    `json:"granted,omitempty"`
	ToppedUp *float64    `json:"toppedUp,omitempty"`
}

// clone deep-copies the credit so a cached snapshot cannot be mutated by a reader.
func (c *Credit) clone() *Credit {
	if c == nil {
		return nil
	}
	copied := *c
	for _, field := range []**float64{&copied.Total, &copied.Granted, &copied.ToppedUp} {
		if *field != nil {
			value := **field
			*field = &value
		}
	}
	return &copied
}

// State is one account's normalized quota snapshot. CreditAvailable is the
// provider's own account-level availability flag where it reports one (DeepSeek
// is_available); nil means not reported, which is neither available nor not.
//
// Message is a reader's fixed notice for an account the provider answered
// without quota (unsupported or permission-gated quota); it is never upstream
// text and never an error. Metadata holds provider-specific scalars the
// reference returns beside the windows (GitHub resetDate, Claude legacy
// organization). ExtraUsage is Claude's extra_usage object as the provider
// reported it (claude.js:131).
type State struct {
	Status          Status
	Plan            string
	Windows         []Window
	ObservedAt      time.Time
	Err             string
	AvailableReset  *float64
	CreditAvailable *bool
	Message         string
	Metadata        map[string]string
	ExtraUsage      json.RawMessage
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
// Cooldown is produced by routing classification, not by quota data, so this
// method never returns StatusCooldown; callers combine the two axes themselves.
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
