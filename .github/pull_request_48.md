## Goal

Surface failed manual Overview refreshes while retaining the last good snapshot.
Follow-up to the valid non-blocking review suggestion on PR #47.

## Scope / non-goals

One Overview error state plus browser regression coverage. No API, auth, storage,
polling or other UI workflow changes.

## Contract references

- PRD §14 Overview: actionable operator errors
- UI_STYLE §10 explicit error state; §12 persistent error state is not replaced
  by toast-only reporting
- REQUIREMENTS_TRACEABILITY: UI browser smoke evidence
- Sprint 7, Overview; PR #47 review suggestion

## Behavior / compatibility

- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

When refresh fails after data has loaded, keep the stale summary visible and
show an inline alert with Retry. Initial-load failures keep their existing
blocking error state.

## Storage, security, performance

- [x] No schema/API/storage impact.
- [x] No security-boundary change.
- [x] No inference hot-path impact.

## Tests

Passed: `npm run typecheck`; `npm run build`; `npm run smoke`. Chromium smoke
forces the first refresh to fail, asserts error visibility plus stale data,
then retries and confirms recovery.

## Rollback

Revert this UI-only change.

## Review discipline

- [x] Coherent head; await review of this exact head before further pushes.
- [x] Valid prior PR #47 finding implemented and tested.
- [x] Dedicated feature worktree and branch.
