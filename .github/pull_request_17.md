## Goal

Implement Sprint 4 PR 17: final-body prompt-cache anchoring and cache-token accounting.

Deliver:
- deterministic Anthropic cache anchors applied to the final outbound body (native Messages and translated-to-Messages targets);
- client marker preservation, marker budget, deferred tools, thinking/redacted-thinking, first turn, assistant turns and N/N+1 stability;
- cache-read/cache-create token accounting parsed from upstream Messages responses and forwarded to the caller-supplied `Options.OnUsage` sink.
- an `Options.TransformFinalBody` hook invoked before anchoring so the wiring layer can run the Sprint 4 token savers in the normative order.

## Non-goals

No control API/UI surface, no telemetry queue/batch durability (Sprint 6 owns the bounded Usage queue), no token-saver configuration/compilation, and no cache accounting for protocols that do not report cache usage.

## Contract references

- PRD: §12, PRD-CACHE-001..002, PRD-API-007
- SPEC: §17 (order), §18 (prompt-cache preservation)
- BDR: BDR-012 (anchors after transforms)
- Sprint: Sprint 4, PR #17

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant fixtures added or updated.
- [x] No retained behavior was silently simplified.

`internal/transforms/promptcache.Anchor` decodes the final outbound JSON body, counts existing client `cache_control` markers, then spends only the remaining breakpoint budget (default 4, the platform limit) on stable-prefix anchors in priority order: last system block, last tool definition, completed conversation turn. Client markers and unknown fields are preserved; the active final user turn is never anchored, so the N and N+1 requests share the same cache boundary. Thinking/redacted-thinking blocks are skipped as anchor targets, string `system`/`content` shapes are left untouched, and any decode/shape problem leaves the body byte-identical because `handleMessages` fails open on anchor errors.

`handleMessages` calls `Options.TransformFinalBody` first (fail-open on error) and only then applies anchors, so anchors always describe the post-transform prefix required by BDR-012.

`ParseAnthropicUsage` extracts `input_tokens`, `output_tokens`, `cache_read_input_tokens` and `cache_creation_input_tokens` from non-streaming responses and from streamed `message_start`/`message_delta` events. `handleMessages` keeps streaming byte forwarding intact, adds a tee scanner only when `OnUsage` is set, and reports usage only after the upstream terminal event so a cancelled stream never records partial accounting. Non-streaming paths record usage only for 2xx responses and write the upstream body unchanged.

## Storage / migration impact

- [x] No storage/schema/migration impact.

`usage_events`/`usage_daily` already carry cache columns; this PR only produces the parsed counters for the Sprint 6 bounded queue to persist.

## Security impact

- [x] No security-boundary impact.
- [ ] Auth/secrets/network/SSRF impact is documented and tested.

Details: no new network destination, credential, or log surface. The `OnUsage` callback receives only provider ID, upstream model, and token counters, never prompt content.

## Performance impact

- [x] No inference hot-path impact.
- [ ] Hot-path impact measured and reported.

Details: anchoring adds one decode/encode of an already-buffered request body on Claude-target paths, matching native-normalized mode (SPEC §9.2). Streaming remains chunk-forwarded; the usage tee performs one linear scan with no extra buffering beyond the existing SSE accumulator. Usage accounting is skipped entirely when `OnUsage` is nil.

## Tests

`go test ./...`; `go test -race ./...`; `go vet ./...`; `go build ./...`; UI `npm run typecheck`; `npm run build`.

Coverage: N/N+1 stable prefix, active-turn exclusion, client-marker preservation, budget enforcement, idempotence, malformed/plain bodies, deferred tools, thinking/redacted thinking, non-stream usage extraction, split-chunk stream usage merging, terminal gating, and the ingress native path reporting fixture cache-read tokens.

## Rollback

Revert this PR to stop anchoring and usage reporting; no durable state depends on it.

## Review discipline

- [x] This is a coherent head ready for review.
- [x] Prior findings were re-read and valid fixes were bundled.
- [x] I will not push a new head while the FIFO reviewer is still reviewing this head unless a blocker requires it.

## Worktree isolation

- [x] This PR was implemented in its own dedicated feature worktree.
- [x] The worktree is bound to this PR branch only.
- [x] Review fixes will be made in the same worktree.
