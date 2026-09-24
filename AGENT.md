# Routeweft Agent Instructions

This file is the canonical instruction set for Codex and other implementation agents working in this repository.

## 1. Read order

Before changing implementation, read these files in order:

1. `docs/PRD.md` — product contract and non-goals.
2. `docs/SPEC.md` — normative technical behavior.
3. `docs/BDR.md` — accepted architecture/build decisions.
4. `docs/UI_STYLE.md` — normative visual contract for UI work.
5. `docs/RUNBOOK.md` — operational contract.
6. `docs/SPRINT_PLAN.md` — sequencing and PR boundaries.

When documents conflict, the precedence above applies unless a later approved PR explicitly changes the decision.

## 2. Product boundary

Routeweft is a standalone product.

Do not:

- turn Routeweft into a LiteRouter migration wrapper;
- share a SQLite file, runtime cache, or writable credential state with LiteRouter;
- create a runtime dependency on LiteRouter;
- preserve JavaScript/Next module boundaries merely because they exist elsewhere;
- add standalone image/video generation, TTS/STT, embeddings, or media-provider product surfaces;
- introduce PostgreSQL, Redis, microservices, Kubernetes, or a message broker without an explicit BDR change.

When implementing behavior already proven in LiteRouter, it is acceptable to inspect LiteRouter current implementation/tests as a behavioral oracle. Reimplement the behavior using Routeweft-owned interfaces.

## 3. Core architecture invariants

A successful steady-state inference request must not synchronously require:

- SQLite configuration reads;
- Redis;
- filesystem configuration reads;
- dashboard code;
- configuration recomputation.

Use:

- immutable compiled `RuntimeSnapshot` for request-serving configuration;
- separate mutable `RuntimeState` for RR cursors, sticky counters, cooldowns, breakers, health, and in-flight state;
- pooled upstream HTTP transports;
- bounded asynchronous telemetry;
- SQLite WAL as the single-instance durable store.

Configuration mutations must follow the exact transaction protocol in `docs/SPEC.md`: validate/compile candidate first, commit second, atomic publish third.

## 4. Routing/product behavior

Hard-retain:

- Chat Completions, Responses, Responses Compact, Messages, count-tokens compatibility, System One;
- streaming and non-streaming;
- provider/account fallback;
- fill-first, round-robin, sticky round-robin;
- `providerStrategies` and `comboStrategies`;
- quota/cooldown/health-aware routing;
- model aliases/custom/disabled models;
- Combo CRUD/order/fallback/RR/sticky;
- Fusion + judge + quorum/grace/timeout;
- capability-aware Combo auto-switch;
- capacity-adapter pools and their empty-pool/deselect semantics;
- proxy pools and outbound proxy/no-proxy;
- RTK, Caveman, Ponytail, Headroom, PXPIPE;
- prompt-cache behavior and cached-token accounting;
- Usage, Quota, request details/logs, console logs, provider topology, settings, API keys, auth.

Do not silently simplify a retained behavior in the name of a cleaner rewrite.

## 5. Protocol implementation rules

Prefer the cheapest correct path:

1. native passthrough when safe;
2. native normalization when transforms require parsing;
3. canonical translation only when source/target protocols differ.

Do not decode/re-encode streamed chunks unnecessarily.

Unknown forward-compatible fields should survive native paths where safe.

Cancellation must propagate from client disconnect to upstream work.

## 6. Token saver and prompt-cache order

The request-transform order is normative unless a reviewed BDR changes it:

1. protocol/provider preparation;
2. RTK;
3. Headroom;
4. Caveman;
5. Ponytail;
6. PXPIPE;
7. prompt-cache anchoring on the final transformed body;
8. final provider normalization/dispatch.

Transforms that are defined as fail-open must not turn a valid inference request into a failure.

## 7. UI rules

Routeweft UI follows `docs/UI_STYLE.md`.

Do not invent a new visual system. In particular:

- use neutral warm surfaces;
- use color primarily for state;
- avoid decorative gradients in operator UI;
- use the defined spacing/radius/typography tokens;
- preserve light/dark/system themes;
- use Material Symbols for the main icon vocabulary;
- support keyboard/focus/reduced-motion behavior.

The frontend is static React + TypeScript + Vite. Do not introduce a production Next server runtime.

## 8. PR discipline

Each PR must be small enough to review as one responsibility.

For every PR:

- state the exact goal and non-goals;
- reference relevant PRD/SPEC/BDR sections;
- add/update tests for behavior changed;
- update docs when a contract changes;
- report benchmark impact when touching the hot path;
- report storage/migration impact when touching SQLite;
- report security impact when touching auth/secrets/networking.

Review queue discipline is strict:

1. push a coherent head;
2. wait for review of that exact head;
3. read all current and prior review findings;
4. validate findings against the latest code;
5. bundle all valid fixes into one push;
6. wait for review of the new exact head;
7. do not flood the FIFO reviewer with tiny pushes.

Do not begin the next dependent PR until the current PR is approved/merged unless the sprint plan explicitly permits parallel independent work.

## 9. Testing expectations

At minimum, relevant changes need:

- unit tests;
- race/concurrency tests where shared runtime state changes;
- deterministic protocol fixtures for wire behavior;
- integration tests for SQLite/store mutations;
- cancellation tests for streaming paths;
- negative/security tests for auth/outbound-network changes.

Run `go test -race ./...` for Go code once the scaffold exists.

UI PRs must run typecheck, unit tests, build, and the browser smoke suite defined by the sprint.

## 10. Safety and secrets

Never:

- commit provider credentials, OAuth tokens, API keys, cookies, or real production DB files;
- log Authorization/cookie/provider secrets;
- expose raw provider credentials to the browser;
- send production prompt bodies to fixtures;
- weaken SSRF/private-network protections to make provider validation easier.

Use sanitized fixtures only.

## 11. No speculative architecture

If a decision is intentionally deferred in `docs/BDR.md`, resolve it in the PR named by that decision deadline. Do not make an unrelated implementation PR the place where a project-wide architecture choice is silently introduced.
