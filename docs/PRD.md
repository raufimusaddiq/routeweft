# Routeweft Product Requirements

Status: **Foundation / normative**

## 1. Product definition

Routeweft is a standalone, local-first LLM routing gateway with a web control plane.

Its job is to present stable client-facing LLM interfaces while Routeweft owns the complexity of:

- provider selection;
- multiple accounts per provider;
- credentials and refresh;
- model mapping;
- fallback and rotation;
- quota/cooldown/health state;
- Combo routing;
- prompt-efficiency transforms;
- prompt-cache preservation;
- Usage and operational visibility.

Routeweft is not a LiteRouter release line. It has its own database, release process, configuration schema, binary, container, UI, and operational lifecycle.

## 2. Primary user

The first product is optimized for a technically sophisticated operator running one Routeweft instance on a private server.

The operator wants:

- one endpoint for many LLM providers;
- reliable daily-drive behavior;
- predictable routing;
- low router overhead;
- local durable state;
- no mandatory cloud control plane;
- clear Usage/Quota/provider health;
- easy provider/account management;
- reversible upgrades.

## 3. Product principles

1. **Correct before clever.** A routing optimization is invalid if it changes client-visible behavior unexpectedly.
2. **Memory hot path, durable local state.** SQLite is authoritative; requests use compiled in-memory state.
3. **Native first.** Do not translate a protocol that can be forwarded safely.
4. **Provider identity is not protocol identity.** Multiple providers can share a wire adapter.
5. **One coherent runtime view.** Requests read one immutable snapshot rather than many repository caches.
6. **Failure is bounded.** Retries, queues, timeouts, fallback, and shutdown all have explicit limits.
7. **Daily-drive observability.** Failures must be diagnosable without attaching a debugger.
8. **Operator UI, not a marketing site.** Fast, information-dense, neutral, accessible.
9. **Standalone means standalone.** No writable state is shared with another router product.

## 4. Goals

### 4.1 Inference

Routeweft must support:

- OpenAI Chat Completions;
- OpenAI Responses;
- OpenAI Responses Compact compatibility;
- Anthropic Messages;
- Anthropic count-tokens compatibility;
- System One;
- Gemini-compatible LLM generate/stream ingress where enabled;
- Ollama-compatible chat ingress where enabled;
- SSE/streaming and non-streaming;
- client cancellation;
- native passthrough;
- protocol translation when required.

### 4.2 Routing

Routeweft must support:

- multiple providers;
- multiple accounts per provider;
- provider/account enable/disable;
- connection priority;
- fill-first;
- global round-robin;
- sticky round-robin;
- per-provider `providerStrategies`;
- bounded account fallback;
- retry exclusion;
- quota-aware routing;
- model cooldown/locks;
- provider/model/account health;
- proxy assignment;
- proxy pools;
- global outbound proxy and no-proxy policy;
- model aliases;
- custom models;
- disabled models;
- pricing overrides.

### 4.3 Combo

Everything in the Routeweft Combo product is retained as a first-class feature:

- named Combo CRUD;
- ordered model membership;
- reorder/add/remove/deselect;
- fallback strategy;
- round-robin/sticky strategy;
- per-Combo `comboStrategies`;
- Fusion panel + judge;
- explicit/default judge;
- quorum, straggler grace, hard timeout;
- graceful degradation when only one panel succeeds;
- capability-aware reordering of existing members;
- capacity-adapter pools;
- per-capability fallback or round-robin;
- vision and audio-input adapter UI;
- stored pdf/video capability compatibility for future/hidden support;
- empty adapter pool = no-op;
- final model may be deselected;
- adapter context-window history trimming.

Capability routing exists to route an LLM request to an LLM model that can accept its input. It does not make Routeweft a general media platform.

### 4.4 Prompt efficiency

Hard-retain:

- RTK;
- Caveman;
- Ponytail;
- Headroom;
- PXPIPE;
- per-request token-saver bypass;
- prompt-cache anchoring;
- cache-read/cache-create token accounting;
- request-detail diagnostics for transforms.

### 4.5 Provider/auth workflows

Routeweft architecture must support provider modules that use:

- API keys;
- access tokens;
- OAuth;
- rotating refresh tokens;
- cookies/session credentials;
- PAT-like credentials;
- no-auth/free endpoints;
- provider-specific import helpers.

Provider modules may also provide:

- model discovery;
- suggested models;
- quota;
- precise reset times;
- model validation/test;
- connection test;
- provider-thinking controls;
- provider-specific headers/quirks.

Initial implementation should maximize shared protocol adapters rather than create one transport implementation per provider.

### 4.6 Control plane

The web UI must provide:

**Operate**
- Overview
- Endpoint & Key
- Providers

**Route**
- Combo & Capability Adapter
- System One

**Observe**
- Usage
- Quota Tracker
- Token Saver

**System**
- Console Log
- Settings

Supporting workflows include:

- API key create/pause/resume/delete/copy;
- dashboard login/password;
- provider add/edit/reorder/test/import;
- connection health/quota;
- model aliases/custom/disabled/pricing;
- provider nodes;
- proxy pools;
- Combo/Fusion/capability settings;
- request detail;
- provider topology/activity;
- database backup/restore;
- theme and local UI preferences.

## 5. Public API contract

Primary public routes:

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

Optional compatibility ingress implemented as LLM-only surfaces:

```text
POST /v1/api/chat
GET  /v1beta/models
POST /v1beta/models/{model}:generateContent
POST /v1beta/models/{model}:streamGenerateContent
```

Routeweft must not add standalone image/video generation, speech/TTS/STT, embeddings, or media-provider APIs under the initial product scope.

## 6. Streaming contract

For supported streaming protocols:

- first bytes should be forwarded as soon as safely possible;
- Routeweft must not buffer a full response unless a feature requires it;
- cancellation must abort upstream work;
- terminal events must be emitted exactly once;
- malformed upstream termination must map to an explicit client-visible failure rather than silently hanging;
- telemetry completion must be independent from keeping the client connection open after terminal delivery.

Fusion is an explicit exception: panel members are collected non-streaming; the judge can stream to the client.

## 7. Runtime and persistence requirements

Single-instance Routeweft uses SQLite WAL.

Steady-state inference configuration must be served from an immutable in-memory snapshot.

SQLite is used for:

- startup/reload;
- configuration mutations;
- credentials;
- API keys/admin state;
- Usage/request-detail persistence;
- selected restart-durable operational state;
- backup/restore/migrations.

SQLite must not be queried synchronously on every successful inference request for routing configuration.

Redis is not required.

PostgreSQL is not part of the initial product.

## 8. Security requirements

Routeweft must:

- require API-key protection according to configured policy;
- protect the dashboard with login/password according to configured policy;
- hash dashboard passwords;
- compare API keys safely;
- redact credentials from logs and request details;
- keep provider credentials server-side;
- protect configurable outbound URLs from SSRF;
- revalidate redirects;
- block link-local/metadata destinations unless explicitly safe and required;
- sanitize forwarded hop-by-hop and sensitive headers;
- propagate trusted client IP only through an explicit trusted-proxy policy;
- never expose database backup endpoints without admin authentication.

## 9. UI requirements

The visual contract is [UI_STYLE.md](UI_STYLE.md).

Routeweft deliberately follows the current LiteRouter operator-console visual language while remaining an independent frontend implementation.

Key requirements:

- neutral warm light/dark themes;
- IBM Plex Sans + JetBrains Mono;
- Material Symbols;
- compact left control-plane sidebar;
- 10px card primitives with subtle border/ring;
- strong information hierarchy;
- color used for status rather than decoration;
- responsive mobile drawer;
- keyboard/focus support;
- reduced-motion support;
- no production Next runtime.

## 10. Observability requirements

Routeweft must expose:

- request count;
- active requests;
- success/failure;
- latency/TTFT where available;
- provider/model/account/API-key attribution;
- prompt/completion tokens;
- cache-read/cache-create tokens;
- estimated cost from configured pricing;
- route/fallback decisions;
- cooldown/quota state;
- request details with redaction;
- transform diagnostics;
- live events;
- console/service logs.

Critical Usage accounting must not be silently dropped under ordinary queue pressure.

## 11. Performance objectives

Performance targets are budgets, not marketing claims.

Initial engineering goals:

- router-only native-path p95 <= 12 ms against local mock upstream after warmup;
- target native-path p50 <= 7 ms;
- no request-path global mutex;
- no per-request durable RR cursor write;
- stable behavior at 1/10/50/100 concurrent streams;
- post-burst RSS target <= 64 MiB for the Go service under representative fixtures;
- production server image goal <= 100 MiB; stretch <= 60 MiB;
- cached dashboard navigation perceived < 100 ms after assets/data are warm;
- initial overview API target < 20 ms from local server;
- no prompt-cache regression on controlled fixtures.

The benchmark harness is authoritative; upstream LLM latency is reported separately from router overhead.

## 12. Reliability requirements

- readiness is false until SQLite migrations/load and RuntimeSnapshot compilation succeed;
- liveness must not depend on an upstream provider;
- configuration mutations are serialized and atomic at the application level;
- OAuth refresh is singleflight per identity;
- rotating refresh-token updates are durable before old credentials can be lost;
- telemetry is bounded;
- shutdown drains active streams and accepted critical telemetry within configured deadlines;
- backup restore is validated on a temporary candidate before activation;
- schema changes must have upgrade/rollback behavior documented.

## 13. Non-goals

Initial Routeweft does not target:

- Kubernetes;
- microservices;
- multi-region HA;
- multiple active SQLite writers;
- mandatory Redis;
- PostgreSQL;
- agent orchestration;
- vector databases/RAG;
- standalone media generation;
- speech products;
- embeddings products;
- a hosted SaaS control plane;
- LiteRouter database migration compatibility;
- LiteRouter API-internal compatibility;
- preserving another project's package/module layout.

## 14. Standalone boundary

Routeweft may run beside LiteRouter for evaluation, but:

- use a separate port;
- use a separate SQLite/database directory;
- use a separate container;
- use separate writable OAuth credential ownership;
- use a separate reverse-proxy route;
- do not mount the same data volume.

A user can choose either product independently.

## 15. Release gates

A release suitable for daily-drive use requires:

- public protocol contract tests green;
- routing/Combo/token-saver fixtures green;
- prompt-cache parity fixtures green;
- provider modules used by the deployment green;
- OAuth rotation tests green where relevant;
- SQLite integrity/backup/restore tests green;
- race tests green;
- streaming cancellation/terminal tests green;
- security/SSRF tests green;
- benchmark budgets reviewed;
- UI critical workflows smoke-tested;
- immutable image built;
- upgrade and rollback rehearsal completed.

## 16. Definition of done for initial GA

Initial GA is complete when Routeweft can be installed on a fresh host with no other router present and the operator can:

1. create/login to the control plane;
2. configure providers/accounts/credentials;
3. create client API keys;
4. discover/select models;
5. configure routing strategies;
6. create/use Combo fallback/RR/Fusion/capability adapters;
7. use Chat Completions, Responses, Messages, and System One;
8. use RTK/Caveman/Ponytail/Headroom/PXPIPE as configured;
9. observe Usage/Quota/request details/logs;
10. back up and restore the Routeweft database;
11. upgrade and roll back Routeweft without involving LiteRouter.

## 17. Reference provenance

The initial requirements were informed by behavior proven in LiteRouter and by its current control-plane UX. The UI reference snapshot used when this document was created is LiteRouter main `2ffb7922954112b30425cd487d686758e519397e`.

This provenance is informational only. Routeweft's contract is this repository's approved documentation and tests.
