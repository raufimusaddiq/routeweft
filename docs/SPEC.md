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
- A restricted key may request only what `allow` names: a Combo name, a model
  alias, a bare model ID, or `provider/model`. Entries are compiled into
  resolved identities once per snapshot (pin `src/sse/services/keyAccess.js`
  `getAllowSets`): a listed Combo is granted as a unit; every other listed name
  is resolved to the `provider/model` it routes to. A request passes when the
  Combo it names is listed, or when the model it resolves to is a granted
  identity, so `fast`, `openai/gpt-a` and any other spelling of one target are a
  single grant. Allowing a Combo does not admit its members for direct use. A
  name that resolves to nothing matches only by exact string. Matching is exact
  and case-sensitive (the router's own resolution is), and the check judges the
  request string byte-for-byte as it will be routed (no trimming). A restricted
  key with an empty list can use nothing.
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
  never widened. When `requireApiKey` is false a request with no or an unknown
  key is unrestricted, but a presented known restricted key keeps its
  restriction even if paused or disabled.
- A stored allowlist is re-validated on load; one that cannot be decoded or
  normalized stops the catalog load (fail closed) and never becomes a grant.
- Discovery is access-aware: `/v1/models`, `/v1/models/{kind}`, `/v1/models/info`,
  `/v1/models/{provider}/{model}`, `/v1beta/models` and `/api/tags` list or
  return only permitted entries; hidden single lookups return 404.
- Admin API: `POST /admin/v1/keys` accepts `access`; `PATCH /admin/v1/keys/{id}`
  accepts `paused` and/or `access` and returns the stored key (same shape as
  create); `GET /admin/v1/keys` returns `access`.
  Entries are trimmed, de-duplicated, at most 1000 entries of 256 bytes (request
  bodies up to 512 KiB). A missing `restricted` is a 400, never read as false.
  Switching to unrestricted clears the stored list.

### 12.2 Dashboard/admin authentication policy (#78 C03)

**Decision: admin login is immutable-true, with no local, token, header or
first-run bypass. This is a deliberate hardening of v1, not parity.** Every
oracle citation is `git show 1c3b9f47:<path>`.

What v1 does:

- `requireLogin` defaults true (`src/lib/db/repos/settingsRepo.js:30`) and is
  writable through `PATCH /api/settings`, which applies any body key and asks
  for no re-authentication (`src/app/api/settings/route.js:37-72`).
- `false` turns `isAuthenticated` into "anyone" (`src/dashboardGuard.js:169-174`),
  and that function gates the deny-by-default `/api/*` branch (`:220-226`), so
  providers, combos, usage, logs, outbound-proxy settings and OAuth routes
  answer anonymously. `/dashboard/*` is waved through (`:228-242`). With it on, an
  anonymous caller gets 401 JSON on `/api/*` and a redirect to `/login` on pages.
- A short list stays session-only whatever the setting (`:36-42`, `:208-213`):
  `/api/keys`, `/api/settings/database`, Cursor/Kiro auto-import. v1 therefore
  already limited how far login-off reached; the list is its own admission that
  some admin routes are too dangerous to open.
- Bypasses besides the setting: a CLI token equal to a machine-id derivation
  (`:7-20`, accepted at `:210`, `:223`), a loopback peer exemption for the LLM
  routes (`:143`, `:215-218`) and local-only routes (`:61-69`, `:148-153`), and
  `POST /api/auth/reset-password`, local-only, which clears the password hash
  (`src/app/api/auth/reset-password/route.js:4-13`).
- Bootstrap in v1 is a published default: no stored hash means the password is
  `INITIAL_PASSWORD` or `123456` (`src/app/api/auth/login/route.js:26-36`). A
  non-local login with the default is refused rather than given a session
  (`:43-64`), which v1 itself describes as closing a CVE-class chain where a
  remote caller logged in with the default and PATCHed `requireLogin` off.
- Public without a session: `/api/health`, `/api/locale`, login, logout,
  `/api/auth/status`, `/api/settings/require-login` (`dashboardGuard.js:23-30`),
  plus the LLM prefixes (key or local peer).

What Routeweft does, exhaustively:

| Surface | Credential | Notes |
| --- | --- | --- |
| `/admin/v1/*` except the three below | admin session cookie | `requireSession` (`internal/control/api/api.go:273`); non-GET also needs same-origin. Unauthenticated: 401 `unauthenticated`. |
| `POST /admin/v1/auth/login` | username + password | 8 failures per 5 minutes per client host (the socket address without its port), then 429 + `Retry-After`. 409 `no_admin` while no admin exists. |
| `POST /admin/v1/auth/logout` | none | Revokes the presented session if any; otherwise a 204 no-op. |
| `GET /admin/v1/auth/session` | cookie | 401 without a session. No `hasPassword`/`requireLogin`/bootstrap state is exposed. |
| `/v1/*`, `/v1beta/*`, `/api/v1*`, `/responses`, `/codex`, `/messages`, `/api/tags` | client API key | `authorize` (`internal/ingress/ingress.go:843`). Separate domain from admin sessions (SPEC 12). No loopback or forwarded-header exemption. |
| `GET /health/live`, `GET /health/ready` | none | Body is `ok\n` or a bare 503; no configuration, counts or versions. |
| Static UI (`ROUTEWEFT_UI_DIR`) | none | Built assets and `index.html` only; every data call it makes is an admin or inference call above. |
| Metrics, pprof, expvar, debug | n/a | Not served. |
| CLI token, reset-password, auth-status, require-login, setup/bootstrap endpoints | n/a | Do not exist. |
| `routeweft migrate/backup/restore` | filesystem access to the data dir | Local commands, not network listeners. Their trust boundary is host file permissions (RUNBOOK 3). |

Trusted-peer logic for **access** does not exist: no code path reads `X-Forwarded-For`,
`X-Real-Ip`, `Host` or `Origin` to grant access. Forwarded headers are read in one
place only, `internal/clientip`, to key the login throttle, and only from a peer
listed in `ROUTEWEFT_TRUSTED_PROXIES` (default none; 12.3). Behind an unlisted
reverse proxy every client still shares the proxy's bucket; that can lock operators
out for the window but never grants access.

`requireLogin` is in the defaults and in `GET /admin/v1/settings` as `true` for
shape compatibility only. It is not in the writable allowlist, nothing reads it
to authorize a request, and the compiler pins the compiled value to `true`, so a
stored or restored `false` (hand-edited database or backup) is neither honored
nor displayed as honored. The tested `PlanV1Migration` library refuses a
`requireLogin=false` source. Migration from v1 is manual and gradual, with no
migration command or admin route; history stays in v1 (`docs/V1_MIGRATION.md`).

Why not preserve the v1 behavior:

- Routeweft's admin plane is larger than the part v1 kept behind a session.
  `GET /admin/v1/backup` returns the whole database, `POST /admin/v1/backup/restore`
  replaces it, and the
  provider/connection/key routes mint and rewrite credentials. v1's
  `ALWAYS_PROTECTED` tiering would have to be rebuilt route by route, and one
  missed route is an anonymous takeover. There is no such tier today.
- The cost of supporting an open instance is that tiering, a settings flag with
  its own re-authentication and audit trail, a loopback/proxy trust model that
  PRD-SEC-002 forbids relying on, and tests for every admin route in both modes.
  An operator who truly wants no login on a trusted network can still reach that
  goal at the network layer (private listener, or a reverse proxy that injects
  the session); Routeweft will not hold the switch.
- An instance that needs no *client* keys keeps `requireApiKey=false`, which v1
  also has (`src/sse/handlers/chat.js:75`) and which is the supported
  trusted-network open mode. It opens inference only; it never opens `/admin`.

Bootstrap, and how it differs from v1:

- The first credential is supplied at process start by the operator:
  `ROUTEWEFT_BOOTSTRAP_ADMIN_USERNAME` and `ROUTEWEFT_BOOTSTRAP_ADMIN_PASSWORD`,
  both or neither (a lone one aborts startup, `internal/app/app.go:286`).
- It acts only while `admin_users` is empty, decided and written in one
  transaction (`internal/adminauth/store.go:41-62`), so it is single-use: later
  boots ignore the variables, and a wrong or attacker-chosen value on an existing
  database provisions nothing.
- It is not a network bypass. There is no endpoint that creates the first admin,
  so "loopback-only" and "time-limited" do not apply: the only actor is whoever
  controls the process environment, who already controls the host.
- With no credential configured the server runs locked: every admin route is 401,
  login is 409 `no_admin`, and no default password exists. Inference requires a
  key and keys are minted through admin, so a never-bootstrapped fresh instance has
  no client that can call it.
- Known limit: the only password rule is non-empty (`internal/adminauth/password.go:36`);
  strength is the operator's responsibility. The variables stay visible in the
  deployment environment until removed (RUNBOOK 3).
- Recovery: there is no reset endpoint. With the service stopped and write
  access to the SQLite file, delete the `admin_users` row and restart with
  bootstrap credentials. That needs host access, the same bar as v1's local
  reset, without being reachable over HTTP. (Restoring a backup does not help:
  it carries the same forgotten hash.) Existing sessions are in memory and die
  on restart.

Tests asserting each statement: `internal/app/c03_auth_test.go` (route-scan of
every admin pattern, bypass headers, closed recovery/status routes, fresh-install
lockout, stored `requireLogin=false`, health-only unauthenticated surface,
recovery path) and `TestStoredRequireLoginFalseCompilesToTrue` /
`TestSetSettingsRejectsNonWritableKeys` in `internal/runtime`.

### 12.3 Trusted proxies, client address and the login throttle (#283 C10, #281 C03)

Oracle: LiteRouter `refs/heads/main` at `4279093457458e168a2431604935f0145f2e76ec`
(paths below are relative to it; `9router-go` `internal/auth/session.go:453-494`
was read as a supplementary reference only).

**What the oracle does.** `custom-server.js:57-77` replaces the request's address
with one derived from the TCP socket: when the peer is loopback (`127.0.0.1`, `::1`,
`::ffff:127.0.0.1`, `:62`) and the request carries `X-Real-IP` or
`X-Forwarded-For`, the client is `x-real-ip` else the **leftmost** XFF entry
(`:65-66`); every other peer is keyed on its socket address. It deletes
client-supplied `x-9r-real-ip`, `x-forwarded-for`, `x-9r-via-proxy` and
`x-9r-peer-token` (`:67-70`) and stamps its own, with a per-process token proving
the stamp (`:14-15,75`; `src/lib/auth/trustedPeer.js:4-7`). The login throttle
(`src/lib/auth/loginLimiter.js:49-64`) uses that stamped address; only without the
wrapper does `TRUST_PROXY=true` make it read the leftmost XFF entry from any peer
(`:57-60`), and otherwise it uses one global `unknown` bucket (`:63`). The bucket
policy is five failures, then 30 s / 2 m / 10 m / 30 m locks, reset after an idle
hour (`:4-6`). Password policy: the server tests only that `newPassword` is
non-empty and, once a password exists, that the current one verifies
(`src/app/api/settings/route.js:45-62`); the profile page checks the confirmation
matches (`src/app/(dashboard)/dashboard/profile/page.js:169`). There is no minimum
length or complexity rule anywhere in the server or the UI.

**Routeweft contract.**

1. `ROUTEWEFT_TRUSTED_PROXIES` (flag `--trusted-proxies`, the flag wins) is a list of
   IP addresses and CIDR prefixes separated by commas or whitespace. It is empty by
   default: no peer is trusted (PRD-SEC-002). Startup fails before the data
   directory is touched when an entry is not an address or prefix, and for any `/0`
   prefix (it would trust every peer, which is what the setting exists to prevent).
   IPv4-mapped IPv6 is normalised so a peer matches either way. There is no
   `TRUST_PROXY=true` equivalent that trusts every peer.
2. The setting changes one thing: the key of the admin **login and password-change
   throttle** (`internal/clientip`). It does not exempt any request from
   authentication, is not read by inference or key authorization, and no client
   address is stored or logged.
3. The key is the normalised socket peer unless the peer is a trusted proxy. For a
   trusted peer, `X-Forwarded-For` (all header lines, comma-joined) is walked from
   the **right**: hops that are themselves trusted proxies are skipped and the first
   other hop is the client. If every hop is a trusted proxy the leftmost is used.
   A hop that is not an IP address (a port, a hostname, an empty entry) makes the
   request fall back to the proxy's own address. `X-Real-IP` is consulted only when
   the request has no `X-Forwarded-For` and carries exactly one valid value.
   `Forwarded` (RFC 7239) is never read. Entries to the left of the first
   untrusted hop are never read, so a client cannot forge its way into another
   bucket by prepending identities.
4. A peer that is not a trusted proxy is keyed on its own address whatever headers
   it sends, so it can neither escape its own lockout by rotating forwarded values
   nor charge failures to another client by naming it.
5. The bucket policy is unchanged by this section: eight failures in a fixed
   five-minute window, `429` with `Retry-After` (RUNBOOK 7).
6. Password policy matches the oracle: any non-empty new password is accepted, an
   empty one is refused (`invalid_password`), the current password is required, and
   the stored form is always the salted PBKDF2 hash of 12.2. There is no minimum
   length; a stricter rule would be a divergence needing its own D row (see D48-D50
   for the ones recorded here).
7. Logging. `ROUTEWEFT_LOG_LEVEL` (flag `--log-level`, the flag wins) takes `debug`,
   `info`, `warn` or `error`, case-insensitively; anything else stops startup before
   data is opened. The oracle reads `LOG_LEVEL` the same four levels
   (`src/sse/utils/logger.js:3-10`) and gates its console lines on them
   (`:37,69-93`), falling back to `INFO` for an unknown value (`:10`). Its
   `LOG_USAGE_VERBOSE=1` only adds a per-usage console line
   (`open-sse/utils/usageTracking.js:8,414`); the authoritative per-request line,
   with the token counts, is printed at `INFO` whether or not that toggle is set
   (`open-sse/handlers/chatCore/streamingHandler.js:165`).
   Routeweft has no separate verbose toggle because its `request completed` record
   (`internal/app/request_log.go:17-45`) always carries tokens, cache tokens, status,
   account and client ids at `info` (`warn` for status >= 400), and the console log
   redacts credential-like keys. No level logs a credential.

Evidence: `internal/clientip` (unit table, including spoofing, chains, malformed
input, IPv6), `internal/control/api/trusted_proxy_test.go` and
`c09_trusted_peer_test.go` (handler level), `internal/app/c10_trusted_proxy_test.go`
(real listener, injected upstream), `cmd/routeweft/main_test.go` (env/flag mapping,
failure before data).

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

### 15.0 Nested Combos and failure

A Combo member with no provider names another Combo. It is served by that Combo's own strategy, rotation and capability handling when the outer Combo reaches it (an inner rotation moves only then). The name is checked when the Combos are saved: it must be a Combo (an alias or model of that name wins), a Combo may not reach itself, and nesting is at most 8 deep (the oracle accepts a cycle and recurses until the process fails; PARITY D15).

A Combo with a nested member is walked member by member: each member, in the order the Combo plans, is served as a request of its own, so an inner Combo answers with its own strategy, rotation, waits and failure answer, and a member that is a Fusion Combo answers as Fusion (§16). A member whose failure is not one the Combo falls past is the answer, written as it came.

When a Combo's models have all failed, the client gets the status of the first model that failed (503 if the last message says credentials are missing) and the message of the last failure, taken from the error body (`error.message`, a string `error`, or `message`), else the status text, else the status code. An inner Combo's exhausted answer is one failed model of its parent, read the same way. If a failed model's body carries a top-level `retryAfter` date, the earliest such time is added to the message as `(reset after 1h 2m 3s)` and sent as `Retry-After` in seconds (at least 1); a value that is not a date is ignored. A failure the loop treats as terminal is returned as it is. Before the Combo falls past a 502, 503 or 504 whose message names a short transient condition (`request not allowed`: 5 s; rate limit, quota, capacity, overloaded and similar: 2 s) it waits that long, also after the last model; a bare 5xx, or any longer cooldown, is not waited on. The wait ends with the client.

### 15.1 Ordered fallback

Try candidates in configured order. Only fallback-classified failures advance.

### 15.2 Round-robin/sticky

Rotate the starting candidate by Combo-local RuntimeState. Round-robin and sticky round-robin are one rotation whose step is the sticky limit (1 is plain round-robin); the position is re-reduced by the current list length, so a list that gains an adapter model keeps its place. Rotation runs over the whole list, adapter models included, before the capability reorder (PARITY 6.34).

### 15.3 Capability-aware reorder

Inspect only capabilities relevant to the active LLM request: input modalities (vision, pdf, audio-input, video-input) read from the current user turn only, so media in older turns never pins a Combo. After rotation, stably move members that satisfy every required hard capability ahead without dropping original fallback candidates. Member capabilities come from the capability table (docs/MODEL_CAPABILITIES.md) plus any a catalog entry declares.

### 15.4 Capacity adapter

If none of the original candidates satisfies every required hard capability:

- inspect the enabled capability pools, flattened in the order vision, pdf, audio-input, video-input and de-duplicated;
- keep only adapter models that satisfy every required capability and are not already candidates;
- keep only the adapter models the calling key may use directly (a restricted key is never routed to an unlisted pool model);
- add them ahead of the original candidates; they are then rotated and reordered with the rest of the list by the Combo's strategy;
- preserve original routes as fallback.

A single model (not a Combo) that lacks a required capability is rescued the same way, with itself as the last fallback; there the strategy is the pool strategy of the first required capability that has an enabled non-empty pool (round-robin only if that pool's roundRobin is set), with a sticky limit of 1. The pool's roundRobin flag has no effect on a Combo.

Enabled empty pool is a deliberate no-op.

Vision/audio-input are initially exposed in UI. pdf/video configuration may exist without creating standalone media endpoints.

When an adapter model (one the gateway added) is called, its history is fitted to that model's context window first (open-sse `stripHistoryForContext`), for every protocol that carries a history:

- keeps every system/developer message;
- keeps the active user turn (the run of messages after the last assistant turn), which carries the media;
- keeps the first six older messages while they fit, drops the rest of the older middle first, and drops kept older messages from the end while the total exceeds 80% of the window at four characters a token;
- uses a 200,000-token window when the capability table has none;
- never changes a Combo's own members or a client-chosen model.

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

Serving (PARITY 6.34, 6.35): a Fusion Combo may also be a member of an ordered Combo; it answers as Fusion when the walk reaches it, and a Fusion member that fails (no panel answer, a failed judge) is fallen past like any other.

Serving (PARITY 6.34): a Combo is Fusion when its `comboStrategies[name].fallbackStrategy` is `fusion`, or when its own flag is set and the entry names no plain strategy. `judgeModel` and `fusionTuning` come from the entry, else from the Combo. The panel and the judge are in-process requests to the same handler with the caller's credentials; a member is admitted by the Combo's grant. A panel answer counts when the call returned 2xx and has non-blank assistant text; answers reach the judge in panel order, anonymised as `[Source N]`. A single answer is returned by calling that model with the original request. Calls still running when the collection ends are cancelled, and a panel larger than 8, an answer over 1 MiB, and a ninth concurrent Fusion request are refused or dropped (these caps are Routeweft's; the oracle has none).

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

`headroomUrl` is the Headroom helper's base URL; `/v1/compress` is appended to it
(17.2). `pxpipeUrl` is the PXPIPE helper's endpoint, empty by default: an enabled
but unconfigured PXPIPE reports `not_installed` and changes nothing (17.4). Both URL
settings use the common outbound policy and may carry neither user info nor a
query; local endpoints require the explicit trusted-local deployment option.

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

Wire (PARITY 6.37). The helper is called with

```text
POST <headroomUrl>/v1/compress        Content-Type: application/json
{"messages": [{"role": "...", "content": "..."}, ...],
 "model": "<model the request is addressed to>",          (omitted when unknown)
 "config": {"compress_user_messages": true}}              (only when enabled)
```

and answers `{"messages": [...], "tokens_before": n, "tokens_after": n,
"tokens_saved": n}`; the statistics are optional. `messages` is the conversation's
text slots in OpenAI Chat shape: the system slots first as `system` turns, then each
turn with its role, a tool result as a `tool` turn. A reply is used only when it has
the same number of turns with the same roles and a text for each (a string, or an
array of strings and `{text}` parts joined by newlines). Unless
`headroomCompressUserMessages` is on, what the helper did to a user turn is
discarded. Headroom is not asked about a Gemini body, or about a Responses input
that holds anything but plain `message` items (tool calls and results, reasoning).
Any other outcome (no URL, a failed or slow call, an HTTP error, a reply without
`messages[]`, one that changes the shape) leaves the request as it was and is
recorded with the oracle's reason; a reply that reports tokens saved while the
messages shrank by under 5% is logged as such.

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

Host contract (PARITY 6.37). PXPIPE acts on the whole final body, after the text
savers, and only on an Anthropic Messages body (any other protocol reports
`unsupported_format`). In order: not enabled, no `pxpipeUrl` (`not_installed`), an
empty body, another protocol, a body shorter than `pxpipeMinChars` (default 25000,
counted in UTF-16 units of the compact JSON; `below_threshold`). Otherwise the
helper is called at `pxpipeUrl`:

```text
POST <pxpipeUrl>
{"body": "<base64 of the Anthropic body>", "model": "...", "options": {"minCompressChars": N}}
-> {"applied": bool, "reason": "...", "detail": ..., "body": "<base64 of a JSON object>",
    "info": {"compressedChars", "imageCount", "imageBytes", "imagePixels", "imageTokens", "baselineTokens"},
    "cache": {"ownsCacheControl": bool}}
```

`applied: false` leaves the body (reason `passthrough` when none is given); a call
over `pxpipeTimeoutMs` (default 15000) is `timeout`; a failed call, an unreadable or
oversize answer, or a body that is not a JSON object is `transform_error`. Applied,
the helper's body replaces the request body and the attempt reports the figures the
reference reports: characters before and after, characters imaged, image count and
bytes, estimated tokens before and after (the text left plus the images' own token
cost: from `imageTokens`, else `imagePixels / 750`, else 4761 per image), tokens and
percent saved. The prompt-cache anchors are placed after this step (18). The client's
token-saver opt-out skips PXPIPE with the other savers.

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
- a refresh that a newer install of a different credential (re-registration,
  import) lands during is discarded, and the installed credential is checked like
  any request's: served if valid, otherwise refreshed once from its own refresh
  token for the refresher and every waiter, never served expired (#250; the
  reference adopts the latest stored credential and then checks its expiry,
  `src/sse/services/tokenRefresh.js:175-197`);
- a re-registration whose credential equals the one held (the startup seed, the
  quota poll, autoping and the Codex credits reset all `Load` the provider) is
  not a new install: it applies changed metadata but keeps the generation, so a
  refresh in flight still commits its rotation and the spent refresh token is
  never used again. Every path that publishes a stored row (`Load`, the control
  API's create paths, an update of a connection not yet held) seeds it: a
  connection not held is added atomically with the check, and one held is read
  again under its install lock, so a row read before a refresh committed never
  reverts it (#252; the reference reads the latest stored row before quota
  work, `src/app/api/usage/[connectionId]/route.js:25-28`, and adopts a stored
  credential only when it is newer, `src/sse/services/tokenRefresh.js:177-191`);
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

`POST /admin/v1/oauth/{provider}/start` (body: optional account name, an identity
when the flow cannot resolve one, and optional provider `meta` such as GitLab's base URL/client id or Kiro's region)
mints a PKCE pair and a 256-bit state, and returns
`{state, authorizeUrl, completion, flowType, expiresAt}`. A device flow has its
own endpoint (`/device`, below). The verifier and any flow-private state never
leave the server.

A provider may offer a browser sign-in beside its device flow (Kiro's Google and
GitHub login beside its AWS SSO device code, #219): `/start` and `/callback`
drive the browser sign-in, `/device` and `/poll` the device flow. Where a flow
reads `meta.authMethod` it is a closed set (Kiro: `builder-id`/`idc` on
`/device`, defaulting to `builder-id`; `google`/`github` on `/start`, no default),
and any other value answers 400 `oauth_auth_method_invalid` before a provider is
contacted, so a sign-in never falls back to a method the operator did not choose.
A Reauthenticate (`connectionId`) uses the connection's own stored method and
answers 400 `oauth_reauth_unsupported` for a connection that browser sign-in did
not make.

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

`POST /admin/v1/oauth/{provider}/import` (body: optional account name/identity, `input`,
optional `meta`) completes an import mode: a token bundle, a cookie, a PAT or an
API key. `POST /admin/v1/oauth/{provider}/bulk-import` accepts an array, a single
object, or `{accounts: [...]}` and imports each entry serially, for the providers
whose reference exposes a bulk route (codex, grok-cli); every other provider
answers `oauth_unsupported` rather than accepting a batch it would drop. Each item
reports its index, status and saved connection id, and no credential is echoed.

A provider may offer a browser or device flow *and* an import mode (codex, grok-cli, iflow, kiro, xiaomi-mimo, cursor); the
import endpoint resolves the import flow independently of the browser one.

Specialized credential import (#218). `input` is at most 64 KiB (`import_too_large`,
413): a whole exported file (a Kiro IDE token or CLIProxyAPI export, a MiMo
`auth.json`, a full iFlow cookie string) fits; a pasted code stays at 4 KiB. A JSON
document (input starting `{` or `[`) may carry line breaks and tabs; any other
control character is `import_invalid`. A malformed paste or a missing field the
mode requires (Cursor's token length and machine-id shape, Kiro's refresh token or
CLIProxyAPI fields and Microsoft token endpoint, a MiMo key without `sk-`, a cookie
without `BXAuth`, a Grok account without `access_token`) is `400 import_invalid`
with fixed text that never quotes the input, and nothing reaches the provider;
Cursor without a machine id stays `oauth_meta_required`. A credential the provider
refuses (invalid, expired or revoked) is `502 oauth_exchange_failed` without the
provider's text. Outside Codex, an import of an account already stored on the
provider's built-in node follows the reference's insert
(`connectionsRepo.js:228-261`): an oauth credential with the same identity and the
same username, or none on both sides, updates that row in place (`outcome:
updated`), replacing its tokens and provider data whole and keeping its name,
priority, enabled flag and proxy pool; a MiMo API key for the same account merges
its provider data (`xiaomi-mimo/api-key/route.js:66-95`). Where the reference would
keep a second row instead (another credential mode, another username, an iFlow
cookie or Kiro API key, which never dedup), the built-in node's one-connection-per-
identity rule answers `409 identity_conflict` and the stored row is unchanged. The
same rules apply per bulk item; a bulk array element that is not an object fails
alone ("item is not an object", `bulk-import/route.js:62-64`), and an item's store
failure is reported with fixed text, never the store's own message. A credential
that had already expired is stored as given, as the reference stores it; the saved
connection and the bulk item carry `expired: true` and `refreshable` (whether a
refresh token came with it).

Onboarding golden path (#215). Every saved sign-in, import and bulk item says
what it did: the connection a callback, poll or import returns carries
`outcome` (`added`; `updated` when the Codex duplicate rule below, or a device
approval of a connected account (§19.2), put the credential on a stored
connection; `reauthenticated`), and a Codex one carries
`plan` from the sign-in's `chatgptPlanType` (a label, not a credential); each
bulk result item carries the same `outcome`. The provider read model reports
`bulkImport` exactly where the bulk endpoint accepts the provider.
`POST /start` takes an optional `connectionId`, which starts a Reauthenticate of
that connection of this provider (`404 connection_not_found` otherwise; device
flows do not take it). Its name and identity are the connection's own. The
callback saves only if the result is that connection's account: for Codex, a
ChatGPT account id on either side must be on both and equal, and an email on
both must be equal, with no account id and no two emails nothing is confirmed;
a bare access token pasted as the code is never accepted; for other providers
the identity must equal the connection's (one the provider does not report falls
back to it). Otherwise the callback answers `409 oauth_account_mismatch`, and a
connection deleted meanwhile `409 connection_not_found`; both have consumed the
attempt and saved nothing, so a Reauthenticate never adds a row or writes over
another. A match replaces the credential and keeps name, priority, enabled
state, proxy and identity, and clears the connection's health, as a duplicate
update does. The reference has no Reauthenticate: signing in again reaches the
same row through its duplicate rule, which the plain sign-in still follows.

The dashboard opens the sign-in window in the operator's click (browsers block a
window opened after the `/start` request), as `about:blank` with its opener cut so
the provider's page cannot navigate the dashboard, then sends it to the
`authorizeUrl` (only an `http`/`https` URL is opened or linked). A blocked window
leaves the link and the paste-back field, which remain the only way to finish:
there is no listener, because the reference's `start-proxy` listens on the
server's own `localhost:1455`, which a browser on another computer never
reaches. Without an opener the dashboard cannot close a window that has reached
the provider, so the result says the operator can close it. Bulk import parses
the paste in the browser first (an array, `{accounts}` or one object, as
`BulkImportCodexModal.js` does), describes a parse error without quoting the
input, sends nothing then, clears the paste once sent, and lists each item's
outcome or error. After a sign-in the result shows the account and plan and
offers a connection check and the account's quota read, and no credential
field.

Provider-aware browser journeys (#216). The sign-in screen is shared; each
provider's browser method keeps its own completion and preconditions, which the
read model states and the dashboard words per provider (`ui/src/journeys.ts`):

- A browser `signInMethod` carries `redirectUri` when the flow sends this
  server's public origin (Google, GitLab, Cline, ClinePass, iFlow, Kimchi,
  Xiaomi): the address the provider returns the browser to, which a GitLab OAuth
  application registers. With no public origin it carries
  `redirectUnconfigured` instead and the dashboard does not offer the start.
  `infersIdentity` marks a method that reads the account from the provider best
  effort (Gemini CLI, Antigravity, xAI, Cline, ClinePass, Kimchi, Zed, Xiaomi
  MiMo): `/start` does not require an identity for it, as the reference asks for
  none, and a result without one answers `502 oauth_identity_unresolved` before
  anything is saved, so the next attempt asks. Claude reports no account and
  still requires one.
- `input` may be a callback URL whose `token` query parameter carries the
  credential (Kimchi), read wherever `code` is absent, as the reference does; its
  `state`, when present, must still be this attempt's.
- xAI returns the browser to `http://127.0.0.1:56121/callback`, a port on the
  operator's own computer that Routeweft never listens on; the copied code xAI
  shows instead of redirecting is accepted bare.
- GitLab: the instance is `baseUrl` (gitlab.com when blank); a public OAuth
  application sends only its client id (PKCE), a confidential one also its
  secret; a start without a client id answers `400 oauth_client_id_required`.
  The personal access token import is the alternative without an application.
- Cline and ClinePass exchange a code that is not a token bundle as the
  reference reads the answer: camelCase fields under `data` or at the top level,
  the email under `data.userInfo`, an hour's lifetime when no `expiresAt` is
  given. The stored access token is the bare WorkOS JWT; the `workos:` prefix is
  added where it is sent.
- Zed and Xiaomi MiMo callbacks carry a token encrypted to a key minted for that
  attempt and held server-side, so a pasted address finishes only its attempt.
- A Gemini CLI or Antigravity result carries `project` (the Code Assist project
  its sign-in found) and `projectMissing`; requests refuse a connection without
  one, so the dashboard asks for a project id and saves only
  `secret.providerData.projectId` through `PATCH /connections/{id}`, which keeps
  the stored credential. When a project is found, Antigravity then calls
  `onboardUser` for the account's default tier (else `legacy-tier`) in the
  background, at most ten times five seconds apart, stopping when it reports
  done, as the reference does; it saves nothing and a failure leaves the
  connection as stored.

An import's `input` is one masked field, so a flow whose import needs several
secrets takes JSON in it rather than extra plain-text fields. Kiro: a bare string is
an API key (proven against the Amazon Q model catalog); a JSON object with
`refreshToken` (and `clientId`/`clientSecret`/`region` for an organization account)
is proven by refreshing it; a JSON object with a `token_endpoint` or `auth_method` is a CLIProxyAPI `external_idp` export. Xiaomi MiMo: a bare `sk-` key, the JSON body
with `apiKey`/`uid`/`baseUrl`, or the contents of MiMo Code's `auth.json`.
The owner approved account credentials in #181 (same as LiteRouter): import JSON
also accepts `mimoPassToken`, `mimoUserId`, and `mimoCUserId`, sealed alongside the
connection's key and never echoed in read models, logs, or errors. Quota reads
prefer the derived account session; if the session or account usage read fails,
the stored API key is tried. If neither path yields quota, return an error
observation with no fabricated window; failed reads retain the last good view.
Routeweft never returns a stored credential, and reads the host's home directory
only for MiMo Desktop: its discovery (BDR-028, #296) and the quota-read renewal
an operator turns on per connection (#299, below). Every other
reference `auto-import` route is replaced by pasting the same file contents
(docs/P04_ONBOARDING_AUDIT.md). Discovery needs two consents: the server started
with `ROUTEWEFT_ALLOW_HOST_CREDENTIAL_DISCOVERY=true` (default false), and
`consent: true` in the admin-session request
`POST /admin/v1/oauth/xiaomi-mimo/discover` (`{name?, identity?, consent}`).
Without the first it answers 403 `host_discovery_disabled`, without the second
400 `consent_required`, and nothing is opened. With both it reads the first
readable MiMoCode `auth.json` of the reference's candidates
(`~/.local/share/mimocode/auth.json`; then `%APPDATA%/Xiaomi MiMo/auth.json` on
Windows or `~/Library/Application Support/mimocode/auth.json` on macOS), checks
it with the reference's messages (missing file 404 `discovery_not_found` naming
the paths checked; invalid JSON, no `xiaomi.key`, no `sk-` prefix or a control
character 400 `import_invalid`; no uid and no identity given 400
`oauth_identity_unresolved`), then reads `passToken`, `userId` and `cUserId`
from the `.account.xiaomi.com` rows of a private copy of Desktop's Chromium
cookie store (`value` only; a locked, missing or unusable store imports the key
alone). The result goes through the Xiaomi import above, duplicate merge
included, and the answer is the saved connection plus
`discovery: {source, accountSession}`, never a key or session. A Xiaomi `/start`
with `desktopSession: true` (403 without the host's consent, 400 for another
provider) attaches the same session when its callback completes. The provider
read model reports `hostDiscovery: true` only where this endpoint would read.
Quota-read renewal (#299, BDR-028 amendment) is the reference's per-read
fallback to Desktop's store, as a per-connection opt-in:
`PUT /admin/v1/connections/{id}/desktop-session-renewal` (`{enabled, consent}`;
Xiaomi MiMo connections only, 400 `desktop_session_unsupported` otherwise)
turns it on only with both consents above (same 403/400) and off with neither,
reads nothing itself, and answers the connection view. The flag
(`mimoDesktopRenewal`) is sealed provider data that connection edits cannot
set or drop. While it is on and the host allows discovery, a quota read whose
connection has no sealed session, or whose sealed session fails the account
handshake, reads Desktop's session at most once per 30 minutes per connection,
seals it into the connection (with `mimoDesktopReadAt`), and reads the quota
with it. The interval is durable: each attempt first records
`mimoDesktopAttemptAt` in the connection (its own `ReviseSecret` step, after
the consent check), and only then does a second step, which re-checks consent,
the session and that this claim is still the latest, open the store. Failed,
unusable and interrupted reads therefore count, a restart does not reset the
interval, a refused claim records nothing, and a recorded time in the future
delays the next read. Connection edits cannot set any of the three fields. The flag and the session being replaced are checked on the connection
as stored at that moment, inside the same registry exclusion as the read and
the write (`Registry.ReviseSecret`) that every connection update, revocation
included, takes; so a revocation is ordered entirely before the store is opened
or entirely after the read is sealed. The write is a rotation (`RotateSecret`):
it never recreates a connection deleted during the read, and the credential
revision and validation status stay. An absent, locked or unchanged store, a
revoked flag, a deleted connection or a failed write uses the key path as
before. The connection read model reports
`desktopSessionRenewal`, `desktopSessionReadAt` and `desktopSessionAttemptAt`
for MiMo rows, and an
account-path quota read carries `metadata.desktopSessionReadAt`. A
refresh dispatches on the
stored credential shape, and a rotation is committed before it is published
(SPEC 19); an export that carries no lifetime has an unknown expiry, never `now`.

The provider read model reports `flowType` and `signInModes` (`browser`,
`device`, `import`) from the same flow tables the endpoints use, so the dashboard
offers exactly the affordances that exist rather than guessing from the auth
kind. `signInMethods` describes each mode separately: `mode`, the `authKind` it
stores, `resolvesIdentity`, the `metaKeys` that mode alone reads (GitLab's PAT
import reads `baseUrl`, not the OAuth application's client id and secret), a
browser mode's `completion`, and `loopbackRedirect` when the provider registered
a loopback callback (Codex `http://localhost:1455/auth/callback`, xAI
`http://127.0.0.1:56121/callback`), and `variant` when a browser mode is fixed to
one `authMethod` (Kiro lists `browser` twice, `google` and `github`). Connections
report their non-secret `signInMethod` (the stored `authMethod`) so the dashboard
offers Reauthenticate only where it can renew that method. A Kiro OAuth sign-in result carries `profileArnMissing`: such a connection is saved but cannot serve until it has a profile ARN, which the operator can name with `PATCH /admin/v1/connections/{id}` (`secret.providerData.profileArn`) or recover by Reauthenticate; the next refresh also looks it up. The dashboard offers each method, and each
manual credential mode the spec accepts, as its own choice showing only its own
fields (#214).

Account name and identity at sign-in (#214). The name is an optional nickname:
the reference opens a sign-in without asking for one (`OAuthModal.js`) and labels
the connection from what the provider reports. A blank name falls back to the
provider's display name and then to the identity, and the dashboard offers to set
a nickname once the sign-in succeeds. An identity is required up front only when
the flow does not declare `ResolvesIdentity`; a flow declares it only where a
genuine credential always yields one (iFlow's browser and cookie flows; GitLab's
browser and PAT flows; Cursor) or, with `IdentityOptional`, where the reference
stores a credential that names no account without one (Codex). An operator
identity sent anyway stays the fallback, and a sign-in that resolves no identity
answers `502 oauth_identity_unresolved` before anything is saved, except under
`IdentityOptional`.

Codex identity follows the reference at 9router `1c3b9f47` (#239); the identity
is what the reference stores as the connection's email, and the ChatGPT account
id is never used as one:

- Browser sign-in: the id token's `email`, else the access token's `email`,
  `preferred_username` or `sub` (`codex.js:48-66`, `providerHelpers.js:47-82`).
  The account id and plan come from the id token only.
- Token import: the access token's profile email, `email` or
  `preferred_username` (`import-token/route.js:29-66`); the label falls back to
  the operator's name, then the email, then `ChatGPT Access Token`.
- Bulk import: the item's `email`, else the `email` claim of its id token, or
  of its access token when it has none (`bulk-import/route.js:76-95`).
- A Codex credential that names none of these is stored with no identity,
  labelled `Account N` (N is one more than the provider's connections,
  `connectionsRepo.js:289-291`) unless named, and is never matched to another
  connection, so importing it twice stores two. The store keeps `identity` NULL
  for it (migration v11). The manual connection form still requires one, and an
  edit that sends a blank identity keeps the stored one.
- A connection stored before #239 with the ChatGPT account id as its identity
  is kept as is: v11 and v12 copy it unchanged and nothing rewrites it. An email
  never matches it, so signing in, importing or bulk-importing the same account
  again adds a connection beside it, which the operator may delete.

Codex duplicate handling follows the same pin (#241, `connectionsRepo.js:226-292`):

- A browser sign-in or bulk item with an email updates the existing Codex oauth
  connection with that email only when both carry the same non-empty
  `chatgptAccountId` (`:239-242`). A different account id, or none on either
  side, inserts a new connection with the same email; so does an item with no
  email (`:228`). When more than one stored connection matches, the oldest is
  updated (`all.find` over the table).
- A token import, and a raw access token pasted where the sign-in code goes
  (`[provider]/[action]/route.js:414-454`, stored without an exchange), are the
  reference's `access_token` connection: each always inserts and is never the
  existing side of a match (`:232,264`, `import-token/route.js:73`). Routeweft
  stores every Codex connection with auth kind `oauth`, so onboarding records
  the reference's `authType` inside the sealed credential: `access_token` for
  these two, `oauth` for a sign-in and for every bulk item, even one whose
  exported `providerSpecificData.authMethod` is `access_token`
  (`bulk-import/route.js:106-108`). Refresh, import and edits keep it, and no
  request can set it. A connection stored before it existed has none; if its
  `authMethod` is `access_token` it may be a token import or a bulk re-import of
  one, which stored identical payloads, so it is treated as a token import and
  never matched (a sign-in adds a row beside it). Nothing records which path
  created such a row, so this cannot be resolved for stored rows.
- The Codex bulk route reads camelCase keys only (`bulk-import/route.js:72-99`):
  an item without `accessToken` fails with `missing accessToken`, and a
  `snake_case` key is ignored. Its `providerSpecificData` is kept as given:
  strings verbatim, other values as their JSON text; the JWT backfills only a
  falsy account id or plan (`:78-91`).
- An update is `{...existing, ...incoming}` (`:283`). The connection keeps its
  id, proxy pool and identity. The access token and `providerSpecificData` are
  replaced, the map whole. A sign-in replaces the refresh token, id token and
  expiry too, clearing one its reply lacks (`codex.js:51-55`); a bulk item
  replaces each of those only when it has the key, clearing it when the value is
  empty or null, and keeps the rest. A sign-in keeps the name, priority and
  enabled flag (it sends none of them). A bulk item replaces the name only with
  its own `name`, sets enabled from `isActive`, true unless it says false
  (`bulk-import/route.js:103`), and replaces the priority with its own; a zero
  or null one sorts the connection after the rest, as `(priority || 999)` does
  (`:159`). A new bulk connection takes the item's non-zero priority
  (`:293-294`). Every update clears the connection's cooldown, the reference's
  health reset on `testStatus: "active"` (`:64-82`).
- The duplicate lookup and the write it decides are one step, as the
  reference's create transaction is (`:210`): the control API serializes them
  with every other connection write it makes (manual create and edit, move,
  proxy assignment, delete), so a concurrent manual save cannot pass its identity
  check twice and a Codex update cannot overwrite a concurrent edit or bring back
  a deleted connection. An update of a stored connection (a matching sign-in or
  bulk item, or an edit) is also one step with that connection's credential
  refresh: a refresh in flight commits first and the update merges onto its
  rotated credential, a refresh that would start waits for the update and then
  checks the credential again, refreshing it if the update left it expired or
  failed, and the write and its runtime publication happen together
  (`Registry.Update`). The reference checks every request's credential against
  the latest stored one and refreshes it when due (`tokenRefresh.js:175-196`,
  `chat.js:287`). The
  reference's update and refresh are each one read-merge-write transaction
  (`:210,266-286`; `:334-352`).
- Migration v12 drops `UNIQUE(node_id, identity)` and keeps it by trigger for
  every node except the built-in Codex node, so every other provider (built-in
  or Generic, including a Generic node that names `codex`) still refuses a second
  connection with an identity its node has. The manual connection form keeps that
  refusal for Codex too: it is Routeweft's own (the reference's manual create
  stores only API-key and cookie rows, `providers/route.js:179-181`), so a create,
  or an edit that changes the identity, onto one another Codex connection holds is
  refused, while an edit that keeps it succeeds. v12 is forward-only (RUNBOOK 14).

`POST /admin/v1/oauth/{provider}/cancel` (body: `state`) ends a pending browser
or device attempt so it can no longer complete, and answers
`{status: "cancelled"}`. It has the same session and provider binding as the
other steps, and an unknown, expired, replayed or foreign state answers
`oauth_state_invalid`. A device attempt that a poll is already saving answers
`409 oauth_completing` and is left to finish; that is not a confirmed save, since
a save that persists nothing releases the attempt again. The dashboard shows each
attempt as pending, then connected, expired, cancelled, closed or failed, and
reports only what the server answered, never what the clock or a missing answer
suggests: an approval just before the time limit may still be saving.

- The attempt stays tracked, and a device poll keeps running, until a server
  answer ends it. Passing `expiresAt` only changes the wording.
- Whether an error leaves the attempt open is decided per endpoint by the code
  table in `ui/src/signIn.ts` (`SIGN_IN_CODES`), which a test checks against the
  codes this file's callback, poll and cancel handlers can write. No answer (a
  network failure, or a response without an API error code) and an unknown code
  keep the attempt.
- A device save that persisted nothing (`connection_save_failed`,
  `oauth_identity_unresolved`, `invalid_auth_kind`, `oauth_exchange_failed` from
  the poll) releases the attempt on the server, so the dashboard keeps it pending
  with the server's message and offers Check again or Cancel. The same codes from
  a callback end the attempt, because the callback consumed its state first.
- An unreachable server or provider (no answer, `oauth_poll_failed`) is retried,
  then polling pauses for the operator to resume.
- A cancel counts only on `200`; any other failure keeps the attempt for a retry,
  and Close does not drop it. `oauth_completing` waits for the poll's result.
- `oauth_state_invalid` reads as closed: the server no longer holds the attempt,
  which does not say whether an earlier request saved it, so the dashboard points
  at the connection list. Only the provider's `oauth_expired` reads as expired
  with nothing saved.
- An attempt belongs to the provider it was started for; selecting another
  provider leaves it pending and polling.

A pinned loopback redirect names a port on the operator's own computer. Routeweft
never listens there: a remote server cannot receive the browser's own localhost,
so the dashboard says the page will not load and asks for the full address to be
pasted back.

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

Pending state is process-local, expires after 10 minutes, is bound to the admin session that started it and to the provider, and is single-use even when the exchange fails. A state that is unknown, expired, replayed or owned by another session returns the same `oauth_state_invalid` error, and a foreign session does not consume the owner's attempt. A pasted result whose own state names a different attempt answers `oauth_state_mismatch` and leaves the attempt open. A device-authorization start the provider rate-limits (Kilo: too many pending authorizations) returns HTTP 429 `oauth_rate_limited`, distinct from `oauth_start_failed`. A device poll that loses the save reservation to a concurrent poll of the same approved attempt answers `{"status":"pending"}`; a state that is gone keeps `oauth_state_invalid`. Responses carry fixed messages only: no provider text, code, verifier or token.

Connections whose provider has a refresh grant are registered with a refresh
exchanger by the credential registry, so every registration path (sign-in,
import, manual entry, startup load) refreshes through the same singleflight and
durable commit. The grant table mirrors the reference refresh dispatcher; a
provider without a grant (a static API key, or a long-lived seat key such as
Windsurf or Zed) resolves no exchanger and is never failed by a refresh that
cannot succeed. A rotated refresh token is always committed before the refresh
counts as successful.

### 19.2 Device sign-in

Providers whose oracle flow is device authorization use `POST /admin/v1/oauth/{provider}/device` (body: optional account name, and an identity when the flow cannot resolve one), which returns `{state, userCode, verificationUrl, intervalSeconds, expiresAt}`, and `POST /admin/v1/oauth/{provider}/poll` (body: `state`). The browser's only handle on an attempt is the opaque `state`; poll requests carry nothing else, and poll responses never echo the code. Kilo Code's protocol uses one code as both the device code and the user code, so that code is shown to the operator once, in the start response, because the operator must confirm it on the provider's page. A provider with a separate device code keeps it server-side.

`expiresAt` is the earlier of the 10-minute flow limit and the lifetime the provider reported for its code, so an attempt never outlives its code. Poll is session-bound, answers `{status:"pending"}` while the operator has not approved (throttled server-side so a fast client cannot drive upstream traffic), and on approval answers `{status:"connected", connection}`. Approval reserves the attempt so concurrent polls cannot save it twice, and ends it only once a connection has been persisted; a save that persisted nothing releases the attempt so the next poll can save the token already issued upstream. Denial and expiry end the attempt (`oauth_denied`, `oauth_expired`); a transient upstream fault answers `oauth_poll_failed` (502) and leaves the attempt open, and clients must retry on it. A provider offers either the browser flow or the device flow, never both. Kilo Code's approved token is a non-expiring bearer with no refresh token.

Device approval journeys (#217) for GitHub, Grok CLI, Kimi, Kilo Code, CodeBuddy CN and international, Kiro (AWS Builder ID or IAM Identity Center) and Qoder:

- Polling is bounded per attempt. The attempt keeps the provider's interval (at least 2 s) and spaces upstream polls by it whatever the browser does; every pending answer carries `intervalSeconds`. RFC 8628 `slow_down` lengthens it by 5 s, capped at 30 s as the reference does (`OAuthModal.js:184-186`) only when the provider's own interval was 30 s or less; a longer provider interval is never lowered and grows by 5 s each time. The attempt ends at `expiresAt` (the provider's code lifetime, else the method's own, never past the 10-minute flow limit), after which polls answer `oauth_state_invalid` without an upstream call. The dashboard waits the interval it was last told, retries `oauth_poll_failed` five times before pausing, and stops on its own 60 s past `expiresAt` with a Check again action.
- `expired_token` answers `oauth_expired` and `access_denied` `oauth_denied` for every RFC 8628 method (GitHub, Grok CLI, Kimi, Kiro), as Kilo's 410 and 403 already did; another named error is `oauth_sign_in_rejected`. A poll the provider answers 408, 429 or 5xx is `oauth_poll_failed` and leaves the attempt open, as the reference keeps polling through it. CodeBuddy and Qoder report neither expiry nor denial and end at the deadline.
- A device approval of an account already connected to that provider (an oauth connection on its built-in node with the same identity) renews that connection in place, answering `outcome: "updated"`: it keeps id, name, priority, enabled state, proxy and identity, takes the new credential whole, and clears health, as the reference's oauth duplicate rule does (`connectionsRepo.js:228-283`). Another account is added beside it; a connection of another provider, CodeBuddy's other region included, is never matched. A non-oauth connection with that identity still makes the save fail (`connection_save_failed`). Browser sign-ins beyond Codex keep their #215 behaviour.
- Kilo Code's approval names the account (`userEmail`, `kilocode.js:47,55`), so its sign-in asks for no identity up front. CodeBuddy without `expiresIn` stores a one-day expiry (`codebuddy-cn.js:75`).
- A GitHub result carries `copilotMissing`: true when the Copilot token mint returned none (an account without Copilot entitlement). The connection is still saved, as the reference saves it (`github.js:56-65`).
- Storing a Qoder credential changes nothing about readiness: Qoder stays `unavailable` and not served (§36) with any number of enabled connections.

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
- per-connection proxy (LiteRouter `providerSpecificData.connectionProxyEnabled`,
  `connectionProxyUrl`, `connectionNoProxy`; PARITY §6.24);
- no-auth provider rotation where configured.

A connection's outbound proxy is, in order: a member of its bound pool when the
pool is enabled and has an enabled member; else its own proxy when
`connectionProxyEnabled` is the JSON `true` and the URL is not blank; else the
global proxy. Only the connection's own no-proxy list bypasses its own proxy, and
a bypassed host takes the global proxy under the global no-proxy list. The global
no-proxy list bypasses a pool member. A value without a scheme and host is an
http proxy. A proxy that fails, or a stored URL that cannot be used, fails the
request; nothing falls back to a direct connection.

The same policy applies to all outbound work made for a connection, not only
inference: token refresh, usage reads, quota auto-ping, Codex reset credits,
connection checks, model tests and model discovery. Work made for no connection
(sign-in, helpers) uses the direct SSRF-protected client.

Outbound URL validation and redirects use the same SSRF policy for inference, provider tests, model discovery, Headroom/PXPIPE, and other server-side fetches.

### 20.1 Pool kinds, strict pools, relays, no-auth rotation and Netlify provisioning

Evidence is LiteRouter `main` at `4279093457458e168a2431604935f0145f2e76ec` (PARITY 6.38).
`C` = `src/lib/network/connectionProxy.js`, `F` = `open-sse/utils/proxyFetch.js`,
`R` = `src/app/api/proxy-pools/route.js`, `I` = `.../[id]/route.js`,
`N` = `.../netlify-deploy/route.js`, `L` = `src/lib/network/netlifyRelay.js`,
`A` = `src/sse/services/auth.js`, `P` = `src/lib/db/repos/proxyPoolsRepo.js`.

**Kind.** A pool is one of `http` (its members are forward proxies; the default),
`vercel`, `cloudflare`, `deno` or `netlify` (its members are relay functions). The
kind is read exactly: anything else, on a create or an update that names it, is `http`
(`R:10,18`, `I:40-43`). A relay member is an http(s) URL without credentials; a
forward-proxy member keeps the schemes of 20 above.

**Relay.** A connection whose pool is a usable relay pool sends every request to the
relay member's URL instead of to its destination, with the same method, headers and
body, plus `X-Relay-Target` = scheme and host of the destination (a default port is
omitted) and `X-Relay-Path` = its path and query (`F:97-110`; `C:111-125`). The relay is
the only host dialed, and it passes the SSRF policy; a destination that can never be a
public host (`localhost`, a private or loopback address) is refused rather than sent to
a third party. A relay pool's `noProxy` does not apply (`F:97-110` returns before it is
read). The relay function (`L:29-99`) forwards the request and returns the answer; its
text is `internal/proxy/netlify/bundle_gen.go`, generated from the oracle.

**Resolution** (`C:66-181`), per request, from the pool the connection is bound to
(`__none__` or no id means none):
1. the pool, when it exists, is enabled and has a usable member: a relay pool gives a
   relay; any other gives the member as the connection's proxy with the pool's `noProxy`
   (hosts that bypass the member, and then take the global proxy);
2. else the connection's own proxy, when `connectionProxyEnabled` is the JSON `true` and
   its URL is not blank;
3. else no proxy.
A pool that is `strict` keeps that guarantee when it cannot be used (disabled, or no
usable member), and a pool that no longer exists counts as strict (`C:80-85,103-104`).
For a strict connection a request that no proxy applies to (including a host that the
pool's `noProxy` or the connection's bypass list excludes, with no global proxy to take
over) fails and is not sent direct (`F:130-146`). A pool that is not strict falls back
as in 20. A connection that names no pool is never strict.

**No-auth rotation** (`A:78-104`). A `providerStrategies` entry for a provider whose
connections need no credential may carry `proxyPoolId` (a pool id, `__none__` for none) and
`rotateStrategy` (`none`, `round-robin`, `random`; any other value takes the first pool).
For such a connection the entry replaces its pool binding and its own proxy:
`rotateStrategy` `none` or absent uses `proxyPoolId`; otherwise each request takes one of
the enabled pools that have a usable member (newest `updatedAt` first), by `round-robin`
(a position per provider, kept in memory, restarting at the first after a restart) or
`random`, and with no such pool the request goes direct (`C:18-34`). A pool that an entry
names cannot be deleted (`P:108-131`).

**Admin API** (the reference's routes `R`, `I`, `N`, in Routeweft's envelope):
`GET /admin/v1/proxy-pools?isActive=&includeUsage=` filters by `enabled` when the value is
exactly `true` or `false` (`R:4-8,47-56`) and, for `includeUsage=true`, adds
`boundConnectionCount` (connections bound to the pool; `R:31-42,58-70`). A pool carries
`type`, `strictProxy`, `noProxy`, `testStatus` (`unknown`, `active`, `error`),
`lastTestedAt` and `lastError`; `strictProxy` is true only for the JSON `true`, and a
`noProxy` that is not a string is empty (`R:12-29`). An update changes only the fields it
names (`I:9-46`). `DELETE` is `409 proxy_pool_in_use` with `boundConnectionCount` while a
connection is bound or a `providerStrategies` entry names the pool (`I:94-125`). A test
of a whole pool records `testStatus`, `lastTestedAt`, `lastError` and enables the pool
when it passed and disables it when it failed (`proxy-pools/[id]/test/route.js:49-56`); a relay is
tested by sending a request through it.

**Netlify provisioning** (`POST /admin/v1/proxy-pools/netlify-deploy`; `N`, `L:1-27,193-219`).
Body `{netlifyToken, projectName | siteName}`. Without a token: `400 Netlify API token is
required`, and nothing is called. The site name is the requested one lower-cased with
anything but letters, digits and hyphens turned into a hyphen, runs collapsed and the
ends trimmed, else `relay-` and the time in base 36 (`N:13-19`). Then, with the token as a
bearer credential and the user agent `9Router`:
1. `POST /sites {name}`: a failure answers Netlify's status (422, name taken, as 409) and
   message, or `Failed to create Netlify site`, followed by a full stop and, for 422,
   ` Site name "x" is taken — choose a different name.` (`N:43-57`); an answer without an id
   or URL is 502 (`N:59-64`);
2. `POST /sites/{id}/deploys {files: {"/index.html": sha1}, functions: {relay: sha256}}`:
   a failure answers its status and message, an answer without an id is 502 (`N:70-90`);
3. only what the answer lists as required is uploaded: `PUT /deploys/{id}/files/index.html`
   and `PUT /deploys/{id}/functions/relay?runtime=js`, as `application/octet-stream`; a
   failure answers its status and message (`N:92-125`). The function is a stored (not
   compressed) ZIP of `relay.js`, written byte for byte as `L:133-186` writes it;
4. `GET /deploys/{id}` every 3 s until `ready`; `error` answers `error_message` (else
   `Netlify deploy failed`), a failed status call `Netlify status failed (n)`, and more than
   120 s `Netlify deploy timed out`, all as 500 (`N:128`, `L:204-219`).
On success a pool is created (`type netlify`, enabled, not strict, no bypass list, one
member: `<site url>/.netlify/functions/relay`) and the answer is `201 {proxyPool, deployUrl}`.
The token is used for these calls only: it is not stored, logged or returned, and a
transport error is stripped of it before it is reported.

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

### 21.0 Request details, cost and live activity

While observability is enabled the gateway keeps a bounded copy of the client request, the provider request, the first bytes of the provider response and the time of the first upstream byte for each request; they are turned into a detail on the batcher goroutine, never on the request path. A detail is the oracle's document (provider, model, connectionId, requestedModel, selectedModel, status, latency{ttft,total}, tokens, request, providerRequest, providerResponse, response{content,thinking,finish_reason}, routeMode), redacted (keys that name credentials, the request's own credentials and `Bearer` values), with each large field replaced by `{_truncated,_originalSize,_preview}` at `observabilityMaxJsonSize` bytes. Rows are kept by count (`observabilityMaxDetailRecords`) and age (seven days). A request that no upstream answered stores an error detail and no usage row.

A usage row also records `requested_model` (what the client sent), `selected_model` (`provider/model` the router chose; `model_id` is what the provider was called with), `ttft_ms` (the first upstream byte for a stream, the duration otherwise) and `cost`, computed in the batch writer from `pricing_overrides` or the generated default catalog (`internal/pricing`) with the protocol's cache convention. In-flight and recent requests are in `internal/live` and published as `usage.pending`.

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
GET    /admin/v1/proxy-pools            bounded list (existing read model); ?isActive=true|false, ?includeUsage=true (20.1)
GET    /admin/v1/proxy-pools/{id}
POST   /admin/v1/proxy-pools            {name, enabled?, strategy?, type?, strictProxy?, noProxy?, members: [{url, enabled?}]}; 201
POST   /admin/v1/proxy-pools/netlify-deploy  {netlifyToken, projectName?|siteName?}; 201 {proxyPool, deployUrl} (20.1)
PUT    /admin/v1/proxy-pools/{id}       replaces fields and members; member {id} alone, or with the redacted
                                         url echoed back, keeps its stored url; url with userinfo replaces it;
                                         url with an empty userinfo (http://@host:3128) removes credentials
DELETE /admin/v1/proxy-pools/{id}       409 proxy_pool_in_use while any connection is bound to it or a providerStrategies entry names it
POST   /admin/v1/proxy-pools/{id}/test  {memberId?}; per-member classified result; a whole-pool test is recorded and switches the pool (20.1)
POST   /admin/v1/settings/proxy-test    {url?}; tests url, else the global outbound proxy (400 if disabled)
POST   /admin/v1/connections/{id}/proxy {proxyPoolId?, connectionProxyEnabled?, connectionProxyUrl?, connectionNoProxy?}
```

`POST /admin/v1/connections/{id}/proxy` binds a pool and sets the connection's
own proxy. Any of the three `connectionProxy*` keys sets all three, as the pin's
provider routes do: only a JSON `true` enables, a non-string URL or no-proxy list
reads as empty, and both are trimmed. Enabling needs a URL that passes the pool
member checks (`invalid_connection_proxy`, `proxy_host_blocked`). A body with
`proxyPoolId` sets the binding; a body with neither kind of field is the pool
assignment it always was, so a missing `proxyPoolId` unbinds. Everything is
validated first, then the binding and the connection's own proxy are written in
one transaction and published as one snapshot, as the pin's route writes them
once (`providers/[id]/route.js:109-117,135-158`): a failed write, an unknown
pool included, changes neither. The response redacts the URL. `GET /admin/v1/connections` reports the connection's own proxy
when it is enabled, as `connectionProxy: {url, noProxy?}` with the URL redacted.

Mutations publish through `RefreshPoolBindings` and return `configRevision`.
Proxy hosts and test destinations pass the shared SSRF policy; redirects are not
followed. A connectivity failure is a 200 with `ok:false` and a `class`
(`blocked`, `dns`, `refused`, `timeout`, `tls`, `proxy_auth`, `proxy_error`,
`unreachable`); raw transport errors are never returned.

Headroom and PXPIPE are external HTTP helpers reached only at the configured
`headroomUrl` (+ `/v1/compress`) and `pxpipeUrl` (§17). Routeweft owns no helper process, so the control
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
`404 request_not_found`. Details are enqueued by the serving path (21.0); `detail` is null for a request
whose detail was pruned, shed under queue pressure or never captured (observability off).

`GET /admin/v1/usage/active` returns `{activeRequests, recentRequests, errorProvider}`.
`GET /admin/v1/requests` accepts `providerId`, `modelId`, `connectionId`, `status`
(`success` or `error`), `startDate` and `endDate`.

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
when `requireApiKey` is false. `ttftMs` is exposed per event and as the summary's
`avgTtftMs`. `cost` is exposed on a priced usage event and as `totals.cost`, on
each daily series item and on each provider/model breakdown: the stored estimate
from the operator's pricing override or the generated catalog, never a bill. A
request whose usage was never observed is not priced and omits `cost`; a
breakdown sums only the priced rows. A metrics scrape endpoint is not defined by
this specification and is not exposed.

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

### 22.1 Live event channel

`GET /admin/v1/events` is the one live channel of the dashboard (PRD-OBS-003). It is session-gated SSE. Every
frame is `id: N`, `event: <type>`, `data: {id,type,createdAt,data}`; `id` is a single increasing sequence over the
whole bus, and nothing is filtered per subscriber. A subscriber that falls behind loses events rather than slowing
anyone (per-subscriber buffer 16), so a jump in `id` means at least one event was shed. Payloads pass through
`telemetry.Redact`; no credential, Authorization value or cookie is published.

| `type` | Published when | `data` |
| --- | --- | --- |
| `config.updated` | a configuration mutation (settings, provider, combo, proxy pool, capability adapter) is committed | `resource` plus the changed identity or `configRevision` |
| `quota.refresh` | a quota refresh job changes state | the refresh job |
| `usage.pending` | in-flight request counts change; coalesced to one event per 150 ms | pending counts |
| `request.completed` | a request that delivered a response finishes (not a client abandon before any response) | `requestId`, `providerId`, `modelId`, `requestedModel`, `selectedModel`, `connectionId`, `status`, `durationMs`, `ttftMs` |
| `console.log` | log lines were appended; coalesced per 150 ms, at most 100 lines per event | `{"lines":[{id,time,level,message,attributes?}],"truncated":bool}`; `truncated` means lines beyond the cap were left out and the reader re-reads `GET /admin/v1/logs` |
| `console.cleared` | `DELETE /admin/v1/logs` emptied the buffer | `{"cleared":n}` |

A `console.log` batch is never published after the `console.cleared` that follows it in time, and log ids are not
reused after a clear, so a page can drop lines it already shows by id.

The browser opens exactly one `EventSource` on this URL for all pages and keeps it across page changes (it closes
about 1 s after the last page that wants live data leaves). Pages react to the event types they target by updating
or re-reading the matching state. The channel also tells every page to re-read what it shows when it cannot trust
that nothing was missed: an `id` gap, a reconnect after an error, or, while the stream is down, every 15 s. While the
stream is down the status reads "Live updates unavailable · refreshing every 15 s"; a stream the browser gave up on
is reopened with backoff 1, 2, 5, 10, 30 s. Without `EventSource` the polling fallback is the channel.

The Overview re-reads its data on `config.updated` and `request.completed`, at most once per 3 s; `console.log`,
`usage.pending` and `quota.refresh` do not reload it. The Console Log page prepends `console.log` lines to page 1
(subject to its level and text filter, deduplicated by id, bounded at the page size), counts them instead while the
view is paused or on another page, and empties on `console.cleared`; its **Clear logs** button confirms first and
empties the whole buffer. The Usage requests tab keeps its own usage-only stream (`/admin/v1/usage/stream`, D09).

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

### 23.1 Display language (#279, U10)

Oracle: LiteRouter `4279093457458e168a2431604935f0145f2e76ec`, `src/i18n/*`, `public/i18n/literals/*`, `components/LanguageSwitcher.js` (file:line in PARITY 6.40).

- The dashboard offers the 35 languages of `ui/src/locale.ts` (the oracle's list and order); English is the default and needs no catalog.
- The choice is the `locale` cookie: path `/`, one year, `SameSite=Lax`, `Secure` over https. The page writes it; no server code reads it, and no unauthenticated route is added for it (D51). A missing, unknown or malformed value is English; `zh` is Simplified Chinese.
- Every language but English has a catalog served from the UI directory at `/i18n/literals/<locale>.json`, byte-identical to the oracle's. The page translates its own text nodes in place from the catalog, keyed by the exact trimmed English; a text with no entry stays English. Text under `script`, `style`, `code`, `pre`, table structure, `select`, `datalist`, `optgroup` and any `data-i18n-skip` ancestor is not translated. New and changed text is translated as it appears; the sign-in page is translated like any other.
- A language is saved only once its catalog has loaded; if it cannot be loaded the previous language stays, nothing is saved and the dialog says so. `<html lang>` follows the choice; `dir` is not set.
- The control is a button showing the current language (flag and its own name, never translated) that opens a dialog of every language; it is keyboard operable, traps and returns focus, states the current language in text, has 44px targets and does not overflow at 320px.
- The `requireLogin` login preference and password recovery are not offered (D14, 12.2).

### 23.2 System One and Combo pages (#278, U09)

Oracle: LiteRouter `4279093457458e168a2431604935f0145f2e76ec` (file:line in PARITY 6.41).

- The System One page reads every page of models, provider nodes and connections (a bounded admin list is read to its last page, never only the first 100) and shows models and accounts 25 at a time. A provider is any provider with a System One model or a node with the `systemone` transport.
- A custom System One provider is a generic node whose only transport is `systemone`, a prefix that is not a built-in provider id and not already in use, and an endpoint that passes the outbound policy; its models are added one request each (at most 64 from the form). An account is a connection with an identity and an API key; a provider that serves System One under an account (Cloudflare) also takes an account ID, validated as one path segment. The API key is write-only: it is cleared from the form after saving and no read returns it.
- The typed request button runs the bounded probe; a free-form request editor is not offered (D53).
- A Combo member with no provider and a model id is a nested Combo. The Combos page offers only Combos that can be nested without a cycle or beyond `MaxComboDepth` (8), shows the capabilities (vision, pdf, audio-input, video-input) its selected members supply through nested Combos, and edits the Fusion timing overrides (`minPanel`, `stragglerGraceMs`, `panelHardTimeoutMs`, blank = default). Saving a Combo from the page sends the timing it already has.

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
ROUTEWEFT_BOOTSTRAP_ADMIN_PASSWORD=
ROUTEWEFT_MAX_BODY_BYTES=134217728
ROUTEWEFT_CORS_ORIGINS=
```

The public request-body limit defaults to 128 MiB. Cross-origin requests are
denied unless an explicit comma-separated origin allowlist is configured.
Preflight requests do not require an API key.

`ROUTEWEFT_LOG_LEVEL` (or `--log-level`) controls the JSON service logger through
Go `slog` level parsing (default `info`; `debug`, `warn`, and `error` are
supported). Invalid levels fail before opening the data directory.

`ROUTEWEFT_TRUSTED_PROXIES` (or `--trusted-proxies`) lists the reverse-proxy peers
whose `X-Forwarded-For` / `X-Real-IP` may name the client for the admin login
throttle; it is empty by default and is specified in 12.3. Without it, forwarded-IP
headers are ignored for all peers and login throttling uses the socket remote
address, which preserves the default-deny rule in PRD-SEC-002.

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

### 28.6 Pinned differential against the reference and the divergence ledger

Three comparisons run the reference's own code, not a restatement of it, and any difference they find must be accounted for before a change merges. The reference is LiteRouter `main`, resolved once per run and frozen (PARITY 6, `compat/parity/resolve-oracle.mjs`); no provider is called.

1. **Conversion legs** (`compat/parity/generate-protocol-legs.mjs`, `internal/parity/legs_test.go`). The functions each reference translator module registers are run unmodified over a corpus of synthetic requests (`legs-corpus.mjs`) and upstream streams, and Routeweft's leg is run on the same input, decoded as the request path decodes it. Requests are compared leaf by leaf as JSON, with generated ids normalised; a failure on either side is a difference. Streams are compared by what a client ends up with (`legs-fold.mjs`): text, reasoning, tool calls, finish reason, usage, or the blocks, items and parts of the target protocol. Chunk boundaries are not compared.
2. **Control workflows** (`compat/parity/generate-control-workflows.mjs`, `internal/control/api/control_workflows_test.go`). Short ordered workflows over the reference's combo, API key and model alias routes, replayed through the admin API. A step compares by outcome (success, or the refusal status) and a workflow by the state it ends in.
3. **The ledger** (`compat/parity/divergences.json`). Every difference is a group (direction, leg or workflow, kind, path) with a reference that resolves in `docs/PARITY.md` (a D row, a register item of 6.3-6.12, or a section) and an example (the first case, both values). A difference with no entry fails; an entry that no longer matches a difference fails; an entry whose example changed fails, so a leg that changes how it differs must update the ledger. `LEGS_LEDGER_UPDATE=1` and `CONTROL_LEDGER_UPDATE=1` rewrite the examples and mark new groups `UNTRIAGED`, which fails the reference check until an item is cited.

`compat/parity/ledger.mjs` writes `docs/DIVERGENCE_LEDGER.md` from the ledger and the D rows: counts, each D row with its state (approved, or decision required), every group by leg, and a sign-off block. `--check` fails when it is stale or a reference does not resolve. The `parity` CI job runs all of it and uploads the sheet as the `divergence-ledger` artifact. A divergence is never silent: it is fixed, or it has an entry, and an entry cites a decision.

## 29. CI gates

Once code exists, pull requests should run:

- `go test ./...`;
- `go test -race ./...` for relevant packages/full suite as runtime permits;
- Go vet/static checks;
- UI install lockfile check;
- UI typecheck;
- UI unit tests;
- UI production build;
- browser smoke and accessibility checks (`npm run smoke`), in the `ui-smoke` job, against a fresh preview stopped by PID;
- protocol fixture suite;
- pinned differential and divergence ledger (28.6), in the `parity` job;
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
`usage: true` advertisement: `iflow` has a message-only handler without the flag, while the current pin has
a real `qoder` handler with PAT resolution. The readable set excludes handlers
that make no upstream observation.
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

### 33.2 Connection validation lifecycle (#220)

Each connection keeps a persisted validation lifecycle beside it in
`provider_connections` (migration v13), outside the sealed secret and outside the
runtime snapshot: recording a result never opens a credential, recompiles
configuration or touches the serving path. The connections read model reports
four separate things the operator must not confuse:

| Question | Field(s) | Source |
| --- | --- | --- |
| Did the provider accept the credential? | `testStatus`: `untested`, `active` or `failed`; `lastError`, `lastErrorAt` | sign-in/import, connection test |
| Did a model answer? | `lastTestedAt`, `modelsAvailable` | connection test / batch test |
| Is quota observed? | `plan`, `quotaObservedAt` | quota service (in memory) |
| Is the provider ready? | catalog `readiness` | build-time catalog (unchanged) |

`authOrigin` (`manual`, `oauth`, `device`, `import`) names how the credential
arrived; `expiresAt` mirrors the sealed secret's expiry, rewritten with every
new secret, including a refresh rotation (`RotateSecret`). Neither is credential
material.

Transitions, after the reference (`testUtils.js:892-899`,
`[provider]/[action]/route.js:69`, `connectionsRepo.js:64-75`). A save's status
and origin are written by the same statement that writes its credential
(`credentials.Lifecycle` on `PutConnection`), so no concurrent save or check
can pair one credential with another's status; a new credential also clears
the last test, error and model count:

- A browser or device sign-in, a Reauthenticate, and an import the provider
  confirmed (an exchange, refresh or verification call: iflow cookie, Kiro
  token or API key, GitLab PAT, Xiaomi with a successful model listing),
  including one that updates a stored duplicate, mark the connection `active`
  with its origin and clear the last error. An import the flow only parses (a
  pasted Codex, grok-cli or Cursor token, a Kiro CLI-proxy file) and every
  bulk item reach no provider: they are saved `untested` with origin `import`
  when new and, on a stored duplicate, keep an `active` status but turn a
  `failed` one `untested`, as a soft-validated Xiaomi key does. `active` means
  a provider confirmed the credential; the reference writes `active` on these
  imports too (`codex/import-token/route.js:78`, `cursor/import/route.js:56`,
  `kiro/import-cli-proxy/route.js:23`, `grok-cli/bulk-import/route.js:99`,
  `codex/bulk-import/route.js:102`), which Routeweft does not: the status is
  a dashboard label and no routing decision reads it. The response carries
  `testStatus` and `authOrigin`, beside the existing `outcome` (`added`,
  `updated`, `reauthenticated`).
- A manual create, or a manual edit that carries credential material (a
  relink), marks it `untested` with origin `manual` and clears the last test,
  model count and error: no provider has seen the new credential. A relink's
  new key also replaces every stored copy of the old one:
  `ProviderData.apiKey`, which onboarding keeps beside the access token for
  kimi, iflow and xiaomi-mimo and which usage readers prefer, and any other
  credential entry that held the old access token; other provider data is
  kept. A relink replaces only the credential fields it names and keeps every
  stored access, refresh and ID token and cookie it does not name: a
  cookie-only relink keeps the stored tokens, an access-token relink keeps
  the refresh and ID tokens and cookie, and an
  ID-token or refresh-token edit keeps the access token and its expiry. A
  cookie-only relink of a provider that serves with a key minted from its
  cookie (iflow: the cookie flow mints the API key and stores it as the access
  token and `ProviderData.apiKey`, `onboarding/manual.go:189-275`, and the
  resolver prefers that key over the cookie) re-runs the provider's cookie
  flow with the new cookie, as the reference's `iflow/cookie/route.js:29-119`
  does, and saves what it mints: the new key, its copy and `expireTime`, and
  only the `BXAuth` cookie. A cookie the provider refuses answers
  `502 relink_rejected`, one without `BXAuth` `400 relink_invalid`, and
  neither changes the stored credential. A provider that serves with the
  cookie itself (grok-web, perplexity-web) has its cookie replaced as typed. A
  relink resets the connection even when the operator types the stored key
  again: it is `untested`, its last test, error and model count are cleared
  and a check started before it is refused, so the dashboard's "Credential
  replaced" never sits beside the previous result. The
  reference keeps the previous status on such an edit; Routeweft does not
  report a result for a credential that was never tested. An edit of name,
  identity, order, proxy or enabled state keeps the stored lifecycle.
- `POST /admin/v1/connections/{id}/test` and each account of
  `POST /admin/v1/providers/test-batch` record `active` (clearing the error)
  with the time and the listed model count, or `failed` with the error and the
  time, keeping the last confirmed model count: a failed probe's listing is not
  a count of usable models. A pass that listed no models (a provider with no
  listing, an empty listing, or a failed listing after which the check ran on
  the supplied model) also keeps the last confirmed count rather than recording 0. A canceled
  test and a disabled connection record nothing. A result is recorded only
  against the `credential_revision` read with the credential it tested. Only a
  replacement increments it: a manual relink, or a save whose credential
  material differs, by a
  keyed digest (`credential_digest`, HMAC under a key derived from the master
  key), from the stored one. An edit that reseals the same credential (name,
  order, proxy, provider settings) and a refresh rotation do not, so a check
  survives them, while a check that started before a relink and ends after it
  is refused and the replacement keeps its own status. A connection stored
  before the digest existed has its digest filled in from its stored secret by
  its first save, before the comparison, so that save is not mistaken for a
  replacement. Every replacement clears the last test, error and model count,
  which describe the credential it replaced, whether or not the save names a
  status. An `untested` lifecycle applies only on a replacement; a replacement
  that names no status (Xiaomi's soft-validated duplicate) keeps an `active`
  status, as the reference keeps `existing.testStatus`
  (`xiaomi-mimo/api-key/route.js:79`), but turns a `failed` one `untested`:
  that failure was the old credential's, and the reference's would show the
  old error against the new key. An `active` one applies on any save
  of a credential a provider issued or accepted, the same credential included
  (a reauthentication or validated reimport returning the stored token), and
  clears the last error, as the reference's activation does
  (`connectionsRepo.js:64-75,282,347`). The error is the probe's bounded text with
  every credential value of the connection (stored and as the registry
  currently holds it), whatever its length, replaced by `[redacted]`, in both
  the answer and the stored row: the access, refresh and ID tokens, the cookie,
  and provider-data values under credential keys (the provider's named
  credential, keys the log redaction treats as secret, proxy URLs). Provider
  metadata such as project, region, account ids and plan is not redacted. A
  value shorter than 8 characters is replaced only where it stands alone, with
  no letter or digit on either side, so a short credential is still removed
  but cannot rewrite an ordinary word that contains it. Unsaved node validation applies the same redaction.
- A save that fails (invalid account id, unknown proxy pool, store error)
  leaves the stored credential, its runtime registration and its lifecycle
  unchanged. Deleting the connection deletes its lifecycle with the row.

The Providers UI shows the status as a state tag, a one-line summary (last
check, models listed, token expiry, origin, plan, quota seen) and the last error
when failed. Edit changes name and identity and, for API-key and cookie
connections, replaces the credential through a masked field that starts empty
(a blank field keeps the stored one) and is cleared once saved; sign-in
connections are renewed with Reauthenticate.

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

Implemented as `registry.Spec.Readiness` (status plus a reason for any non-ready
value) and exposed as `readiness{status,served,reason}` on `GET /admin/v1/providers`.
It is distinct from two other facts: `enabled` is an operator preference per
connection, and a stored credential is account state. Readiness depends on neither:
an unavailable provider can be enabled and credentialed, and requests to it fail
honestly rather than being refused at enable time; a ready provider is ready with
no account. A spec with no declared readiness reads `unknown`, never `ready`.

## 37. Requirements source-of-truth rule

Before implementation, agents must treat:

1. PRD;
2. PROVIDER_BASELINE;
3. SPEC;
4. BDR;
5. UI_STYLE/RUNBOOK/SPRINT_PLAN

as the approved product/technical hierarchy described in `AGENTS.md`.

A LiteRouter source observation that conflicts with an approved Routeweft product decision is not copied silently; it becomes a product-drift review.

### P05 quota admin actions and opt-in warming

PRD-QUOTA-001: GET `/admin/v1/quota` adds cached named windows, plan and optional
Codex `availableResetCredits`. A prepaid credit balance (DeepSeek, #212) is one
window per currency carrying `credit` `{currency, state, total?, granted?,
toppedUp?}`, where `state` is `positive`, `zero`, `negative`, `missing` or
`malformed`; such a window is never `unlimited`, has no `remaining`, and is
never summed with or compared to another currency. The provider's account flag
appears as `creditAvailable` only when it was reported.
Provider metadata (#222): a window may carry `count` `{used?, total?, remaining?}`,
an absolute allowance in the provider's unit (GitHub Copilot; a zero total is a
reported zero, with no `remaining` fraction, never unlimited); `scope`
`{kind, model}` (Claude `weekly_scoped`); and `limitReached`, the provider's
flag for the window's group (Codex), which routing does not read. An item may
carry `metadata` (string values: GitHub `resetDate`, Claude legacy
`organization`), `extraUsage` (Claude `extra_usage` as reported) and `message`,
a reader's fixed notice for an account answered without readable quota (status
`unknown`, never `error`).
Regional package metadata (#223): a window may also carry `recurring`, the
provider's refill flag (CodeBuddy: `true` resets at `resetAt`, `false` expires
there for good), and `limit` `{type, unit?, number?}`, the provider's own limit
description with `unit` and `number` as the JSON values sent (GLM). Kimi windows,
counted MiniMax buckets and CodeBuddy packs carry `count`; a percent-only
MiniMax bucket does not, and a zero CodeBuddy capacity has no `remaining`
fraction and is neither unlimited nor exhausted.
Billing amounts (#225): a `count` may also carry `currency`, the ISO code when
its amounts are money: Kenari windows in `IDR`, and Vercel AI Gateway's
`Used (USD)` (unlimited, the amount spent) and `Remaining (USD)` (the balance
against the reference's $5 allocation). Command Code windows carry `count`;
its credits without a known plan cap are unlimited, as in the reference.
Answers without quota are notices for Kenari (no plan or no windows), OpenCode
Go (no quota, or no subscription), Ollama (no limits), Vercel AI Gateway (BYOK
or unfunded) and a Xiaomi MiMo key that cannot read the weekly quota. A MiMo
account session is kept for 30 minutes per account credential, in memory.
A Kenari amount the provider omits is read as 0, as in the reference, so a
window with neither amount is 0% remaining. Quota readers never read a host
browser or desktop cookie store or other host secret themselves; MiMo's account
path uses the account fields sealed into the connection, which only a consented
renewal (#299, above) may refresh from MiMo Desktop.
A window a reader served from its own cache carries `observedAt`, the time it
was read upstream, and the UI labels it "cached, read <time>"; a window without
`observedAt` is as fresh as `windowsObservedAt`. Only the Antigravity weekly
summary is cached (3 minutes per token and project, never when the summary
failed or was empty, not bypassed by a manual refresh, #240). Cloud Code quota
calls (`gemini-cli`, `antigravity`) and the Xiaomi MiMo usage reads are each
bounded at 10 seconds as in the reference; every other usage call keeps 15.
`remaining` (item and window) is a fraction in [0,1]; the UI renders it as a
rounded whole percentage, never as a count (#213). The read accepts `page`,
`pageSize` (max 100), `provider` (exact provider id), `q` (case-insensitive
substring of account name or provider id) and `sort` (`default`, `reset`,
`exhausted`, `error`; anything else is 400 `invalid_sort`); filtering and sorting
apply to every account before paging, and the response adds `providers` (every
configured provider id) and `sort`. Item `status` is one of `available`,
`exhausted`, `cooldown`, `error`, `unknown` (observed, no bound reported),
`unlimited` (every reported window unlimited) or `not-reported` (never
observed); a missing observation is never zero, unlimited or available. Items
add `nextResetAt` (earliest reset across the routing observation and windows),
`windowsObservedAt` (the read the windows came from, which an outage keeps) and
`stale` (observation older than three refresh intervals, default 15 minutes).
Session-gated POST `/admin/v1/quota/refresh` accepts a refresh job (#226): 202
`{accepted:true, joined, job}` where `job` is `{id, status, requestedAt,
startedAt?, finishedAt?, error?}` and `status` is `queued`, `running`,
`succeeded` or `failed`. A 202 is acceptance, never an observation; rows change
only through GET `/admin/v1/quota`, each with its own `observedAt`. At most one
refresh runs: a POST while one is queued or running returns that job with
`joined:true`. The job is bounded at 60 seconds, and the deadline ends it
whether or not the refresher returns; an overrun or a read failure is `failed`
with a fixed `error` phrase (a reader's own text is never echoed). A refresher
that overran is still held: until it returns, a POST is 409 `refresh_busy`, so
two refreshers never run at once, and its late answer cannot change the job.
Session-gated GET at the same path returns `{status, job}` for the latest job,
or `{status:"idle"}` before any. Each transition publishes `quota.refresh` with
the job. The UI follows the job, keeps saying the rows are the previous
observations until it is terminal, reloads them then, and marks an enabled row
whose observation predates the job's start as not updated by it.
GET `/admin/v1/quota/{id}/codex-reset-credits` returns the existing normalized credit listing. Session-gated POST at the same
path requires `{"confirm":true}`, spends one credit with a server UUID, returns
409 for no credit and 502 for an unavailable/ambiguous result. It never retries
an ambiguous spend; the one replay is after a credential refusal (401/403, so
nothing was spent): once, with the same redeem id and a forced refresh of the
access token (`Registry.ForceRefresh`; codex-reset-credits/route.js:136-147), and
only when the connection has a refresh token and is not an access-token connection. A 4xx whose text reads as an expired credential (route.js:9-20) is replayed like 401/403; a 2xx or 5xx never is. The consume, the refresh and the replay each have their own 15s budget; a refresh's durable commit is detached and bounded at 30s separately, so one POST can take about 75s (about 105s if the refresh before the spend also commits). An access-token connection is the sign-in match's `codexTokenOnly`, including one stored before its auth type was recorded. A Codex refusal is definite and keeps its 4xx status
(codex-reset-credits/route.js:42-47), including a text-matched 4xx after the replay or a failed refresh; a refused credential (401/403) is 403
`reauth_required`, because 401 is the admin session's status. Both actions reject non-Codex token connections before any
network work and refresh credentials through the shared registry. The UI lists
credits soonest expiry first, spends one credit per confirmed click only when a
positive count was reported (a freshly read list's count wins over the row's
cached one). Every spend first reads the credit list live from Codex; the
confirmation shows that read's count, and a read that fails or reports no credit
spends nothing and offers no confirmation. Nothing held in the page stands in
for that read, so the guard holds after leaving the page, a reload or in another
tab. Any 5xx, or a request that did not complete, is an unknown outcome: it is
never sent again, and the credit list is read again from Codex at once.

`claudeAutoPing` and `codexAutoPing` are string-valued settings containing JSON
`{"connections":{"id":true}}`, defaulting to `{"connections":{}}`. The minute
worker reads current settings and targets enabled matching OAuth connections.
Claude warms at reset with a 5-second lead tolerance, and between resets does
not read usage until five minutes before the cached reset
(quotaAutoPing.js:191-193); Codex reads every tick. Opted-in accounts are
visited one at a time, and a quota read or refresh never sends a warming request. Codex requires a reset
slide of at least 30 seconds, a 10-minute minimum interval and no exhausted
non-session windows. Failed reads/sends cool down for 15 minutes. A successful
send is suppressed for that reset minute; history persists through the existing
settings transaction protocol under non-writable `quotaAutoPingHistory`.
Codex streams must finish before a ping counts as successful. A crash between
upstream acceptance and the local commit can repeat a ping after restart.
Per-provider evidence and retained gaps: `P05_QUOTA_AUDIT.md`.

**Warming switch on the Quota page (#297).** `GET /admin/v1/quota` items carry
`authKind` (the connection's credential kind, never a secret). The page draws a
per-account switch only for `claude` and `codex` items whose `authKind` is `oauth`,
as the oracle does (`usage/components/ProviderLimits/index.js:1193`, LiteRouter
`4279093457458e168a2431604935f0145f2e76ec`). It reads the opt-ins from
`GET /admin/v1/settings` (`:543-555`), and a change reads them again, sets or removes
only that account's entry and writes the provider's whole `{"connections":{...}}`
value with `PATCH /admin/v1/settings` (`:557-577`); the account list is therefore
last-writer-wins across browsers, as the oracle's is. Unlike the oracle, which reverts
only when the request throws and ignores an HTTP error, the page also leaves the
switch unchanged and reports the error on a refused save, and it hides the switches
when the settings cannot be read instead of drawing them off. State is shown in words,
not colour alone; the controls are keyboard operable with 44px targets and no
horizontal overflow at 320px. Default-off and the bounded, one-at-a-time worker above
are unchanged.
