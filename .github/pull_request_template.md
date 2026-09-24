## Goal

Add SQLite WAL storage/migration foundation and immutable RuntimeSnapshot publication, with readiness, versioned config mutation, and safe backup/restore validation.

## Non-goals

No inference endpoints/providers, routing, transforms, admin APIs/UI, telemetry workers, or restore activation.

## Contract references

- PRD: §18, §22
- SPEC: §3–5, §24–26, §28–30
- BDR: BDR-004, 006, 007, 017, 019, 021
- Sprint: Sprint 1, PR #4

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant protocol/routing/provider fixtures added or updated. (No protocol behavior changed.)
- [x] No retained behavior was silently simplified.

## Storage / migration impact

- [x] Storage impact is documented below and migration/rollback is tested.

Details: schema v1; transactional migrations; WAL, foreign keys, busy timeout; config revision transaction; online backup + metadata; restore validation uses a private candidate copy. Activation restore deferred. Rollback: revert code; retain DB/backup.

## Security impact

- [ ] No security-boundary impact.
- [x] Auth/secrets/network/SSRF impact is documented and tested.

Details: DB/backup/metadata files use mode 0600; parent data/staging directories 0700; existing backup paths are never overwritten; provider credential data is not logged. No auth/network behavior introduced.

## Performance impact

- [x] No inference hot-path impact.
- [ ] Hot-path impact includes before/after benchmark evidence.

Details:

## Tests

`go test ./...`; `go test -race ./...`; `go vet ./...`; `go build ./...`; UI `npm ci`, `npm run typecheck`, `npm run build`; Docker build + scratch-container health/migrate/backup/restore-check smoke. Tests cover fresh/restart migrations, failed migration rollback, compile/commit failures, concurrent readers, WAL/permissions, backup integrity/collision, metadata matching, candidate immutability, foreign DB rejection, readiness, defaults. Benchmark: same 16k synthetic writes, modernc 0.81–1.19s vs mattn 0.52–0.90s (directional; no inference path impact). Docker image 14,451,484 bytes; CGO disabled, no Node runtime.

## Rollback

Revert this PR to restore the prior executable scaffold. No released database compatibility commitment exists yet. Preserve any test/operator DB before changing binary; do not delete user data.

## Review discipline

- [x] This is a coherent head ready for review.
- [x] Prior findings were re-read and valid fixes were bundled. (No PR #4 review yet.)
- [x] I will not push a new head while the FIFO reviewer is still reviewing this head unless a blocker requires it.


## Worktree isolation

- [x] This PR was implemented in its own dedicated feature worktree.
- [x] The worktree is bound to this PR's branch only.
- [x] Review fixes will be made in the same worktree.
