# LiteRouter v1 parity inventory and divergence ledger

Status: **R11 disposition audit; limited parallel evaluation only, not full parity certification.**
Parent: #73; sprint tracker #75. Baseline audit: 2026-10-17. R11 review: 2026-10-18, base `0745d88`.

## 1. Scope, pins and interpretation

- Oracle: `raufimusaddiq/literouter@1c3b9f47837d42ea64992063309064af755a14a3`.
- Routeweft: `013d26a2014c16e0e4f2a35aa9acf2fa5bfd4e7c` (the common `main` / `origin/main` base).
- Worktree: `/home/ubuntu/routeweft-wt/s5-parity-73`; branch: `sprint/5-parity-73`.
- The oracle checkout actually reports `2df83ab99d158e3beb53eb88a089fc28e6dae948`, with existing untracked intake files. All oracle evidence below is read using `git show 1c3b9f47:<path>` / `git ls-tree`, never the newer working files. No oracle checkout, database, credentials, runtime or deployment is modified/accessed.
- Baseline #73 read order: AGENTS, PRD, SPEC, BDR, UI_STYLE, RUNBOOK, SPRINT_PLAN, then #73. That inventory was documentation-only. R11 adds the tests, CI and small readiness fixes described below; existing product decisions remain binding.
- Scope reaches API handlers/auth/rewrites, provider registry/executors/OAuth/discovery/quota, pages/components, settings/schema/files/environment, CI and operational contracts. R11 authorizes no deployment, traffic shift or production secret access.

| Inventory | PRESENT | PARTIAL | MISSING | Total |
| --- | ---: | ---: | ---: | ---: |
| API route files (R11 dispositions) | 28 | 39 | 30 | 97 |
| Active providers | 0 | 80 | 1 | 81 |
| Dashboard page files | 0 | 13 | 0 | 13 |
| Supporting root/login/callback page files | 1 | 1 | 1 | 3 |

**PRESENT** means the scoped logical capability has an identifiable implementation; it does not mean differential v1/v2 parity has been demonstrated. **PARTIAL** means some relevant behavior exists but a named gap or unverified integration remains. **MISSING** means no callable equivalent of the capability was found. Missing approved exclusions remain listed. A renamed admin endpoint is not itself a gap (PRD 16, SPEC 22, BDR-014).

No active provider is certified PRESENT as a complete first-class product. R01-R10 added real app account resolution/fallback, native protocol paths, Chat/Responses translation, savers/cache, System One, aliases, workflows, probes and read-only diagnostics. The per-row R11 notes supersede stale baseline shortfalls below. Provider-specific qualification and broad-provider GA remain open (#76, SPEC 36). Green CI is not a full-provider certification.

### R11 disposition and parallel-run boundary

The 97 route-file rows now contain 28 PRESENT, 39 PARTIAL and 30 MISSING
capabilities. PRESENT identifies the scoped implementation, not a live-provider
certificate. The baseline pins and earlier progress notes remain as history.
Current per-row R11 notes take precedence over those historical findings.

Every remaining PARTIAL/MISSING table row carries one explicit disposition:

- `product decision`: an approved exclusion, or a retained gap requiring an
  owner to approve a deferral or complete it. A pending decision is not a waiver.
- `privileged host operation`: no remote lifecycle, install, host-secret read or
  arbitrary helper dashboard passthrough. SPEC 22 permits external diagnostics.
- `no SPEC basis`: PRD may retain a capability, but SPEC does not define its
  Routeweft fields, endpoint or action semantics. The cited owner must specify
  it; R11 does not invent an API or silently remove the product requirement.

R11 adds a pinned differential test, a complete route declaration/guard check,
and a PR CI job named `parity`. It also supplies the scratch image CA bundle,
honors `ROUTEWEFT_LOG_LEVEL`, and returns the remaining throttle window in
`Retry-After` on login/password 429 responses. No schema or inference hot-path
change; existing shared SSRF and usage implementations remain authoritative.

The differential test executes `handleSystemOne` from the pinned v1 source with
synthetic settings/credentials/upstream responses, then compares actual-app
Routeweft status, prepared JSON, native response bytes, error envelopes and
cancellation. The oracle's signal propagation and Routeweft's upstream abort
are both asserted. `inventory.mjs` checks all 97 files / 153 exported HTTP
operations, provider/page enumeration, remote anonymous guards and every
supported client-key form in the pinned dashboard guard. It rejects an
unannotated gap. Run with:

```bash
ORACLE_DIR=/opt/9router make parity-test
```

Only `git show` / `git ls-tree` / commit resolution read the oracle. CI fetches
that immutable commit into disposable storage without a checkout or production
state. The Go app receives an independent temporary database and synthetic
credentials. The gate fails if the pin, source, Node or annotations are absent.
Ordinary unit tests skip the differential test when `ORACLE_DIR` is unset;
the dedicated PR job always sets it.

Coverage limits remain explicit: the 97 shape descriptions are an inventory,
not 153 replayed control workflows. Other protocols use the existing native,
translation, streaming, cancellation and security tests; they are not a full
pinned-v1 differential comparison. R11 therefore remains open for broader
differential cases under #75. Neither green CI nor this PR closes #73/#75 or
certifies full parity. Full serving Combo/Fusion, Messages/other translators,
specialized providers and complete observability remain retained requirements,
not exclusions. Parallel-run procedure and stop conditions are RUNBOOK 25.

### R01 implementation progress (after the inventory baseline)

The following R01 progress note records its first review boundary (Sprint 5.1).
Current table statuses and R11 dispositions supersede its baseline gap claims:

- `internal/app` now wires configured provider/account metadata from immutable
  snapshots and credentials from the memory-first registry into native Chat,
  Responses, Responses Compact and Messages serving. Builtin transport defaults
  and endpoint overrides are compiled; Generic nodes retain their own URLs.
- Alias/qualified/unique bare-model lookup, stable priority ordering,
  fill-first/RR/sticky/provider overrides and cooldown/quota eligibility feed a
  bounded account chain (at most 10 wire attempts, including retries).
  Account/network/quota failures can advance before response headers; client
  errors stay terminal. Retry-same-account outcomes get one cancellable delayed
  retry before advancing. Streams are never replayed after output begins.
- Provider metadata mutations use validate/compile, durable transaction, atomic
  publish. Secrets stay outside snapshots; no schema migration. Global proxy
  settings/no-proxy parsing move to snapshot compilation; pooled transports and
  SSRF guards remain in use. Registry seed models remain ephemeral across config
  writes/restarts; explicit custom overrides persist. Missing durable rows abort
  metadata edits before commit/publication. Missing credentials return distinct
  503 errors; a missing master key stops the chain immediately.
- Evidence: `internal/app/serving_test.go` starts the real app, seeds durable
  configuration, restarts, then exercises native requests after SQLite closes.
  Separate real control-API tests prove immediate create/edit/disable/delete
  publication; tests also cover endpoint overrides, retry limits, terminal
  errors, network fallback, concurrent rotation, stream completion/cancellation.
  `internal/runtime/providers_test.go` covers rollback, compile rejection,
  defensive copies and concurrent snapshot readers during metadata updates.
- R01 remains **PARTIAL**: actual Combo/Fusion/capacity dispatch and pinned Fusion
  defaults/one-success semantics require a dependent reviewed PR. Specialized
  provider refresh/wire behavior (#76), cross-protocol translation (R02), saver
  pipeline (R03), telemetry expansion (R10) and differential certification (R11)
  are not delivered by this step. No production-provider readiness claim.
  Gemini/Ollama/System One handlers still lack actual-app dispatch. Compact
  tests prove configured URL forwarding only, not provider support: pinned v1
  `open-sse/handlers/chatCore.js` restricts native compact to Codex Responses.
  Codex compact gating/headers/body remain R01/R11 + #76 gaps; Kenari compact
  support is not asserted.

### R02 implementation progress (after the inventory baseline)

#75 task R02 is split for review per Sprint 5.2 / AGENTS section 9. The first
boundary implements bidirectional Chat Completions / Responses translation:

- Actual app wiring selects a source-matching native transport first, then
  an implemented translation. Configured endpoint overrides, upstream-model
  remapping, memory-first credentials and the shared account fallback budget
  apply to translated attempts too. SQLite is not read on the serving path.
- Request/response adapters cover instructions, conversation order, parallel
  calls/results, custom-tool input wrappers, reasoning/encrypted continuity,
  image/file/audio request blocks, refusal, usage/cache/reasoning token details.
  Native unknown fields and native streaming bypass the new converters.
- Incremental SSE emits source-protocol events, preserves independent tool
  indices, includes final output and usage, and distinguishes failed/incomplete
  streams. Client disconnect cancels upstream work without replay. Translated
  response input is capped at 32 MiB with 4 MiB event/line bounds (SPEC 9.3).
- Evidence: synthetic `internal/protocol/openai/testdata/chat-responses.json`
  and paired SSE transcripts; adapter tests and actual-app restart/closed-DB,
  native-first/fallback and cancellation regressions in
  `internal/app/translation_test.go`. Fixed expected SSE transcripts pin the
  complete translated wire output. Compact eligibility, stream failure
  cooldown and non-stream exact/over-limit regressions are included. These are
  deterministic Routeweft wire fixtures informed by pinned v1, not the R11
  differential certification suite.
- R02 remains **PARTIAL**. Messages in both directions, count-tokens parity,
  Gemini/Ollama/System One hooks and provider-specific compact semantics are
  separate dependent work, not implemented or certified by this first PR.
  Opaque server-side Responses state (`previous_response_id`/item references)
  is not resolved by these stateless adapters; native Responses retains it.
  Broad provider readiness, saver/cache pipeline wiring (R03) and telemetry
  completeness (R10) remain outside this boundary. No live providers tested.

### R03 implementation progress (serving-pipeline review boundary)

#75 task R03 / Sprint 5.3 now wires the existing saver implementations into
the actual app. This boundary delivers pipeline integration, not helper parity:

- Saver configuration compiles with immutable snapshots. The single upstream
  request boundary applies RTK, Headroom, Caveman, Ponytail, PXPIPE, then cache
  anchors to each prepared body, including account fallback and same-account
  retry. Fallback starts from the original prepared body, not previous output.
- Native Chat, Responses/Compact, Messages, Gemini and Ollama paths, plus both
  implemented Chat/Responses translated directions, use that boundary. Gemini,
  Ollama and System One now share account-attempt dispatch. System One's typed
  state/questions remain opaque rather than receiving invented prompt fields.
  Local count-tokens compatibility uses transformed text, excluding cache metadata.
- Both saver-bypass headers are recognized and allowed in CORS preflight.
  Bypass does not disable cache anchors. Native unknown fields, media/tool IDs,
  thinking/signatures and client cache markers survive text edits; unchanged
  bodies retain their bytes. String Messages system prompts can be anchored.
- Each optional saver fails open independently. Malformed helper output or
  changed text-slot counts/roles cannot corrupt the original wire structure.
  Headroom/PXPIPE use client cancellation, bounded responses/timeouts and pooled
  guarded clients. Settings updates validate helper URLs, timeouts and thresholds.
  `pxpipeUrl` exposes the existing HTTP helper interface, empty by default; no
  helper is installed, started or contacted unless explicitly enabled/configured.
- Evidence: actual-app native/translated fallback and retry ordering tests,
  final-body N/N+1 cache prefix tests, broken-helper/bypass/count-token tests,
  helper cancellation tests, concurrent snapshot publication tests and fixed
  native-wire fixtures in `internal/transforms/body/testdata/wire.json`.
- R03 remains **PARTIAL**. Pinned Headroom calls `/v1/compress` with messages/model
  and optional config, unlike the current Routeweft neutral HTTP interface.
  Pinned PXPIPE uses a local `transformAnthropicMessages` image-rendering library;
  Routeweft's existing HTTP text helper does not implement that image conversion.
  These individual-helper parity gaps require a dependent reviewed PR, alongside
  R09 managed lifecycle/extras/diagnostics. No native helper, production provider
  or pinned differential parity claim is made. Unimplemented R02 translators and
  actual Combo/Fusion serving (R01) remain their existing prerequisite gaps;
  their future wire dispatch must reuse this shared boundary.

### R04 implementation progress (System One serving boundary)

#75 task R04 addresses the System One rows without changing the
audit-baseline table counts:

- Exact envelope validation rejects null/array/empty/scalar questions; null
  state and opaque question values remain valid. Model aliases/prefix resolution
  reuse the serving snapshot; unchanged requests retain their original bytes.
- Actual-app TypeSafe, sole-System-One Generic nodes and Cloudflare Clef/Clef
  Flash transport eligibility are wired. TypeSafe's `jev-latest` and both Clef
  seeds are available. Cloudflare Chat and typed endpoints use per-connection
  `accountId`, persisted by additive schema v5 and published in snapshots.
  Existing connections default to empty/ineligible until configured; explicitly
  Chat-only nodes are not silently expanded. Custom typed-node CRUD/probes and
  Cloudflare typed/Chat probes share the serving endpoint rules.
- Native status, response bytes and allowed headers pass through. Upstream-owned
  SSE streams incrementally without invented events, including when the request
  omits a streaming hint. Native/Cloudflare usage and cache counters are parsed
  separately from the wire response, once per completed request; streaming
  accounting requires clean EOF. Client disconnect cancels upstream work.
- Evidence: `internal/app/systemone_test.go` configures durable accounts,
  restarts the real app, tests DB-closed routing/validation/fallback, fixed
  native/Cloudflare streaming and non-streaming fixtures, incremental relay,
  cancellation and durable usage/daily accounting. Parser, transport, snapshot,
  store migration, control API and scanner tests cover the lower boundaries.
  R03's shared fail-open saver/cache boundary remains unchanged; typed bodies
  do not acquire invented prompt fields even with savers enabled.
- Scope limits: deterministic synthetic fixtures are not live TypeSafe/Clef
  certification or the R11 differential suite. Remaining Cloudflare Chat catalog
  breadth belongs to P03; dashboard configuration workflow belongs to U09.
  Native Headroom `/v1/compress` and PXPIPE image-rendering parity are separate
  **R09** work, not System One behavior; neither is folded into R04.

### R05 implementation progress (public model catalog boundary)

#75 task R05 adds historical route coverage and model discovery semantics.
The audit-baseline inventory/counts below remain unchanged.

- Every retained `/v1` route registers under `/v1/v1` and `/api/v1`; Gemini
  listing/generation also register under `/api/v1beta`. Existing short aliases
  remain. These aliases share handlers, inference-key validation, configured
  CORS, native transports and the R03 saver/final-body cache boundary.
- Explicit legacy decision: retain `/api/v1` and `/api/v1beta` public URLs.
  Pinned `next.config.mjs` exposes these destinations directly; dropping them
  would break clients without simplifying the shared Go handler. They do not
  expose dashboard control operations or excluded media products. This follows
  PRD-API-001-004 / SPEC 34 / BDR-008/014, not a Next module-layout dependency.
- Public model IDs are provider-qualified; aliases and Combo names remain bare.
  Info requires `id`, returns one model's kind/owner/endpoint/context/capabilities,
  rejects disabled/unknown/kind-mismatched IDs, and never resolves an ambiguous
  bare upstream name. Default listing is LLM-only; System One and typed aliases
  have their own filter and endpoint. Qualified nested IDs and Ollama `:tags`
  remain intact.
- Explicit non-media decision: retain image-to-text and web as capability views
  over LLM models (`vision`, `webSearch`, `webFetch`), not standalone products.
  Unclassified names do not imply a capability. Standalone media-kind routes
  remain excluded by PRD 4.2 / BDR-016. This decision and query precedence are
  normative in SPEC 34.1.
- `/api/tags` now lists real enabled LLM catalog IDs, aliases and Combos with
  inference auth/CORS. Intentional divergence from pinned route 73: no two fake
  downloaded-model manifests, sizes or hashes; unavailable artifact metadata
  uses zero/empty/epoch sentinels. This is a routing gateway, not an Ollama model
  file store (PRD 1/4/9, BDR-001). Readiness is not inferred from discovery.
- Evidence: `internal/ingress/catalog_test.go` checks historical route/method/
  auth/CORS coverage, kind views, negative/info/disabled/paused-key behavior and
  fixed `testdata/model-info.json` / `testdata/ollama-tags.json` responses.
  `internal/app/catalog_test.go` restarts configured native accounts, closes
  SQLite, then exercises retained inference and discovery aliases.
- Remaining separate gaps: restricted-key visibility/serving grants remain
  **#78/C02**, not certified by active-key checks; explicit custom kind persistence
  remains R06/C06; provider aliases/seeds/live capability breadth remain P03;
  actual Combo/Fusion dispatch remains R01; pinned differential certification
  remains R11. No live providers or production state were used. R03's native
  Headroom/PXPIPE deferral stays **R09**; System One transport/usage is unchanged.

### R07 implementation progress (first-token model probes)

#75 task R07 replaces the control-plane non-streaming/shared-deadline probes.
The original inventory remains the audit baseline.

- Native Chat, Responses, Messages, Gemini, Ollama and System One probes request
  streaming, stop at first content/reasoning/tool/typed output and cancel the
  remaining generation. JSON-only providers retain native bounded fallback.
- Each model gets a 200-second first-token budget. Discovery keeps its own
  20-second limit; catalog sync retains its two-minute aggregate limit. Model
  lists and inference batches have a ten-minute aggregate ceiling. Per-model
  budgets are independent within that remaining allowance; interruption returns
  completed/interrupted verdicts and stops later work.
- Parent request/batch deadlines classify the interrupted model as `canceled`,
  not `slow`. Multiline SSE events join data lines before decoding. A pooled
  handler-owned transport preserves discovery isolation and connection reuse
  where the protocol permits it. Providers retains partial results on errors,
  can cancel an in-flight model test and states the aggregate limit. Gemini's
  unframed JSON array envelope is accepted.
- Own timeout is `slow`/504; hard failure is `failed`/502; caller cancellation is
  `canceled`/408. `upstreamStatus` preserves the real HTTP response status,
  separately from the synthesized probe status. Upstream HTTP errors retain
  their status with `failed` rather than being relabeled as own timeouts.
  Empty/malformed/error streams cannot
  certify inference, and discovery success alone still cannot pass validation.
- Results separate tested `modelId`, resolved outbound `upstreamModelId` and
  optional successful provider-reported `servedModelId`. Snapshot lookup prefers
  account-native IDs (including passthrough IDs and alias-name collisions), then
  same-provider qualified IDs/aliases. Other slash-bearing manual IDs stay
  literal. Cross-provider aliases and disabled targets fail explicitly.
- Evidence: `internal/control/api/probes_test.go` covers fixed protocol payloads,
  framing, empty/error/oversized responses, slow versus failed classification,
  per-transport first-token cancellation, real client disconnect propagation
  and alias attribution through saved tests, model lists and batch workflows.
- Non-goals: specialized-provider auth/envelopes remain P02; request-log model
  correlation remains R10/C07. Environment timeout overrides and the pinned
  differential CI suite remain R11; the model-probe default now matches v1.
  No inference routing, saver/cache, catalog mutation or Kilo-cache change.

### R06 implementation progress (provider/model control workflows)

#75 task R06 adds the workflow boundary described in SPEC 33.1. The original
inventory rows/counts below remain the audit baseline, not a certification.

- Saved connection tests now perform minimal native inference, including manual
  ID fallback when listing is absent; listing success alone cannot pass a test.
  Unsaved Generic validation uses the same SSRF/auth/native probe path without
  storing credentials or configuration. Listing auth rejection stops fallback.
- Admin availability lists actual account-wide RuntimeState cooldowns as
  `__all`, with paginated reads and provider-scoped reset. Quotas and disabled
  models are not reset. Routeweft does not invent per-model lock state.
- Catalog sync/status covers enabled connections, unions account catalogs,
  refreshes discovered metadata, preserves known/disabled/manual models on
  partial failure and rejects overlapping syncs. Batch validation selects
  enabled connections by the pinned logical groups before credential reads,
  reports individual failures and aggregate totals, bounds work to 200
  connections, rejects overlapping runs, applies a two-minute aggregate deadline
  and propagates cancellation.
- Suggested-model filters match pinned Kenari, OpenRouter, OpenCode, Mimo and
  Airforce rules. Kilo free filtering uses a one-hour in-memory cache with stale
  fallback/warnings. Arbitrary suggestion URLs gain Routeweft's SSRF guard;
  redirects, credential forwarding and unbounded responses are not retained.
- Custom kind/capabilities persist through discovery and restart. Custom delete
  uses candidate compile/commit/publish, rejects referenced entries and removes
  orphaned durable model rows. Schema v7 is additive; old binaries require a
  pre-upgrade backup for rollback. Node route templates have an admin read model.
- Evidence: `internal/control/api/workflows_test.go`, `suggestions_test.go`,
  `internal/runtime/workflows_test.go`, `internal/providers/discovery/auth_test.go`,
  migration upgrade tests and `internal/app/workflows_test.go` cover wire
  fixtures, auth/SSRF/redirects, cancellation, cache/reset races, restart metadata,
  actual-app publication and continued legacy-alias serving with SQLite closed.
- Non-goals: first-token/slow-model probes remain R07; specialized provider auth
  validators and seed/default-pricing breadth remain P02/P03/C06; dashboard
  controls remain U03/U04. No claim of per-model cooldown granularity, scheduler
  parity, or R11 differential certification. R03 savers/cache, R04 typed
  transports/usage and R05 aliases/Ollama/info remain unchanged. Native
  Headroom/PXPIPE helper parity remains R09.

### Area issues and ownership

| Area | Backlog | Gap IDs owned |
| --- | --- | --- |
| Routes/protocol/control operations | #75 | R01-R11 |
| Providers/auth/discovery/quota | #76 | P01-P08 |
| Dashboard workflows/delivery | #77 | U01-U11 |
| Config/data/security/operations | #78 | C01-C10 |

All four issues carry `status:backlog` and reference #73. Each is an area tracker, not permission for a single whole-product implementation PR. Dependent fixes follow the reviewed Sprint 5 prerequisite sequence. #74 owns separately approved non-public deployment.

## 2. API route inventory (97/97)

Each row corresponds to exactly one pinned file. For path `/api/X`, the oracle file is `src/app/api/X/route.js`; bracketed path parameters are literal source-directory names. The count is files, not HTTP operations. Three sibling helper files (`models/test/ping.js`, `providers/[id]/test/testUtils.js`, `providers/suggested-models/filters.js`) are not routes. `GET_DEFAULTS` in pricing is not a Next HTTP verb or a 98th route.

Routeweft evidence prefixes: **A/** = `internal/control/api/`; **I/** = `internal/ingress/`. All mapped admin paths are under `/admin/v1`; their mutations use session + same-origin checks. Bodies below name contract-bearing fields, not every optional JSON member. Unless stated otherwise, v1 errors are JSON `{error:...}`, unknown IDs return 404, invalid input 400; individual error/stream details still require R11.

### v1 auth legend

The effective guard is `src/dashboardGuard.js`, not just each route body.

- **PUB**: guard public allowlist. Login adds password verification/rate limit; logout clears cookie.
- **D**: dashboard JWT or valid local machine CLI token; `requireLogin=false` permits dashboard access. Deny-by-default protects other `/api/*`.
- **S**: valid dashboard JWT or valid CLI token regardless of `requireLogin`.
- **L**: trusted local peer + dashboard auth (or login disabled), or valid CLI token. Forwarded browser origin/proxy restrictions apply. Cursor/Kiro auto-import additionally require S.
- **DB**: S plus current password re-authentication (export header `x-9r-password`, import body `password`); valid CLI token skips password.
- **K**: remote LLM guard requires a valid client key (Bearer, `x-api-key`, `x-goog-api-key`, `?key=`) unless trusted local/CLI; handlers also apply their inference auth/settings. Restricted keys filter model access. OPTIONS handlers expose CORS but remote guard behavior must be captured, not assumed.
- `MINIMAL_PROFILE=true` hides the entire Headroom prefix even though files exist. This inventory covers the pinned full surface, not only a deployed profile.

| # | v1 path | Methods | Auth | Request / response shape | Status | Routeweft path / evidence | Shortfall / disposition |
| ---: | --- | --- | --- | --- | --- | --- | --- |
| 01 | `/api/auth/login` | POST | PUB | {password} => success + auth_token cookie; 401/429 | PARTIAL | /admin/v1/auth/login; A/api.go, A/throttle.go; Retry-After regression in A/api_test.go | Username/opaque sessions intentionally differ (D04); R11 adds Retry-After to login/password 429 responses. Public bootstrap/recovery policy remains C03. R11: product decision: recovery/bootstrap/login-disabled policy remains unapproved (#78/C03); mandatory sessions stay in place, without a new bypass (SPEC 12). |
| 02 | `/api/auth/logout` | POST | PUB | no body => {success}; clear cookie | PRESENT | /admin/v1/auth/logout; A/api.go | D01,D04: Routeweft session cookie, same logout capability. |
| 03 | `/api/auth/reset-password` | POST | L | no body => {success}; delete configured password | MISSING | No reset action; A/password.go only rotates current password | C03: local emergency reset absent; do not copy bypass without approval. R11: product decision: recovery/bootstrap/login-disabled policy remains unapproved (#78/C03); mandatory sessions stay in place, without a new bypass (SPEC 12). |
| 04 | `/api/auth/status` | GET | PUB | => {requireLogin,hasPassword,authenticated} | PARTIAL | /admin/v1/auth/session; A/api.go | C03: session identity available, public bootstrap/login-required shape absent. R11: product decision: recovery/bootstrap/login-disabled policy remains unapproved (#78/C03); mandatory sessions stay in place, without a new bypass (SPEC 12). |
| 05 | `/api/combos/[id]` | GET, PUT, DELETE | D | id; PUT Combo fields => Combo; DELETE => success | PARTIAL | /admin/v1/combos/{id}; A/combos.go, A/read_models.go | R01,C04,C06: CRUD/order exists; runtime path and v1 strategy/Fusion tuning incomplete. R11: product decision: retained Combo/Fusion serving is still unwired, not waived (PRD 11, BDR-011); #75/R01 must finish it before any Combo-dependent parallel traffic. |
| 06 | `/api/combos` | GET, POST | D | POST {name,models,kind} => Combo (201); GET {combos} | PARTIAL | /admin/v1/combos; A/combos.go, A/read_models.go | R01,C04,C06: fallback/RR/sticky/Fusion app parity unproven. R11: product decision: retained Combo/Fusion serving is still unwired, not waived (PRD 11, BDR-011); #75/R01 must finish it before any Combo-dependent parallel traffic. |
| 07 | `/api/console-logs` | GET, DELETE | D | GET => {success,logs}; DELETE clears buffer | PRESENT | GET /admin/v1/logs; A/logs_handler.go, A/logs.go | R10: GET plus DELETE /admin/v1/logs clear; A/logs_handler.go. |
| 08 | `/api/console-logs/stream` | GET | D | => SSE console-log updates | MISSING | /admin/v1/events does not carry console records; A/events.go | R10,U08: no console-log SSE equivalent. R11: product decision: PRD-OBS-001/003 retains the missing diagnostics/live workflow; SPEC 21/22 does not complete its wire contract; #75/R10 and #77 own completion, not an approved waiver. |
| 09 | `/api/headroom/extras` | GET, POST, DELETE | D | ?log=1 => install log; GET extras; POST/DELETE extra spec => result | MISSING | Settings only; A/read_models.go | R09: extras/install-log control absent. Not built: privileged host operation / passthrough; needs product decision (SPEC §22). R11: privileged host operation: SPEC 22 forbids remote helper lifecycle/extras/arbitrary passthrough; operator manages an external helper locally; R09 is not a remote-process API. |
| 10 | `/api/headroom/proxy/[...path]` | GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS | L | path/query/body passthrough => proxied bytes; dashboard HTML rewritten | MISSING | No proxy control route | R09: managed proxy/dashboard access absent; local-only must remain protected. Not built: privileged host operation / passthrough; needs product decision (SPEC §22). R11: privileged host operation: SPEC 22 forbids remote helper lifecycle/extras/arbitrary passthrough; operator manages an external helper locally; R09 is not a remote-process API. |
| 11 | `/api/headroom/restart` | POST | D | no body => {success,...process status}; external mode 400 | MISSING | No lifecycle route | R09: restart ownership/external-mode behavior absent. Not built: privileged host operation / passthrough; needs product decision (SPEC §22). R11: privileged host operation: SPEC 22 forbids remote helper lifecycle/extras/arbitrary passthrough; operator manages an external helper locally; R09 is not a remote-process API. |
| 12 | `/api/headroom/start` | POST | L | no body => {success,...process status}; external mode 400 | MISSING | No lifecycle route | R09: managed start absent. Not built: privileged host operation / passthrough; needs product decision (SPEC §22). R11: privileged host operation: SPEC 22 forbids remote helper lifecycle/extras/arbitrary passthrough; operator manages an external helper locally; R09 is not a remote-process API. |
| 13 | `/api/headroom/status` | GET | D | => {...health,url,managedPid} | PARTIAL | GET /admin/v1/headroom/status; A/token_saver_helpers.go | R09: live health + settings; no managedPid (Routeweft owns no process). R11: privileged host operation: external helper health is implemented; no owned process exists to report a managed PID/install state (SPEC 22). |
| 14 | `/api/headroom/stop` | POST | L | no body => stopped/process status | MISSING | No lifecycle route | R09: managed stop absent. Not built: privileged host operation / passthrough; needs product decision (SPEC §22). R11: privileged host operation: SPEC 22 forbids remote helper lifecycle/extras/arbitrary passthrough; operator manages an external helper locally; R09 is not a remote-process API. |
| 15 | `/api/health` | GET, OPTIONS | PUB | GET => {ok:true}; OPTIONS CORS | PRESENT | /health/live, /health/ready; internal/app/app.go | D02: split liveness/readiness, plain-text ok; no upstream dependency. |
| 16 | `/api/keys/[id]` | GET, PUT, DELETE | S | GET key; PUT {isActive,access:{restricted,allow}} => key; DELETE success | PRESENT | /admin/v1/keys; A/keys.go, A/key_access_test.go | C02 key lifecycle and access policy are implemented; digest-only, create-once secret is D03 (SPEC 12.1). |
| 17 | `/api/keys` | GET, POST | S | GET {keys}; POST {name} => {key,name,id,machineId} (201) | PRESENT | /admin/v1/keys; A/keys.go, A/read_models.go | Access-aware create/list and secret-once delivery; D03 intentionally omits recoverable secrets and machine identity. |
| 18 | `/api/locale` | POST | PUB | POST {locale} => {success,locale} + locale cookie | MISSING | No locale endpoint/UI catalog | U10: locale support not explicitly excluded by PRD. R11: no SPEC basis: SPEC defines neither supported locales nor locale persistence/API; #77/U10 must settle the preference contract. |
| 19 | `/api/models/alias` | GET, PUT, DELETE | D | GET {aliases}; PUT {model,alias}; DELETE ?alias => success | PRESENT | GET/POST /admin/v1/aliases, DELETE /aliases/{alias}; A/providers.go | D01: logical alias CRUD retained with split provider/model fields. |
| 20 | `/api/models/availability` | GET, POST | D | GET availability state; POST {action,provider,model} => {ok} | MISSING | No availability reset action | R06 adds /admin/v1/models/availability GET/POST reset. RuntimeState is account-scoped; v1 per-model lock persistence/granularity remains C05. R11: product decision: R06 adds /admin/v1/models/availability GET/POST reset. RuntimeState is account-scoped; v1 per-model lock persistence/granularity remains C05. Follow-up remains in the cited area tracker; no retained requirement is waived. |
| 21 | `/api/models/catalog-sync` | GET, POST | D | GET {sync state,catalog}; POST => {success,result} or 503 | PRESENT | GET/POST /admin/v1/models/catalog-sync; A/workflows.go | R06: sync action/status, cancellation and preserved catalog on failure; A/workflows_test.go. |
| 22 | `/api/models/custom` | GET, POST, DELETE | D | GET {models}; POST {providerAlias,id,type,name,caps}; DELETE identifiers | PRESENT | GET/POST/DELETE /admin/v1/models; A/providers.go | R06: kind/capability upserts persist across restart; referenced models reject deletion with 409; runtime/workflows_test.go. |
| 23 | `/api/models/disabled` | GET, POST, DELETE | D | GET ?providerAlias; POST {providerAlias,ids}; DELETE providerAlias/id | PRESENT | PATCH /admin/v1/models/disabled, GET /models; A/providers.go | D01: single-model {providerId,modelId,disabled} plus disabled flag; bulk via repeated mutations. |
| 24 | `/api/models` | GET, PUT | D | GET {models}; PUT {model,alias} => success | PRESENT | GET /admin/v1/models, POST /aliases; A/read_models.go, A/providers.go | D01: model read/alias mutation represented; readiness/listing gaps separately tracked. |
| 25 | `/api/models/test` | POST | D | {model,kind} => {ok,status,...probe}; own timeout 504/network 502 | PARTIAL | POST /admin/v1/connections/{id}/test-models; A/providers.go | R07 native first-token probes, own 200s/aggregate 600s deadlines, alias/upstream attribution and cancel are implemented. Specialized provider wire/auth probes remain P02/P06. R11: product decision: R07 native first-token probes, own 200s/aggregate 600s deadlines, alias/upstream attribution and cancel are implemented. Specialized provider wire/auth probes remain P02/P06. Follow-up remains in the cited area tracker; no retained requirement is waived. |
| 26 | `/api/oauth/[provider]/[action]` | GET, POST | D | GET authorize/device/proxy/poll; POST exchange/poll => flow/status/connection | MISSING | Provider PKCE/header helpers only; no admin OAuth routes | P04: no full admin OAuth action dispatcher; hidden providers remain excluded D06. R11: no SPEC basis: PRD-AUTH-002 retains this workflow but SPEC 35 has no Routeweft flow/state/identity/action contract; #76/P04 must define it before implementation. |
| 27 | `/api/oauth/codex/bulk-import` | POST | D | token batch => {success,failed,results} | PRESENT | POST /admin/v1/oauth/{provider}/bulk-import; A/oauth.go | Array, single object and accounts wrapper accepted; serial for priority; per-item index/ok/id, no credential echoed. |
| 28 | `/api/oauth/codex/import-token` | POST | D | {accessToken,name} => success + connection summary | PRESENT | POST /admin/v1/oauth/codex/import; A/oauth.go | Single-item path of the same flow; JWT claims backfill identity and account data. |
| 29 | `/api/oauth/cursor/auto-import` | GET | L + S | => {found,...IDE discovery/import information} | MISSING | No host import action | P04: host discovery must be local and always authenticated. R11: privileged host operation: host credential discovery needs an approved local-only import policy; no remote filesystem reader will be added (#76/P04, PRD-AUTH-002). |
| 30 | `/api/oauth/cursor/import` | POST, GET | D | POST {accessToken,machineId} => success; GET instructions | MISSING | No Cursor import action | P04: machine identity/session validation missing. R11: no SPEC basis: PRD-AUTH-002 retains this workflow but SPEC 35 has no Routeweft flow/state/identity/action contract; #76/P04 must define it before implementation. |
| 31 | `/api/oauth/gitlab/pat` | POST | D | PAT payload => {success}; verification errors 401 | MISSING | No PAT validation action | P04: GitLab token exchange/identity missing. R11: no SPEC basis: PRD-AUTH-002 retains this workflow but SPEC 35 has no Routeweft flow/state/identity/action contract; #76/P04 must define it before implementation. |
| 32 | `/api/oauth/grok-cli/bulk-import` | POST | D | token batch => aggregate success/failed/results | PRESENT | POST /admin/v1/oauth/{provider}/bulk-import; A/oauth.go | Same handler; snake_case and camelCase, email backfilled from the id token. |
| 33 | `/api/oauth/iflow/cookie` | POST | D | {cookie} containing BXAuth => account/API-key result | PRESENT | POST /admin/v1/oauth/iflow/import; onboarding/manual.go | Cookie exchanged for the account key; iflow declares AuthCookie in AuthModes so the stored mode matches. |
| 34 | `/api/oauth/kiro/api-key` | POST | D | {apiKey,region} => validated credential/connection result | MISSING | No Kiro action | P04: validation/region wiring absent. R11: no SPEC basis: PRD-AUTH-002 retains this workflow but SPEC 35 has no Routeweft flow/state/identity/action contract; #76/P04 must define it before implementation. |
| 35 | `/api/oauth/kiro/auto-import` | GET | L + S | => {found,...CLI token/cache information} | MISSING | No host import action | P04: local host credential discovery absent. R11: privileged host operation: host credential discovery needs an approved local-only import policy; no remote filesystem reader will be added (#76/P04, PRD-AUTH-002). |
| 36 | `/api/oauth/kiro/import-cli-proxy` | POST | D | CLI-proxy credential payload => connection result | MISSING | No CLI-proxy import action | P04: payload parsing/identity checks absent. R11: no SPEC basis: PRD-AUTH-002 retains this workflow but SPEC 35 has no Routeweft flow/state/identity/action contract; #76/P04 must define it before implementation. |
| 37 | `/api/oauth/kiro/import` | POST | D | {refreshToken,clientId,clientSecret,region,authMethod,profileArn} => result | MISSING | No Kiro token action | P04: refresh/profile identity not modeled as workflow. R11: no SPEC basis: PRD-AUTH-002 retains this workflow but SPEC 35 has no Routeweft flow/state/identity/action contract; #76/P04 must define it before implementation. |
| 38 | `/api/oauth/kiro/social-authorize` | GET | D | ?provider=google/github => authorization URL + verifier/state | MISSING | No social authorize action | P04: social flow absent. R11: no SPEC basis: PRD-AUTH-002 retains this workflow but SPEC 35 has no Routeweft flow/state/identity/action contract; #76/P04 must define it before implementation. |
| 39 | `/api/oauth/kiro/social-exchange` | POST | D | {code,codeVerifier,provider} => connection result | MISSING | No social exchange action | P04: exchange/callback validation absent. R11: no SPEC basis: PRD-AUTH-002 retains this workflow but SPEC 35 has no Routeweft flow/state/identity/action contract; #76/P04 must define it before implementation. |
| 40 | `/api/oauth/xiaomi-mimo/api-key` | POST | D | {apiKey,uid,baseUrl,mimoPassToken,mimoUserId,mimoCUserId} => result | MISSING | No Xiaomi key action | P04: provider-specific identity/import absent. R11: no SPEC basis: PRD-AUTH-002 retains this workflow but SPEC 35 has no Routeweft flow/state/identity/action contract; #76/P04 must define it before implementation. |
| 41 | `/api/oauth/xiaomi-mimo/auto-import` | GET | D | => {found,...browser/session discovery status} | MISSING | No host import action | P04: auto-import absent; approved host-access policy required. R11: privileged host operation: host credential discovery needs an approved local-only import policy; no remote filesystem reader will be added (#76/P04, PRD-AUTH-002). |
| 42 | `/api/pricing` | GET, PATCH, DELETE | D | GET map; PATCH nested provider/model prices; DELETE ?provider&model resets | PARTIAL | GET/POST/DELETE /admin/v1/pricing; A/providers.go | R06,C06: override CRUD exists; default-price catalog/reset behavior not equivalent. GET_DEFAULTS is not an exported HTTP verb. R11: product decision: undefined Follow-up remains in the cited area tracker; no retained requirement is waived. |
| 43 | `/api/provider-nodes/[id]` | PUT, DELETE | D | PUT {name,prefix,apiType,baseUrl} => {node}; DELETE success | PRESENT | PATCH/DELETE /admin/v1/provider-nodes/{id}; A/providers.go | R04/R06: builtin/Generic typed node edits and deletion; System One transport tested through the actual app. |
| 44 | `/api/provider-nodes` | GET, POST | D | GET {nodes}; POST {name,prefix,apiType,baseUrl,type} => node (201) | PRESENT | GET/POST /admin/v1/provider-nodes; A/providers.go | R04/R06: Generic C/R/M and custom typed System One nodes; D07 unified node representation. |
| 45 | `/api/provider-nodes/routes` | GET | D | ?id => {id,routes} | PRESENT | GET /admin/v1/provider-nodes/{id}/routes; A/workflows.go | R06: resolved node transport templates; A/workflows_test.go. |
| 46 | `/api/provider-nodes/validate` | POST | D | {baseUrl,apiKey,type,modelId} => {valid,error?,method?} | PRESENT | POST /admin/v1/provider-nodes/validate; A/workflows.go | R06: native inference fallback with manual model after failed/absent discovery; shared SSRF policy; A/workflows_test.go. |
| 47 | `/api/providers/[id]/models` | GET | D | id => provider-specific authenticated model list | PARTIAL | POST /admin/v1/connections/{id}/discover; A/providers.go | P03: shared discovery does not implement every provider listing/token exchange. R11: product decision: undefined Follow-up remains in the cited area tracker; no retained requirement is waived. |
| 48 | `/api/providers/[id]` | GET, PUT, DELETE | D | GET connection; PUT name/priority/auth/proxy/providerSpecificData; DELETE success | PARTIAL | GET /connections list, PATCH/DELETE /connections/{id}; A/providers.go | C06,P04: opaque provider-specific fields/import state not retained fully. R11: product decision: undefined Follow-up remains in the cited area tracker; no retained requirement is waived. |
| 49 | `/api/providers/[id]/test-models` | POST | D | id => {provider,connectionId,results} for configured models | PARTIAL | POST /admin/v1/connections/{id}/test-models; A/providers.go | R07 native probes preserve completed results and distinguish parent/own timeout; UI consumes interrupted bodies and permits abort. Specialized codecs remain P02. R11: product decision: R07 native probes preserve completed results and distinguish parent/own timeout; UI consumes interrupted bodies and permits abort. Specialized codecs remain P02. Follow-up remains in the cited area tracker; no retained requirement is waived. |
| 50 | `/api/providers/[id]/test` | POST | D | id => validation/latency/error/status result | PARTIAL | POST /admin/v1/connections/{id}/test; A/providers.go | R06/R07 native inference validation with manual fallback and first-token cancellation is implemented; provider-specific auth exchange remains P02/P04. R11: product decision: R06/R07 native inference validation with manual fallback and first-token cancellation is implemented; provider-specific auth exchange remains P02/P04. Follow-up remains in the cited area tracker; no retained requirement is waived. |
| 51 | `/api/providers/client` | GET | D | ?provider,accountStatus,sort,page,pageSize => paginated safe account view | PARTIAL | /admin/v1/providers and /connections; A/read_models.go | R10,U04: no equivalent provider/status filtering/joined page semantics. R11: product decision: PRD-OBS-001/003 retains the missing diagnostics/live workflow; SPEC 21/22 does not complete its wire contract; #75/R10 and #77 own completion, not an approved waiver. |
| 52 | `/api/providers/kilo/free-models` | GET | D | => {models,cached,warning?}; preserve cached data on fetch failure | PRESENT | GET /admin/v1/providers/kilo/free-models; A/suggestions.go | R06: one-hour cache, free filtering and warm-cache-only stale fallback; A/suggestions_test.go. |
| 53 | `/api/providers` | GET, POST | D | GET {connections}; POST {provider,apiKey,name,...config} => connection (201) | PARTIAL | /admin/v1/providers, /connections; A/providers.go | P01,P04,C06: catalog+connection CRUD exists, missing muse/auth workflows/provider state. R11: product decision: undefined Follow-up remains in the cited area tracker; no retained requirement is waived. |
| 54 | `/api/providers/suggested-models` | GET | D | ?url,type => {data} filtered upstream suggestions | PRESENT | GET /admin/v1/providers/suggested-models; A/suggestions.go | R06: pinned Kenari/OpenRouter/OpenCode/Mimo/Airforce filters, bounded SSRF-protected fetch; A/suggestions_test.go. |
| 55 | `/api/providers/test-batch` | POST | D | {mode,providerId} => aggregate batch results | PRESENT | POST /admin/v1/providers/test-batch; A/workflows.go | R06/R07: across-connection/provider batches, isolated credential failures, first-token probes and partial interruption results; A/workflows_test.go. |
| 56 | `/api/providers/validate` | POST | D | {provider,apiKey,providerSpecificData} => {valid,error?} | PARTIAL | /connections/{id}/test; A/providers.go | R06 adds pre-save native inference validation; specialized identity/exchange validators remain P02/P04. R11: product decision: R06 adds pre-save native inference validation; specialized identity/exchange validators remain P02/P04. Follow-up remains in the cited area tracker; no retained requirement is waived. |
| 57 | `/api/proxy-pools/[id]` | GET, PUT, DELETE | D | GET pool; PUT pool fields; DELETE => success / 409 in use | PRESENT | GET/PUT/DELETE /admin/v1/proxy-pools/{id}; A/proxy_pools.go | R08: delete returns 409 while bound. |
| 58 | `/api/proxy-pools/[id]/test` | POST | D | pool id => connectivity/status result | PRESENT | POST /admin/v1/proxy-pools/{id}/test; A/proxy_pools.go | R08: classified per-member result. |
| 59 | `/api/proxy-pools/netlify-deploy` | POST | D | Netlify deployment credentials/config => {proxyPool,deployUrl} (201) | MISSING | No provisioning action | R08: pending product decision; cloud-sync exclusion does not itself exclude outbound relay provisioning. R11: no SPEC basis: SPEC 20/22 defines proxy use/testing, not Netlify provisioning credentials or deployment; #75/R08 must obtain a relay product/host contract. |
| 60 | `/api/proxy-pools` | GET, POST | D | ?isActive,includeUsage => {proxyPools}; POST pool => pool (201) | PARTIAL | GET/POST /admin/v1/proxy-pools; A/proxy_pools.go | R08: isActive filter and includeUsage not provided. R11: no SPEC basis: SPEC 22 defines pool CRUD but not isActive/includeUsage query semantics; no new filter/usage fields invented. |
| 61 | `/api/pxpipe/health` | POST, GET | D | => {healthy,checks,...} | PARTIAL | GET /admin/v1/pxpipe/health; A/token_saver_helpers.go | R09: GET only; POST variant not provided. R11: no SPEC basis: SPEC 22 explicitly exposes GET health only; POST is not an additional required diagnostics action. |
| 62 | `/api/pxpipe/install` | POST | D | => installation info + health | MISSING | internal/transforms/pxpipe has HTTP transform/health/log/stats helpers only | R09: managed install/auto-install control absent. Not built: privileged host operation / passthrough; needs product decision (SPEC §22). R11: privileged host operation: SPEC 22 forbids remote helper lifecycle/extras/arbitrary passthrough; operator manages an external helper locally; R09 is not a remote-process API. |
| 63 | `/api/pxpipe/logs` | GET | D | ?limit<=500 => logs result | PRESENT | GET /admin/v1/pxpipe/logs; A/token_saver_helpers.go | R09: in-process log, last 100 entries. |
| 64 | `/api/pxpipe/restart` | POST | D | => status after restart | MISSING | No lifecycle route | R09: managed restart absent. Not built: privileged host operation / passthrough; needs product decision (SPEC §22). R11: privileged host operation: SPEC 22 forbids remote helper lifecycle/extras/arbitrary passthrough; operator manages an external helper locally; R09 is not a remote-process API. |
| 65 | `/api/pxpipe/start` | POST | D | => status; 409 NOT_INSTALLED if policy forbids install | MISSING | No lifecycle route | R09: start/install policy workflow absent. Not built: privileged host operation / passthrough; needs product decision (SPEC §22). R11: privileged host operation: SPEC 22 forbids remote helper lifecycle/extras/arbitrary passthrough; operator manages an external helper locally; R09 is not a remote-process API. |
| 66 | `/api/pxpipe/stats` | GET | D | ?limit<=500 => aggregate/recent transform stats | PRESENT | GET /admin/v1/pxpipe/stats; A/token_saver_helpers.go | R09: process-local counters; reset on restart. |
| 67 | `/api/pxpipe/status` | GET | D | => helper installation/load/runtime state | PARTIAL | GET /admin/v1/pxpipe/status; A/token_saver_helpers.go | R09: settings + health + stats; no install/load state. R11: privileged host operation: external helper health is implemented; no owned process exists to report a managed PID/install state (SPEC 22). |
| 68 | `/api/pxpipe/stop` | POST | D | => {stopped,...status} | MISSING | No lifecycle route | R09: stop absent. Not built: privileged host operation / passthrough; needs product decision (SPEC §22). R11: privileged host operation: SPEC 22 forbids remote helper lifecycle/extras/arbitrary passthrough; operator manages an external helper locally; R09 is not a remote-process API. |
| 69 | `/api/settings/database` | GET, POST | DB | GET export JSON; POST {password,...export} => success | PARTIAL | /admin/v1/backup, /backup/restore[/check]; A/backup.go | Routeweft ZIP validation/restore is implemented (D08). Parallel evaluation recreates configuration in an independent DB; v1 history/key import is not provided (C01). R11: product decision: use isolated manual recreation for this parallel evaluation (BDR-001/017); import/history continuity remains #78/C01 and is not claimed. |
| 70 | `/api/settings/proxy-test` | POST | D | proxy config => {ok,...connectivity,error} | PRESENT | POST /admin/v1/settings/proxy-test; A/proxy_pools.go | R08: tests a url or the configured global proxy. |
| 71 | `/api/settings/require-login` | GET | PUB | => {requireLogin}, defaults true | PARTIAL | /admin/v1/auth/session; A/api.go | C03: requireLogin stored true; no public setting read/change behavior. R11: product decision: recovery/bootstrap/login-disabled policy remains unapproved (#78/C03); mandatory sessions stay in place, without a new bypass (SPEC 12). |
| 72 | `/api/settings` | GET, PATCH | D | GET safe merged settings; PATCH settings/password fields => safe settings | PARTIAL | GET/PATCH /admin/v1/settings; A/settings.go | D01,D04; C03-C06: allowlist string settings, missing/changed semantics and live effects. R11: product decision: undefined Follow-up remains in the cited area tracker; no retained requirement is waived. |
| 73 | `/api/tags` | OPTIONS, GET | D | => Ollama {models} manifest + CORS | PRESENT | GET /api/tags; I/catalog.go | R05: Ollama tags, aliases, disabled/access filtering; I/catalog_test.go. D03/D11 require client auth instead of dashboard/local bypass. |
| 74 | `/api/usage/[connectionId]/codex-reset-credits` | GET, POST | D | GET reset-credit balance; POST consume => result | PARTIAL | internal/providers/codex/usage.go, internal/quota; no action route | Codex helper methods and quota service exist; no operator reset-credit action is wired. PRD-QUOTA-001 retains the capability; SPEC has no action HTTP/body contract. R11: no SPEC basis: SPEC has no reset-credit admin route/body despite PRD-QUOTA-001; #76/P05 must specify safe consumption and authorization. |
| 75 | `/api/usage/[connectionId]` | GET | D | ?force=1 => provider usage/limits/reset or unavailable | PARTIAL | GET /admin/v1/quota, POST /quota/refresh; A/quota_refresh.go | Codex/Claude observers and background refresh exist; other provider usage clients/reset actions remain #76/P05. R11: product decision: Codex/Claude observers and background refresh exist; other provider usage clients/reset actions remain #76/P05. Follow-up remains in the cited area tracker; no retained requirement is waived. |
| 76 | `/api/usage/chart` | GET | D | ?period => chart series | PARTIAL | GET /admin/v1/usage/summary; A/usage_summary.go | R10: daily series exists; v1 periods/cost/metric dimensions incomplete. R11: product decision: PRD-OBS-001/003 retains the missing diagnostics/live workflow; SPEC 21/22 does not complete its wire contract; #75/R10 and #77 own completion, not an approved waiver. |
| 77 | `/api/usage/history` | GET | D | => accumulated usage stats/history | PARTIAL | GET /admin/v1/usage, /usage/summary; A/usage_summary.go | R10: historical retention/aggregate shape/cost parity not proven. R11: product decision: PRD-OBS-001/003 retains the missing diagnostics/live workflow; SPEC 21/22 does not complete its wire contract; #75/R10 and #77 own completion, not an approved waiver. |
| 78 | `/api/usage/logs` | GET | D | => recent log list | PARTIAL | GET /admin/v1/usage; A/read_models.go | R10: filters (provider/model/connection/apiKey/status/dates) and API-key attribution added; cost and detail correlation remain. R11: product decision: PRD-OBS-001/003 retains the missing diagnostics/live workflow; SPEC 21/22 does not complete its wire contract; #75/R10 and #77 own completion, not an approved waiver. |
| 79 | `/api/usage/providers` | GET | D | => {providers:[{id,name}]} distinct from request details, names resolved via nodes/registry | PARTIAL | /admin/v1/providers + /usage/summary; A/read_models.go | R10: catalog/breakdown is not the distinct request-detail provider filter. Dashboard topology/activity is a separate retained workflow. R11: product decision: PRD-OBS-001/003 retains the missing diagnostics/live workflow; SPEC 21/22 does not complete its wire contract; #75/R10 and #77 own completion, not an approved waiver. |
| 80 | `/api/usage/request-details` | GET | D | ?page,pageSize,provider,model,connectionId,status,startDate,endDate => redacted details | PARTIAL | GET /admin/v1/requests; A/read_models.go | R10: bounded read exists; no normal app detail producer/filter equivalence. R11: product decision: PRD-OBS-001/003 retains the missing diagnostics/live workflow; SPEC 21/22 does not complete its wire contract; #75/R10 and #77 own completion, not an approved waiver. |
| 81 | `/api/usage/request-logs` | GET | D | => request-log list | PARTIAL | GET /admin/v1/usage; A/read_models.go | R10: unchanged by this slice; request/transform/fallback/cost context incomplete. R11: product decision: PRD-OBS-001/003 retains the missing diagnostics/live workflow; SPEC 21/22 does not complete its wire contract; #75/R10 and #77 own completion, not an approved waiver. |
| 82 | `/api/usage/stats` | GET | D | ?period => totals/breakdowns | PARTIAL | GET /admin/v1/usage/summary; A/usage_summary.go | R10: token/cache totals exist; cost/TTFT/API-key attribution incomplete in app. R11: product decision: PRD-OBS-001/003 retains the missing diagnostics/live workflow; SPEC 21/22 does not complete its wire contract; #75/R10 and #77 own completion, not an approved waiver. |
| 83 | `/api/usage/stream` | GET | D | => SSE activeRequests/recentRequests/errorProvider updates | PARTIAL | GET /admin/v1/usage/stream; A/usage_stream.go, internal/control/events/usage.go; W/UsageLogs.tsx | #106: completed requests stream to the Requests tab, filtered and bounded (SPEC usage stream). Departs from D09 (one channel) with a second, usage-only channel. Still missing: pending/in-flight events and activeRequests/errorProvider aggregates. R11: product decision: PRD-OBS-001/003 retains the missing diagnostics/live workflow; SPEC 21/22 does not complete its wire contract; #75/R10 and #77 own completion, not an approved waiver. |
| 84 | `/api/v1/api/chat` | OPTIONS, POST | K | Ollama {model,messages,stream} => message JSON / NDJSON | PARTIAL | /v1/api/chat; I/compat.go | Native Ollama app serving, fallback, savers and aliases exist (R01/R03/R05). Non-native translation hooks remain R02. R11: product decision: retained R01/R02/provider paths remain incomplete; no release waiver exists (PRD 5, SPEC 9/10); exclude affected configurations from parallel traffic pending #75/#76. |
| 85 | `/api/v1/chat/completions` | OPTIONS, POST | K | Chat {model,messages,tools,stream,...} => completion / SSE | PARTIAL | /v1/chat/completions; I/ingress.go | Configured native Chat plus Chat/Responses translation, fallback, savers/cache and key policy exist. Messages/other protocol hooks and specialized codecs remain R02/P02. R11: product decision: retained R01/R02/provider paths remain incomplete; no release waiver exists (PRD 5, SPEC 9/10); exclude affected configurations from parallel traffic pending #75/#76. |
| 86 | `/api/v1/messages/count_tokens` | OPTIONS, POST | K | Messages input => {input_tokens} | PARTIAL | /v1/messages/count_tokens; I/messages_count_tokens.go | Local transformed count estimate and key policy exist. Native provider count parity and cross-protocol tokenization are not certified; R02/P02. R11: product decision: retained R01/R02/provider paths remain incomplete; no release waiver exists (PRD 5, SPEC 9/10); exclude affected configurations from parallel traffic pending #75/#76. |
| 87 | `/api/v1/messages` | OPTIONS, POST | K | Messages {model,messages,system,tools,...} => message / SSE | PARTIAL | /v1/messages; I/ingress.go | Native Messages app serving, fallback, final cache and key policy exist. Messages cross-protocol translators remain R02. R11: product decision: retained R01/R02/provider paths remain incomplete; no release waiver exists (PRD 5, SPEC 9/10); exclude affected configurations from parallel traffic pending #75/#76. |
| 88 | `/api/v1/models/[...model]` | OPTIONS, GET | K | provider/model => model; ?kind => kind-filtered list | PRESENT | GET /v1/models/{provider}/{model...}; I/catalog.go | R05: provider/model and retained kind/access-aware lookup; I/catalog_test.go; SPEC 34. |
| 89 | `/api/v1/models/[kind]` | OPTIONS, GET | K | image/tts/stt/embedding/image-to-text/web => {object:list,data} | PRESENT | GET /v1/models/{kind}; I/catalog.go | R05: retained non-media kind semantics; excluded standalone media kinds are empty, never invented media services; SPEC 34. |
| 90 | `/api/v1/models/info` | OPTIONS, GET | K | ?id,kind => model capability/context information | PRESENT | GET /v1/models/info; I/catalog.go | R05: id/kind/access-aware info, aliases and upstream ID; I/catalog_test.go. |
| 91 | `/api/v1/models` | OPTIONS, GET | K | => {object:list,data} including aliases/Combos, access-filtered | PARTIAL | /v1/models; I/ingress.go | R05 access-aware aliases/model catalog is implemented; live/static provider catalog drift remains #76/P03. R11: product decision: R05 access-aware aliases/model catalog is implemented; live/static provider catalog drift remains #76/P03. Follow-up remains in the cited area tracker; no retained requirement is waived. |
| 92 | `/api/v1/responses/compact` | OPTIONS, POST | K | Responses compact input => compact output | PARTIAL | /v1/responses/compact; I/ingress.go | Native compact eligibility and no-translator 503 semantics exist. Codex-only provider normalization/compact capability parity remains R01/P02. R11: product decision: retained R01/R02/provider paths remain incomplete; no release waiver exists (PRD 5, SPEC 9/10); exclude affected configurations from parallel traffic pending #75/#76. |
| 93 | `/api/v1/responses` | OPTIONS, POST | K | Responses {model,input,tools,stream,...} => response / event SSE | PARTIAL | /v1/responses; I/ingress.go | Native Responses and Chat/Responses translation with fallback/cache are implemented. Messages/other protocol hooks remain R02; specialized codecs P02. R11: product decision: retained R01/R02/provider paths remain incomplete; no release waiver exists (PRD 5, SPEC 9/10); exclude affected configurations from parallel traffic pending #75/#76. |
| 94 | `/api/v1` | GET, OPTIONS | K | => public API index | PRESENT | GET /v1; I/ingress.go | D01: index handler exists; aliases/auth negative cases require R05/R11. |
| 95 | `/api/v1/systemone` | OPTIONS, POST | K | {model,state,questions,...} => typed answers + usage / upstream stream | PRESENT | POST /v1/systemone and retained aliases; I/compat.go | R04/R11: exact questions validation, native/custom/Cloudflare typed transport, opaque streams, shared usage and cancellation; internal/app/systemone_test.go and parity_test.go. |
| 96 | `/api/v1beta/models/[...path]` | OPTIONS, POST | K | model:generateContent / streamGenerateContent; contents => candidates / SSE | PARTIAL | /v1beta/models/{rest...}; I/compat.go | Native Gemini app serving, aliases, savers and client-key policy are wired. Non-native translation and specialized Google provider behavior remain R02/P02. R11: product decision: retained R01/R02/provider paths remain incomplete; no release waiver exists (PRD 5, SPEC 9/10); exclude affected configurations from parallel traffic pending #75/#76. |
| 97 | `/api/v1beta/models` | OPTIONS, GET | K | => {models:[{name,supportedGenerationMethods,...}]} | PARTIAL | /v1beta/models; I/compat.go | R05 access-aware Gemini listing is implemented; provider-specific discovery/seed drift remains #76/P03. R11: product decision: R05 access-aware Gemini listing is implemented; provider-specific discovery/seed drift remains #76/P03. Follow-up remains in the cited area tracker; no retained requirement is waived. |

### Public rewrites and compatibility aliases (outside the 97-file count)

Oracle evidence: `next.config.mjs`; Routeweft evidence: `I/ingress.go:Attach`.

| v1 client path / rule | Status | Routeweft equivalent / remaining work |
| --- | --- | --- |
| `/v1/:path*` and `/v1` map to `/api/v1` | PRESENT | R03/R05: retained aliases and both bypass headers are wired; same client-key/CORS policy; internal/ingress/catalog_test.go, internal/app/transforms_test.go. Underlying untranslated/provider paths remain R02/P02. |
| `/v1beta/:path*` and `/v1beta` | PARTIAL | Models + generation handlers exist; root `/v1beta` target has no route file in the pinned source. Do not invent a root contract. R11: no SPEC basis: pinned v1 and SPEC 34 have no /v1beta root contract; implemented models/generation aliases are covered separately. |
| `/responses` | PRESENT | R03/R05: retained aliases and both bypass headers are wired; same client-key/CORS policy; internal/ingress/catalog_test.go, internal/app/transforms_test.go. Underlying untranslated/provider paths remain R02/P02. |
| `/codex/:path*` | PRESENT | R03/R05: retained aliases and both bypass headers are wired; same client-key/CORS policy; internal/ingress/catalog_test.go, internal/app/transforms_test.go. Underlying untranslated/provider paths remain R02/P02. |
| `/v1/v1/:path*` and `/v1/v1` | PRESENT | R03/R05: retained aliases and both bypass headers are wired; same client-key/CORS policy; internal/ingress/catalog_test.go, internal/app/transforms_test.go. Underlying untranslated/provider paths remain R02/P02. |
| Ollama `/api/tags` | PRESENT | R03/R05: retained aliases and both bypass headers are wired; same client-key/CORS policy; internal/ingress/catalog_test.go, internal/app/transforms_test.go. Underlying untranslated/provider paths remain R02/P02. |
| Browser CORS/body/h2c | PRESENT | D11 explicit-origin CORS/body-limit and client-key policies are intentional (PRD-API-005, SPEC 34). internal/app/parity_test.go proves an HTTP/1.1 h2c Upgrade attempt still serves native inference and strips hop headers; prior-knowledge HTTP/2 serving is not claimed. |
| Client saver bypass headers | PRESENT | R03: both x-9router-token-saver and x-literouter-token-saver are recognized and permitted in CORS preflight; cache anchoring remains active; internal/app/transforms_test.go. |

## 3. Provider inventory (81 active + 3 hidden)

Oracle registry: `open-sse/providers/registry/index.js`. Exactly **81 active imports**, including `muse`; three commented-out providers remain hidden. Routeweft `internal/providers/registry/builtins.go` declares **81 identities**: `muse` was added by the owner decision on #116, which is the PRD 21 / SPEC 37 drift review this row asked for. PRD 6 and PROVIDER_BASELINE now carry 81 rows.

Every provider row below has its own pinned source `open-sse/providers/registry/<id>.js`, and its Routeweft declaration in `internal/providers/registry/builtins.go`. v1 executor column maps every registered specialized factory; `default` means `open-sse/executors/default.js`, not absence of provider behavior. Native protocol codes: C=Chat, R=Responses, M=Messages, G=Gemini, O=Ollama, SO=System One. A specialized executor may convert/envelope a wire whose registry default says C.

**Evidence / dimension notation:** P=PARTIAL, M=MISSING, R=PRESENT, -=not advertised as a quota product by the v1 registry. Dimensions are **L/A/Q/S/N/E/T** = authenticated listing, auth/import/refresh workflow, quota/limits, streaming, non-streaming, error mapping, token accounting. These are source-coverage assessments, not passed live-provider tests. Every P remains a real gap until pinned fixtures/app-level proof exists (P08).

- Shared listing: `internal/providers/discovery/{discovery,lifecycle}.go`, `A/providers.go:fetchModels`; not every provider's token exchange, filter or format.
- Shared storage/auth: `internal/credentials/{store,registry,resolver,seal}.go`; API-key/cookie/token storage does not implement OAuth onboarding. Codex/Claude PKCE helpers exist; app passes nil refresh callback to credential registry.
- Shared S/N/E/T: `I/{ingress,compat}.go`, `internal/providers/shared/{errors,usage}.go`, native protocol parsers and deterministic fixtures. Specialized modules are mostly declarations, not executable codecs.
- Q: only Codex and Claude usage observers are wired in `internal/app/app.go:initializeQuota`. Other v1-advertised usage/limits remain M; no v1 quota claim is fabricated for providers marked "-".
- Seed column preserves the original `013d26a` audit counts: literal inline v1 model IDs / Routeweft StaticModels / absent v1 IDs. It is not a current seed recount or live catalog count; R11 notes identify later changes such as System One. Zero is not "no model support". Full catalog qualification remains P03.

| Provider | v1 auth | v1 wire / executor | v1 catalog | Seeds v1/RW/absent | L/A/Q/S/N/E/T | Status | Explicit shortfall |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `alicode` | API key | C / default | seed/provider-specific | 8/0/8 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `alicode-intl` | API key | C / default | seed/provider-specific | 7/0/7 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `alims-intl` | API key | C / default | seed/provider-specific | 7/0/7 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `alitp-intl` | API key | C / default | seed/provider-specific | 6/0/6 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `anthropic` | API key | M / default | seed/provider-specific | 7/3/4 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `antigravity` | OAuth | antigravity / antigravity | seed/provider-specific | 20/4/19 | P/M/M/M/M/M/M | PARTIAL | P02-P05: specialized wire/auth/refresh/listing/limits and stream codecs absent. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `api-airforce` | apikey | C / default | dynamic:airforce-free; passthrough | 3/0/3 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `azure` | API key | C / azure | seed/provider-specific | 0/0/0 | P/P/-/P/P/P/P | PARTIAL | P02/C06: deployment/API-version/org/endpoint provider data and specialized URL rules. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `baidu` | apikey | C / default | seed/provider-specific | 7/0/7 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `bazaarlink` | apikey | C / default | seed/provider-specific | 24/0/24 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `blackbox` | API key | C / default | seed/provider-specific | 10/0/10 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `bluesminds` | apikey | C / default | seed/provider-specific | 14/0/14 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `byteplus` | API key | C / default | seed/provider-specific | 7/0/7 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `cerebras` | API key | C / default | seed/provider-specific | 7/0/7 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `chutes` | API key | C / default | seed/provider-specific | 0/0/0 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `claude` | OAuth/PKCE | M / default | seed/provider-specific | 5/5/0 | P/P/P/P/P/P/P | PARTIAL | P04/P05: auth/refresh actions/auto-ping absent; PKCE/usage helpers only. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `cline` | oauth | C / default | seed/provider-specific | 8/8/0 | P/M/-/P/P/P/P | PARTIAL | P02-P05: onboarding/provider hooks/refresh; quota where advertised; app contracts missing. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `clinepass` | apikey+oauth | C / default | seed/provider-specific | 10/10/0 | P/M/-/P/P/P/P | PARTIAL | P02-P05: onboarding/provider hooks/refresh; quota where advertised; app contracts missing. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `cloudflare-ai` | apikey | C+SO / default | seed/provider-specific | 15/0/15 | P/P/-/P/P/P/P | PARTIAL | R04 supplies Clef/Clef-flash System One transport and usage; #115 PR-H: the account-scoped URLs (`{accountId}/ai/v1/chat/completions` for Chat, `{accountId}/ai/run/{model}` for Clef; pin `registry/cloudflare-ai.js:26,30`, `executors/default.js:158-161`) are built only by `wire.ResolveTarget` (`wire/cloudflare.go`), fail closed on a missing or unsafe account ID, and auth is the default Bearer; implemented from the pin and unit tests, unverified against a live Workers AI gateway. Cloudflare chat/account-scoped catalog qualification remains #76/P03/P08. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `codebuddy-cn` | oauth+apikey | C / codebuddy-cn | seed/provider-specific | 13/13/0 | P/M/M/P/P/P/P | PARTIAL | P02-P05: onboarding/provider hooks/refresh; quota where advertised; app contracts missing. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `codebuddy-intl` | oauth+apikey | C / codebuddy-intl | seed/provider-specific | 15/15/0 | P/M/M/P/P/P/P | PARTIAL | P02-P05: onboarding/provider hooks/refresh; quota where advertised; app contracts missing. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `codex` | OAuth/PKCE | R / codex | seed/provider-specific | 17/18/1 | P/P/P/P/P/P/P | PARTIAL | P04/P05: import/bulk/reset actions and refresh wiring absent; PKCE/usage helpers only. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `cohere` | API key | C / default | seed/provider-specific | 3/0/3 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `commandcode` | API key | commandcode / commandcode | seed/provider-specific | 11/11/0 | P/P/M/M/M/M/M | PARTIAL | P02-P05: specialized wire/auth/refresh/listing/limits and stream codecs absent. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `cursor` | OAuth/import | cursor / cursor | seed/provider-specific | 14/14/0 | P/M/-/M/M/M/M | PARTIAL | P02-P05: specialized wire/auth/refresh/listing/limits and stream codecs absent. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `deepseek` | API key | C+M / default | seed/provider-specific | 9/0/9 | P/P/M/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof; limits client absent. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `featherless` | API key | C / default | seed/provider-specific | 7/0/7 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `fireworks` | API key | C / default | seed/provider-specific | 3/0/3 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `gemini` | apikey | G / default | seed/provider-specific | 11/11/0 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `gemini-cli` | OAuth | gemini-cli / gemini-cli | seed/provider-specific | 7/7/0 | P/M/M/M/M/M/M | PARTIAL | P02-P05: specialized wire/auth/refresh/listing/limits and stream codecs absent. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `github` | OAuth | C+R+M / github | seed/provider-specific | 17/17/0 | P/M/M/P/P/P/P | PARTIAL | P02-P05: Copilot token exchange, model-name native Messages routing, live models/limits. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `gitlab` | OAuth/PAT | C / default | seed/provider-specific | 0/0/0 | P/M/-/P/P/P/P | PARTIAL | P02-P05: onboarding/provider hooks/refresh; quota where advertised; app contracts missing. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `glm` | API key | M+C / default | seed/provider-specific | 8/0/8 | P/P/M/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof; limits client absent. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `glm-cn` | API key | C / default | seed/provider-specific | 10/0/10 | P/P/M/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof; limits client absent. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `grok-cli` | oauth | R / grok-cli | seed/provider-specific | 4/0/4 | P/M/M/P/P/P/P | PARTIAL | P02-P05: onboarding/provider hooks/refresh; quota where advertised; app contracts missing. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `grok-web` | cookie | grok-web / grok-web | seed + passthrough | 12/0/12 | P/M/-/M/M/M/M | PARTIAL | P02-P05: specialized wire/auth/refresh/listing/limits and stream codecs absent. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `groq` | API key | C / default | seed/provider-specific | 4/0/4 | P/P/M/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof; limits client absent. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `hyperbolic` | API key | C / default | seed/provider-specific | 8/0/8 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `iflow` | OAuth | C / iflow | seed/provider-specific | 15/15/0 | P/M/-/P/P/P/P | PARTIAL | P02-P05: onboarding/provider hooks/refresh; quota where advertised; app contracts missing. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `kenari` | API key | C+R+M / default | dynamic:kenari; passthrough | 67/0/67 | P/P/M/P/P/P/P | PARTIAL | P03/P05/P06: quota client, Responses function-tools/system-role normalization, live catalog/67 seeds. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `kilo-gateway` | apikey | C / default | seed/provider-specific | 6/0/6 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `kilocode` | OAuth | C / default | dynamic:openrouter-free; passthrough | 8/0/8 | P/M/-/P/P/P/P | PARTIAL | P02-P05: onboarding/provider hooks/refresh; quota where advertised; app contracts missing. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `kimchi` | oauth+apikey | C / kimchi | seed + passthrough | 8/0/8 | P/M/-/P/P/P/P | PARTIAL | P02-P05: onboarding/provider hooks/refresh; quota where advertised; app contracts missing. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `kimi` | oauth+apikey | M+C+R / default | seed/provider-specific | 10/10/0 | P/M/M/P/P/P/P | PARTIAL | P02-P05: device/header/quota behavior; the Responses transport and `X-Msh-*` serving headers landed in #115 PR-C. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `kiro` | OAuth + API key | kiro / kiro | seed/provider-specific | 44/6/39 | P/M/M/M/M/M/M | PARTIAL | P02-P05: specialized wire/auth/refresh/listing/limits and stream codecs absent. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `llm7` | apikey | C / default | seed + passthrough | 5/0/5 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `mimo-free` | none | C / mimo-free | dynamic:mimo-free; passthrough | 1/0/1 | P/R/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `minimax` | API key | M+C / default | seed/provider-specific | 7/0/7 | P/P/M/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof; limits client absent. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `minimax-cn` | API key | M+C / default | seed/provider-specific | 4/0/4 | P/P/M/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof; limits client absent. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `mistral` | API key | C / default | seed/provider-specific | 3/0/3 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `mmf` | none | C / mimo-free | seed/provider-specific | 1/0/1 | P/R/-/P/P/P/P | PARTIAL | P02/P03: v1 executor alias mmf=mimo-free; no-auth/seed parity. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `morph` | apikey | C / default | seed/provider-specific | 6/0/6 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `muse` | API key | R / default | seed/provider-specific | 2/0/2 | M/M/-/M/M/M/M | PARTIAL | P01: identity, Responses transport, base URL and both seed models now exist (registry entry + generated seeds). R11: product decision: muse is adopted and routable on #116; remaining adapter/credential/listing test depth is retained work under #76, not an approved deferral and not a waiver. |
| `nebius` | API key | C / default | seed/provider-specific | 1/0/1 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `nvidia` | apikey | C / default | seed/provider-specific | 7/0/7 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `ollama` | apikey | O / default | seed/provider-specific | 8/0/8 | P/P/M/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof; limits client absent. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `ollama-local` | local/optional key | O / ollama-local | seed/provider-specific | 0/0/0 | P/P/-/P/P/P/P | PARTIAL | P02/P03: local model discovery/proxy/private-URL policy and native codecs need contracts. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `openai` | API key | C / default | seed/provider-specific | 31/20/11 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `opencode` | none | C / opencode | dynamic:opencode-free; passthrough | 3/0/3 | P/R/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; **endpoint (#115 PR-E2):** Chat models post to `{base}/zen/v1/chat/completions` (`executors/opencode.js:471-476`) and `forceStream` is mirrored (`registry/opencode.js:20`, `Hook.ForceStream`). Implemented, **UNVERIFIED against the live gateway**. The `muse-spark*` (`/zen/v1/responses`) and `union-alpha` (`/zen/v1/messages`) models (`registry/opencode.js:35-37`, `executors/opencode.js:271-278`) are **refused**, not routed: the registry has no per-model transport. The pin's `opencode/1.18.31` UA and decoy tools (`executors/opencode.js:18,457-462,493`) are not implemented, so the free-tier gate will likely 403; accepted. The `reasoningInject` quirk (`registry/opencode.js:26`) is not part of this change. shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `opencode-go` | API key | C+M+R / opencode-go | seed/provider-specific | 29/0/29 | P/P/M/P/P/P/P | PARTIAL | P02/P03: multi-protocol model dispatch/envelopes/seeds. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `openrouter` | apikey | C / default | dynamic:openrouter-free; passthrough | 0/0/0 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `perplexity` | API key | C / default | seed/provider-specific | 4/0/4 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `perplexity-agent` | API key | R / default | dynamic:openai; passthrough | 11/0/11 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `perplexity-web` | cookie | perplexity-web / perplexity-web | seed/provider-specific | 7/7/0 | P/M/-/M/M/M/M | PARTIAL | P02-P05: specialized wire/auth/refresh/listing/limits and stream codecs absent. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `poolside` | apikey | C / default | seed/provider-specific | 3/0/3 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `qoder` | oauth+apikey | C / qoder | seed/provider-specific | 16/16/0 | P/M/M/M/M/M/M | PARTIAL | P02-P05: COSY signing call sites are wired (#115 PR-I; full 19-header set, stored machineId, final body/URL), but production dispatch and model probes cannot reach them until the Qoder conversion leg lands; unverified against a live gateway. Specialized envelope/stream codecs, PAT exchange, refresh/listing/limits remain unqualified. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `sambanova` | apikey | C / default | seed/provider-specific | 1/0/1 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `siliconflow` | API key | C / default | seed/provider-specific | 16/0/16 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `tencent` | apikey | C / default | seed/provider-specific | 2/0/2 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `together` | API key | C / default | seed/provider-specific | 4/0/4 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `tokenrouter` | API key | C / default | dynamic:openai; passthrough | 22/0/22 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `typesafe` | API key | SO / default | seed/provider-specific | 1/0/1 | P/P/-/P/P/P/M | PARTIAL | R04 supplies jev-latest, typed validation/transports/stream/accounting; internal/app/systemone_test.go. Full provider qualification remains #76/P08. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `venice` | API key | C / default | dynamic:openai; passthrough | 9/0/9 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `vercel-ai-gateway` | API key | C / default | dynamic:openai; passthrough | 0/0/0 | P/P/M/P/P/P/P | PARTIAL | P02/P03/P08: shared adapter/listing not app-level provider proof; limits client absent. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `vertex` | API key | vertex / vertex | seed/provider-specific | 4/4/0 | P/P/-/M/M/M/M | PARTIAL | P02-P05: specialized wire/auth/refresh/listing/limits and stream codecs absent. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `vertex-partner` | API key | C / vertex | seed/provider-specific | 4/0/4 | P/P/-/P/P/P/P | PARTIAL | **endpoint (#115 PR-E2):** `{base}/v1/projects/{projectId}/locations/global/endpoints/openapi/chat/completions` (`executors/vertex.js:76-81`), `projectId` from the connection's provider settings only; absent -> the request is refused naming `projectId`. Implemented, **UNVERIFIED against a real gateway**. Not implemented: the project-id probe (`:35-49,158-164`), project from SA JSON / ADC (`:71-74`), SA/ADC token minting and the raw-key `?key=` (`:80`, credential in a URL). The pin has no auth-method setting (it infers from the key shape), so none is added; the stored credential is sent as the bearer token. P02/C06: Vertex credentials/signing/project/location, partner wire. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `volcengine-ark` | API key | C / default | seed/provider-specific | 9/0/9 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `xai` | oauth+apikey | C / default | seed/provider-specific | 9/6/3 | P/M/-/P/P/P/P | PARTIAL | P02-P05: onboarding/provider hooks/refresh; quota where advertised; app contracts missing. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `xiaomi-mimo` | oauth+apikey | C+M / xiaomi-mimo | seed/provider-specific | 6/6/0 | P/M/M/P/P/P/P | PARTIAL | P02-P05: onboarding/provider hooks/refresh; quota where advertised; app contracts missing. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `xiaomi-tokenplan` | API key | C+M / xiaomi-tokenplan | seed/provider-specific | 9/0/9 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |
| `zed` | OAuth | C / zed | seed + passthrough | 0/0/0 | P/M/M/P/P/P/P | PARTIAL | P02-P05: onboarding/provider hooks/refresh; quota where advertised; app contracts missing. R11: product decision: provider qualification remains #76/P02-P08; no GA waiver (PRD-PROV-004). |

### Hidden providers and non-provider assets

| Provider / surface | Status | Disposition |
| --- | --- | --- |
| `trae` | MISSING | Intentional D06; v1 registry import commented out. Executor/OAuth files exist but not an active product provider. R11: product decision: excluded hidden providers under PRD-PROV-003 / D06; no production support claim. |
| `devin-cli` | MISSING | Intentional D06; commented out, local shell/filesystem agent boundary. R11: product decision: excluded hidden providers under PRD-PROV-003 / D06; no production support claim. |
| `windsurf` | MISSING | Intentional D06; commented out, tool-call limitations. R11: product decision: excluded hidden providers under PRD-PROV-003 / D06; no production support claim. |
| Standalone image/video/speech/embedding provider icons | MISSING | Intentional D05. `public/providers` assets are not active LLM registry entries. R11: product decision: excluded standalone media under PRD 4.2 / BDR-016 / D05; multimodal LLM content stays retained. |
| Generic compatible provider nodes | PARTIAL | `internal/providers/generic`, credentials store and admin/UI CRUD exist; D07 permits unified model. R01/R06/P08 still apply. R11: product decision: native configured C/R/M paths are implemented (R01/R06); cross-protocol and full provider qualification remain #75/R02/#76, without a waiver (BDR-023). |

#73's "369 provider-related files" is not a provider count or an acceptance test.
This audit identifies all active registry entries and all specialized executor factories, plus the OAuth action inventory above. A narrower reproducible file set (`open-sse/providers/`, `open-sse/executors/`, `src/lib/oauth/`) contains 167 tracked files; other listing/quota/control/test files live elsewhere. No claim is made that 369 files were individually behavior-tested.

## 4. Dashboard and supporting pages (16/16)

Oracle dashboard prefix **V/** = `src/app/(dashboard)/dashboard/`; **W/** = Routeweft `ui/src/`. Routeweft uses ten hash-selected screens in `W/main.tsx`, not Next page modules. Route relocation is D01/D04; absent functionality remains a gap. These rows cover every pinned `src/app/**/page.js`, including root/supporting pages.

| Oracle page file | Routeweft screen / evidence | Status | Shortfall |
| --- | --- | --- | --- |
| `V/page.js` | `#overview`; W/Overview.tsx | PARTIAL | Runtime/count/error summary exists; readiness per provider, topology/live activity/invalidation incomplete (U03/U05/U08). R11: product decision: the row's remaining UI workflow is retained by PRD 14; #77 must finish and browser-qualify it before declaring full UI parity. No waiver or production-readiness claim. |
| `V/endpoint/page.js` | `#endpoint-key`; W/EndpointAndKey.tsx | PARTIAL | Key create/copy/pause/revoke and the restricted/unrestricted model, alias and Combo allowlist editor exist (U02); picker lists only reachable entries (enabled connection, not disabled). Adapter-pool enforcement for restricted keys still waits on Combo planning being wired (C02). R11: product decision: the row's remaining UI workflow is retained by PRD 14; #77 must finish and browser-qualify it before declaring full UI parity. No waiver or production-readiness claim. |
| `V/providers/page.js` | `#providers`; W/Providers.tsx | PARTIAL | Catalog/filter and complete paged reads exist (U04). Muse/readiness/health and real OAuth/import flows remain U03/P01/P04. R11: product decision: the row's remaining UI workflow is retained by PRD 14; #77 must finish and browser-qualify it before declaring full UI parity. No waiver or production-readiness claim. |
| `V/providers/new/page.js` | Inline add-account/Generic form; W/Providers.tsx | PARTIAL | API-key/token paste is not OAuth/device/PAT/cookie/import onboarding; pre-save validation incomplete (U03/R06/P04). R11: no SPEC basis: OAuth/import flow/state contracts are not defined in SPEC 35; #76/P04 and #77/U03 must define them before a functional onboarding UI. |
| `V/providers/[id]/page.js` | Selected provider panel; W/Providers.tsx | PARTIAL | R06/R07/U04 provide native model tests, alias/price/delete/order and paged reads; special imports, quota/compatibility/default-model and richer proxy management remain U03/P04/P05. R11: product decision: the row's remaining UI workflow is retained by PRD 14; #77 must finish and browser-qualify it before declaring full UI parity. No waiver or production-readiness claim. |
| `V/combos/page.js` | `#combo-capability-adapter`; W/Combos.tsx | PARTIAL | Ordered members/Fusion/vision/audio pool UI exists; member and pool pickers read every page of models/aliases (typed, validated against the catalog), flag unknown/disabled/unconnected members, and show which restricted keys list a Combo by name (also warned on rename/delete). No per-member weight/share exists in SPEC §15 or the API; app dispatch, per-Combo Fusion tuning/object override semantics, end-to-end empty/deselect/context behavior not proven (U09/R01/C04). R11: product decision: the row's remaining UI workflow is retained by PRD 14; #77 must finish and browser-qualify it before declaring full UI parity. No waiver or production-readiness claim. |
| `V/systemone/page.js` | `#system-one`; W/SystemOne.tsx | PARTIAL | R04/R07 supply actual typed serving, seeds, Cloudflare and first-token probes. This page still reads the first 100 models/nodes/accounts and lacks custom typed-node/account-ID onboarding (U09/U03). R11: product decision: the row's remaining UI workflow is retained by PRD 14; #77 must finish and browser-qualify it before declaring full UI parity. No waiver or production-readiness claim. |
| `V/usage/page.js` | `#usage`; W/Usage.tsx | PARTIAL | Totals/cache/daily chart/recent list exist; costs, details, topology, account/key filters/paging/live updates incomplete (U05/U08). R11: product decision: the row's remaining UI workflow is retained by PRD 14; #77 must finish and browser-qualify it before declaring full UI parity. No waiver or production-readiness claim. |
| `V/quota/page.js` | `#quota-tracker`; W/Quota.tsx | PARTIAL | State/read/refresh exists for first 100 accounts; reset credits/auto-ping/visibility and full provider clients absent (U06/P05). R11: product decision: the row's remaining UI workflow is retained by PRD 14; #77 must finish and browser-qualify it before declaring full UI parity. No waiver or production-readiness claim. |
| `V/token-saver/page.js` | `#token-saver`; W/TokenSaver.tsx | PARTIAL | R03 ordered serving pipeline/cache and settings are wired. UI lacks the R09 read-only helper health/log/stats controls; privileged lifecycle controls stay excluded by SPEC 22 (U07). R11: privileged host operation: serving savers/cache and read-only helper diagnostics are wired; lifecycle/install controls stay outside the remote UI under SPEC 22; native helper wire parity remains R09. |
| `V/console-log/page.js` | `#console-log`; W/ConsoleLog.tsx | PARTIAL | Bounded filter/page/refresh exists; clear/live stream missing (U08/R10). R11: product decision: the row's remaining UI workflow is retained by PRD 14; #77 must finish and browser-qualify it before declaring full UI parity. No waiver or production-readiness claim. |
| `V/profile/page.js` | `#settings`; W/Settings.tsx | PARTIAL | Password, settings, proxy fields, backup/restore present; requireLogin, semantic maps, compatibility flags, locale/live controls incomplete (U10/C03-C06). R11: product decision: recovery/login-disabled/locale policies await #78/C03 and #77/U10; no implicit bypass or locale contract (SPEC 12/22). |
| `src/app/dashboard/settings/pricing/page.js` | Providers pricing panel; W/Providers.tsx | PARTIAL | Relocation allowed D01; built-in default pricing/reset/complete model catalog incomplete (U04/R06). R11: product decision: the row's remaining UI workflow is retained by PRD 14; #77 must finish and browser-qualify it before declaring full UI parity. No waiver or production-readiness claim. |
| `src/app/login/page.js` | W/SignIn.tsx + main.tsx | PARTIAL | Session login/logout exists; deliberate username/session changes D04, recovery/locale behavior unresolved U10/C03. R11: product decision: recovery/login-disabled/locale policies await #78/C03 and #77/U10; no implicit bypass or locale contract (SPEC 12/22). |
| `src/app/callback/page.js` | No callback screen | MISSING | OAuth code/state/callback workflow absent (U03/P04). R11: no SPEC basis: OAuth/import flow/state contracts are not defined in SPEC 35; #76/P04 and #77/U03 must define them before a functional onboarding UI. |
| `src/app/page.js` | W/main.tsx Overview default | PRESENT | Production static delivery is tested in internal/app/static_test.go; UI assets copied by Docker (U01). |

**Delivery (U01 resolved):** Docker copies Vite output to `/ui` and sets `ROUTEWEFT_UI_DIR=/ui`; `internal/app/static.go` serves it from `Handler` (tested in `static_test.go`). `ui/scripts/smoke.sh` starts Vite preview, so it does not establish production delivery. `ui/package.json` now has unit tests; CI runs typecheck/unit/build. Browser smoke is still not a required PR job (U11).

**Cross-page gap:** only the Usage requests tab subscribes (`EventSource`, #106); every other page still relies on manual refresh. Backend SSE exists, but manual refresh is not one live channel + refresh fallback (U08, PRD-OBS-003). Providers paging now uses ProviderPaging/fetchAll (U04). Quota and System One still read the first 100 accounts/models; U06/U09 remain open. Live events remain U08.

## 5. Configuration and durable data

### 5.1 Every default setting in the pinned v1 repository

Oracle: `src/lib/db/repos/settingsRepo.js:DEFAULT_SETTINGS`.
Routeweft: `internal/runtime/compiler.go:defaultSettings`, `settings_update.go`, `A/settings.go`.
PRESENT here refers only to the named stored/compiled setting; where its effect is not wired, status is PARTIAL. Values cross the admin API as strings with a writable allowlist, not arbitrary v1 JSON settings (D01).

| v1 setting (default) | Routeweft mapping | Status | Disposition / shortfall |
| --- | --- | --- | --- |
| cloudEnabled (false) | absent | MISSING | Intentional D12, PRD 4.2/15; no cloud sync. R11: product decision: standalone/media/hidden-provider exclusions D04-D06/D12-D13 apply (PRD 4.2, BDR-001/005/016); retained helper functionality is not excluded. |
| stickyRoundRobinLimit (3) | same | PRESENT | R01: snapshot-compiled provider-local sticky limit feeds actual serving; internal/app/serving_test.go and runtime/providers_test.go. |
| providerStrategies ({}) | same key, map[string]string | PARTIAL | v1 values are objects with fallbackStrategy/sticky limit/proxyPoolId/rotateStrategy; current API rejects them (C04/C06). R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| quotaVisibility ({}) | same JSON object | PARTIAL | Stored; SPEC 22 explicitly leaves runtime semantics unspecified; v1 visibility workflow needs product decision (C05/U06). R11: no SPEC basis: the exact v1 field/env runtime semantics are not specified by SPEC 22/27; #78 must define the behavior before exposing another knob. |
| comboStrategy (fallback) | same | PARTIAL | Stored/compiled; R01 app integration. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| comboStickyRoundRobinLimit (1) | same | PARTIAL | Global setting exists; compiled Combo defaults its own limit=1, potentially shadowing global values (C04/R01). R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| comboStrategies ({}) | same key, map[string]string | PARTIAL | v1 objects carry fallbackStrategy/judgeModel/fusionTuning; missing representation and tuning (C04/C06). R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| capacityAdapter.vision ({enabled:false,roundRobin:false,models:[]}) | capacityAdapterVision {enabled,pool} | PARTIAL | Renaming/normalization D10; API omits roundRobin/strategy, so saves lose selection policy (C04); R01 empty/deselect/trim runtime proof. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| capacityAdapter.pdf (same) | capacityAdapterPDF | PARTIAL | State preserved, no standalone PDF API implied; C04. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| capacityAdapter.audioInput (same) | capacityAdapterAudioInput | PARTIAL | Visible LLM audio adapter; C04/R01. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| capacityAdapter.videoInput (same) | capacityAdapterVideoInput | PARTIAL | State preserved, no standalone video product; C04. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| requireLogin (true) | same default, not writable; session always required | PARTIAL | Strong auth fits baseline security; disabling capability not explicitly rejected by approved contract (C03), not quietly "intentional". R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| requireApiKey (true) | same | PRESENT | Compiled key index and setting mutation; policy scope C02 remains separate. |
| enableObservability (false) | same | PARTIAL | Worker options sampled on initialize, not live settings updates (C05). R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| observabilityMaxRecords (1000) | same | PARTIAL | Stored/worker startup option; dynamic retention/detail capture gaps C05/R10. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| observabilityBatchSize (20) | same | PARTIAL | Same default; startup-only worker configuration C05. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| observabilityFlushIntervalMs (5000) | same | PARTIAL | Same default; live effect C05. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| observabilityMaxJsonSize (5) | 5242880 bytes | PARTIAL | Important: actual pinned v1 requestDetailsRepo multiplies by 1024 (5 KiB), despite Routeweft PRD 15 requiring 5 MB. Routeweft's 5 MiB follows PRD; migration must encode units, not multiply by an assumed MiB (D10/C01/C04). R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| outboundProxyEnabled (false) | same | PRESENT | R08: compiled settings, pooled global proxy/no-proxy binder, A/proxy_pools_test.go and internal/app/proxy_hotpath_test.go. JSON noProxy array is D10. |
| outboundProxyUrl ("") | same | PRESENT | R08: compiled settings, pooled global proxy/no-proxy binder, A/proxy_pools_test.go and internal/app/proxy_hotpath_test.go. JSON noProxy array is D10. |
| outboundNoProxy ("") | noProxy JSON string array, [] | PRESENT | R08: compiled settings, pooled global proxy/no-proxy binder, A/proxy_pools_test.go and internal/app/proxy_hotpath_test.go. JSON noProxy array is D10. |
| dnsToolEnabled ({}) | boolean false | PARTIAL | Provider-keyed compatibility map cannot round-trip through boolean validator (C04). R11: no SPEC basis: the exact v1 field/env runtime semantics are not specified by SPEC 22/27; #78 must define the behavior before exposing another knob. |
| rtkEnabled (true) | same | PRESENT | R03: snapshot-compiled option used on every retry/fallback; internal/app/transforms_test.go. Native helper wire qualification is separate R09. |
| headroomEnabled (false) | same | PRESENT | R03: snapshot-compiled option used on every retry/fallback; internal/app/transforms_test.go. Native helper wire qualification is separate R09. |
| headroomUrl (HEADROOM_URL or localhost:8787) | headroomUrl localhost:8787 | PRESENT | R03: snapshot-compiled option used on every retry/fallback; internal/app/transforms_test.go. Native helper wire qualification is separate R09. |
| headroomCompressUserMessages (false) | same | PRESENT | R03: snapshot-compiled option used on every retry/fallback; internal/app/transforms_test.go. Native helper wire qualification is separate R09. |
| headroomTimeoutMs (3000) | same | PRESENT | R03: snapshot-compiled option used on every retry/fallback; internal/app/transforms_test.go. Native helper wire qualification is separate R09. |
| cavemanEnabled (false) | same | PRESENT | R03: snapshot-compiled option used on every retry/fallback; internal/app/transforms_test.go. Native helper wire qualification is separate R09. |
| cavemanLevel (full) | same, lite/full/ultra validated | PRESENT | R03: snapshot-compiled option used on every retry/fallback; internal/app/transforms_test.go. Native helper wire qualification is separate R09. |
| ponytailEnabled (false) | same | PRESENT | R03: snapshot-compiled option used on every retry/fallback; internal/app/transforms_test.go. Native helper wire qualification is separate R09. |
| ponytailLevel (full) | same, lite/full/ultra validated | PRESENT | R03: snapshot-compiled option used on every retry/fallback; internal/app/transforms_test.go. Native helper wire qualification is separate R09. |
| pxpipeEnabled (false) | same | PRESENT | R03: snapshot-compiled option used on every retry/fallback; internal/app/transforms_test.go. Native helper wire qualification is separate R09. |
| pxpipeAutoInstall (true) | same | PARTIAL | Install policy stored, no admin install/lifecycle path R09. R11: privileged host operation: SPEC 22 gives Routeweft external HTTP helpers only; install/process/host-credential ownership needs a local product policy, not a remote trigger. |
| pxpipeMinChars (25000) | same | PRESENT | R03: snapshot-compiled option used on every retry/fallback; internal/app/transforms_test.go. Native helper wire qualification is separate R09. |
| pxpipeTimeoutMs (15000) | same | PRESENT | R03: snapshot-compiled option used on every retry/fallback; internal/app/transforms_test.go. Native helper wire qualification is separate R09. |

### 5.2 Additional consumed settings / configuration objects

v1 PATCH accepts arbitrary non-password settings, so defaults alone are not the surface. Evidence: `src/app/api/settings/route.js`, `src/sse/services/auth.js`, `open-sse/services/combo.js`, `src/shared/services/quotaAutoPing.js`, `src/lib/db/repos/requestDetailsRepo.js`, Headroom routes.

| v1 surface | Routeweft mapping / evidence | Status | Shortfall / disposition |
| --- | --- | --- | --- |
| fallbackStrategy (fill-first fallback) | providerStrategy allowlist + routing/accounts.go | PARTIAL | Rename is reasonable D10, but no tested import or object overrides; R01/C04. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| password / newPassword / currentPassword; hasPassword | admin_users + /auth/password, adminauth | PARTIAL | KDF + revoke sessions implemented D04; local recovery/bootstrap/login-optional policy unresolved C03. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| claudeAutoPing / codexAutoPing connections maps | no scheduler/action/settings | MISSING | Opt-in window warming after reset absent P05/C05/U06. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| headroomCodeAware / headroomKompress | no writable settings/control API | MISSING | Used by v1 managed start/restart; R09/C04. R11: privileged host operation: SPEC 22 gives Routeweft external HTTP helpers only; install/process/host-credential ownership needs a local product policy, not a remote trigger. |
| observabilityMaxBuffered | bounded queue options, no corresponding writable setting | PARTIAL | Bounded design retained BDR-013; exact pressure/loss/live policy fixtures needed C05/R10. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| ccFilterNaming / bypass naming probes | no corresponding writable setting/ingress behavior | MISSING | v1 Chat bypass uses this flag; retained-client decision required C04/R11. R11: no SPEC basis: the exact v1 field/env runtime semantics are not specified by SPEC 22/27; #78 must define the behavior before exposing another knob. |
| providerCompatibility | RW stores JSON object, v1 uses provider-specific flags | PARTIAL | SPEC 22 leaves semantics unspecified; not evidence of functional equivalence C04/C05. R11: no SPEC basis: the exact v1 field/env runtime semantics are not specified by SPEC 22/27; #78 must define the behavior before exposing another knob. |
| Provider connection name/email/identity/authType/isActive/priority/globalPriority/defaultModel/testStatus/lastError/lastErrorAt | normalized credentials connections + RuntimeState | PARTIAL | Some naming/enable/order/identity exists; global priority/default-model/status persistence semantics need mapping C06. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| apiKey/accessToken/refreshToken/idToken/expiry/workspace/project/account/region/profile/session/cookie providerSpecificData | sealed Secret + registry/resolver | PARTIAL | Secret isolation is D03/D04; module-specific signing/identity/import/refresh/PAT/cookie data/workflows incomplete P02/P04/C06. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| Per-connection proxy enabled/URL/no-proxy/proxyPoolId/vercelRelayUrl/strictProxy | proxy_pools/members + connection binding | PARTIAL | Global/pool primitives exist; direct/relay/strict proxy semantics and operator CRUD/test incomplete C06/R08. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| No-auth providerStrategies proxyPoolId / rotateStrategy | no equivalent object setting | MISSING | v1 virtual free account can rotate all active proxy pools; not represented by map[string]string C04/C06/R01. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| Nodes type/name/prefix/apiType/baseUrl; System One typed nodes | builtin/generic node/transports model | PARTIAL | R04 models custom typed System One and Cloudflare secondary endpoints; D07 unifies compatible nodes. Remaining opaque provider-specific fields are C06/P02. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| Combo name/kind/ordered model IDs; fallback/RR/sticky/judge/fusionTuning | combos/combo_models + fusion boolean/judge | PARTIAL | Kind/tuning/object overrides not mapped. v1 default minPanel=2/grace=8000ms/hard=90000ms vs Go min=1/grace=250ms/hard=30000ms. Both defaults return a sole successful answer; Go with explicit MinPanelQuorum>1 rejects it with ErrFusionQuorum, whereas v1 minPanel only gates the grace window (C06/R01). No approved exception. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| capacity pools roundRobin/models, empty pool/deselect, context trim | runtime/combo.go + routing/capability.go | PARTIAL | Unit primitives exist; exact app cases and migration of object shape needed R01/C04. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| Model aliases/custom/disabled, kind/caps/context/thinking/upstream IDs/quota family | normalized model tables/compiler | PARTIAL | R05/R06 implement aliases/kinds/custom delete and metadata persistence. Provider seeds, specialized capabilities and default pricing remain P03/C06. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| Pricing defaults/overrides and cached token prices | pricing_overrides + shared token usage | PARTIAL | Override storage exists; cost production/read model and baseline default pricing incomplete R10/C06. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| API-key accessRestricted/accessAllow | api_keys.access_restricted/access_allow (v6; default 0/`[]` = unrestricted, as pin `KEY_ACCESS_UNRESTRICTED`), compiled into resolved identities per snapshot; SPEC 12.1 | PARTIAL | #78/C02. Enforced before credential resolution on every client-key form (Bearer, x-api-key, x-goog-api-key, `?key=`) for Chat, Responses, Compact, Messages, count_tokens, System One, Gemini and Ollama chat; Combo grants cover the Combo as a unit; alias/model grants are resolved identities; discovery (`/v1/models*`, `/v1beta/models`, `/api/tags`) filtered; refusal is `403 model_not_allowed` with no existence oracle; corrupt stored allowlists fail closed. Not implemented: (1) capacity-adapter pool filtering exists in `PlanCombo` but has no production caller, so it is not enforced in serving until Combo planning is wired in dispatch (`internal/ingress/dispatch.go`, not touched here); (2) the pin's provider-as-model grants for search/fetch kinds (Routeweft models them as llm capabilities); (3) case-insensitive matching (Routeweft resolution is case-sensitive; stricter, never wider); (4) dashboard editor (U02). R11: product decision: unimplemented retained semantics have no waiver and block configurations that depend on them. |
| Locale/theme/preferences | theme localStorage light/dark/system | PARTIAL | Theme retained; locale endpoint/translations/preferences mapping absent U10. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| Removed tunnelEnabled/tunnelUrl/tunnelProvider/tailscaleEnabled/tailscaleUrl/tunnelDashboardAccess/mitmRouterBaseUrl/mitmSudoEncrypted | absent | MISSING | Intentional D12; pinned v1 strips these settings on update/export too. R11: product decision: standalone/media/hidden-provider exclusions D04-D06/D12-D13 apply (PRD 4.2, BDR-001/005/016); retained helper functionality is not excluded. |
| cloudUrl / cloud enable integrations | absent | MISSING | Intentional D12; do not infer exclusion for an unrelated Netlify outbound relay R08. R11: product decision: standalone/media/hidden-provider exclusions D04-D06/D12-D13 apply (PRD 4.2, BDR-001/005/016); retained helper functionality is not excluded. |

### 5.3 Tables, files and migration boundary

Never point Routeweft at the oracle's live database. BDR-001 intentionally gives Routeweft a new schema; #73 additionally requires an explicit tested migration path or reviewed divergence. Those are different assertions: direct compatibility is excluded, but a safe migration/recreation policy is still missing C01.

| v1 schema v3 table / file | Routeweft schema v4 equivalent | Status | Shortfall / disposition |
| --- | --- | --- | --- |
| _meta | meta + schema_migrations | PARTIAL | Independent revision/version metadata D08; cross-product mapping C01. R11: product decision: independent state is mandatory (BDR-001/017); manually recreate configuration/reissue keys for parallel evaluation. Import/history continuity remains #78/C01; never share a live DB. |
| settings singleton JSON | settings key/value rows | PARTIAL | D08 normalization; all field/unit conversions C01/C04. R11: product decision: independent state is mandatory (BDR-001/017); manually recreate configuration/reissue keys for parallel evaluation. Import/history continuity remains #78/C01; never share a live DB. |
| providerConnections JSON + indexed identity columns | provider_connections + credential_events | PARTIAL | Encrypted secrets D03; provider state/identity conversion C01/C06. R11: product decision: independent state is mandatory (BDR-001/017); manually recreate configuration/reissue keys for parallel evaluation. Import/history continuity remains #78/C01; never share a live DB. |
| providerNodes JSON | provider_nodes | PARTIAL | D07/D08; typed SO config C01/R04. R11: product decision: independent state is mandatory (BDR-001/017); manually recreate configuration/reissue keys for parallel evaluation. Import/history continuity remains #78/C01; never share a live DB. |
| proxyPools JSON | proxy_pools + proxy_pool_members | PARTIAL | D08 normalized members; binding/deletion/relay config C01/C06. R11: product decision: independent state is mandatory (BDR-001/017); manually recreate configuration/reissue keys for parallel evaluation. Import/history continuity remains #78/C01; never share a live DB. |
| apiKeys plaintext + accessRestricted/accessAllow | api_keys digest/prefix/pause | PARTIAL | D03 one-way verification intentional; key reissue/import policy C01, access policy C02 (see the accessRestricted row). R11: product decision: independent state is mandatory (BDR-001/017); manually recreate configuration/reissue keys for parallel evaluation. Import/history continuity remains #78/C01; never share a live DB. |
| combos models JSON | combos + combo_models | PARTIAL | Ordered normalized members exist; kind/tuning/strategy conversion C01/C06. R11: product decision: independent state is mandatory (BDR-001/017); manually recreate configuration/reissue keys for parallel evaluation. Import/history continuity remains #78/C01; never share a live DB. |
| kv scopes (aliases, custom/disabled models, pricing, provider-specific values) | model_aliases/custom_models/disabled_models/pricing_overrides/provider_models | PARTIAL | Typed tables intentional D08; enumerate scoped mappings, do not silently discard unknown scopes C01/C06. R11: product decision: independent state is mandatory (BDR-001/017); manually recreate configuration/reissue keys for parallel evaluation. Import/history continuity remains #78/C01; never share a live DB. |
| usageHistory | usage_events | PARTIAL | Tokens/cache/status/duration fields; cost/endpoint/attribution/history mapping C01/R10. R11: product decision: independent state is mandatory (BDR-001/017); manually recreate configuration/reissue keys for parallel evaluation. Import/history continuity remains #78/C01; never share a live DB. |
| usageDaily dateKey JSON | usage_daily dimensional rows | PARTIAL | Rollup policy/history/cost conversion C01/R10. R11: product decision: independent state is mandatory (BDR-001/017); manually recreate configuration/reissue keys for parallel evaluation. Import/history continuity remains #78/C01; never share a live DB. |
| requestDetails JSON | request_details | PARTIAL | Bounded/redacted independent detail storage; no normal app detail producer/filter parity R10/C01. R11: product decision: independent state is mandatory (BDR-001/017); manually recreate configuration/reissue keys for parallel evaluation. Import/history continuity remains #78/C01; never share a live DB. |
| DATA_DIR/db/data.sqlite + WAL/SHM | ROUTEWEFT_DATA_DIR/routeweft.sqlite + WAL/SHM | MISSING | Direct read intentionally unsupported D08; no import tool or tested recreation policy C01. R11: product decision: independent state is mandatory (BDR-001/017); manually recreate configuration/reissue keys for parallel evaluation. Import/history continuity remains #78/C01; never share a live DB. |
| Legacy db.json/usage.json/disabledModels.json/request-details.json | no LiteRouter legacy reader | MISSING | D08 independent storage; explicit history/config disposal/recreation policy C01. R11: product decision: independent state is mandatory (BDR-001/017); manually recreate configuration/reissue keys for parallel evaluation. Import/history continuity remains #78/C01; never share a live DB. |
| /api/settings/database JSON export/import | Routeweft ZIP containing SQLite + metadata | PARTIAL | D08 safe validation/rollback intentional; not a v1 importer C01. R11: product decision: independent state is mandatory (BDR-001/017); manually recreate configuration/reissue keys for parallel evaluation. Import/history continuity remains #78/C01; never share a live DB. |
| db/backups schema-change safety copies excluding requestDetails, keep 3 | online backup/staged restore/pre-restore rollback artifact | PARTIAL | Stronger independent backup contract D08; cross-product restore forbidden, v1 migration rehearsal C01. R11: product decision: independent state is mandatory (BDR-001/017); manually recreate configuration/reissue keys for parallel evaluation. Import/history continuity remains #78/C01; never share a live DB. |

Evidence: pinned `src/lib/db/{schema,paths,backup,migrate}.js` and repositories; Routeweft `internal/store/migrations/v1.go` through `v4.go`, `internal/backup`, `internal/credentials`, `docs/RUNBOOK.md` 11-14.

### 5.4 Environment / process / deployment configuration

Names below are source names, not secret values. Default same-function mappings are assessed; preserving a LiteRouter environment name is not required by standalone BDR-001. Platform/build/debug-only knobs are explicitly grouped, not presented as product feature gaps.

| v1 configuration names / purpose | Status | Routeweft equivalent / disposition |
| --- | --- | --- |
| PORT (20128), DATA_DIR | PRESENT | ROUTEWEFT_LISTEN (:21128), ROUTEWEFT_DATA_DIR; intentional identity/path D02/D08. |
| INITIAL_PASSWORD, JWT_SECRET, AUTH_COOKIE_SECURE | PARTIAL | ROUTEWEFT_BOOTSTRAP_ADMIN_PASSWORD / USERNAME, process-local opaque sessions, always-Secure cookie. D04 auth design; C03 recovery/disable behavior. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| API_KEY_SECRET, MACHINE_ID_SALT, NINEROUTER_PEER_TOKEN | PARTIAL | Own key digest generation + sealed credentials; no machine CLI bypass D03/D04. Trusted-peer deployment behavior requires C09. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| REQUIRE_API_KEY (.env.example) | PARTIAL | requireApiKey compiled setting. No direct consumer found in pinned src/open-sse; do not claim this sample variable overrides v1 runtime. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| NINEROUTER_PROXY_CLIENT_MAX_BODY_SIZE | PRESENT | ROUTEWEFT_MAX_BODY_BYTES / --max-body-bytes, 134217728 default. |
| TRUST_PROXY | PARTIAL | Spoofed forwarded IPs ignored; ROUTEWEFT_TRUSTED_PROXIES documented, no CLI consumer found C09/C10. R11: no SPEC basis: the exact v1 field/env runtime semantics are not specified by SPEC 22/27; #78 must define the behavior before exposing another knob. |
| HTTP_PROXY, HTTPS_PROXY, ALL_PROXY, NO_PROXY; lowercase variants | PARTIAL | Global/pool/noProxy configuration exists; exact scheme/env precedence and direct per-connection behavior need C04/C06/R08. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| NINE_ROUTER_PROXY_URL, NINE_ROUTER_NO_PROXY, NINE_ROUTER_PROXY_MANAGED | PARTIAL | RW uses its own pooled binder rather than environment mutation; prove matching/binding changes C05/C06. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| HEADROOM_URL; headroom managed process/extras | PARTIAL | headroomUrl setting; helper process ownership/control R09. R11: privileged host operation: SPEC 22 gives Routeweft external HTTP helpers only; install/process/host-credential ownership needs a local product policy, not a remote trigger. |
| ENABLE_REQUEST_LOGS, OBSERVABILITY_ENABLED, OBSERVABILITY_MAX_RECORDS, OBSERVABILITY_BATCH_SIZE, OBSERVABILITY_FLUSH_INTERVAL_MS, OBSERVABILITY_MAX_JSON_SIZE, OBSERVABILITY_MAX_BUFFERED | PARTIAL | Compiled enableObservability/size/batch/flush settings + bounded telemetry; v1 env-vs-UI precedence and actual KiB units must be explicit C04/C05. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| USAGE_BATCH_SIZE, USAGE_FLUSH_INTERVAL_MS, USAGE_MAX_BUFFERED | PARTIAL | Bounded usage queue/batcher, not same knob names; accounting/cost/failure behavior needs R10/C05. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| REQUEST_LOG_MAX_SESSIONS, REQUEST_LOG_MAX_SIZE_MB, REQUEST_LOG_PRUNE_INTERVAL_MS | PARTIAL | Independent detail retention/prune policy; no proven equivalent capture/retention effects R10/C05. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| LOG_LEVEL, LOG_USAGE_VERBOSE | PARTIAL | R11 configures ROUTEWEFT_LOG_LEVEL and rejects invalid levels before data access; cmd/routeweft/main_test.go. R11: no SPEC basis: SPEC 27 defines log level, not a separate verbose prompt/usage logging toggle; never add secret logging. |
| MAX_ACCOUNT_FALLBACK_ATTEMPTS | PRESENT | R01: fixed cap of 10 wire attempts, including retries, cancellation and one same-account retry; internal/app/serving_test.go. SPEC 14 does not require a v1 environment override. |
| FETCH_CONNECT_TIMEOUT_MS (60000), STREAM_FIRST_CHUNK_TIMEOUT_MS (200000), STREAM_STALL_TIMEOUT_MS (360000), PENDING_COMPLETION_FLUSH_MS (3000), MODEL_TEST_TIMEOUT_MS (200000) | PARTIAL | R07 implements fixed 200s first-token model budgets and 600s aggregate cancellation (A/probes_test.go). R11: no SPEC basis: SPEC 33 defines those probe budgets, not v1 environment overrides or all inference first-byte/stall settings; #78/C09 must define retained serving timeout policy. |
| DISABLE_BACKGROUND_TOKEN_REFRESH, BG_REFRESH_DELAY_MS, BG_REFRESH_GOOGLE_DELAY_MS, EAGER_PROJECT_ID_REFRESH, ONBOARD_MAX_ATTEMPTS, ONBOARD_RETRY_DELAY_MS | PARTIAL | Registry/singleflight primitives exist; provider refresh/onboarding/project wiring P04. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| ANTIGRAVITY_REDIRECT_URI, KIMI_CODING_OAUTH_CLIENT_ID, KIMI_OAUTH_CLIENT_ID | MISSING | Provider flow overrides not represented in callable admin actions P04. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| AZURE_API_VERSION, AZURE_DEPLOYMENT, AZURE_ENDPOINT, AZURE_ORGANIZATION; COMMAND_CODE_API_BASE_URL; MIMO_PLATFORM_URL; KIRO_TOOL_CALL_REPAIR | PARTIAL | Provider-specific behavior/config mapping P02/C06, not satisfied by a base URL declaration. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| MODEL_CATALOG_SYNC | PRESENT | R06: catalog sync action/status/cancellation and catalog retention on failure; A/workflows_test.go. Same environment name is not required (BDR-001). |
| CLOUD_URL, NEXT_PUBLIC_CLOUD_URL; REDIS_URL, REDIS_KEY_PREFIX, REDIS_POOL_SIZE, REDIS_TIMEOUT_MS; LITEROUTER_MULTI_REPLICA; RUNTIME_CONFIG_TTL_MS, RUNTIME_CONFIG_VERSION_TTL_MS | MISSING | Intentional D12/D13: no cloud/Redis/multi-replica/cache-invalidation bus; immutable snapshots replace process cache polling. R11: product decision: standalone/media/hidden-provider exclusions D04-D06/D12-D13 apply (PRD 4.2, BDR-001/005/016); retained helper functionality is not excluded. |
| MINIMAL_PROFILE | MISSING | v1 hides Headroom; Routeweft has explicit fixed product boundary PRD 4.2. Not permission to drop retained helper integration R09. R11: product decision: standalone/media/hidden-provider exclusions D04-D06/D12-D13 apply (PRD 4.2, BDR-001/005/016); retained helper functionality is not excluded. |
| CLI_DEVIN_BIN, CLI_DEVIN_AGENT_TYPE, DEVIN_MCP_RESULTS, DEVIN_MCP_SERVERS, DEVIN_MCP_TOOLS, DEVIN_PERMISSION_MODE, TRAE_STREAM_TIMEOUT_MS | MISSING | Intentional D06: hidden non-product providers. R11: product decision: standalone/media/hidden-provider exclusions D04-D06/D12-D13 apply (PRD 4.2, BDR-001/005/016); retained helper functionality is not excluded. |
| GEMINI_NATIVE_TTS_FETCH_TIMEOUT_MS | MISSING | Intentional D05 standalone TTS exclusion. R11: product decision: standalone/media/hidden-provider exclusions D04-D06/D12-D13 apply (PRD 4.2, BDR-001/005/016); retained helper functionality is not excluded. |
| NODE_ENV, NEXT_DIST_DIR, NEXT_PHASE, NEXT_RUNTIME, NEXT_TRACING_ROOT_MODE | MISSING | Intentional D04: no production Node/Next process. R11: product decision: standalone/media/hidden-provider exclusions D04-D06/D12-D13 apply (PRD 4.2, BDR-001/005/016); retained helper functionality is not excluded. |
| CURSOR_PROTOBUF_DEBUG, CURSOR_STREAM_DEBUG, DEBUG_BACKGROUND_TOKEN_REFRESH | MISSING | Debug toggles are implementation-specific; provider functionality/redacted diagnostics still required P02/P04/R10, not raw-debug parity. R11: product decision: the row's precise configuration/workflow shortfall remains #78/C04-C06 (PRD 10-15); unimplemented retained semantics have no waiver and block configurations that depend on them. |
| HOME, PATH, APPDATA, LOCALAPPDATA, COMPUTERNAME, HOSTNAME, platform PATH probes | PARTIAL | OS inputs are not new Routeweft settings; retained host auto-import needs explicit local/admin policy P04. R11: privileged host operation: SPEC 22 gives Routeweft external HTTP helpers only; install/process/host-credential ownership needs a local product policy, not a remote trigger. |
| ROUTEWEFT_CREDENTIAL_KEY; ROUTEWEFT_ALLOW_PRIVATE_UPSTREAMS; ROUTEWEFT_CORS_ORIGINS | PRESENT | New own-product security knobs (D03/D11); no oracle credential/state sharing. |
| Caddy credential redaction, token-sync limits, deploy-time image retention | MISSING | No corresponding config/service among pinned application routes. Deployment evidence/ownership decision C08; do not invent a 98th application route. R11: product decision: external deployment/incident evidence belongs to #74/#78; no application endpoint or production execution is authorized by R11. |
| TLS CA trust in production image | PRESENT | R11: Docker copies the CA bundle into scratch; container CI asserts a non-empty bundle. SPEC 20/30; no TLS verification bypass. |

## 6. Behavioral and operational divergence ledger

Only rows marked **approved intentional** are justified by existing requirements. A **gap** or **decision required** is not an accepted exception, even when a stronger behavior may be desirable. #73 requires review/sign-off of this ledger before any future cutover; R11 proposes parallel evaluation only and does not sign off unresolved product decisions.

### 6.1 Approved intentional differences

| ID | Difference | Authority / reason | Boundary that is not waived |
| --- | --- | --- | --- |
| D01 | Versioned /admin/v1 resources, different admin envelopes/verbs/pagination and page locations | PRD 16; SPEC 22; BDR-014. Public inference contracts do not inherit Next file structure. | Missing CRUD/actions/auth/access policies remain R/C/U gaps. |
| D02 | Listen :21128; /health/live and /health/ready; plain text health | PRD 19 PRD-REL-001; SPEC 1/24/27; BDR-018; RUNBOOK 1. | Readiness/security/bootstrap/cancellation still require C09 proof. |
| D03 | Client key digest/prefix and create-once secret, sealed provider credentials; no browser credential reads | PRD-SEC-001; SPEC 4/12/19; BDR-001. | Missing restricted-key access policies C02 must not be excused by hashing. |
| D04 | Go service/static React-Vite, own admin KDF/opaque session, no Next/JWT runtime or machine-token dependency | PRD 1/17; SPEC 12/23; BDR-001/002/003; UI_STYLE. | Local recovery/login-disabled behavior is C03 decision required; UI/OAuth actions still retained. |
| D05 | No standalone image/video/TTS/STT/embedding/media-provider product APIs/catalogs | PRD 4.2/11/12; BDR-016. | Multimodal content in retained LLM requests and vision/audio capacity routing remain required. Single kind route's non-media semantics require R05 decision. |
| D06 | trae, devin-cli, windsurf absent | PRD-PROV-003; SPEC 32; PROVIDER_BASELINE hidden rows; v1 index comments. | muse is active, not hidden, and is P01 drift. |
| D07 | One Generic Provider can advertise C/R/M instead of separate compatible node products | PRD 7; SPEC 33; BDR-023. | Native route choice/manual-model fallback/multiple accounts/SSRF and typed SO workflows not waived. |
| D08 | Independent normalized SQLite schema/files/backup artifacts, no direct LiteRouter DB compatibility | PRD 1/4.2/18; SPEC 4/25; BDR-001/004/017; RUNBOOK 2/11/12. | Explicit tested migration/recreation policy C01 still required to reconcile #73; never share live DB. |
| D09 | One SSE event channel, targeted invalidation, refresh fallback | PRD-OBS-003; SPEC 22/23. | #106 adds a second, usage-only channel (`/admin/v1/usage/stream`) at the ticket's request; needs owner sign-off against this decision. The Requests tab subscribes; console/activity content still missing U08/R10. |
| D10 | Typed/validated string settings and normalized adapters/models; final-body cache order; PRD's 5 MB detail target | PRD 9/11/12/15; SPEC 7/17/18; BDR-007/011/012. | Changed v1 strategy/map/size/no-proxy semantics need explicit conversion C04; cache transforms cannot remain unwired R03. |
| D11 | Explicit CORS allowlist, strict SSRF/private-network policy, trusted local opt-in, no forwarded-header blind trust | PRD-API-005 / PRD-SEC-002/003/004; SPEC 20/27/34. | Do not weaken network policy for parity; callable local-provider/redirect tests and h2c client coverage remain required. |
| D12 | No cloud sync, tunnel/Tailscale/MITM lifecycle, removed legacy platform state | PRD 4.2/15; BDR-001/016. | Does not automatically exclude outbound proxy relay provisioning; R08 needs decision. |
| D13 | No Redis/Postgres/remote cache bus/multi-replica topology; immutable snapshots + separate mutable state, pooled transports, bounded async telemetry | PRD 3/18/19; SPEC 5-8/20/21; BDR-004-008/013/017. | Same routing/cache/accounting outcomes still required; architecture is not evidence of end-to-end parity. |

### 6.2 Real gaps / decisions (all assigned)

The checklists in #75-#78 are normative follow-up scope for this inventory. Every PARTIAL/MISSING row above maps to these gaps or an approved D row.

| IDs | Explicit gap / decision required | Authority | Owner |
| --- | --- | --- | --- |
| R01 | Native configured app account resolution/fallback/retry is implemented. Retained actual Combo/Fusion/capacity serving and pinned Fusion defaults remain open; no PRD 11 / BDR-011 waiver. | PRD 5/10/11; SPEC 8/13-16; Sprint 5.1 | #75 |
| R02 | Chat/Responses, Messages<->Chat, Ollama (provider side), the Gemini and Vertex legs (Gemini routable, Vertex gated on #115 PR-C: see 6.7), the Kiro Chat and Messages legs (implemented, not routable: see 6.8, 6.11) and the Cursor and CommandCode conversion legs (not yet routable: see 6.5) are implemented (#119). The Gemini CLI and Antigravity legs are implemented and unrouted (see 6.9). The Cursor, CommandCode, Gemini CLI and Antigravity transports, count-tokens and provider compact behavior remain open under SPEC 9/10; #119 tracks each leg. | PRD 5; SPEC 9/10; Sprint 5.2 | #75 |
| R03 | Serving pipeline slice resolved: normative per-attempt savers/final cache, bypass and fail-open tests in internal/app/transforms_test.go. Native helper parity stays R09. | PRD 12; SPEC 17/18; BDR-012; Sprint 5.3 | #75 |
| R04 | Typed validation, native/custom/Cloudflare transports, streams/cancel and shared usage are implemented; internal/app/systemone_test.go. R11 adds pinned handler comparison; no broad-provider claim. | PRD 5/14; SPEC 10/28; #73 explicit System One | #75 |
| R05 | Historical aliases, Ollama tags and id/kind/access catalog semantics are implemented (SPEC 34). Legacy /api/v1 is retained with identical inference auth/CORS; media kinds stay excluded. | PRD-API-001-004; SPEC 34 | #75 |
| R06 | Native validation/manual fallback, availability/reset, catalog sync, suggestions/free cache, custom delete and batch workflows implemented. Specialized provider/default-price breadth remains #76/P02/P03. | PRD 7/9/14; SPEC 22/33 | #75 |
| R07 | Native first-token probes, own/parent timeout distinction, partial results, abort and tested/upstream/served attribution implemented. Specialized Kenari qualification remains #76/P06. | PRD 14/21; #73 production drift; pinned ping.js/runtimeConfig.js | #75 |
| R08 | Proxy-pool CRUD/connectivity and global proxy tests implemented. Netlify provisioning and extra filter/usage fields still need a product/SPEC contract; route rows 59/60. | PRD-ROUTE-005/14; SPEC 20/22 | #75 |
| R09 | Read-only external helper diagnostics implemented under SPEC 22. Remote install/lifecycle/extras/passthrough is forbidden; native Headroom/PXPIPE wire qualification remains open. | PRD-XFORM-004/005; SPEC 17 | #75 |
| R10 | Usage key attribution/filters and console clear implemented. Normal request-detail production, cost/TTFT, inference model correlation, topology/live UI and reset actions remain open. | PRD 13/14; SPEC 21/22 | #75 |
| R11 | Pinned System One differential and 97-route/153-operation inventory/guard CI gate added. Full cross-protocol/control-workflow differential coverage and release sign-off remain open; no full-parity claim. | PRD 20/21; SPEC 28/29; #73 acceptance | #75 |
| P01 | muse adopted on #116 (owner decision); baseline is now 81 active rows | PRD 6/21; SPEC 32/37 | #76 |
| P02 | Provider-specific wire/executor/envelope/signing/native-header/error/usage hooks (including Kimi Responses, Cloudflare SO, Kenari) | PRD-PROV-001/002; SPEC 11/32 | #76 |
| P03 | Authenticated live/provider-specific discovery, aliases/caps/seeds/filter/passthrough; never delete known manual models on outage | PRD 6/9; SPEC 11/33 | #76 |
| P04 | Callable OAuth/device/exchange/import/auto-import/PAT/cookie, validated distinct identity, durable singleflight refresh with real callbacks | PRD 8; SPEC 19/35; Sprint 5.5 | #76 |
| P05 | All v1-advertised limits/quota/reset clients; Codex credits and opt-in Claude/Codex auto-ping | PRD 8/13; SPEC 14/19 | #76 |
| P06 | Kenari production fixes: endpoint/header/tool/system-role/live-list/quota contracts | PRD 6/21; #73 | #76 |
| P07 | Immutable ready/experimental/unavailable metadata separate from enabled | PRD-PROV-004; SPEC 36; Sprint 5.4 | #76 |
| P08 | Per-provider listing/auth/quota/S/N/error/token fixtures + actual app evidence; no catalog-only proof | PRD 6/20; SPEC 28/32; #73 | #76 |
| U01 | Static Go asset delivery and Docker packaging resolved; internal/app/static_test.go. | PRD 14/19; SPEC 23/30; BDR-003 | #77 |
| U02 | Restricted/unrestricted model/alias/Combo key editor resolved; serving adapter-pool enforcement remains R01/C02. | #73 endpoint-key scope; PRD 14/17 | #77 |
| U03 | Readiness/auth/import/callback/provider/model tests/health/quota UI | PRD 8/14; SPEC 36 | #77 |
| U04 | Providers account/model/alias paging and typed complete pickers resolved. Specialized onboarding/default-pricing workflows remain U03/#76; Quota/System One first-100 reads remain U06/U09. | PRD 9/14; SPEC 22 list bounds | #77 |
| U05 | Usage costs/details/filter/topology/live data/pagination | PRD-OBS-001/002; SPEC 21/22 | #77 |
| U06 | Quota visibility/all accounts/reset credits/auto-ping | PRD-QUOTA-001/14; #73 | #77 |
| U07 | Helper lifecycle/health/logs/stats/extras/diagnostics | PRD-XFORM-004/005/14 | #77 |
| U08 | Shared live SSE + refresh fallback, console clear/activity/Overview invalidation | PRD-OBS-003/14; SPEC 23 | #77 |
| U09 | App-level typed SO and full Combo/Fusion/capability workflows | PRD 11/14; UI_STYLE | #77 |
| U10 | Locale/profile/recovery/login preferences retained or explicitly rejected | PRD 14/21; pinned locale/profile pages | #77 |
| U11 | UI unit/browser/a11y checks in PR CI | SPEC 28/29; Sprint 7/8; AGENTS 10 | #77 |
| C01 | Tested isolated v1 migration/recreation policy, synthetic table/JSON/unit/key/history mapping | #73 data acceptance; PRD 18; BDR-001 | #78 |
| C02 | Key policy persistence, compiled enforcement/discovery and UI editor implemented (SPEC 12.1). Restricted adapter-pool enforcement still waits on actual Combo serving R01. | #73 current v1 scope; PRD 17; pinned keyAccess.js/tests | #78 |
| C03 | Explicit auth policy decision for requireLogin/local CLI/reset/bootstrap; do not silently introduce bypass | PRD 14/15/17; SPEC 12/22 | #78 |
| C04 | Exact settings map/strategy/sticky/unit/default conversions; adapter API drops RR selection; ccFilterNaming/Headroom extras flags | PRD 10-12/15/21; SPEC 7/22 | #78 |
| C05 | Dynamic settings actually affect telemetry/proxy/transforms/cache/compatibility/quota/auto-ping | PRD 12-16; SPEC 7/21/22 | #78 |
| C06 | Provider-specific config/identity/proxy/relay, model/pricing/Combo kind/tuning fields mapped without loss | PRD 8-11/14; SPEC 4/11/15/16/19 | #78 |
| C07 | Login/password Retry-After and model-probe attribution implemented. Inference requested/selected/upstream correlation and deployed sync-secret redaction evidence remain C08/R10. | PRD 17/21; #73 production incidents | #78 |
| C08 | External token-sync/Caddy/retention evidence and ownership decisions; no invented endpoint/production execution | #73; RUNBOOK 7/10/13/14; #74 coordination | #78 |
| C09 | Readiness/bootstrap/h2c/trusted-peer/shutdown/cancellation/accounting/race behavior evidence | PRD 17/19; SPEC 24/28/34 | #78 |
| C10 | R11 adds production TLS CA trust and documented log-level handling. Trusted-proxy/verbose environment semantics still need a concrete SPEC contract; C09 owns their behavior. | PRD 18-20; SPEC 20/27/30; RUNBOOK | #78 |

### 6.3 Conversion legs: where Routeweft differs from the pin (#119)

Oracle paths are at `1c3b9f47`. These are not approved exceptions: each is a
reviewed choice that needs owner sign-off, and none is silent.

| ID | Pin behavior | Routeweft behavior | Why |
| --- | --- | --- | --- |
| V1 | `claude-to-openai.js:96-99` repeats the tool-call `id` on every argument delta. | The id is sent once, with the call's first chunk. | OpenAI clients and SDKs concatenate string fields while accumulating, so a repeated id reads back as `call_1call_1`; the matrix test caught it through the Chat to Responses leg. |
| V2 | `claude-to-openai.js:183-187` (`message_stop` without a `stop_reason`) reports `prompt_tokens = input_tokens`, no cache. The merged path at `:127-170` folds cache in. | Both paths use the cache-folded figure. | One request must not report two prompt sizes; `:127-170` agrees with `canonicalizeUsage` (`utils/usageTracking.js:147-210`) and `openai-to-claude.js:89`. |
| V3 | `nonStreamingHandler.js:303-309` (Messages body to Chat) reports `prompt_tokens = input_tokens`, no cache. | Cache-folded, as the stream leg. | Same as V2. |
| V4 | `nonStreamingHandler.js:291-293` maps only `end_turn` and `tool_use`; `max_tokens` and `refusal` pass through unmapped. | `concerns.ToOpenAIFinish` (`finishReason.js:6-52`): `max_tokens` is `length`, `refusal` is `content_filter`. | The stream leg and the shared concern already say so. |
| V5 | `nonstream-to-claude.js:25-28` (Chat body to Messages) sets `input_tokens = prompt_tokens || input_tokens`, dropping cache. | `input_tokens = prompt - cached - cache_creation` plus the cache fields, as `openai-to-claude.js:89-106`. | Same as V2. |
| V6 | `openai-to-claude.js:72` drops any chunk without choices, and `:246-256` closes the message at `finish_reason`. A usage-only chunk after it is lost and the client sees zero usage. | The closing `message_delta` and `message_stop` wait for the end of the stream and carry the latest usage. Block events still flow as they arrive. | Otherwise a Chat provider that honours `include_usage` reports zero tokens to a Messages client. |
| V7 | `response/claude-to-openai.js` has no case for an Anthropic `error` event; the stream just continues or stops. | Failure terminal in the client's protocol. | SPEC 9.3: a failed stream never claims success. |
| V8 | `utils/usageTracking.js:20-57,343-404`, `utils/stream.js:101-104,238-249,414-419`, `nonStreamingHandler.js:422` add 2000 tokens to real prompt usage and replace missing usage with a chars/4 estimate, in what the client receives. | Neither. A stream without a usage chunk stays `usage_observed=0`. Cursor is the case in point: its wire reports no usage and the pin's executor estimates it (`executors/cursor.js:834,1087,1310`); Routeweft deliberately does not (6.5 V32), so for Cursor a missing usage means "not reported", never "zero tokens used". | An estimate or buffer is not a measurement (SPEC 21.1). Posted on #119 for a decision; confirmed: no chars/4 estimate anywhere. |
| V9 | No `stream_options` on a translated stream (only `executors/iflow.js:100-102` injects it). | `stream_options.include_usage` is set on a translated Messages to Chat stream unless the client set `stream_options` itself (owner decision, #119). | `ResponsesToChat` already does this; without it the Messages client gets zero usage. Native Chat is untouched (#122). |
| V10 | `claude-to-openai.js:12` stamps a new `created` on every chunk. | One `created` per stream. | The OpenAI contract. |
| V11 | `claude-to-openai.js:177-186` drops an image whose source is a `url`; a request left with no content becomes `messages: []`. | A `url` image is forwarded as an `image_url`, and a conversion that would leave no message keeps one empty text turn (`"Continue."` toward a Messages provider). | Chat takes remote URLs natively; an empty list is an invalid request that would be sent upstream or rejected as a 502 for a request Anthropic accepts. |
| V12 | The pin drops `top_p`, `stop_sequences`, `metadata.user_id`, `disable_parallel_tool_use`, `document` blocks, a Chat `developer` message, `max_completion_tokens`, `stop`, `user`, `parallel_tool_calls`, and sends an empty `tools` array (`claude-to-openai.js:52-103`, `openai-to-claude.js:17-201,204-299`). | Each is carried, or refused with a translation error so the candidate is skipped. | A dropped document or instruction is a confident wrong answer with a 200. See the field tables below. |
| V13 | `openai-to-claude.js:142,184` ask for `cache_control` `ttl: "1h"` on the system block and the last tool. | Plain `{"type":"ephemeral"}`. | Anthropic gates the 1h TTL behind the `extended-cache-ttl` beta header, which the gateway never sends; a body that asks for what the header does not grant is rejected or silently downgraded. |
| V14 | `openai-to-claude.js:136` prepends "You are Claude Code, Anthropic's official CLI for Claude." to every request. | Only the caller's own system text is sent, and no `system` key when there is none. | A translation leg must not give third-party models (deepseek, glm, kimi, minimax, kenari, github) another vendor's identity or spend tokens on it. Not gated by provider. **Claude OAuth, decided in this PR:** the `claude` provider's credential is used with the CLI identity (`providers/claude/headers.go`), and I could not verify against the live service (no credentials, no external calls) that it accepts a request without the identity block. So the block is added in that provider's preparation (`providers/claude.WithIdentity`, applied by ingress for a translated Messages target to `claude` only), not in the leg. `github` also speaks Messages but a Chat or Responses client reaches it natively, so no leg is involved. |
| V15 | `openai-to-claude.js:262-264` turns `reasoning_content` into an unsigned `thinking` block; `top_p` is not carried, so the pair question never arises. | Unsigned thinking is not sent (signed parts are kept); `top_p` is dropped when `temperature` is also set. | Anthropic rejects an unsigned thinking block, and current Claude models reject a request that sets both sampling controls. Reasoning continuity for providers that want it returns with #125. |
| V16 | `concerns/toolCall.js:60-67` repairs an invalid tool_use/tool_result id but leaves an absent one; `nonstream-to-claude.js:45` mints `toolu_<time>_<n>`; `openai-to-claude.js:188` opens a streamed tool block only when the delta has an id. | An absent id is generated (`call_msg<i>_tc<j>_<name>`, deterministic), a result with no id answers the oldest unanswered call, and a streamed call with no id still opens a block. Tool input is always a JSON object. A terminal that promises tool calls when none were sent is a failure. | An id-less call reached Chat with no id, `input` could be a string or null (an upstream 400), and the client was told a tool ran when no block existed. |
| V17 | `openai-to-claude.js:195-199` strips a "proxy_" prefix from every streamed tool name. | Not stripped. | The request leg adds no prefix, so the strip only renames a real tool such as `proxy_search`, and the buffered leg never stripped it. |
| V18 | `utils/usageTracking.js:147-210` (`canonicalizeUsage`) stores one cache-inclusive convention. | The usage record carries the upstream's own figures (#122). For a Messages provider that is `input_tokens` without the cache read, so a request the client is shown as `prompt_tokens: 130` (cache 100) is recorded as `input=30, cache_read=100`; a Chat or Responses provider records 130. | **The reconciliation is open.** Owner decision on #119: keep the row as reported and fold in the read model, once, in one function used by every read path. That belongs to the accounting PR; until it lands, /usage totals differ by the cache-read count for identical traffic depending on the provider's wire. Pinned by `TestEveryConvertedPairServesAClient`. |
| V19 | `claude-to-openai.js:273-283` and `openai-to-claude.js:310-338` map every `tool_choice` they do not know, `none` included, to `auto`; `claude-to-openai.js:158` makes any other role the assistant. | `none` is carried as `none`. An unknown `tool_choice`, a `tool` choice with no name and an unknown role are translation errors. A choice with no tools is not forwarded. A Chat tool message's part array becomes text/image blocks. | A default that grants a capability is the wrong direction: the pin re-enabled tools the client had forbidden. When a mapping has no answer the fallback is the restrictive one (refuse), never the permissive one. |
| V20 | `concerns/toolCall.js:123-154` adds a reply only when the next message answers none of an assistant turn's calls. | Every unanswered call gets its own empty reply, and it runs for a Responses-sourced Chat body too. | Anthropic needs every `tool_use` id answered; a turn answering one of two parallel calls would be a 400 with nothing left to try. |

#### Request fields, both directions (`TestMessagesRequestFieldsAreCarriedOrRefused`, `TestChatRequestFieldsAreCarriedOrRefused`, `TestDocumentBlocksAreCarriedBothWays`)

Every field is **carried**, **not carried for a stated reason** (the request is still served; nothing it asks for is lost), or **refused** (translation error, so the dispatcher tries the next candidate). None is dropped silently.

| Messages request field | Chat | Why |
| --- | --- | --- |
| `model`, `messages`, `system`, `stream`, `max_tokens`, `temperature` | carried | pin |
| `top_p`, `stop_sequences` (to `stop`), `metadata.user_id` (to `user`) | carried | V12; the pin drops them |
| `tools`, `tool_choice`, `tool_choice.disable_parallel_tool_use` (to `parallel_tool_calls:false`) | carried | pin, V12 for the flag |
| `reasoning`, `reasoning_effort` | carried | pin |
| `thinking`, `output_config` | not carried | thinking normalisation needs the capability table (#125) |
| `top_k`, `service_tier` | not carried | sampling/billing hints; OpenAI-compatible upstreams reject unknown parameters |
| `mcp_servers`, `container`, `context_management` | refused | they change what the server does; serving the rest is a wrong answer |
| block `text`, `image` (base64, url), `tool_use`, `tool_result` | carried | pin, V11 for url |
| block `document` (base64 to `file`, text source to `text`) | carried | V12; the pin drops it |
| block `document` with url/content/file source; image with a file source; unknown block types | refused | the model would not see it |
| block `thinking`, `redacted_thinking` | not carried | signed by Anthropic, meaningless to another provider (the pin drops them) |
| block `server_tool_use`, `web_search_tool_result` | not carried | server-executed intermediate blocks of an earlier turn; the model's text reflects them |
| `tool_result.is_error`, `cache_control` | not carried | no Chat equivalent; the error text itself is carried |
| a message holding both `tool_use` and `tool_result` | refused | invalid in Anthropic and ambiguous in Chat |

| Chat request field | Messages | Why |
| --- | --- | --- |
| `model`, `messages`, `stream`, `temperature`, `response_format` (system prompt), `tools`, `tool_choice` | carried | pin |
| `max_tokens`; `max_completion_tokens` when `max_tokens` is absent | carried | V12; the pin reads only `max_tokens` and sends 64000 |
| `top_p`, `stop` (string or list), `user` (to `metadata.user_id`), `parallel_tool_calls:false` | carried | V12 |
| `developer` role | carried (as system) | V12; the pin drops the message |
| `tools:[]`, and a `tool_choice` with no tools | omitted | Anthropic rejects both |
| part `text`, `image_url` (data URI, http(s)), `image`, `file` (inline PDF to `document`), `refusal` | carried | pin, V12 for `file` title and `refusal` |
| `reasoning_effort`, `reasoning` | not carried | thinking normalisation (#125) |
| `seed`, `presence_penalty`, `frequency_penalty`, `logit_bias`, `top_logprobs`, `store`, `metadata`, `prompt_cache_key`, `service_tier`, `stream_options` | not carried | no Anthropic equivalent; none changes what is asked |
| `n>1`, `logprobs:true`, `modalities` with audio, `audio`, `functions`, `function_call` | refused | Anthropic cannot do what was asked |
| part `input_audio`, `audio_url`, unknown part types; `file` that is not an inline PDF (file_id, other types); `image_url` that is neither data URI nor http(s); unknown roles; tool calls that are not `function` | refused | the model would not see it |

Pin behavior mirrored on purpose, though it looks like a defect: a tool-using
request's `max_tokens` is raised to 32000 even when the client asked for less
(`formats/maxTokens.js:15-20`, to stop truncated tool arguments; the client's
budget is overridden upward); an unknown
stop reason maps to `stop`/`end_turn` (`finishReason.js:6-68`; there is no
safer finish value to fall back to); a Chat stream that reports no usage is closed with a zero usage object because
Messages requires one (`openai-to-claude.js:250`); non-streamed tool arguments
that are not JSON become `{}` (`nonstream-to-claude.js:5-13`); a non-streamed
Messages text block has a leading or trailing markdown JSON fence stripped
(`nonStreamingHandler.js:277`);
`chatcmpl-` ids are kept, only `chatcmpl_` and `resp_` become `msg_`
(`nonstream-to-claude.js:18`); a negative `input_tokens` is not clamped; tool-id
repair is skipped for a Responses-sourced body (`translator/index.js:82`, no
`messages` yet); `stop_reason` is `end_turn` when a provider says `stop` while
returning tool calls.

Not ported by the Messages legs, tracked on #119: `prepareClaudeRequest`
(`translator/formats/claude.js:485-707`: cache anchoring, tool-use ordering,
thinking placeholders, built-in tool filtering, OAuth cloaking); thinking
normalisation (`concerns/thinkingUnified.js`, which needs the model capability
table, and with it the model-aware `max_tokens` ceiling at
`openai-to-claude.js:25`); tool-name cloaking for OAuth (`index.js:165-176`).

### 6.4 Provider wire: where Routeweft differs from the pin (#115)

Registry quirks are applied to the final outbound body by `internal/providers/wire` (AGENTS §6 step 8). Pin: `1c3b9f47837d42ea64992063309064af755a14a3`.

| Quirk | Providers | Pin | Status |
| --- | --- | --- | --- |
| `dropClientMetadata` | cerebras, mistral | `executors/default.js:103` | ported |
| `dropOutputConfig`, `requireClaudeToolType` | minimax, minimax-cn | `translator/formats/claude.js:487`, `translator/concerns/toolCall.js:165-181` | ported |
| `claudeSupportedToolTypes` | deepseek | `translator/formats/claude.js:643-668,681-684` | ported for the whitelist; the pin's folding of `function`-shaped tools is not (Routeweft's Messages legs never emit that shape) |
| `responsesSystemRoleUnsupported` | kenari | `executors/default.js:21-42,97` | ported, with the divergence below |
| `responsesFunctionToolsOnly` | kenari | `handlers/chatCore.js:235`, `translator/concerns/responsesFunctionTools.js` | **deferred to #119** (request and response rewrite in the shared concerns) |
| `thinkingFormat` | blackbox, cloudflare-ai, codebuddy-cn, codebuddy-intl, iflow, openrouter, siliconflow, tokenrouter, venice, vercel-ai-gateway | `translator/concerns/thinkingUnified.js:133` | **deferred to #119** |
| `reasoningInject` and `stripUnsupportedParams` | minimax, minimax-cn; every provider | `utils/reasoningContentInjector.js`, `translator/concerns/paramSupport.js` | **deferred to #119** |
| `cloakToolsOnOAuth` | claude | `providers/registry/claude.js:41` | **not shipped**: tool cloaking disguises the client to the provider; Routeweft sends its own identity (PR #123 precedent) |

Divergences, all so no instruction or tool pairing is silently broken or lost:

- **deepseek, `tool_choice`.** The pin drops `tool_choice` only when every tool is filtered out. A `tool_choice` of type `tool` naming a filtered-out tool is dropped too when other tools survive (DeepSeek rejects a `tool_choice` whose tool is absent); `auto`, `any`, `none` and a choice naming a surviving tool stay.
- **kenari, `input` shapes.** A plain string `input` is folded as one user turn (the pin leaves it alone, so `instructions` still reached kenari). An item with no `type` but role `system` is folded (the pin requires `type: "message"`). A body that would end with an empty `input` is left as received.
- **kenari, nothing is discarded.** With system text or `instructions` but no user turn, the pin drops the system messages; Routeweft leaves the body as received. The body is also left as received when the fold would have to discard what it cannot carry as text: non-string `instructions`, a system message whose content is an object, a number or has a non-text part (an image), or a first user message whose content is an object or a number. The gateway's own error then reaches the client.

Every other `input` shape (empty array, non-message items, object, number) is byte-identical.

### 6.5 Provider headers, URLs and chat-body quirks (#115 PR-C)

Applied by `internal/providers/wire` (Target, header and Finalize hooks). Pin `1c3b9f47837d42ea64992063309064af755a14a3`.

One seam builds every upstream URL: `wire.NodeBase` (node base, else catalog default, else the base the connection names) and `wire.ResolveTarget` / `Target.URL` (target, endpoint, query). Serving, model probes, model discovery, catalog sync and route listings all use it, with the connection's own settings, so a probe tests the URL, headers and body a request would use (the registry quirks of `wire.FinalizeBody` apply to the probe body too). `TestEveryUpstreamBaseComesFromTheWireSeam` fails any function that reads a provider base URL field and is not listed with its reason, `TestDialingFunctionsCallTheWireSeam` fails a dialling function that stops calling it, and `TestEveryInferenceBodyPostGoesThroughFinalizeBody` fails any other function in `ingress` or `control/api` that POSTs a request without being a listed inference site that calls `wire.FinalizeBody`. A route listing has no connection, so a node whose base comes from the connection (Azure) lists no routes.

| Behaviour | Providers | Pin | Status |
| --- | --- | --- | --- |
| `{azureEndpoint}/openai/deployments/{dep}/chat/completions?api-version=`, `api-key` header, `OpenAI-Organization` | azure | `executors/azure.js:8-52` | ported; base URL comes from the connection (`runtime/providers.go`) |
| Region host and `…/anthropic/v1/messages` | xiaomi-tokenplan | `executors/xiaomi-tokenplan.js:13-19`, `config/providers.js:13-19` | ported |
| `?beta=true` on the Messages transport | claude, glm, kimi, minimax, minimax-cn | `registry/{claude:23,glm:21,42,kimi:29,51,minimax:21,52,minimax-cn:21,52}.js`, `executors/default.js:154` | ported |
| `X-Msh-*` device headers on every transport | kimi | `executors/default.js:66`, `registry/kimi.js:38,46,53,59` | ported with Routeweft identity |
| Responses transport (`/coding/v1/responses`) | kimi | `registry/kimi.js:55-59` | ported (was a tracked URL gap) |
| `X-Kilocode-OrganizationID` | kilocode | `executors/default.js:68`, `registry/kilocode.js:24-26` | ported (`orgId` is now projected) |
| `max_completion_tokens`, `reasoning_effort:"none"`, content-part sanitising on /chat/completions | github | `executors/github.js:56-107,144-147` | ported |
| Gateway field drops, `system` merge, artifact stripping | kimchi | `executors/kimchi.js:4-120` | ported |
| Per-request HMAC request signature: `x-iflow-signature` = HMAC-SHA256(apiKey, `"<User-Agent>:<session-id>:<unix-ms>"`) hex, with `session-id: session-<uuid4>` and `x-iflow-timestamp` | iflow | `executors/iflow.js:29-35` (canonical input `:31`, HMAC `:32-34`), headers `:43-64`, UA source `registry/iflow.js:19-21` | **ported (#115 PR-E)**; a connection with no credential is refused before dispatch rather than sent unsigned. **Live acceptance is UNVERIFIED offline** — the signature is pinned byte-for-byte against a fixed vector, but no request has been made to `apis.iflow.cn`, so that the gateway accepts it is not claimed here. |
| Qoder COSY request signing: `Authorization: Bearer COSY.<payloadB64>.<md5hex>` where md5hex covers `payloadB64\ncosyKey\ntimestamp\nbodyLatin1\nsigPath`; AES-128-CBC user-info payload with the key RSA-wrapped per request; 19 `Cosy-*`/`Login-Version`/`X-Request-Id` headers | qoder | `shared/qoder/cosy.js:116-175` (signature `:146-148`, AES `:44-55`, RSA `:57-63`), constants `shared/qoder/constants.js:67-74,98-105` | **call sites wired (#115 PR-I), production paths unreachable until the Qoder conversion leg lands; unverified against a live gateway.** Dispatch and model probes have `Hook.RequestHeaders` call sites after URL resolution and final body normalization, consuming all 19 headers from `wire.BuildCosyHeaders`. Neither production Qoder path reaches signing yet: ingress has no Qoder conversion leg (`internal/ingress/reachability_test.go`), and model probes reject the declared `TransportQoder` as unimplemented. The sealed connection's `machineId` is projected into immutable runtime settings; onboarding already mints it and includes it in the saved credential (`onboarding/providers.go:428-431,492-496,518-524`). At the dispatch seam, missing identity fails closed as `credentials_unavailable` with the auth-refresh account cooldown; missing `machineId` names Qoder sign-in as the recovery action; dispatch never generates or persists it. A synthetic Chat route tests the dispatch seam: it recomputes the signature over transmitted bytes and the resolved URL, checks all 19 headers and trimmed machine identity, and pins missing identity refusal/cooldown. Other tests cover stored-settings projection, the exact set of body-aware hook providers, and the currently unreachable Qoder probe. Pin: `open-sse/shared/qoder/cosy.js:81-92,116-175`, `open-sse/executors/qoder.js:664-680,692-700`. This wires signing only: Qoder envelope encoding/translation and stream codecs remain unimplemented and no live gateway call was made. |
| Model listing | azure | none at the pin: `registry/azure.js:15-20` declares no models fetcher and `src/app/api/providers/validate/route.js:155-181` validates by a deployment chat request | **no listing**: discovery is a clean no-op (no credential read, no request, no error); `{endpoint}/openai/models` is not invented because it cannot be verified |
| Claude models to the Messages endpoint, `/responses` fallback after a 400 | github | `executors/github.js:126-165,255-297` | **not in this PR**: it selects the transport by model name and retries across wires |
| `User-Agent: kimchi/0.1.50`, `x-opencode-client`, OpenRouter `HTTP-Referer`/`X-Title` | kimchi, opencode, openrouter, api-airforce | `registry/kimchi.js:22`, `opencode.js:17`, `openrouter.js:23`, `api-airforce.js:28` | **not shipped / not in this PR**: product identities and attributions that are not Routeweft's |

Divergences, each so a credential or an instruction is not sent or dropped without the operator knowing:

- **azure.** The pin falls back to `AZURE_ENDPOINT` and then `https://api.openai.com` (`azure.js:9-11`), which would send an Azure key to OpenAI; Routeweft has no ambient environment config and refuses a connection with no endpoint. The deployment name is a path segment and must look like a name (`[A-Za-z0-9][A-Za-z0-9._-]*`, no `..`); the pin splices it in raw (`azure.js:23`).
- **kimi.** The pin sends `X-Msh-Platform: 9router`, `X-Msh-Version`, the host name and the OS (`appConstants.js:215-244`). Routeweft sends `X-Msh-Platform: routeweft`, the connection's device id and `X-Msh-Device-Name: routeweft`, the same three headers onboarding and usage already send; host and OS are not disclosed. An API-key connection has no device id and sends the placeholder `routeweft` where the pin invents `kimi-<timestamp>` per request.
- **github.** When a client sends both `max_tokens` and `max_completion_tokens`, the pin overwrites the explicit one (`github.js:100-103`); Routeweft keeps it. The pin's o-series test `/o[134]-/i` is unanchored and needs a dash (`github.js:94-96`): it misses the bare ids `o1`, `o3`, `o4` and matches foreign ids such as `deepseek-o3-mini`; Routeweft matches an id that starts `o1`, `o3` or `o4` and then ends or continues with a dash.
- **kimchi.** The pin consults live Kimchi model metadata to decide whether a model is Anthropic-backed (`kimchi.js:89-93`); Routeweft uses the model name alone. When a top-level `system` cannot be merged (no messages array, a first system message whose content is neither string nor array, or a `system` that is not a string or text parts) the `system` field is left in place where the pin deletes its text (`kimchi.js:104-108`); every other sanitation (field drops, reasoning strip, artifact stripping) still runs.
- **xiaomi-tokenplan.** A Token Plan key is issued for one region, so the connection's host is never replaced by a default. The region setting is trimmed and overrides the host only when it is set and names a known region (`sgp`, `cn`, `ams`) on an official Xiaomi host. With no region, or one that names none of them, the connection keeps the host it was configured with; the pin falls back to the default region (`config/providers.js:16-19`), which would send a CN or AMS key to the SGP host. A base that is not an official Xiaomi host is kept as given.

### Production-incident drift explicitly dispositioned

| #73 incident | Pinned source / Routeweft evidence | Status / decision |
| --- | --- | --- |
| Kenari model probe first-token/timeout | v1 `src/app/api/models/test/ping.js`, `tests/unit/model-test-first-token-probe.test.js`, runtimeConfig MODEL_TEST_TIMEOUT_MS=200000; RW `A/providers.go:testProviderModel` | PARTIAL; R07 supplies native first-token probes and timeout attribution; specialized Kenari parity remains P06. R11: product decision: #76/P06 must qualify Kenari wire behavior before advertising it ready; generic probe fixtures are not provider certification. |
| Dashboard model-test logged different model | v1 `tests/unit/model-test-results.test.js`; RW modelTestResult.ModelID set from loop input | PRESENT; R07 reports testedModel/upstreamModel/servedModel; A/probes_test.go covers alias/outbound/response attribution and interrupted results. |
| Model-mismatch log correctness | RW completion callback records selected outcome.ModelID, but there is no differential regression fixture | PARTIAL; R07 model-test attribution is implemented, but inference usage requested/selected/upstream correlation is not complete. R11: product decision: #75/R10 and #78/C07 must complete the PRD-OBS-001 attribution contract before certifying diagnostics parity. |
| Auth-adjacent rate limit + Retry-After | R11: login/password throttle computes Retry-After from the remaining fixed window; A/api_test.go covers expiry and rounding. | PRESENT; token-sync is an external service, not a new application route. |
| Caddy X-Sync-Secret redaction / token log suppression | No Caddy/token-sync application implementation found in pinned src/open-sse/scripts; RW log buffer has generic secret markers but not deployed ingress evidence | MISSING evidence; C07/C08 require explicit external deployment ownership/fixtures. Do not claim default access-log redaction. R11: product decision: #74 owns deployed ingress redaction/token-sync evidence; R11 cannot attest to or modify an unprovided deployment. |
| Image retention at deploy, not timer | v1 `scripts/post-deploy.sh`, `tests/unit/production-image-retention.test.js`; RW no deploy workflow/post-deploy retention script | MISSING; C08/#74 decision: keep running+recorded rollback images, bounded deploy-time cleanup if adopted; no timer or cleanup run in Phase 1. R11: product decision: #74 owns deploy-time cleanup policy/rehearsal; retain running and recorded rollback images, do not introduce a timer or run production cleanup. |

### 6.4 Ollama legs: where Routeweft differs from the pin (#119, PR 2)

The pin's two Ollama legs are `request openai-chat>ollama` (`translator/request/openai-to-ollama.js:195`) and `response ollama>openai-chat` (`translator/response/ollama-to-openai.js:134`); the buffered body is converted by `nonStreamingHandler.js:313-316`. Neither bypasses the hub, and the pin has no leg that makes Ollama a *client* wire, so Ollama is a provider wire only (`internal/protocol/hub`). A Messages client reaches Ollama as `Messages>Chat>Ollama` and the Chat body between the two hops is not filtered for OpenAI-compatible upstreams, as at `translator/index.js:150`.

| ID | Pin behavior | Routeweft behavior | Why |
| --- | --- | --- | --- |
| V21 | `openai-to-ollama.js:125` forwards the `developer` role unchanged. | It is sent as `system`. | Ollama has no `developer` role; the instruction would be ignored or rejected. Same choice as the Messages leg (V12). |
| V22 | `openai-to-ollama.js:35` reads only `max_tokens`. | `max_completion_tokens` is `options.num_predict` when `max_tokens` is absent. | The pin leaves a caller that used the current name unbounded. Same as V12. |
| V23 | Everything the wire cannot carry is dropped without a word: `n>1`, `logprobs`, audio output, `functions`/`function_call`, parts other than text and data-URI images, roles it does not know, non-`function` tool calls (`:156-163,179-189`). A remote `image_url` is fetched and inlined first by `concerns/prefetch.js:10-12,45`. | Each is a translation error, so the dispatcher tries the next candidate; a request with no surviving message is refused too. A remote image is refused: fetching it is an outbound request with its own SSRF rules (`concerns/image.js`), which this wire does not make. | A dropped image or tool is a confident wrong answer with a 200. Prefetch is tracked separately. |
| V24 | `ollama-to-openai.js:93` numbers a response tool call `function.index ?? i`, with `i` restarting at 0 on every line; `openai-to-ollama.js:108` gives every request call index 0 unless the client sent one. | Response: the count of calls so far in the whole response. Request: the call's place in its turn. | Two calls on two lines shared index 0 and a Chat accumulator merges them. |
| V25 | `concerns/toolCall.js:9` mints `call_<i>_<Date.now()>` for a call with no id. | `call_msg0_tc<n>_<name>` (`GenerateToolCallID`), stable for the same response. | A clock in an id makes the round trip untestable and defeats prompt-cache reuse. |
| V26 | `ollama-to-openai.js:44-46,129` always attach `usage`, with zeros when the response reports no counts. | Usage is present only when `prompt_eval_count` or `eval_count` is positive (`concerns.OllamaUsage`), the same rule the accounting parser uses; `prompt_eval_cached_count` is the cache-read subset of the prompt. | A zero that was never measured is not a measurement (V8). The accounting parser (`shared.parseOllamaUsage`) now reads the cached count too, so a native Ollama record gains `cache_read`; this changes recorded usage, not stored schema. |
| V27 | `ollama-to-openai.js:35-47` returns at `done:true` without reading `message`. | Text, thinking and tool calls on the final line are emitted before the finish chunk. | Some Ollama versions put the last text there; the pin loses it. |
| V28 | An `{"error":...}` line or body becomes an empty completion; a stream that stops before `done:true` ends silently (`ollama-to-openai.js:49-58`). | The error object, or the generic incomplete-stream error, ends the stream in the client's protocol (an error object then `[DONE]`), and a buffered error body is a 502. | SPEC 9.3: a failed stream never claims success. |
| V29 | `openai-to-ollama.js:28-44` sends only `temperature`, `num_predict` and `top_p`. | `stop` is `options.stop`, `seed`, `presence_penalty` and `frequency_penalty` are `options.*`, and `response_format` is `format` (`json_object` is `"json"`, `json_schema` is the schema). An unknown format type, a schema-less `json_schema`, or a `stop` that is not a string or list of strings is refused. | Ollama's `/api/chat` takes `format` ("`json` or a JSON schema") and `options` (`stop`, `seed`, `presence_penalty`, `frequency_penalty`, ...) (ollama `docs/api.md`, checked for this round). The pin drops them, so JSON the client asked for arrives as prose and a stop string is run past, with a 200. The Messages leg already carries the same fields from the same hub body. |
| V30 | `openai-to-ollama.js:51-54` forwards `tool_choice` verbatim; `/api/chat` has no such field. | `none` withholds the tools. `auto` or absent sends no `tool_choice` (absent already means auto). `required` or a named function is refused. A non-empty `logit_bias` is refused. | A forwarded `none` lets the model call a tool the client forbade; a forwarded `required` is not honoured. `/api/chat` has no `tool_choice`, `n`, `logit_bias`, or logprobs control. |

Mirrored though odd, each asserted by a test:

- **M1** `openai-to-ollama.js:130`: a non-assistant turn with no text is dropped even when it holds an image, and the image goes with it.
- **M2** `openai-to-ollama.js:87`: an empty tool result is dropped, which leaves its call unanswered (the empty replies `FixMissingToolResponses` inserts are therefore dropped too, so it is not run for this wire).
- **M3** `openai-to-ollama.js:90`: a tool result whose call id is unknown takes `msg.name`, then `unknown_tool`.
- **M4** `ollama-to-openai.js:78`: the first chunk carries no `role`.
- **M5** `openai-to-ollama.js:110-112`: unparseable tool arguments are `{}`. (Valid JSON that is not an object is `{}` here; the pin would forward an array.)

#### `ReportsUsage` and `ollama-local`

The pin advertises usage for `ollama` only (`registry/ollama.js:36-39`, `features.usage`); `registry/ollama-local.js:1-19` declares no features, and `TestUsageCapabilityMatchesOracle` asserts `ollama-local` is `usage=false`. Routeweft still accounts usage for `ollama-local` requests through `shared.ParseUsage(FamilyOllama)` (`prompt_eval_count`, `eval_count`, `prompt_eval_cached_count`). The flag mirrors the pin's capability advertisement, the Ollama Cloud limits API, not our accounting, so the read model describes the two Ollama providers differently on purpose.

#### Thinking

The Ollama wire sends thinking and Routeweft carries it: `message.thinking` is `reasoning_content` in a stream and in a buffered body (`ollama-to-openai.js:54,70,111,116`). **Known gap, not carried by this wire:** the request side. Ollama takes a top-level `think` (and `thinking` on a message), but it rejects `think` for a model that does not think, and the pin sends neither (`providers/capabilities.js:336-343` records that it drops the toggle). Which models accept it is per-model knowledge this PR does not have, so `reasoning_effort` and `reasoning` are not carried and an assistant turn's `reasoning_content` is not sent back.

#### Request fields, Chat to Ollama (`TestChatRequestFieldsAreCarriedOrRefused`)

| Chat request field | Ollama | Why |
| --- | --- | --- |
| `model`, `stream`, `messages` | carried | pin |
| `temperature`, `top_p` | carried (`options`) | pin |
| `max_tokens`; `max_completion_tokens` when `max_tokens` is absent | carried (`options.num_predict`) | pin; V22 |
| `tools` | carried | pin |
| part `text`, `refusal`; `image_url` data URI | carried (text joined with a newline; `images[]` raw base64) | pin |
| `developer` role | carried (as `system`) | V21 |
| `tool` message, assistant `tool_calls` | carried (`tool_name`; arguments as an object) | pin; V24 |
| `stop` (string or list) | carried (`options.stop`) | V29 |
| `seed`, `presence_penalty`, `frequency_penalty` | carried (`options.*`) | V29 |
| `response_format` (`json_object`, `json_schema`; `text` asks for nothing) | carried (`format`) | V29 |
| `tool_choice` `auto` (nothing to send); `none` (withholds the tools) | carried | V30 |
| `top_logprobs`, `store`, `metadata`, `user`, `stream_options`, `prompt_cache_key`, `service_tier`, `modalities` (text only) | not carried | bookkeeping or caching hints; `/api/chat` has no field for them and none changes the answer. `top_logprobs` only means something with `logprobs`, which is refused |
| `parallel_tool_calls:false` | not carried | **known gap**: `/api/chat` has no control for it, so a response may hold several tool calls; the client still receives a valid list |
| `reasoning_effort`, `reasoning`, assistant `reasoning_content` | not carried | known gap, see Thinking |
| `n>1`, `logprobs:true`, audio output, `audio`, `functions`, `function_call`, non-empty `logit_bias`, `tool_choice` `required` or a named function, a `response_format` or `stop` that cannot be expressed | refused | V23, V29, V30 |
| part `input_audio`, `file`, unknown types; `image_url` that is not a base64 data URI; image in a tool-call turn or tool result; unknown roles; non-`function` tool calls; nothing left after conversion | refused | V23 |

#### Response fields, Ollama to Chat (`TestStreamIsConvertedLineByLine`, `TestBufferedBodyIsConverted`)

| Ollama field | Chat | Notes |
| --- | --- | --- |
| `message.content` | `content` | |
| `message.thinking` | `reasoning_content` | |
| `message.tool_calls[]` | `tool_calls[]` (`index`, `id`, `type`, `function.name`, `function.arguments` as a string) | V24, V25 |
| `done_reason` | `finish_reason`: `tool_calls`/`tool_use` to `tool_calls`, `length`/`max_tokens` to `length`, anything else `stop` (`finishReason.js:40-48`) | forced to `tool_calls` when the response made calls |
| `prompt_eval_count` | `prompt_tokens` | cache-inclusive |
| `prompt_eval_cached_count` | `prompt_tokens_details.cached_tokens` | subset of the prompt |
| `eval_count` | `completion_tokens` | |
| `model` | `model` | request model (stream) or `"ollama"` (buffered) when absent |
| `error` | failure (V28) | |
| `total_duration`, `load_duration`, `created_at` and other timing fields | not carried | no Chat equivalent |

#### Errors

A translation error (refusal) moves the dispatcher to the next candidate. A non-2xx upstream answer is relayed with its status and body, as for every leg. A 2xx answer that cannot be converted is a 502 for a buffered body; for a stream it is a failure terminal (V28). A cancelled client gets no terminal.

### 6.5 Cursor and CommandCode legs: where Routeweft differs from the pin (#119, PR 3)

The pin's four legs are `request openai-chat>cursor` (`translator/request/openai-to-cursor.js:172-183`, registered `:185`), `response cursor>openai-chat` (`translator/response/cursor-to-openai.js:13-28`, `:30`), `request openai-chat>commandcode` (`translator/request/openai-to-commandcode.js:185-219`, `:221`) and `response commandcode>openai-chat` (`translator/response/commandcode-to-openai.js:52-181`, `:183`). None bypasses the hub (`translator/index.js:106-125`, `:208-235`), and the pin has no leg that makes either a *client* wire, so both are provider wires only (`internal/protocol/hub`).

**Scope.** These are the legs, as pure Chat-body and Chat-stream converters (`internal/protocol/cursor`, `internal/protocol/commandcode`). Neither provider is routable yet, and `ingress.unwired` keeps it that way, so `TestEveryProviderIsReachableOrTracked` still lists both:

- **Cursor** needs a transport the pin implements in its executor, not its translator: Connect-RPC protobuf over HTTP/2 (`executors/cursor.js:114-180`, `cursorProtobuf.js:616`) and its checksum headers (`utils/cursorChecksum.js`). The pin's response leg is the identity because that executor already decodes the reply into Chat chunks.
- **CommandCode** needs behaviour that belongs in `internal/providers/wire` (owned by #115 PR-C): the per-request `x-session-id` and the registry headers (`executors/commandcode.js:28-40`), the always-streamed upstream (`:24`; a non-stream client is served by `CommandCodeResponseToChat`, which aggregates the NDJSON), and the pre-stream error inspection and 502/503/504 retry (`:42-61, 140-241`, `parseCommandCodeError` `:80-138`).

#### Two kinds of "cannot carry"

Every refusal below says which of these it is, because they are not the same claim:

- **E, evidence.** The wire's own shape shows it cannot express the field. For Cursor the evidence is the pin's encoder and field list (`cursorProtobuf.js:17-189`, `executors/cursor.js:114-180`); the `.proto` itself is not available offline, so this is evidence about what the pin sends, and nothing stronger is claimed.
- **U, not verifiable offline.** There is no evidence either way, so the field is refused rather than guessed. No row in this section says E when it means U.

#### Differences

| ID | Pin behavior | Routeweft behavior | Why |
| --- | --- | --- | --- |
| V31 | `openai-to-cursor.js:181` overwrites `max_tokens` with `DEFAULT_MIN_TOKENS` (32000), and the executor never reads it. | A client's `max_tokens`, `max_completion_tokens` and `max_output_tokens` are **not carried and not refused**; nothing resembling a limit reaches the output. | **E**: the pin's protobuf has no token-limit field. Refusing would break every client that always sends one. `TestMaxTokensIsNotCarriedAndNotRefused` fails if a later change starts honouring or refusing it. |
| V32 | `executors/cursor.js:834,1087,1310` attach an estimate (`estimateUsage`, chars/4) as Cursor's usage. | No usage is made. The response leg returns what the transport emits and adds none; a chunk or body with no `usage` stays without one. A `usage` the transport itself reports is passed through unchanged, so the rule that nothing estimates one is the transport's to keep. | Cursor reports no usage on this wire. An estimate is not a measurement (V8): a missing usage is "not reported", never "zero tokens used", and `usage_observed` stays 0. The `cursor` registry row has no `ReportsUsage`, as at the pin (`registry/cursor.js`); `TestUsageCapabilityMatchesOracle` is untouched. |
| V33 | `openai-to-commandcode.js:100-104` replaces tool-call arguments it cannot parse with `{}`. | Arguments that are not a JSON object (malformed, array, number, a JSON string) are refused. An empty string or absent arguments are `{}` (a tool with no parameters). | `{}` rewrites the history the model reads, with a 200. |
| V34 | `openai-to-commandcode.js:192` sends `temperature: 0.3` when the client sent none. | No `temperature` is sent unless the client sent one (zero included). | The router inventing a sampling value is a behaviour no client asked for; the upstream's own default applies. `max_tokens` keeps the pin's default (64000) because the wire is Anthropic-style, where it is required. |
| V35 | `openai-to-commandcode.js:203-218` fills `config.workingDir` and `config.environment` from `process.cwd()` and `process.platform`. | `"/"` and `"linux"`, constants. `date` is today (UTC). | **Security-visible.** The pin sends the router host's working directory and OS to a third party, and the value differs between deployments. Whether the upstream needs a realistic value is U; the constants are the minimum. |
| V36 | `commandcode-to-openai.js:100,121,135` keys a call on `event.id` at `tool-input-start` and `toolCallId` at `tool-call`; a `tool-call` whose id differs from its start is emitted as a second call, and `tool-input-delta` for an unknown id is dropped (`:120-123`). | One id space. Every id an event carries (`toolCallId`, `id`) names the same call, resolved by one helper for `tool-input-start`, `-delta` and `tool-call`; a start registers all of them. A `tool-call` that names a known id adds nothing; one with a different id, **or with no id at all**, is the **same call** as the oldest started call of that tool name not yet consolidated, so nothing is emitted twice and nothing is dropped. When only the start was seen, the consolidated `input` becomes the arguments (the pin leaves the call with none, `:136`). A call that never receives arguments ends with `{}` on both legs, where the pin leaves an empty string. A `tool-input-delta` for a call never started fails the stream. | The id the client sees is the one the start carried: a streamed call cannot be renamed afterwards. A phantom second call, or lost argument bytes, is a wrong answer with a 200. Same-name parallel calls are matched oldest first. |
| V37 | `concerns/usage.js:61-65` reads `inputTokens`, `outputTokens`, `totalTokens` only, and always attaches usage when the event has an object. | Those three as given (`total_tokens` is the upstream's when it sent a number, else prompt plus output), only when a count is positive. `cachedInputTokens` and `reasoningTokens`, when present and positive, travel as `usage.commandcode_observed.{cached_input_tokens,reasoning_tokens}`: observations beside the counts, **never** subtracted from them and not placed in `prompt_tokens_details`/`completion_tokens_details`, whose meaning is "a subset of". | It is not established that `inputTokens` is cache-inclusive, and a wrong subtraction corrupts every downstream cost figure. Our record is richer than the pin's, which ignores both. Reading `commandcode_observed` into the usage record is accounting work for the PR that binds the wire. |
| V38 | `commandcode-to-openai.js:174-178` ignores every event type it does not list, a line that is not JSON (`:68-72`), and an event with no `type` (`:75`). | The pin's listed types, and `tool-input-end`, are ignored. Any other type is passed to the `unknown` callback (counted) and ignored, **unless** it carries `text`, `delta`, `textDelta`, `inputTextDelta`, `input`, `toolName` or `toolCallId`, in which case the stream fails. A typeless event is treated as unknown. A line that is not JSON fails the stream. A `text` or `delta` of the wrong type fails it, and so does a `finishReason` of the wrong type on `finish-step` or `finish`. | Forward compatibility is fine until it costs the client content: a new event type that silently dropped text is a confident wrong answer. |
| V39 | `commandcode-to-openai.js:90-98` reads reasoning from `text` only. | `text`, else `delta` (the AI SDK's name for it). | The pin loses reasoning an upstream names `delta`. |
| V28 (this wire) | The executor appends `[DONE]` on flush whatever came before (`executors/commandcode.js:303-309`); a stream that never sent `finish` looks successful, and `finish` with `tool-calls` is not checked against the calls made. | A stream that ends before `finish`, an `error` event, a `finish` for tool calls with no call, and every failure above end with an error object then `[DONE]`. A buffered body is an error. | SPEC 9.3. For Cursor the same checks apply to the Chat the transport emits (no finish, `[DONE]` before a finish, error object, tool finish without a call). |

Same as for Ollama and carried over unchanged: V21 (`developer` is a system message; the pin drops it for Cursor and turns it into a user turn for CommandCode), V22 (`max_completion_tokens` is read by CommandCode), V23 (a remote image, audio, a file or an unknown part is refused, not dropped), V25 (stable generated ids) and V30 (`tool_choice` `none` withholds the tools; `required` or a named tool is refused; the output never names a `tool_choice`).

Mirrored though odd, each asserted by a test:

- **M6** `openai-to-cursor.js:84-90,92-101`: a system turn is a user turn `[System Instructions]\n…`, and a tool output is an XML `<tool_result>` block in a user turn (the pin's own comment: protobuf tool results made Cursor loop, `:3-7`).
- **M7** `openai-to-cursor.js:126,158-165`: a user or assistant turn with no text (and no tool call) is dropped.
- **M8** `openai-to-commandcode.js:137-139`: an assistant turn with tool calls and no reasoning carries a `{"type":"reasoning","text":" "}` block.
- **M9** `commandcode-to-openai.js:84,108`: the first chunk carries `role: "assistant"`; a later `finish-step` replaces the finish reason of an earlier one, and `finish` uses `totalUsage`, else the last `finish-step` usage.

#### Request fields, Chat to Cursor (`TestChatRequestFieldsAreCarriedOrRefused`)

| Chat request field | Cursor | Class and why |
| --- | --- | --- |
| `model`, `stream`, `messages` | carried | pin |
| text parts (`text`, `refusal`); `system`, `developer` (V21), `user`, `assistant`, `tool` roles | carried (M6) | pin |
| `tools` (function tools; a named function with no `parameters` is a tool, as the encoder defaults them, `cursorProtobuf.js:443`; `parameters` that are not an object are refused) | carried | pin (MCP tools, `executors/cursor.js:168`). The CommandCode leg applies the same rule (empty object schema) |
| assistant `tool_calls` | carried (the executor flattens them to text, `:95-99`) | pin |
| `reasoning_effort`; `reasoning.effort` (`summary` tolerated) | carried as `reasoning_effort` | pin (`:618`) |
| `tool_choice` absent or `auto` (nothing to send); `none` (withholds the tools) | carried | V30 |
| `max_tokens`, `max_completion_tokens` | **not carried, not refused** | **E**, V31 |
| `top_logprobs`, `store`, `metadata`, `user`, `stream_options`, `prompt_cache_key`, `service_tier`, `modalities` (text only), `parallel_tool_calls`, empty `stop` / `logit_bias` / `response_format` / `tools` | not carried | bookkeeping or caching hints that change nothing about the answer |
| `temperature`, `top_p`, `seed`, `presence_penalty`, `frequency_penalty` (any non-null value, zero included) | refused | **U** |
| `stop`, `logit_bias` (any non-empty shape), `response_format` other than text, `strict` on a function tool, `reasoning` keys other than `effort`/`summary` | refused | **U** |
| `tool_choice` `required`, a named function, or any other value | refused | **U**: the pin strips it (`:176`) and the field list has none |
| `n>1`, `logprobs:true`, audio output, `audio`, `functions`, `function_call` | refused | **E**: the executor emits one choice, text and tool calls only (`executors/cursor.js:833,859`) |
| `image_url`, `input_audio`, `file`, unknown parts, in any role; a non-function tool; unknown roles; a non-function tool call; a tool call without an id or name; a tool result without a `tool_call_id` (both legs); nothing left after conversion | refused | **E** for parts (the agent path takes text, `:81-90`, and the legacy field list has no image field); structural otherwise |

#### Request fields, Chat to CommandCode

| Chat request field | CommandCode | Class and why |
| --- | --- | --- |
| `model`, `stream`, `messages`, `max_tokens`, `top_p`, `temperature` (only when given: V34) | carried (`params`) | pin |
| `max_completion_tokens`, then `max_output_tokens`, when `max_tokens` is absent | carried | V22 |
| `system`, `developer` roles | carried (`params.system`, joined with a blank line) | pin; V21 |
| text parts; `image_url` data URI in a user turn | carried (`image` block with `mimeType` and `mediaType`) | pin |
| `tool` message, assistant `tool_calls`, assistant `reasoning_content`/`thought`/`reasoning` | carried (`tool-result` with the name resolved from the call, `unknown_tool` when there is none; `tool-call`; `reasoning`) | pin; V33 |
| `tools` (function tools; the pin's bare `{name, input_schema\|parameters}` shape) | carried (`{name, description, input_schema}`) | pin |
| `tool_choice` absent or `auto`; `none` | carried / withholds the tools | V30 |
| `top_logprobs`, `store`, `metadata`, `user`, `stream_options`, `prompt_cache_key`, `service_tier`, `modalities` (text only), `parallel_tool_calls` | not carried | as for Cursor |
| `seed`, `presence_penalty`, `frequency_penalty` (any non-null value); `stop`, `logit_bias`, `reasoning_effort`, `reasoning` (any non-empty); `response_format` other than text; `strict` on a function tool; `tool_choice` `required` or a named function | refused | **U** |
| `n>1`, `logprobs:true`, audio output, `audio`, `functions`, `function_call` | refused | **U** for this wire (no evidence it can); the pin drops them |
| remote or malformed `image_url`; an image in any turn but a user's; audio, file and unknown parts; non-function tools and tool calls; arguments that are not an object (V33); a tool result without an id | refused | V23 (a remote image is an outbound fetch this wire does not make); structural otherwise |

#### Response fields, to Chat

| Upstream | Chat | Notes |
| --- | --- | --- |
| Cursor: the Chat chunks/body the transport emits | the same, as received | identity (V32: no usage made) |
| Cursor: `error` object with no `choices` | failure | |
| CommandCode `text-delta` (`text`, else `delta`) | `content` | |
| CommandCode `reasoning-delta` (`text`, else `delta`: V39) | `reasoning_content` | |
| CommandCode `tool-input-start` / `-delta` / `tool-call` | `tool_calls[]` (`index`, `id` once, `type`, `function`) | V36 |
| CommandCode `finish-step` / `finish` `finishReason` | `finish_reason`: `stop`/`error` to `stop`, `length`, `tool-calls`/`tool_use` to `tool_calls`, `content-filter` to `content_filter`, anything else as given (`finishReason.js:20-29`) | not forced to `tool_calls` when calls were made |
| CommandCode `totalUsage` / `usage` | `usage` | V37 |
| CommandCode `error` | failure `[CommandCode error: …]` | V28 |
| `start`, `start-step`, `*-start`, `*-end`, `tool-input-end`, `provider-metadata`, `message-metadata` | not carried | no client-visible content |
| any other type | counted; fails the stream if it carries content | V38 |

### 6.6 Direct routes and AWS EventStream foundation (#119, PR 4a)

PR 4a adds a provider-neutral binary reader and proves the existing direct-pair
preference in `hub.Route` through ingress against a scripted HTTP endpoint.
The fixture registers synthetic request/response pairs only in the test binary,
with available Chat pivots that must lose to the direct pair. It preserves the
source request bytes, including a Messages-only system field and a nested
extension. It proves incremental output, failure terminals, no replay,
non-2xx status/body preservation and upstream cancellation. It is a framing and
dispatch test, not a Kiro translator or live-provider qualification. The two Kiro Chat legs are PR 4b (6.8); the two Messages legs are PR 4c (6.11).

Framing provenance, all at oracle `1c3b9f47837d42ea64992063309064af755a14a3`:

- `open-sse/executors/kiro.js:17-18`: 24 MiB message / 128 KiB header bounds.
- `:33-39`, `:58-62`: CRC32 polynomial and checksum; Go uses `hash/crc32` IEEE.
- `:945-967`: incremental prelude validation and frame bounds.
- `:1216-1236`: 16-byte minimum, exact length, prelude CRC and message CRC.
- `:1238-1288`: bounded typed headers, duplicate names and unknown-type errors.
- `:1291-1299`: the pin's JSON payload decoding; the shared reader retains bytes
  instead, leaving JSON/shape validation to each protocol adapter.

The reader retains int64, timestamp and UUID header values that the pin advances
past without storing (`:1272-1274`, `:1283-1285`). It additionally rejects empty
header names and malformed UTF-8 instead of decoding with replacement characters.
These are explicit validation differences. It shares Routeweft's 32 MiB total
translation budget and validates frames past a terminal through EOF, so trailing
garbage cannot turn an otherwise valid prefix into success. Unlike the pin's
output-based clean-EOF success (`:1065-1075`), absence of a recognized terminal
always fails. No integrity retry/replay is implemented.

Guard coverage before push:

| Input dimension | Crossed branches / expected result |
| --- | --- |
| Absent bytes | No recognized terminal: error; clean EOF after a valid terminal: success |
| Literal `null`, short/truncated prelude, every truncated frame prefix, invalid bounds/CRCs, duplicate/empty/invalid UTF-8 headers, every unknown header type 10–255 | Alone and following a valid terminal; contiguous and byte-fragmented input; error, with no malformed frame delivered |
| Prelude lengths 0/1/11/12/15/16/message-limit/message-limit+1/uint32-max | Header lengths 0/1/header-limit−1/header-limit/header-limit+1/uint32-max × good/bad prelude CRC; a failing source proves invalid preludes are refused before a body read |
| Header types 0–9, including empty byte/string values | Every non-empty header prefix; complete values retained, incomplete values refused even with a valid frame CRC |
| Empty / `null` / string / boolean / number / list / object / binary payload | Terminal absent/present × contiguous/byte-fragmented; bytes retained, EOF succeeds only with a recognized terminal |
| Two coalesced frames | Every transport split point; exactly two deliveries |
| Message / header limits | Limit minus one, exact limit, limit plus one; bounded acceptance/refusal |
| Total stream budget | Exact 32 MiB and an extra frame; extra frame refused |
| Source errors, consumer errors, canceled context | Before/during/after frames; failure propagated, no synthesized success |

Approved Kiro decisions for PRs 4b/4c (not implemented by 4a): honor explicit
client `max_tokens` instead of the Chat leg's fixed 32000
(`request/openai-to-kiro.js:311`); use shared cache-exclusive Messages wire usage
with separate cache counts instead of the direct leg's prompt copy
(`response/kiro-to-claude.js:77-95`); refuse lossy description/schema/history
normalization; never implement the session replay cache
(`utils/kiroSessionReplay.js:65-130`); require a terminal and never replay output.
Unknown events must be counted, proven harmless transport metadata may be
ignored, and content-bearing unknown events must fail. The framing reader
delivers every event; event classification/counting belongs to those legs.

**Known gap / separate follow-up under #119: Kiro saver view.** Kiro conversation
history/current-message fields have no saver view. That follow-up must preserve
the established transform order; 4a adds neither a Kiro view nor a new order.

**Pending wire integration after #135:** account-aware request preparation
(auth method, profile ARN, region, connection/session identity and relevant
original headers); endpoint-aware Kiro auth/identity/target headers; successful
AWS EventStream response adaptation before translation and measured accounting,
including bounded aggregation for non-stream clients. Non-2xx bodies, SSRF,
cancellation and bounded account fallback must retain their existing behavior.
No `internal/providers/wire` file changes are part of 4a. No provider, capability,
credential flow, stored usage convention, database schema or dependency changes.

### 6.7 Gemini and Vertex legs: where Routeweft differs from the pin (#119, PR 5a)

The pin registers five legs for these two wires, plus seven for Gemini CLI and Antigravity (PR 5b). This section is the five: `request openai-chat>gemini` (`translator/request/openai-to-gemini.js:314-316`, registered `:529`), `request openai-chat>vertex` (`openai-to-vertex.js:37-40`, `:42`), `request gemini>openai-chat` (`gemini-to-openai.js:9-72`, `:150`), `response gemini>openai-chat` (`response/gemini-to-openai.js:41-156`, `:159`) and `response vertex>openai-chat` (`:162`). None bypasses the hub (`translator/index.js:106-125`, `:208-235`). The pin shares one request function (`:150-151`) and one response function (`:159-162`) between Gemini and Gemini CLI, and the response function across all four wires; PR 5a implements them and PR 5b (6.9) registers the rest. The pin has **no** `openai-chat>gemini` response leg, so the `gemini>openai-chat` request leg, which serves a Gemini *client*, has nothing to convert the answer back with: it is built because the ratchet demands it, and it is not routable (`TestGeminiIsTranslatableFromConvertedClients`).

**Routing.** Gemini becomes reachable from Chat, Responses and Messages clients. **Vertex does not** (G13). Gemini's URL is `v1beta/models/{model}:generateContent` or `:streamGenerateContent`, its credential `x-goog-api-key` (`registry/gemini.js:26-30`).

Two kinds of "cannot verify", kept apart as in 6.5: **E**, the wire shows it cannot express the field; **U**, not verifiable offline, so the field is refused rather than guessed or the claim is recorded as unverified. No row here says E when it means U.

#### Differences

| ID | Pin behavior | Routeweft behavior | Why |
| --- | --- | --- | --- |
| G1 | `executors/vertex.js:112-123,66-110` authenticates Vertex with a bearer token (service-account JSON or ADC) or, for a raw key, `?key=` in the URL; the project is optional for a raw key (`:104-106`) and required for OAuth (`:86-93`). `x-goog-api-key` is the **Gemini** row's header only. `resolveProjectId` (`:35-49`) probes with a dummy request, for `vertex-partner` only (`:158-164`). | The legs are auth- and URL-agnostic: they produce a body. Vertex's URL and credential are #115 PR-C's target hook. **That `x-goog-api-key` is accepted by Vertex is not asserted: U, not tested here.** No probe, ever. | The owner's brief named the header for Vertex; the pin does not support it, and the pin wins. A header no test can confirm is not written into the repository. |
| G2 | `config/defaultThinkingSignature.js` ships three captured base64 thought-signature blobs and `openai-to-gemini.js:162-171,187-190` attach them to replayed reasoning and to a turn's first function call; `services/thoughtSignatureStore.js` caches real ones in memory (1 h) and SQLite (7 days) keyed by session and tool-call id. | A signature a response carried is kept in a bounded, in-memory, TTL cache (`Signatures`: 2000 entries, one hour, least recently used evicted) and replayed on the next request. **Never persisted.** The key is the **scope** (authenticated client key id and provider connection id, length-prefixed), the call id, name and arguments; no scope means nothing is stored and nothing is found. When no signature is held the placeholder `skip_thought_signature_validator` is sent on a turn's first call and on replayed reasoning; a sibling call stays unsigned, as at the pin. The captured blobs are **not** copied. Vertex keeps the signature the response carried; the pin overwrote every signature with its Vertex blob. | A signature is a client's model state; served to another client it crosses a tenant boundary, and a generated id such as `call_msg0_tc0_lookup` repeats across clients. The captured constants are vendor values this repository has no right to impersonate. **That Google accepts the placeholder is U: not verified.** |
| G3 | `formats/gemini.js:481-516` invents turns: `"..."` when the history starts with a model turn, `"Continue."` when it ends on one, and a `functionResponse` of `"Continue."` for each unanswered trailing call; `openai-to-gemini.js:224-237` answers a call mid-history with an empty result. | Mirrored, all four, **deliberately**: they are a shape requirement of the wire (it starts and ends on a user turn and pairs every call with a response), not something said to the user. Assistant prefill is **not** refused. `TestConversationShape` asserts each. | Refusing prefill would make a shape real clients send unroutable for no benefit. |
| G4 | `thinkingUnified.js:307-317` applies `reasoning_effort`/`thinking` after translation, to `thinkingConfig`, from a model capability table. | `reasoning_effort`, `reasoning`, `thinking` and `generationConfig.thinkingConfig` are **not carried** (tracked on #125), as in 6.3. | This is "deferred", not "inexpressible": `thinkingConfig` carries them; choosing level or budget needs the table. Refusing would make every reasoning-capable client unroutable here. |
| G5 | `formats/gemini.js:407-434` adds a required `reason` parameter to every empty-object schema, at any depth, as an "Antigravity requirement". | A tool that declares no properties is sent **with no `parameters`**; no parameter is invented (6.9 A3 decides it for the two Cloud Code wires: omitted there too). A nested `{"type":"object"}` with no properties is sent as it is. | The model would return an argument the client never declared, a wrong answer with a 200. **That Gemini accepts a declaration with no `parameters`, and a nested bare object, is U: not verified.** |
| G6 | `openai-to-gemini.js:85-95`: for `gemini-4*` models `temperature`, `top_p` and `top_k` are silently left out (`isGemini4Model`, `providers/capabilities.js:752`). | **Left out, and recorded here as a named omission**: `temperature` and `top_p` for models matching `(^\|/)gemini-4($\|[-.])`. A malformed value is still refused. `TestGeminiFourOmitsSamplingVisibly` fails if it changes. | The pin's comment says Gemini 4 rejects them. **That is U: not verified here**; the ticket to verify it is #119. A drop nobody wrote down is the bug; this one is written down. |
| G7 | `formats/gemini.js:77-80` sends a remote image as `fileData` with `mimeType: "image/*"`; an unknown part is skipped (`:62-106`). | A remote (`http`/`https`) `image_url` is **refused**, with an error that says fetching it is out of leg scope. Audio and `file` data URIs are carried as `inlineData`; `file_id` and every unknown part are refused. | Whether Gemini accepts `image/*` is U. A skipped part is a model that never saw it. |
| G8 | `openai-to-gemini.js:82` and `formats/gemini.js:47-53` send all five `safetySettings` as `OFF` on every request. | Mirrored. | Gateway behaviour, not a client choice. Whether Vertex accepts `HARM_CATEGORY_CIVIC_INTEGRITY` is U. |
| G9 | `formats/gemini.js:147-168` deletes every name in the unsupported-keyword list at every depth, so a tool **parameter** called `title`, `format`, `default` or `const` is removed from `properties` and the model never learns it exists; a `$ref` is dropped, leaving `{}`; `convertConstToEnum` and `x-` stripping hit property names too. | Keywords are removed only where a keyword can stand, never as an entry of `properties`. A local `#/$defs/…` or `#/definitions/…` `$ref` is inlined (depth 8); a remote, dangling or recursive one is refused. The rest of the narrowing is the pin's, silent by necessity: a union keeps its best member, `allOf` merges `properties`/`required`, constraint keywords go. | A parameter that vanishes is a wrong answer. A schema the wire cannot express is a refusal, not `{}`. |
| G10 | The pin's buffered path (`nonStreamingHandler.js:196-256`) and its stream disagree: buffered `prompt = prompt + thoughts`, `completion = candidates`, finish is the raw lowercased reason; streamed `completion = candidates + thoughts` (`concerns/usage.js:38`) and the finish is mapped (`finishReason.js:30-39`). | One leg for both: the stream's arithmetic and the mapped finish. `concerns.GeminiUsage` is the only reader of the fields; `observed` is false when no count is positive, and a missing or zero total is prompt plus completion. `STOP` with calls is `tool_calls` (`:140`). | One request must not report two prompt sizes (V2). The usage record still carries the upstream's own figures through `shared.ParseUsage`, which reads `candidatesTokenCount` without thoughts: a thinking model's record is lower than what its client is shown (#122). |
| G11 | The pin streams with `?alt=sse` (`executors/vertex.js:102`: "required for proper SSE streaming"). | The stream leg reads **SSE or the JSON array** Gemini sends without it, from the first byte. Ingress cannot put a query on a translated endpoint, and the hook that will is #115 PR-C's. A body is complete only when a `finishReason` arrived; a plain EOF after one is success. | Until `alt=sse` is set, a streamed Gemini attempt works but the usage scanner reads no `data:` lines and the record says `UsageUnobserved`, never zero. |
| G12 | `finishReason.js:30-39` maps every reason it does not list to `stop`; `gemini-to-openai.js:46` returns nothing for a response with no candidate, so a blocked prompt never ends the stream. | `STOP`, `MAX_TOKENS`, `SAFETY`, `RECITATION`, `BLOCKLIST`, `PROHIBITED_CONTENT` and `OTHER` are answers (`stop`, `length`, `content_filter`, `stop`). **Any other reason** (`SPII`, `IMAGE_SAFETY`, `LANGUAGE`, `MALFORMED_FUNCTION_CALL`, `UNEXPECTED_TOOL_CALL`, an unspecified one, one added next year) is a failure terminal that names it. A blocked prompt (`promptFeedback.blockReason`) is a `content_filter` finish; no candidate and no block is a failure. A part with `executableCode`, `codeExecutionResult`, `fileData` or `functionResponse` fails (the pin ignores it); an upstream `error` object reaches the client as it was sent. | An unknown future reason silently becoming success is the class this ledger exists to stop. |
| G13 | n/a (the pin has no gate). | `vertex` is in `ingress.unwired`: `CanTranslate` is false for it from every client wire, `TranslateChat` has no vertex arm, and `unreachableProviders` still lists it. `TestVertexIsNotTranslatableUntilItsURLAndAuthLand` fails if the gate is lifted. **#115 PR-C removes the gate** together with `unreachableProviders["vertex"]` and adds the arm; it computes the endpoint and credential. | A client routed to Vertex before its URL and authentication exist fails at the last hop. |

Carried over unchanged: V19 (a tool choice is checked, `none` is `NONE`, `required` or a named tool is `ANY`, and a choice with no tools is not forwarded: the pin sends none for Gemini and silently serves `auto`), V20 (every unanswered call is answered), V22 (`max_completion_tokens` when `max_tokens` is absent), V25 (a call Gemini did not number gets `call_msg<i>_tc<j>_<name>`, the same every time: the pin's embeds the clock; the id of a repeated call is made unique on the way back in, as at the pin, `-2`, `-3`), and V30. `ToolNames` is parametrised for Gemini's name rule (`[A-Za-z_][A-Za-z0-9_.:-]{0,63}`): a rewritten name is put back on the response (the pin never restores it, so a renamed tool came back under a name the client did not declare), and two names that are one name once normalised are **refused**, never deduplicated or suffixed.

Mirrored though odd, each asserted by a test:

- **M10** `openai-to-gemini.js:251-266`: a tool result is `{result: parsed}` wrapped again, so plain text is `{"result":{"result":"text"}}`.
- **M11** `formats/gemini.js:449-472`: a `$ref` key in a tool result is renamed `_ref`.
- **M12** `formats/gemini.js:474-479`: on `gemini-4*` a merged user turn puts its `functionResponse` parts last.
- **M13** `openai-to-gemini.js:153`: a conversation of one system message is a user turn.
- **M14** `formats/maxTokens.js:15-20`: a Gemini client's `maxOutputTokens` with tools is raised to 32000 (as in 6.3).

#### Request fields, Chat to Gemini and Vertex (`TestChatRequestFieldsAreCarriedOrRefused`, `TestPresenceGuardsCoverEveryShape`, `TestToolChoiceAcrossToolShapes`)

| Chat request field | Gemini | Class and why |
| --- | --- | --- |
| `model`, `messages`, `max_tokens`, `temperature`, `top_p`, `tools`, `stream` (in the path) | carried | pin; G6 for Gemini 4 |
| `top_k` | not carried | OpenAI Chat defines no `top_k`; one rule for the family (6.9 F1). The pin forwarded it (`openai-to-gemini.js:90-95`) |
| `max_completion_tokens` when `max_tokens` is absent; `stop` (a string or up to five strings); `seed`, `presence_penalty`, `frequency_penalty`; `response_format` `json_object` / `json_schema` | carried | V22; the pin drops the rest. `stop` over five, a non-string, `max_tokens` not a positive number, a non-number sampling value, a `json_schema` with no or empty schema, an unknown `response_format`: refused |
| `tool_choice` | carried | V19: `none`, `required`, a named declared tool; an unknown value, an undeclared name or a non-function type is refused |
| `system`, `developer` (all of them, joined), `user`, `assistant`, `tool` | carried | pin, G3, M13 |
| text, data-URI `image_url`, `input_audio`, `audio_url`, inline `file` | carried | pin |
| assistant `reasoning_content` | carried (thought part and placeholder signature) | G2 |
| `reasoning_effort`, `reasoning`, `thinking` | not carried | G4 |
| `stream_options`, `store`, `metadata`, `user`, `service_tier`, `prompt_cache_key` | not carried | change nothing about the answer |
| `n>1`, `logprobs:true`, `top_logprobs`, `audio` output, `audio`, `functions`, `function_call`, `logit_bias`, `web_search_options`, `prediction`, `parallel_tool_calls:false` | refused | **E** for the choice count and logprobs (the response leg reads one candidate and no logprobs); **U** for the rest. Every shape of an "asks for something" value is refused and every shape of "asks for nothing" is accepted (`TestPresenceGuardsCoverEveryShape`) |
| a non-function tool, a tool with no name or declared twice, a name that collides once normalised | refused | the model would not be offered it, or could not be called back by its name |
| remote `image_url` (G7), `file_id`, unknown parts, a part list on a system or tool message that is not text, unknown roles, a tool call that is not a function, has no name or has no usable id, a `tool` message with no id or answering no call | refused | the model would not see it, or the pair cannot be formed |

#### Request fields, Gemini to Chat (`TestGeminiRequestFieldsAreCarriedOrRefused`)

| Gemini request field | Chat | Class and why |
| --- | --- | --- |
| `contents` (`text`, image `inlineData`), `systemInstruction` / `system_instruction`, `generationConfig.maxOutputTokens` (M14), `temperature`, `topP`, `tools[].functionDeclarations` | carried | pin |
| `stopSequences`, `seed`, `presencePenalty`, `frequencyPenalty`, `responseMimeType` / `responseSchema` / `responseJsonSchema`, `toolConfig` (`NONE`, `ANY`, one allowed name) | carried | the pin drops them; `toolConfig` was silently `auto` |
| a missing `role` | user | the pin reads anything but `user` as the assistant |
| `functionResponse` parts | one `tool` message each, paired by id, else by name oldest first | the pin returns at the first and discards the rest of the content |
| `thought:true` text | `reasoning_content` | the pin replays it as visible text |
| `topK`, `safetySettings`, `labels`, `thinkingConfig` | not carried | no Chat field (6.9 F1) / G4 |
| `cachedContent`, `candidateCount>1`, `responseLogprobs`, `logprobs`, an unknown `responseMimeType`, tools other than `functionDeclarations` (`googleSearch`, `codeExecution`, `urlContext`), `fileData`, non-image `inlineData`, an unknown part field, an unknown `toolConfig` key or mode, more than one allowed function name, a `functionResponse` that answers no call, a non-text system part | refused | the answer would be wrong or the capability silently missing |

#### Response, Gemini and Vertex to Chat (`TestFinishReasonsAreCompleteOrAFailure`, `TestStreamMatchesBuffered`, `TestTruncatedStreamsFail`)

| Upstream | Chat | Notes |
| --- | --- | --- |
| `text`; `thought:true` text; `functionCall`; `inlineData` | `content`; `reasoning_content`; `tool_calls[]` (`index`, `id`, name restored); `images` | the pin's buffered path appended markdown for an image; the stream form is used for both (G10) |
| `thoughtSignature` on a call, or on a part before it | kept under the scope (G2) | |
| `finishReason` | per G12 | |
| `usageMetadata` | `usage` (cache-inclusive prompt; reasoning inside completion) | G10 |
| a body that ends before a `finishReason`; invalid JSON; a part that is not an object | failure terminal then `[DONE]` | SPEC 9.3 |
| a cancelled client | no terminal | |


### 6.8 Kiro Chat legs: where Routeweft differs from the pin (#119, PR 4b)

This PR builds the two Chat legs of the Kiro wire: `request openai-chat>kiro` (`translator/request/openai-to-kiro.js:308-447`, registered `:449`) and `response kiro>openai-chat` (registered `translator/response/kiro-to-openai.js:185`). The pin dispatches both directly, with no pivot (`translator/index.js:106-108`, `:208-213`). The two Messages legs (`claude-to-kiro.js:231-366`, `kiro-to-claude.js:57-285`) are PR 4c and stay in `trackedLegGaps`. The code is `internal/protocol/kiro`.

**The response leg is not where the ticket says.** `kiro-to-openai.js:37-55` returns the chunks it is given, because the pin's executor already turned the EventStream into Chat chunks (`executors/kiro.js:629-1190`, `transformEventStreamToSSE`); the raw-event branch below it (`:57-181`) reads `usageEvent` and `done`, which that transform never emits, so no real stream reaches it. The leg is therefore the executor transform, reading frames with `concerns.ReadEventStream` instead of the pin's parser (`:1216-1300`). Where the ticket and the pin disagree, the pin wins.

**Routing.** Kiro stays in `ingress.unwired`: its transport needed the account-aware body and headers from `internal/providers/wire` (6.10), and without the Messages legs (6.11) a Messages client would pivot through Chat where the pin has a direct pair. It is still unwired for the endpoint binding named in 6.10 and 6.11. The legs receive the raw upstream body: ingress passes `response.Body` to `translation.Stream` (`ingress/openai.go:211-215`) and the whole body to `translation.Response` (`:231-242`), and the Kiro wire hook (6.10) does not wrap responses, so the legs still read EventStream directly.

#### Request, Chat to Kiro

| Chat field | Disposition | Reason |
| --- | --- | --- |
| `messages[].content` (string, text parts) | carried | `openai-to-kiro.js:108-170`; text parts of one message join with a newline, messages of one turn with a blank line |
| `system` / `developer` messages | carried as a user turn wrapped in `<instructions>` | Kiro has no system field (`:165-169`). The pin silently loses `developer` (`:94-100`); here it is a system message, as in every other leg |
| `image_url` with a base64 data URI of an image type | carried as `images[{format, source.bytes}]` | `:118-124`; the format is the media subtype as in the pin. Whether Kiro accepts every subtype is not verifiable offline |
| `image_url` with an `http(s)` URL | **refused** | Kiro takes bytes. The pin substitutes the text `[Image: url]` (`:125-128`) |
| `image_url.detail` | advisory | a rendering hint Kiro has no field for |
| other content parts, wrong-typed `content`, a part with no `type` | **refused** | the pin ignores them (`:129-136`) |
| `tool_calls` | carried as `toolUses` | `:186-216`; arguments must be a JSON object, or empty for a tool that takes none |
| malformed `tool_calls[].function.arguments` | **refused** | the pin flattens the call to text (`kiroConversation.js:129-134`) |
| `tool` messages | carried as `toolResults` | `:158-164`; `is_error` or `status:"error"` give status error. Array content joins its text parts (the pin sends `""` for it, `:159`); non-text parts are refused |
| tool-call/result pairing | **refused** when a call is unanswered, a result is orphaned, answers a call another turn made, or answers one twice | the pin repairs by flattening both sides to text (`:261-338`, `:373-391`). The error names the turn |
| tool-call ids | carried; characters Kiro rejects are removed on both sides, a missing id is generated for the client's own body | `concerns.EnsureToolCallIDs`. **Refused** when the id is empty once cleaned, over 64 characters, or two calls end up with one id (the pin suffixes, `:208-223`) |
| a call to a tool that is not among `tools` | **refused** | the pin flattens it (`:299-310`) |
| `tools` (function) | carried as `toolSpecification` on the current message only | `kiroConversation.js:429-431` |
| tool of another type, tool with no name, a name declared twice | **refused** | the pin ignores the type, skips the nameless tool (`:100`) and the second definition (`:103`). Byte-identical repeats are also refused |
| tool names | valid names are sent unchanged; the rest are rewritten and put back on the response | `concerns.ToolNameRule{Strict: true}` with Kiro's rule. **Refused** when two names are one name once normalized (the pin suffixes `_2`, `:47-61`). The pin also renames valid names that begin or end with `_`; this does not |
| tool description | carried; absent or empty becomes `Tool: <name>`; **refused** over 10237 characters | the pin truncates (`:108-111`). The limit is the pin's; whether Kiro enforces it is not verifiable offline |
| `temperature`, `top_p` | carried, an explicit `0` as `0` | `:426-431`. Absent and `null` send nothing; a non-number is refused |
| `max_tokens`, else `max_completion_tokens` | carried to `inferenceConfig.maxTokens` | the pin sends a fixed 32000 (`:311`). Absent sends nothing: whether Kiro applies a default is not verifiable offline. Zero, negative, fractional and non-numbers are refused |
| thinking intent: `reasoning_effort`, `reasoning.effort`, `thinking`, `output_config.effort`, `enable_thinking`, `thinking_budget`, `thinkingConfig`, the `model(level)` suffix, `-thinking`, a `<thinking_mode>` tag in a system or user message | carried | `kiroConstants.js:44-259` and `thinkingUnified.js:34-103`, ported inside the Kiro package: the budget prefix and, for Claude 4.6+ and GPT 5.6, `additionalModelRequestFields`. Unknown effort values and unrecognized parenthesized model overrides are refused instead of falling back to a default budget or dropping the suffix. The Gemini envelope shapes nested under `generationConfig` and `request` are refused as unknown fields |
| `-agentic` model suffix | carried | the pin's chunked-write prompt, verbatim, in front of the first user turn |
| `anthropic-beta: interleaved-thinking` | **Messages only** | a Chat client has no such header and `ChatRequest` carries none; the Messages leg reads it (6.11) |
| `tool_choice` other than absent, `null`, `"auto"`; `parallel_tool_calls:false` | **refused** | Kiro has neither; the pin ignores both. Non-boolean `parallel_tool_calls` is also refused |
| `stop`, `seed`, `logit_bias`, `top_logprobs`, `logprobs:true`, `n` other than numeric `1`, audio, `functions`, `function_call`, `web_search_options`, `prediction`, `response_format` other than text, non-zero `presence_penalty` / `frequency_penalty`, any field not listed here | **refused** | Kiro's request has no equivalent. A zero penalty is the identity |
| `stream`, `stream_options`, `user`, `store`, `metadata`, `service_tier`, `prompt_cache_key`, `prompt_cache_retention`, `safety_identifier` | advisory | the answer is the same without them |
| message/part `cache_control`, message `annotations`, echoed `reasoning_content` / `reasoning` / `reasoning_details` / `refusal` | advisory | Kiro has no cache field or reasoning history. **Known gap:** prompt-cache anchoring does not reach Kiro (as at the pin) |
| message `name` on user, assistant, system | **refused** | a participant name has no Kiro equivalent. Empty and wrong-typed names are also refused. On a `tool` message it is ignored: the call id pairs the result |
| conversation starting or ending on an assistant turn; a user turn with no text and no tool result (an image alone) | **refused** | the pin invents `continue` user turns (`kiroConversation.js:181-186`, `:19-21`) |
| consecutive turns of one role | carried, merged | Kiro alternates; no content is lost |
| tool-results-only user turn, assistant turn with no text | placeholder `Tool results provided.` / `...` | structural: the wire needs some text; neither reads as an instruction (`:81`, `kiroConversation.js:15`) |

**Deliberate divergences.** There is no session replay cache and the first user message is not frozen (`kiroSessionReplay.js:65-130`, `openai-to-kiro.js:359-376`): the pin keys it with no client dimension, so one client's prompt could be served to another. `conversationId` is a fresh random UUID for each request. The `[Context: Current time is …]` line the pin adds to every turn (`:356`) is not sent: it exists to keep the frozen message cacheable, nothing shows the wire needs it, and it would be a system fact the client never sent. The executor's integrity retry (`executors/kiro.js:437-505`) and its content heuristics (`:46-56`, `:185-197`) are not ported: a replay after output would repeat an answer already sent.

#### Tool schema rows

Kiro does not accept some keywords. Each row below is a decision, and `RequestInfo.Normalized` counts the rewrites so a test can assert them.

| Schema content | Disposition | Consequence |
| --- | --- | --- |
| no `parameters`, `{}`, a missing root `type`, missing `properties` | `type:"object"` and empty `properties` are filled in | says the same thing |
| root `type` other than `object` (including a list) | **refused** | a tool input is an object |
| `additionalProperties: true` or `null`, at any depth | stripped | the JSON-schema default; carries nothing |
| `additionalProperties: false`, at any depth | stripped, counted | **Kiro does not accept the keyword, so the model may emit a property the client did not declare; the client's own validator is the backstop.** It is a guarantee about what comes back, not a field the caller sent |
| `additionalProperties` as a schema, including `{}`, at any depth | **refused** | it declares structure the model must know about; dropping it changes what the model can produce |
| `required` naming an undeclared property (any depth, no `allOf`/`anyOf`/`oneOf`/`$ref`) | entry dropped, counted; duplicates collapse | the model cannot satisfy it either way. Not dropped where composition may declare the property |
| `required: []`, at any depth | dropped | says nothing |
| `required` that is not a list (any depth), or has an entry that is not a string | **refused** | |
| a property that is *named* `additionalProperties` or `required`; `enum`, `const`, `default`, `examples` values | untouched | the pin's blind walk (`kiroConversation.js:63-74`) strips these too; here only schemas are visited |

#### Response, Kiro EventStream to Chat

| Event or condition | Disposition |
| --- | --- |
| `assistantResponseEvent{content}` | carried as `content`; a payload that is not an object, or a `content` that is not a string, fails the stream (the pin ignores both, `:808`) |
| `<thinking>…</thinking>` in answer text | when the request injected thinking mode: the inside is `reasoning_content`, the tags are removed, one newline after the close is removed, and an unfinished tag is held back until the next frame (the pin drops the inside and leaks a split tag, `:810-828`). Otherwise the text is the model's own and passes through |
| `reasoningContentEvent` | carried as `reasoning_content`, in the pin's three shapes (`:834-841`) |
| `codeEvent{content}` | carried as `content` (`:842`) |
| `toolUseEvent` | fragments joined per `toolUseId`; a name change, a fragment-type change, a missing name or input, a blank or non-string id, input that is not an object, and the `tool_call` wrapper without a nested name and `arguments` fail the stream (`:846-871`, `:746-753`). An empty input string is `{}`. Calls are emitted after the reader confirms the body is whole (`:1036`); one unusable call means none of the turn's calls are emitted and the stream ends with an error (the pin drops it and answers 200, `:739-759`) |
| `messageStopEvent`, `metadataEvent` with a stop reason | the terminal. `end_turn`, `stop_sequence`, none: `stop`, or `tool_calls` if calls were made. `tool_use` with no call: failure. `max_tokens`: `length`, but failure with a call. `model_context_window_exceeded`: `length` only after output, otherwise failure. `refusal`, content-filter-like, `cancelled`, `pause_turn`, `malformed_model_output` and unknown reasons: failure (`:145-183`, `:1077-1120`). Failure reasons outrank reasons that can succeed; `tool_use` outranks `end_turn`, so a second terminal cannot hide a missing call |
| `metricsEvent` | usage from `concerns.KiroUsage`: `inputTokens` is taken as cache-exclusive, the prompt is input + cache read + cache creation, the cache counts are `prompt_tokens_details`; explicit zero and cache-only counts are measurements; negative, fractional, out-of-range, conflicting alias counts or overflowing totals are not a measurement (the pin copies input into the prompt and spells the cache counts outside `prompt_tokens_details`, `:906-922`). Whether Kiro's `inputTokens` is cache-exclusive is the owner's decision, not verified offline |
| `contextUsageEvent`, `meteringEvent`, `metadataEvent` without a stop reason | counted and ignored: the only events the pin reads for accounting and never for content |
| any other event type | counted; **fails the stream, including an empty payload**. Nothing else is proven harmless: that needs a sanitized capture |
| known event with an unread key holding a value | fails the stream; the pin ignores the key and the content in it may be text |
| clean EOF with no stop event, truncation, corrupt bytes after the stop | failure; the pin counts clean EOF with output as success (`:1065-1075`) |
| content after the stop event | failure; usage and bookkeeping after it are accepted |
| `:message-type` `exception` / `error` | failure carrying the upstream message (`:793-803`) |
| tool id absent | a stable generated id; the pin embeds the clock (`:854-855`) |
| tool name | restored from the request's name map; a name not in the map passes through, as at the pin |

**Known gaps, each its own follow-up under #119.** *Kiro saver view:* conversation history and the current message have no saver view; the transform order is unchanged. *Token estimates:* the pin's `contextUsagePercentage × context window` and `chars/4` estimates (`:1102-1113`) are not reported; no usage is better than an invented one. *Credits:* `meteringEvent` credits (`:895-905`) are counted, not reported, and no `kiro_credits` key is added to `usage`. *Time context:* see above.

**Wire prerequisites for #135 (header/body hooks in 6.10; region/order/fallback remain pending).** `profileArn` in the body and `x-amzn-codewhisperer-profile-arn` in the headers, account-aware: API-key, IdC and external-IdP accounts never use the shared default (`openai-to-kiro.js:325-341`, `executors/kiro.js:276-279`); the auth headers `TokenType`, `x-amz-sso-bearer`, `x-amzn-kiro-agent-mode`, `X-Amz-Target` for `codewhisperer.*` hosts and `Accept: application/vnd.amazon.eventstream` (`:235-282`); the three-host order and region rewrite (`:299-328`); endpoint-fallback statuses 401/403/404 (`kiroConstants.js:28`).

The request guards also refuse unread tool, call, content-part and thinking keys (including empty values), malformed thinking configurations, and strict tool-output guarantees. Supplied IDs are validated before shared repair can rewrite them. Known response events enumerate the keys they read or treat as bookkeeping; mixed nested and flat envelopes, multiple reasoning values and conflicting object tool fragments fail rather than select one and lose another.

Guard coverage before push:

| Input dimension | Crossed branches / expected result |
| --- | --- |
| Sampling values: absent, `null`, `0`, `0.0`, positive, string, bool, list | `temperature`, `top_p` and each `max_*` name; zero is emitted as `0`, absent and null send nothing, wrong types refuse; `max_tokens` wins, `null` falls to `max_completion_tokens` |
| Message and content shapes: absent, `null`, empty, wrong type, populated, malformed part | all four roles; refused unless meaningful |
| Tool history: unanswered, orphaned, wrong id, twice answered, undeclared, bad arguments (invalid / array / number), duplicate id, id equal once cleaned, unusable id, 65-character id, image result | each refused with the turn named; the whole conversation served |
| Tool names and descriptions: collision with a valid name, with each other, empty twice, same name different definition, identical repeat (refused), 10237 / 10238 characters | refused or served accordingly |
| Schema rows above, flat and nested | each row asserted, with its count |
| Thinking: every source, suffix and model family | prefix, flag, upstream model and extra fields asserted |
| Event payloads: absent, `null`, empty, string, number, list, object, wrong-typed field, unread key | every known event; error or carried |
| Stop reasons ×  with and without output, with and without calls | one outcome each |
| Thinking tag split | every split of the sample into two and into three frames, and byte by byte; plus tag edges |
| Truncation, trailing garbage, missing stop, post-stop content, source error, cancellation | failure terminal, nothing written after a cancel |
| Buffered and streamed | the same answer |

Validation for PR 4b uses sanitized synthetic EventStream fixtures and the pinned
leg inventory. The converter benchmark (three-frame text/usage/stop response,
Go 1.22.12, shared host under load) measured 0.779–0.890 ms/op, about 13.1 KB/op
and 197 allocations/op across three runs. This is a new, gated path with no
previous Kiro converter baseline; it does not establish production latency.
No SQLite schema, provider inventory, credentials or runtime dependency changes.
No live provider calls were made.

`go build ./...` and the normal Go suite (`go test -p 1 ./...`) pass. Race tests
pass for Kiro, concerns, hub, ingress and the pinned parity suite. The broad
`go test -race ./...` run on this host hit the existing runbook's ten-second
readiness deadline and the app package's ten-minute total deadline; the normal
versions pass, and the app test active at the deadline passes separately under
race. These broad-run timeouts remain a validation limitation, not a green race
suite claim.

### 6.9 Gemini CLI and Antigravity legs: where Routeweft differs from the pin (#119, PR 5b)

Seven legs, all through the Chat hub (none bypasses it): `request openai-chat>gemini-cli` (`translator/request/openai-to-gemini.js:319-340`, `:343-376`, registered `:530`), `request openai-chat>antigravity` (`:518-526`, registered `:531`; Claude models `:379-509`, `request/openai-to-claude.js:17-201,341-392`), `request gemini-cli>openai-chat` (`request/gemini-to-openai.js:9-147`, registered `:151`), `request antigravity>openai-chat` (`request/antigravity-to-openai.js:11-230`, registered `:242`), `response gemini-cli>openai-chat` and `response antigravity>openai-chat` (`response/gemini-to-openai.js:41-156`, registered `:160-161`; the same function as 6.7, which already reads the `{"response": ...}` envelope) and `response openai-chat>antigravity` (`response/openai-to-antigravity.js:9-120`, registered `:123`). The pin has no `openai-chat>gemini-cli` response leg, so `gemini-cli>openai-chat` serves a Gemini CLI client with nothing to answer it, as `gemini>openai-chat` does (6.7); it is built because the ratchet demands it.

**Both wires are unrouted.** `gemini-cli` and `antigravity` are in `ingress.unwired`, as Vertex, Cursor, CommandCode and Kiro are. Binding either needs the Cloud Code URL and its `?alt=sse` query, the OAuth bearer, the client headers, and the project id the envelope names (`executors/gemini-cli.js:10-35`, `executors/antigravity.js:118-135`): the wire hook's. When a wire is bound, `ingress.unwired`, the missing `TranslateChat`/`TranslateResponses`/`TranslateMessages` arms, `unreachableProviders` and `TestCloudCodeWiresAreNotTranslatableUntilTheyAreBound` all come out together. **Streaming is SSE** (`streamGenerateContent?alt=sse`, `Accept: text/event-stream`); neither wire uses AWS EventStream, so the reader from PR 4a is not consumed here.

Not legs, so not here: URL, headers, OAuth refresh, retry and backoff, the IDE-format `requestId` rebuild (`executors/antigravity.js:100-111`), the image-generation path (`:144-189`; out of scope, AGENTS 2), the tool-group merge (`:237-259`), the competing-client prompt rewrites (`config/appConstants.js:175-183`) and `cloakTools` (dead at the pin: `translator/index.js:178-185` is commented out).

U = not verifiable offline (no live call exists in this repository); E = the wire cannot express it. No row says E when it means U.

#### Differences

| ID | Pin behavior | Routeweft behavior | Why |
| --- | --- | --- | --- |
| C1 | The CLI client leg reads only top-level `contents` (`gemini-to-openai.js:43`), so a Cloud Code envelope is an empty conversation; the Antigravity leg reads `body.request \|\| body` (`antigravity-to-openai.js:12`) and ignores the rest. | Both client legs accept a bare or a `request`-wrapped body. A body with the Gemini keys in both places, a `request` that is not an object, an unknown envelope key, no or empty `contents`, or a `requestType` other than absent, null or `agent` is **refused**. `project`, `model`, `userAgent`, `requestId`, `user_prompt_id` and the client's `sessionId`/`session_id` are ignored in either form. Both the envelope and its inner body undergo the key and `requestType` checks. | The gateway has already decided the project, model and session; a client's value is not honoured, and that is a stated reason, not a drop. `requestType` other than `agent` is image generation or search, which this product does not serve. |
| C2 | `generateProjectId()` invents `useful-fuze-ab12c` when the credential has no project (`formats/gemini.js:137-143`, `openai-to-gemini.js:344`). | `CloudCodeOptions.Project` is required and checked (`[A-Za-z0-9_.:-]{1,128}`); an empty or malformed one is **refused**. Nothing is invented. `RequestID` defaults to `agent-<uuid>`; `SessionID` defaults to `toNumericSession("routeweft:" + RequestOptions.Scope)`, and to nothing without a scope. | An identifier that routes a request must never be made up. Whether Cloud Code needs `sessionId` is U. |
| A1 | The CLI and Antigravity bodies share one base with Gemini (`openai-to-gemini.js:77-311`). | The same function (`ChatRequestToGemini`) runs for both, so 6.7 G1-G12 apply: tool ids, V19 tool choice, G3 turn shape, G6, the schema narrowing, refused fields. | One conversion, one behaviour. |
| A2 | The leg replays reasoning as a `thought` part and a signature-only part (`:162-171`); Antigravity's executor then deletes every `thought` part and signature-only part before sending (`executors/antigravity.js:203-207`). | Antigravity: replayed `reasoning_content` is **not sent** (the wire never saw it at the pin). CLI: sent, with the placeholder signature (6.7 G2). | Net behaviour, stated. Whether Antigravity would accept them is U. |
| A3 | `cleanJSONSchemaForAntigravity` phase 5 (`formats/gemini.js:407-444`) and the executor (`antigravity.js:254`) add a required `reason` argument to every empty object schema, at any depth. The response leg never removes it (`gemini-to-openai.js:21,31`), so the client receives an argument it never declared. No pin test covers it. The executor placeholder traces to `c43f8c54` (2026-04-26, "Antigravity INVALID_ARGUMENT errors"); the `{}` fill to `e3e3e235` (2026-07-29), whose message says **Vertex** rejects an orphan `{}`. | **Omitted** for both wires: a tool that declares no properties is sent without `parameters`, as 6.7 G5. No argument is injected and nothing is stripped. | Injecting an argument the client never declared is a wrong answer with a 200; a rejection is a 400 the caller can see. **That Antigravity or Cloud Code rejects a declaration with no `parameters` is U**; the evidence for it is one commit title. `TestNoReasonArgumentIsInjected` fails if it changes. |
| A4 | Claude models (`model` contains `claude`) take a different route (`openai-to-gemini.js:379-509`): images, documents and thinking are dropped, `tool_choice` is computed and never read, every sampling control but the temperature is dropped, an explicit `temperature: 0` becomes `1` (`:393`), `max_tokens` is raised or clamped by `adjustMaxTokens`, `response_format` becomes system prose (`openai-to-claude.js:120-133`), a system block containing "You are Claude Code" is deleted whole (`:11-18`), and the tool-name map is discarded so a renamed tool is never restored. | One path for every model. Behind a `claude` model **images, documents, audio and any non-text part are refused**, and **`response_format` other than absent, null or `text` is refused**. The sampling controls, tool choice and tool names follow the shared base. `temperature` is `1` only when the request named none; an explicit `0` is kept. System text is never deleted. | A request that asks for enforcement and gets a suggestion, or for an image the model never sees, is a wrong answer. Whether the Claude backend accepts `inlineData` is U, so it is refused rather than guessed. |
| A5 | `executors/antigravity.js:277-279` clamps `maxOutputTokens` to 64000 silently. | `max_tokens` (or `max_completion_tokens`) above 64000 is **refused** on Antigravity, with the limit in the message. The CLI has no limit at the pin and none here. | A silent clamp answers a different request. Whether the backend enforces 64000 is U. |
| A6 | `thoughtSignatureStore.js:14-24`: Antigravity serves two model families and each rejects the other's signature ("Corrupted thought signature"); the pin sends its Gemini blob to Claude models too (`executors/antigravity.js:216`). | The request legs and the shared response leg for the Cloud Code **wire** (`ResponseOptions.Wire`, set by the gateway, not inferred from the payload) derive the signature scope internally as `FamilyScope(scope, model)`: the CLIENT/CONNECTION scope (length-prefixed) followed by `claude`, `gemini` or the model name. An empty scope stays empty, so a request with no tenant stores and finds nothing. The Gemini family gets the placeholder when none is held; **a Claude call with none is left unsigned**. | A cache keyed without a tenant is one client's state served to another; a family without the tenant is the same defect. That Claude accepts an unsigned call, and Gemini the placeholder, is U. |
| A7 | `toolConfig.functionCallingConfig.mode = "VALIDATED"` whenever tools exist (`:369-373`); a client's `tool_choice` is dropped. | A client's choice is kept (6.7 V19: `NONE`, `ANY`, `ANY` with the allowed name). `VALIDATED` is sent only when the request made none or `auto`. | A default must not override what the client said. That `VALIDATED` accepts `allowedFunctionNames` is U. |
| A8 | `safetySettings`: CLI keeps all five `OFF` (`:364-367`); Antigravity sends none (`antigravity.js:287`). | Mirrored. | Gateway behaviour per wire. |
| F1 | The Chat to Gemini leg forwards `top_k` (`openai-to-gemini.js:90-95`); the Antigravity client leg emits it (`antigravity-to-openai.js:32-34`); the Gemini client leg drops it (`gemini-to-openai.js:9-29`). | **No wire of the family reads or emits `top_k`.** A Chat `top_k` is not carried and a Gemini `topK` is not carried; the request is still served. This also changes the Gemini leg of 6.7, which read it. `TestTopKIsNotCarriedByAnyWire`. | OpenAI Chat defines no `top_k`; emitting a field Chat does not define has a client validating against a shape we invented. The Messages leg already does not carry it (6.3). |
| F2 | The Antigravity client leg maps `thinkingBudget` to `reasoning_effort` (`concerns/thinking.js:12-17`); the Gemini and CLI client legs drop `thinkingConfig`. | **Antigravity client leg only**: `thinkingBudget` 1..2048 `low`, ..16384 `medium`, above `high`; `0` is not carried; `-1` (dynamic) and any other negative are **refused**; `includeThoughts: true` is carried (Chat surfaces reasoning whenever it exists), `false` is not carried, any other type and `thinkingLevel` or any unknown key are refused. The CLI and Gemini legs keep 6.7 G4. | Reasoning selection is deferred (#125) for the Gemini wires; the Antigravity client leg carried it at the pin and it maps to a Chat field. |
| F3 | `antigravity-to-openai.js:89-116` lowercases schema types and drops `enumDescriptions` at the schema, each property and `items`. | Mirrored on the Antigravity client leg only; the CLI leg passes parameters through, as the pin's shared function does. A property **named** `enumDescriptions` is kept. | Parity. |
| R1 | `openai-to-antigravity.js:62`: a tool call whose arguments do not parse becomes `{}`. | Absent, empty or blank arguments are `{}`. Anything that is not exactly one JSON object (`null`, a number, a string, an array, malformed, truncated, trailing data, over 4 MiB in total) **fails the stream**. A call with no name fails. More than 128 calls, a negative or non-integer index fails. | A malformed call served as a 200 is a wrong answer. |
| R2 | The accumulated tool call `id` is dropped (`:48,65-70`). | `functionCall.id` is the id the model gave, else `call_msg0_tc<index>_<name>` (the same every time). | Two calls to one tool cannot otherwise be told apart, and the ticket requires tool ids to survive a round trip. |
| R3 | An unknown `finish_reason` becomes `STOP` (`:93`). | `stop`, `tool_calls`, `function_call` are `STOP`; `length` `MAX_TOKENS`; `content_filter` `SAFETY`. **Any other reason, or a non-string, fails the stream.** | An unknown future reason silently becoming success is the class 6.7 G12 stops. |
| R4 | A usage chunk with no `choices` is stored (`:13-17`) but a finish chunk already sent never carries it, so the usual `stream_options` order loses usage entirely. | The finish is held until `[DONE]` or the end of the body; usage seen before, with or after it rides the finish once. No usage chunk means no `usageMetadata`, never zeros. | `usage_observed` exists so absent is not zero. |
| R5 | `candidatesTokenCount = completion_tokens` and `thoughtsTokenCount = reasoning_tokens` (`:108-113`), counting reasoning twice. | `candidatesTokenCount = completion_tokens - reasoning_tokens` (never below 0); the total is the upstream's, else prompt plus completion. | Chat counts reasoning inside completion; Gemini counts it beside (`concerns/usage.js:38`). |
| R6 | `responseId` is `resp_<clock>` when the chunk has no id (`:25`); more than one choice, a choice index other than 0, `delta.images` and `delta.refusal` are ignored. | `resp_unknown`; a second choice, a non-numeric or non-zero index, a `choices` that is not a list and a `usage` that is not an object **fail the stream**; `delta.images` data URIs are sent as `inlineData` and any other image URL fails. `delta.refusal` is typed: absent, `null`, `false` and `""` are silence, a non-empty string or `true` is a refusal and **fails the stream**, and any other type fails as a shape the leg cannot read. A repeated finish chunk with an empty delta keeps the first finish and the parts it held. Content after the finish fails. | Nothing dropped silently; deterministic output. `false` is how a Chat provider says "no refusal", and a `any`-to-`""` comparison read it as one. |
| R8 | The registration (`openai-to-antigravity.js:123`) is a stream function; the pin's buffered path never calls it, so a non-streaming Antigravity client is served the raw Chat body. | `ChatResponseToAntigravity` is the buffered half: it folds the one completion through the stream's own state machine, so parts, tool calls, the finish and the usage cannot disagree with the stream, and it refuses what the stream refuses (no finish, an unknown reason, a refusal, a bad call, an error object). The pin's `usage` in that path counts reasoning twice (R5) and its id embeds the clock (R6); this does not. | A registered Response leg without a body converter hands a non-streaming client another wire's shape. |
| R7 | A broken stream ends however the stream function ends. | An error object the client can read (`{"error": ...}`, the upstream's own when it sent one), never a finish and never `[DONE]` (`utils/stream.js:467`: Gemini-family clients reject the sentinel). A cancelled client gets nothing. | SPEC 9.3. |

#### Request fields, Chat to Gemini CLI and Antigravity (`TestCloudCode*`, `TestAntigravity*`)

| Chat request field | Class and why |
| --- | --- |
| everything in 6.7's table | the same, plus: `top_k` not carried (F1) |
| `max_tokens` / `max_completion_tokens` above 64000 | Antigravity: refused (A5) |
| assistant `reasoning_content` | CLI carried; Antigravity not sent (A2) |
| non-text content parts, `response_format` other than text | refused for `claude` models behind Antigravity (A4) |
| tool with no properties | sent with no `parameters` (A3) |
| the envelope's project, session, request id | options, never invented (C2) |

Verified by test, not by a live call: the envelope shape, the project/session/request-id cross products, the tool-config rule, the family scope (`TestFamilyScopeKeepsTheTenant`, `TestSignaturesAreScopedByTenantAndFamily`), the client-leg envelope cross product, the thinking-config table, the argument matrix and the usage orderings.


### 6.10 Kiro wire hooks (#119, PR 4d)

Oracle: `1c3b9f47837d42ea64992063309064af755a14a3`. Paths below are
under `open-sse/`. The existing Chat legs and operation path are in
`internal/ingress/translate.go`; the direct-leg declaration and EventStream
reader remain the PR 4a/4b implementations described in 6.6 and 6.8.

`internal/providers/wire` registers the connection-aware Kiro hooks.
`FinalizeBody` injects the stored profile after the saver/cache pipeline; the
Target hook requires `providerData.authMethod` and requires
`providerData.profileArn` for every method except `api_key` before constructing
an outbound request. API-key onboarding stores no profile, so that method may
omit it; onboarding itself is unchanged. Neither a profile default nor a
discovery probe supplies a missing value. These two
non-secret settings travel in the immutable connection projection, with no
serving-path SQLite read. The credential still comes from the credential
registry and `RequireCredential` refuses an empty one. Kiro builds Authorization
and SSO bearer headers from the same trimmed token and emits neither for a blank
credential.

| Hook | Pin evidence | Routeweft behavior |
|---|---|---|
| Content-Type | `providers/registry/kiro.js:29-30` | `application/json` |
| Accept | `providers/registry/kiro.js:31` | `application/vnd.amazon.eventstream`, regardless of the client's streaming preference |
| User-Agent | `providers/registry/kiro.js:32` | `AWS-SDK-JS/3.0.0 kiro-ide/1.0.0` |
| X-Amz-User-Agent | `providers/registry/kiro.js:33` | `aws-sdk-js/3.0.0 kiro-ide/1.0.0` |
| Amz-Sdk-Request | `executors/kiro.js:235-240` | `attempt=1; max=3`; this is a wire value, not a new retry budget |
| Amz-Sdk-Invocation-Id | `executors/kiro.js:239` | Fresh UUID v4 for each header construction |
| Authorization | `executors/kiro.js:253-266` | `Bearer <access token>`; Routeweft stores API keys in the connection access-token slot too |
| TokenType | `executors/kiro.js:253-265` | `API_KEY` for stored `authMethod=api_key`, `EXTERNAL_IDP` for `external_idp`, absent otherwise |
| x-amz-sso-bearer | `executors/kiro.js:271-273` | The connection access token, including API-key connections |
| x-amzn-kiro-agent-mode | `executors/kiro.js:274` | `spec` |
| x-amzn-codewhisperer-machine-id | `executors/kiro.js:275` | `kiro-desktop` |
| x-amzn-codewhisperer-profile-arn | `executors/kiro.js:276-279` | The stored connection `profileArn`, omitted when an API-key connection has none |
| X-Amz-Target | `executors/kiro.js:241-245`; `config/kiroConstants.js:26-27` | `AmazonCodeWhispererStreamingService.GenerateAssistantResponse` only for a parsed hostname starting with `codewhisperer.`; no target header for Q, Kiro runtime or a proxy path containing that name |
| POST operation | `providers/registry/kiro.js:19-23`; `executors/kiro.js:330-334` | `generateAssistantResponse` under the configured base; the Target hook overrides a caller's endpoint |
| Body profileArn | `translator/request/openai-to-kiro.js:325-341,419-421`; `translator/request/claude-to-kiro.js:248-255` | Stored value replaces any caller value. For `api_key` only, a missing/blank stored profile removes any client-supplied profile and emits neither body field nor profile header, matching the pin's account-bound omission. Other auth methods still fail closed without a stored profile; no shared OAuth/social default is used. An already matching profile preserves the original bytes |
| Other body fields | `executors/kiro.js:344-346`; `translator/request/openai-to-kiro.js:398-430` | Executor leaves the body alone. Conversation state, inference config and additional model fields belong to the existing request leg; the hook adds no other account field |
| anthropic-beta / interleaved thinking | `config/kiroConstants.js:44,144-158`; `translator/request/claude-to-kiro.js:238-242`; `executors/kiro.js:235-282` | The pin sends **no outbound beta header**. It reads original inbound headers only after body thinking configuration returns no setting. A string beta value containing `interleaved-thinking`, case-insensitively, selects the default budget 16000. Explicit body thinking, including disabled, wins. The inbound Messages behavior is the Messages request leg's (PR 4c, 6.11); no fabricated outbound header is added, and `TestKiroWireRequestConstruction` pins that none is forwarded |
| Endpoint ordering and region | `executors/kiro.js:299-334` | Pin orders Q, other Amazon surfaces, then Kiro runtime, and rewrites Amazon regions from stored `region` (default `us-east-1`). This PR keeps the configured base; automatic surface selection remains a binding prerequisite |
| Endpoint fallback | `executors/kiro.js:336-341`; `config/kiroConstants.js:28` | Pin adds 401/403/404 to endpoint fallback. No new endpoint retry loop in this hook PR; binding must provide the bounded surface fallback |

**Binding remains closed.** *(Kiro still stays in `ingress.unwired` after PR 4c; see 6.11.)* **Blocking items for the unwire:** the three-host order, the region rewrite and the 401/403/404 endpoint fallback in `wire/targets.go` (rows above). This PR does not claim provider readiness, endpoint
failover, the saver view or live-provider qualification. It retains the existing
Chat/direct-leg/EventStream tests, tests every emitted header and auth/host
condition, refuses missing/invalid settings and missing credentials, and checks
that other providers receive none of the Kiro-specific headers. Both credential
headers pass the existing telemetry redactor. Request
construction tests exercise the existing finalization seam without a provider
call. The SQLite integration fixture proves that the stored settings reach the
published snapshot and survive restart; no migration changes.

Local Go 1.22.12 benchmark (`go test ./internal/providers/wire -run '^$'
-bench BenchmarkKiroFinalizeBody -benchmem -count=3`): for the small synthetic
conversation, median finalization was 8.15 microseconds without an account hook
and 10.49 microseconds with the Kiro profile injection; allocation cost was
1000 B / 14 allocations versus 1312 B / 20 allocations. Other checks were
running concurrently, so these timings do not establish production latency.

### 6.11 Kiro Messages legs: where Routeweft differs from the pin (#119, PR 4c)

This PR builds the two Messages legs of the Kiro wire: `request anthropic-messages>kiro` (`translator/request/claude-to-kiro.js:231-366`, registered `:366`) and `response kiro>anthropic-messages` (`translator/response/kiro-to-claude.js:57-285`, registered `:285`). The pin dispatches both directly, with no Chat pivot (`translator/index.js:106-108`, `:208-213`), and so does `hub.Route`. The code is `internal/protocol/kiro` (`messages_request.go`, `messages_response.go`).

**Shape.** The request leg reads Messages blocks into the same alternating turns, tool specifications and thinking intent the Chat leg builds, and then runs the Chat leg's one assembly and its one conversation check (`assembly.payload`, `checkConversation`), so the two legs produce the same Kiro body for the same conversation and refuse the same malformed histories. The response leg reads the EventStream through the Chat leg's machine and writes Messages events directly; the pin's executor makes Chat chunks first (`executors/kiro.js:629-1190`) and `kiro-to-claude.js` turns the chunks into Messages events (`:9-14`), which is the same transform with no chunk built in between. Everything in 6.8 about events, stop reasons, tool buffering, tag splitting, truncation and cancellation therefore holds here unchanged.

**The interleaved-thinking header is read, not written.** The wire-hook brief says PR 4d added it; it did not, and the pin does not send one (6.10). `claude-to-kiro.js:241` hands the *inbound* request headers to `resolveKiroThinkingBudget`, which treats an `anthropic-beta` value containing `interleaved-thinking` (case-insensitive) as the default budget, after the body's own thinking configuration and before the `<thinking_mode>` tag and the model name (`kiroConstants.js:144-169`). So the Messages request leg is the thing that needs it: `RequestOptions.AnthropicBeta` carries it, `ingress` fills `MessagesRequest.AnthropicBeta` from every `Anthropic-Beta` header value, and `resolveBudget` takes it as a parameter that only the Messages leg sets. The condition is tested both ways (`TestMessagesThinkingIntent`: header alone, among other betas, an unrelated beta, explicit `thinking:disabled`, a budget, an effort and a `(none)` suffix over it) and end to end (`TestMessagesBetaHeaderSelectsThinkingAndIsNotForwarded`: the thinking prefix reaches Kiro, no `Anthropic-Beta` does, and the tags in the answer come back as a `thinking` block). The Chat leg never reads it.

#### Request, Messages to Kiro

| Messages content | Disposition | Pin and consequence |
| --- | --- | --- |
| `system` (string or text blocks) | carried, in front of the first user turn after the thinking and agentic prefixes, text blocks joined with a newline | `claude-to-kiro.js:216-226`, `:257-270`. Not wrapped in `<instructions>`: the pin wraps only on the Chat route (`openai-to-kiro.js:165-169`). A non-text system block is **refused**; the pin reads its `text` and drops the rest. The `x-anthropic-billing-header` text Claude Code puts in `system` is sent as the client wrote it; the Messages>Chat leg strips it, the direct pin leg does not |
| `messages[].content` text (string, or blocks) | carried; each text block is a paragraph of its turn (`\n\n`), turns of one role merge | `:86-92`, `:181-207` |
| assistant text blocks of one message | joined with a newline | the pin concatenates them with nothing between (`:135-136`), which runs words together |
| `image` with a base64 source | carried as `images[{format, source.bytes}]`, `format` the media subtype | `:93-96`. Whether Kiro accepts every subtype is not verifiable offline |
| `image` with a `url` or `file` source, an image with no or a non-image `media_type`, invalid base64 | **refused** | the pin silently drops a non-base64 image (`:93`) and defaults a missing type (`:94`) |
| `tool_result` (string or text blocks) | carried as `toolResults`, `is_error` as status error, text blocks joined with a newline | `:97-125` |
| `tool_result` containing an image | **refused** | the pin moves the image to the user turn and writes `(image attached)` (`:102-116`); the image no longer says which result it belongs to |
| `tool_result` content that is not a string or text blocks | **refused** | the pin sends its JSON (`:116-118`) |
| `tool_use` | carried as `toolUses`; `input` must be a JSON object or absent | `:137-143`; the pin takes `|| {}` for anything falsy and passes a non-object through |
| `thinking`, `redacted_thinking` blocks in history | advisory, dropped | `:135-147` reads only text and `tool_use`. Kiro has no reasoning history and the answer does not depend on replaying it; as the Chat leg's echoed `reasoning_content` |
| `document`, `search_result`, `server_tool_use`, `web_search_tool_result`, `mcp_tool_use`, any other block, any unread key on a block | **refused** | the pin ignores every block type it does not list (`:90-126`) |
| `cache_control` on a system, text, image, `tool_use`, `tool_result` or tool, and at the top level | advisory when `{type: ephemeral[, ttl: 5m\|1h]}` | Kiro has no cache field and the answer is the same without the marker (the pin drops it, `:90-126`). Any other marker is **refused**. **Known gap:** prompt-cache anchoring does not reach Kiro, as at the pin |
| `tools` (custom) | carried as `toolSpecification` on the current message; the schema, name, description and collision rows of 6.8 apply unchanged | `kiroConversation.js:92-123`, `:429-431`. A server tool (`web_search_*`, `computer_*`, `bash_*`, `text_editor_*`, `code_execution_*`), `strict: true`, `defer_loading`, `input_examples`, `allowed_callers` and any other tool key are **refused**; the pin ignores the type and passes the tool |
| `tool_choice` | `{type: auto}` carried (identity); `any`, `tool`, `none`, a string, and `disable_parallel_tool_use: true` **refused** | Kiro has neither; the pin ignores the field |
| `max_tokens` | carried to `inferenceConfig.maxTokens`; absent sends nothing | the pin sends `body.max_tokens \|\| 32000` (`:234`); a zero, negative, fractional or non-number value is refused |
| `temperature`, `top_p` | carried, an explicit `0` as `0` | `:235-236`, `:343-348` |
| `thinking`, `output_config.effort`, the `model(level)`, `-thinking` and `-agentic` suffixes, a `<thinking_mode>` tag, the `anthropic-beta` header | carried as in 6.8; the tag is found in a user message, a string `system` or a `system` text block (the pin looks at a string `system` only, `kiroConstants.js:379-380`, so a block list would have sent the tag to Kiro without reading the answer as thinking) | `claude-to-kiro.js:238-243`, `:262-265`. `thinking.display` and any unread `thinking` key are **refused** |
| `output_config.format` (structured output) | **refused** | Kiro has no structured-output mode; the pin never reads the field |
| `top_k`, non-empty `stop_sequences`, `mcp_servers`, `container`, `context_management`, any unlisted field | **refused** | Kiro's request has no equivalent and the first three change what the server does. `stop_sequences: []` is the identity. The pin reads none of them |
| `model`, `stream`, `metadata`, `service_tier` | advisory | `stream` is Kiro's only mode; the rest have no Kiro field |
| message `role` other than user or assistant, a message key other than `role` and `content` | **refused** | |
| conversation ending on an assistant turn (**prefill**), starting on one, a user turn with no text (an image alone) | **refused** | the pin invents turns (`kiroConversation.js:181-186`). Prefill continues a sentence Kiro cannot continue |
| unanswered, orphaned, twice-answered or undeclared-tool calls; ids with no usable characters or over 64 characters | **refused** with the turn named | the pin flattens both sides to text (`kiroConversation.js:261-338`) or, for an unusable id, generates a different id for the call and its result (`translator/concerns/toolCall.js:27-75`), which breaks the pairing |
| tool names | as 6.8: valid names unchanged, the rest rewritten and restored on the response | `claude-to-kiro.js:356-362` |

**Deliberate divergences carried from 6.8.** No session replay cache and no frozen first message (`claude-to-kiro.js:272-294`, `kiroSessionReplay.js:65-130`): `conversationId` is a fresh random UUID per request, so there is no state to scope by client or connection and no way for one client's conversation to be continued by another (`TestMessagesKeepNoStateBetweenRequests`). The `[Context: Current time is …]` line (`:269`) is not sent. Kiro signs no reasoning, so the leg stores no signature and the `thinking` blocks it writes carry none; there is nothing a signature cache could be keyed by, and none is built. `profileArn` is the wire hook's (6.10), not the leg's (`:248-255`, `:338`).

#### Response, Kiro EventStream to Messages

| Event or condition | Disposition |
| --- | --- |
| text, `<thinking>` tags, `reasoningContentEvent`, `codeEvent` | `text` and `thinking` blocks in arrival order, one open at a time (`kiro-to-claude.js:121-158`). The tags are read only when the request injected thinking mode (6.8). No `signature` is written |
| `toolUseEvent` | one `tool_use` block per call, opened with the client's own tool name and closed with its whole `input` in one `input_json_delta` once the body is confirmed whole (`:160-215`); an empty input is `{}`. Failure rules as 6.8 |
| stop reason | `end_turn`, none, `stop_sequence`: `end_turn`; with a call, `tool_use`; `max_tokens` and `model_context_window_exceeded` after output: `max_tokens`. Every other reason is an `error` event. The pin maps all unknown reasons to `end_turn` (`:32-43`) |
| usage | from `concerns.KiroUsage` as in 6.8: `input_tokens` is what Kiro reported, cache counts separate (`cache_read_input_tokens`, `cache_creation_input_tokens` when positive). The pin copies the prompt into `input_tokens` and the cache counts beside it (`:77-95`). No measurement is `{input_tokens: 0, output_tokens: 0}`, the shape Messages requires, not an estimate. `message_start` carries zeros as the pin's does (`:115`) and `message_delta` carries the totals (`:219-223`) |
| message id and model | `msg_` plus the completion id's number; the request's model (`:100-103`) |
| failure (no terminal, truncation, corrupt bytes, content after the stop, an unknown content-bearing event, a `refusal` or other failing reason, upstream `exception`) | an `error` event with the upstream's own message and **no** `message_stop`; blocks already open are not closed. A cancelled client gets nothing more. The pin counts a clean EOF with output as success |
| buffered answer | the same machine, folded into one `message` body: thinking, text and tool blocks in order, `stop_sequence: null`, an answer with no content is one empty text block. The pin has no buffered Kiro path (its executor only streams, `:230-234`); `TestMessagesBufferedMatchesStream` pins that the two agree |

**Not ported from the Chat route:** the Read-tool argument repair that `openai-to-claude.js:14-47` does for other providers. The pin's Kiro leg does not do it either (`kiro-to-claude.js:203-215` sends the buffered arguments as they came), so a Kiro tool input is never edited.

**Routing: Kiro stays in `ingress.unwired`, and why.** The four legs exist and `TranslateChat`, `TranslateResponses` and `TranslateMessages` each have a Kiro arm (Responses goes through Chat, as the pin does, `translator/index.js:116-125`; compact is not translated). The arms are correct work and what makes the later unwire safe, but nothing may route to Kiro yet. **Blocking items for the unwire, all in `internal/providers/wire/targets.go` and the next PR's, not this one's:** (1) the three-host order, Q then other Amazon surfaces then the Kiro runtime (`executors/kiro.js:299-334`); (2) the region rewrite of Amazon hosts from the stored `region`, default `us-east-1` (same lines); (3) the 401/403/404 endpoint fallback (`executors/kiro.js:336-341`, `kiroConstants.js:28`). Without them Routeweft would serve Kiro from only the connection's configured base URL, which can be the wrong host for the account and fail, a silent wrong answer in place of a loud "not yet". 6.10 lists the same three as binding prerequisites. `TestKiroLegsAreDeclaredButKiroStaysUnwired` pins that the legs are declared, Messages uses the direct leg, and `CanTranslate` is false for all three client wires; `TestEveryProviderIsReachableOrTracked` keeps `kiro` tracked as unreachable. The saver view, token estimates, credits and live-provider qualification remain the follow-ups of 6.8.

Validation: sanitized synthetic EventStream fixtures and the pinned leg inventory (`trackedLegGaps` is now empty). Every refusal row has a test that names the field; request and response cross-products are in `messages_request_test.go` and `messages_response_test.go`. No live provider calls were made. No SQLite schema, credential or runtime-dependency change. Security: no new network surface; the inbound `Anthropic-Beta` header is read for one decision and never forwarded or logged.

## 7. Validation, limits and completion

### Reproducible enumeration check

Run from a Routeweft checkout. Set `ORACLE_DIR` to a read-only LiteRouter checkout containing the pinned commit; `/opt/9router` was used for this audit. The provenance worktree path in section 1 is not required.
This checks omissions/duplicates and pins, not behavior; it is deliberately not a replacement for R11.

```bash
ORACLE_DIR=/path/to/literouter node <<'JS'
const assert = require('node:assert/strict');
const fs = require('node:fs');
const { execFileSync } = require('node:child_process');
const pin = '1c3b9f47837d42ea64992063309064af755a14a3';
const oracle = process.env.ORACLE_DIR;
assert.ok(oracle, 'Set ORACLE_DIR to a LiteRouter checkout containing the pinned commit');
const git = (...args) => execFileSync('git', ['-C', oracle, ...args], { encoding: 'utf8' });
const text = fs.readFileSync('docs/PARITY.md', 'utf8');
const files = git('ls-tree', '-r', '--name-only', pin).trim().split('\n');
const expected = files.filter(p => /^src\/app\/api\/.*\/route\.(js|ts)$/.test(p))
  .map(p => p.replace('src/app', '').replace(/\/route\.(js|ts)$/, '')).sort();
const listed = [...text.matchAll(/^\| \d+ \| `(\/api\/[^\`]+)` \|/gm)].map(m => m[1]).sort();
assert.equal(expected.length, 97);
assert.deepEqual(listed, expected);
const registry = git('show', pin + ':open-sse/providers/registry/index.js');
const providers = [...registry.matchAll(/^import \w+ from "\.\/(.+)\.js";/gm)].map(m => m[1]).sort();
const providerSection = text.split('### Hidden providers and non-provider assets')[0].split('| Provider | v1 auth |')[1];
const rows = [...providerSection.matchAll(/^\| `([a-z0-9-]+)` \|/gm)].map(m => m[1]).sort();
assert.equal(providers.length, 81);
assert.deepEqual(rows, providers);
const pageSection = text.split('## 4. Dashboard and supporting pages (16/16)')[1].split('## 5.')[0];
const pages = [...pageSection.matchAll(/^\| `([^`]+page\.js)` \|/gm)]
  .map(m => m[1].replace(/^V\//, 'src/app/(dashboard)/dashboard/')).sort();
assert.deepEqual(pages, files.filter(p => /^src\/app\/(?:.*\/)?page\.js$/.test(p)).sort());
assert.equal(pages.length, 16);
const routeRows = text.split('### Public rewrites')[0].split('\n').filter(line => /^\| \d+ \|/.test(line));
for (const [status, count] of Object.entries({"PRESENT":32,"PARTIAL":39,"MISSING":26})) {
  assert.equal(routeRows.filter(line => line.includes('| ' + status + ' |')).length, count);
}
assert.match(text, /013d26a2014c16e0e4f2a35aa9acf2fa5bfd4e7c/);
console.log('97 routes, 81 active providers, 16 pages: exact pinned inventory, no duplicates; route totals agree.');
JS
```

### Baseline evidence (historical, superseded by R01-R11 progress)

- Existing `compat/fixtures/systemone-nonstreaming.json` is exercised by `I/compat_test.go:TestSystemOneNativePassthroughPreservesTypedEnvelope`; tests assert the full upstream request and response bytes using an injected resolver. CI's `go test ./...` includes it. No System One streaming fixture found; request parser only checks presence of questions, whereas v1 rejects null/array/empty questions. `internal/providers/shared/usage.go:ParseUsage` has no System One case, so the fixture's usage metadata does not prove actual accounting (R04).
- Native protocol fixtures, auth/error/stream cancellation tests, selector/cache/transform tests, SQLite transactions/backup/restore and SSRF/security suites exist. They are useful components, not all-provider or v1/v2 parity certification.
- Current CI: Go tests, race only `./compat/...`, vet/build/benchmark; UI lockfile/typecheck/build; Docker build/entrypoint/image-size. No pinned differential suite, full race gate or browser smoke gate (R11/U11/C09).
- Local Go race attempt: **not run**, executable unavailable (`go: command not found`). No production/oracle runtime calls made. Documentation changes do not alter runtime, hot-path benchmarks, SQLite schema or security implementation.
- Local UI `npm ci`, `npm run typecheck` and `npm run build` passed. `npm audit` reports one existing high-severity transitive `source-map-js` advisory (GHSA-68fv-2mgg-jv7q); no dependency changes made in this inventory-only PR.
- This document's inventory checks and PR CI results are recorded in the PR handoff. Passing CI validates the documentation head against existing checks only; #73 and #74 remain blocked on real parity work and reviewed divergence sign-off.

## CodeBuddy CN and international wire (#115 PR D)

Reference: LiteRouter `1c3b9f47837d42ea64992063309064af755a14a3`,
`open-sse/executors/codebuddy-{cn,intl}.js`, `codebuddy-error.js`,
`providers/registry/codebuddy-{cn,intl}.js`, `utils/reasoningContentInjector.js`
and `services/usage/codebuddy-cn.js`. Verification uses scripted endpoints only.

Both existing providers force Chat streaming at the final wire seam. A client
requesting JSON receives a bounded folded completion, including all choices,
reasoning, refusal, tool calls and any reported usage. A malformed, truncated,
oversized or unrepresentable stream fails before a JSON success is exposed.
Only the enumerated chunk fields `object` and `obfuscation` are ignored and
counted as transport metadata. Other unknown chunk, choice, delta, tool-call or
function fields fail folding. Native streaming carries content through and strips
only empty delta `tool_calls` arrays, after a byte pre-check; long lines beyond
1 MiB pass through unchanged. No request tools are hidden or replaced.

Owner-approved differences from the pin:

- Chat sends Routeweft's own User-Agent and no CodeBuddy identity X-headers.
  Onboarding, token polling, refresh and usage also use Routeweft's User-Agent;
  onboarding and refresh retain their required handshake X-headers.
  If the gateway requires the removed identity headers, CodeBuddy is dropped,
  not softened. This is an accepted outcome, with no impersonation fallback.
- International keeps caller system messages at the front in order, maps
  developer to system, and turns user strings (including empty strings) into
  typed text blocks. It fabricates neither an identity prompt nor a message.
- CN carries system prompts verbatim, including agent prompts over 2000
  characters. Upstream content-filter rejections retain their real status/body.
- Explicit reasoning summaries survive. An absent summary defaults to `auto`
  only for a nonempty string reasoning effort. `none`/`off` omit effort, as in
  the pin; matching also tolerates case and surrounding whitespace.
- The single-space assistant reasoning placeholder is no longer CodeBuddy
  specific: it is generic finalize behaviour (section "Body sanitation at
  finalize"). Existing nonempty reasoning and malformed values remain for the
  gateway to judge.
- Code 6004 (numeric or numeric string), or limit text, sets internal quota
  cooldown accounting without rewriting the client-visible status or body.
  Non-string messages cannot hide 6004. Valid future reset timestamps override
  generic cooldowns; invalid/past timestamps supply no reset. Error relay keeps
  the full body, including when later fallback accounts cannot serve.
- CodeBuddy keeps the translators' default `stream_options.include_usage` on translated
  Messages and Responses streams, so those clients receive real usage.
  Caller stream options, including null, empty and zero values, survive.
  When the gateway omits usage, accounting remains explicitly unobserved.

The usage reader now posts to the pinned billing path
`/v2/billing/meter/get-user-resource` for each domain; the old plugin billing
path had no support in the pinned registry. No provider is added, removed or
marked ready, and no runtime dependency or storage migration is introduced.

Remaining limits: gateway acceptance of Routeweft identity and international
requests without a caller system prompt is unverified offline. Omitted usage
cannot be recovered. Broader thinking-format translation remains #119. These
fixtures establish wire behavior, not live-provider qualification or GA readiness.

## Body sanitation at finalize (#115 PR F)

Oracle: `1c3b9f47`. `DefaultExecutor.transformRequest` runs
`stripUnsupportedParams` and `injectReasoningContent` on the body it is about to
send (`open-sse/executors/default.js:106,109`); the translator only applies
`preserveCacheControl` (`translator/index.js:152`). Routeweft now applies both
rule sets in `wire.FinalizeBody` (`internal/providers/wire/sanitize.go`), before
the provider's own hook, so a body is sanitised whichever leg built it. Before
this PR none of the strip rules existed in Routeweft at any stage, and the
reasoning placeholder existed only inside the two CodeBuddy hooks; nothing was
moved out of a translator, every rule below except `dropClientMetadata`,
`foldResponsesSystem` and the CodeBuddy placeholder is **added**.

| Rule | Pin | Routeweft | Divergence and consequence |
| --- | --- | --- | --- |
| `claude` model: drop `temperature` | `paramSupport.js:10` | finalize, any provider, any wire | none |
| github `gpt-5.4`: drop `temperature` | `paramSupport.js:12` | finalize, github | none |
| github Claude except opus/sonnet 4.6: drop `thinking`, `reasoning_effort` | `paramSupport.js:14` | finalize, github | none |
| cloudflare-ai: flatten content parts to a string | `paramSupport.js:16,78-86` | finalize, Chat bodies | Chat only: on a Messages body flattening would destroy `tool_use`/image blocks. A Messages body to cloudflare-ai is not flattened. |
| xiaomi-mimo `preview`: flatten content | `paramSupport.js:19` | finalize, Chat bodies | as above |
| volcengine-ark `kimi`: cap output at 32768 | `paramSupport.js:26,87-102` | finalize, `max_tokens`/`max_completion_tokens`/`max_output_tokens` | the pin also clamps to the model's catalogued `maxOutput`; Routeweft has no capability catalog, so only the explicit cap applies |
| volcengine-ark `glm-5`: clamp to model max output | `paramSupport.js:20` | **not carried** | needs the same catalog; a request above the model's ceiling reaches Ark unclamped and is answered 400 there |
| groq, mistral, cerebras: drop `reasoning_content`, `reasoning`, `reasoning_details` from assistant messages | `paramSupport.js:33-35,69-76` | finalize, Chat bodies | Chat only (Messages and Responses carry no such field) |
| opencode-go `deepseek*`: drop `response_format` | `paramSupport.js:44` | finalize | none |
| reasoning placeholder `" "` on assistant messages: registry providers deepseek, minimax, minimax-cn, opencode (all turns); `kimi-*` models (tool-call turns); `deepseek` models (all turns) | `reasoningContentInjector.js:6-43,70-76` | finalize, Chat bodies | Chat only: a Messages body would be sent a field its schema rejects. A non-string `reasoning_content` is left for the gateway where the pin overwrites it. A mistral/groq/cerebras request for a deepseek model is stripped then re-injected, in the pin's order. |
| `deepseek-v4-pro-max` / `-none` aliases | `reasoningContentInjector.js:17-27,45-68` | finalize, deepseek provider, Chat bodies | an `extra_body` or `extra_body.thinking` that is present but not an object leaves the body as received (the pin spreads it into garbage) |
| `dropClientMetadata`, `foldResponsesSystem` | `default.js:97-104` | finalize, already (earlier PRs) | see their own rows |
| `preserveCacheControl` | `translator/index.js:152` | translator (`openai.FilterChatBody`); **not** repeated at finalize | none; a test fails if finalize touches `cache_control` |

Which providers: the pin reaches only DefaultExecutor-derived executors.
Routeweft therefore applies neither set to antigravity, azure (its
`transformRequest` returns the body, `azure.js:54`), codex, commandcode, cursor,
devin-cli, gemini-cli, grok-cli, grok-web, iflow, kiro, mimo-free,
perplexity-web, qoder, trae, vertex, vertex-partner, windsurf or zed; github gets
the strip rules only (`github.js:109`) and opencode the injection only
(`opencode.js:464`).

Hot path: `FinalizeBody` now parses the body for every provider that takes either
set (all but the list above), where before only providers with a hook did. It is
one top-level decode and a re-encode only when a rule fires; no benchmark was run.
