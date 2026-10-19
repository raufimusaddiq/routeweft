## Goal

Implement the first built-in provider identity batch for Sprint 5: 32 OpenAI Chat Completions providers using API-key credentials, fixed base URLs, or baseline-declared model passthrough.

Deliver:
- declarative built-in Spec catalog entries;
- model catalog class, passthrough-model, usage-reporting, and provider-quirk metadata;
- `NewBuiltinCatalog` validated immutable index.

## Non-goals

No provider-specific model-name catalogs/discovery endpoints, control-plane workflow, provider-specific quota/reset parsers, OAuth/cookie/import flows, specialized transport, or runtime snapshot wiring. Those land with the corresponding later provider/model/control-plane increments. Excludes providers requiring operator-defined endpoint fields (Azure, Cloudflare, Vertex Partner) and multi-protocol/specialized/auth-family providers.

## Contract references

- PRD: PRD-PROV-001..004, PRD-PROV-002
- SPEC: §11, §32
- BDR: BDR-009
- Sprint: Sprint 5, first OA-chat/API-key catalog batch

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant provider identity tests added.
- [x] No retained behavior was silently simplified.

`registry.Builtins` adds 32 provider identities from PROVIDER_BASELINE. Default base URLs and existing transport/auth behavior were verified against the explicitly cited LiteRouter reference snapshot at commit 2ffb7922954112b30425cd487d686758e519397e; Routeweft retains no runtime dependency. Model catalog class and passthrough semantics follow the baseline. Groq/Vercel usage flags follow baseline; Alibaba Code/Token Plan cache-control quirks are represented explicitly for their later hooks.

`Spec.Validate` now requires a declared model catalog class and rejects invalid passthrough combinations/unknown quirks. Catalog construction and lookup defensively copy transport and quirk slices.

## Storage / migration impact

- [x] No storage/schema/migration impact.

Metadata only; no durable mutation.

## Security impact

- [x] No security-boundary impact.

No credentials, networking, or inference dispatch added. Base URLs are declarative metadata and are consumed later through central transport validation.

## Performance impact

- [x] No inference hot-path impact.

Catalog construction is control/configuration-path only.

## Tests

`go test ./...`; `go test -race ./...`; `go vet ./...`; `go build ./...`; UI `npm run typecheck`; `npm run build`.

Coverage: 32 expected IDs, API-key auth and Chat transport for every row, required base URLs, dynamic/passthrough and usage semantics, Alibaba cache-control quirk metadata, validation rejection, and defensive-copy behavior.

## Rollback

Revert this PR to remove the first built-in catalog batch. No durable state depends on it.

## Review discipline

- [x] Coherent head ready for review.
- [x] Prior findings re-read; valid fixes bundled.
- [x] No new head while FIFO reviewer reviews this exact head absent blocker.

## Worktree isolation

- [x] Dedicated feature worktree.
- [x] Worktree bound to this branch only.
- [x] Review fixes remain in this worktree.
