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
| BDR-015 | Routeweft-owned graphite-rule visual language and static UI | Accepted; visual part superseded by BDR-027 |
| BDR-016 | No standalone media product surfaces | Accepted |
| BDR-017 | One application writer per SQLite DB | Accepted |
| BDR-018 | Default listen port 21128 | Accepted |
| BDR-019 | Exact SQLite Go driver | Accepted: `modernc.org/sqlite` v1.34.5 |
| BDR-020 | Strict FIFO review discipline | Accepted |
| BDR-021 | One feature/PR per dedicated worktree | Accepted |
| BDR-022 | 81-provider built-in catalog is a first-class GA target | Accepted |
| BDR-023 | Generic Provider unifies Chat/Responses/Messages compatible nodes | Accepted |
| BDR-024 | Product requirements freeze gates Codex implementation | Accepted |
| BDR-025 | OpenAI Chat Completions is the conversion hub | Accepted |
| BDR-026 | Model capabilities are generated from the parity pin | Accepted |
| BDR-027 | Retro neo-brutalist visual language (supersedes BDR-015's visual part) | Accepted; direction pending owner approval of #263 previews |

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

Do not force every request through the conversion hub (BDR-025) if no translation is required.

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

Owner decision #98 (2026-10-19) supersedes the LiteRouter v1 visual reference.
Routeweft owns both its visual language and its static UI implementation.

`docs/UI_STYLE.md` defines the graphite rule: cool neutral flat surfaces,
ink-on-paper primary actions, ruled sections instead of cards, 0/2px geometry,
geometric sans prose, monospace operational values and restrained motion.
Color carries state only. Light, dark, system, keyboard access and WCAG AA
contrast remain required.

LiteRouter remains a behavioral reference, not a visual acceptance criterion.
Navigation groups/items/order/labels/icons/hashes and operator workflows stay
unchanged. This decision authorizes no API, backend or product-scope change.

The visual part of this decision (graphite rule, flat with no shadow, ruled
sections instead of cards, 0/2px geometry) is superseded by BDR-027. Ownership,
the static UI, navigation, workflows, WCAG AA and LiteRouter as a behavioral
reference only stay in force.

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

Routeweft targets all 81 active providers in `docs/PROVIDER_BASELINE.md` as first-class built-ins for broad-provider GA.

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

## BDR-025 — OpenAI Chat Completions is the conversion hub

Decision (owner, 2026-10-19): when a request arrives in one wire format and the selected provider speaks another, Routeweft converts through OpenAI Chat Completions, exactly as LiteRouter does. There is no separate canonical intermediate type; the `internal/protocol/canonical` package was never built and is removed.

Reference: LiteRouter `open-sse/translator/index.js` `translateRequest` and `translateResponse`: use a registered direct pair if one exists, otherwise source→OpenAI then OpenAI→target. Pairs are registered per direction in `translator/request/*.js` and `translator/response/*.js`, so each format adds one or two legs instead of a leg per other format.

Rules:

- Native passthrough and native normalization (BDR-010) never enter the hub.
- A format joins by adding legs to and from Chat. Responses↔Messages is Responses→Chat→Messages, not a direct converter.
- Where LiteRouter registers a direct pair that bypasses the hub, Routeweft mirrors the bypass rather than forcing the pair through Chat. The pairs are derived from LiteRouter's translator directory at test time, not listed here.
- The format set and every pair are the pinned oracle's, read from its source by the drift tests in `internal/parity`. A format or pair that LiteRouter has and Routeweft lacks fails those tests unless it is listed as tracked work in that package. Tracked work only ever shrinks; it is never a reason to make a provider unroutable.
- Routeweft's `internal/protocol/hub` is the single declaration of which legs exist. Provider selection asks it whether a source/target pair is convertible.

Reopening this decision requires a reviewed BDR change.

## BDR-026 — Model capabilities are generated from the parity pin

Decision (#125): generate checked-in declarative Go capability data from the
parity pin, following the provider seed generation pattern. Keep resolution in
hand-written tested code, corrections outside the generated file, and publish
private immutable catalog copies through `RuntimeSnapshot`. Do not install a
runtime catalog source or fetch models.dev: the pin is the behavioral reference,
and mutable remote data would introduce an SSRF surface and availability/drift
failure modes. [MODEL_CAPABILITIES.md](MODEL_CAPABILITIES.md) records the exact
fields, lookup order, override precedence and remaining #119 integration.

## BDR-027 — Retro neo-brutalist visual language

Decision (owner, Suhu, 2026-10-27, issue #263): restyle the control plane into a
retro neo-brutalist language in the family of Richmod and Antares. This
supersedes only the visual part of BDR-015: "cool neutral flat surfaces,
ink-on-paper primary actions, ruled sections instead of cards, 0/2px geometry"
and UI_STYLE's "No gradient, blur, glass, shadow or glow anywhere".

Borrowed traits: flat fills; a crisp ink outline only where a box means
something; depth as a hard offset shadow with no blur; tinted paper; a few
pastels that each mean one thing; one highlighter colour; light and dark set the
same token names.

Kept as Routeweft's own, not borrowed: the weave mark; square 0/2px geometry
(Richmod and Antares are rounded); Space Grotesk prose with JetBrains Mono for
technical values; cyan `--live` for current data; the editorial layout of
UI_STYLE §1 and §5; the 11/12/16/28px type scale. Excluded: Richmod's Fraunces
and cream/teal palette, Antares' cobalt action colour, marker gradient, pills,
rotated tags and two-tone focus halo.

Still forbidden: blur, glass, glow, backdrop filters, gradients and soft
shadows. `box-shadow` is allowed only as the hard-offset tokens `--hard` and
`--hard-sm`.

`docs/UI_STYLE.md` carries the tokens and the acceptance checks. Phase 1 of #263
records the contract and an isolated preview (`ui/preview/263/`). The direction
is the owner's choice from those screenshots, and the token tables follow that
choice before phase 2. The owner judged round 1 (A, B) "not brutal enough"
(2026-10-27), so round 2 adds C and D. Options, with the guards each rewrites
(file:line in `ui/preview/263/README.md`):

- A "Loom": square, linen paper, 4px shadow, lime marker. Fewest changes, but
  judged too soft. Rewrites the error-border assert (`shell-smoke.cjs:1553`).
- B "Patchwork": rounded tiles, pills, pastel patches. Still soft. Rewrites the
  radius guard (`shell-smoke.cjs:55-58`, `:74`) and the error-border assert.
- C "Ledger" (recommended): square everything, a heavy black ruled grid,
  inverted heads and nav block, a vermilion slab with an 8px hard shadow, mono
  capital labels, stamped tags. The hardest surface that keeps Routeweft's
  square identity; rewrites only the error-border assert.
- D "Signal": a poster with a black rail, oversized 64px titles, pills and
  circles, a pink slab with an asymmetric 10x6px shadow, open data. The most
  anti-mainstream; rewrites the radius guard and the type-scale guard
  (`shell-smoke.cjs:60`, `:77-79`, `:1558`).

The "kept as Routeweft's own" list above (square geometry, the type scale)
holds for A and C. Choosing B or D revises it in the same PR that records the
choice. Until
phase 2 lands, `ui/src/style.css` still implements the BDR-015 values and the
smoke guards enforce them; each later phase changes code, guards and this
contract together. Navigation, routes, features, aria, 44px targets, status
never shown by colour alone and no horizontal overflow at 320px are unchanged.
This decision authorizes no API, backend or product-scope change.

