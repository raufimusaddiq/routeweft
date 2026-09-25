## Goal

Register the multi-transport API-key provider group (deepseek, glm, glm-cn, minimax, minimax-cn, xiaomi-tokenplan) with native OpenAI Chat + Anthropic Messages transports so source-matching requests skip translation.

## Non-goals

No per-transport endpoint resolution, usage/quota clients, OAuth/import workflows, header hooks, or request dispatch wiring beyond existing provider-header behavior. Dynamic/passthrough (kenari, mimo-free) and OAuth-capable (kimi, xiaomi-mimo) providers are separate slices. No model discovery.

## Contract references

- PRD: provider transport/model catalog requirements
- SPEC: §9 native-first route selection, §11 provider identity
- BDR: BDR-009, BDR-010, BDR-022
- Provider baseline: `deepseek`, `glm`, `glm-cn`, `minimax`, `minimax-cn`, `xiaomi-tokenplan` rows
- Sprint: Sprint 5, group 3-5 multi-transport slice

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

Each multi-provider advertises both transports in baseline order; `NativeBinding` returns the source-matching transport. DeepSeek keeps its cache-control quirk. Shared base URLs are provider origins; per-transport endpoints remain provider-module work.

## Storage / migration impact

- [x] No storage/schema/migration impact.

Provider metadata only.

## Security impact

- [x] No security-boundary impact.

No credentials, networking, or auth handling added.

## Performance impact

- [x] No inference hot-path impact.

Catalog-only metadata.

## Tests

`gofmt -l`; `go test -race ./internal/providers/registry`; `go test -race ./...`; `go vet ./...`; `go build ./...`; `git diff --check`; UI `npm ci`, `npm run typecheck`, `npm run build`. All pass. Tests assert catalog membership, dual-transport order, both native bindings, DeepSeek quirk, and glm-cn single transport.

## Rollback

Revert this PR to remove the multi-transport identities; no durable state depends on them.

## Review discipline

- [x] Coherent head ready for review.
- [x] Prior findings re-read; none exist for this PR.
- [x] No new head while FIFO reviewer reviews this exact head absent blocker.

## Worktree isolation

- [x] Dedicated feature worktree.
- [x] Worktree bound to this branch only.
- [x] Review fixes remain in this worktree.
