## Goal

Deliver the Sprint 5 proxy-behavior requirements: durable proxy pools with per-connection binding, compiled `transport.ProxyPolicy` resolution, and a request path that uses one pooled client per connection instead of a single fixed client.

## Non-goals

No proxy admin API/UI (Sprint 6), no connectivity-test endpoint, no global-proxy settings mutation, and no SOCKS credential-management UI. Proxy rotation for providers that support it is represented through pool member selection; wiring it into per-provider account rotation is later work.

## Contract references

- PRD: §10 PRD-ROUTE-005 (global proxy, no-proxy, pools, per-connection binding, rotation, connectivity test)
- SPEC: §4 durable storage, §20 transport pooling and proxying
- BDR: BDR-008 (standard net/http), BDR-009 (provider/protocol separation)
- REQUIREMENTS_TRACEABILITY: provider/account routing row (proxy behavior)
- Sprint: Sprint 5, proxy-behavior slice

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

Proxy pools are now real durable records (`proxy_pool_members`, migration v4) rather than a dangling `proxy_pool_id`. Pool members are validated at write time (scheme + host) and de-duplicated. The compiled policy prefers a connection's bound pool member over the global proxy, falls back to the global proxy when the pool is empty or unbound, and disables proxying when nothing is configured. The ingress request path now resolves the outbound client per connection, so a bound pool is honored without building a transport per request; unbound connections keep the existing SSRF-protected default client.

## Storage / migration impact

- [x] Storage/schema/migration impact documented below.

Adds migration v4 (`proxy_pool_members`): one normalized row per proxy endpoint with position and enabled flag, unique per `(pool_id, proxy_url)`, and `ON DELETE CASCADE` from `proxy_pools`. Additive and backward-compatible; the previous binary ignores the new table.

## Security impact

- [x] Security-boundary impact documented below.

Proxy URLs are validated before persistence, and outbound requests still pass through the existing SSRF destination guard (`transport.destinationGuard` + dial-time checks) — configuring a proxy does not weaken private-network protections. Proxy credentials embedded in a URL are allowed (some pools require them) and are never logged; the no-proxy list and SSRF policy are unchanged.

## Performance impact

- [x] No inference hot-path impact.

Clients remain cached per material transport configuration via the existing `PooledClients`; the request path performs one map lookup by connection ID. No per-request transport construction.

## Tests

`gofmt -l internal cmd`; `go build ./...`; `go vet ./...`; `go test -race ./...` (29 packages ok, no FAIL); `git diff --check`; UI `npm ci`, `npm run typecheck`, `npm run build`. All pass.

New tests cover: pool member validation/ordering/de-duplication, replace-not-append semantics, delete/not-found, empty-pool listing, policy preference/fallback/disabled/explicit-pool-enables cases, round-robin and fill-first member selection including skipping disabled/blank members, and ingress per-connection client resolution with default fallback.

## Rollback

Revert the PR. Migration v4 is additive; if the schema is already applied, earlier binaries ignore the extra table and the request path returns to the single default client.
