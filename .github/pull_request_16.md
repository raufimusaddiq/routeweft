## Goal

Implement Sprint 4 PR 16: PXPIPE large-context external transform primitive.

Deliver:
- pxpipeEnabled gate and pxpipeMinChars size threshold;
- pxpipeTimeoutMs hard timeout and bounded request/response handling;
- fail-open behavior for optional transform;
- request-detail diagnostics without prompt content;
- health/log/stats surface for the control API.

## Non-goals

No prompt-cache anchoring/accounting (PR 17), dispatch/pipeline wiring, streaming interaction, or control API/UI endpoints (later sprints).

Managed process install/start/stop/restart is not implemented. No BDR/SPEC clause assigns process supervision to Routeweft; pxpipeAutoInstall remains a deployment policy for a later settings/deployment PR rather than an invented launch contract.

## Contract references

- PRD: §12, PRD-XFORM-001..005, §15 defaults
- SPEC: §17, §17.4
- BDR: BDR-012 (after Ponytail, before prompt-cache anchors)
- Sprint: Sprint 4, PR #16

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant fixtures added or updated.
- [x] No retained behavior was silently simplified.

internal/transforms/pxpipe adds a Transform in the normative pipeline slot. It estimates neutral-view size and skips the service below MinChars (default 25000). Above threshold it POSTs {system, messages} and accepts only a {system, messages} response.

The URL is validated as HTTP(S) without user info/query/fragment and checked by transport.ValidatePublicURL (SSRF). Redirects are disabled; timeout and response byte cap are enforced. Failures return errors, allowing Pipeline to preserve the original request (fail-open).

Diagnostic records status/attempted/succeeded/input/output chars/duration/error. Transport and body-read failures use static messages to prevent content leakage. Service exposes process-local Stats, bounded Logs, and Health for later control API wiring.

## Storage / migration impact

- [x] No storage/schema/migration impact.

Counters/logs are process-local and non-durable.

## Security impact

- [ ] No security-boundary impact.
- [x] Auth/secrets/network/SSRF impact documented and tested.

HTTP(S)-only URL validation plus transport.ValidatePublicURL rejects loopback/private/metadata targets. No embedded credentials/query/fragment; redirects disabled; response size capped; diagnostics exclude prompt/response content.

## Performance impact

- [x] No inference hot-path impact.

Below threshold, no I/O. Above threshold, one bounded HTTP call. No SQLite read/config recomputation. Transform is not wired into dispatch here.

## Tests

go test ./...; go test -race ./...; go vet ./...; go build ./...; UI npm run typecheck; npm run build.

Coverage: threshold skip, round-trip, immutability, HTTP/JSON/schema errors, oversized body, timeout fail-open, diagnostic redaction, health, stats/logs.

## Rollback

Revert this PR to remove PXPIPE client. No durable state depends on it.

## Review discipline

- [x] Coherent head ready for review.
- [x] Prior findings re-read; valid fixes bundled.
- [x] No new head while FIFO reviewer reviews this head absent blocker.

## Worktree isolation

- [x] Dedicated feature worktree.
- [x] Worktree bound to this PR branch only.
- [x] Review fixes remain in this worktree.

