## Goal

Implement native Anthropic Messages ingress and `/v1/messages/count_tokens`, preserving tool-use/tool-result order, thinking, redacted thinking, and prompt-cache controls.

## Non-goals

No Messages-to-other-protocol translator implementation, provider catalog/accounts/OAuth, fallback/routing strategies, Combo/Fusion, Usage/Quota, admin API/UI, or tokenization SDK. Cross-protocol translation is a typed hook for later adapter work.

## Contract references

- PRD: §5 (API-001/005/007), §6 (Anthropic native Messages)
- SPEC: §8–10, §20, §34
- BDR: BDR-008, BDR-009, BDR-010
- Sprint: Sprint 2, PR #9
- Behavioral reference: LiteRouter `2ffb7922954112b30425cd487d686758e519397e`, count_tokens route and provider shared headers

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant protocol/routing/provider fixtures added or updated.
- [x] No retained behavior was silently simplified.

`POST /v1/messages` preserves raw native JSON, including tool blocks/order, thinking, system content, `cache_control`, and unknown fields; changes only `model` on explicit remap. Adds `/messages` and historical `/v1/v1/messages` aliases. Native dispatch sets provider `x-api-key`, default Anthropic version `2023-06-01`, client beta opt-in. SSE relays incrementally, filters terminal duplicates/non-final markers, guarantees one final `message_stop` on clean EOF, emits an error plus terminal marker on incomplete clean EOF. Count-tokens follows the approved compatibility estimator (nested character count divided by four, rounded up); no upstream call.

## Storage / migration impact

- [x] No storage/schema/migration impact.

Details: no SQLite changes; request routing reads RuntimeSnapshot only.

## Security impact

- [ ] No security-boundary impact.
- [x] Auth/secrets/network/SSRF impact is documented and tested.

Reuses client key authentication and SSRF-protected pooled transport from PR #7. Provider credential is sent server-side only as `x-api-key`; upstream error body/status are passed through without logging secrets. Only explicit Anthropic protocol headers are forwarded; no arbitrary client headers.

## Performance impact

- [ ] No inference hot-path impact.
- [x] Hot-path impact includes before/after benchmark evidence.

Adds one parsed view/map for validation, but native body is still forwarded byte-for-byte unless model remap is configured. Responses remain streamed; no full response buffering. No prior Messages inference baseline exists. Chat benchmark regression check included with final evidence.

## Tests

`go test ./...`; `go test -race ./...`; `go vet ./...`; `go build ./...`; UI `npm ci`, `npm run typecheck`, `npm run build`; Docker build + scratch entrypoint smoke; existing Chat ingress benchmark. Coverage: fixture-driven Messages body identity, tool/thinking/cache preservation, model remap, malformed input, native request/auth/version/beta headers, aliases, message_stop passthrough, stream terminal normalization, client cancellation, upstream auth error relay, count_tokens estimate and malformed body, cross-protocol hook, SSRF guard reuse.

## Rollback

Revert this PR; remove only Anthropic ingress/adapter/count_tokens code. No storage state to unwind. PR #7/#8 remain unaffected.

## Review discipline

- [x] This is a coherent head ready for review.
- [x] Prior findings were re-read and valid fixes were bundled. (No review on this head yet.)
- [x] I will not push a new head while the FIFO reviewer is still reviewing this head unless a blocker requires it.

## Worktree isolation

- [x] This PR was implemented in its own dedicated feature worktree.
- [x] The worktree is bound to this PR branch only.
- [x] Review fixes will be made in the same worktree.
