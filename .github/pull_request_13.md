## Goal

Implement Sprint 3 PR 13: bounded Fusion orchestration.

Deliver:

- parallel panel fan-out, non-streaming/tool-free panel contract and tool-history flattening helper;
- configurable judge with first-Combo-model default;
- minimum-panel quorum, straggler grace and hard timeout;
- 0 successes -> error, 1 -> direct answer, 2+ -> judge;
- bounded panel count, response bytes and concurrent Fusion requests.

## Non-goals

No control-plane API/UI or public route changes. No provider-specific panel/judge wire adapters; `PanelFunc`/`JudgeFunc` keep request construction, protocol translation, stream formatting and tool preservation at the existing ingress/provider boundary. No prompt transforms or cache behavior.

## Contract references

- PRD: §11, PRD-COMBO-003
- SPEC: §16, §28.1–28.2
- BDR: BDR-006, BDR-009, BDR-011
- Sprint: Sprint 3, PR #13

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant protocol/routing/provider fixtures added or updated.
- [x] No retained behavior was silently simplified.

`internal/routing/fusion.go` adds the transport-agnostic `Fusion` orchestration API. Each panel is started concurrently with the same hard-timeout context; configured `MinPanelQuorum` starts the straggler grace timer; cancellation/deadline ends collection. Zero successes returns `ErrFusionNoPanels`; a sub-quorum result returns `ErrFusionQuorum`; one answer is returned directly; 2+ anonymous answer strings are passed to the judge. Empty explicit judge uses `DefaultJudge`, which callers set to the first Combo member per SPEC §16. `Grace == 0` stops collection as soon as quorum is met.

`PanelFunc` returns an `io.ReadCloser`; Fusion reads it through `io.LimitReader` and rejects the panel as soon as `MaxResponseBytes` is exceeded, so an oversized upstream is never fully buffered. Panel calls must be non-streaming, tool-free, and use prose-flattened prior tool history. `StripTools` removes `tools` and `tool_choice`; `FlattenToolHistory` joins already-rendered tool turns. `JudgeFunc` receives the original request context and is responsible for preserving client streaming/tools behavior.

Defaults cap panel count (8), per-panel response bytes (1 MiB), hard panel timeout (30s), grace (250ms), and concurrent requests (8). Limits are validated (negatives rejected) and frozen on first use so a concurrent `Config` mutation cannot race with in-flight requests. Tests cover fan-out result states, quorum, grace, timeout, judge fallback, concurrency cap, streaming byte/panel caps, config freeze, zero-grace stop, and tool/history helpers.

## Storage / migration impact

- [x] No storage/schema/migration impact.

Details: Fusion configuration already persists on Combo records from PR12. This PR adds only in-memory orchestration primitives; no durable state changes.

## Security impact

- [x] No security-boundary impact.

Details: no credentials, URLs, auth, or provider response data are logged. Concurrency, panel count, response-byte and hard-timeout limits bound resource use. Panel/Judge implementations use existing SSRF-protected provider dispatch.

## Performance impact

- [x] No inference hot-path impact.

Details: only Fusion requests incur fan-out; cap is explicit, and panel orchestration performs no SQLite reads or configuration recomputation.

## Tests

`go test ./...`; `go test -race ./...`; `go vet ./...`; `go build ./...`; UI `npm run typecheck`, `npm run build`. Fusion-specific tests cover concurrent panel collection, hard timeout, quorum miss, straggler grace, 0/1/2+ success behavior, explicit/default judge, concurrency rejection, panel/response caps, tool stripping and prose flattening.

## Rollback

Revert this PR to remove the in-memory Fusion orchestrator. Existing Combo configuration remains inert and unchanged.

## Review discipline

- [x] This is a coherent head ready for review.
- [x] Prior findings were re-read and valid fixes were bundled. (No review on this head yet.)
- [x] I will not push a new head while the FIFO reviewer is still reviewing this head unless a blocker requires it.

## Worktree isolation

- [x] This PR was implemented in its own dedicated feature worktree.
- [x] The worktree is bound to this PR branch only.
- [x] Review fixes will be made in the same worktree.
