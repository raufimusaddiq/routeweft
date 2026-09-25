## Goal

Add the first-party Anthropic Messages API-key provider identity and its approved static catalog, API version and beta headers.

## Non-goals

No Claude OAuth/refresh/import, CLI fingerprint, usage/quota workflow, request dispatch integration, admin workflow, or UI. These remain baseline requirements for a separate Claude OAuth slice and are not dropped.

## Contract references

- PRD: provider/auth/model catalog requirements
- SPEC: §11 provider identity and native source-matching transport
- BDR: BDR-009, BDR-022
- Provider baseline: `anthropic` row; exact metadata from cited reference snapshot
- Sprint: Sprint 5, Anthropic/Claude group, first-party API-key slice

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

Registers `anthropic` as API-key + Anthropic Messages at `https://api.anthropic.com/v1`, static model IDs. Provider module exposes exact `anthropic-version: 2023-06-01` and `Anthropic-Beta: claude-code-20250219,interleaved-thinking-2025-05-14`. Registry identity remains separate from shared protocol adapter.

## Storage / migration impact

- [x] No storage/schema/migration impact.

## Security impact

- [x] No security-boundary impact.

No credentials or networking added.

## Performance impact

- [x] No inference hot-path impact.

Metadata/helpers only; dispatch integration not present in this slice.

## Tests

`gofmt -l`; `go test -race ./internal/providers/registry ./internal/providers/anthropic`; `go test -race ./...`; `go vet ./...`; `go build ./...`; `git diff --check`; UI `npm ci`, `npm run typecheck`, `npm run build`. All pass. Tests assert provider identity/catalog and exact headers; model/header accessors return defensive copies.

## Rollback

Revert this PR to remove first-party Anthropic identity; shared Messages protocol support remains independent.

## Review discipline

- [x] Coherent head ready for review.
- [x] Prior findings re-read; none exist for this PR.
- [x] No new head while FIFO reviewer reviews this exact head absent blocker.

## Worktree isolation

- [x] Dedicated feature worktree.
- [x] Worktree bound to this branch only.
- [x] Review fixes remain in this worktree.
