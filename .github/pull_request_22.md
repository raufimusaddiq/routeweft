## Goal

Add Codex CLI identity headers and deterministic review-model behavior to the Codex provider core: review variants route as their base upstream model while retaining review-family classification; `codex-auto-review` remains verbatim.

## Non-goals

No request-dispatch integration (the shared provider dispatch path is not implemented yet), Codex usage/quota/reset-credit client, connection/admin workflow, UI, or other provider groups. These remain required by the approved baseline and are not dropped.

## Contract references

- PRD: provider behavior and multi-account requirements
- SPEC: §11 provider identity/native transport, §19 credentials
- BDR: BDR-009, BDR-022
- Provider baseline: `codex` row; behavioral values cross-checked against its cited reference snapshot
- Sprint: Sprint 5, group 2 (native OpenAI/Codex), behavior slice

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

Exports exact `originator: codex_cli_rs` and `User-Agent: codex_cli_rs/0.154.0` headers. Model helper strips one `-review` suffix to select the upstream base and identifies review quota-family variants; `codex-auto-review` is explicitly preserved.

## Storage / migration impact

- [x] No storage/schema/migration impact.

## Security impact

- [x] No security-boundary impact.

No credentials or network behavior added.

## Performance impact

- [x] No inference hot-path impact.

Small pure helpers; dispatch integration awaits the assigned provider transport work.

## Tests

`gofmt -l`; targeted `go test -race ./internal/providers/codex ./internal/providers/registry`; `go test -race ./...`; `go vet ./...`; `go build ./...`; `git diff --check`; UI `npm ci`, `npm run typecheck`, `npm run build`. All pass. Fixtures cover exact headers, plain/review/stacked-suffix/auto-review IDs, and defensive header map ownership.

## Rollback

Revert this PR to remove the new Codex helper surface; the OAuth core remains independent.

## Review discipline

- [x] Coherent head ready for review.
- [x] Prior findings re-read; none exist for this PR.
- [x] No new head while FIFO reviewer reviews this exact head absent blocker.

## Worktree isolation

- [x] Dedicated feature worktree.
- [x] Worktree bound to this branch only.
- [x] Review fixes remain in this worktree.
