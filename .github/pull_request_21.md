## Goal

Implement the first Codex credential slice: authorization URL generation, code exchange, and serialized refresh-token rotation that persists new credentials before exposing the access token. This is not the complete Codex provider group.

## Non-goals

No connection-store wiring, admin OAuth endpoints, Codex request dispatch/CLI fingerprint headers, review-model mapping, usage/quota/reset-credit API, browser action workflow, or at-rest encryption decision. These remain required by the approved provider baseline and must land in subsequent assigned Sprint 5 Codex work; they are not dropped. Persistence remains a caller-provided callback; durable storage integration belongs to its assigned sprint item.

## Contract references

- PRD: PRD-AUTH-001..003, §6
- SPEC: §19
- BDR: BDR-009
- Provider baseline: Codex OAuth row and cited behavioral reference snapshot
- Sprint: Sprint 5, group 2 (native OpenAI/Codex), credential-core slice

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

PKCE uses a cryptographically random verifier and S256 challenge. Token responses are bounded; concurrent refreshes share one operation; a rotated refresh token is persisted before success, while omission preserves the prior refresh token. Authentication rejection requires reauthorization.

## Storage / migration impact

- [x] No schema/migration impact.

The durable persistence contract is injected through a required callback. No credential-at-rest format or encryption policy is invented here.

## Security impact

- [ ] No security-boundary impact.
- [x] Auth/secrets/network impact is documented and tested.

OAuth token exchange/refresh added. Requests use caller context for exchange and bounded timeouts for refresh; shared refresh work survives an individual waiter's cancellation. Response bodies are capped, error strings do not expose token payloads, and refresh success requires persistence first. No credentials are logged or returned in errors.

## Performance impact

- [x] No inference hot-path impact.

OAuth operations are control-plane only.

## Tests

`gofmt -l`; `go test ./...`; `go test -race ./...`; `go vet ./...`; `go build ./...`; `git diff --check`; UI `npm ci`, `npm run typecheck`, `npm run build`. Tests cover PKCE/authorization URL, code exchange, rejection/malformed responses, concurrent singleflight refresh, durable rotation, and persistence failure without token exposure. All listed checks pass.

## Rollback

Revert this PR to remove Codex OAuth primitives. No durable schema or provider dispatch path depends on this core.

## Review discipline

- [x] Coherent head ready for review.
- [x] Prior findings re-read; none exist for this new PR.
- [x] No new head while FIFO reviewer reviews this exact head absent blocker.

## Worktree isolation

- [x] Dedicated feature worktree.
- [x] Worktree bound to this branch only.
- [x] Review fixes remain in this worktree.
