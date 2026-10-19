## Goal

Add provider-level wire fixtures and replay tests for the Sprint 5 provider groups, so the compatibility oracle covers the newly registered first-class identities (dual-auth multi-transport, provider-specific dispatch paths, provider identity headers, passthrough streaming, usage-reporting gateways) rather than only the core protocol families.

## Non-goals

No new provider wire adapters or request dispatch behavior, no cline-envelope unwrapping, no usage/quota clients, and no live provider calls. Core protocol fixtures are unchanged. This slice only extends the deterministic fixture oracle and its replay assertions.

## Contract references

- PRD: PRD §6 provider requirements; PRD-API-005 request behavior
- SPEC: §9 native route selection, §10 protocol adapters, §11 provider modules, §28.3 contract fixtures, §29 CI gates (protocol fixture suite)
- BDR: BDR-009, BDR-010
- Provider baseline: §9 shared-adapter examples and per-provider row semantics (kimi/xiaomi-mimo/github/cline/codebuddy-cn/openrouter)
- REQUIREMENTS_TRACEABILITY: public API + provider rows' protocol-fixture evidence
- Sprint: Sprint 5, provider fixtures slice

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

Six synthetic fixtures plus replay tests assert: a dual-auth multi-transport provider (kimi) round-trips the shared Chat adapter byte-for-byte; a source-matching Messages route posts to the provider-specific path (xiaomi-mimo) and uses x-api-key; the Copilot Chat path carries the VS Code fingerprint and never forwards the client credential; the Cline request carries its referer/title pair on its own path; a passthrough gateway's vendor-prefixed stream relays byte-for-byte and terminates exactly once; and a usage-reporting gateway (codebuddy-cn) round-trips its provider path and user-agent. A coverage guard prevents silent deletion of these fixtures.

## Storage / migration impact

- [x] No storage/schema/migration impact.

## Security impact

- [x] No security-boundary impact documented below.

Fixtures contain synthetic content only (no real prompts, credentials, cookies, or provider responses); the existing fixture loader already rejects secret-shaped content, and a new test asserts no client credential is forwarded on the Copilot path.

## Performance impact

- [x] No inference hot-path impact.

## Tests

`gofmt -l`; `go build ./...`; `go vet ./...`; `go test -race ./...`; `git diff --check`; UI `npm ci`, `npm run typecheck`, `npm run build`. All pass.

## Rollback

Revert this PR to remove the provider-level fixtures and tests; the core protocol fixtures remain the oracle.

## Review discipline

- [x] Coherent head ready for review.
- [x] Prior findings re-read; none exist for this PR.
- [x] No new head while FIFO reviewer reviews this exact head absent blocker.

## Worktree isolation

- [x] Dedicated feature worktree.
- [x] Worktree bound to this branch only.
- [x] Review fixes remain in this worktree.
