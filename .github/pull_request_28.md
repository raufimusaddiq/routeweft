## Goal

Wire first-party provider identity/credential headers into the real outbound dispatch path: Codex CLI fingerprint on Codex Responses, Claude CLI fingerprint plus bearer auth for Claude OAuth, and Anthropic API-key version/beta defaults.

## Non-goals

No provider token refresh at request time, no account/connection store, no retry/fallback of auth failure, and no other provider groups. Existing OpenAI and Gemini credential header behavior is preserved.

## Contract references

- PRD: provider identity/auth headers; PRD-API-005 request behavior
- SPEC: §9 native/normalized/translated path, §11 provider identity, §19 credentials
- BDR: BDR-009, BDR-010
- Provider baseline: `codex`, `claude`, `anthropic` rows (headers/beta behavior)
- Sprint: Sprint 5, groups 2-4 provider request-header integration slice

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

Native OpenAI/Responses dispatch now uses a provider-aware header helper: `openai` keeps bearer-only; `codex` adds `originator: codex_cli_rs` and `User-Agent: codex_cli_rs/0.154.0`. Anthropic keeps client `Anthropic-Version`/`Anthropic-Beta` opt-in, defaults `anthropic-version` and the first-party beta set, and switches to bearer auth plus Claude CLI headers when `ProviderID == "claude"`. The client credential is never forwarded.

## Storage / migration impact

- [x] No storage/schema/migration impact.

## Security impact

- [ ] No security-boundary impact.
- [x] Auth/secrets impact is documented and tested.

Provider credentials are still injected only from `ProviderRef.APIToken`; no client credential is forwarded. Tests assert header shape and that absent provider identity adds no fingerprint.

## Performance impact

- [x] No inference hot-path impact.

Constant-size header maps built per dispatch attempt; no extra I/O.

## Tests

`gofmt -l`; `go test -race ./internal/ingress ./internal/providers/claude ./internal/providers/codex ./internal/providers/anthropic`; `go test -race ./...`; `go vet ./...`; `go build ./...`; `git diff --check`; UI `npm ci`, `npm run typecheck`, `npm run build`. All pass.

## Rollback

Revert this PR to restore the previous bearer/x-api-key-only headers; provider modules remain.

## Review discipline

- [x] Coherent head ready for review.
- [x] Prior findings re-read; none exist for this PR.
- [x] No new head while FIFO reviewer reviews this exact head absent blocker.

## Worktree isolation

- [x] Dedicated feature worktree.
- [x] Worktree bound to this branch only.
- [x] Review fixes remain in this worktree.
