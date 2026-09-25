## Goal

Implement Sprint 4 PR 14: RTK + Caveman + Ponytail token savers.

Deliver:

- a protocol-neutral request-transform pipeline that runs the token savers in the binding order RTK -> Headroom -> Caveman -> Ponytail -> PXPIPE;
- fail-open step semantics (a non-essential transform failure cannot fail a valid request);
- client per-request bypass;
- RTK tool-result compression;
- deterministic, idempotent Caveman and Ponytail policy injection at configured levels.

## Non-goals

No Headroom or PXPIPE implementation (PR 15/16) and no prompt-cache anchoring (PR 17). No wire-format rewrite or provider dispatch change: this PR delivers the transform primitives plus the ordered fail-open pipeline; the concrete Chat/Responses/Messages body adaptation and cache anchoring are Sprint 4/PR 17 work.

## Contract references

- PRD: §12, PRD-XFORM-001..003
- SPEC: §17, §17.1, §17.3, §26
- BDR: BDR-012
- Sprint: Sprint 4, PR #14

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant protocol/routing/provider fixtures added or updated.
- [x] No retained behavior was silently simplified.

- `internal/transforms`: `Request`/`Message` neutral view, `Transform` interface, and `Pipeline.Run` which clones input, skips on `Bypass`, and applies each step fail-open (a failed step's output is discarded and the previous request is kept). Step order is caller-declared and tested to match BDR-012.
- `internal/transforms/rtk`: folds consecutive duplicate tool-result lines into a single annotated line. This is lossless for unique content and preserves order, rather than truncating unseen middle context.
- `internal/transforms/caveman` and `.../ponytail`: insert a deterministic, level-specific policy block (lite/full/ultra) marked `[routeweft:caveman]` / `[routeweft:ponytail]`. Insertion is idempotent and placed after existing Routeweft policy blocks but before caller instructions, so repeated runs and translated protocols cannot duplicate or reorder blocks.
- Enable flags/levels map to the existing compiled settings `rtkEnabled`, `cavemanEnabled`/`cavemanLevel`, `ponytailEnabled`/`ponytailLevel`.

Pending-decision disclosure: the exact RTK token-budget algorithm and the concrete Caveman/Ponytail prompt wording are not fixed in BDR/SPEC; this PR pins a deterministic, tested behavior and notes it here. Wire-body adaptation and cache anchoring are deferred to PR 17.

## Storage / migration impact

- [x] No storage/schema/migration impact.

Details: transforms read existing compiled settings only; no durable state changes.

## Security impact

- [x] No security-boundary impact.

Details: no credentials, auth, or network behavior touched. Policy blocks contain no user or secret data.

## Performance impact

- [x] No inference hot-path impact.

Details: pipeline operates on in-memory request copies only; no SQLite read or config recomputation. No transforms are wired into dispatch in this PR.

## Tests

`go test ./...`; `go test -race ./...`; `go vet ./...`; `go build ./...`; UI `npm run typecheck`, `npm run build`. Coverage: declared normative order, fail-open rollback isolation, client bypass, input immutability, RTK duplicate folding preserving unique content/order, non-tool content untouched, idempotent policy injection and block ordering.

## Rollback

Revert this PR to remove the transform pipeline and three savers. No durable or wire state depends on it yet.

## Review discipline

- [x] This is a coherent head ready for review.
- [x] Prior findings were re-read and valid fixes were bundled. (No review on this head yet.)
- [x] I will not push a new head while the FIFO reviewer is still reviewing this head unless a blocker requires it.

## Worktree isolation

- [x] This PR was implemented in its own dedicated feature worktree.
- [x] The worktree is bound to this PR branch only.
- [x] Review fixes will be made in the same worktree.
