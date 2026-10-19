## Goal

Implement the Claude Usage/quota client: OAuth usage endpoint with 5-hour, 7-day and model-scoped weekly windows, plus legacy settings/organization fallback, token-scoped cache and 429 cooldown.

## Non-goals

No admin/control-plane endpoint, UI, durable quota persistence, scheduled polling, usage telemetry ingestion, or request dispatch integration. Legacy organization usage is surfaced as a raw provider-owned payload rather than a fabricated quota shape.

## Contract references

- PRD: Usage/Quota requirements
- SPEC: §11 provider modules, §13 quota visibility
- BDR: BDR-009, BDR-022
- Provider baseline: `claude` row (Usage/quota) and §8; behavior cross-checked against the cited snapshot
- Sprint: Sprint 5, Anthropic/Claude group, usage slice

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

OAuth-first usage normalizes utilization into used/remaining/resetAt for session, weekly and model-scoped windows, omitting absent windows instead of fabricating them. 429 cools down only the OAuth usage endpoint for three minutes while chat tokens remain usable; legacy settings/organization fallback still runs. Successful quota reads cache per token for five minutes. Responses are size-bounded and cancellation-aware.

## Storage / migration impact

- [x] No storage/schema/migration impact.

Cache and cooldown are in-memory runtime state only.

## Security impact

- [ ] No security-boundary impact.
- [x] Auth/network impact is documented and tested.

Access tokens stay in request headers and are never returned or logged. Uses the SSRF-protected client by default and bounded reads. No secrets included in normalized output.

## Performance impact

- [x] No inference hot-path impact.

Control-plane only; five-minute per-token cache prevents quota-endpoint hammering.

## Tests

`gofmt -l`; `go test -race ./internal/providers/claude`; `go test -race ./...`; `go vet ./...`; `go build ./...`; `git diff --check`; UI `npm ci`, `npm run typecheck`, `npm run build`. All pass. Fixtures cover window normalization, clamping, model-scoped limits, caching, 429 cooldown/expiry, legacy fallback and admin-access messaging, missing token, and oversized-body rejection.

## Rollback

Revert this PR to remove the Claude Usage client; OAuth credential core remains independent. In-memory cache/cooldown disappears with the process.

## Review discipline

- [x] Coherent head ready for review.
- [x] Prior findings re-read; none exist for this PR.
- [x] No new head while FIFO reviewer reviews this exact head absent blocker.

## Worktree isolation

- [x] Dedicated feature worktree.
- [x] Worktree bound to this branch only.
- [x] Review fixes remain in this worktree.
