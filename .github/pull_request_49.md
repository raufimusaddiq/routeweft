## Goal

Deliver Sprint 7 item 3: the Endpoint & Key page (PRD §14 Endpoint & Key) plus
the client-key mutations and `requireApiKey` setting it requires, all
session-gated under `/admin/v1` (PRD §16; SPEC §22).

## Non-goals

No Providers, Combo, System One, Usage, Quota, Token Saver, Console Log or
Settings page. No schema change: key lifecycle already had durable storage and
compiled-index primitives; this PR exposes them through the control API and UI.
No provider credential surfacing and no key-secret retrieval after creation.

## Contract references

- PRD §14 Endpoint & Key (base endpoint, transport examples, create/name/copy/
  pause/resume/revoke/delete client keys, require-API-key setting)
- PRD §15 `requireApiKey` default `true`
- PRD §16 + SPEC §22 versioned `/admin/v1`, mutation responses, bounded lists
- SPEC §4 client keys store only a one-way digest and short display prefix
- BDR-004/BDR-006/BDR-007 (SQLite durable, snapshot/state split, compile before
  commit), BDR-014 (versioned control API)
- REQUIREMENTS_TRACEABILITY: Admin/control API + UI shell/workflows
- Sprint: Sprint 7 item 3 (Endpoint & Key)

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

New routes: `POST /admin/v1/keys` returns the plaintext secret exactly once,
`PATCH /admin/v1/keys/{id}` pauses/resumes, and `DELETE /admin/v1/keys/{id}`
revokes; mutation responses return the active config revision (SPEC §22).
`requireApiKey` joins the writable settings allowlist and is validated as strict
`"true"`/`"false"` so a typo cannot silently disable enforcement. The
page shows the base endpoint with copy, protocol transport examples, the
enforcement toggle, and a paginated key list with create/copy-once/pause/resume/
revoke, explicit empty/loading/error states, and inline notices.

## Storage / migration impact

- [x] No schema/migration impact.

Keys continue to be persisted through the existing `api_keys` table and the
runtime update protocol; no new tables, columns or opaque JSON blobs.

## Security impact

- [x] Security-boundary impact documented and tested.

Every route requires an admin session and same-origin mutation, matching the
existing control API. The database stores only `auth.Hash(secret)`; tests assert
the stored digest, that the list read model never contains the plaintext, that a
paused or revoked key stops authenticating against the compiled index, and that
unknown key IDs return 404. The UI never fetches provider credentials and clears
the one-time secret from view when dismissed.

## Performance impact

- [x] No inference hot-path impact.

The request path still validates keys against the compiled `APIKeyIndex`; the new
routes only mutate and republish snapshots. Client bundle grows to 16.9 kB CSS /
166.6 kB JS (52.3 kB gzip).

## Tests

Go: `go build ./...`; `go vet ./...`; `gofmt`; `go test -race -count=1 ./...`.
New Go tests cover session/same-origin gating, create + one-time secret,
digest-only storage, list redaction, pause/resume, revoke (index and DB), unknown
ID 404, blank-name rejection, and the `requireApiKey` setting round trip incl.
invalid-value rejection.

UI: `npm run typecheck`; `npm run build`; `npm run smoke`. The Chromium smoke run
stubs the control API and drives the full page: base endpoint, six transport
examples, enforcement label/toggle, create → one-time secret shown then cleared,
pause → resume, revoke → empty state, and the resulting request counts.

## Rollback

Revert this PR. The key read model remains; the mutation routes, `requireApiKey`
writability and the page disappear. No schema or stored-data change.

## Review discipline

- [x] This is a coherent head ready for review.
- [x] Prior findings were re-read and valid fixes were bundled. (New PR; no prior findings.)
- [x] I will wait for review of this exact head before pushing further changes.

## Worktree isolation

- [x] This PR was implemented in its dedicated feature worktree.
- [x] The worktree is bound to this PR's branch only.
- [x] Review fixes will be made in the same worktree.
