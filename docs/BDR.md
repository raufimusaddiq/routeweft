# Routeweft Build Decision Record (BDR)

**BDR** means **Build Decision Record** in this repository.

This document records binding architecture/build choices so implementation agents do not repeatedly reopen settled questions. A decision changes only through a reviewed PR that updates this file and the affected PRD/SPEC sections.

## Decision index

| ID | Decision | Status |
|---|---|---|
| BDR-001 | Routeweft is a standalone product | Accepted |
| BDR-002 | Go modular monolith | Accepted |
| BDR-003 | Static React/Vite control plane | Accepted |
| BDR-004 | SQLite WAL is the initial durable store | Accepted |
| BDR-005 | No Redis/PostgreSQL in normal single-instance architecture | Accepted |
| BDR-006 | Immutable RuntimeSnapshot + separate RuntimeState | Accepted |
| BDR-007 | Candidate compile before durable config commit | Accepted |
| BDR-008 | Standard `net/http` default | Accepted |
| BDR-009 | Provider identity separated from protocol adapters | Accepted |
| BDR-010 | Native-first request path | Accepted |
| BDR-011 | Full Combo/Fusion/capability routing | Accepted |
| BDR-012 | Token savers + final-body cache anchoring | Accepted |
| BDR-013 | Bounded async telemetry with protected critical accounting | Accepted |
| BDR-014 | Versioned `/admin/v1` control API | Accepted |
| BDR-015 | LiteRouter visual language, Routeweft-owned UI implementation | Accepted |
| BDR-016 | No standalone media product surfaces | Accepted |
| BDR-017 | One application writer per SQLite DB | Accepted |
| BDR-018 | Default listen port 21128 | Accepted |
| BDR-019 | Exact SQLite Go driver | Constrained/Deferred |
| BDR-020 | Strict FIFO review discipline | Accepted |

## BDR-001 — Standalone product

Routeweft is not LiteRouter v2.

Consequences:

- new binary/package naming;
- new database/schema;
- no shared writable volume;
- no guarantee that a LiteRouter DB can be opened by Routeweft;
- no runtime import/dependency on LiteRouter;
- Routeweft releases and rollback operate independently.

LiteRouter may be consulted as reference behavior during bootstrap, but Routeweft documentation/tests become authoritative.

## BDR-002 — Go modular monolith

Inference, control API, snapshot compiler, routing, provider modules, persistence, and telemetry live in one Go service.

Why:

- low process/runtime overhead;
- shared in-memory runtime state;
- atomic snapshot publication;
- simpler single-server operation;
- easier cancellation/transport pooling.

Rejected initially:

- microservices;
- separate inference/control services;
- Kubernetes-oriented topology.

## BDR-003 — Static React/Vite UI

Frontend is React + TypeScript + Vite.

No Next production runtime.

The UI consumes Go control APIs and SSE events.

## BDR-004 — SQLite WAL

SQLite is authoritative for the first product topology.

Why:

- one server/one writer fits the workload;
- config writes are infrequent;
- Usage writes are batched;
- routing configuration is not read from DB per request;
- backup/restore and deployment remain simple.

## BDR-005 — No mandatory Redis/PostgreSQL

Redis is not a request-path/config dependency.

PostgreSQL is not implemented merely for architectural fashion.

Revisit only when Routeweft intentionally targets multiple independently serving replicas/HA or workload evidence makes SQLite inappropriate.

## BDR-006 — Snapshot/state separation

Immutable configuration goes into RuntimeSnapshot.

Mutable operational state goes into RuntimeState.

This prevents config coherence bugs without forcing per-request persistence.

## BDR-007 — Compile before commit

A control mutation is validated and compiled as a complete candidate before SQLite commit.

After commit, publication is an atomic in-process operation.

There is no commit-then-hope-compilation-succeeds variant.

## BDR-008 — Standard net/http default

Use Go standard HTTP primitives unless a benchmark/correctness requirement demonstrates a need for another router/server library.

A convenience framework is not sufficient justification.

## BDR-009 — Provider/protocol separation

Provider modules describe identity/auth/endpoints/quota/quirks.

Protocol adapters own Chat/Responses/Messages/Gemini/Ollama/SystemOne wire semantics.

A provider that is OpenAI-compatible should reuse the OpenAI adapter rather than clone translation logic.

## BDR-010 — Native-first

Routing modes:

1. native passthrough;
2. native normalized;
3. translated.

Do not force every request through a universal canonical object if no translation is required.

## BDR-011 — Full Combo product

Routeweft retains:

- fallback;
- RR/sticky;
- Fusion + judge;
- capability-aware member reorder;
- capacity-adapter pools;
- context trimming for adapter targets.

These are LLM routing features.

## BDR-012 — Token saver order

Normative order:

```text
RTK
 -> Headroom
 -> Caveman
 -> Ponytail
 -> PXPIPE
 -> prompt-cache anchors
 -> final provider normalization/dispatch
```

Anchoring earlier than transforms is rejected because transforms may invalidate the cached prefix.

## BDR-013 — Telemetry

Normal telemetry is bounded and asynchronous.

Critical accounting is protected with:

- diagnostic shedding first;
- short bounded critical backpressure;
- emergency flush;
- explicit degraded health/lost-accounting metric on persistent failure.

Unbounded queues and ordinary silent Usage loss are rejected.

## BDR-014 — Versioned admin API

New UI APIs live under `/admin/v1`.

The UI is a consumer, not an alternate owner of business logic.

## BDR-015 — UI visual contract

Routeweft follows the LiteRouter current operator-console visual language captured in `docs/UI_STYLE.md`, but the implementation is Routeweft-owned and static.

The reference commit when frozen is `2ffb7922954112b30425cd487d686758e519397e`.

## BDR-016 — Media boundary

Allowed:

- image/audio/file content blocks inside retained LLM protocols;
- capability detection/rerouting for LLM models.

Not initial product surfaces:

- image generation;
- video generation;
- TTS/STT;
- embeddings;
- media-provider management.

## BDR-017 — Single DB writer

One Routeweft application process owns writable SQLite state.

If another Routeweft instance is started for test/canary, it gets its own DB/credentials.

Do not use shared network-filesystem SQLite as an HA mechanism.

## BDR-018 — Default port

Default server listen: `:21128`.

The value avoids assuming LiteRouter's port and reinforces standalone coexistence.

It remains configurable through `ROUTEWEFT_LISTEN`.

## BDR-019 — SQLite driver decision deadline

Status: constrained/deferred.

Resolve in the first storage PR.

Evaluation must include:

- fresh schema/migrations;
- WAL;
- busy timeout;
- backup API;
- concurrent telemetry/config writes;
- race tooling;
- Linux amd64 production target;
- optional arm64;
- binary/image size;
- CGO build complexity;
- measured latency/throughput.

Default preference is a maintained pure-Go driver if it meets correctness/performance budgets. Evidence can justify CGO.

## BDR-020 — Review discipline

The project uses FIFO review discipline.

Do not stack noisy pushes onto a PR waiting for exact-head review.

Validate all prior findings, bundle valid fixes, then push once.

Dependent work proceeds after reviewed/merged prerequisites unless explicitly planned otherwise.
