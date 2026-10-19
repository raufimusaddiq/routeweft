## Goal

Deliver the Sprint 6 auth/settings/backup item's database backup and restore
surface: CLI backup/restore-check moved onto a shared validated artifact package,
and session-gated `/admin/v1/backup` download plus restore check/activation
(PRD §16, §18; SPEC §22, §25; BDR-014).

## Non-goals

No UI, no write CRUD for other control resources, and no new schema or
migration. No LiteRouter/Redis/PostgreSQL dependency. Offline CLI restore still
validates only; activation is only offered by the running server so two writers
never contend for one SQLite file.

## Contract references

- PRD: §16 (backup/restore in the control API), §18 (persistence/backup/standalone), PRD-DATA-003
- SPEC: §22 (versioned `/admin/v1` resources, bounded pagination), §24 (health/shutdown), §25 (backup/restore protocol), §26 (CLI surface)
- BDR: BDR-004, BDR-007, BDR-013, BDR-014, BDR-017
- RUNBOOK: §11 backup, §12 restore
- REQUIREMENTS_TRACEABILITY: Backup/restore; Admin/control API
- Sprint: Sprint 6, auth/settings/backup

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

New session-gated routes: `GET /admin/v1/backup` streams a zip of the validated
artifact (database + `meta.json`); `POST /admin/v1/backup/restore/check`
validates an uploaded archive without activation; `POST /admin/v1/backup/restore`
validates and activates it. `routeweft backup` and `routeweft restore --check`
now share the same `internal/backup` validation used by the API, so all entry
points produce and accept byte-compatible artifacts.

## Storage / migration impact

- [x] No schema/migration impact.

Backups continue to be produced by SQLite's online backup API, integrity-checked,
switched to rollback-journal mode, and published without overwriting an existing
file. Activation stages the candidate in a private directory, preserves a
pre-restore rollback database, renames the validated candidate over the live
file after removing stale `-wal`/`-shm` sidecars, then recompiles state.

## Security impact

- [x] Security-boundary impact documented and tested.

Backup/restore routes require the admin session and same-origin policy. Uploads
are bounded (8 GiB cap, per-entry cap, exact-size check) and only the two expected
archive entries are accepted; path traversal, duplicate, oversized, and
directory entries are rejected. Artifacts and staging directories use 0600/0700
permissions. The public 128 MiB inference body limit no longer caps admin paths,
while admin uploads stay bounded by their own limit. Malformed candidates never
touch the live database.

## Performance impact

- [x] No inference hot-path impact.

The only request-path change is that the body-limit middleware now applies just to
inference paths, matching its purpose and previous behavior for those paths.
Backup streams from disk; restore is an operator action that deliberately pauses
serving.

## Tests

Passed: `gofmt -l internal cmd`; `go build ./...`; `go vet ./...`;
`go test -race -count=1 ./...`; `git diff --check`; UI `npm ci`,
`npm run typecheck`, `npm run build`.

New tests cover: validated artifact creation with metadata and no-overwrite;
candidate staging/discard and rejection of foreign/mismatched candidates;
zip download shape and restore check/apply gating plus foreign-archive rejection;
rollback preservation and snapshot recompilation after activation; end-to-end
`Serve` restore-and-resume; quiescence refusal of new work; and admin-upload
exemption from the inference body limit.

Also fixed a pre-existing `telemetry.FlushNow` shutdown race: it now returns
when the service has already drained and closed, instead of blocking a
cancelled-context flush after `Run` exited.

## Rollback

Revert this PR. No schema change. Routes and the shared package are additive;
reverting restores the PR44 control surface with CLI-only backup/validation.

## Review discipline

- [x] This is a coherent head ready for review.
- [x] Prior findings were re-read and valid fixes were bundled. (New PR; no prior findings.)
- [x] I will wait for review of this exact head before pushing further changes.

## Worktree isolation

- [x] This PR was implemented in its dedicated feature worktree.
- [x] The worktree is bound to this PR's branch only.
- [x] Review fixes will be made in the same worktree.
