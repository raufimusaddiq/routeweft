# Routeweft Product Requirements Document

Status: **Implementation-ready product requirements**
Product owner/design authority: Routeweft repository
Behavioral reference snapshot: LiteRouter `2ffb7922954112b30425cd487d686758e519397e`
Architecture reference: `docs/SPEC.md`
Provider requirements: `docs/PROVIDER_BASELINE.md`

## 1. Product thesis

Routeweft is a standalone, local-first LLM routing gateway and operator control plane.

It gives clients one stable endpoint while owning the operational complexity behind it:

- many LLM providers;
- many accounts per provider;
- API-key/OAuth/cookie/no-auth credentials;
- model discovery/mapping;
- provider/account/Combo routing;
- quota, cooldown and health;
- native protocol selection and translation fallback;
- prompt-efficiency transforms;
- prompt-cache preservation;
- Usage/Quota/request diagnostics;
- backup, restore and operator workflows.

Routeweft is **not** a LiteRouter release line, migration wrapper, shared database, plugin or runtime dependency. LiteRouter is used only as a behavioral/product reference while Routeweft establishes its own tests.

## 2. Primary user and jobs-to-be-done

The first user is a technically sophisticated operator running one self-hosted Routeweft instance on a private server or behind a trusted reverse proxy.

The operator needs to:

1. point tools/clients at one endpoint;
2. add built-in or custom providers without code edits;
3. manage multiple provider accounts;
4. choose deterministic routing policy;
5. keep working through provider limits/failures;
6. see exactly which provider/account/model handled traffic;
7. preserve prompt-cache economics;
8. tune token-saving features;
9. operate/upgrade/restore the router without another control service.

## 3. Product principles

1. **Correctness before optimization.**
2. **Native transport before translation.**
3. **Built-in provider support is a product contract; shared adapters are an implementation detail.**
4. **Routing state is explicit and bounded.**
5. **SQLite is durable state; RuntimeSnapshot/RuntimeState serve requests.**
6. **Usage and quota are product features, not debug extras.**
7. **Prompt-cache preservation is release-blocking.**
8. **Operator UI is an instrument panel, not a marketing surface.**
9. **Standalone deployment must remain simple.**
10. **A feature is not "supported" merely because a provider name appears in a list.**

## 4. Product scope summary

### 4.1 In scope

- OpenAI Chat Completions ingress;
- OpenAI Responses + compact;
- Anthropic Messages + count tokens;
- Gemini-compatible LLM ingress;
- Ollama-compatible chat ingress;
- System One/Jev;
- streaming + non-streaming;
- built-in 80-provider catalog;
- Generic Provider;
- multiple provider connections/accounts;
- API key/OAuth/PAT/cookie/no-auth credentials;
- model discovery, aliases, custom/disabled models, pricing;
- fill-first, RR, sticky RR, provider strategies;
- fallback, quota/cooldown/health routing;
- full Combo;
- Fusion;
- capability-aware Combo routing/capacity adapter;
- proxy pools/global proxy/no-proxy;
- RTK, Caveman, Ponytail, Headroom, PXPIPE;
- prompt-cache anchors/accounting;
- API-key and dashboard auth;
- Usage, Quota, request details, topology, console logs;
- backup/restore;
- static Routeweft UI following `UI_STYLE.md`.

### 4.2 Explicitly out of scope

- standalone image generation;
- standalone video generation;
- TTS/STT/speech product APIs;
- embeddings product APIs;
- media-provider management;
- RAG/vector database;
- general agent orchestration;
- SaaS multi-tenancy;
- Kubernetes/microservices;
- multi-region HA;
- Redis/PostgreSQL for the initial single-instance product;
- LiteRouter database compatibility;
- built-in tunnel/Tailscale lifecycle management;
- cloud sync;
- enterprise SAML/OIDC.

Multimodal image/audio/file content remains valid when embedded in retained LLM protocols and supported by the selected provider/model.

## 5. Public inference and compatibility contract

### PRD-API-001 — primary public routes

Routeweft must expose:

```text
POST /v1/chat/completions
POST /v1/responses
POST /v1/responses/compact
POST /v1/messages
POST /v1/messages/count_tokens
POST /v1/systemone

GET  /v1
GET  /v1/models
GET  /v1/models/{provider}/{model}
GET  /v1/models/info
```

### PRD-API-002 — retained compatibility ingress

```text
POST /v1/api/chat
GET  /v1beta/models
POST /v1beta/models/{model}:generateContent
POST /v1beta/models/{model}:streamGenerateContent
```

These are LLM compatibility routes. They do not authorize standalone media product APIs.

### PRD-API-003 — client aliases

Retain compatibility behavior equivalent to:

- `/responses` -> Responses;
- `/codex/:path*` -> Responses compatibility;
- historical double-prefix `/v1/v1[/...]` where existing clients depend on it.

Routeweft may implement aliases directly rather than via framework rewrites.

### PRD-API-004 — authentication forms

Inference authentication must understand the current supported client forms, including:

- Bearer authorization;
- Anthropic-style `x-api-key`;
- Gemini-compatible API key forms where required by the compatibility route.

Provider credentials are not the same thing as client Routeweft API keys.

### PRD-API-005 — request/stream behavior

- streaming and non-streaming;
- cancellation on client disconnect;
- exactly-once terminal semantics;
- finite retries/fallback;
- CORS/preflight needed by current clients;
- required response/content-type headers;
- configurable request-body limit at least equivalent to the current 128 MB long-context/base64 use case;
- h2c-capable clients must not fail merely because they attempt an HTTP/2 cleartext upgrade;
- unknown native-path JSON fields survive when safe.

### PRD-API-006 — Responses requirements

Support current behavior for:

- tools/custom tools;
- parallel tool calls;
- multi-turn state;
- compact mode;
- reasoning fields;
- stream terminal events;
- abort/cancel;
- native Responses providers when available.

### PRD-API-007 — Messages requirements

Support:

- tool-use/tool-result ordering;
- thinking/reasoning;
- prompt-cache control fields;
- count-tokens compatibility;
- native Messages providers when available.

## 6. Built-in provider product contract

### PRD-PROV-001 — first-class provider definition

Routeweft targets the 80 active built-in providers listed in `PROVIDER_BASELINE.md`.

A first-class provider:

- is present in the built-in catalog;
- has working connection/auth flows;
- has a correct wire adapter;
- preserves its model discovery/passthrough semantics;
- preserves usage/quota/reset behavior where applicable;
- preserves provider quirks required for successful inference;
- works with multiple accounts/routing/Combo/Usage;
- can be tested/managed from the UI.

A provider backed by a shared OpenAI/Anthropic/Responses adapter is still first-class.

"Native support" means Routeweft uses a provider's same-protocol endpoint when that endpoint is a declared built-in capability. Native capability is provider-specific, not inferred just because an upstream happens to be OpenAI-compatible. At minimum the first-party OpenAI built-in must expose both native Chat Completions and native Responses; Anthropic must expose native Messages; Gemini must expose native GenerateContent; providers explicitly marked Multi in the provider matrix must expose their listed native transports.

### PRD-PROV-002 — implementation model

Do not create 80 near-identical executors.

Provider implementation is composed from:

```text
ProviderSpec
 + protocol adapter
 + authentication module
 + optional model-discovery module
 + optional quota/usage module
 + optional provider hook/quirk module
```

### PRD-PROV-003 — hidden providers

`trae`, `devin-cli`, and `windsurf` remain hidden/non-product until an explicit requirements decision changes them.

### PRD-PROV-004 — release breadth

- **Daily-driver beta** may ship before all 80 are ready, but only configured/advertised-ready providers may be selected as production-ready.
- **Broad-provider GA** requires all 80 built-in provider rows implemented and green.
- No built-in row may disappear silently because porting it is difficult.

## 7. Generic Provider contract

### PRD-GEN-001 — creation

The operator can create a Generic Provider with:

- friendly name;
- unique provider/model prefix;
- base URL;
- one or more native transports:
  - Chat Completions;
  - Responses;
  - Messages.

### PRD-GEN-002 — connection/auth

Generic Provider supports multiple API-key connections/accounts.

A Generic Provider connection stores credentials separately from provider-node definition so multiple keys can share one node.

### PRD-GEN-003 — native transport selection

Given a Generic Provider advertising all three transports:

- Chat ingress uses `/chat/completions`;
- Responses ingress uses `/responses`;
- Messages ingress uses `/messages`.

Translation is used only when the selected provider does not advertise the source protocol.

### PRD-GEN-004 — validation/model discovery

The control plane must:

- validate URL shape;
- protect remote validation with SSRF policy;
- allow a trusted local operator to validate LAN/self-hosted nodes;
- try `/models` when available;
- allow a manual model ID fallback and minimal inference validation when model listing is absent;
- keep manual model configuration if discovery is unavailable.

### PRD-GEN-005 — forward compatibility

Native Generic Provider paths preserve unknown supported JSON fields where safe.

## 8. Provider connection and credential workflows

### PRD-AUTH-001

Connection modes may include:

- API key;
- access token/PAT;
- OAuth authorization code/PKCE;
- OAuth device flow;
- rotating refresh token;
- cookie/session;
- no-auth/free connection;
- dual auth.

### PRD-AUTH-002

Retain the user capability of current provider-specific helpers:

- Codex token import/bulk import;
- Cursor import/auto-import;
- GitLab PAT;
- Grok CLI bulk import;
- iFlow cookie;
- Kiro API-key/import/auto-import/CLI-proxy/social auth;
- Xiaomi Mimo API-key/auto-import;
- generic provider OAuth action flows.

Internal endpoint names may change.

### PRD-AUTH-003

Refresh correctness:

- singleflight per credential identity;
- new access token immediately updates RuntimeState;
- rotated refresh token durably commits before success is considered stable;
- failed refresh cannot destroy the last valid durable credential;
- distinct workspaces/accounts remain distinct identities.

## 9. Model catalog, capability and pricing requirements

### PRD-MODEL-001

Support:

- built-in seed models;
- live model discovery;
- passthrough model IDs;
- aliases;
- custom models;
- disabled models;
- upstream-model mapping;
- model capabilities;
- context window;
- service kind;
- thinking/reasoning options;
- pricing overrides;
- quota-family metadata.

### PRD-MODEL-002

Dynamic catalog failure must not delete a configured/known model.

### PRD-MODEL-003

Model IDs added to built-in providers after implementation begins are treated as product drift and require catalog update, not a code fork.

## 10. Routing requirements

### PRD-ROUTE-001 — multi-provider/account

Multiple providers and multiple accounts per provider are core.

Connections support enable/disable, naming, priority and reorder.

### PRD-ROUTE-002 — strategies

Support:

- fill-first;
- round-robin;
- sticky round-robin;
- global defaults;
- per-provider `providerStrategies`;
- per-provider sticky limits.

RR/sticky cursor mutation is hot in-memory state; no synchronous per-request durable write.

### PRD-ROUTE-003 — fallback

Fallback is bounded.

Retryable provider/account failures can advance to the next eligible candidate.

Non-retryable client errors must not create provider storms.

The current attempt chain excludes already-failed accounts/candidates as required to avoid loops.

### PRD-ROUTE-004 — quota/cooldown/health

Routing state distinguishes:

- available;
- exhausted;
- cooldown;
- unknown;
- error.

Unknown/error are not automatically exhausted.

Provider-specific precise reset timestamps override generic backoff when trusted.

### PRD-ROUTE-005 — proxying

Support:

- global outbound proxy;
- no-proxy list;
- proxy pools;
- per-connection pool binding;
- rotation for providers that support it;
- proxy connectivity test.

## 11. Combo product requirements

### PRD-COMBO-001 — CRUD/order

- create/edit/delete Combo;
- ordered members;
- drag/reorder and explicit order semantics;
- add/remove/deselect model;
- aliases usable as members.

### PRD-COMBO-002 — fallback/RR/sticky

- ordered fallback;
- round-robin;
- sticky RR;
- global Combo strategy;
- `comboStrategies` per Combo;
- `comboStickyRoundRobinLimit`.

### PRD-COMBO-003 — Fusion

Fusion must preserve:

- parallel panel fan-out;
- panel calls forced non-streaming;
- panel tools removed;
- prior tool history flattened to prose;
- configurable judge;
- first Combo model as default judge;
- minimum-panel quorum;
- straggler grace;
- hard panel timeout;
- 0 successes -> error;
- 1 success -> direct answer;
- 2+ successes -> judge synthesis;
- judge preserves original client streaming/tools behavior.

### PRD-COMBO-004 — capability routing

- detect hard capability needed by active LLM request;
- stably prioritize existing capable Combo members;
- only consult capacity-adapter pool when original candidates cannot satisfy capability;
- per-capability fallback or RR;
- enabled empty pool = no-op;
- final adapter model may be deselected;
- adapter target with smaller context trims old middle history first while preserving system/instructions and active user/media tail.

Initial visible capability adapters: vision and audio-input.

PDF/video state may exist for compatibility/future use but does not create standalone media APIs.

## 12. Prompt-efficiency and cache requirements

### PRD-XFORM-001 — token-saver master behavior

Retain:

- RTK;
- Headroom;
- Caveman;
- Ponytail;
- PXPIPE;
- client per-request bypass.

### PRD-XFORM-002 — normative order

```text
protocol/provider preparation
 -> RTK
 -> Headroom
 -> Caveman
 -> Ponytail
 -> PXPIPE
 -> prompt-cache anchoring
 -> final provider normalization/dispatch
```

### PRD-XFORM-003 — fail-open

A non-essential compression/transform failure must not convert an otherwise valid inference request into a failure unless the feature explicitly defines strict behavior.

### PRD-XFORM-004 — Headroom

Retain:

- enable;
- URL;
- timeout;
- optional user-message compression;
- extras;
- managed status/start/stop/restart when Routeweft owns integration;
- proxy behavior;
- diagnostics.

### PRD-XFORM-005 — PXPIPE

Retain:

- enable;
- auto-install policy where applicable;
- min-char threshold;
- timeout;
- transform behavior;
- health/status/start/stop/restart;
- logs;
- stats;
- request-detail diagnostics.

### PRD-CACHE-001 — prompt cache

Cache anchors are applied to the **final transformed body**.

Controlled fixtures must cover stable system/tool prefix, first turn, assistant turns, client markers, marker budget, deferred tools, thinking/redacted thinking, token savers, fallback and translated/native paths.

### PRD-CACHE-002

Usage records cache-read/cache-create token data when upstream provides it.

A release cannot knowingly reduce controlled prompt-cache reuse merely to save small router latency.

## 13. Usage, quota and observability

### PRD-OBS-001 — Usage

Preserve user-visible capability for:

- total requests;
- token totals;
- estimated cost;
- provider/model/account/API-key attribution;
- latency/TTFT where known;
- success/error;
- time-period selection;
- charts/history;
- provider topology/activity;
- request logs/details;
- cache tokens;
- routing/fallback decision;
- transform diagnostics.

### PRD-OBS-002 — request detail

Request details are bounded/redacted.

Do not store/render authorization, refresh token, cookies, admin password or provider secret.

### PRD-OBS-003 — live events

UI gets one live event channel for meaningful operational updates and falls back to query refresh.

### PRD-OBS-004 — telemetry durability

Normal Usage persistence is asynchronous/batched and bounded.

On pressure:

1. diagnostic detail sheds/coalesces first;
2. critical accounting gets short bounded backpressure;
3. emergency flush may run;
4. persistent DB failure marks degraded health;
5. any ultimately lost critical event increments visible monotonic lost-accounting state.

No unbounded queue.

### PRD-QUOTA-001

Quota page shows provider/account limit and reset state where available and can refresh without blocking normal inference.

Provider-specific actions currently exposed, such as Codex reset-credit operations, remain representable.

## 14. Control-plane UI requirements

The Routeweft information architecture follows the current LiteRouter operator workflow and Routeweft `UI_STYLE.md`.

### Operate

**Overview**
- runtime/version/health;
- configured/ready providers;
- API-key state;
- active/recent traffic;
- actionable errors.

**Endpoint & Key**
- base endpoint;
- transport examples;
- create/name/copy/pause/resume/revoke/delete Routeweft client keys;
- require-API-key setting.

**Providers**
- built-in catalog;
- add/edit/delete/enable/disable/reorder connection;
- multiple accounts;
- auth/import workflow;
- model discovery/manual model;
- model test/batch test;
- provider health/quota;
- aliases/custom/disabled/pricing;
- provider nodes/Generic Provider;
- proxy assignment.

### Route

**Combo & Capability Adapter**
- all Combo requirements in section 11.

**System One**
- configure/use System One/Jev provider/model and exercise the typed request workflow.

### Observe

**Usage**
- section 13 Usage contract.

**Quota Tracker**
- section 13 quota contract.

**Token Saver**
- master/feature toggles;
- Caveman/Ponytail levels;
- Headroom settings/status;
- PXPIPE settings/status/diagnostics.

### System

**Console Log**
- bounded service logs with filtering/refresh appropriate for operator troubleshooting.

**Settings**
- dashboard password/login;
- API-key requirement;
- routing defaults;
- provider/combo strategy defaults;
- quota visibility;
- observability settings;
- outbound proxy/no-proxy;
- provider compatibility/tool flags;
- database backup/restore;
- theme/preferences.

## 15. Settings product contract

Initial Routeweft defaults should intentionally mirror proven daily-driver behavior where applicable:

| Setting | Initial requirement/default |
|---|---|
| requireLogin | true |
| requireApiKey | true |
| stickyRoundRobinLimit | 3 |
| providerStrategies | empty map |
| quotaVisibility | empty map |
| comboStrategy | fallback |
| comboStickyRoundRobinLimit | 1 |
| comboStrategies | empty map |
| capacityAdapter vision/pdf/audioInput/videoInput | disabled, empty pools |
| enableObservability | false |
| observabilityMaxRecords | 1000 |
| observabilityBatchSize | 20 |
| observabilityFlushIntervalMs | 5000 |
| observabilityMaxJsonSize | 5 MB |
| outboundProxyEnabled | false |
| outboundProxyUrl/noProxy | empty |
| dnsToolEnabled/provider compatibility map | empty |
| rtkEnabled | true |
| headroomEnabled | false |
| headroom default URL | http://localhost:8787 |
| headroomCompressUserMessages | false |
| headroomTimeoutMs | 3000 |
| cavemanEnabled / level | false / full |
| ponytailEnabled / level | false / full |
| pxpipeEnabled | false |
| pxpipeAutoInstall | true unless deployment policy disables managed install |
| pxpipeMinChars | 25000 |
| pxpipeTimeoutMs | 15000 |

Routeweft does **not** inherit LiteRouter cloud/tunnel/removed-platform settings solely for compatibility.

## 16. Admin/control API product requirements

New Routeweft UI-facing API is versioned under `/admin/v1`.

Required logical resources:

- overview;
- providers/connections;
- provider nodes;
- models/aliases/pricing;
- combos;
- proxy pools;
- keys;
- settings;
- usage/request details;
- quota;
- token saver;
- System One;
- logs;
- backup/restore;
- live events.

Internal endpoint names do not need to match LiteRouter `/api/*`.

Mutations report an active configuration revision where relevant so the UI can distinguish "persisted" from "active".

## 17. Security and network requirements

### PRD-SEC-001

- hash admin password using an appropriate password KDF;
- store only hashed Routeweft client API keys or equivalent non-recoverable verification material when product UX permits;
- redact credentials from logs/details;
- keep provider secrets server-side.

### PRD-SEC-002 — trusted proxy

Client-supplied forwarded-IP headers are not trusted by default.

Only configured trusted proxy peers can influence the canonical client IP.

### PRD-SEC-003 — SSRF

Every operator-configurable outbound URL path—Generic Provider validation, provider test/discovery, Headroom/PXPIPE endpoints, proxy tests and redirects—uses one SSRF policy.

Remote operator requests block loopback/private/link-local/metadata targets unless explicitly allowed by policy.

Trusted local operator flows may support LAN nodes without disabling redirect revalidation.

### PRD-SEC-004

Hop-by-hop headers and sensitive client headers are not blindly forwarded upstream.

## 18. Persistence, backup and standalone operation

### PRD-DATA-001

SQLite WAL is the initial durable source of truth.

Normal successful inference does not synchronously read SQLite for already-compiled routing configuration.

### PRD-DATA-002

Routeweft has its own data directory/database and never shares writable state with LiteRouter.

### PRD-DATA-003

Backup/restore is product functionality:

- online-safe backup;
- schema/version/config-revision metadata;
- candidate integrity/migration/snapshot validation before restore activation;
- rollback copy;
- no overwrite of live DB before validation.

## 19. Performance and reliability budgets

### PRD-PERF-001

Engineering targets against local mock upstream after warmup:

- native router p50 <= 7 ms target;
- native router p95 <= 12 ms;
- no request-global selection mutex;
- stable 1/10/50/100 concurrent streams;
- post-burst Go service RSS target <= 64 MiB;
- server image goal <= 100 MiB, stretch <= 60 MiB.

Provider latency is reported separately.

### PRD-PERF-002

Static UI:

- one useful Overview read model;
- cached navigation target perceived <100 ms;
- local Overview API target <20 ms;
- no Next production server.

### PRD-REL-001

Readiness remains false until durable state is migrated/validated and RuntimeSnapshot compiles.

Liveness does not depend on any upstream provider.

### PRD-REL-002

Graceful shutdown:

- readiness false;
- stop new work;
- bounded stream drain;
- stop schedulers;
- flush accepted critical telemetry;
- safe SQLite close/checkpoint.

## 20. Release profiles

### 20.1 Development scaffold

May have no real providers. Must satisfy architecture/tooling gates.

### 20.2 Daily-driver beta

Requires:

- core public protocols used by deployment;
- all providers/accounts actually configured for the target daily driver;
- routing/Combo/token savers used by deployment;
- Usage/Quota/control plane;
- backup/restore;
- security suite;
- benchmark/rollback rehearsal.

Unsupported built-in provider rows may remain clearly non-ready during beta, but must stay tracked.

### 20.3 Broad-provider GA

Requires:

- all 80 built-in provider rows production-ready;
- no unexplained provider requirement gaps;
- Generic Provider;
- all public ingress compatibility contracts;
- complete UI workflows;
- operational/runbook gates.

## 21. Product drift policy

Until Routeweft tests become the sole oracle, LiteRouter reference changes are reviewed as product drift, not copied automatically.

A drift review asks:

1. Is this a new/changed behavior inside Routeweft product scope?
2. Does it affect a built-in provider, protocol, Combo, cache, Usage/Quota or operator workflow?
3. Should Routeweft adopt, explicitly reject, or defer it?

Every accepted product change updates PRD/provider requirements/tests before implementation.

## 22. Implementation-start gate

Codex implementation is allowed only when all are true on Routeweft `main`:

- PRD status is **Implementation-ready product requirements**;
- `PROVIDER_BASELINE.md` has exactly 80 active built-in rows and no `TBD` disposition;
- hidden provider decisions are explicit;
- Generic Provider contract is explicit;
- public API/alias/auth compatibility is explicit;
- all current major dashboard workflows are classified;
- Combo/Fusion/capability adapters are explicit;
- RTK/Caveman/Ponytail/Headroom/PXPIPE/cache are explicit;
- standalone/storage/security/backup decisions are explicit;
- requirements traceability document has no product-area blocker.

Technical implementation details explicitly deferred in BDR (for example exact SQLite driver) do not make the product requirements incomplete.

## 23. Definition of initial product done

Routeweft initial GA is done when a fresh standalone installation can:

1. bootstrap/login securely;
2. create client API keys;
3. configure built-in and Generic Providers;
4. manage multiple provider accounts;
5. discover/map/test models;
6. route Chat/Responses/Messages/System One;
7. use Gemini/Ollama compatibility as defined;
8. use provider/account strategies and bounded fallback;
9. create/use Combo fallback/RR/sticky/Fusion/capability routing;
10. use RTK/Caveman/Ponytail/Headroom/PXPIPE;
11. preserve prompt-cache behavior on controlled fixtures;
12. observe Usage/Quota/details/logs;
13. manage proxy settings/pools;
14. back up/restore;
15. upgrade/rollback without LiteRouter.

## 24. Reference inventory

At the reference SHA used for this product freeze, LiteRouter exposes:

- 80 active built-in provider registry entries;
- 3 intentionally hidden provider entries;
- 95 internal API route files;
- 12 dashboard page routes;
- public LLM/model/SystemOne routes represented in section 5;
- provider-specific OAuth/import helpers represented in sections 6-8;
- current settings defaults represented in section 15.

Routeweft does not need to reproduce LiteRouter's internal 95-route control API. It must reproduce the classified user capabilities through Routeweft's own `/admin/v1` design.

## 25. Provenance

The product requirements were frozen against LiteRouter `main` at `2ffb7922954112b30425cd487d686758e519397e`.

After this requirements document is merged, Routeweft's own PRD/SPEC/BDR/provider matrix/tests are authoritative.
