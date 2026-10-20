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
    hub/
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

Connection publication and startup compilation project provider settings from
the existing sealed credential payload into typed, immutable value fields.
`AccountProvider` carries node ID/kind/name/prefix and connection
name/auth-kind/identity/account ID/proxy-pool ID alongside the selected route.
The provider-settings allowlist is `baseUrl`, `azureEndpoint`, `apiVersion`,
`deployment`, `organization`, `region`, `workspaceId`, `deviceId`, `agentId`,
`email`, `userId`, `systemId`, `organizationId`, `defaultOrganizationId`, `uid`.
The raw `ProviderData` map, tokens, cookies and client secrets remain outside
snapshots. Settings are not returned to browser clients by connection CRUD.
No-auth connections may persist settings in the sealed payload without a token.
All connection edits preserve omitted provider fields, including token-only
rotations. Metadata-only edits also preserve existing credentials.
Credential imports/renewals that change projected settings also use the
compile/commit/publish protocol, so durable and serving metadata agree.
Token-only renewals commit under the same mutation gate without recompiling
configuration or advancing its revision. Queueing for this gate honors the
refresh persistence deadline; an install whose queueing deadline expires writes
and publishes nothing.
No upstream HTTP runs while the gate is held.
Startup with a configured credential key fails if a sealed settings payload
cannot be authenticated/decoded; it must not silently serve default settings.
A sealed payload without a credential key also fails serving startup. Keyless
backup structural validation and availability inventories explicitly omit sealed
settings and never publish serving snapshots. Serving reads
only the compiled projection; no per-request SQLite/decryption lookup is added.

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

Use the OpenAI Chat Completions hub (BDR-025) only when source/target protocols differ.

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
The Messages adapter (`internal/protocol/anthropic`) covers the same ground for
Messages clients and Messages providers: it maps both request and
success-response directions, incremental SSE, tool-call identity, thinking
blocks and finish reasons, and it composes with the Chat/Responses adapter
through the hub, so a Messages client reaches Chat and Responses providers and
a Chat or Responses client reaches Messages providers. A stream that ends before
its terminal event, or carries an upstream error event, ends with a failure
terminal in the client's own protocol (an error object then `[DONE]` for Chat,
an `error` event for Messages) and never with a normal completion. The rules
every leg shares (finish reasons, tool ids and names, reasoning text, usage
shape) live in `internal/protocol/concerns`. Deliberate divergence from the
pin: a translated Messages-to-Chat stream sets `stream_options.include_usage`
so the usage is observable, unless the client set `stream_options` itself, in
which case its value is forwarded. Native Chat is never modified. The Ollama adapter
(`internal/protocol/ollama`) is the provider side of the pin's two Ollama legs:
a Chat, Responses or Messages client reaches an Ollama provider through the
hub, and a stream is read as NDJSON (`concerns.ReadLines`), not SSE. Ollama is
never a client wire of the hub. What each leg carries, refuses or does not carry
is PARITY 6.4. Error bodies are not
converted: a non-2xx answer from a translated attempt is relayed with its
status and body as the provider sent it, so a Messages client served by a Chat
provider can receive an OpenAI-shaped error. Where
Routeweft deliberately differs from the pinned oracle, the difference and its
reason are recorded in `docs/PARITY.md` §6.3.

Native paths bypass these converters. Translated responses are bounded to
32 MiB (body or total SSE input) with a 4 MiB SSE event/line bound. A truncated,
malformed or oversized translated stream emits a failure terminal rather than
claiming successful completion; client cancellation emits no synthetic terminal.
Failed translated streams penalize the selected account through normal runtime
cooldown classification; client cancellation does not penalize the account.

### 9.4 Hub topology

Conversion is expressed as legs. A request leg converts a client-format body into a provider-format body; a response leg converts provider-format output (non-stream and stream) into the client format. A conversion from A to B uses a registered direct leg if there is one, otherwise the A→Chat leg followed by the Chat→B leg (request side), and B→Chat followed by Chat→A (response side). A format with no leg in one direction cannot be used in that direction; that is reported, never silently skipped.

`internal/protocol/hub` declares the implemented legs and answers whether a pair is convertible. Provider selection uses that answer, so a provider whose wire is reachable from a client wire is selected without a per-pair special case.

The format identifiers and the legs LiteRouter registers are read from the pinned oracle by `internal/parity` (`translator/formats.js`, `translator/request/`, `translator/response/`). They are not written in this document.

### 9.5 AWS EventStream framing

`concerns.ReadEventStream` validates the 12-byte prelude, big-endian frame and
header lengths, prelude CRC32/IEEE and message CRC32/IEEE before delivering a
frame. A frame is at least 16 bytes and at most 24 MiB; headers are at most
128 KiB; the stream shares the 32 MiB translated-response budget. Header types
0–9 are retained as distinct Go types (bool, int8, int16, int32, int64, `[]byte`,
string, `EventStreamTimestamp` for timestamp milliseconds, `[16]byte` UUID), so a
timestamp never reads as a long. Payload and byte-array values alias the
per-frame buffer; copy them to outlive the event. Duplicate names, empty names, invalid UTF-8 names/strings, unknown
header types and truncated headers fail. Payload bytes remain opaque: an empty
payload and JSON `null` are distinct, and JSON validation belongs to the adapter.

The consumer identifies terminal events and validates protocol-specific event
shapes. The reader continues through EOF to reject trailing malformed bytes;
even a valid terminal followed by corrupt framing is a failure. EOF without a
consumer-recognized terminal is a failure. Consumers must wait for the reader's
successful return before emitting a success terminal. They receive every valid
frame, including unknown event types; framing never silently skips an event.
The reader starts no goroutine and retains no session state. Callers must close
the source to interrupt blocked reads on cancellation, as the HTTP dispatcher
does when its request context is canceled. There is no replay/retry in the reader.

The direct-pair path and binary reader are tested together using a test-only
protocol through ingress and a scripted HTTP endpoint. No Kiro conversion leg
or provider capability is enabled by this framing foundation (#119 PR 4a).

## 10. Protocol adapters

A protocol adapter owns wire behavior, not provider identity.

Conceptual interface:

```go
type ProtocolAdapter interface {
    Name() string
    DecodeRequest(ctx context.Context, req *http.Request) (*openai.ChatRequest, error)
    EncodeRequest(ctx context.Context, req *openai.ChatRequest, target Target) (*UpstreamRequest, error)
    StreamResponse(ctx context.Context, upstream *http.Response, dst io.Writer) (*Usage, error)
    DecodeResponse(ctx context.Context, upstream *http.Response) (*openai.ChatResponse, error)
}
```

Native fast paths may bypass the hub when safe.

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
Client cancellation stops upstream work; it does not produce a completed record,
but the request is still recorded once as `client_abandoned` (§21.1).
Usage parsing recognizes native and Cloudflare `result.usage` token/cache
counters without unwrapping the client response. Streaming usage is accounted
once. For the retained native OpenAI Chat and Responses paths a canceled or broken
relay keeps whatever usage had arrived and is marked per §21.1; other families
report usage only after clean EOF.
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
    CredentialName string
    CredentialExpiryName string
    DefaultBaseURL string
    Capabilities   ProviderCapabilities
    Quirks         ProviderQuirks
}
```

`CredentialName` selects a sealed `Secret.ProviderData` field for inference;
`CredentialExpiryName` names its Unix-seconds expiry. Both must be declared
together, or both omitted. Omitted names use the primary access token/cookie.
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

- primary and provider-named credential state lives in the memory-first credential registry;
- refresh uses singleflight;
- callers wait for one refresh rather than stampeding;
- a successful refresh updates in-memory credentials immediately;
- rotated refresh token is committed durably before success is considered stable;
- credential identity/dedup keys are provider-defined and must not collapse distinct accounts.

Inference resolves the provider-declared credential through the memory-first
credential registry. A named token uses its own expiry with the registry's
60-second refresh lead, independently of the primary access token's expiry.
GitHub stores its OAuth token as the primary token and serves the separately
minted Copilot token. Onboarding and renewal record a ten-minute Copilot expiry
when `expires_at` is absent, unparseable, or nonpositive. Renewal preserves the
primary token's expiry; Copilot expiry never overwrites it. Legacy named tokens
with missing/invalid expiry renew once to acquire the bounded expiry. Repeated
requests reuse the token until the refresh lead is reached; renewal remains
singleflight with durable installation. No serving-path SQLite read is added.

No credential is sent to browser code.

### 19.1 Provider onboarding flows

Onboarding implements the flow shapes the reference declares per provider
(`src/lib/oauth/providers/*.js` `flowType`, plus the separate import, cookie, PAT
and API-key routes). One implementation exists per shape, not per provider:

| Flow | Meaning |
|---|---|
| `authorization_code_pkce` | browser sign-in with PKCE; the operator pastes a code or the redirect URL |
| `authorization_code` | browser sign-in without PKCE |
| `device_code` | the operator enters a code; Routeweft polls the token endpoint |
| `browser_token` | the browser returns a token, validated before storage |
| `import_token` | credential read from a local IDE install |
| `manual_token` | token, cookie, PAT or API key pasted by the operator |

The provider-to-flow mapping is `internal/providers/onboarding`; the parity gate
compares it against the reference so a provider cannot lose its sign-in mode.

`POST /admin/v1/oauth/{provider}/start` (body: account name, stable identity, and
optional provider `meta` such as GitLab's base URL/client id or Kiro's region)
mints a PKCE pair and a 256-bit state, and returns
`{state, authorizeUrl, completion, flowType, expiresAt}`. A device flow has its
own endpoint (`/device`, below). The verifier and any flow-private state never
leave the server.

`POST /admin/v1/oauth/{provider}/device` (same body) begins a device
authorization and returns `{state, flowType, userCode, verificationUrl,
intervalSeconds, expiresAt}`. The provider's device handle stays server-side on
the pending attempt: the browser holds only the opaque state, so a client cannot
substitute one attempt's handle for another.

`POST /admin/v1/oauth/{provider}/callback` (body: `state`, `input`) redeems the
state, runs the provider exchange, and creates the connection through the
credential store and registry. `input` is the bare code, Claude's `code#state`,
a redirect URL, or a callback URL carrying the credential.

`POST /admin/v1/oauth/{provider}/poll` (body: `state`) advances a device-code
attempt using the handle the server captured at `/device`. A pending
authorization answers `200 {status: "pending"}` and is never an error; approval
answers `200 {status: "connected", connection}`; denial and expiry end the
attempt. Concurrent approved polls cannot both save: one reserves the attempt,
and a save that persisted nothing releases it so the token already issued
upstream is not lost.

`POST /admin/v1/oauth/{provider}/import` (body: account name/identity, `input`,
optional `meta`) completes an import mode: a token bundle, a cookie, a PAT or an
API key. `POST /admin/v1/oauth/{provider}/bulk-import` accepts an array, a single
object, or `{accounts: [...]}` and imports each entry serially, for the providers
whose reference exposes a bulk route (codex, grok-cli); every other provider
answers `oauth_unsupported` rather than accepting a batch it would drop. Each item
reports its index, status and saved connection id, and no credential is echoed.

A provider may offer a browser or device flow *and* an import mode (codex, grok-cli, iflow, kiro, xiaomi-mimo, cursor); the
import endpoint resolves the import flow independently of the browser one.

The provider read model reports `flowType` and `signInModes` (`browser`,
`device`, `import`) from the same flow tables the endpoints use, so the dashboard
offers exactly the affordances that exist rather than guessing from the auth
kind.

The providers' registered redirect URIs never point at Routeweft, so the operator
hands the result back from the authenticated dashboard rather than receiving a
browser redirect. Providers whose client registration pins the callback keep
their exact registered host and port (Codex `localhost:1455/auth/callback`, xAI
`127.0.0.1:56121/callback`).

**Deliberate divergence: Google OAuth client.** The reference ships a shared
Google OAuth client id and secret in its own public tree
(`open-sse/providers/shared.js` `GOOGLE_OAUTH_CLIENT`, `ANTIGRAVITY_OAUTH_CLIENT`)
and uses it for `gemini-cli` and `antigravity`. Routeweft deliberately does not
republish it:

- it is a third-party credential and this repository is public, so copying it
  would put it in permanent public git history;
- using it would make Routeweft impersonate the reference's OAuth app, which is a
  different failure than a parity gap.

The operator supplies `ROUTEWEFT_GOOGLE_OAUTH_CLIENT_ID` and
`ROUTEWEFT_GOOGLE_OAUTH_CLIENT_SECRET`. There is no fallback: enabling either
Google-family provider without them fails that sign-in with a configuration
error, and refresh fails closed the same way. The same applies to iflow, whose
client the reference also ships. An internal test
(`TestNoThirdPartyProviderSecretInTree`) reads every secret the pinned reference
ships and fails if any appears in the tree, so the decision is enforced rather
than remembered.

Provider-specific connection fields the reference persists with a credential
(ChatGPT account id and plan, Google project id, Cursor machine id, Kiro profile
ARN and client registration, Kimi device id, Qoder machine id, Kilocode org id)
live inside the sealed credential payload, never in a snapshot or a browser
response.

Pending state is process-local, expires after 10 minutes, is bound to the admin session that started it and to the provider, and is single-use even when the exchange fails. A state that is unknown, expired, replayed or owned by another session returns the same `oauth_state_invalid` error, and a foreign session does not consume the owner's attempt. A device-authorization start the provider rate-limits (Kilo: too many pending authorizations) returns HTTP 429 `oauth_rate_limited`, distinct from `oauth_start_failed`. A device poll that loses the save reservation to a concurrent poll of the same approved attempt answers `{"status":"pending"}`; a state that is gone keeps `oauth_state_invalid`. Responses carry fixed messages only: no provider text, code, verifier or token.

Connections whose provider has a refresh grant are registered with a refresh
exchanger by the credential registry, so every registration path (sign-in,
import, manual entry, startup load) refreshes through the same singleflight and
durable commit. The grant table mirrors the reference refresh dispatcher; a
provider without a grant (a static API key, or a long-lived seat key such as
Windsurf or Zed) resolves no exchanger and is never failed by a refresh that
cannot succeed. A rotated refresh token is always committed before the refresh
counts as successful.

### 19.2 Device sign-in

Providers whose oracle flow is device authorization use `POST /admin/v1/oauth/{provider}/device` (body: account name and stable identity), which returns `{state, userCode, verificationUrl, intervalSeconds, expiresAt}`, and `POST /admin/v1/oauth/{provider}/poll` (body: `state`). The browser's only handle on an attempt is the opaque `state`; poll requests carry nothing else, and poll responses never echo the code. Kilo Code's protocol uses one code as both the device code and the user code, so that code is shown to the operator once, in the start response, because the operator must confirm it on the provider's page. A provider with a separate device code keeps it server-side.

`expiresAt` is the earlier of the 10-minute flow limit and the lifetime the provider reported for its code, so an attempt never outlives its code. Poll is session-bound, answers `{status:"pending"}` while the operator has not approved (throttled server-side so a fast client cannot drive upstream traffic), and on approval answers `{status:"connected", connection}`. Approval reserves the attempt so concurrent polls cannot save it twice, and ends it only once a connection has been persisted; a save that persisted nothing releases the attempt so the next poll can save the token already issued upstream. Denial and expiry end the attempt (`oauth_denied`, `oauth_expired`); a transient upstream fault answers `oauth_poll_failed` (502) and leaves the attempt open, and clients must retry on it. A provider offers either the browser flow or the device flow, never both. Kilo Code's approved token is a non-expiring bearer with no refresh token.

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

### 21.1 Native OpenAI Chat and Responses accounting

A successful native Chat or Responses request produces exactly one usage record. Usage is read by a tee on the relayed bytes (a streaming scanner for SSE, a bounded capture of the top-level `usage` object for a JSON body); the response is never buffered to be measured, and the client receives the upstream bytes unchanged.

The record carries the upstream's reported figures. Input, output and cache counts are never summed into a total or reconciled against `total_tokens`. `reasoning_tokens` is already part of the upstream's output count and is not recorded separately until #119 defines its representation.

A record says what is known and what is not:

- `usage_observed = 0` means the upstream never reported usage (an abandoned stream that ended before the usage chunk, a Chat stream without `stream_options.include_usage`, a body with no `usage`). The token columns then hold no measurement and readers must not treat them as measured zero; the admin API omits the counts for such a row and returns `usageUnobserved: true` (the live stream uses the same field, present only when true).
- `termination` is empty for a request that ran to its end, `client_abandoned` when the client went away mid-flight, and `upstream_truncated` when the upstream stream ended without its terminal event.

An abandoned stream is still recorded, because the upstream kept generating and was billed. Whatever usage had arrived is kept. The status stays what was sent to the client (a stream or body that began is `200`); the abandonment is carried by `termination`, never by an invented status.

**A terminated request is not a successful request.** A row with a non-empty `termination` is excluded from request counts (the `requests` in the `usage_daily` rollup, the usage summary totals and breakdowns, the hourly series and the overview request/failed counters) and from the error counts, so a client disconnect neither inflates successes nor raises the error rate. The tokens it carries are still summed, because they were billed. The summary reports the excluded rows as `totals.terminated`. `GET /admin/v1/usage` still lists every row.

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

Proxy pools (SPEC §20) are managed through the admin API. Member URLs may carry
credentials; responses always strip userinfo, query, and fragment, and an update
may send a stored member by `id` alone to keep its URL.

```text
GET    /admin/v1/proxy-pools            bounded list (existing read model)
GET    /admin/v1/proxy-pools/{id}
POST   /admin/v1/proxy-pools            {name, enabled?, strategy?, members: [{url, enabled?}]}; 201
PUT    /admin/v1/proxy-pools/{id}       replaces fields and members; member {id} alone, or with the redacted
                                         url echoed back, keeps its stored url; url with userinfo replaces it;
                                         url with an empty userinfo (http://@host:3128) removes credentials
DELETE /admin/v1/proxy-pools/{id}       409 proxy_pool_in_use while any connection is bound to it
POST   /admin/v1/proxy-pools/{id}/test  {memberId?}; per-member classified result
POST   /admin/v1/settings/proxy-test    {url?}; tests url, else the global outbound proxy (400 if disabled)
```

Mutations publish through `RefreshPoolBindings` and return `configRevision`.
Proxy hosts and test destinations pass the shared SSRF policy; redirects are not
followed. A connectivity failure is a 200 with `ok:false` and a `class`
(`blocked`, `dns`, `refused`, `timeout`, `tls`, `proxy_auth`, `proxy_error`,
`unreachable`); raw transport errors are never returned.

Headroom and PXPIPE are external HTTP helpers reached only at the configured
`headroomUrl`/`pxpipeUrl` (§17). Routeweft owns no helper process, so the control
API is read-only diagnostics; every route is session-gated and probes use the
shared SSRF-guarded client (trusted-local only when the deployment allows it)
and forward no credentials or client headers:

```text
GET /admin/v1/headroom/status   {enabled, url, timeoutMs, compressUserMessages, health}
GET /admin/v1/pxpipe/status     {enabled, url, autoInstall, minChars, timeoutMs, health, stats}
GET /admin/v1/pxpipe/health     live probe of the helper's /health
GET /admin/v1/pxpipe/logs       ?limit=1..500 (default 50); bounded in-process diagnostic log
GET /admin/v1/pxpipe/stats      ?limit=1..500; cumulative counters plus recent diagnostics
```

An unavailable helper is a 200 with `healthy:false` and a `class` (`not_configured`,
`blocked`, `bad_status` with `statusCode`, `dns`, `refused`, `timeout`, `tls`,
`unreachable`); raw transport errors are never returned. PXPIPE counters and logs
are process-local and fed by every compiled saver pipeline, so they survive
snapshot republication but reset on restart. Installing, starting, stopping or
restarting a helper, Headroom extras, and proxying the Headroom dashboard are
privileged host operations or arbitrary passthroughs; they are intentionally not
exposed over this API and need an explicit product/host decision.

Usage and console logs (SPEC §21):

```text
GET    /admin/v1/usage    bounded list; optional exact filters requestId, providerId,
                          modelId, connectionId, apiKeyId, status (100-599), and inclusive
                          startDate/endDate (RFC 3339); a malformed filter is 400
                          invalid_filter without echoing the value
DELETE /admin/v1/logs     clears the bounded console buffer -> {cleared}
```

`GET /admin/v1/usage/summary?period=` accepts `today`, `24h`, `7d` (default), `30d`,
`60d`, and `all`. `today` begins at the current UTC midnight, the calendar the daily
series uses; the others roll back from now. The hourly series below does not serve
`today` or `60d`.

`GET /admin/v1/usage/requests/{requestId}` returns `{requestId, events, detail}`:
every usage event stored for the id (oldest first, at most 50) and, when the
`request_details` table holds one, `detail: {routeMode, detail, createdAt}` with the
payload redacted again on read; otherwise `detail` is `null`. An id with neither is
`404 request_not_found`. Nothing in the gateway currently enqueues request details,
so `detail` is null until diagnostics capture is wired.

`GET /admin/v1/usage/stream` is a session-authenticated `text/event-stream` of
completed requests for the Usage requests tab. It accepts the usage list's filter
grammar (same validation, same `400 invalid_filter`) and delivers only events that
match it. Each frame is `event: request.completed` with `data` limited to
`requestId, providerId, modelId, connectionId, apiKeyId, status, durationMs,
inputTokens, outputTokens, createdAt`, plus `termination` and `usageUnobserved`
only when set (§21.1; for an unobserved request the token counts are 0 and
carry no measurement): a subset of what `GET /admin/v1/usage`
already returns, never a body, prompt or credential. A client-abandoned request is
recorded and published like any other (`termination: client_abandoned`, §21.1). It is a separate channel from `/admin/v1/events`
(see PARITY D09) so that per-request volume and per-subscriber filtering cannot evict
configuration-invalidation subscribers. Delivery contract:

- The publisher is the ingress completion hook. `Publish` never waits on a consumer:
  one short mutex, one non-blocking send per subscriber, and no work at all with no
  subscribers.
- Each subscriber has a 64-event buffer. A subscriber that fills it is removed, its
  stream ends, and `EventSource` reconnects; the UI reloads recorded history on every
  (re)connect and deduplicates by `requestId`. Events are never silently dropped
  for a connected client, and there is no `Last-Event-ID` replay.
- At most 32 concurrent streams; the next is `503 stream_limit` with `Retry-After`.
  Connects, disconnects and rejections are logged with the subscriber count.
- A `: heartbeat` comment every 25 s; every frame write has a 5 s deadline, so a
  client that stops reading frees its handler. A revoked or expired session ends the
  stream at the next event or heartbeat.

`GET /admin/v1/usage/hourly?period=7d` returns a flat, oldest-first hourly
series from `usage_events` under the same admin session policy and filter grammar
as the usage list. Supported periods are `24h`, `7d` (default), and `30d`, giving
24, 168, or 720 buckets. `all` and unknown periods return `400 invalid_period`.
There is no grouping parameter.

The response is `{period, timezone: "UTC", since, until, series}`. `since` is
inclusive and `until` is exclusive; both are RFC 3339 UTC hour boundaries. The
window ends at the start of the current UTC hour, excluding the incomplete hour.
Each series item is `{hour, requests, failed, tokens, queried, observed}`. `hour`
is the UTC start timestamp; `failed` counts non-2xx requests; `tokens` sums input
and output tokens without adding cache counters again. Failed requests contribute
their recorded tokens too.

Every hour in the period is present, including empty hours. `queried` indicates
that the hour intersects the requested date filters; it is false for hours wholly
excluded by those filters. Date filters retain their inclusive event semantics
and can partially cover a bucket. `observed` is true only when at least one event
matches all filters in that hour. Filled buckets have zero counts and
`observed: false`; these are not request observations. Missing hours outside
`[since, until)` were not queried. Database failures return an error rather than
a zero-filled success. This series describes persisted telemetry, not a guarantee
that no accounting was delayed or lost (SPEC section 21).

Each usage event carries `apiKeyId`: the opaque id of the authenticated client
key, resolved once at authorization, never the key or its prefix. It is empty
when `requireApiKey` is false. Time-to-first-token, cost, and a metrics scrape
endpoint are not yet defined by this specification and are not exposed.

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

All 81 active rows are first-class product targets. The transport class in that matrix determines the default adapter family, while authentication/model-discovery/quota modules remain orthogonal.

### 32.1 Quota readers and model seeds

Quota support is the reference dispatcher's handler table, not the registry's
`usage: true` advertisement: `iflow` has a handler without the flag and `qoder`
advertises without a handler, so the readable set is what the dispatcher lists.
Every reader takes the full connection credential (access token, API key, cookie
and the provider-specific fields) because providers differ in which they need. A
provider with no reader publishes `ErrNotConfigured`, which routing treats as
"not exhausted", never as healthy.

Model seeds are generated from the reference registry
(`compat/parity/generate-seeds.mjs`) rather than hand-maintained, so a provider
the reference serves models for cannot ship with an empty catalog and force the
operator to run discovery before its first request.

Seeds carry the reference's display name and, where the reference specifies one,
its context window. Most entries have no `contextLength` in the reference, so
their context window is zero rather than guessed; it is not required for routing.

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
JSON, subject to the same deadline and a 1 MiB response limit. Gemini's
unframed top-level JSON array is accepted. Non-streaming fallback generation is
capped at 32 output tokens because only first-token reachability is certified.
Probes never use
user prompts or expose provider response bodies/secrets in results.

Probe `classification` is `reachable`, `slow`, `failed` or `canceled`.
Own deadline/network timeouts have status 504 (`slow`); connection/read/invalid
response failures have status 502 (`failed`); caller cancellation has status 408
(`canceled`). Parent request/batch deadlines cancel the active probe with
`canceled`/408, not `slow`; only the model's own budget certifies slowness.
An upstream HTTP 504 remains `failed`, not an own timeout. Saved
connection tests return HTTP 504/408 for own timeout/cancellation with error code
`connection_test_timeout`/`connection_test_canceled`, preserving the HTTP 502
`connection_test_failed` envelope otherwise. That envelope also includes the probe
`result`. `upstreamStatus` retains the actual upstream HTTP status (zero when no
response arrived), separately from the synthesized probe `status`. Aggregate
endpoints return HTTP 200 with per-model verdicts when complete; cancellation
returns 408, aggregate deadline expiry returns 504. Both retain completed and
interrupted verdicts in `results`; later models/accounts are not started.
The Providers UI retains these partial verdicts alongside the interruption error,
can abort an in-flight model test and states the aggregate limit for multi-model
runs. SSE data lines within one event are joined
with a newline before decoding. A leading `data:` frame selects SSE framing even
when an upstream mislabels its Content-Type.
Probes reuse a handler-owned guarded transport with their 200-second header
budget, without mutating discovery's transport. Complete responses and HTTP/2
streams can reuse connections; aborting an unfinished HTTP/1 stream closes it.

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

Schema v8 is a data-only step: it rewrites a built-in `mimo-free` or `mmf` node
whose `base_url` is exactly the old default
`https://api.xiaomimimo.com/api/free-ai/openai/chat` to
`https://api.xiaomimimo.com`; the path now lives in the provider's Chat
`TransportEndpoints` entry, matching the pinned registry URL. Rows with any other
base URL (operator overrides) and every other provider are untouched. Older
binaries reject schema v8; rollback requires the pre-upgrade backup.

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
