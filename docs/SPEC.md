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

The static UI may be served by the Go binary or Caddy; the API contract must not depend on that choice.

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
- **Static asset serving:** embedded vs Caddy is selected in the UI shell PR. Admin API remains identical.
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
