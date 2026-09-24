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
| BDR-019 | Exact SQLite Go driver | Accepted: `modernc.org/sqlite` v1.34.5 |
| BDR-020 | Strict FIFO review discipline | Accepted |
| BDR-021 | One feature/PR per dedicated worktree | Accepted |
| BDR-022 | 80-provider built-in catalog is a first-class GA target | Accepted |
| BDR-023 | Generic Provider unifies Chat/Responses/Messages compatible nodes | Accepted |
| BDR-024 | Product requirements freeze gates Codex implementation | Accepted |

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

## BDR-019 — SQLite driver

Decision: use `modernc.org/sqlite` v1.34.5, a maintained pure-Go SQLite driver.

Evidence collected for Sprint 1 PR 4:

- Go 1.22 Linux/amd64 build, `CGO_ENABLED=0`, succeeds; this avoids a C toolchain in production builds.
- Store tests verify fresh schema/migrations, idempotent restart, WAL, foreign keys, 5-second busy timeout, online backup, and integrity check.
- `go test -race ./...` passes, including concurrent snapshot readers/writers.
- Online backup uses the driver's SQLite backup API; restore candidates are copied into a private stage and integrity/schema/snapshot validated there before any activation.
- Same local 16,000-row synthetic write benchmark, 8 serialized `database/sql` writers, `MaxOpenConns(1)`, WAL, 5 repetitions: modernc v1.34.5 0.81–1.19s; `mattn/go-sqlite3` v1.14.24 0.52–0.90s. This is a directional microbenchmark, not a production telemetry workload or release latency budget.
- Driver/dependency import modules occupy roughly 304 MiB in the Go module cache; that is source/module footprint, not binary/image size. The latter is separately measured in CI/Docker evidence.
- Linux/amd64 is exercised locally. arm64 was not measured.

The pure-Go choice accepts slower synthetic bulk writes in exchange for CGO-free cross-builds and simpler static container builds. Revisit only if representative batched telemetry benchmarks miss an agreed workload budget or maintenance/correctness evidence changes.

## BDR-020 — Review discipline

The project uses FIFO review discipline.

Do not stack noisy pushes onto a PR waiting for exact-head review.

Validate all prior findings, bundle valid fixes, then push once.

Dependent work proceeds after reviewed/merged prerequisites unless explicitly planned otherwise.


## BDR-021 — Dedicated worktree per feature/PR

Every implementation feature/PR uses exactly one dedicated branch and one dedicated git worktree.

```text
1 feature / PR = 1 branch = 1 worktree
```

The primary checkout is coordination-only and must not contain feature implementation work.

Why:

- isolates simultaneous Codex/agent sessions;
- prevents accidental branch switching and cross-feature staging;
- keeps build/test artifacts attributable to one PR;
- makes review fixes return to the exact PR environment;
- permits safe parallel work only when items are truly independent.

Naming:

```text
branch:   sprint/<sprint-number>-<feature-slug>
worktree: ../routeweft-wt/s<sprint-number>-<feature-slug>
```

The same worktree is reused for all review fixes on its PR and removed only after merge/close with no uncommitted/unpushed work.

Dependent PRs are based on the reviewed/merged prerequisite by default. Stacked worktrees require explicit sprint-plan authorization.


## BDR-022 — Built-in provider catalog

Routeweft targets all 80 active providers in `docs/PROVIDER_BASELINE.md` as first-class built-ins for broad-provider GA.

First-class does not imply one executor per provider. Shared protocol adapters are preferred.

A provider may ship later than daily-driver beta only when its readiness is explicit; it may not disappear from the requirements matrix without an approved product decision.

## BDR-023 — Unified Generic Provider

Routeweft exposes one Generic Provider product that can advertise any combination of:

- OpenAI Chat Completions;
- OpenAI Responses;
- Anthropic Messages.

A Generic Provider node owns endpoint/prefix/transport capability. Connections own credentials.

Native source-matching transport is preferred. Manual model configuration remains possible when model discovery is absent.

Validation uses the common SSRF policy with explicit trusted-local behavior.

## BDR-024 — Product requirements freeze before Codex

Implementation work does not start until the product requirements freeze is merged to `main`.

The implementation-start gate is PRD §22 plus `docs/REQUIREMENTS_TRACEABILITY.md`.

After the freeze, technical decisions intentionally deferred by BDR (for example SQLite driver selection) are resolved in their named implementation PR; product scope is not reopened casually inside code PRs.
