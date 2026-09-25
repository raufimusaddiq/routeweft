## Goal

Close the quota half of the Sprint 5 provider-group requirements: normalize provider usage endpoints into one routing-eligibility contract and actually publish it to `RuntimeState`, so the existing eligibility rule in ingress has a producer.

## Non-goals

No new provider usage endpoints, no quota admin API/UI (Sprint 6), no background polling scheduler, no persisted quota state, and no reset-credit UI. Provider HTTP clients already exist (`codex`, `claude`); this slice only normalizes and wires them.

## Contract references

- PRD: §10 PRD-ROUTE-004 (quota/cooldown/health states), §13 PRD-QUOTA-001
- SPEC: §6 RuntimeState, §14 cooldown and error classification
- BDR: BDR-006 (RuntimeState separation), BDR-009 (provider/protocol separation)
- PROVIDER_BASELINE: §8 usage/quota requirements
- REQUIREMENTS_TRACEABILITY: quota row
- Sprint: Sprint 5, quota/reset slice

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

Quota now distinguishes all five PRD-ROUTE-004 states: available, exhausted, cooldown, unknown and error. An account is exhausted only when a bounded window reports zero remaining with a reset still in the future; unlimited and unbounded windows never mark exhaustion, and a reset that has already passed stops constraining routing. A quota read failure is published as error with no remaining bound, so it is never inferred as exhaustion. Multi-window snapshots reduce to the most constrained window for eligibility. Reset-credit consumption remains representable through a provider-agnostic `ResetCreditConsumer`.

Production wiring: `App.Initialize` builds a `quota.Service` over the durable credential store/registry (using `ROUTEWEFT_CREDENTIAL_KEY`); `App.Serve` starts its background refresh loop and `App.RefreshQuota` is the operator-initiated entry point. `cooldown` is not derived from quota data — it stays owned by routing classification — so quota only produces available/exhausted/unknown/error and the routing layer combines that with the cooldown axis.

## Storage / migration impact

- [x] No storage/schema/migration impact.

Quota state is memory-first in `RuntimeState` (SPEC §6) and is never written per request.

## Security impact

- [x] No security-boundary impact documented below.

Usage reads reuse each provider's existing SSRF-protected client; this slice adds no new outbound path and no credential handling. No credential is logged or published.

## Performance impact

- [x] No inference hot-path impact.

The observer is invoked by existing provider usage paths, not by the request path. Publication is one mutex-guarded map write.

## Tests

`gofmt -l internal cmd`; `go build ./...`; `go vet ./...`; `go test -race ./...` (28 packages ok, no FAIL); `git diff --check`; UI `npm ci`, `npm run typecheck`, `npm run build`. All pass.

New tests cover all five status derivations, exhaustion edge cases (past reset, unbounded, unlimited), clamp/NaN handling of percent helpers, most-constrained-window publication, failure-is-not-exhaustion, unknown-provider rejection, exhausted-publishes-zero, and an end-to-end check that the observer feeds `RuntimeState` such that the existing ingress eligibility rule keeps failed-read accounts and drops exhausted ones.

Evidence command: `go test -race ./internal/quota/ ./internal/runtime/ -count=1` (both packages ok).

## Rollback

Revert the PR. No schema change and no persisted state, so an earlier binary is unaffected; quota observations simply stop being published.
