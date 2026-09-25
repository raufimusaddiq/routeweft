// Package quota normalizes provider usage endpoints into one routing-eligibility
// contract (PRD-ROUTE-004, PROVIDER_BASELINE §8).
//
// It owns no network code: provider packages already implement their usage
// endpoints (internal/providers/{codex,claude}). This package maps their output
// into a single State, feeds RuntimeState.ObserveQuota, and decides whether an
// account may still be selected. A quota read failure is never inferred as
// exhaustion.
package quota
