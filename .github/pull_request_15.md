## Goal

Implement Sprint 4 PR 15: Headroom external compression integration.

Deliver:

- configurable Headroom URL and timeout;
- optional user-message compression flag;
- fail-open behavior for optional compression;
- bounded request/response handling and redacted diagnostics;
- health probe for the configured service.

## Non-goals

No managed process start/stop/supervision: SPEC §17.2 ties managed health/lifecycle to Routeweft owning the Headroom process, which the accepted architecture does not establish (no sidecar management in BDR). No PXPIPE (PR 16), no prompt-cache anchoring (PR 17), no control API/UI surface (later sprints).

## Contract references

- PRD: §12, PRD-XFORM-001..003
- SPEC: §17.2, §17
- BDR: BDR-012
- Sprint: Sprint 4, PR #15

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant protocol/routing/provider fixtures added or updated.
- [x] No retained behavior was silently simplified.

`internal/transforms/headroom` adds a `Transform` that POSTs the neutral `{system, messages, compress_user_messages}` payload and accepts only a `{system, messages}` response. It validates the URL (HTTP(S) only, no user info/query/fragment) and runs it through the existing SSRF public-URL validator before dispatch, applies a hard timeout and response-byte cap, and rejects non-2xx or malformed responses.

Fail-open is preserved: any error returns the original `transforms.Request` (and the shared `Pipeline` also discards failed step output), so an unavailable Headroom service cannot fail an inference request when compression is optional. `Diagnostics` reports enabled/attempted/succeeded/duration/error with no prompt content, satisfying the request-detail diagnostics requirement without leaking user text.

## Storage / migration impact

- [x] No storage/schema/migration impact.

Details: uses compiled settings `headroomEnabled`, `headroomUrl`, `headroomCompressUserMessages`, `headroomTimeoutMs`; no durable state.

## Security impact

- [ ] No security-boundary impact.
- [x] Auth/secrets/network/SSRF impact is documented and tested.

Details: outbound URL is validated as HTTP(S) with no embedded credentials/query/fragment and verified by `transport.ValidatePublicURL` so loopback/LAN/metadata targets are rejected. Redirects are disabled by the default client. Diagnostics never include prompt or response content.

## Performance impact

- [x] No inference hot-path impact.

Details: the transform performs one bounded HTTP call with a hard timeout; no SQLite read or config recomputation. It is not wired into dispatch in this PR.

## Tests

`go test ./...`; `go test -race ./...`; `go vet ./...`; `go build ./...`; UI `npm run typecheck`, `npm run build`. Coverage: successful payload/response round-trip, user-compression flag propagation and input immutability, private-URL rejection, non-2xx, malformed body, oversized body, timeout fail-open, diagnostic redaction, and the health probe/URL validation.

## Rollback

Revert this PR to remove the Headroom client. No durable state depends on it.

## Review discipline

- [x] This is a coherent head ready for review.
- [x] Prior findings were re-read and valid fixes were bundled. (No review on this head yet.)
- [x] I will not push a new head while the FIFO reviewer is still reviewing this head unless a blocker requires it.

## Worktree isolation

- [x] This PR was implemented in its own dedicated feature worktree.
- [x] The worktree is bound to this PR branch only.
- [x] Review fixes will be made in the same worktree.
