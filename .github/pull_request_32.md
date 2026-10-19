## Goal

Register the OAuth/PAT/cookie-specialized provider group (xai, github, gitlab, iflow, kimi, xiaomi-mimo, cline, clinepass, kilocode, codebuddy-cn, codebuddy-intl) with their approved transports, auth kinds and catalog semantics, and add the provider identity header hooks their upstreams require.

## Non-goals

No OAuth/device/PAT token-exchange or refresh implementation, no connection store/admin OAuth endpoints, no provider-specific response-envelope unwrapping (cline), no usage/quota clients for this group, no multi-account selection policy, and no specialized wire-format (antigravity/cursor/gemini-cli/vertex) work. Those remain required baseline items for their assigned slices and are not dropped.

## Contract references

- PRD: provider transport/auth/model catalog requirements and connection workflows
- SPEC: §9 native route selection, §11 provider identity, §19 credentials
- BDR: BDR-009, BDR-010, BDR-022
- Provider baseline: `xai`, `github`, `gitlab`, `iflow`, `kimi`, `xiaomi-mimo`, `cline`, `clinepass`, `kilocode`, `codebuddy-cn`, `codebuddy-intl` rows; header values cross-checked against the cited reference snapshot
- Sprint: Sprint 5, group 5 OAuth-specialized slice

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

Dual-auth providers (xai, kimi, xiaomi-mimo) declare their native Chat plus Responses/Messages transports so a source-matching route skips translation (BDR-010); `github` advertises Chat, Responses and Messages so Copilot's Anthropic shim is reachable. Because `AuthKind` is single-valued, `Spec` now carries `AuthModes`, an ordered multi-valued credential-mode list whose first entry is the default `Auth`; xai, kimi, xiaomi-mimo, clinepass, codebuddy-cn and codebuddy-intl list both `api-key` and `oauth` per the baseline matrix. `kilocode` proxies the OpenRouter catalog, so arbitrary IDs stay routable. `cline`/`clinepass` share the cline.bot gateway endpoint. `codebuddy-cn`/`codebuddy-intl` report usage. The dispatch layer now applies the GitHub Copilot VS Code fingerprint, the Cline referer/title pair, and the iFlow/CodeBuddy user-agent from a provider-identity module; unknown providers add no fingerprint and credentials are never placed in identity headers.

## Storage / migration impact

- [x] No storage/schema/migration impact.

## Security impact

- [ ] No security-boundary impact.
- [x] Auth/secrets impact is documented and tested.

Identity headers carry no credential; tests assert no `Authorization`/`X-Api-Key` value leaks into them, and that mutating a returned map does not affect later calls. No credential flows are implemented in this slice.

## Performance impact

- [x] No inference hot-path impact.

Constant-size header maps built per dispatch attempt; no extra I/O.

## Tests

`gofmt -l`; `go build ./...`; `go vet ./...`; `go test -race ./...`; `git diff --check`; UI `npm ci`, `npm run typecheck`, `npm run build`. All pass. Tests assert catalog membership plus per-provider transports, auth kind, usage flags, passthrough semantics, and the identity-header policy including the no-leak/immutability checks. 66 of 80 baseline rows are now registered.

## Rollback

Revert this PR to remove these identities and header hooks; no durable state depends on them.

## Review discipline

- [x] Coherent head ready for review.
- [x] Prior findings re-read and validated. Finding 1: dual-auth rows could not represent both approved credential modes on a single-valued `AuthKind`; valid, so `Spec.AuthModes` is added with validation, defensive copy, and assertions for every affected provider. Finding 2: Copilot's advertised native `anthropic-messages` binding used `x-api-key` and dropped the Copilot fingerprint; valid, so `anthropicHeaders` now sends bearer auth plus the Copilot identity headers for `github` (and shared provider identity headers for any OAuth-specialized id), with a request-level header test.
- [x] No new head while FIFO reviewer reviews this exact head absent blocker.

## Worktree isolation

- [x] Dedicated feature worktree.
- [x] Worktree bound to this branch only.
- [x] Review fixes remain in this worktree.
