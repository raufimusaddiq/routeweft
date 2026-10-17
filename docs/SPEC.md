# Routeweft Technical Specification

Status: **Foundation / normative**

## 1. System topology

Normal deployment is a modular monolith:

```text
                       +-------------------+
client / Caddy ------> | Routeweft Go      |
                       |                   |
                       | ingress           |
                       | auth              |
                       | routing           |
                       | protocols         |
                       | providers         |
                       | runtime snapshot  |
                       | runtime state     |
                       | admin API         |
                       | telemetry         |
                       +---------+---------+
                                 |
                       +---------+---------+
                       | SQLite WAL        |
                       +-------------------+

browser ----------------> static React UI
                              |
                              +--> /admin/v1/*
                              +--> /admin/v1/events
```

The static UI may be served by the Go binary or Caddy; the API contract must not depend on that choice. The reference image serves it from the Go binary (see §Open decisions, static asset serving).

Default standalone listen address: `:21128`.

## 2. Repository target layout

```text
cmd/
  routeweft/
    main.go

internal/
  app/
  ingress/
  auth/
  runtime/
    snapshot.go
    state.go
    compiler.go
    manager.go
  routing/
    plan.go
    accounts.go
    fallback.go
    cooldown.go
    combo.go
    fusion.go
    capability.go
  protocol/
    canonical/
    openai/
    anthropic/
    gemini/
    ollama/
    systemone/
  providers/
    registry/
    shared/
    <provider>/
  transport/
    pool.go
    proxy.go
    ssrf.go
  transforms/
    rtk/
    caveman/
    ponytail/
    headroom/
    pxpipe/
    promptcache/
  telemetry/
    queue.go
    batcher.go
    usage.go
    requests.go
  control/
    api/
    events/
  store/
    sqlite/
    migrations/
  security/
  buildinfo/

ui/
  src/
  public/

compat/
  fixtures/
  mockupstream/

bench/
  router/

docs/
```

Package names are responsibility-based, not ports of another project's file graph.

## 3. Startup lifecycle

```text
process start
  -> load environment
  -> open SQLite
  -> configure busy timeout / WAL
  -> run supported migrations
  -> integrity/version validation
  -> load durable configuration
  -> compile complete RuntimeSnapshot
  -> initialize RuntimeState
  -> initialize HTTP transport pools
  -> start telemetry worker
  -> start HTTP server
  -> readiness=true
```

Readiness remains false on:

- unsupported/corrupt schema;
- failed required migration;
- invalid durable configuration;
- failed snapshot compile;
- missing mandatory security bootstrap state.

No upstream provider needs to be healthy for process liveness.

## 4. Durable storage

SQLite is authoritative.

Recommended initial normalized tables:

```text
meta
settings
admin_users
api_keys
provider_connections
provider_nodes
proxy_pools
combos
combo_models
model_aliases
provider_models
custom_models
disabled_models
pricing_overrides
usage_events
usage_daily
request_details
credential_events
```

A small scoped KV table may exist only for provider-specific state that cannot reasonably be normalized yet. New core product state should not default to opaque JSON blobs merely for implementation convenience.

Required pragmas/behavior:

- WAL;
- foreign keys on;
- busy timeout;
- short write transactions;
- checkpoint policy measured under telemetry load;
- one application writer process per database;
- no transaction held across an upstream network call.

Client API keys store only a one-way digest and a short display prefix. The
compiled `APIKeyIndex` contains digests and non-secret metadata, never plaintext
keys. Provider model discovery is cached in `provider_models`; manual models,
aliases, and disabled-model decisions remain separately normalized.

## 5. RuntimeSnapshot

`RuntimeSnapshot` is immutable after publication.

Conceptual fields:

```go
type RuntimeSnapshot struct {
    Version        uint64
    ConfigRevision uint64

    Settings       *Settings
    APIKeys        *APIKeyIndex
    Models         *ModelIndex
    Aliases        *AliasIndex
    Providers      *ProviderIndex
    Connections    *ConnectionIndex
    Combos         *ComboIndex
    ProxyPools     *ProxyPoolIndex
    RoutePlans     *RoutePlanIndex
    Pricing        *PricingIndex
}
```

The exact representation may use maps/slices/tries as benchmarking justifies, but request handlers receive one coherent snapshot reference.

A successful request may not synchronously query SQLite to discover routing configuration.

## 6. RuntimeState

Mutable runtime state is deliberately separate:

- provider/account RR cursor;
- Combo RR cursor;
- sticky use counters;
- active request counts;
- circuit breaker state;
- transient cooldown;
- health observations;
- quota observations;
- transport pools;
- OAuth access-token state;
- singleflight refresh state.

Hot counters use atomics or narrow locks. There must be no process-global request-selection mutex.

Operational state that needs restart durability uses memory-first update plus asynchronous/batched persistence unless correctness explicitly requires synchronous durability.

## 7. Configuration mutation protocol

This is normative.

```text
control mutation lock
  -> load current logical config
  -> apply mutation to in-memory candidate
  -> validate complete candidate
  -> compile complete candidate RuntimeSnapshot
  -> begin SQLite transaction
  -> persist exactly the candidate mutation
  -> bump monotonic config_revision in meta
  -> commit
  -> atomically publish already-compiled snapshot
  -> emit config.changed
  -> return active snapshot/config revision
  -> release lock
```

Rules:

- all fallible validation/compilation happens before commit;
- if commit fails, discard candidate and retain old snapshot;
- snapshot publication after commit cannot depend on network/filesystem work;
- crash after commit/before publish is recovered at startup by compiling committed SQLite before readiness;
- IDs/timestamps required by snapshot are chosen before commit;
- direct external modification of the live SQLite DB is unsupported.

Inference readers never acquire this control mutation lock.

## 8. Request pipeline

Conceptual request path:

```text
accept
  -> request size / protocol validation
  -> client authentication
  -> source protocol detection
  -> snapshot.Load()
  -> resolve requested model / alias / Combo
  -> build RoutePlan
  -> detect required capabilities when relevant
  -> choose provider/account using RuntimeState
  -> acquire/refresh credentials if needed
  -> choose native / normalized / translated route mode
  -> apply request transforms in normative order
  -> apply final prompt-cache anchoring
  -> dispatch via pooled transport
  -> stream/translate response
  -> update RuntimeState immediately
  -> enqueue critical Usage
  -> enqueue optional request detail/diagnostics
```

Every retry/fallback attempt has an explicit attempt budget.

## 9. Route modes

Each request is classified:

### 9.1 Native passthrough

Use when source and target wire protocols match and no transform requires structural rewrite.

Objectives:

- preserve forward-compatible fields;
- minimize allocations;
- avoid whole-response buffering;
- copy stream bytes/chunks directly when terminal semantics allow.

### 9.2 Native normalized

Use when protocol matches but Routeweft must structurally modify the body for aliases, token savers, cache markers, provider quirks, or model mapping.

Decode once, transform once, encode once.

### 9.3 Translated

Use canonical intermediate types only when source/target protocols differ.

Translation must explicitly cover:

- messages/content;
- system/developer instructions;
- tool definitions;
- tool calls/results;
- reasoning/thinking fields;
- image/audio/file blocks accepted by retained LLM protocols;
- streaming event mapping;
- terminal event semantics;
- token Usage where available.

The Chat/Responses adapter maps both request and success-response directions,
including incremental SSE, parallel tool-call identity, custom-tool wrappers,
reasoning continuity, refusal and upstream usage details. Non-2xx upstream
responses retain their status/body and normal fallback classification.
Native paths bypass these converters. Translated responses are bounded to
32 MiB (body or total SSE input) with a 4 MiB SSE event/line bound. A truncated,
malformed or oversized translated stream emits a failure terminal rather than
claiming successful completion; client cancellation emits no synthetic terminal.
Failed translated streams penalize the selected account through normal runtime
cooldown classification; client cancellation does not penalize the account.

## 10. Protocol adapters

A protocol adapter owns wire behavior, not provider identity.

Conceptual interface:

```go
type ProtocolAdapter interface {
    Name() string
    DecodeRequest(ctx context.Context, req *http.Request) (*canonical.Request, error)
    EncodeRequest(ctx context.Context, req *canonical.Request, target Target) (*UpstreamRequest, error)
    StreamResponse(ctx context.Context, upstream *http.Response, dst io.Writer) (*Usage, error)
    DecodeResponse(ctx context.Context, upstream *http.Response) (*canonical.Response, error)
}
```

Native fast paths may bypass canonical objects when safe.

Initial protocol families:

- OpenAI Chat;
- OpenAI Responses;
- Anthropic Messages;
- Gemini GenerateContent;
- Ollama chat compatibility;
- System One.

### 10.1 System One native transport

`POST /v1/systemone` requires one JSON object: a non-blank string `model`,
presence of `state` (including null), and a non-null, non-array, non-empty
`questions` object. Question values and state remain provider-owned; the gateway
does not validate primitives or inject prompt-policy fields. Unchanged native
bodies preserve their bytes, including unknown fields; model resolution may
replace only the model value while preserving opaque JSON values.

TypeSafe seeds `jev-latest` and posts to its full configured typed endpoint.
A Generic node with sole transport `systemone` provides the same full-endpoint
behavior for custom models; typed and Chat transports cannot share that node.
Cloudflare seeds `@cf/cloudflare/clef` and `@cf/cloudflare/clef-flash`, serving
them only through System One. Its base URL is the accounts root; connection
`accountId` selects `/{accountId}/ai/run/{model}` for typed requests and
`/{accountId}/ai/v1/chat/completions` for Chat models. Missing/unsafe account IDs
or unsupported model/transport pairs are ineligible before dispatch.

Schema v5 adds non-secret `provider_connections.account_id`, default empty,
without changing existing secrets. Admin connection create/PATCH accepts
`accountId`; omitted PATCH preserves it, explicit empty clears it. Account IDs
permit ASCII letters, digits, hyphens and underscores only. Existing Cloudflare
connections need this metadata configured; nodes explicitly restricted to Chat
remain restricted (create a builtin node advertising System One to enable it).
No writable state is shared with LiteRouter. Older binaries cannot open a v5
database; rollback requires the pre-upgrade database backup.

Upstream response status/body and allowed headers remain opaque. A successful
`text/event-stream` response streams incrementally regardless of request-side
stream hints; Routeweft neither synthesizes events nor rewrites terminal markers.
Client cancellation stops upstream work and suppresses completed accounting.
Usage parsing recognizes native and Cloudflare `result.usage` token/cache
counters without unwrapping the client response. Streaming usage is accounted
once only after clean EOF; canceled/broken relays cannot report partial tokens.
Accounting scans skip opaque SSE lines over 4 MiB without changing relayed bytes.

## 11. Provider modules

Provider modules own:

- identity/name;
- base endpoint rules;
- protocol selection;
- auth strategy;
- credential refresh;
- safe header policy;
- model discovery;
- quota/reset parsing;
- error classification;
- provider-specific quirks.

They do not reimplement generic Chat/Responses/Messages translation if a shared protocol adapter suffices.

Conceptual metadata:

```go
type ProviderSpec struct {
    ID             string
    Protocol       string
    AuthKind       AuthKind
    DefaultBaseURL string
    Capabilities   ProviderCapabilities
    Quirks         ProviderQuirks
}
```

Provider-specific executable hooks should be small and explicit.

## 12. Client authentication

API keys are indexed in RuntimeSnapshot.

The request path:

1. extracts supported client key forms;
2. hashes/normalizes as designed by the storage implementation;
3. checks the in-memory key index;
4. never queries SQLite merely to validate an already-active key.

Dashboard sessions are a separate auth domain from inference API keys.

### 12.1 Key access policy

Each client key carries `access: {restricted, allow[]}` (stored in `api_keys`
`access_restricted`/`access_allow`, compiled into the key index in the
`RuntimeSnapshot`). Existing keys migrate as unrestricted.

- Unrestricted keys behave as before.
- A restricted key may request only names in `allow`: a Combo name, a model
  alias, or `provider/model`. A request also passes when the name resolves to an
  allowed `provider/model` (so allowing a model admits its aliases). Allowing an
  alias does not admit its canonical `provider/model`, and allowing a Combo does
  not admit its members directly. A restricted key with an empty list can use
  nothing.
- Enforcement is a single check at the top of dispatch, before model
  resolution, for Chat, Responses, Compact, Messages, System One, Gemini and
  Ollama chat; a denied request returns `403 model_not_allowed` whether or not
  the model exists (no existence oracle). `count_tokens` applies it when the body
  names a model.
- Capacity-adapter pool members: **not yet enforced in serving.** `PlanCombo`
  takes the caller's key and drops pool members outside its allowlist, but
  `PlanCombo`/`PlanComboRequest` have no production caller yet (Combo and adapter
  planning is not wired into dispatch). Whoever wires them must pass the
  request's key; until then restricted-key pool enforcement is covered only by
  unit tests of the planner.
- An unresolvable key (paused, revoked or reloaded between authentication and
  dispatch) is treated as restricted with an empty allowlist: it is denied,
  never widened.
- Discovery is access-aware: `/v1/models`, `/v1/models/{kind}`, `/v1/models/info`,
  `/v1/models/{provider}/{model}`, `/v1beta/models` and `/api/tags` list or
  return only permitted entries; hidden single lookups return 404.
- Admin API: `POST /admin/v1/keys` accepts `access`; `PATCH /admin/v1/keys/{id}`
  accepts `paused` and/or `access` and returns the stored key (same shape as
  create); `GET /admin/v1/keys` returns `access`.
  Entries are trimmed, de-duplicated, at most 1000 entries of 256 bytes (request
  bodies up to 512 KiB). A missing `restricted` is a 400, never read as false.
  Switching to unrestricted clears the stored list.

## 13. Account selection

Inputs:

- requested provider/model;
- provider strategy;
- enabled accounts;
- account priority;
- excluded accounts from current retry chain;
- cooldown/health/quota state;
- proxy availability.

Supported base strategies:

- fill-first;
- round-robin;
- sticky round-robin.

`providerStrategies` may override strategy/sticky behavior for one provider.

RR/sticky cursors are in RuntimeState and must not synchronously persist per request.

Provider RR/sticky cursors are provider-local, shared across its models and
aliases, matching pinned v1 `src/sse/services/auth.js` (`selectionState` is keyed
by provider ID). Transport eligibility is applied before account rotation and
the attempt cap. Matching native accounts form the first tier; accounts with an
implemented translation form the fallback tier. RR/sticky ordering applies
within each tier using the same provider-local cursor. Unsupported pairs do not
consume an attempt; both tiers share the same request wire budget.
Eligibility includes the request mode and wired translator: a compact request
cannot select a Chat account without a compact translator. Such accounts are
skipped before dispatch, preserving `503 no_eligible_accounts` when none remain
and preserving the final real upstream error when failover is exhausted.
Protocol-pair support belongs to the translator layer; snapshot resolution uses
the caller's policy. Each account reference is resolved once per request.

## 14. Cooldown and error classification

Provider adapters classify responses into categories such as:

- success;
- retry same account after delay;
- fallback account;
- fallback model/provider;
- terminal client error;
- auth refresh required;
- quota lock until reset.

Cooldown is visible immediately in RuntimeState.

Durable cooldown/reset state is persisted asynchronously when restart continuity matters.

Retry-After and known provider reset timestamps take precedence over generic backoff.

Native serving retries a retry-same-account outcome once after a one-second
default delay, unless a trusted reset/Retry-After supplies the delay. Cancellation
interrupts that wait. The request has at most ten wire attempts including retries;
after advancing to another account it never revisits an earlier account. No
retry occurs after upstream headers have been exposed to the client.

## 15. Combo

A compiled Combo contains:

- name;
- ordered route candidates;
- selected strategy;
- sticky limit;
- Fusion configuration;
- judge model;
- capability metadata.

### 15.1 Ordered fallback

Try candidates in configured order. Only fallback-classified failures advance.

### 15.2 Round-robin/sticky

Rotate starting candidate by Combo-local RuntimeState. Sticky limit controls requests per selected starting candidate.

### 15.3 Capability-aware reorder

Inspect only capabilities relevant to the active LLM request. If an existing Combo member satisfies required hard capabilities, stably move capable members ahead without dropping original fallback candidates.

### 15.4 Capacity adapter

If none of the original candidates satisfy a required hard capability:

- inspect configured capability pool;
- keep only capable adapter models;
- apply per-capability fallback/RR;
- prepend eligible adapter candidates;
- preserve original routes as fallback.

Enabled empty pool is a deliberate no-op.

Vision/audio-input are initially exposed in UI. pdf/video configuration may exist without creating standalone media endpoints.

When an adapter target has smaller context, history trimming:

- keeps system/developer instruction head;
- keeps the active user/media tail;
- drops older middle turns first;
- leaves headroom for response generation.

## 16. Fusion

Fusion flow:

```text
client request
  -> panel models in parallel
  -> panel calls forced non-streaming
  -> tools removed from panel request
  -> prior tool history flattened to prose
  -> collect successes with quorum/grace/hard-timeout
      0 -> error
      1 -> return successful panel directly
      2+ -> append anonymous source answers for judge
           -> judge model
           -> preserve client's stream/tools behavior on judge call
```

Default judge is first Combo model when no explicit judge is configured.

Fusion limits must cap:

- panel fan-out;
- panel timeout;
- response bytes collected per panel;
- total judge context;
- concurrent Fusion requests if resource testing requires it.

## 17. Token saver pipeline

Normative order on the final provider request body:

1. RTK;
2. Headroom;
3. Caveman;
4. Ponytail;
5. PXPIPE;
6. prompt-cache anchoring;
7. final provider normalization/dispatch.

A client opt-out can disable token savers as defined by the ingress contract.

Saver settings compile with each immutable RuntimeSnapshot. Every wire attempt
starts from its own protocol/provider-prepared body, including same-account
retry and account fallback. The shared dispatch boundary runs savers before
cache anchoring; a retry never reuses another attempt's transformed body.
Both `x-9router-token-saver: off` and `x-literouter-token-saver: off` bypass
savers, not cache anchoring. Count-tokens compatibility estimates the transformed
Messages body without counting cache-control metadata as prompt text.

Native normalization edits known text slots without replacing opaque media,
tool identities, thinking/signatures or unknown fields. Unchanged bodies retain
their bytes. Typed System One state/questions have no prompt-policy field and
remain opaque; routing them through the shared boundary must not invent one.
Optional helper failures preserve the pre-step content and allow later savers
to run. Helper work inherits the client context and uses pooled, SSRF-guarded
clients; no credentials or client headers are forwarded to compression helpers.

Implementation status, not a helper-parity contract change: the initial R03
wiring uses the existing Routeweft Headroom/PXPIPE HTTP helper
interfaces, not a claim of pinned helper-wire parity. `headroomUrl` is the full
Routeweft compression endpoint. `pxpipeUrl` is an optional full helper endpoint,
empty by default; an enabled but unconfigured PXPIPE fails open. Both URL
settings use the common outbound policy; local endpoints require the explicit
trusted-local deployment option. Native Headroom `/v1/compress` compatibility
and PXPIPE image-rendering integration belong to R09, not System One R04.

### 17.1 RTK

Compress supported tool-result/context structures while preserving required semantics.

### 17.2 Headroom

External compression integration.

Requirements:

- configurable URL;
- timeout;
- optional user-message compression;
- fail-open when configured semantics say compression is optional;
- diagnostics exposed in request detail;
- managed health/start-stop behavior when Routeweft owns the process integration.

### 17.3 Caveman / Ponytail

Inject deterministic prompt policy at configured level.

Injection must not produce duplicate/conflicting blocks across translated protocols.

### 17.4 PXPIPE

Large/image-heavy context transform used by retained LLM request flows.

Requirements:

- enable flag;
- size threshold;
- timeout;
- fail-open behavior;
- request diagnostics;
- health/log/stats control API.

## 18. Prompt-cache preservation

Prompt-cache logic is applied **after** transforms so anchors represent the final outbound prefix.

For Claude-style caching, fixtures must cover:

- stable system/tools prefix;
- first turn with no assistant;
- subsequent assistant turns;
- client-supplied markers;
- marker budget;
- deferred/lazy tools;
- thinking/redacted-thinking;
- token savers individually and together;
- account fallback;
- Combo fallback;
- translated-to-Claude routes;
- OpenAI-compatible providers that preserve cache-control fields.

Cache-read and cache-create token data must flow into Usage when upstreams report it.

A release must not knowingly trade away controlled cache reuse for small router-latency gains.

## 19. OAuth and rotating credentials

Per identity:

- access-token state lives in RuntimeState;
- refresh uses singleflight;
- callers wait for one refresh rather than stampeding;
- a successful refresh updates in-memory credentials immediately;
- rotated refresh token is committed durably before success is considered stable;
- credential identity/dedup keys are provider-defined and must not collapse distinct accounts.

No credential is sent to browser code.

## 20. Transport pooling and proxying

Use shared `http.Transport` instances keyed by material transport configuration, such as:

- proxy;
- TLS/SNI policy;
- endpoint class;
- provider-specific connection constraints.

Do not create a transport/client per request.

Proxy support:

- global outbound proxy;
- no-proxy;
- per-connection proxy pool;
- no-auth provider rotation where configured.

Outbound URL validation and redirects use the same SSRF policy for inference, provider tests, model discovery, Headroom/PXPIPE, and other server-side fetches.

## 21. Telemetry

Normal request completion:

```text
request
  -> enqueue Usage event
  -> return/finish client path
          |
          v
      bounded queue
          |
          v
      batch writer
          |
          v
        SQLite
```

Event classes:

**critical**
- request completion/failure;
- provider/model/account attribution;
- token Usage/cached token Usage;
- cost inputs;
- route outcome.

**diagnostic**
- large request/response bodies;
- verbose transport details;
- transform diagnostics beyond core accounting.

Under queue pressure:

1. diagnostic events shed/coalesce first;
2. critical enqueue gets short bounded backpressure;
3. emergency/direct batch flush may execute on saturation;
4. persistent SQLite failure marks degraded health;
5. any lost critical accounting increments a visible monotonic counter.

Normal success path still does not synchronously write SQLite.

## 22. Admin/control API

All new UI-facing control APIs are versioned under:

```text
/admin/v1
```

Core resources:

- `/overview`
- `/providers`
- `/connections`
- `/provider-nodes`
- `/models`
- `/aliases`
- `/pricing`
- `/combos`
- `/proxy-pools`
- `/keys`
- `/settings`
- `/usage`
- `/requests`
- `/quota`
- `/token-saver`
- `/systemone`
- `/logs`
- `/events`
- `/backup`

Mutation responses return the persisted object and active config/snapshot revision where relevant.

Client API key management:

```text
GET    /admin/v1/keys            bounded list; digest and plaintext never returned
POST   /admin/v1/keys            create {name, access?}; returns the plaintext secret exactly once
PATCH  /admin/v1/keys/{id}       {paused?: bool, access?: {restricted: bool, allow: []}} applied atomically;
                                 access.restricted is required whenever access is sent
                                 (see §12.1); restricted:false takes an empty allow
DELETE /admin/v1/keys/{id}       revoke; the compiled key index drops it immediately
```

Dashboard password rotation uses `POST /admin/v1/auth/password` with the
current and replacement password. It is same-origin and session-gated; the
current password is reverified before changing the stored hash. All sessions
for that admin, including the caller, are invalidated after success and the
browser must sign in again.

Keys are created, paused, resumed, and revoked through the compiled
`RuntimeSnapshot` candidate protocol (BDR-007), so the request path never reads
SQLite to validate a key. `requireApiKey` is writable through
`PATCH /admin/v1/settings` and accepts only `"true"`/`"false"`; the compiled
default is `true` (PRD §15).

Settings updates reject malformed booleans, strategy names, positive integer
limits, strategy override maps, no-proxy string arrays, and global proxy URLs
disallowed by the shared SSRF policy before the configuration transaction
begins. `quotaVisibility` and
`providerCompatibility` are retained as JSON objects; their runtime semantics
remain unspecified by the current product contract.

List APIs have bounded pagination and stable sort.

Live events use SSE initially.

## 23. UI architecture

Frontend:

- React;
- TypeScript;
- Vite;
- TanStack Router;
- TanStack Query;
- Tailwind CSS;
- a small local store only for ephemeral/persisted UI preferences.

No production SSR/Next runtime.

Initial page data should come from purpose-built read models; the browser should not need to join many low-level endpoints to paint Overview.

Live SSE invalidates/updates targeted query keys.

## 24. Health and shutdown

Endpoints:

```text
GET /health/live
GET /health/ready
```

Graceful shutdown:

1. readiness false;
2. stop accepting new work;
3. drain active requests/streams for bounded deadline;
4. stop background schedulers;
5. flush accepted critical telemetry;
6. checkpoint/close SQLite safely;
7. exit.

## 25. Backup/restore

Backup uses Routeweft-owned tooling/API and produces metadata containing:

- schema version;
- Routeweft version/commit;
- created timestamp;
- config revision.

Restore:

1. authenticate operator/offline CLI;
2. stage candidate DB;
3. integrity check;
4. apply supported migrations to candidate;
5. compile RuntimeSnapshot from candidate;
6. stop mutations/telemetry writes;
7. preserve rollback copy;
8. activate;
9. publish candidate snapshot;
10. resume workers.

Never overwrite the live DB with an unvalidated file.

The online admin API uses:

```text
GET  /admin/v1/backup
POST /admin/v1/backup/restore/check
POST /admin/v1/backup/restore
```

`GET /backup` streams a ZIP containing exactly `routeweft.sqlite` and
`routeweft.sqlite.meta.json`. The upload endpoints accept that archive, cap
compressed and total uncompressed size at 8 GiB, and reject extra, duplicate,
non-regular, or mismatched entries. Restore-check validates integrity, schema,
metadata, supported migrations, and RuntimeSnapshot compilation on a private
copy; it never mutates live state.

Restore activation returns HTTP 202 only after candidate validation/staging.
The serving process then rejects new requests, drains existing work for at most
30 seconds, closes remaining connections if needed, flushes accepted telemetry,
and stops schedulers. It creates a non-overwriting
`routeweft.sqlite.pre-restore-<timestamp>` rollback copy before atomically
replacing the database. It recompiles process state, invalidates admin sessions,
and resumes serving in the same process. If activation cannot recover either
the candidate or rollback DB, readiness stays false and the process exits with
an error. Offline CLI restore remains check-only.

## 26. CLI surface

Planned operational commands:

```text
routeweft serve
routeweft version
routeweft doctor
routeweft migrate
routeweft backup --output <file>
routeweft restore --input <file> --check
```

CLI implementation must use the same store/migration/compiler packages as the server.

## 27. Configuration

Environment variable prefix: `ROUTEWEFT_`.

Expected base settings:

```text
ROUTEWEFT_LISTEN=:21128
ROUTEWEFT_DATA_DIR=/var/lib/routeweft
ROUTEWEFT_LOG_LEVEL=info
ROUTEWEFT_TRUSTED_PROXIES=
ROUTEWEFT_BOOTSTRAP_ADMIN_PASSWORD=
ROUTEWEFT_MAX_BODY_BYTES=134217728
ROUTEWEFT_CORS_ORIGINS=
```

The public request-body limit defaults to 128 MiB. Cross-origin requests are
denied unless an explicit comma-separated origin allowlist is configured.
Preflight requests do not require an API key.

Provider secrets are configured through the admin/control plane or import tooling, not committed config files.

The bootstrap admin password is only required to initialize a fresh database. Once an admin credential exists, an environment bootstrap value must not silently replace it.

## 28. Testing

### 28.1 Unit

- route compiler;
- error classification;
- account selection;
- RR/sticky;
- cooldown;
- Combo;
- Fusion;
- capacity adapter;
- transforms;
- protocol translations;
- prompt-cache anchors;
- redaction;
- SSRF.

### 28.2 Race/concurrency

Run Go race detector for:

- snapshot swaps;
- account selection;
- Combo RR;
- OAuth singleflight;
- telemetry queue;
- shutdown.

### 28.3 Contract fixtures

Deterministic mock upstream tests for every public protocol and route mode.

### 28.4 SQLite integration

Test:

- fresh migration;
- upgrade;
- failed mutation;
- concurrent telemetry/config writes;
- backup;
- restore;
- startup compile after crash-like committed state.

### 28.5 Browser

Critical workflows:

- login;
- API key;
- provider/connection;
- Combo/Fusion/capability adapter;
- Usage/Quota;
- Token Saver;
- backup;
- theme/mobile navigation.

## 29. CI gates

Once code exists, pull requests should run:

- `go test ./...`;
- `go test -race ./...` for relevant packages/full suite as runtime permits;
- Go vet/static checks;
- UI install lockfile check;
- UI typecheck;
- UI unit tests;
- UI production build;
- protocol fixture suite;
- container build;
- size budget;
- security fixture suite.

Hot-path PRs additionally run the router benchmark set and post before/after results.

## 30. Container/deployment

Target production image contains:

- Routeweft Go binary;
- static UI assets if embedded/served by binary;
- CA certificates/timezone data only as required;
- no Node runtime;
- no build toolchain.

State is mounted separately from the immutable image.

## 31. Deferred implementation decisions

These are constrained, not free-form:

- **SQLite Go driver:** choose in the first store PR using schema compatibility, race/concurrency behavior, static image complexity, performance, and image-size evidence. Pure-Go is preferred if it meets budgets; CGO is allowed if evidence is materially better.
- **Static asset serving:** decided (U01): the Go binary serves the built Vite output from a filesystem directory (`ROUTEWEFT_UI_DIR` / `--ui-dir`; the image sets `/ui`). Not embedded, so the UI can be rebuilt without recompiling Go. Admin API is unchanged; Caddy-served assets remain valid by leaving the variable unset.
- **Telemetry queue sizes:** fixed by load test in telemetry PR; durability semantics above are not negotiable.
- **Exact provider registry contents:** provider modules land in reviewable groups; shared protocol adapters come first.

None of these decisions may introduce PostgreSQL/Redis or change the standalone product boundary without a BDR update.


## 32. Built-in provider implementation contract

`docs/PROVIDER_BASELINE.md` is normative for provider breadth.

All 80 active rows are first-class product targets. The transport class in that matrix determines the default adapter family, while authentication/model-discovery/quota modules remain orthogonal.

Provider readiness is a real capability state; the UI must not imply production readiness for a provider whose connection/request path is not implemented.

Hidden reference providers remain absent unless PRD/BDR changes.

## 33. Generic Provider technical contract

Routeweft has one Generic Provider product model rather than separate hard-coded "OpenAI Compatible" and "Anthropic Compatible" products.

Conceptual durable node:

```go
type GenericProvider struct {
    ID         string
    Name       string
    Prefix     string
    BaseURL    string
    Transports []GenericTransport // chat_completions, responses, messages
}
```

Connections/credentials are separate records so a single node may have multiple accounts/API keys.

Native route resolution:

```text
chat_completions -> <base>/chat/completions
responses        -> <base>/responses
messages         -> <base>/messages
```

A route can be disabled independently.

Validation must use the central SSRF guard. Trusted-local operator policy may allow LAN destinations, but redirects are still re-resolved/revalidated.

Model validation order:

1. try a compatible `/models` endpoint;
2. if unavailable and operator supplied a model ID, run the smallest safe inference probe;
3. return an actionable auth/not-found/network result;
4. never require model discovery in order to save a manual model.

### 33.1 Provider/model control workflows (R06)

All actions use admin sessions; mutations also require the shared same-origin
policy. Public `/api/v1` aliases, inference auth and CORS are unchanged.

| Operation | Routeweft action |
| --- | --- |
| Unsaved Generic endpoint validation | `POST /admin/v1/provider-nodes/validate` with `baseUrl`, one `transports` entry, `secret.accessToken` (or explicit `authKind: none`), optional `modelId` |
| Saved connection validation | `POST /admin/v1/connections/{id}/test` with optional `modelId`, `transport`; an empty body remains supported |
| Across-account/provider tests | `POST /admin/v1/providers/test-batch` with `mode`, optional `providerId`, `modelId` |
| Availability/reset | `GET/POST /admin/v1/models/availability`; POST uses `action: clearCooldown`, `provider`, `model: __all` |
| Catalog sync/status | `POST/GET /admin/v1/models/catalog-sync` |
| Suggestions | `GET /admin/v1/providers/suggested-models?url=...&type=...` |
| Kilo free catalog | `GET /admin/v1/providers/kilo/free-models` |
| Resolved native node routes | `GET /admin/v1/provider-nodes/{id}/routes` |
| Custom model deletion | `DELETE /admin/v1/models` with `providerId` and `modelId` JSON |

Validation tries listing first, then probes the supplied manual ID, first listed
ID or first enabled known ID. Discovery alone never certifies inference. Listing
401/403 stops validation without an inference attempt. If listing is unavailable
and no model is known/supplied, the response asks for a manual ID. Saved
connection failures retain HTTP 502 so existing UI callers cannot report false
success; unsaved validation and aggregate batch results expose `ok/status/error`.
Discovery has its own 20-second deadline. Each inference probe gets a fresh
200-second first-token budget, bounded by caller cancellation/deadlines and a
ten-minute aggregate ceiling on both connection `test-models` and provider
`test-batch` requests. Native Chat, Responses, Messages, Gemini, Ollama and
System One requests stream; probes
stop at the first content, reasoning, tool-call or typed-answer event, then
close/cancel upstream work. Empty/error streams fail. A length-limited reasoning
response certifies reachability. Providers ignoring streaming may return native
JSON, subject to the same deadline and a 1 MiB response limit. Probes never use
user prompts or expose provider response bodies/secrets in results.

Probe `classification` is `reachable`, `slow`, `failed` or `canceled`.
Own deadline/network timeouts have status 504 (`slow`); connection/read/invalid
response failures have status 502 (`failed`); caller cancellation has status 408
(`canceled`). An upstream HTTP 504 remains `failed`, not an own timeout. Saved
connection tests return HTTP 504/408 for own timeout/cancellation, preserving the
HTTP 502 failure envelope otherwise. That envelope also includes the probe
`result`. `upstreamStatus` retains the actual upstream HTTP status (zero when no
response arrived), separately from the synthesized probe `status`. Aggregate
endpoints return HTTP 200 with per-model verdicts when complete; cancellation
returns 408, aggregate deadline expiry returns 504. Both retain completed and
interrupted verdicts in `results`; later models/accounts are not started.

`modelId` always records the tested ID, including its alias/qualified spelling.
`upstreamModelId` records the resolved model sent in the native request.
`servedModelId` records the model reported by a successful provider probe, only
when present; it is not inferred from the alias or retained on failures.
Account-local native catalog IDs take precedence over aliases, including native
slash-bearing passthrough IDs such as `openai/gpt-5.4`. A qualified spelling is
resolved only when its provider equals the selected account's provider. Otherwise
slash-bearing manual IDs remain literal. Aliases resolving to another provider
or a disabled target fail before sending credentials; disabled targets never
fall through as literal alias text. `latencyMs` measures the inference probe,
excluding discovery. This is control-result attribution, not R10 request logs.

Batch modes are `all`, `provider`, `oauth`, `free` (no-auth connections),
`apikey` (excluding typed nodes), `systemone`, and `compatible` (Generic nodes).
Disabled connections are excluded. Selection occurs before credential reads;
one unreadable credential produces a failed result without stopping other
accounts. At most 200 selected connections run sequentially, each with its own
deadline; cancellation stops subsequent work and cancels the active fetch.

Availability reads paginate with the normal `page/pageSize` bounds. Batch
validation and catalog sync are single-flight. Catalog sync retains a two-minute
aggregate deadline; inference batches allow up to ten minutes total, with fresh
per-model budgets within that remaining aggregate allowance. Both stop on
caller cancellation. Current
routing cooldowns are account-wide, represented by model `__all`. Availability
includes disabled accounts so operators can inspect retained cooldowns; batch
validation and catalog sync exclude disabled accounts. Reset clears
only that provider's account cooldowns in RuntimeState, not quotas, disabled
models, credentials or other providers. It does not claim per-model locks or
durable health history. Catalog sync is a single-flight, explicit operator
action over at most 200 enabled accounts, unioning provider catalogs through
compile/commit/publish. It never removes known, disabled or manual entries on
partial/empty/failing discovery. GET exposes running/last-attempt/error state;
that operational state is process-local, the catalog is durable.

Suggestions retain pinned `kenari`, `openrouter-free`, `opencode-free`,
`mimo-free`, and `airforce-free` filters. Suggestion fetch failures yield an
empty list, not a model deletion. Kilo keeps only `isFree: true`, caches for one
hour and serves stale data with a warning on refresh failure (502 when no cache
exists). Concurrent cold/expired Kilo requests share one refresh; waiting clients
can cancel independently. URL fetches share SSRF policy, reject redirects, inherit cancellation,
have a 10-second deadline and a 4 MiB response limit. No stored credentials are
sent to suggestion URLs.

Custom model POST accepts `kind` (`llm` or `systemone`, omitted keeps existing
provider-derived defaults), capabilities and context metadata. The legacy
`imageToText` capability is stored as `vision`, so the image-to-text view and
capability adapters share one vocabulary. Schema v7 adds
`provider_models.kind`, empty by default for existing rows. Explicit kind and
capabilities survive discovery and restart; intrinsic typed-provider rules
remain authoritative. The admin System One catalog applies the same per-model
classification as runtime, including mixed builtin and typed Generic nodes,
before pagination. Standalone media kinds remain excluded. Deletion removes
only custom entries; referenced models fail candidate compilation rather than
silently deleting aliases/Combos. A removed seed override exposes the built-in
seed again. Deleted catalog entries cannot revive from orphaned durable rows.
Main's v6 API-key access policies remain unchanged. Older binaries reject schema
v7; rollback requires the pre-upgrade backup.

## 34. Public compatibility details

Routeweft ingress must account for:

- aliases `/responses`, `/codex/:path*`, and historical `/v1/v1`;
- Bearer, Anthropic `x-api-key`, and Gemini-compatible key forms;
- CORS/preflight;
- configurable body limit >= 128 MB default compatibility target;
- h2c-capable clients;
- exact cancellation and terminal-event semantics;
- safe unknown-field preservation on native paths.

The Go server can implement these directly and should not mimic Next rewrite architecture.

### 34.1 Model discovery and historical aliases (R05)

Every retained `/v1` route also registers under `/v1/v1` and `/api/v1`,
including index, model discovery/detail/info, Chat, Responses/Compact, Messages,
count-tokens, Ollama chat and System One. Gemini model listing/generation also
register under `/api/v1beta`. Existing `/responses`, `/codex/:path*`,
`/messages` and `/messages/count_tokens` aliases remain. There is no invented
Gemini root endpoint, arbitrary rewrite or standalone-media alias. Direct
registration preserves method dispatch, cancellation and the shared saver/cache
boundary. This retains URLs clients could address directly in pinned v1 without
preserving its Next implementation (PRD-API-003, BDR-008/014).

Public catalog IDs are provider-qualified (`provider/model`, retaining embedded
slashes and tags); configured model aliases and Combo names stay unqualified.
These are identifiers, not separate copies of upstream models. Model-info
requires `?id=...` (400 when absent); an unknown, disabled or kind-mismatched ID
returns 404. A bare ambiguous upstream ID is not an info lookup. Info returns
the selected ID, display name, `kind`, owner, endpoint and known context/capability
metadata. Alias metadata comes from its enabled target. No request fetches an
upstream catalog or reads SQLite. Paused/revoked/invalid keys cannot read these
routes; `requireApiKey=false` explicitly permits unauthenticated reads.

`GET /v1/models` defaults to kind `llm`; `?kind=systemone` lists typed models.
TypeSafe, Cloudflare Clef/Clef Flash and sole-System-One custom nodes remain
typed, including their aliases. `GET /v1/models/{provider}/{model...}?kind=...`
returns the requested filtered list, matching pinned v1's query precedence.
Unknown/excluded kind queries return an empty list; unsupported single-segment
kind routes return 404. Info without a kind may resolve either retained family.

Non-media decision: `/v1/models/image-to-text` is a vision-capable LLM view;
`/v1/models/web` selects LLM entries with `webSearch` or `webFetch` capabilities.
The query spellings `imageToText`, `webSearch`, `webFetch` are also recognized.
These views keep kind `llm` and the Chat endpoint. They neither create separate
OCR/search/fetch products nor infer capabilities from model names. Typed models
use kind `systemone` and `/v1/systemone`. Standalone image/video/TTS/STT/embedding
catalog routes remain absent under PRD 4.2 / BDR-016. R06 persists explicit
custom service-kind metadata; broader metadata/default-pricing completeness
remains C06/P03.

`GET /api/tags` uses the enabled LLM catalog, aliases and Combos with the same
inference auth/CORS policy. Each Ollama manifest entry has identical `name` and
`model` IDs; tag suffixes are preserved, not silently given `:latest`. Unknown
artifact metadata uses size 0, empty digest/details and epoch modified time.
Routeweft does not claim local model files or return pinned v1's two dummy
manifests. Discovery alone does not certify an account or Combo as dispatch-ready.

Access limitation: model visibility currently enforces active client keys and
disabled model/alias state. Restricted-key provider/model/Combo grants do not
exist yet; persistence, compilation and uniform inference/discovery enforcement
remain #78/C02. R05 does not claim that independent access-policy gap is closed
or introduce a discovery-only grant mechanism.

## 35. Provider import/action capability

Provider-specific connection helpers are exposed as Routeweft admin actions, not necessarily copied route-for-route.

Required product actions are listed in PRD §8 and PROVIDER_BASELINE.

These actions must:

- be authenticated as admin control operations;
- never return raw stored refresh tokens to browser UI;
- validate imported identity before overwriting an existing connection;
- use the same durable credential update path as normal OAuth refresh.

## 36. Product readiness metadata

Provider registry entries should expose readiness separately from enabled/disabled user state.

Conceptual:

```go
type ProviderReadiness string
const (
    ProviderReady ProviderReadiness = "ready"
    ProviderExperimental ProviderReadiness = "experimental"
    ProviderUnavailable ProviderReadiness = "unavailable"
)
```

Broad-provider GA requires every active baseline provider to be `ready`.

This metadata is a Routeweft release/build capability, not a mutable per-user setting.

## 37. Requirements source-of-truth rule

Before implementation, agents must treat:

1. PRD;
2. PROVIDER_BASELINE;
3. SPEC;
4. BDR;
5. UI_STYLE/RUNBOOK/SPRINT_PLAN

as the approved product/technical hierarchy described in `AGENTS.md`.

A LiteRouter source observation that conflicts with an approved Routeweft product decision is not copied silently; it becomes a product-drift review.
