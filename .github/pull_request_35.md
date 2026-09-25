## Goal

Add built-in-provider model discovery: an SSRF-guarded, bounded client that fetches a provider's live catalog, plus a runtime mutation that swaps in discovered models while preserving operator-managed models (PROVIDER_BASELINE §7, SPEC §4/§7).

## Non-goals

No admin/control API endpoint or UI trigger, no scheduled/background refresh loop, no per-provider discovery-path table beyond the explicit override the caller passes, and no model validation/probe flow. Writes stay inside the existing compile-before-commit transaction protocol. Generic Provider discovery already exists and is unchanged.

## Contract references

- PRD: PRD §6 provider model discovery; PRD-AUTH/§8 credential handling
- SPEC: §4 durable store (`provider_models` cache), §7 config transaction protocol, §20/§33 SSRF validation for discovery, §34 model validation order
- BDR: BDR-009, BDR-010, BDR-022
- Provider baseline: §7 model catalog requirements (dynamic + passthrough, seed retained offline, discovery failure must not delete configured models)
- Sprint: Sprint 5, model discovery slice

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

`discovery.Client` fetches over the central SSRF guard, honors one relative override path, bounds the response, supports bearer / `x-api-key` / no-auth credential styles, and returns sorted, de-duplicated IDs. `ParseModels` accepts OpenAI (`data[].id`), Gemini (`models[].name`) and bare-array shapes. `Manager.ReplaceDiscoveredModels` replaces only the target provider's non-custom, discovered models, keeps custom models and every other provider untouched, marks entries `discovered`, and treats an empty result as a no-op so a failed refresh never deletes the last durable catalog. IDs stay routable under existing passthrough policy.

## Storage / migration impact

- [x] No schema/migration impact.

Reuses the existing `provider_models` table and `discovered` source tag through the standard `UpdateCatalog` path.

## Security impact

- [ ] No security-boundary impact.
- [x] Auth/secrets/network impact is documented and tested.

Discovery URLs pass the same SSRF guard as inference (trusted-local only under the explicit operator policy); response bodies are bounded; error strings carry no body content; the no-auth style omits the credential; tests assert the credential never leaks on the no-auth path or in an HTTP-failure message.

## Performance impact

- [x] No inference hot-path impact.

Discovery runs on the control path and publishes through the normal snapshot transaction; the request path never triggers discovery.

## Tests

`gofmt -l`; `go build ./...`; `go vet ./...`; `go test -race ./...`; `git diff --check`; UI `npm ci`, `npm run typecheck`, `npm run build`. All pass.

Discovery tests cover bearer/x-api-key/no-auth styles, endpoint/path construction, traversal and missing-id rejection, the three catalog shapes, HTTP failure without body leakage, and invalid JSON. Runtime tests cover custom-model and cross-provider preservation, replacement (not unbounded append), empty-discovery no-op, blank-id rejection, and durable reload.

## Rollback

Revert this PR to drop discovery; no schema change is introduced and existing persisted models remain valid.

## Review discipline

- [x] Coherent head ready for review.
- [x] Prior findings re-read and validated. The reviewer flagged that an empty discovered slice would still delete the provider's discovered models, contradicting the documented no-op contract; that was valid, so `ReplaceDiscoveredModels` now returns before mutating when the slice is empty, and the regression test asserts an existing discovered model survives an empty refresh.
- [x] No new head while FIFO reviewer reviews this exact head absent blocker.

## Worktree isolation

- [x] Dedicated feature worktree.
- [x] Worktree bound to this branch only.
- [x] Review fixes remain in this worktree.
