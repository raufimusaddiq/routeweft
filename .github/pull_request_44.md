## Goal

Deliver the Sprint 6 control API read models for `/admin/v1` plus the live-event and console-log resources, so the operator UI can paint Overview and the resources from purpose-built, session-gated endpoints instead of joining many low-level calls (SPEC §22–§23, PRD §16, BDR-014).

## Non-goals

No mutation CRUD for provider/model/Combo/proxy/key resources, no backup/restore API, and no UI. Mutation payload shapes are not invented here; only resources whose shapes the contract already fixes are added (settings PATCH was delivered in PR43). Backup/restore remains a later Sprint 6 PR.

## Contract references

- PRD: §16 (admin/control resources, active-revision reporting), §13 (Usage/observability), PRD-OBS-003 (live events), §14 (Overview/Tokens/Logs operator workflow)
- SPEC: §22 (versioned `/admin/v1`, bounded pagination, stable sort, SSE events), §23 (purpose-built read models), §24 (health), §21 (diagnostic class)
- BDR: BDR-014
- RUNBOOK: §10 logs, admin API protection
- REQUIREMENTS_TRACEABILITY: Admin/control API
- Sprint: Sprint 6, control API read models

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

New session-gated `GET /admin/v1` endpoints: `overview`, `providers`, `provider-nodes`, `connections`, `models`, `aliases`, `pricing`, `combos`, `proxy-pools`, `keys`, `usage`, `requests`, `quota`, `token-saver`, `systemone`, `logs`, and `events` (SSE). All list endpoints use bounded pagination (`page`/`pageSize`, default 50, hard cap 100) with a stable sort and a `total`; invalid pagination returns 400, not 500. `overview` reports runtime health/version/commit/config revision, catalog and connection counts, active API keys, traffic totals plus in-flight count, and telemetry health/counters.

`events` is a bounded SSE channel that emits `request.completed` per finished upstream request from the existing post-completion hook and `config.updated` after settings mutations. Publishing is bounded/non-blocking; slow subscribers shed notifications and fall back to query refresh.

`logs` exposes a bounded newest-first console view (capacity 1000). Only records that reach the redacting handler are visible.

## Storage / migration impact

- [x] No schema/migration impact.

Read models query the existing schema read-only (provider_nodes, provider_connections, provider_models, model_aliases, pricing_overrides, combos, proxy_pools, api_keys, usage_events, request_details). No migration added.

## Security impact

- [x] Security-boundary impact documented and tested.

Every new route is behind the existing admin session gate and same-origin policy. No endpoint returns credential material: connections expose `credentialConfigured` (a boolean) and never the sealed blob or secret; keys expose name/prefix/state and never the stored hash; provider nodes and proxy members expose URL scheme/host/path with userinfo, query, and fragment removed; request details are re-redacted and invalid stored JSON is replaced with an omission marker before serialization. Console logs carry message text plus redacted, size-bounded attributes; secret-shaped keys, `error` values, bearer tokens, and URL credentials are redacted or omitted, and the read model cannot emit raw process attributes. Live events are redacted before fan-out.

## Performance impact

- [x] No inference hot-path impact.

The only request-path addition records an in-flight counter in existing middleware; the SSE publish runs inside the existing post-completion hook and is bounded/non-blocking. Read models are operator-only SQL reads.

## Tests

Passed: `gofmt -l internal cmd`; `go build ./...`; `go vet ./...`; `go test -race -count=1 ./...` (all packages ok, no FAIL); `git diff --check`; UI `npm ci`, `npm run typecheck`, `npm run build`.

New tests cover: session gating for every read model; overview and providers shapes; bounded pagination and clamping with disjoint pages and invalid-page handling; System One model filtering; a smoke pass over every GET resource; SSE authentication, frame delivery, and redaction; and log-buffer bounding, ordering, level filtering, credential/URL redaction, and size limits. `internal/control/events` tests cover bounded fan-out and redaction.

## Rollback

Revert this PR. No schema change. Routes, the event bus, and the log buffer are additive; reverting restores the PR43 settings/auth surface only.

## Review discipline

- [x] This is a coherent head ready for review.
- [x] Prior findings were re-read and valid fixes were bundled. (New PR; no prior findings. Final self-review caught and fixed a log-attribute leakage path before this push.)
- [x] I will wait for review of this exact head before pushing further changes.

## Worktree isolation

- [x] This PR was implemented in its dedicated feature worktree.
- [x] The worktree is bound to this PR's branch only.
- [x] Review fixes will be made in the same worktree.
