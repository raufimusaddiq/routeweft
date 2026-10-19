## Goal

Implement Sprint 2 PR 10: System One + Gemini/Ollama compatibility ingress (LLM-only).

## Non-goals

No standalone media/image/audio/embedding APIs, no provider catalog/OAuth/accounts, no routing strategies/fallback, no Combo/Fusion, no token savers or prompt-cache anchoring (Sprint 4), no admin API or UI. Cross-protocol translation stays a typed hook; no translator implementation lands here.

## Contract references

- PRD: §5 (API-001/002/004/005), §6 (typesafe SystemOne; gemini/ollama transports), §23 item 7
- SPEC: §8–§10, §20, §34
- BDR: BDR-008, BDR-009, BDR-010, BDR-016
- Sprint: Sprint 2, PR #10
- Behavioral reference: LiteRouter `2ffb7922954112b30425cd487d686758e519397e` — `src/app/api/v1/api/chat/route.js`, `open-sse/utils/ollamaTransform.js`, `src/sse/handlers/systemOne.js`

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant protocol/routing/provider fixtures added or updated.
- [x] No retained behavior was silently simplified.

Routes added:

```text
GET  /v1beta/models
POST /v1beta/models/{model}:generateContent
POST /v1beta/models/{model}:streamGenerateContent
POST /v1/api/chat
POST /v1/systemone
```

- Gemini: native GenerateContent/streamGenerateContent passthrough. Provider credential is sent as `x-goog-api-key`; the client Routeweft key is never forwarded. Unknown Gemini fields survive because the raw body is forwarded unchanged unless an explicit upstream model remap applies.
- Gemini model listing: derived from the compiled RuntimeSnapshot catalog, so aliases/disabled models follow the same rules as `/v1/models`.
- Ollama `/v1/api/chat`: accepts OpenAI Chat Completions request wire (matching the reference route, which delegates to the Chat handler and transforms the response). Native Ollama providers receive `/api/chat`; other targets use the existing Chat translator hook. Responses are emitted as `application/x-ndjson`; streaming converts OpenAI SSE deltas to Ollama JSON lines with a `done:true` terminal record.
- System One `/v1/systemone`: validates `model`, `state` and non-empty `questions`, then posts the typed envelope to the provider base URL verbatim (baseline `typesafe` base URL already ends in `/v1/systemone`). No upstream model rewriting happens unless an operator configures a remap.
- Cancellation propagates from the client context; compatibility streams are relayed with incremental flushing rather than buffering the whole response.

## Storage / migration impact

- [x] No storage/schema/migration impact.

Details: no SQLite changes. Gemini model listing reads the already-published RuntimeSnapshot; no new per-request DB read.

## Security impact

- [ ] No security-boundary impact.
- [x] Auth/secrets/network/SSRF impact is documented and tested.

Details: all three surfaces reuse the existing client-key authentication (`Authorization`, `x-api-key`, `x-goog-api-key`, `?key=`). Provider credentials are injected server-side only. Upstream URLs continue to pass `transport.ValidatePublicURL` unless the operator has explicitly enabled trusted local upstreams. The only trust-boundary change is that an empty relative endpoint now posts to the provider base URL verbatim; that path is reachable only from the System One route and the base URL is still SSRF-validated. Test `TestCompatRoutesRequireClientKey` asserts unauthenticated compatibility calls are rejected.

## Performance impact

- [ ] No inference hot-path impact.
- [x] Hot-path impact includes before/after benchmark evidence.

Gemini and System One native paths forward the request body without decoding it. The Ollama compatibility route necessarily decodes the OpenAI Chat body to remap the model and, when streaming, re-encodes deltas as ndjson; this matches the reference behavior and is not on the Chat/Responses/Messages hot path. Existing hot-path benchmark unchanged: `BenchmarkChatCompletionsNative-4` 1,607,870 ns/op, 58,653 B/op, 180 allocs/op (vs 1,579,486 ns/op, 58,882 B/op, 180 allocs/op at PR #9).

## Tests

`go test ./...`; `go test -race ./...`; `go vet ./...`; `go build ./...`; UI `npm ci`, `npm run typecheck`, `npm run build`. Fixture-driven coverage: Gemini generateContent byte-identical passthrough plus upstream path and `x-goog-api-key` injection; Gemini streaming byte passthrough; Gemini model listing from snapshot; unsupported/malformed Gemini requests; Ollama compatibility non-streaming ndjson plus `done:true`; System One typed-envelope passthrough and model remap; required-field rejection; unauthenticated compatibility routes rejected; translation-hook endpoint and headers. Docker: local build blocked by a reproducible npm CLI crash (`npm error Exit handler never called!`) inside the build container; the CI `container` job is the authoritative container check for this PR.

## Rollback

Revert this PR; remove only the compatibility ingress, protocol request adapters, and their tests. No storage or runtime state to unwind, and PR #7–#9 routes are unaffected.

## Review discipline

- [x] This is a coherent head ready for review.
- [x] Prior findings were re-read and valid fixes were bundled. (No review on this head yet.)
- [x] I will not push a new head while the FIFO reviewer is still reviewing this head unless a blocker requires it.

## Worktree isolation

- [x] This PR was implemented in its own dedicated feature worktree.
- [x] The worktree is bound to this PR branch only.
- [x] Review fixes will be made in the same worktree.
