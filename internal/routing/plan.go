// Package routing turns a resolved model into a bounded dispatch plan.
// PR 7 ships the single-provider plan needed by the OpenAI Chat native path;
// multi-account strategies, fallback chains and Combo arrive in Sprint 3.
package routing

import (
	"errors"
	"strings"
)

// ProviderRef identifies one configured provider node and connection.
type ProviderRef struct {
	ProviderID    string
	Protocol      string
	BaseURL       string
	APIToken      string
	ConnectionID  string
	UpstreamModel string
}

// Plan is the immutable route decision for one request attempt.
type Plan struct {
	SourceProtocol string
	TargetProtocol string
	Provider       ProviderRef
}

// NativePath reports whether source and target share a wire protocol, so no
// canonical translation is required (SPEC 9.1/9.2).
func (p Plan) NativePath() bool { return p.SourceProtocol == p.TargetProtocol }

// PlanOptions are the request-scoped inputs required to build one plan.
type PlanOptions struct {
	SourceProtocol string
	TargetProtocol string
	Provider       ProviderRef
}

// BuildPlan validates the inputs and returns one bounded attempt.
func BuildPlan(options PlanOptions) (Plan, error) {
	if strings.TrimSpace(options.SourceProtocol) == "" {
		return Plan{}, errors.New("source protocol is required")
	}
	if strings.TrimSpace(options.Provider.ProviderID) == "" {
		return Plan{}, errors.New("provider is required")
	}
	if strings.TrimSpace(options.Provider.BaseURL) == "" {
		return Plan{}, errors.New("provider base URL is required")
	}
	target := options.TargetProtocol
	if target == "" {
		target = options.Provider.Protocol
	}
	return Plan{
		SourceProtocol: options.SourceProtocol,
		TargetProtocol: target,
		Provider:       options.Provider,
	}, nil
}

// AttemptBudget bounds retries/fallback for one request (SPEC section 8).
type AttemptBudget struct {
	Max int
}

// DefaultAttemptBudget keeps one native attempt until Sprint 3 adds fallback.
func DefaultAttemptBudget() AttemptBudget { return AttemptBudget{Max: 1} }

// Allow reports whether another attempt may run.
func (b AttemptBudget) Allow(used int) bool {
	if b.Max <= 0 {
		return false
	}
	return used < b.Max
}
