## Goal

Add the deterministic protocol fixture set, local mock upstream, and benchmark harness that later ingress/provider PRs use as their compatibility oracle.

## Non-goals

No public endpoints, protocol adapters/translation, routing, providers, transforms, admin API, or UI. Fixtures + mock upstream + harness only.

## Contract references

- PRD: §5 (API-005/006/007), §19
- SPEC: §2, §8–10, §28.3, §29
- BDR: BDR-010, BDR-020, BDR-021
- Sprint: Sprint 1, PR #5

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change. (No public behavior change; fixtures define expected wire behavior for later PRs.)
- [x] Relevant protocol/routing/provider fixtures added or updated.
- [x] No retained behavior was silently simplified.

## Storage / migration impact

- [x] No storage impact.

## Security impact

- [x] Auth/secrets/network/SSRF impact is documented and tested.

Details: fixtures are synthetic only and validated to reject key-like content; the mock upstream binds to 127.0.0.1 on an ephemeral port; auth-error fixtures carry placeholder credentials.

## Performance impact

- [x] No inference hot-path impact.
- [x] Hot-path impact includes before/after benchmark evidence.

Details: harness only; no inference hot path exists yet. Baseline (Intel Xeon E5-2680 v4): fixture HTTP round trip ~364–395 us/op, 92 allocs/op via `go test ./bench/router -run '^$' -bench . -benchmem -count=5`.

## Tests

`go test ./...`; `go test -race ./compat/...`; `go vet ./...`; `go build ./cmd/routeweft`; `go test ./bench/router -run '^$' -bench . -benchmem -count=5`. Fixtures cover OpenAI Chat, Responses, Anthropic Messages, Gemini, Ollama, System One, plus streaming termination, client cancellation, and auth/upstream error mapping. Loader tests assert embedded coverage, synthetic content, valid JSON, and exactly-once stream terminal markers; the mock upstream replays every fixture byte-for-byte and proves delayed-upstream client cancellation.

## Rollback

Revert this PR; no runtime behavior or durable state depends on it yet.

## Review discipline

- [x] This is a coherent head ready for review.
- [x] Prior findings were re-read and valid fixes were bundled. (No PR #5 review yet.)
- [x] I will not push a new head while the FIFO reviewer is still reviewing this head unless a blocker requires it.

## Worktree isolation

- [x] This PR was implemented in its own dedicated feature worktree.
- [x] The worktree is bound to this PR's branch only.
- [x] Review fixes will be made in the same worktree.
