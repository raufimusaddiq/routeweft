## Goal

Add client API-key validation on the request path, the read-only public discovery routes, and the model-catalog/alias/custom/disabled compile primitives that later ingress PRs build on.

## Non-goals

No inference POST handlers, protocol adapters/translation, routing/fallback, providers, transforms, admin API, or UI.

## Contract references

- PRD: §5 (API-001/004/005), §9 (MODEL-001/002), §12 (SPEC §12 Client authentication)
- SPEC: §2, §5, §6, §7, §12, §27, §28, §34
- BDR: BDR-004, BDR-006, BDR-007, BDR-009, BDR-014, BDR-021
- Sprint: Sprint 2, PR #6

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant protocol/routing/provider fixtures added or updated.
- [x] No retained behavior was silently simplified.

Adds `GET /v1`, `GET /v1/models`, `GET /v1/models/info`, and `GET /v1/models/{provider}/{model...}`. Auth accepts Bearer, `x-api-key`, and Gemini-compatible `x-goog-api-key`/`?key=` forms; conflicting key forms are rejected. `requireApiKey=false` intentionally disables key checks per the settings contract. Protocol fixtures are unchanged; catalog/API cases get dedicated Go tests.

## Storage / migration impact

- [x] Storage impact is documented below and migration/rollback is tested.

Details: migration v2 adds `provider_models` for discovered/passthrough catalog rows and keeps `custom_models`/`model_aliases`/`disabled_models` normalized. `v1` databases upgrade in place (`TestV1DatabaseUpgradesWithoutLosingCatalogRecords`); API keys persist only a SHA-256 digest plus a short display prefix. Rollback: revert code and the additive v2 tables; existing data is untouched.

## Security impact

- [ ] No security-boundary impact.
- [x] Auth/secrets/network/SSRF impact is documented and tested.

Details: keys are never stored or returned in plaintext (only digest + prefix; creation returns the secret once). Key lookup is an in-memory hash index in `RuntimeSnapshot`, so validation never queries SQLite on the request path. CORS denies cross-origin requests unless an allowlist is configured; body limit defaults to 128 MiB. No outbound network path is introduced, so no SSRF surface is added or weakened.

## Performance impact

- [x] No inference hot-path impact.
- [ ] Hot-path impact includes before/after benchmark evidence.

Details: discovery endpoints are read-only snapshot lookups; key validation is `BenchmarkAPIKeyLookup` ~0.71-0.78 us/op, 176 B/op, 3 allocs/op (Xeon E5-2680 v4). No inference hot path exists yet.

## Tests

`go test ./...`; `go test -race ./...`; `go vet ./...`; `go build ./...`; UI `npm ci`, `npm run typecheck`, `npm run build`; `docker build` + scratch-container health/model-auth smoke; `go test ./internal/auth -bench BenchmarkAPIKeyLookup`. Coverage: key digest/prefix rules, Bearer/x-api-key/Gemini forms, conflicting-form rejection, revoked/paused keys, alias/custom/disabled compile + restart persistence, CORS allow/deny, body-limit rejection, request-ID echo, and concurrent catalog readers vs writers.

## Rollback

Revert this PR. Discovery routes and key management disappear; v2 tables are additive and can remain. Do not delete operator data.

## Review discipline

- [x] This is a coherent head ready for review.
- [x] Prior findings were re-read and valid fixes were bundled. (No PR #6 review yet.)
- [x] I will not push a new head while the FIFO reviewer is still reviewing this head unless a blocker requires it.

## Worktree isolation

- [x] This PR was implemented in its own dedicated feature worktree.
- [x] The worktree is bound to this PR's branch only.
- [x] Review fixes will be made in the same worktree.
