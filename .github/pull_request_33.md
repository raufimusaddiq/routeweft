## Goal

Register the final provider group: the ten Specialized-wire providers (antigravity, gemini-cli, cursor, qoder, kiro, vertex, commandcode, grok-web, perplexity-web) plus the remaining dual-auth/passthrough identities (grok-cli, kenari, zed, kimchi, azure), so all 80 active baseline rows are represented in the built-in catalog.

## Non-goals

No provider-specific wire adapters (antigravity/cursor/kiro/gemini-cli/commandcode/vertex/grok-web/perplexity-web/qoder request+stream codecs), no Google/AWS/RSA credential flows or refresh, no cookie/session capture UI, no import/auto-import helpers, no usage/quota clients for this group, and no operator connection CRUD for azure. These remain required baseline items for their assigned Sprint 5/6 slices and are not dropped.

## Contract references

- PRD: provider transport/auth/model catalog requirements and connection workflows (PRD-AUTH-001/002)
- SPEC: §9 native route selection, §11 provider identity, §19 credentials
- BDR: BDR-009, BDR-010, BDR-022
- Provider baseline: special-cased rows in PROVIDER_BASELINE §3 and the §2 Specialized transport legend; base URLs/auth/catalog classes cross-checked against the cited reference snapshot
- Sprint: Sprint 5, specialized-wire + remaining identities slice

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

The catalog now models provider-specific wire formats as first-class protocols (antigravity, gemini-cli, grok-web, perplexity-web, qoder, kiro, cursor, vertex, commandcode) so identity stays separate from shared adapters (BDR-009). Cookie/session providers (grok-web, perplexity-web) keep arbitrary current IDs routable; kenari advertises Chat+Responses+Messages so a source-matching route skips translation; grok-cli reuses the shared Responses transport; zed/kimchi declare passthrough catalogs; qoder and kiro carry their dual auth modes. `azure` keeps an intentionally empty seeded origin because its endpoint is operator-supplied per connection.

## Storage / migration impact

- [x] No storage/schema/migration impact.

## Security impact

- [x] No security-boundary impact documented below.

Catalog-only metadata plus protocol enum validation; no credential handling or new outbound network calls are added by this slice. Cookie/session providers store no secrets here.

## Performance impact

- [x] No inference hot-path impact.

## Tests

`gofmt -l`; `go build ./...`; `go vet ./...`; `go test -race ./...`; `git diff --check`; UI `npm ci`, `npm run typecheck`, `npm run build`. All pass.

Tests assert the full 80-row membership, each specialized provider's provider-specific transport, the cookie/passthrough and dual-auth semantics, kenari's three native bindings, and that azure's operator-supplied origin is preserved as empty while every other provider has a base URL. New protocol values are covered by the existing transport-validation test.

## Rollback

Revert this PR to remove these identities; no durable state depends on them. The protocol enum additions are inert without catalog entries that use them.

## Review discipline

- [x] Coherent head ready for review.
- [x] Prior findings re-read; none exist for this PR.
- [x] No new head while FIFO reviewer reviews this exact head absent blocker.

## Worktree isolation

- [x] Dedicated feature worktree.
- [x] Worktree bound to this branch only.
- [x] Review fixes remain in this worktree.
