## Goal

Implement Sprint 3 PR 11: provider/account routing policy and fallback foundation.

Deliver:

- declarative provider registry (identity/auth/transports, separated from protocol adapters);
- account selection for fill-first / round-robin / sticky round-robin plus `providerStrategies` overrides;
- bounded fallback attempt chain that advances only on fallback-classified failures;
- cooldown/error classification and memory-first RuntimeState (cursors, cooldowns, quota observations);
- pooled, proxy-aware outbound transports (global proxy, no-proxy, per-connection proxy, SSRF preserved).

## Non-goals

No Combo/Fusion/capability routing (PR 12/13), no client-side strategy UI, no admin API, no telemetry, and no provider-credential persistence/import/refresh. Account credential storage, encryption at rest, OAuth refresh and per-provider quota parsers remain Sprint 5 work; see "Deferred" below.

## Contract references

- PRD: §10 (PRD-ROUTE-001..005), §9
- SPEC: §6, §8–§14, §20, §28.1
- BDR: BDR-006, BDR-009, BDR-010
- Sprint: Sprint 3, PR #11
- Behavioral reference: LiteRouter `2ffb7922954112b30425cd487d686758e519397e` (account selection/cooldown/error classification and proxy handling)

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant protocol/routing/provider fixtures added or updated.
- [x] No retained behavior was silently simplified.

New packages/behavior:

- `internal/providers/registry`: immutable provider identity catalog with `Transports`, `Auth` and `NativeBinding(source)`, so a Multi provider binds a source-matching native transport before any translation (BDR-010).
- `internal/routing/accounts.go`: `Select` filters disabled/excluded/ineligible accounts, orders by priority, then applies fill-first, round-robin or sticky round-robin rotation from an in-memory cursor; `providerStrategies` overrides resolve per provider.
- `internal/routing/cooldown.go`: `ClassifyStatus` maps upstream status to success, retry-same-account, fallback-account, fallback-provider, terminal-client-error, auth-refresh-required or quota-lock. Only `fallback-*` outcomes advance the chain, so client errors cannot create provider storms.
- `internal/routing/fallback.go`: `AttemptChain` bounds one request to the eligible candidate list; `CooldownDeadline` prefers a trusted reset timestamp, then `Retry-After`, then bounded default backoff.
- `internal/runtime/state.go`: `RuntimeState` now holds RR cursors, cooldowns and quota observations with narrow locking only, and is never part of the immutable `RuntimeSnapshot` (BDR-006).
Hermes SSRF finding fix: proxy dial validation only sees the proxy address. `destinationGuard.RoundTrip` now validates the target URL and resolves/checks all destination IPs before handing the request to the proxy. `TestProxyClientRejectsPrivateDestinationBeforeProxyDial` verifies a loopback target is rejected without contacting a local proxy.

`internal/transport/pool.go`: `PooledClients` caches one `http.Client` per material transport configuration. `destinationGuard` validates the requested destination URL and every DNS answer before proxy dispatch; the dial guard independently validates the proxy's own destination. Proxy policy supports global/per-connection proxy and no-proxy wildcard/domain rules; proxy schemes are allowlisted.
- `internal/ingress/policy.go`: ingress consumes the above for strategy resolution, sticky limits, candidate eligibility and cooldown recording.

## Deferred (explicit, no silent narrowing)

The PRD/SPEC/Sprint documents place provider credential storage, import and refresh in Sprint 5 ("Provider breadth and credentials"), and SPEC §795 states provider secrets are configured through the control plane/import tooling rather than committed config. This PR therefore does not add a plaintext credential column or a hard-coded encryption key. Sprint 5 owns `provider_connections.secret_blob` encryption-at-rest, credential rotation and per-provider quota parsers. `providerStrategies` and sticky limits are already read from the durable `settings` row compiled into `RuntimeSnapshot`, so no new schema is required here.

## Storage / migration impact

- [x] No storage/schema/migration impact.

Details: no new tables or columns. Routing policy reads the compiled snapshot; cooldown/quota observations are deliberately memory-first in RuntimeState (SPEC §14) and do not write SQLite on the request path.

## Security impact

- [ ] No security-boundary impact.
- [x] Auth/secrets/network/SSRF impact is documented and tested.

Details: proxy support never disables SSRF validation; the dial guard still resolves and validates every destination, metadata addresses stay blocked and redirects stay disabled. Proxy URL schemes are restricted to HTTP(S)/SOCKS and malformed URLs are rejected at compile time. No provider secret is logged, returned, or added to the database in this PR.

## Performance impact

- [ ] No inference hot-path impact.
- [x] Hot-path impact includes before/after benchmark evidence.

Selection and classification are pure in-memory operations with narrow locks, and proxy clients are pooled rather than constructed per request. Existing hot-path benchmark remains the baseline; this PR adds no per-request SQLite read, filesystem read or config recomputation.

## Tests

`go test ./...`; `go test -race ./...`; `go vet ./...`; `go build ./...`; UI `npm ci`, `npm run typecheck`, `npm run build`. New coverage: priority ordering with disabled/excluded/ineligible filtering; RR rotation across cursors; sticky-limit progression; bounded attempt chain that cannot repeat excluded accounts; status classification separating terminal client errors from quota/fallback; reset-timestamp and Retry-After precedence; RuntimeState cursor/cooldown-expiry/quota visibility; provider catalog transport binding, defensive copies and duplicate/invalid rejection; proxy pool reuse, scheme rejection, wildcard and domain no-proxy rules.

## Rollback

Revert this PR; remove the routing policy, registry, RuntimeState additions and proxy pooling. No storage state to unwind and no ingress contract change is visible to clients.

## Review discipline

- [x] This is a coherent head ready for review.
- [x] Prior findings were re-read and valid fixes were bundled. (No review on this head yet.)
- [x] I will not push a new head while the FIFO reviewer is still reviewing this head unless a blocker requires it.

## Worktree isolation

- [x] This PR was implemented in its own dedicated feature worktree.
- [x] The worktree is bound to this PR branch only.
- [x] Review fixes will be made in the same worktree.
