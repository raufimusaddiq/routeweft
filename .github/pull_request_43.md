## Goal

Deliver the Sprint 6 admin authentication/session and writable-settings foundation for `/admin/v1`, so no operator endpoint is exposed without an authenticated session (PRD-SEC-001, PRD-SEC-002, PRD §16).

## Non-goals

No remaining admin resource/read-model endpoints, backup/restore API, or UI. Those remain later Sprint 6/Sprint 7 work. No LiteRouter dependency or shared state.

## Contract references

- PRD: §16, PRD-SEC-001, PRD-SEC-002
- SPEC: §12 auth/session, §22 configuration mutation protocol, `/admin/v1` settings
- BDR: BDR-014
- RUNBOOK: one-time bootstrap admin environment configuration; protected admin API
- REQUIREMENTS_TRACEABILITY: Admin/control API
- Sprint: Sprint 6, admin auth/session and settings foundation

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

Adds PBKDF2-HMAC-SHA256 admin password hashing, transactional one-time bootstrap, in-memory expiring/revocable admin sessions, same-origin login/logout and mutation checks, login throttling, and session-gated `GET/PATCH /admin/v1/settings`. Settings mutations go through the Runtime Manager compile-before-commit-before-publish protocol. Bootstrap credentials are environment-only; an existing admin is never replaced by bootstrap values.

Settings responses redact secret-shaped fields and URL userinfo/query/fragment. Session cookies are HttpOnly, SameSite=Strict, Secure. Client throttling keys use the direct peer address; forwarded headers are not trusted.

## Storage / migration impact

- [x] No schema/migration impact.

Admin account data uses the existing SQLite schema; sessions are process-memory only. No migration added.

## Security impact

- [x] Security-boundary impact documented and tested.

Unauthenticated settings access is denied. Login and mutation routes reject cross-origin browser requests. Password hashes use constant-time verification; failed login attempts are throttled. Bootstrap credentials are not exposed through the API. Settings output is redacted before serialization. No credentials or session state are shared with LiteRouter.

## Performance impact

- [x] No inference hot-path impact.

Admin auth/settings work is outside successful inference serving configuration reads.

## Tests

Passed: `gofmt -l internal cmd`; `go build ./...`; `go vet ./...`; `git diff --check`; targeted `go test -race -count=1 ./internal/adminauth ./internal/control/api ./internal/runtime ./internal/app`; UI `npm ci`, `npm run typecheck`, `npm run build`.

The full `go test -race ./...` was attempted twice. Both attempts exposed unrelated existing timing flakes outside this PR: `internal/telemetry.TestRunFlushesOnShutdown` can block in `FlushNow` when cancellation wins the `Run` startup race; `internal/routing.TestFusionGraceCollectsStragglerAfterQuorum` intermittently omits a straggler. Each test passed in isolation (Fusion repeated 20 times); telemetry passed isolated. No files in those packages are changed here.

New tests cover PBKDF2 known vectors and failure cases, transactional bootstrap/provision-once, session expiry/revocation, login/logout/settings flow, unauthenticated/cross-origin rejection, throttling, settings redaction/validation, and app bootstrap wiring.

## Rollback

Revert this PR. No schema changes. If an admin was bootstrapped, retain the admin record; removing code must not be treated as account deletion or data cleanup.

## Review discipline

- [x] This is a coherent head ready for review.
- [x] Prior findings were re-read and valid fixes were bundled. (New PR; no prior findings.)
- [x] I will wait for review of this exact head before pushing further changes.

## Worktree isolation

- [x] This PR was implemented in its dedicated feature worktree.
- [x] The worktree is bound to this PR's branch only.
- [x] Review fixes will be made in the same worktree.
