## Goal

Add the durable provider credential layer Sprint 5 requires: a sealed-at-rest store for provider nodes/connections plus a memory-first rotating-credential registry with singleflight refresh, so OAuth/API-key/cookie connections have one durable commit path shared by refresh and import.

## Non-goals

No admin/control API or UI routes (Sprint 6), no provider-specific OAuth endpoints (they already live in `internal/providers/{codex,claude,...}` and plug in through the `Exchanger` hook), no request-path wiring of `ProviderResolver`, no proxy-pool CRUD, and no quota polling. This slice is the storage + refresh substrate only.

## Contract references

- PRD: §8 PRD-AUTH-001/002/003, §6 PRD-PROV-002, §7 PRD-GEN-002
- SPEC: §4 durable storage, §11 provider modules, §19 OAuth/rotating credentials, §20 transport, §35 provider import/action capability
- BDR: BDR-006 (RuntimeState separation), BDR-007 (compile before commit), BDR-009 (provider/protocol separation)
- PROVIDER_BASELINE: §5 connection workflows, §6 credential identity and refresh requirements
- REQUIREMENTS_TRACEABILITY: credentials/import/refresh row
- Sprint: Sprint 5, credentials slice

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

Connection secrets are stored as a versioned AES-256-GCM envelope keyed by an operator master key, so the SQLite file never contains a plaintext API key, refresh token or cookie. Editing a connection without supplying a secret preserves the stored blob; a refresh that yields no usable access token/cookie fails instead of writing an empty credential; a rotation that omits the refresh token keeps the previous durable one. Refresh is singleflight per connection and commits the rotated payload before success is visible to any caller. Import reuses the same durable path and rejects an identity that does not match the connection.

## Storage / migration impact

- [x] No storage/schema/migration impact.

Uses the existing `provider_nodes`, `provider_connections` and `credential_events` tables from schema v1. Secret material continues to live in the `secret_blob` column; only the encoding changes (plaintext JSON is now a sealed envelope).

## Security impact

Credentials are encrypted at rest with a random per-blob nonce (no nonce reuse), authenticated (tamper/truncation rejected) and never logged; `credential_events` records non-secret audit metadata only. The store refuses to construct without a valid 32-byte master key, and refuses to overwrite a durable credential with an empty payload. No new outbound network path is introduced in this PR.

## Performance impact

- [x] No inference hot-path impact.

Reads/writes are single short transactions with no network work inside a transaction (SPEC §4). The request-path resolver is opt-in and not wired into ingress in this PR.

## Tests

`gofmt -l internal cmd`; `go build ./...`; `go vet ./...`; `go test -race ./...` (all green, no FAIL); `git diff --check`; UI `npm ci`, `npm run typecheck`, `npm run build`. All pass.

New tests cover: seal round-trip + nonce freshness + tamper/truncation/version rejection; wrong-key-length rejection; sealed-at-rest persistence and read-back; edit preserving the stored secret; empty/unknown rotation rejection; routing-order listing; enable/delete; audit-event validation; singleflight with 8 concurrent callers (one exchange) and durable commit; refresh keeping the durable refresh token; empty-refresh and provider-error leaving durable state intact; import identity validation and audit; resolver with node identity vs compiled fallback; disabled/unknown connection rejection.

Concurrency evidence: refresh and import install (durable write + in-memory publish) share one per-connection install lock, so a stale rotation can never overwrite a newer import in storage or in the served credential. Regression tests cover the pre-write, post-write and post-publish windows, and are run under `-race -count`.

## Rollback

Revert the PR. No schema migration is involved, so an earlier binary reads the same tables (it will not understand sealed envelopes, but this slice does not change any data the earlier binary read).
