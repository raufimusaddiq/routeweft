## Goal

Resolve native dispatch endpoints per transport so multi-transport providers post to the path their protocol actually serves, instead of reusing one shared default path. Native fast paths remain native (BDR-010); only the target path is refined.

## Non-goals

No provider-specific wire adapters, no per-transport auth/header policy, no model-discovery URLs, no proxy-pool transport keying, and no operator UI. Providers with a single shared path are unchanged. Quota/usage endpoints and import/refresh flows remain in their assigned slices.

## Contract references

- PRD: provider transport requirements; PRD-API-005 request behavior
- SPEC: §9 native route selection, §11 provider modules (base endpoint rules), §20 transport pooling
- BDR: BDR-009, BDR-010
- Provider baseline: the multi-transport rows (`kenari`, `github`, `kimi`, `xiaomi-mimo`) whose Chat and Messages endpoints are distinct; base paths cross-checked against the cited reference snapshot
- Sprint: Sprint 5, per-transport endpoint resolution slice

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

`Spec` gains `TransportEndpoints` (`map[Protocol]string`), and the catalog exposes `EndpointFor`. Validation rejects paths that are absolute, contain a query/fragment, traverse (`.`/`..`), are blank, or name a transport the provider does not advertise. Dispatch consults the resolver on the native path only, falling back to each protocol's shared default when a provider declares no override. `kenari`, `github`, `kimi` and `xiaomi-mimo` now declare their real per-protocol paths; single-transport providers are untouched. The map is copied on catalog insert and lookup so callers cannot mutate published state.

## Storage / migration impact

- [x] No storage/schema/migration impact.

## Security impact

- [ ] No security-boundary impact.
- [x] Auth/secrets impact is documented and tested.

Endpoint overrides are validated at catalog-construction time against the same relative-path/no-traversal/no-query rules dispatch already enforces, so the new field cannot widen the outbound URL surface. No credentials are involved.

## Performance impact

- [x] No inference hot-path impact.

One map lookup per native attempt; no allocation or I/O added.

## Tests

`gofmt -l`; `go build ./...`; `go vet ./...`; `go test -race ./...`; `git diff --check`; UI `npm ci`, `npm run typecheck`, `npm run build`. All pass.

Registry tests cover per-protocol resolution for every affected provider, the shared-default fallback, unknown providers, and rejection of invalid overrides (absolute/traversal/query/blank/wrong-transport). The ingress test drives a real `/v1/chat/completions` request through the handler and asserts the upstream request lands on the provider-specific path.

## Rollback

Revert this PR to restore the single shared default path per protocol; the field is additive so no other behavior depends on it.

## Review discipline

- [x] Coherent head ready for review.
- [x] Prior findings re-read; none exist for this PR.
- [x] No new head while FIFO reviewer reviews this exact head absent blocker.

## Worktree isolation

- [x] Dedicated feature worktree.
- [x] Worktree bound to this branch only.
- [x] Review fixes remain in this worktree.
