## Goal

Register the no-auth/passthrough, hosted Ollama, local Ollama and SystemOne provider group (mimo-free, mmf, opencode, ollama, ollama-local, typesafe) with their approved transports, auth kinds and catalog semantics.

## Non-goals

No live model discovery, Ollama/SystemOne request dispatch wiring beyond existing adapters, thinking-format mapping, quota/usage, or OAuth/import workflows. `ollama-local` loopback access still requires the explicit trusted-local operator policy and is not enabled here.

## Contract references

- PRD: provider transport/auth/model catalog requirements
- SPEC: §9 native route selection, §11 provider identity, §20 transport/proxying
- BDR: BDR-009, BDR-010, BDR-022
- Provider baseline: `mimo-free`, `mmf`, `opencode`, `ollama`, `ollama-local`, `typesafe` rows
- Sprint: Sprint 5, group 6 no-auth/local slice

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

No-auth providers use `AuthNone`; dynamic providers set `PassthroughModels` so arbitrary IDs stay routable. Hosted Ollama uses Ollama transport with API-key auth; `ollama-local` uses Ollama transport with no auth and a loopback base URL. `typesafe` uses the SystemOne transport. Catalog test still asserts transport/base-URL completeness for every built-in.

## Storage / migration impact

- [x] No storage/schema/migration impact.

## Security impact

- [x] No security-boundary impact documented below.

`ollama-local` declares a loopback base URL; the existing SSRF policy still blocks private upstreams unless the operator explicitly enables trusted-local upstreams, so this PR does not weaken protection.

## Performance impact

- [x] No inference hot-path impact.

Catalog-only metadata.

## Tests

`gofmt -l`; `go test -race ./internal/providers/registry`; `go test -race ./...`; `go vet ./...`; `go build ./...`; `git diff --check`; UI `npm ci`, `npm run typecheck`, `npm run build`. All pass. Tests assert catalog membership plus no-auth/passthrough, Ollama hosted vs local, and SystemOne transport semantics; 50 of 80 baseline rows are now registered.

## Rollback

Revert this PR to remove these identities; no durable state depends on them.

## Review discipline

- [x] Coherent head ready for review.
- [x] Prior findings re-read; none exist for this PR.
- [x] No new head while FIFO reviewer reviews this exact head absent blocker.

## Worktree isolation

- [x] Dedicated feature worktree.
- [x] Worktree bound to this branch only.
- [x] Review fixes remain in this worktree.
