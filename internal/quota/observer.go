package quota

import (
	"context"
	"errors"
	"sort"
	"time"

	claudeprovider "github.com/raufimusaddiq/routeweft/internal/providers/claude"
	codexprovider "github.com/raufimusaddiq/routeweft/internal/providers/codex"
)

// Publisher is the RuntimeState surface the observer writes to. It is an
// interface so the quota package stays independent of runtime internals.
type Publisher interface {
	ObserveQuotaResult(account string, remaining *float64, resetAt time.Time, observedAt time.Time, errText string)
}

// UsageClient is one provider usage reader. Each first-class provider group
// supplies an adapter so quota normalization is written once.
type UsageClient interface {
	Usage(ctx context.Context, credential string) (State, error)
}

// Observer reads provider usage, normalizes it, and publishes one account's
// latest observation to RuntimeState (memory-first, no per-request SQLite).
type Observer struct {
	Clients   map[string]UsageClient
	Now       func() time.Time
	Publisher Publisher
}

// Observe fetches quota for one provider/account and publishes the result.
// A read failure is recorded as StatusError with Remaining unset, which routing
// treats as "not exhausted" (PROVIDER_BASELINE §8).
func (o Observer) Observe(ctx context.Context, providerID, account, credential string) (State, error) {
	client, ok := o.Clients[providerID]
	if !ok {
		return State{}, ErrNotConfigured
	}
	now := o.now()
	state, err := client.Usage(ctx, credential)
	if err != nil {
		state = State{Status: StatusError, ObservedAt: now, Err: err.Error()}
	}
	if state.ObservedAt.IsZero() {
		state.ObservedAt = now
	}
	state.Status = state.DeriveStatus(now)
	o.publish(account, state)
	return state, err
}

// publish reduces a multi-window snapshot to the single remaining/reset pair the
// routing eligibility check needs: the most constrained window wins, because an
// account is only usable while every window it is subject to has headroom.
func (o Observer) publish(account string, state State) {
	if o.Publisher == nil || account == "" {
		return
	}
	remaining, resetAt := constrained(state.Windows)
	o.Publisher.ObserveQuotaResult(account, remaining, resetAt, state.ObservedAt, state.Err)
}

func constrained(windows []Window) (*float64, time.Time) {
	var remaining *float64
	var resetAt time.Time
	ordered := append([]Window(nil), windows...)
	sort.SliceStable(ordered, func(i, j int) bool { return normalizeName(ordered[i].Name) < normalizeName(ordered[j].Name) })
	for _, window := range ordered {
		if window.Unlimited || window.Remaining == nil {
			continue
		}
		if remaining == nil || *window.Remaining < *remaining {
			value := *window.Remaining
			remaining = &value
			resetAt = time.Time{}
			if window.ResetAt != nil {
				resetAt = *window.ResetAt
			}
		}
	}
	return remaining, resetAt
}

func (o Observer) now() time.Time {
	if o.Now != nil {
		return o.Now()
	}
	return time.Now()
}

// CodexClient adapts the Codex usage endpoint to the normalized contract.
type CodexClient struct {
	Client    codexprovider.HTTPClient
	AccountID string
}

// Usage implements UsageClient for Codex.
func (c CodexClient) Usage(ctx context.Context, credential string) (State, error) {
	usage, err := (codexprovider.UsageClient{Client: c.Client, AccountID: c.AccountID}).Usage(ctx, credential)
	if err != nil {
		return State{Err: err.Error()}, err
	}
	windows := make([]Window, 0, len(usage.Quotas))
	for name, window := range usage.Quotas {
		reset := window.ResetAt
		windows = append(windows, Window{Name: normalizeName(name), Remaining: PercentFromUsed(window.Used), ResetAt: reset})
	}
	return State{Plan: usage.Plan, Windows: windows}, nil
}

// ResetCreditConsumer is implemented by providers that expose reset-credit
// actions (Codex today). It keeps the operator action representable without
// coupling the observer to that provider's wire types (PRD-QUOTA-001).
type ResetCreditConsumer interface {
	ConsumeResetCredit(ctx context.Context, credential, redeemRequestID string) (bool, error)
}

// CodexResetCredits adapts Codex reset-credit consumption.
type CodexResetCredits struct {
	Client    codexprovider.HTTPClient
	AccountID string
}

// ConsumeResetCredit implements ResetCreditConsumer for Codex.
func (c CodexResetCredits) ConsumeResetCredit(ctx context.Context, credential, redeemRequestID string) (bool, error) {
	result, err := (codexprovider.UsageClient{Client: c.Client, AccountID: c.AccountID}).ConsumeResetCredit(ctx, credential, redeemRequestID)
	if err != nil {
		return false, err
	}
	return result.OK, nil
}

// ClaudeClient adapts the Claude usage endpoint to the normalized contract.
type ClaudeClient struct {
	Client *claudeprovider.UsageClient
}

// Usage implements UsageClient for Claude.
func (c ClaudeClient) Usage(ctx context.Context, credential string) (State, error) {
	if c.Client == nil {
		return State{}, errors.New("claude usage client is required")
	}
	usage, err := c.Client.Usage(ctx, credential, false)
	if err != nil {
		return State{Err: err.Error()}, err
	}
	windows := make([]Window, 0, len(usage.Quotas))
	for name, window := range usage.Quotas {
		reset := window.ResetAt
		windows = append(windows, Window{Name: normalizeName(name), Remaining: RemainingPercent(window.RemainingPercentage), ResetAt: reset, Unlimited: window.Unlimited})
	}
	return State{Plan: usage.Plan, Windows: windows}, nil
}
