## Goal

Register the remaining API-key gateway/aggregator providers: vertex-partner, tokenrouter, perplexity-agent, opencode-go, cloudflare-ai.

## Non-goals

No account-id/deployment path resolution, live model discovery, usage/quota clients, OAuth, or import workflows. azure is deferred because its base URL is operator-supplied and needs provider-specific data handling. Perplexity-web/grok-web cookie transports, Vertex native auth, and OAuth-capable providers are separate slices.

## Contract references

- PRD: provider transport/auth/model catalog requirements
- SPEC: §9 native route selection, §11 provider identity
- BDR: BDR-009, BDR-010, BDR-022
- Provider baseline: `vertex-partner`, `tokenrouter`, `perplexity-agent`, `opencode-go`, `cloudflare-ai` rows
- Sprint: Sprint 5, group 1-5 API-key gateway slice

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

tokenrouter/perplexity-agent are dynamic+passthrough so arbitrary IDs stay routable; perplexity-agent uses the Responses transport; opencode-go advertises all three native transports (Chat, Responses, Messages) with usage reporting and source-matching binding; cloudflare-ai keeps its account-scoped template origin for its provider module to resolve.

## Storage / migration impact

- [x] No storage/schema/migration impact.

## Security impact

- [x] No security-boundary impact.

No credentials or networking added.

## Performance impact

- [x] No inference hot-path impact.

Catalog-only metadata.

## Tests

`gofmt -l`; `go test -race ./internal/providers/registry`; `go test -race ./...`; `go vet ./...`; `go build ./...`; `git diff --check`; UI `npm ci`, `npm run typecheck`, `npm run build`. All pass. Tests assert passthrough/dynamic semantics, perplexity-agent Responses transport, opencode-go three-transport binding, and vertex-partner identity; 55 of 80 baseline rows are registered.

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
