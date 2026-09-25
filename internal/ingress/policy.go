package ingress

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// CandidateResolver returns every configured account candidate for one
// requested model. A false result means the model itself is unknown, which is
// distinct from "known but all accounts are ineligible".
type CandidateResolver func(*runtime.RuntimeSnapshot, string) ([]routing.Account, bool)

// AccountProvider resolves one selected account into a concrete upstream
// provider reference.
type AccountProvider func(*runtime.RuntimeSnapshot, string, string) (routing.ProviderRef, bool)

// DefaultStickyLimit mirrors the compiled stickyRoundRobinLimit default.
const DefaultStickyLimit uint64 = 3

// PlanAttempts builds the ordered candidate list for one request from the
// immutable snapshot plus RuntimeState cooldown/quota observations
// (SPEC §13, PRD-ROUTE-002/004).
func (h *Handler) PlanAttempts(snapshot *runtime.RuntimeSnapshot, model string, now time.Time) ([]routing.Account, bool) {
	return h.PlanProviderAttempts(snapshot, modelProviderID(model), model, now)
}

// PlanProviderAttempts selects candidates with an explicit provider identity,
// required when model names are aliases or unqualified.
func (h *Handler) PlanProviderAttempts(snapshot *runtime.RuntimeSnapshot, providerID, model string, now time.Time) ([]routing.Account, bool) {
	if h.opts.Candidates == nil {
		return nil, false
	}
	accounts, ok := h.opts.Candidates(snapshot, model)
	if !ok {
		return nil, false
	}
	state := h.opts.State
	limit := h.StickyLimit(snapshot)
	cursor := uint64(0)
	strategy := h.StrategyFor(snapshot, providerID)
	if state != nil && strategy != routing.StrategyFillFirst {
		cursor = state.NextCursor(providerID + "|" + model)
	}
	selection := routing.Select(routing.SelectOptions{
		Strategy:    strategy,
		Accounts:    accounts,
		Cursor:      cursor,
		StickyLimit: limit,
		Eligible: func(account routing.Account) bool {
			if state == nil {
				return true
			}
			if _, cooling := state.CooldownUntil(account.ID, now); cooling {
				return false
			}
			if observation, ok := state.Quota(account.ID); ok && observation.Remaining != nil && *observation.Remaining <= 0 && observation.ResetAt.After(now) {
				return false
			}
			return true
		},
	})
	return selection.Candidates, true
}

func modelProviderID(model string) string {
	if provider, _, ok := strings.Cut(model, "/"); ok {
		return provider
	}
	return model
}

// Strategy returns the configured base strategy, honoring the global strategy
// and per-provider overrides. Invalid configuration falls back to fill-first
// rather than failing the request.
func (h *Handler) Strategy(snapshot *runtime.RuntimeSnapshot) routing.Strategy {
	settings := snapshot.Settings()
	if h.opts.Strategy != "" {
		return h.opts.Strategy
	}
	strategy, err := routing.ParseStrategy(settings["providerStrategy"])
	if err != nil {
		return routing.StrategyFillFirst
	}
	return strategy
}

// StrategyFor applies providerStrategies for one provider over the base policy.
func (h *Handler) StrategyFor(snapshot *runtime.RuntimeSnapshot, provider string) routing.Strategy {
	if overrides := providerStrategies(snapshot.Settings()["providerStrategies"]); overrides != nil {
		if raw, ok := overrides[provider]; ok {
			if parsed, err := routing.ParseStrategy(raw); err == nil {
				return parsed
			}
		}
	}
	return h.Strategy(snapshot)
}

// StickyLimit returns how many requests one sticky starting candidate serves
// before the cursor advances.
func (h *Handler) StickyLimit(snapshot *runtime.RuntimeSnapshot) uint64 {
	if h.opts.StickyLimit != 0 {
		return routing.StickyRotations(h.opts.StickyLimit)
	}
	if raw := snapshot.Settings()["stickyRoundRobinLimit"]; raw != "" {
		if parsed, err := strconv.ParseUint(raw, 10, 64); err == nil {
			return routing.StickyRotations(parsed)
		}
	}
	return routing.StickyRotations(DefaultStickyLimit)
}

func providerStrategies(raw string) map[string]string {
	if raw == "" {
		return nil
	}
	var parsed map[string]string
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return nil
	}
	return parsed
}

// recordClassification updates RuntimeState cooldown visibility from an
// upstream response. It is intentionally memory-first (SPEC §14).
func (h *Handler) recordClassification(account string, classification routing.Classification, now time.Time) {
	if h.opts.State == nil || account == "" {
		return
	}
	deadline := routing.CooldownDeadline(classification, now, 30*time.Second)
	if deadline.IsZero() {
		return
	}
	h.opts.State.Cooldown(account, deadline)
}

// classifyResponse is the shared classification entry point for ingress retries.
func classifyResponse(status int, header http.Header) routing.Classification {
	return routing.ClassifyStatus(status, header)
}
