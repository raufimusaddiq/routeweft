## Goal

Deliver Generic Provider runtime primitives as the first Sprint 5 provider-group increment (PRD §7, BDR-023, SPEC §33): transport capability validation, native endpoint resolution, safe model discovery, and manual model acceptance when discovery is unavailable.

## Non-goals

No provider catalog rows, control-plane CRUD/API/UI, credential encryption/persistence, auth refresh, quota, or inference dispatch wiring. Those belong to later provider-group/control-plane increments; no provider-specific behavior is invented here.

## Contract references

- PRD: PRD-GEN-001..004, PRD-PROV-002
- SPEC: §11, §33
- BDR: BDR-009, BDR-023
- Sprint: Sprint 5, ordered example group 1 foundation

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

internal/providers/generic models a provider node separately from API-key connections. Nodes advertise any subset of Chat Completions, Responses, and Messages. Endpoint resolves only declared source-matching transports to <base>/<route>; unsupported protocols return no binding. Validate checks required identity/name/prefix, supported unique transports, HTTP(S) URL shape, and central public/trusted-local SSRF policy.

DiscoverModels requests /models with the connection key, defaults to the shared DNS-guarded SSRF client, bounds response size, and returns unique non-empty IDs. Discovery failure does not prevent manual model setup: ValidateModel accepts any non-empty manual ID when discovery is unavailable/empty and checks membership when a catalog exists.

## Storage / migration impact

- [x] No storage/schema/migration impact.

Existing provider_nodes and provider_connections schema already represents the durable split. This PR adds in-memory domain primitives only.

## Security impact

- [ ] No security-boundary impact.
- [x] Auth/secrets/network/SSRF impact documented and tested.

Discovery sends only the supplied connection key as Bearer credentials to the validated provider URL. The key is never returned, logged, or included in errors. Public and explicit trusted-local modes use the corresponding dial-time DNS-guarded clients; redirects are disabled; response body capped at 2 MiB; URL query/userinfo/fragment rejected.

## Performance impact

- [x] No inference hot-path impact.

Control-path primitives only; no inference dispatch or synchronous DB work.

## Tests

go test ./...; go test -race ./...; go vet ./...; go build ./....

Coverage: native route selection, no binding for unadvertised/unknown protocols, duplicate/unsupported transport rejection, public/private URL policy, /models auth/path, duplicate/empty ID filtering, discovery HTTP/network/JSON/size failures, and manual-model acceptance without discovery.

## Rollback

Revert this PR to remove generic provider primitives. No durable state depends on them.

## Review discipline

- [x] Coherent head ready for review.
- [x] Prior findings re-read; valid fixes bundled.
- [x] No new head while FIFO reviewer reviews exact head absent blocker.

## Worktree isolation

- [x] Dedicated feature worktree.
- [x] Worktree bound to this branch only.
- [x] Review fixes remain in this worktree.
