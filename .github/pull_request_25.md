## Goal

Implement Codex Usage/quota normalization and reset-credit listing/consumption client: standard, review, spark windows; reset timestamps; available credits; account-scoped reset endpoints.

## Non-goals

No UI/admin action endpoint, durable quota persistence, scheduled polling, usage telemetry ingestion, or Codex dispatch integration. Reset-credit consumption is irreversible; this PR exposes a low-level client only and does not trigger it automatically. Operator confirmation belongs to the later control-plane workflow.

## Contract references

- PRD: Usage/Quota and provider-specific Codex reset actions
- SPEC: §11 provider modules, §19 credential handling
- BDR: BDR-009, BDR-022
- Provider baseline: `codex` row and §8 Usage/quota; behavior cross-checked against the cited reference snapshot
- Sprint: Sprint 5, group 2 (native OpenAI/Codex), Usage/reset-credit slice

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

Usage reports plan, normal/review/spark quota windows, limit flags and available reset credits. Reset-credit listing normalizes status and timestamps; consume validates request ID and reports outcome/no-credit separately. Non-2xx Usage is an error, never misreported as exhausted. Missing auth requires reauthorization. Requests use bounded response reads, authorization, Codex beta/originator headers, optional account ID, and the SSRF-protected client by default.

## Storage / migration impact

- [x] No storage/schema/migration impact.

## Security impact

- [ ] No security-boundary impact.
- [x] Auth/network impact is documented and tested.

OAuth access tokens are request-only and not returned/logged. Request/response errors avoid embedding credentials. Reset-credit consumption irreversibly spends a credit; caller must gate behind explicit operator confirmation.

## Performance impact

- [x] No inference hot-path impact.

Control-plane fetch helpers only.

## Tests

`gofmt -l`; `go test -race ./internal/providers/codex`; `go test -race ./...`; `go vet ./...`; `go build ./...`; `git diff --check`; UI `npm ci`, `npm run typecheck`, `npm run build`. All pass. Fixtures cover normal/review/spark windows, clamps/reset times, unavailable-vs-exhausted, account headers, reset-credit normalization, structured errors, consume body/result and missing-ID rejection.

## Rollback

Revert this PR to remove Codex Usage/reset-credit clients; OAuth credential core remains independent. No durable state created.

## Review discipline

- [x] Coherent head ready for review.
- [x] Prior findings re-read; none exist for this PR.
- [x] No new head while FIFO reviewer reviews this exact head absent blocker.

## Worktree isolation

- [x] Dedicated feature worktree.
- [x] Worktree bound to this branch only.
- [x] Review fixes remain in this worktree.
