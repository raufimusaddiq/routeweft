## Goal

Implement the Claude Code OAuth credential core: PKCE authorization URL, JSON token exchange, and singleflight refresh with durable persist-before-success. Register Claude's OAuth provider identity and approved static model IDs.

## Non-goals

No connection-store/admin OAuth workflow, request dispatch integration, provider-specific Claude CLI request header policy, token import, Usage/quota adapter, organization settings lookup, or UI. These remain baseline requirements for subsequent Claude/provider/control-plane slices and are not dropped. Persistence remains a caller-provided callback; no storage format or encryption policy is invented.

## Contract references

- PRD: provider/auth workflow requirements, PRD-PROV/PRD-AUTH
- SPEC: §11 provider identity, §19 OAuth/rotating credentials
- BDR: BDR-009, BDR-022
- Provider baseline: `claude` row; OAuth metadata and model IDs cross-checked against cited snapshot
- Sprint: Sprint 5, Anthropic/Claude group, Claude OAuth credential-core slice

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

PKCE uses a cryptographically random verifier and S256 challenge. Authorization-code exchange uses JSON body. Refresh serializes concurrent calls, preserves omitted rotated refresh token, persists before publishing token, requires reauth for invalid credentials, bounds response bodies, and redacts persistence errors. Provider identity uses shared Anthropic Messages transport and static IDs.

## Storage / migration impact

- [x] No schema/migration impact.

Durable credential persistence is an injected callback.

## Security impact

- [ ] No security-boundary impact.
- [x] Auth/secrets/network impact is documented and tested.

Uses the SSRF-protected client by default and bounded token calls. No secrets are logged or returned in errors. OAuth access/refresh tokens must remain server-side.

## Performance impact

- [x] No inference hot-path impact.

OAuth control-plane only.

## Tests

`gofmt -l`; targeted `go test -race ./internal/providers/claude ./internal/providers/registry`; `go test -race ./...`; `go vet ./...`; `go build ./...`; `git diff --check`; UI `npm ci`, `npm run typecheck`, `npm run build`. All pass. Fixtures cover PKCE/authorization/exchange, malformed/auth responses, concurrent refresh, durable rotation, and persistence failure without token exposure.

## Rollback

Revert this PR to remove Claude OAuth primitives and registry identity. Anthropic API-key provider remains independent. No durable state created by this package.

## Review discipline

- [x] Coherent head ready for review.
- [x] Prior finding re-read and fixed: refresh HTTP now honors caller cancellation/deadline; only the post-refresh durable commit uses a bounded detached context so rotated credentials are not lost. Added race tests for cancellation before refresh completion and commit after refresh completion.
- [x] No new head while FIFO reviewer reviews this exact head absent blocker.

## Worktree isolation

- [x] Dedicated feature worktree.
- [x] Worktree bound to this branch only.
- [x] Review fixes remain in this worktree.
