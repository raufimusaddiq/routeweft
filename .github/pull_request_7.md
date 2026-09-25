## Goal

Add native OpenAI Chat Completions ingress (`POST /v1/chat/completions`) with a
translation hook reserved for later non-Chat providers, so Chat requests reach a
resolved upstream with byte-exact native wire framing.

## Non-goals

No Responses/Messages/Gemini/Ollama/System One routes; no provider catalog,
accounts, routing strategies, fallback, Combo/Fusion, transforms, prompt cache,
Usage/Quota, admin API/UI, or telemetry workers. App-level provider resolution
stays a delegated hook until the Sprint 3 provider registry lands.

## Contract references

- PRD: §5 (PRD-API-001/005/007), §22
- SPEC: §9–10 (native-first), §20 (transport/SSRF), §34 (native preservation)
- BDR: BDR-008, BDR-009, BDR-010
- Sprint: Sprint 2, PR #7 ("Deliver native path first, then translation hooks")

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant protocol/routing/provider fixtures added or updated.
- [x] No retained behavior was silently simplified.

Details: native Chat body bytes are forwarded unchanged (unknown forward-
compatible fields preserved); model remap only rewrites the `model` value when a
resolved upstream model differs. Streaming is relayed incrementally with bounded
line buffering; terminal markers and their separators are filtered, then exactly
one terminal event is emitted on clean EOF. Missing, duplicate, or non-final
markers cannot reach clients. Client
disconnect cancels upstream work; upstream status/body relayed without leaking
the provider credential. Translation is a hook only; no cross-protocol path ships
in this PR.

## Storage / migration impact

- [x] No storage/schema/migration impact.

Details: no SQLite schema change. Requests read only the already-loaded
RuntimeSnapshot; no synchronous configuration read or recomputation on the hot
path.

## Security impact

- [ ] No security-boundary impact.
- [x] Auth/secrets/network/SSRF impact is documented and tested.

Details: the existing client-key auth gate is reused; provider credentials are
injected server-side only and never echoed; hop-by-hop/sensitive client headers
are not forwarded. Central SSRF policy blocks non-public DNS/IP targets at URL
validation and dial time, rejects redirects, ignores proxy environment variables,
and provides an explicit trusted-local mode that still blocks metadata/link-local
targets. Provider base URLs must be absolute HTTP(S) without embedded
userinfo/query/fragment. Status/error paths and loopback/private rejection are
tested.

## Performance impact

- [ ] No inference hot-path impact.
- [x] Hot-path impact includes before/after benchmark evidence.

Details: adds the first inference path (`BenchmarkChatCompletionsNative`), so
there is no prior same-path baseline. Isolated local evidence (Intel Xeon E5-2680
v4, mock upstream, `-count=3`): ~590–644 us/op, 174 allocs/op, ~58 KB/op;
concurrent verification made a repeat noisy (~1.28–1.79 ms/op). No per-request
transport/client allocation; the pooled client and keep-alive transport are reused.

## Tests

`go test ./...`; `go test -race ./...`; `go vet ./...`; `go build ./...`; UI
`npm ci`, `npm run typecheck`, `npm run build`; `docker build` (host network for
registry fetch in this environment). Coverage: fixture-driven parse/native body
identity + unknown-field preservation, model-remap isolation, required-field and
malformed-body rejection, native non-streaming passthrough, streaming framing +
exactly-once terminal, delayed-upstream client-cancellation, auth/upstream-error
relay without credential leakage, unknown-model 404, oversize 413, dead-upstream
502, bounded one-attempt route budget, private/metadata URL denial, loopback
dial-time denial, redirect refusal, trusted-local allowance, and incremental
stream terminal normalization for missing/duplicate/already-final markers,
including long events containing duplicate terminals.

## Rollback

Revert this PR: the route and adapter are additive, so main returns to the
PR #6 ingress surface. No data migration to unwind; no released compatibility
commitment depends on the Chat path yet.

## Review discipline

- [x] This is a coherent head ready for review.
- [x] Prior findings were re-read and valid fixes were bundled. (No review on this head yet.)
- [x] I will not push a new head while the FIFO reviewer is still reviewing this head unless a blocker requires it.

## Worktree isolation

- [x] This PR was implemented in its own dedicated feature worktree.
- [x] The worktree is bound to this PR's branch only.
- [x] Review fixes will be made in the same worktree.
