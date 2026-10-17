# LiteRouter v1 parity inventory and divergence ledger

Status: **Phase 1 inventory; not a parity certification or cutover approval.**
Parent: #73. Audit date: 2026-10-17.

## 1. Scope, pins and interpretation

- Oracle: `raufimusaddiq/literouter@1c3b9f47837d42ea64992063309064af755a14a3`.
- Routeweft: `013d26a2014c16e0e4f2a35aa9acf2fa5bfd4e7c` (the common `main` / `origin/main` base).
- Worktree: `/home/ubuntu/routeweft-wt/s5-parity-73`; branch: `sprint/5-parity-73`.
- The oracle checkout actually reports `2df83ab99d158e3beb53eb88a089fc28e6dae948`, with existing untracked intake files. All oracle evidence below is read using `git show 1c3b9f47:<path>` / `git ls-tree`, never the newer working files. No oracle checkout, database, credentials, runtime or deployment is modified/accessed.
- Read order: AGENTS, PRD, SPEC, BDR, UI_STYLE, RUNBOOK, SPRINT_PLAN, then #73. This PR changes documentation only. Existing product decisions remain binding; conflict with #73 is surfaced, not silently resolved by implementation.
- Scope reaches API handlers/auth/rewrites, provider registry/executors/OAuth/discovery/quota, pages/components, settings/schema/files/environment, CI and operational contracts. No implementation fixes, deployment, traffic shift or production secret access.

| Inventory | PRESENT | PARTIAL | MISSING | Total |
| --- | ---: | ---: | ---: | ---: |
| API route files | 6 | 48 | 43 | 97 |
| Active providers | 0 | 80 | 1 | 81 |
| Dashboard page files | 0 | 13 | 0 | 13 |
| Supporting root/login/callback page files | 0 | 2 | 1 | 3 |

**PRESENT** means the scoped logical capability has an identifiable implementation; it does not mean differential v1/v2 parity has been demonstrated. **PARTIAL** means some relevant behavior exists but a named gap or unverified integration remains. **MISSING** means no callable equivalent of the capability was found. Missing approved exclusions remain listed. A renamed admin endpoint is not itself a gap (PRD 16, SPEC 22, BDR-014).

No active provider is certified PRESENT: the actual app constructs ingress without provider/account resolution, translators or saver/cache pipeline hooks. Existing tests inject those dependencies. SPRINT_PLAN Sprint 5 completion PRs 5.1-5.5 explicitly acknowledge this. Green existing CI is not the green differential parity suite required by #73.

### R01 implementation progress (after the inventory baseline)

The inventory tables describe the pinned audit baseline above, not subsequent
implementation status. #75 task R01, first review boundary (Sprint 5.1):

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
| 01 | `/api/auth/login` | POST | PUB | {password} => success + auth_token cookie; 401/429 | PARTIAL | /admin/v1/auth/login; A/api.go, A/throttle.go | C03,C07: username/session design differs; throttle omits Retry-After. |
| 02 | `/api/auth/logout` | POST | PUB | no body => {success}; clear cookie | PRESENT | /admin/v1/auth/logout; A/api.go | D01,D04: Routeweft session cookie, same logout capability. |
| 03 | `/api/auth/reset-password` | POST | L | no body => {success}; delete configured password | MISSING | No reset action; A/password.go only rotates current password | C03: local emergency reset absent; do not copy bypass without approval. |
| 04 | `/api/auth/status` | GET | PUB | => {requireLogin,hasPassword,authenticated} | PARTIAL | /admin/v1/auth/session; A/api.go | C03: session identity available, public bootstrap/login-required shape absent. |
| 05 | `/api/combos/[id]` | GET, PUT, DELETE | D | id; PUT Combo fields => Combo; DELETE => success | PARTIAL | /admin/v1/combos/{id}; A/combos.go, A/read_models.go | R01,C04,C06: CRUD/order exists; runtime path and v1 strategy/Fusion tuning incomplete. |
| 06 | `/api/combos` | GET, POST | D | POST {name,models,kind} => Combo (201); GET {combos} | PARTIAL | /admin/v1/combos; A/combos.go, A/read_models.go | R01,C04,C06: fallback/RR/sticky/Fusion app parity unproven. |
| 07 | `/api/console-logs` | GET, DELETE | D | GET => {success,logs}; DELETE clears buffer | PARTIAL | GET /admin/v1/logs; A/logs_handler.go, A/logs.go | R10: bounded/redacted/filterable GET exists; DELETE missing. |
| 08 | `/api/console-logs/stream` | GET | D | => SSE console-log updates | MISSING | /admin/v1/events does not carry console records; A/events.go | R10,U08: no console-log SSE equivalent. |
| 09 | `/api/headroom/extras` | GET, POST, DELETE | D | ?log=1 => install log; GET extras; POST/DELETE extra spec => result | MISSING | Settings only; A/read_models.go | R09: extras/install-log control absent. |
| 10 | `/api/headroom/proxy/[...path]` | GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS | L | path/query/body passthrough => proxied bytes; dashboard HTML rewritten | MISSING | No proxy control route | R09: managed proxy/dashboard access absent; local-only must remain protected. |
| 11 | `/api/headroom/restart` | POST | D | no body => {success,...process status}; external mode 400 | MISSING | No lifecycle route | R09: restart ownership/external-mode behavior absent. |
| 12 | `/api/headroom/start` | POST | L | no body => {success,...process status}; external mode 400 | MISSING | No lifecycle route | R09: managed start absent. |
| 13 | `/api/headroom/status` | GET | D | => {...health,url,managedPid} | MISSING | GET /admin/v1/token-saver reports settings only; A/read_models.go | R09: not a live helper-health/process read model. |
| 14 | `/api/headroom/stop` | POST | L | no body => stopped/process status | MISSING | No lifecycle route | R09: managed stop absent. |
| 15 | `/api/health` | GET, OPTIONS | PUB | GET => {ok:true}; OPTIONS CORS | PRESENT | /health/live, /health/ready; internal/app/app.go | D02: split liveness/readiness, plain-text ok; no upstream dependency. |
| 16 | `/api/keys/[id]` | GET, PUT, DELETE | S | GET key; PUT {isActive,access:{restricted,allow}} => key; DELETE success | PARTIAL | /admin/v1/keys list, PATCH/DELETE /keys/{id}; A/keys.go | C02: lifecycle exists; model/Combo access policy missing. D03 removes secret re-read. |
| 17 | `/api/keys` | GET, POST | S | GET {keys}; POST {name} => {key,name,id,machineId} (201) | PARTIAL | /admin/v1/keys; A/keys.go, A/read_models.go | C02: policies missing; D03 returns secret once, stores digest, omits machine identity. |
| 18 | `/api/locale` | POST | PUB | POST {locale} => {success,locale} + locale cookie | MISSING | No locale endpoint/UI catalog | U10: locale support not explicitly excluded by PRD. |
| 19 | `/api/models/alias` | GET, PUT, DELETE | D | GET {aliases}; PUT {model,alias}; DELETE ?alias => success | PRESENT | GET/POST /admin/v1/aliases, DELETE /aliases/{alias}; A/providers.go | D01: logical alias CRUD retained with split provider/model fields. |
| 20 | `/api/models/availability` | GET, POST | D | GET availability state; POST {action,provider,model} => {ok} | MISSING | No availability reset action | R06: no equivalent operator availability clear/reset read-write workflow. |
| 21 | `/api/models/catalog-sync` | GET, POST | D | GET {sync state,catalog}; POST => {success,result} or 503 | MISSING | Per-connection /discover only; A/providers.go | R06: catalog-wide sync status/action not represented. |
| 22 | `/api/models/custom` | GET, POST, DELETE | D | GET {models}; POST {providerAlias,id,type,name,caps}; DELETE identifiers | PARTIAL | GET/POST/DELETE /admin/v1/models; A/providers.go, A/read_models.go | R06,C06: manual add exists; custom-model deletion (DELETE /admin/v1/models, U04) is refused with 409 while a Combo or capacity-adapter pool references the model, and otherwise also drops its aliases, pricing override and disabled marker; kind/capability persistence not fully mapped. |
| 23 | `/api/models/disabled` | GET, POST, DELETE | D | GET ?providerAlias; POST {providerAlias,ids}; DELETE providerAlias/id | PRESENT | PATCH /admin/v1/models/disabled, GET /models; A/providers.go | D01: single-model {providerId,modelId,disabled} plus disabled flag; bulk via repeated mutations. |
| 24 | `/api/models` | GET, PUT | D | GET {models}; PUT {model,alias} => success | PRESENT | GET /admin/v1/models, POST /aliases; A/read_models.go, A/providers.go | D01: model read/alias mutation represented; readiness/listing gaps separately tracked. |
| 25 | `/api/models/test` | POST | D | {model,kind} => {ok,status,...probe}; own timeout 504/network 502 | PARTIAL | POST /admin/v1/connections/{id}/test-models; A/providers.go | R07: v1 first-token probe default 200000 ms vs non-streaming shared 20000 ms; requested-model attribution needs fixtures. |
| 26 | `/api/oauth/[provider]/[action]` | GET, POST | D | GET authorize/device/proxy/poll; POST exchange/poll => flow/status/connection | MISSING | Provider PKCE/header helpers only; no admin OAuth routes | P04: no full admin OAuth action dispatcher; hidden providers remain excluded D06. |
| 27 | `/api/oauth/codex/bulk-import` | POST | D | token batch => {success,failed,results} | MISSING | Generic /admin/v1/connections POST is not bulk import | P04: validation/dedup/bulk results missing. |
| 28 | `/api/oauth/codex/import-token` | POST | D | {accessToken,name} => success + connection summary | MISSING | No token-import action | P04: validated identity/import missing. |
| 29 | `/api/oauth/cursor/auto-import` | GET | L + S | => {found,...IDE discovery/import information} | MISSING | No host import action | P04: host discovery must be local and always authenticated. |
| 30 | `/api/oauth/cursor/import` | POST, GET | D | POST {accessToken,machineId} => success; GET instructions | MISSING | No Cursor import action | P04: machine identity/session validation missing. |
| 31 | `/api/oauth/gitlab/pat` | POST | D | PAT payload => {success}; verification errors 401 | MISSING | No PAT validation action | P04: GitLab token exchange/identity missing. |
| 32 | `/api/oauth/grok-cli/bulk-import` | POST | D | token batch => aggregate success/failed/results | MISSING | No bulk import action | P04: Grok identity/merge/import missing. |
| 33 | `/api/oauth/iflow/cookie` | POST | D | {cookie} containing BXAuth => account/API-key result | MISSING | No cookie-to-key action | P04: cookie validation/key provisioning absent. |
| 34 | `/api/oauth/kiro/api-key` | POST | D | {apiKey,region} => validated credential/connection result | MISSING | No Kiro action | P04: validation/region wiring absent. |
| 35 | `/api/oauth/kiro/auto-import` | GET | L + S | => {found,...CLI token/cache information} | MISSING | No host import action | P04: local host credential discovery absent. |
| 36 | `/api/oauth/kiro/import-cli-proxy` | POST | D | CLI-proxy credential payload => connection result | MISSING | No CLI-proxy import action | P04: payload parsing/identity checks absent. |
| 37 | `/api/oauth/kiro/import` | POST | D | {refreshToken,clientId,clientSecret,region,authMethod,profileArn} => result | MISSING | No Kiro token action | P04: refresh/profile identity not modeled as workflow. |
| 38 | `/api/oauth/kiro/social-authorize` | GET | D | ?provider=google/github => authorization URL + verifier/state | MISSING | No social authorize action | P04: social flow absent. |
| 39 | `/api/oauth/kiro/social-exchange` | POST | D | {code,codeVerifier,provider} => connection result | MISSING | No social exchange action | P04: exchange/callback validation absent. |
| 40 | `/api/oauth/xiaomi-mimo/api-key` | POST | D | {apiKey,uid,baseUrl,mimoPassToken,mimoUserId,mimoCUserId} => result | MISSING | No Xiaomi key action | P04: provider-specific identity/import absent. |
| 41 | `/api/oauth/xiaomi-mimo/auto-import` | GET | D | => {found,...browser/session discovery status} | MISSING | No host import action | P04: auto-import absent; approved host-access policy required. |
| 42 | `/api/pricing` | GET, PATCH, DELETE | D | GET map; PATCH nested provider/model prices; DELETE ?provider&model resets | PARTIAL | GET/POST/DELETE /admin/v1/pricing; A/providers.go | R06,C06: override CRUD exists; default-price catalog/reset behavior not equivalent. GET_DEFAULTS is not an exported HTTP verb. |
| 43 | `/api/provider-nodes/[id]` | PUT, DELETE | D | PUT {name,prefix,apiType,baseUrl} => {node}; DELETE success | PARTIAL | PATCH/DELETE /admin/v1/provider-nodes/{id}; A/providers.go | D07 unifies compatible nodes; R04: custom System One node workflow absent. |
| 44 | `/api/provider-nodes` | GET, POST | D | GET {nodes}; POST {name,prefix,apiType,baseUrl,type} => node (201) | PARTIAL | GET/POST /admin/v1/provider-nodes; A/providers.go | D07: Generic multi-transport nodes; R04: typed System One custom-node parity absent. |
| 45 | `/api/provider-nodes/routes` | GET | D | ?id => {id,routes} | PARTIAL | Node transports in /admin/v1/provider-nodes; A/read_models.go | R06: metadata present; no resolved per-node route read model. |
| 46 | `/api/provider-nodes/validate` | POST | D | {baseUrl,apiKey,type,modelId} => {valid,error?,method?} | PARTIAL | /connections/{id}/test; generic.Validate; A/providers.go | R06: save-time URL validation exists; connection test is discovery-only, lacks inference fallback/manual model probe. |
| 47 | `/api/providers/[id]/models` | GET | D | id => provider-specific authenticated model list | PARTIAL | POST /admin/v1/connections/{id}/discover; A/providers.go | P03: shared discovery does not implement every provider listing/token exchange. |
| 48 | `/api/providers/[id]` | GET, PUT, DELETE | D | GET connection; PUT name/priority/auth/proxy/providerSpecificData; DELETE success | PARTIAL | GET /connections list, PATCH/DELETE /connections/{id}; A/providers.go | C06,P04: opaque provider-specific fields/import state not retained fully. |
| 49 | `/api/providers/[id]/test-models` | POST | D | id => {provider,connectionId,results} for configured models | PARTIAL | POST /admin/v1/connections/{id}/test-models; A/providers.go | R07,P02: max 20 explicit IDs; generic native probes, no specialized/first-token equivalence. |
| 50 | `/api/providers/[id]/test` | POST | D | id => validation/latency/error/status result | PARTIAL | POST /admin/v1/connections/{id}/test; A/providers.go | R06: currently model discovery, not full provider-specific auth/inference probe. |
| 51 | `/api/providers/client` | GET | D | ?provider,accountStatus,sort,page,pageSize => paginated safe account view | PARTIAL | /admin/v1/providers and /connections; A/read_models.go | R10,U04: no equivalent provider/status filtering/joined page semantics. |
| 52 | `/api/providers/kilo/free-models` | GET | D | => {models,cached,warning?}; preserve cached data on fetch failure | MISSING | No free-model control action | R06,P03: Kilo free filtering/cache behavior absent. |
| 53 | `/api/providers` | GET, POST | D | GET {connections}; POST {provider,apiKey,name,...config} => connection (201) | PARTIAL | /admin/v1/providers, /connections; A/providers.go | P01,P04,C06: catalog+connection CRUD exists, missing muse/auth workflows/provider state. |
| 54 | `/api/providers/suggested-models` | GET | D | ?url,type => {data} filtered upstream suggestions | MISSING | Shared /discover is not suggested-model filter | R06,P03: named filters/cache/failure behavior missing. |
| 55 | `/api/providers/test-batch` | POST | D | {mode,providerId} => aggregate batch results | MISSING | Per-connection test-models only | R06: no across-connection/provider batch equivalent. |
| 56 | `/api/providers/validate` | POST | D | {provider,apiKey,providerSpecificData} => {valid,error?} | PARTIAL | /connections/{id}/test; A/providers.go | R06,P02: pre-save specialized provider validation absent. |
| 57 | `/api/proxy-pools/[id]` | GET, PUT, DELETE | D | GET pool; PUT pool fields; DELETE => success / 409 in use | PARTIAL | GET /admin/v1/proxy-pools; internal/runtime/proxy_update.go | R08: storage/rotation exists; HTTP create/update/delete absent. |
| 58 | `/api/proxy-pools/[id]/test` | POST | D | pool id => connectivity/status result | MISSING | internal/proxy test primitive, no control route | R08: operator API missing. |
| 59 | `/api/proxy-pools/netlify-deploy` | POST | D | Netlify deployment credentials/config => {proxyPool,deployUrl} (201) | MISSING | No provisioning action | R08: pending product decision; cloud-sync exclusion does not itself exclude outbound relay provisioning. |
| 60 | `/api/proxy-pools` | GET, POST | D | ?isActive,includeUsage => {proxyPools}; POST pool => pool (201) | PARTIAL | GET /admin/v1/proxy-pools; A/read_models.go | R08: read-only HTTP resource; create/filter/usage parity incomplete. |
| 61 | `/api/pxpipe/health` | POST, GET | D | => {healthy,checks,...} | MISSING | internal/transforms/pxpipe health helper only | R09: no admin control action. |
| 62 | `/api/pxpipe/install` | POST | D | => installation info + health | MISSING | internal/transforms/pxpipe has HTTP transform/health/log/stats helpers only | R09: managed install/auto-install control absent. |
| 63 | `/api/pxpipe/logs` | GET | D | ?limit<=500 => logs result | MISSING | No PXPIPE log endpoint | R09: diagnostic log control absent. |
| 64 | `/api/pxpipe/restart` | POST | D | => status after restart | MISSING | No lifecycle route | R09: managed restart absent. |
| 65 | `/api/pxpipe/start` | POST | D | => status; 409 NOT_INSTALLED if policy forbids install | MISSING | No lifecycle route | R09: start/install policy workflow absent. |
| 66 | `/api/pxpipe/stats` | GET | D | ?limit<=500 => aggregate/recent transform stats | MISSING | No stats endpoint | R09: live diagnostic stats absent. |
| 67 | `/api/pxpipe/status` | GET | D | => helper installation/load/runtime state | MISSING | /admin/v1/token-saver settings only | R09: no process/helper state read model. |
| 68 | `/api/pxpipe/stop` | POST | D | => {stopped,...status} | MISSING | No lifecycle route | R09: stop absent. |
| 69 | `/api/settings/database` | GET, POST | DB | GET export JSON; POST {password,...export} => success | PARTIAL | /admin/v1/backup, /backup/restore[/check]; A/backup.go | D08: validated Routeweft ZIP intentional; C01: v1 import/recreation policy and tests missing. |
| 70 | `/api/settings/proxy-test` | POST | D | proxy config => {ok,...connectivity,error} | MISSING | No control proxy-test route | R08: outbound connectivity action absent. |
| 71 | `/api/settings/require-login` | GET | PUB | => {requireLogin}, defaults true | PARTIAL | /admin/v1/auth/session; A/api.go | C03: requireLogin stored true; no public setting read/change behavior. |
| 72 | `/api/settings` | GET, PATCH | D | GET safe merged settings; PATCH settings/password fields => safe settings | PARTIAL | GET/PATCH /admin/v1/settings; A/settings.go | D01,D04; C03-C06: allowlist string settings, missing/changed semantics and live effects. |
| 73 | `/api/tags` | OPTIONS, GET | D | => Ollama {models} manifest + CORS | MISSING | No /api/tags handler in internal/ingress/ingress.go | R05: retained chat-client discovery compatibility missing. |
| 74 | `/api/usage/[connectionId]/codex-reset-credits` | GET, POST | D | GET reset-credit balance; POST consume => result | PARTIAL | internal/providers/codex/usage.go, internal/quota; no action route | P05,R10: provider helpers exist, not operator HTTP workflow. |
| 75 | `/api/usage/[connectionId]` | GET | D | ?force=1 => provider usage/limits/reset or unavailable | PARTIAL | GET /admin/v1/quota, POST /quota/refresh; A/quota_refresh.go | P05,R10: only Codex/Claude observers; no per-account forced/action parity. |
| 76 | `/api/usage/chart` | GET | D | ?period => chart series | PARTIAL | GET /admin/v1/usage/summary; A/usage_summary.go | R10: daily series exists; v1 periods/cost/metric dimensions incomplete. |
| 77 | `/api/usage/history` | GET | D | => accumulated usage stats/history | PARTIAL | GET /admin/v1/usage, /usage/summary; A/usage_summary.go | R10: historical retention/aggregate shape/cost parity not proven. |
| 78 | `/api/usage/logs` | GET | D | => recent log list | PARTIAL | GET /admin/v1/usage; A/read_models.go | R10: events present; attribution/cost/detail correlation gaps. |
| 79 | `/api/usage/providers` | GET | D | => {providers:[{id,name}]} distinct from request details, names resolved via nodes/registry | PARTIAL | /admin/v1/providers + /usage/summary; A/read_models.go | R10: catalog/breakdown is not the distinct request-detail provider filter. Dashboard topology/activity is a separate retained workflow. |
| 80 | `/api/usage/request-details` | GET | D | ?page,pageSize,provider,model,connectionId,status,startDate,endDate => redacted details | PARTIAL | GET /admin/v1/requests; A/read_models.go | R10: bounded read exists; no normal app detail producer/filter equivalence. |
| 81 | `/api/usage/request-logs` | GET | D | => request-log list | PARTIAL | GET /admin/v1/usage; A/read_models.go | R10: detailed request/transform/fallback/cost context incomplete. |
| 82 | `/api/usage/stats` | GET | D | ?period => totals/breakdowns | PARTIAL | GET /admin/v1/usage/summary; A/usage_summary.go | R10: token/cache totals exist; cost/TTFT/API-key attribution incomplete in app. |
| 83 | `/api/usage/stream` | GET | D | => SSE activeRequests/recentRequests/errorProvider updates | PARTIAL | GET /admin/v1/events; A/events.go | D09 single SSE intentional; R10,U08: request.completed/config.updated only, UI not subscribed. |
| 84 | `/api/v1/api/chat` | OPTIONS, POST | K | Ollama {model,messages,stream} => message JSON / NDJSON | PARTIAL | /v1/api/chat; I/compat.go | R01,R02,R03,R11: native handler/fixtures exist; actual app unconfigured. |
| 85 | `/api/v1/chat/completions` | OPTIONS, POST | K | Chat {model,messages,tools,stream,...} => completion / SSE | PARTIAL | /v1/chat/completions; I/ingress.go | R01-R03,R11,C02: handlers/fixtures, no production resolver/translation/pipeline or key policy. |
| 86 | `/api/v1/messages/count_tokens` | OPTIONS, POST | K | Messages input => {input_tokens} | PARTIAL | /v1/messages/count_tokens; I/messages_count_tokens.go | R01,R11,C02: estimate/native handling requires pinned behavioral/auth contract comparison. |
| 87 | `/api/v1/messages` | OPTIONS, POST | K | Messages {model,messages,system,tools,...} => message / SSE | PARTIAL | /v1/messages; I/ingress.go | R01-R03,R11,C02: native fixture paths only; app and final-body cache gaps. |
| 88 | `/api/v1/models/[...model]` | OPTIONS, GET | K | provider/model => model; ?kind => kind-filtered list | PARTIAL | /v1/models/{provider}/{model...}; I/ingress.go | R05,C02: model lookup exists; kind query semantics/access filtering missing. |
| 89 | `/api/v1/models/[kind]` | OPTIONS, GET | K | image/tts/stt/embedding/image-to-text/web => {object:list,data} | MISSING | No single-segment kind handler | D05 excludes standalone media; R05: explicitly decide retained image-to-text/web compatibility instead of silently dropping entire route. |
| 90 | `/api/v1/models/info` | OPTIONS, GET | K | ?id,kind => model capability/context information | PARTIAL | /v1/models/info; I/ingress.go | R05,C02: Routeweft returns catalog info without v1 id/kind/access contract. |
| 91 | `/api/v1/models` | OPTIONS, GET | K | => {object:list,data} including aliases/Combos, access-filtered | PARTIAL | /v1/models; I/ingress.go | C02,P03,R11: basic discovery exists; provider drift/access filtering/matrix not complete. |
| 92 | `/api/v1/responses/compact` | OPTIONS, POST | K | Responses compact input => compact output | PARTIAL | /v1/responses/compact; I/ingress.go | R01-R03,R11,C02: handler exists, configured app/native/translated parity missing. |
| 93 | `/api/v1/responses` | OPTIONS, POST | K | Responses {model,input,tools,stream,...} => response / event SSE | PARTIAL | /v1/responses; I/ingress.go | R01-R03,R11,C02: handler/fixtures not live app parity. |
| 94 | `/api/v1` | GET, OPTIONS | K | => public API index | PRESENT | GET /v1; I/ingress.go | D01: index handler exists; aliases/auth negative cases require R05/R11. |
| 95 | `/api/v1/systemone` | OPTIONS, POST | K | {model,state,questions,...} => typed answers + usage / upstream stream | PARTIAL | /v1/systemone; I/compat.go, internal/protocol/systemone/request.go | R01,R04,C02: questions validation weaker; streaming fixture/custom nodes/secondary transports absent. |
| 96 | `/api/v1beta/models/[...path]` | OPTIONS, POST | K | model:generateContent / streamGenerateContent; contents => candidates / SSE | PARTIAL | /v1beta/models/{rest...}; I/compat.go | R01-R03,R11,C02: native fixtures, not app translation/provider/access parity. |
| 97 | `/api/v1beta/models` | OPTIONS, GET | K | => {models:[{name,supportedGenerationMethods,...}]} | PARTIAL | /v1beta/models; I/compat.go | P03,C02,R11: snapshot list not provider-authenticated/access-filtered parity. |

### Public rewrites and compatibility aliases (outside the 97-file count)

Oracle evidence: `next.config.mjs`; Routeweft evidence: `I/ingress.go:Attach`.

| v1 client path / rule | Status | Routeweft equivalent / remaining work |
| --- | --- | --- |
| `/v1/:path*` and `/v1` map to `/api/v1` | PARTIAL | Direct retained ingress handlers exist. R01-R04/R11 apply. Legacy `/api/v1/*` URL exposure itself needs an explicit compatibility decision, not Next module replication (R05). |
| `/v1beta/:path*` and `/v1beta` | PARTIAL | Models + generation handlers exist; root `/v1beta` target has no route file in the pinned source. Do not invent a root contract. |
| `/responses` | PARTIAL | Registered directly, subject to R01-R03/R11. |
| `/codex/:path*` | PARTIAL | Registered directly (including `/codex`); subject to R01-R03/R11. |
| `/v1/v1/:path*` and `/v1/v1` | PARTIAL | Only Responses/compact/Messages/count_tokens aliases registered. Chat, index, models, Ollama/System One aliases not covered (R05; PRD-API-003). |
| Ollama `/api/tags` | MISSING | Explicit route 73 above; retain chat-client discovery (R05), not a media feature. |
| Browser CORS/body/h2c | PARTIAL | 128 MiB limit and allowlisted CORS implemented; arbitrary-origin CORS differs intentionally D11. No explicit h2c serving setup found in `internal/app/app.go`; validate upgrade-compatible clients (R11/C09; PRD-API-005). |
| Client saver bypass headers | MISSING | v1 recognizes `x-9router-token-saver: off` and `x-literouter-token-saver: off` in `open-sse/config/runtimeConfig.js`. Routeweft transform Bypass field exists but ingress does not extract these headers (R03). |

## 3. Provider inventory (81 active + 3 hidden)

Oracle registry: `open-sse/providers/registry/index.js`. Exactly **81 active imports**, including new `muse`; three commented-out providers remain hidden. Routeweft `internal/providers/registry/builtins.go` declares **80 identities**. PRD 6 / PROVIDER_BASELINE still freeze 80 at an older snapshot; `muse` requires drift review under PRD 21 / SPEC 37, not silent exclusion.

Every provider row below has its own pinned source `open-sse/providers/registry/<id>.js`, and its Routeweft declaration (except muse) in `internal/providers/registry/builtins.go`. v1 executor column maps every registered specialized factory; `default` means `open-sse/executors/default.js`, not absence of provider behavior. Native protocol codes: C=Chat, R=Responses, M=Messages, G=Gemini, O=Ollama, SO=System One. A specialized executor may convert/envelope a wire whose registry default says C.

**Evidence / dimension notation:** P=PARTIAL, M=MISSING, R=PRESENT, -=not advertised as a quota product by the v1 registry. Dimensions are **L/A/Q/S/N/E/T** = authenticated listing, auth/import/refresh workflow, quota/limits, streaming, non-streaming, error mapping, token accounting. These are source-coverage assessments, not passed live-provider tests. Every P remains a real gap until pinned fixtures/app-level proof exists (P08).

- Shared listing: `internal/providers/discovery/{discovery,lifecycle}.go`, `A/providers.go:fetchModels`; not every provider's token exchange, filter or format.
- Shared storage/auth: `internal/credentials/{store,registry,resolver,seal}.go`; API-key/cookie/token storage does not implement OAuth onboarding. Codex/Claude PKCE helpers exist; app passes nil refresh callback to credential registry.
- Shared S/N/E/T: `I/{ingress,compat}.go`, `internal/providers/shared/{errors,usage}.go`, native protocol parsers and deterministic fixtures. Specialized modules are mostly declarations, not executable codecs.
- Q: only Codex and Claude usage observers are wired in `internal/app/app.go:initializeQuota`. Other v1-advertised usage/limits remain M; no v1 quota claim is fabricated for providers marked "-".
- Seed column is literal inline v1 model IDs / Routeweft StaticModels / v1 IDs absent from those seeds. Counts are not live catalog counts; 0 is not "no model support". Dynamic/passthrough can preserve unknown IDs, but offline pickers/capabilities still need P03.

| Provider | v1 auth | v1 wire / executor | v1 catalog | Seeds v1/RW/absent | L/A/Q/S/N/E/T | Status | Explicit shortfall |
| --- | --- | --- | --- | --- | --- | --- | --- |
| `alicode` | API key | C / default | seed/provider-specific | 8/0/8 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `alicode-intl` | API key | C / default | seed/provider-specific | 7/0/7 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `alims-intl` | API key | C / default | seed/provider-specific | 7/0/7 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `alitp-intl` | API key | C / default | seed/provider-specific | 6/0/6 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `anthropic` | API key | M / default | seed/provider-specific | 7/3/4 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `antigravity` | OAuth | antigravity / antigravity | seed/provider-specific | 20/4/19 | P/M/M/M/M/M/M | PARTIAL | P02-P05: specialized wire/auth/refresh/listing/limits and stream codecs absent. |
| `api-airforce` | apikey | C / default | dynamic:airforce-free; passthrough | 3/0/3 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `azure` | API key | C / azure | seed/provider-specific | 0/0/0 | P/P/-/P/P/P/P | PARTIAL | P02/C06: deployment/API-version/org/endpoint provider data and specialized URL rules. |
| `baidu` | apikey | C / default | seed/provider-specific | 7/0/7 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `bazaarlink` | apikey | C / default | seed/provider-specific | 24/0/24 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `blackbox` | API key | C / default | seed/provider-specific | 10/0/10 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `bluesminds` | apikey | C / default | seed/provider-specific | 14/0/14 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `byteplus` | API key | C / default | seed/provider-specific | 7/0/7 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `cerebras` | API key | C / default | seed/provider-specific | 7/0/7 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `chutes` | API key | C / default | seed/provider-specific | 0/0/0 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: shared adapter/listing not app-level provider proof. |
| `claude` | OAuth/PKCE | M / default | seed/provider-specific | 5/5/0 | P/P/P/P/P/P/P | PARTIAL | P04/P05: auth/refresh actions/auto-ping absent; PKCE/usage helpers only. |
| `cline` | oauth | C / default | seed/provider-specific | 8/8/0 | P/M/-/P/P/P/P | PARTIAL | P02-P05: onboarding/provider hooks/refresh; quota where advertised; app contracts missing. |
| `clinepass` | apikey+oauth | C / default | seed/provider-specific | 10/10/0 | P/M/-/P/P/P/P | PARTIAL | P02-P05: onboarding/provider hooks/refresh; quota where advertised; app contracts missing. |
| `cloudflare-ai` | apikey | C+SO / default | seed/provider-specific | 15/0/15 | P/P/-/P/P/P/P | PARTIAL | P02/C06/R04: account-path expansion + secondary System One Clef transport/wrapped usage absent. |
| `codebuddy-cn` | oauth+apikey | C / codebuddy-cn | seed/provider-specific | 13/13/0 | P/M/M/P/P/P/P | PARTIAL | P02-P05: onboarding/provider hooks/refresh; quota where advertised; app contracts missing. |
| `codebuddy-intl` | oauth+apikey | C / codebuddy-intl | seed/provider-specific | 15/15/0 | P/M/M/P/P/P/P | PARTIAL | P02-P05: onboarding/provider hooks/refresh; quota where advertised; app contracts missing. |
| `codex` | OAuth/PKCE | R / codex | seed/provider-specific | 17/18/1 | P/P/P/P/P/P/P | PARTIAL | P04/P05: import/bulk/reset actions and refresh wiring absent; PKCE/usage helpers only. |
| `cohere` | API key | C / default | seed/provider-specific | 3/0/3 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `commandcode` | API key | commandcode / commandcode | seed/provider-specific | 11/11/0 | P/P/M/M/M/M/M | PARTIAL | P02-P05: specialized wire/auth/refresh/listing/limits and stream codecs absent. |
| `cursor` | OAuth/import | cursor / cursor | seed/provider-specific | 14/14/0 | P/M/-/M/M/M/M | PARTIAL | P02-P05: specialized wire/auth/refresh/listing/limits and stream codecs absent. |
| `deepseek` | API key | C+M / default | seed/provider-specific | 9/0/9 | P/P/M/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof; limits client absent. |
| `featherless` | API key | C / default | seed/provider-specific | 7/0/7 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `fireworks` | API key | C / default | seed/provider-specific | 3/0/3 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `gemini` | apikey | G / default | seed/provider-specific | 11/11/0 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: shared adapter/listing not app-level provider proof. |
| `gemini-cli` | OAuth | gemini-cli / gemini-cli | seed/provider-specific | 7/7/0 | P/M/M/M/M/M/M | PARTIAL | P02-P05: specialized wire/auth/refresh/listing/limits and stream codecs absent. |
| `github` | OAuth | C+R+M / github | seed/provider-specific | 17/17/0 | P/M/M/P/P/P/P | PARTIAL | P02-P05: Copilot token exchange, model-name native Messages routing, live models/limits. |
| `gitlab` | OAuth/PAT | C / default | seed/provider-specific | 0/0/0 | P/M/-/P/P/P/P | PARTIAL | P02-P05: onboarding/provider hooks/refresh; quota where advertised; app contracts missing. |
| `glm` | API key | M+C / default | seed/provider-specific | 8/0/8 | P/P/M/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof; limits client absent. |
| `glm-cn` | API key | C / default | seed/provider-specific | 10/0/10 | P/P/M/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof; limits client absent. |
| `grok-cli` | oauth | R / grok-cli | seed/provider-specific | 4/0/4 | P/M/M/P/P/P/P | PARTIAL | P02-P05: onboarding/provider hooks/refresh; quota where advertised; app contracts missing. |
| `grok-web` | cookie | grok-web / grok-web | seed + passthrough | 12/0/12 | P/M/-/M/M/M/M | PARTIAL | P02-P05: specialized wire/auth/refresh/listing/limits and stream codecs absent. |
| `groq` | API key | C / default | seed/provider-specific | 4/0/4 | P/P/M/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof; limits client absent. |
| `hyperbolic` | API key | C / default | seed/provider-specific | 8/0/8 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `iflow` | OAuth | C / iflow | seed/provider-specific | 15/15/0 | P/M/-/P/P/P/P | PARTIAL | P02-P05: onboarding/provider hooks/refresh; quota where advertised; app contracts missing. |
| `kenari` | API key | C+R+M / default | dynamic:kenari; passthrough | 67/0/67 | P/P/M/P/P/P/P | PARTIAL | P03/P05/P06: quota client, Responses function-tools/system-role normalization, live catalog/67 seeds. |
| `kilo-gateway` | apikey | C / default | seed/provider-specific | 6/0/6 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `kilocode` | OAuth | C / default | dynamic:openrouter-free; passthrough | 8/0/8 | P/M/-/P/P/P/P | PARTIAL | P02-P05: onboarding/provider hooks/refresh; quota where advertised; app contracts missing. |
| `kimchi` | oauth+apikey | C / kimchi | seed + passthrough | 8/0/8 | P/M/-/P/P/P/P | PARTIAL | P02-P05: onboarding/provider hooks/refresh; quota where advertised; app contracts missing. |
| `kimi` | oauth+apikey | M+C+R / default | seed/provider-specific | 10/10/0 | P/M/M/P/P/P/P | PARTIAL | P02-P05: v1 native Responses missing from RW transports; device/header/quota behavior. |
| `kiro` | OAuth + API key | kiro / kiro | seed/provider-specific | 44/6/39 | P/M/M/M/M/M/M | PARTIAL | P02-P05: specialized wire/auth/refresh/listing/limits and stream codecs absent. |
| `llm7` | apikey | C / default | seed + passthrough | 5/0/5 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `mimo-free` | none | C / mimo-free | dynamic:mimo-free; passthrough | 1/0/1 | P/R/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `minimax` | API key | M+C / default | seed/provider-specific | 7/0/7 | P/P/M/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof; limits client absent. |
| `minimax-cn` | API key | M+C / default | seed/provider-specific | 4/0/4 | P/P/M/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof; limits client absent. |
| `mistral` | API key | C / default | seed/provider-specific | 3/0/3 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `mmf` | none | C / mimo-free | seed/provider-specific | 1/0/1 | P/R/-/P/P/P/P | PARTIAL | P02/P03: v1 executor alias mmf=mimo-free; no-auth/seed parity. |
| `morph` | apikey | C / default | seed/provider-specific | 6/0/6 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `muse` | API key | R / default | seed/provider-specific | 2/0/2 | M/M/-/M/M/M/M | MISSING | P01: absent identity/Responses models; no adapter/credential/listing tests. |
| `nebius` | API key | C / default | seed/provider-specific | 1/0/1 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `nvidia` | apikey | C / default | seed/provider-specific | 7/0/7 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `ollama` | apikey | O / default | seed/provider-specific | 8/0/8 | P/P/M/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof; limits client absent. |
| `ollama-local` | local/optional key | O / ollama-local | seed/provider-specific | 0/0/0 | P/P/-/P/P/P/P | PARTIAL | P02/P03: local model discovery/proxy/private-URL policy and native codecs need contracts. |
| `openai` | API key | C / default | seed/provider-specific | 31/20/11 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `opencode` | none | C / opencode | dynamic:opencode-free; passthrough | 3/0/3 | P/R/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `opencode-go` | API key | C+M+R / opencode-go | seed/provider-specific | 29/0/29 | P/P/M/P/P/P/P | PARTIAL | P02/P03: multi-protocol model dispatch/envelopes/seeds. |
| `openrouter` | apikey | C / default | dynamic:openrouter-free; passthrough | 0/0/0 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: shared adapter/listing not app-level provider proof. |
| `perplexity` | API key | C / default | seed/provider-specific | 4/0/4 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `perplexity-agent` | API key | R / default | dynamic:openai; passthrough | 11/0/11 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `perplexity-web` | cookie | perplexity-web / perplexity-web | seed/provider-specific | 7/7/0 | P/M/-/M/M/M/M | PARTIAL | P02-P05: specialized wire/auth/refresh/listing/limits and stream codecs absent. |
| `poolside` | apikey | C / default | seed/provider-specific | 3/0/3 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `qoder` | oauth+apikey | C / qoder | seed/provider-specific | 16/16/0 | P/M/M/M/M/M/M | PARTIAL | P02-P05: specialized wire/auth/refresh/listing/limits and stream codecs absent. |
| `sambanova` | apikey | C / default | seed/provider-specific | 1/0/1 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `siliconflow` | API key | C / default | seed/provider-specific | 16/0/16 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `tencent` | apikey | C / default | seed/provider-specific | 2/0/2 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `together` | API key | C / default | seed/provider-specific | 4/0/4 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `tokenrouter` | API key | C / default | dynamic:openai; passthrough | 22/0/22 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `typesafe` | API key | SO / default | seed/provider-specific | 1/0/1 | P/P/-/P/P/P/M | PARTIAL | R04/P03: jev-latest seed absent; typed validation/custom-node/stream contracts incomplete; shared.ParseUsage has no System One case. |
| `venice` | API key | C / default | dynamic:openai; passthrough | 9/0/9 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `vercel-ai-gateway` | API key | C / default | dynamic:openai; passthrough | 0/0/0 | P/P/M/P/P/P/P | PARTIAL | P02/P03/P08: shared adapter/listing not app-level provider proof; limits client absent. |
| `vertex` | API key | vertex / vertex | seed/provider-specific | 4/4/0 | P/P/-/M/M/M/M | PARTIAL | P02-P05: specialized wire/auth/refresh/listing/limits and stream codecs absent. |
| `vertex-partner` | API key | C / vertex | seed/provider-specific | 4/0/4 | P/P/-/P/P/P/P | PARTIAL | P02/C06: Vertex credentials/signing/project/location, partner wire. |
| `volcengine-ark` | API key | C / default | seed/provider-specific | 9/0/9 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `xai` | oauth+apikey | C / default | seed/provider-specific | 9/6/3 | P/M/-/P/P/P/P | PARTIAL | P02-P05: onboarding/provider hooks/refresh; quota where advertised; app contracts missing. |
| `xiaomi-mimo` | oauth+apikey | C+M / xiaomi-mimo | seed/provider-specific | 6/6/0 | P/M/M/P/P/P/P | PARTIAL | P02-P05: onboarding/provider hooks/refresh; quota where advertised; app contracts missing. |
| `xiaomi-tokenplan` | API key | C+M / xiaomi-tokenplan | seed/provider-specific | 9/0/9 | P/P/-/P/P/P/P | PARTIAL | P02/P03/P08: seed drift; shared adapter/listing not app-level provider proof. |
| `zed` | OAuth | C / zed | seed + passthrough | 0/0/0 | P/M/M/P/P/P/P | PARTIAL | P02-P05: onboarding/provider hooks/refresh; quota where advertised; app contracts missing. |

### Hidden providers and non-provider assets

| Provider / surface | Status | Disposition |
| --- | --- | --- |
| `trae` | MISSING | Intentional D06; v1 registry import commented out. Executor/OAuth files exist but not an active product provider. |
| `devin-cli` | MISSING | Intentional D06; commented out, local shell/filesystem agent boundary. |
| `windsurf` | MISSING | Intentional D06; commented out, tool-call limitations. |
| Standalone image/video/speech/embedding provider icons | MISSING | Intentional D05. `public/providers` assets are not active LLM registry entries. |
| Generic compatible provider nodes | PARTIAL | `internal/providers/generic`, credentials store and admin/UI CRUD exist; D07 permits unified model. R01/R06/P08 still apply. |

#73's "369 provider-related files" is not a provider count or an acceptance test.
This audit identifies all active registry entries and all specialized executor factories, plus the OAuth action inventory above. A narrower reproducible file set (`open-sse/providers/`, `open-sse/executors/`, `src/lib/oauth/`) contains 167 tracked files; other listing/quota/control/test files live elsewhere. No claim is made that 369 files were individually behavior-tested.

## 4. Dashboard and supporting pages (16/16)

Oracle dashboard prefix **V/** = `src/app/(dashboard)/dashboard/`; **W/** = Routeweft `ui/src/`. Routeweft uses ten hash-selected screens in `W/main.tsx`, not Next page modules. Route relocation is D01/D04; absent functionality remains a gap. These rows cover every pinned `src/app/**/page.js`, including root/supporting pages.

| Oracle page file | Routeweft screen / evidence | Status | Shortfall |
| --- | --- | --- | --- |
| `V/page.js` | `#overview`; W/Overview.tsx | PARTIAL | Runtime/count/error summary exists; readiness per provider, topology/live activity/invalidation incomplete (U03/U05/U08). |
| `V/endpoint/page.js` | `#endpoint-key`; W/EndpointAndKey.tsx | PARTIAL | Key create/copy/pause/revoke exists; restricted model/Combo access editor missing (U02/C02). |
| `V/providers/page.js` | `#providers`; W/Providers.tsx | PARTIAL | Catalog/list/filter exists; muse/readiness/health/real auth flows and pagination beyond 100 missing (U03/U04). |
| `V/providers/new/page.js` | Inline add-account/Generic form; W/Providers.tsx | PARTIAL | API-key/token paste is not OAuth/device/PAT/cookie/import onboarding; pre-save validation incomplete (U03/R06/P04). |
| `V/providers/[id]/page.js` | Selected provider panel; W/Providers.tsx | PARTIAL | Connection/model/alias/price/order/test operations exist; special imports, quota/compatibility/proxy management/default models/deletion/paging incomplete (U03/U04). |
| `V/combos/page.js` | `#combo-capability-adapter`; W/Combos.tsx | PARTIAL | Ordered members/Fusion/vision/audio pool UI exists; app dispatch, per-Combo Fusion tuning/object override semantics, end-to-end empty/deselect/context behavior not proven (U09/R01/C04). |
| `V/systemone/page.js` | `#system-one`; W/SystemOne.tsx | PARTIAL | Typed probe through connection test exists; custom typed nodes/Cloudflare transport/model seeds and real app dispatch missing (U09/R04/P02). |
| `V/usage/page.js` | `#usage`; W/Usage.tsx | PARTIAL | Totals/cache/daily chart/recent list exist; costs, details, topology, account/key filters/paging/live updates incomplete (U05/U08). |
| `V/quota/page.js` | `#quota-tracker`; W/Quota.tsx | PARTIAL | State/read/refresh exists for first 100 accounts; reset credits/auto-ping/visibility and full provider clients absent (U06/P05). |
| `V/token-saver/page.js` | `#token-saver`; W/TokenSaver.tsx | PARTIAL | Toggles/levels/timeouts persist; no live managed helper controls/diagnostics, actual transform pipeline unwired (U07/R03/R09). |
| `V/console-log/page.js` | `#console-log`; W/ConsoleLog.tsx | PARTIAL | Bounded filter/page/refresh exists; clear/live stream missing (U08/R10). |
| `V/profile/page.js` | `#settings`; W/Settings.tsx | PARTIAL | Password, settings, proxy fields, backup/restore present; requireLogin, semantic maps, compatibility flags, locale/live controls incomplete (U10/C03-C06). |
| `src/app/dashboard/settings/pricing/page.js` | Providers pricing panel; W/Providers.tsx | PARTIAL | Relocation allowed D01; built-in default pricing/reset/complete model catalog incomplete (U04/R06). |
| `src/app/login/page.js` | W/SignIn.tsx + main.tsx | PARTIAL | Session login/logout exists; deliberate username/session changes D04, recovery/locale behavior unresolved U10/C03. |
| `src/app/callback/page.js` | No callback screen | MISSING | OAuth code/state/callback workflow absent (U03/P04). |
| `src/app/page.js` | W/main.tsx Overview default | PARTIAL | Root-to-dashboard conceptual landing exists in preview; production asset delivery by Go static route (`ROUTEWEFT_UI_DIR`, U01). |

**Delivery (U01 resolved):** Docker copies Vite output to `/ui` and sets `ROUTEWEFT_UI_DIR=/ui`; `internal/app/static.go` serves it from `Handler` (tested in `static_test.go`). `ui/scripts/smoke.sh` starts Vite preview, so it does not establish production delivery. `ui/package.json` has no unit-test script; CI runs typecheck/build, not browser smoke (U01/U11).

**Cross-page gap:** no `EventSource` subscription in `ui/src`. Backend SSE exists, but manual refresh is not one live channel + refresh fallback (U08, PRD-OBS-003). First-100 fetches in Providers/Quota can hide existing accounts/models/configuration; not an intentional product limit (U04/U06).

## 5. Configuration and durable data

### 5.1 Every default setting in the pinned v1 repository

Oracle: `src/lib/db/repos/settingsRepo.js:DEFAULT_SETTINGS`.
Routeweft: `internal/runtime/compiler.go:defaultSettings`, `settings_update.go`, `A/settings.go`.
PRESENT here refers only to the named stored/compiled setting; where its effect is not wired, status is PARTIAL. Values cross the admin API as strings with a writable allowlist, not arbitrary v1 JSON settings (D01).

| v1 setting (default) | Routeweft mapping | Status | Disposition / shortfall |
| --- | --- | --- | --- |
| cloudEnabled (false) | absent | MISSING | Intentional D12, PRD 4.2/15; no cloud sync. |
| stickyRoundRobinLimit (3) | same | PARTIAL | In-memory selector exists; R01/C05 app integration. |
| providerStrategies ({}) | same key, map[string]string | PARTIAL | v1 values are objects with fallbackStrategy/sticky limit/proxyPoolId/rotateStrategy; current API rejects them (C04/C06). |
| quotaVisibility ({}) | same JSON object | PARTIAL | Stored; SPEC 22 explicitly leaves runtime semantics unspecified; v1 visibility workflow needs product decision (C05/U06). |
| comboStrategy (fallback) | same | PARTIAL | Stored/compiled; R01 app integration. |
| comboStickyRoundRobinLimit (1) | same | PARTIAL | Global setting exists; compiled Combo defaults its own limit=1, potentially shadowing global values (C04/R01). |
| comboStrategies ({}) | same key, map[string]string | PARTIAL | v1 objects carry fallbackStrategy/judgeModel/fusionTuning; missing representation and tuning (C04/C06). |
| capacityAdapter.vision ({enabled:false,roundRobin:false,models:[]}) | capacityAdapterVision {enabled,pool} | PARTIAL | Renaming/normalization D10; API omits roundRobin/strategy, so saves lose selection policy (C04); R01 empty/deselect/trim runtime proof. |
| capacityAdapter.pdf (same) | capacityAdapterPDF | PARTIAL | State preserved, no standalone PDF API implied; C04. |
| capacityAdapter.audioInput (same) | capacityAdapterAudioInput | PARTIAL | Visible LLM audio adapter; C04/R01. |
| capacityAdapter.videoInput (same) | capacityAdapterVideoInput | PARTIAL | State preserved, no standalone video product; C04. |
| requireLogin (true) | same default, not writable; session always required | PARTIAL | Strong auth fits baseline security; disabling capability not explicitly rejected by approved contract (C03), not quietly "intentional". |
| requireApiKey (true) | same | PRESENT | Compiled key index and setting mutation; policy scope C02 remains separate. |
| enableObservability (false) | same | PARTIAL | Worker options sampled on initialize, not live settings updates (C05). |
| observabilityMaxRecords (1000) | same | PARTIAL | Stored/worker startup option; dynamic retention/detail capture gaps C05/R10. |
| observabilityBatchSize (20) | same | PARTIAL | Same default; startup-only worker configuration C05. |
| observabilityFlushIntervalMs (5000) | same | PARTIAL | Same default; live effect C05. |
| observabilityMaxJsonSize (5) | 5242880 bytes | PARTIAL | Important: actual pinned v1 requestDetailsRepo multiplies by 1024 (5 KiB), despite Routeweft PRD 15 requiring 5 MB. Routeweft's 5 MiB follows PRD; migration must encode units, not multiply by an assumed MiB (D10/C01/C04). |
| outboundProxyEnabled (false) | same | PARTIAL | Binder/global routing exists; app/dynamic/proxy-test coverage C05/R08. |
| outboundProxyUrl ("") | same | PARTIAL | Validated/protected URL, runtime binder; C05/R08. |
| outboundNoProxy ("") | noProxy JSON string array, [] | PARTIAL | Explicit representation conversion needed C04; matching behavior requires fixtures. |
| dnsToolEnabled ({}) | boolean false | PARTIAL | Provider-keyed compatibility map cannot round-trip through boolean validator (C04). |
| rtkEnabled (true) | same | PARTIAL | Transform implementation exists; app/bypass R03. |
| headroomEnabled (false) | same | PARTIAL | R03/R09; settings do not run helper. |
| headroomUrl (HEADROOM_URL or localhost:8787) | headroomUrl localhost:8787 | PARTIAL | Stored; v1 env precedence and actual helper policy C04/R09. |
| headroomCompressUserMessages (false) | same | PARTIAL | R03 pipeline. |
| headroomTimeoutMs (3000) | same | PARTIAL | Stored; validation/effect/proxy/fail-open R03/C05. |
| cavemanEnabled (false) | same | PARTIAL | R03 pipeline/bypass. |
| cavemanLevel (full) | same, lite/full/ultra validated | PARTIAL | Prompt policy code exists; final-body/idempotence/app proof R03. |
| ponytailEnabled (false) | same | PARTIAL | R03 pipeline/bypass. |
| ponytailLevel (full) | same, lite/full/ultra validated | PARTIAL | R03 pipeline. |
| pxpipeEnabled (false) | same | PARTIAL | R03/R09 actual execution/control. |
| pxpipeAutoInstall (true) | same | PARTIAL | Install policy stored, no admin install/lifecycle path R09. |
| pxpipeMinChars (25000) | same | PARTIAL | R03 transform wiring; validate/effect C05. |
| pxpipeTimeoutMs (15000) | same | PARTIAL | R03 transform wiring; validate/effect C05. |

### 5.2 Additional consumed settings / configuration objects

v1 PATCH accepts arbitrary non-password settings, so defaults alone are not the surface. Evidence: `src/app/api/settings/route.js`, `src/sse/services/auth.js`, `open-sse/services/combo.js`, `src/shared/services/quotaAutoPing.js`, `src/lib/db/repos/requestDetailsRepo.js`, Headroom routes.

| v1 surface | Routeweft mapping / evidence | Status | Shortfall / disposition |
| --- | --- | --- | --- |
| fallbackStrategy (fill-first fallback) | providerStrategy allowlist + routing/accounts.go | PARTIAL | Rename is reasonable D10, but no tested import or object overrides; R01/C04. |
| password / newPassword / currentPassword; hasPassword | admin_users + /auth/password, adminauth | PARTIAL | KDF + revoke sessions implemented D04; local recovery/bootstrap/login-optional policy unresolved C03. |
| claudeAutoPing / codexAutoPing connections maps | no scheduler/action/settings | MISSING | Opt-in window warming after reset absent P05/C05/U06. |
| headroomCodeAware / headroomKompress | no writable settings/control API | MISSING | Used by v1 managed start/restart; R09/C04. |
| observabilityMaxBuffered | bounded queue options, no corresponding writable setting | PARTIAL | Bounded design retained BDR-013; exact pressure/loss/live policy fixtures needed C05/R10. |
| ccFilterNaming / bypass naming probes | no corresponding writable setting/ingress behavior | MISSING | v1 Chat bypass uses this flag; retained-client decision required C04/R11. |
| providerCompatibility | RW stores JSON object, v1 uses provider-specific flags | PARTIAL | SPEC 22 leaves semantics unspecified; not evidence of functional equivalence C04/C05. |
| Provider connection name/email/identity/authType/isActive/priority/globalPriority/defaultModel/testStatus/lastError/lastErrorAt | normalized credentials connections + RuntimeState | PARTIAL | Some naming/enable/order/identity exists; global priority/default-model/status persistence semantics need mapping C06. |
| apiKey/accessToken/refreshToken/idToken/expiry/workspace/project/account/region/profile/session/cookie providerSpecificData | sealed Secret + registry/resolver | PARTIAL | Secret isolation is D03/D04; module-specific signing/identity/import/refresh/PAT/cookie data/workflows incomplete P02/P04/C06. |
| Per-connection proxy enabled/URL/no-proxy/proxyPoolId/vercelRelayUrl/strictProxy | proxy_pools/members + connection binding | PARTIAL | Global/pool primitives exist; direct/relay/strict proxy semantics and operator CRUD/test incomplete C06/R08. |
| No-auth providerStrategies proxyPoolId / rotateStrategy | no equivalent object setting | MISSING | v1 virtual free account can rotate all active proxy pools; not represented by map[string]string C04/C06/R01. |
| Nodes type/name/prefix/apiType/baseUrl; System One typed nodes | builtin/generic node/transports model | PARTIAL | D07 unified compatible nodes; custom typed SO + Cloudflare secondary SO not modeled R04/C06. |
| Combo name/kind/ordered model IDs; fallback/RR/sticky/judge/fusionTuning | combos/combo_models + fusion boolean/judge | PARTIAL | Kind/tuning/object overrides not mapped. v1 default minPanel=2/grace=8000ms/hard=90000ms vs Go min=1/grace=250ms/hard=30000ms. Both defaults return a sole successful answer; Go with explicit MinPanelQuorum>1 rejects it with ErrFusionQuorum, whereas v1 minPanel only gates the grace window (C06/R01). No approved exception. |
| capacity pools roundRobin/models, empty pool/deselect, context trim | runtime/combo.go + routing/capability.go | PARTIAL | Unit primitives exist; exact app cases and migration of object shape needed R01/C04. |
| Model aliases/custom/disabled, kind/caps/context/thinking/upstream IDs/quota family | normalized model tables/compiler | PARTIAL | Core add/disable/alias exists; seed drift/kinds/provider capabilities/manual delete/pricing completeness P03/R06/C06. |
| Pricing defaults/overrides and cached token prices | pricing_overrides + shared token usage | PARTIAL | Override storage exists; cost production/read model and baseline default pricing incomplete R10/C06. |
| API-key accessRestricted/accessAllow | api_keys has no policy columns/index | MISSING | Exact resolved model/alias/Combo allowlists, adapter filtering and list masking absent; security blocker C02/U02. |
| Locale/theme/preferences | theme localStorage light/dark/system | PARTIAL | Theme retained; locale endpoint/translations/preferences mapping absent U10. |
| Removed tunnelEnabled/tunnelUrl/tunnelProvider/tailscaleEnabled/tailscaleUrl/tunnelDashboardAccess/mitmRouterBaseUrl/mitmSudoEncrypted | absent | MISSING | Intentional D12; pinned v1 strips these settings on update/export too. |
| cloudUrl / cloud enable integrations | absent | MISSING | Intentional D12; do not infer exclusion for an unrelated Netlify outbound relay R08. |

### 5.3 Tables, files and migration boundary

Never point Routeweft at the oracle's live database. BDR-001 intentionally gives Routeweft a new schema; #73 additionally requires an explicit tested migration path or reviewed divergence. Those are different assertions: direct compatibility is excluded, but a safe migration/recreation policy is still missing C01.

| v1 schema v3 table / file | Routeweft schema v4 equivalent | Status | Shortfall / disposition |
| --- | --- | --- | --- |
| _meta | meta + schema_migrations | PARTIAL | Independent revision/version metadata D08; cross-product mapping C01. |
| settings singleton JSON | settings key/value rows | PARTIAL | D08 normalization; all field/unit conversions C01/C04. |
| providerConnections JSON + indexed identity columns | provider_connections + credential_events | PARTIAL | Encrypted secrets D03; provider state/identity conversion C01/C06. |
| providerNodes JSON | provider_nodes | PARTIAL | D07/D08; typed SO config C01/R04. |
| proxyPools JSON | proxy_pools + proxy_pool_members | PARTIAL | D08 normalized members; binding/deletion/relay config C01/C06. |
| apiKeys plaintext + accessRestricted/accessAllow | api_keys digest/prefix/pause | PARTIAL | D03 one-way verification intentional; key reissue/import policy C01, missing access policy C02. |
| combos models JSON | combos + combo_models | PARTIAL | Ordered normalized members exist; kind/tuning/strategy conversion C01/C06. |
| kv scopes (aliases, custom/disabled models, pricing, provider-specific values) | model_aliases/custom_models/disabled_models/pricing_overrides/provider_models | PARTIAL | Typed tables intentional D08; enumerate scoped mappings, do not silently discard unknown scopes C01/C06. |
| usageHistory | usage_events | PARTIAL | Tokens/cache/status/duration fields; cost/endpoint/attribution/history mapping C01/R10. |
| usageDaily dateKey JSON | usage_daily dimensional rows | PARTIAL | Rollup policy/history/cost conversion C01/R10. |
| requestDetails JSON | request_details | PARTIAL | Bounded/redacted independent detail storage; no normal app detail producer/filter parity R10/C01. |
| DATA_DIR/db/data.sqlite + WAL/SHM | ROUTEWEFT_DATA_DIR/routeweft.sqlite + WAL/SHM | MISSING | Direct read intentionally unsupported D08; no import tool or tested recreation policy C01. |
| Legacy db.json/usage.json/disabledModels.json/request-details.json | no LiteRouter legacy reader | MISSING | D08 independent storage; explicit history/config disposal/recreation policy C01. |
| /api/settings/database JSON export/import | Routeweft ZIP containing SQLite + metadata | PARTIAL | D08 safe validation/rollback intentional; not a v1 importer C01. |
| db/backups schema-change safety copies excluding requestDetails, keep 3 | online backup/staged restore/pre-restore rollback artifact | PARTIAL | Stronger independent backup contract D08; cross-product restore forbidden, v1 migration rehearsal C01. |

Evidence: pinned `src/lib/db/{schema,paths,backup,migrate}.js` and repositories; Routeweft `internal/store/migrations/v1.go` through `v4.go`, `internal/backup`, `internal/credentials`, `docs/RUNBOOK.md` 11-14.

### 5.4 Environment / process / deployment configuration

Names below are source names, not secret values. Default same-function mappings are assessed; preserving a LiteRouter environment name is not required by standalone BDR-001. Platform/build/debug-only knobs are explicitly grouped, not presented as product feature gaps.

| v1 configuration names / purpose | Status | Routeweft equivalent / disposition |
| --- | --- | --- |
| PORT (20128), DATA_DIR | PRESENT | ROUTEWEFT_LISTEN (:21128), ROUTEWEFT_DATA_DIR; intentional identity/path D02/D08. |
| INITIAL_PASSWORD, JWT_SECRET, AUTH_COOKIE_SECURE | PARTIAL | ROUTEWEFT_BOOTSTRAP_ADMIN_PASSWORD / USERNAME, process-local opaque sessions, always-Secure cookie. D04 auth design; C03 recovery/disable behavior. |
| API_KEY_SECRET, MACHINE_ID_SALT, NINEROUTER_PEER_TOKEN | PARTIAL | Own key digest generation + sealed credentials; no machine CLI bypass D03/D04. Trusted-peer deployment behavior requires C09. |
| REQUIRE_API_KEY (.env.example) | PARTIAL | requireApiKey compiled setting. No direct consumer found in pinned src/open-sse; do not claim this sample variable overrides v1 runtime. |
| NINEROUTER_PROXY_CLIENT_MAX_BODY_SIZE | PRESENT | ROUTEWEFT_MAX_BODY_BYTES / --max-body-bytes, 134217728 default. |
| TRUST_PROXY | PARTIAL | Spoofed forwarded IPs ignored; ROUTEWEFT_TRUSTED_PROXIES documented, no CLI consumer found C09/C10. |
| HTTP_PROXY, HTTPS_PROXY, ALL_PROXY, NO_PROXY; lowercase variants | PARTIAL | Global/pool/noProxy configuration exists; exact scheme/env precedence and direct per-connection behavior need C04/C06/R08. |
| NINE_ROUTER_PROXY_URL, NINE_ROUTER_NO_PROXY, NINE_ROUTER_PROXY_MANAGED | PARTIAL | RW uses its own pooled binder rather than environment mutation; prove matching/binding changes C05/C06. |
| HEADROOM_URL; headroom managed process/extras | PARTIAL | headroomUrl setting; helper process ownership/control R09. |
| ENABLE_REQUEST_LOGS, OBSERVABILITY_ENABLED, OBSERVABILITY_MAX_RECORDS, OBSERVABILITY_BATCH_SIZE, OBSERVABILITY_FLUSH_INTERVAL_MS, OBSERVABILITY_MAX_JSON_SIZE, OBSERVABILITY_MAX_BUFFERED | PARTIAL | Compiled enableObservability/size/batch/flush settings + bounded telemetry; v1 env-vs-UI precedence and actual KiB units must be explicit C04/C05. |
| USAGE_BATCH_SIZE, USAGE_FLUSH_INTERVAL_MS, USAGE_MAX_BUFFERED | PARTIAL | Bounded usage queue/batcher, not same knob names; accounting/cost/failure behavior needs R10/C05. |
| REQUEST_LOG_MAX_SESSIONS, REQUEST_LOG_MAX_SIZE_MB, REQUEST_LOG_PRUNE_INTERVAL_MS | PARTIAL | Independent detail retention/prune policy; no proven equivalent capture/retention effects R10/C05. |
| LOG_LEVEL, LOG_USAGE_VERBOSE | PARTIAL | Structured slog exists; ROUTEWEFT_LOG_LEVEL documented but CLI uses default handler without reading it C10. |
| MAX_ACCOUNT_FALLBACK_ATTEMPTS | PARTIAL | Finite attempt primitives; actual app wiring/budgets R01. |
| FETCH_CONNECT_TIMEOUT_MS (60000), STREAM_FIRST_CHUNK_TIMEOUT_MS (200000), STREAM_STALL_TIMEOUT_MS (360000), PENDING_COMPLETION_FLUSH_MS (3000), MODEL_TEST_TIMEOUT_MS (200000) | PARTIAL | v1 envMs overrides in runtimeConfig.js. Native streaming/probe timeout semantics need R07/R11; RW probe fixed shared 20000ms is not equivalent. |
| DISABLE_BACKGROUND_TOKEN_REFRESH, BG_REFRESH_DELAY_MS, BG_REFRESH_GOOGLE_DELAY_MS, EAGER_PROJECT_ID_REFRESH, ONBOARD_MAX_ATTEMPTS, ONBOARD_RETRY_DELAY_MS | PARTIAL | Registry/singleflight primitives exist; provider refresh/onboarding/project wiring P04. |
| ANTIGRAVITY_REDIRECT_URI, KIMI_CODING_OAUTH_CLIENT_ID, KIMI_OAUTH_CLIENT_ID | MISSING | Provider flow overrides not represented in callable admin actions P04. |
| AZURE_API_VERSION, AZURE_DEPLOYMENT, AZURE_ENDPOINT, AZURE_ORGANIZATION; COMMAND_CODE_API_BASE_URL; MIMO_PLATFORM_URL; KIRO_TOOL_CALL_REPAIR | PARTIAL | Provider-specific behavior/config mapping P02/C06, not satisfied by a base URL declaration. |
| MODEL_CATALOG_SYNC | MISSING | No catalog-wide sync controller/action R06/P03. |
| CLOUD_URL, NEXT_PUBLIC_CLOUD_URL; REDIS_URL, REDIS_KEY_PREFIX, REDIS_POOL_SIZE, REDIS_TIMEOUT_MS; LITEROUTER_MULTI_REPLICA; RUNTIME_CONFIG_TTL_MS, RUNTIME_CONFIG_VERSION_TTL_MS | MISSING | Intentional D12/D13: no cloud/Redis/multi-replica/cache-invalidation bus; immutable snapshots replace process cache polling. |
| MINIMAL_PROFILE | MISSING | v1 hides Headroom; Routeweft has explicit fixed product boundary PRD 4.2. Not permission to drop retained helper integration R09. |
| CLI_DEVIN_BIN, CLI_DEVIN_AGENT_TYPE, DEVIN_MCP_RESULTS, DEVIN_MCP_SERVERS, DEVIN_MCP_TOOLS, DEVIN_PERMISSION_MODE, TRAE_STREAM_TIMEOUT_MS | MISSING | Intentional D06: hidden non-product providers. |
| GEMINI_NATIVE_TTS_FETCH_TIMEOUT_MS | MISSING | Intentional D05 standalone TTS exclusion. |
| NODE_ENV, NEXT_DIST_DIR, NEXT_PHASE, NEXT_RUNTIME, NEXT_TRACING_ROOT_MODE | MISSING | Intentional D04: no production Node/Next process. |
| CURSOR_PROTOBUF_DEBUG, CURSOR_STREAM_DEBUG, DEBUG_BACKGROUND_TOKEN_REFRESH | MISSING | Debug toggles are implementation-specific; provider functionality/redacted diagnostics still required P02/P04/R10, not raw-debug parity. |
| HOME, PATH, APPDATA, LOCALAPPDATA, COMPUTERNAME, HOSTNAME, platform PATH probes | PARTIAL | OS inputs are not new Routeweft settings; retained host auto-import needs explicit local/admin policy P04. |
| ROUTEWEFT_CREDENTIAL_KEY; ROUTEWEFT_ALLOW_PRIVATE_UPSTREAMS; ROUTEWEFT_CORS_ORIGINS | PRESENT | New own-product security knobs (D03/D11); no oracle credential/state sharing. |
| Caddy credential redaction, token-sync limits, deploy-time image retention | MISSING | No corresponding config/service among pinned application routes. Deployment evidence/ownership decision C08; do not invent a 98th application route. |
| TLS CA trust in production image | MISSING | Scratch Dockerfile copies binary/UI only, no CA bundle. Typical HTTPS upstream validation cannot be certified C10. |

## 6. Behavioral and operational divergence ledger

Only rows marked **approved intentional** are justified by existing requirements. A **gap** or **decision required** is not an accepted exception, even when a stronger behavior may be desirable. #73 requires review/sign-off of this ledger before cutover; this documentation PR does not sign it off.

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
| D09 | One SSE event channel, targeted invalidation, refresh fallback | PRD-OBS-003; SPEC 22/23. | UI currently has no subscription; console/activity content still missing U08/R10. |
| D10 | Typed/validated string settings and normalized adapters/models; final-body cache order; PRD's 5 MB detail target | PRD 9/11/12/15; SPEC 7/17/18; BDR-007/011/012. | Changed v1 strategy/map/size/no-proxy semantics need explicit conversion C04; cache transforms cannot remain unwired R03. |
| D11 | Explicit CORS allowlist, strict SSRF/private-network policy, trusted local opt-in, no forwarded-header blind trust | PRD-API-005 / PRD-SEC-002/003/004; SPEC 20/27/34. | Do not weaken network policy for parity; callable local-provider/redirect tests and h2c client coverage remain required. |
| D12 | No cloud sync, tunnel/Tailscale/MITM lifecycle, removed legacy platform state | PRD 4.2/15; BDR-001/016. | Does not automatically exclude outbound proxy relay provisioning; R08 needs decision. |
| D13 | No Redis/Postgres/remote cache bus/multi-replica topology; immutable snapshots + separate mutable state, pooled transports, bounded async telemetry | PRD 3/18/19; SPEC 5-8/20/21; BDR-004-008/013/017. | Same routing/cache/accounting outcomes still required; architecture is not evidence of end-to-end parity. |

### 6.2 Real gaps / decisions (all assigned)

The checklists in #75-#78 are normative follow-up scope for this inventory. Every PARTIAL/MISSING row above maps to these gaps or an approved D row.

| IDs | Explicit gap / decision required | Authority | Owner |
| --- | --- | --- | --- |
| R01 | Actual configured app provider/account/Combo/Fusion/native endpoint/fallback execution; bounded budgets and v1 Fusion defaults/one-success semantics | PRD 5/10/11; SPEC 8/13-16; Sprint 5.1 | #75 |
| R02 | Cross-protocol translators and stream/terminal/tool/reasoning mapping, not just nil hooks | PRD 5; SPEC 9/10; Sprint 5.2 | #75 |
| R03 | Ordered savers + final-body cache on all paths/fallback; both bypass header names; fail-open | PRD 12; SPEC 17/18; BDR-012; Sprint 5.3 | #75 |
| R04 | System One exact non-empty questions validation, native/custom/Cloudflare typed transports, missing usage-parser case, streaming and non-streaming/cancellation/accounting tests | PRD 5/14; SPEC 10/28; #73 explicit System One | #75 |
| R05 | Full historical alias coverage, Ollama tags, model info id/kind/access semantics; decide non-media kind/legacy /api/v1 exposure | PRD-API-001-004; SPEC 34 | #75 |
| R06 | Validation/manual inference fallback, availability/reset/catalog sync/suggestions/free-filter/custom delete/batch workflows | PRD 7/9/14; SPEC 22/33 | #75 |
| R07 | First-token canceling model probes, slow-model 504/network 502, correct tested/upstream model attribution vs fixed non-streaming shared 20s probes | PRD 14/21; #73 production drift; pinned ping.js/runtimeConfig.js | #75 |
| R08 | Proxy-pool mutations/connectivity/global proxy test; Netlify relay explicit retain/reject decision | PRD-ROUTE-005/14; SPEC 20/22 | #75 |
| R09 | Managed Headroom extras/proxy/process health/lifecycle + PXPIPE install/logs/stats/health/lifecycle; host safety | PRD-XFORM-004/005; SPEC 17 | #75 |
| R10 | Request-detail producer/filtering, cost/TTFT/API-key attribution/topology/history/log clear/live data, operator quota/reset actions | PRD 13/14; SPEC 21/22 | #75 |
| R11 | Required pinned differential protocol/auth/shape/stream/security suite in PR CI; current fixture tests are not differential | PRD 20/21; SPEC 28/29; #73 acceptance | #75 |
| P01 | muse missing, 81-vs-80 active provider drift reviewed before baseline change | PRD 6/21; SPEC 32/37 | #76 |
| P02 | Provider-specific wire/executor/envelope/signing/native-header/error/usage hooks (including Kimi Responses, Cloudflare SO, Kenari) | PRD-PROV-001/002; SPEC 11/32 | #76 |
| P03 | Authenticated live/provider-specific discovery, aliases/caps/seeds/filter/passthrough; never delete known manual models on outage | PRD 6/9; SPEC 11/33 | #76 |
| P04 | Callable OAuth/device/exchange/import/auto-import/PAT/cookie, validated distinct identity, durable singleflight refresh with real callbacks | PRD 8; SPEC 19/35; Sprint 5.5 | #76 |
| P05 | All v1-advertised limits/quota/reset clients; Codex credits and opt-in Claude/Codex auto-ping | PRD 8/13; SPEC 14/19 | #76 |
| P06 | Kenari production fixes: endpoint/header/tool/system-role/live-list/quota contracts | PRD 6/21; #73 | #76 |
| P07 | Immutable ready/experimental/unavailable metadata separate from enabled | PRD-PROV-004; SPEC 36; Sprint 5.4 | #76 |
| P08 | Per-provider listing/auth/quota/S/N/error/token fixtures + actual app evidence; no catalog-only proof | PRD 6/20; SPEC 28/32; #73 | #76 |
| U01 | Production static UI asset delivery choice/configuration tested, not just Vite preview | PRD 14/19; SPEC 23/30; BDR-003 | #77 |
| U02 | Key restriction editor and access-aware discovery | #73 endpoint-key scope; PRD 14/17 | #77 |
| U03 | Readiness/auth/import/callback/provider/model tests/health/quota UI | PRD 8/14; SPEC 36 | #77 |
| U04 | >100-row paging; complete provider/model/price/default/compatibility/proxy management | PRD 9/14; SPEC 22 list bounds | #77 |
| U05 | Usage costs/details/filter/topology/live data/pagination | PRD-OBS-001/002; SPEC 21/22 | #77 |
| U06 | Quota visibility/all accounts/reset credits/auto-ping | PRD-QUOTA-001/14; #73 | #77 |
| U07 | Helper lifecycle/health/logs/stats/extras/diagnostics | PRD-XFORM-004/005/14 | #77 |
| U08 | Shared live SSE + refresh fallback, console clear/activity/Overview invalidation | PRD-OBS-003/14; SPEC 23 | #77 |
| U09 | App-level typed SO and full Combo/Fusion/capability workflows | PRD 11/14; UI_STYLE | #77 |
| U10 | Locale/profile/recovery/login preferences retained or explicitly rejected | PRD 14/21; pinned locale/profile pages | #77 |
| U11 | UI unit/browser/a11y checks in PR CI | SPEC 28/29; Sprint 7/8; AGENTS 10 | #77 |
| C01 | Tested isolated v1 migration/recreation policy, synthetic table/JSON/unit/key/history mapping | #73 data acceptance; PRD 18; BDR-001 | #78 |
| C02 | Restricted keys missing persistence/compiled enforcement/aliases/Combo-only grants/adapters/model filtering across key forms | #73 current v1 scope; PRD 17; pinned keyAccess.js/tests | #78 |
| C03 | Explicit auth policy decision for requireLogin/local CLI/reset/bootstrap; do not silently introduce bypass | PRD 14/15/17; SPEC 12/22 | #78 |
| C04 | Exact settings map/strategy/sticky/unit/default conversions; adapter API drops RR selection; ccFilterNaming/Headroom extras flags | PRD 10-12/15/21; SPEC 7/22 | #78 |
| C05 | Dynamic settings actually affect telemetry/proxy/transforms/cache/compatibility/quota/auto-ping | PRD 12-16; SPEC 7/21/22 | #78 |
| C06 | Provider-specific config/identity/proxy/relay, model/pricing/Combo kind/tuning fields mapped without loss | PRD 8-11/14; SPEC 4/11/15/16/19 | #78 |
| C07 | Login Retry-After, correct model diagnostics and sync-secret redaction regression evidence | PRD 17/21; #73 production incidents | #78 |
| C08 | External token-sync/Caddy/retention evidence and ownership decisions; no invented endpoint/production execution | #73; RUNBOOK 7/10/13/14; #74 coordination | #78 |
| C09 | Readiness/bootstrap/h2c/trusted-peer/shutdown/cancellation/accounting/race behavior evidence | PRD 17/19; SPEC 24/28/34 | #78 |
| C10 | Production TLS trust bundle, documented log-level/trusted-proxy knobs vs actual CLI/env mapping | PRD 18-20; SPEC 20/27/30; RUNBOOK | #78 |

### Production-incident drift explicitly dispositioned

| #73 incident | Pinned source / Routeweft evidence | Status / decision |
| --- | --- | --- |
| Kenari model probe first-token/timeout | v1 `src/app/api/models/test/ping.js`, `tests/unit/model-test-first-token-probe.test.js`, runtimeConfig MODEL_TEST_TIMEOUT_MS=200000; RW `A/providers.go:testProviderModel` | PARTIAL; port behavioral contract R07/P06. A shorter non-streaming probe is not an approved simplification. |
| Dashboard model-test logged different model | v1 `tests/unit/model-test-results.test.js`; RW modelTestResult.ModelID set from loop input | PARTIAL; explicit results help but full request/outcome/log model correlation not proven R07/C07. |
| Model-mismatch log correctness | RW completion callback records selected outcome.ModelID, but there is no differential regression fixture | PARTIAL; preserve requested/selected/upstream distinction without exposing prompts R07/R10/C07. |
| Auth-adjacent rate limit + Retry-After | v1 login returns Retry-After on 429; RW throttle returns 429 without header | PARTIAL; C07. Token-sync service is not one of the 97 routes. |
| Caddy X-Sync-Secret redaction / token log suppression | No Caddy/token-sync application implementation found in pinned src/open-sse/scripts; RW log buffer has generic secret markers but not deployed ingress evidence | MISSING evidence; C07/C08 require explicit external deployment ownership/fixtures. Do not claim default access-log redaction. |
| Image retention at deploy, not timer | v1 `scripts/post-deploy.sh`, `tests/unit/production-image-retention.test.js`; RW no deploy workflow/post-deploy retention script | MISSING; C08/#74 decision: keep running+recorded rollback images, bounded deploy-time cleanup if adopted; no timer or cleanup run in Phase 1. |

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
for (const [status, count] of Object.entries({ PRESENT: 6, PARTIAL: 48, MISSING: 43 })) {
  assert.equal(routeRows.filter(line => line.includes('| ' + status + ' |')).length, count);
}
assert.match(text, /013d26a2014c16e0e4f2a35aa9acf2fa5bfd4e7c/);
console.log('97 routes, 81 active providers, 16 pages: exact pinned inventory, no duplicates; route totals agree.');
JS
```

### Evidence already present vs still required

- Existing `compat/fixtures/systemone-nonstreaming.json` is exercised by `I/compat_test.go:TestSystemOneNativePassthroughPreservesTypedEnvelope`; tests assert the full upstream request and response bytes using an injected resolver. CI's `go test ./...` includes it. No System One streaming fixture found; request parser only checks presence of questions, whereas v1 rejects null/array/empty questions. `internal/providers/shared/usage.go:ParseUsage` has no System One case, so the fixture's usage metadata does not prove actual accounting (R04).
- Native protocol fixtures, auth/error/stream cancellation tests, selector/cache/transform tests, SQLite transactions/backup/restore and SSRF/security suites exist. They are useful components, not all-provider or v1/v2 parity certification.
- Current CI: Go tests, race only `./compat/...`, vet/build/benchmark; UI lockfile/typecheck/build; Docker build/entrypoint/image-size. No pinned differential suite, full race gate or browser smoke gate (R11/U11/C09).
- Local Go race attempt: **not run**, executable unavailable (`go: command not found`). No production/oracle runtime calls made. Documentation changes do not alter runtime, hot-path benchmarks, SQLite schema or security implementation.
- Local UI `npm ci`, `npm run typecheck` and `npm run build` passed. `npm audit` reports one existing high-severity transitive `source-map-js` advisory (GHSA-68fv-2mgg-jv7q); no dependency changes made in this inventory-only PR.
- This document's inventory checks and PR CI results are recorded in the PR handoff. Passing CI validates the documentation head against existing checks only; #73 and #74 remain blocked on real parity work and reviewed divergence sign-off.
