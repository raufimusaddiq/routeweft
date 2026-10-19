## Goal

Add the first-party Gemini API-key provider identity: native Gemini GenerateContent transport, official base URL, API-key header name, and the approved static model catalog.

## Non-goals

No Google OAuth / Gemini CLI internal wire format / quota workflow, request dispatch integration, admin workflow, or UI. Google OAuth and CLI-specific behavior remain required and belong to the OAuth-specialized slice; no Google OAuth client credential is embedded in this PR.

## Contract references

- PRD: provider/auth/model catalog requirements
- SPEC: §11 provider identity and native source-matching transport
- BDR: BDR-009, BDR-022
- Provider baseline: `gemini` row; endpoint/header/model values from the cited reference snapshot
- Sprint: Sprint 5, Gemini/Google group, first-party API-key slice

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

Registers `gemini` as API-key + Gemini transport at the official `v1beta/models` base URL with 11 static model IDs. The provider module exposes `x-goog-api-key` as the API-key header name and deliberately returns no shared secret headers.

## Storage / migration impact

- [x] No storage/schema/migration impact.

## Security impact

- [ ] No security-boundary impact.
- [x] Auth/secrets impact is documented.

Only the API-key header name is declared; no credential value, OAuth client id/secret, or token is stored or returned. The reference snapshot's embedded Google OAuth client secret was intentionally not copied.

## Performance impact

- [x] No inference hot-path impact.

Metadata/helpers only.

## Tests

`gofmt -l`; `go test -race ./internal/providers/gemini ./internal/providers/registry`; `go test -race ./...`; `go vet ./...`; `go build ./...`; `git diff --check`; UI `npm ci`, `npm run typecheck`, `npm run build`. All pass. Tests assert identity, base URL, header name, catalog size, defensive copies, and absence of shared auth headers.

## Rollback

Revert this PR to remove first-party Gemini API-key identity; shared Gemini protocol adapter support remains.

## Review discipline

- [x] Coherent head ready for review.
- [x] Prior findings re-read; none exist for this PR.
- [x] No new head while FIFO reviewer reviews this exact head absent blocker.

CI/review retrigger: Hermes check failed without publishing a review or details URL payload; CI passed. Documentation-only head refresh.

## Worktree isolation

- [x] Dedicated feature worktree.
- [x] Worktree bound to this branch only.
- [x] Review fixes remain in this worktree.
