## Goal

Add native OpenAI Responses and Responses Compact ingress, preserve native request/response bytes, support compact endpoint semantics, and expose the approved Responses aliases.

## Non-goals

No Responses-to-other-protocol implementation, provider catalog/accounts, fallback/routing strategies, Combo/Fusion, transforms/cache policy, Usage/Quota, admin APIs, or UI. Cross-protocol conversion remains a translator hook for later adapter work.

## Contract references

- PRD: §5 (API-001/003/005/006), §6 (native OpenAI requirement)
- SPEC: §8–10, §34
- BDR: BDR-008, BDR-009, BDR-010
- Sprint: Sprint 2, PR #8

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant protocol/routing/provider fixtures added or updated.
- [x] No retained behavior was silently simplified.

Native Responses preserves exact request bytes including tools, parallel tool calls, reasoning, multi-turn input, and unknown fields. Aliases `/responses`, `/codex/*`, and `/v1/v1/responses` route to Responses ingress. Compact injects an internal flag only at dispatch selection, removes `_compact` from upstream JSON, and targets `/responses/compact` for native same-protocol providers. Responses SSE is relayed incrementally; terminal event is held until clean EOF; duplicate/non-final terminal events are suppressed; incomplete streams emit `response.failed`. Translation hooks receive parsed source requests and own target endpoint/body/headers.

## Storage / migration impact

- [x] No storage/schema/migration impact.

Details: request-serving configuration still reads only RuntimeSnapshot; no persistence changes.

## Security impact

- [ ] No security-boundary impact.
- [x] Auth/secrets/network/SSRF impact is documented and tested.

Reuses client key auth and the merged PR #7 SSRF-protected pooled transport; provider secrets remain server-side. No raw client headers forwarded. Compact/alias routes use the same auth/CORS/body bounds as `/v1/responses`.

## Performance impact

- [x] No inference hot-path impact.
- [ ] Hot-path impact includes before/after benchmark evidence.

Details: extends the first inference surface only; native request paths avoid decoding/re-encoding except compact marker stripping/model remap; response stream stays incremental with bounded per-event buffering (4 MiB cap). Chat benchmark is unchanged in scope; no Responses baseline exists before this PR.

## Tests

`go test ./...`; `go test -race ./...`; `go vet ./...`; `go build ./...`; UI `npm ci`, `npm run typecheck`, `npm run build`; Docker build + scratch entrypoint smoke; existing Chat benchmark and focused Responses ingress tests. Coverage: exact native body preservation, tools/parallel/reasoning/multi-turn fields, aliases, compact upstream path and private marker removal, Responses translation hook, stream fixture terminal passthrough, duplicate/missing/non-final terminal handling, client cancellation, malformed request validation, auth and SSRF shared middleware.

## Rollback

Revert this PR; remove only Responses routes/adapter/tests. No storage state to unwind. PR #7 Chat ingress remains unaffected.

## Review discipline

- [x] This is a coherent head ready for review.
- [x] Prior findings were re-read and valid fixes were bundled. (No review on this head yet.)
- [x] I will not push a new head while the FIFO reviewer is still reviewing this head unless a blocker requires it.

## Worktree isolation

- [x] This PR was implemented in its own dedicated feature worktree.
- [x] The worktree is bound to this PR branch only.
- [x] Review fixes will be made in the same worktree.

