## Goal

Implement native first-party OpenAI provider identity: API-key auth, both OpenAI Chat and Responses transports, native source-protocol binding, first-party base URL, and static model IDs.

## Non-goals

Codex OAuth/PKCE, rotating credentials, CLI fingerprint headers, review-model mapping, Usage/quota/reset credits, and account-import workflows are a separate next PR. No Codex behavior is folded into this API-key provider change.

## Contract references

- PRD: PRD-PROV-001..002, §8 credential workflow boundaries
- SPEC: §11, §32
- BDR: BDR-009
- Sprint: Sprint 5, native OpenAI/Codex group (OpenAI API-key sub-PR)

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

`registry.NativeOpenAI` declares OpenAI with both OpenAI Chat and Responses transports. NativeBinding returns the source-matching transport, preserving native dispatch instead of translating. The base URL is `https://api.openai.com/v1`; static model IDs and labels are verified against the explicitly cited LiteRouter baseline reference snapshot.

## Storage / migration impact

- [x] No storage/schema/migration impact.

Provider metadata only.

CI retrigger note: no product change; this commit exists only to re-run the review check after a review-environment delivery failure.

## Security impact

- [x] No security-boundary impact.

No credentials, networking, or auth handling added beyond existing API-key connection support.

## Performance impact

- [x] No inference hot-path impact.

Catalog-only metadata.

## Tests

`go test ./...`; `go test -race ./...`; `go vet ./...`; `go build ./...`; UI `npm run typecheck`; `npm run build`.

Coverage: both native bindings, transport set, API-key auth, base URL, static catalog size, and defensive copies.

## Rollback

Revert this PR to remove first-party OpenAI identity metadata; no durable state depends on it.

## Review discipline

- [x] Coherent head ready for review.
- [x] Prior findings re-read; valid fixes bundled.
- [x] No new head while FIFO reviewer reviews this exact head absent blocker.

## Worktree isolation

- [x] Dedicated feature worktree.
- [x] Worktree bound to this branch only.
- [x] Review fixes remain in this worktree.
