# Routeweft Sprint Plan

Status: **Execution sequence**

The project is built in small reviewable PRs. Do not turn the first sprint into a whole-product rewrite.

## Mandatory execution model

Every implementation PR below runs in its own dedicated branch and git worktree.

```text
1 feature / PR = 1 branch = 1 worktree
```

Default naming:

```text
branch:   sprint/<sprint-number>-<feature-slug>
worktree: ../routeweft-wt/s<sprint-number>-<feature-slug>
```

Examples:

```text
Sprint 1 / repo scaffold:
  branch   sprint/1-repo-scaffold
  worktree ../routeweft-wt/s1-repo-scaffold

Sprint 3 / full Combo:
  branch   sprint/3-combo
  worktree ../routeweft-wt/s3-combo
```

The primary checkout is coordination-only.

Unless a section explicitly marks PRs as independent/parallel, a dependent PR starts only after its prerequisite is approved/merged. Update `main`, then create the next worktree from the new `origin/main`.

Review fixes remain in the same PR worktree for the PR's entire lifecycle.

The exact create/resume/cleanup commands are normative in `AGENTS.md` and `docs/RUNBOOK.md`.


## Sprint 0 — Foundation

### PR 1: foundation docs

Deliver:

- README;
- PRD;
- technical spec;
- BDR;
- AGENTS.md;
- UI style contract;
- runbook;
- sprint plan.

Gate:

- exact-head review approved;
- no unresolved scope/architecture blocker.

## Sprint 0.5 — Product requirements freeze

### PR 2: complete product requirements

Deliver:

- implementation-ready PRD;
- 80-provider first-class requirement matrix with no TBD rows;
- built-in vs Generic Provider definition;
- public route/alias/auth/body/h2c compatibility;
- exact Combo/Fusion/capability scope;
- exact RTK/Caveman/Ponytail/Headroom/PXPIPE/cache scope;
- Usage/Quota/control-plane workflows;
- requirements traceability;
- BDR/SPEC alignment.

Gate:

- docs-only PR reviewed/merged;
- provider baseline contains exactly 80 active rows;
- no product-area blocker in REQUIREMENTS_TRACEABILITY;
- implementation-start gate in PRD §22 is satisfied.

**No Codex implementation sprint begins before Sprint 0.5 is merged.**

## Sprint 1 — Executable skeleton and correctness harness

### PR 3: repository/tooling scaffold

Deliver:

- `go.mod`;
- `cmd/routeweft`;
- package skeleton matching SPEC;
- `ui/` Vite React TypeScript shell;
- Makefile/task commands;
- Docker multi-stage skeleton;
- CI;
- build info/version;
- `/health/live`.

No provider implementation yet.

Gate:

- Go test/build;
- UI typecheck/build;
- container starts;
- no Node runtime in server image target.

### PR 4: SQLite + migrations + RuntimeSnapshot

Deliver:

- choose SQLite driver per BDR-019 evidence;
- schema v1;
- WAL/busy timeout;
- store interfaces;
- migration runner;
- config compiler;
- RuntimeSnapshot manager;
- RuntimeState skeleton;
- readiness;
- config revision;
- atomic mutation protocol;
- backup/restore primitives.

Gate:

- fresh DB;
- restart;
- mutation failure;
- snapshot race tests;
- backup/restore integration;
- race detector.

### PR 5: protocol fixtures + mock upstream + benchmark harness

Deliver deterministic fixtures for:

- OpenAI Chat;
- Responses;
- Anthropic Messages;
- Gemini compatibility;
- Ollama compatibility;
- System One;
- streaming termination;
- cancellation;
- auth/error mapping.

Deliver local mock upstream and router benchmark.

This harness becomes the compatibility oracle for later PRs.

## Sprint 2 — Core ingress

### PR 6: API key/auth + model discovery

Deliver:

- client API-key validation/index;
- `GET /v1`;
- model list/info;
- alias/custom/disabled model compile primitives;
- request IDs/limits/CORS.

Implementation scope: compiled key digest index and key lifecycle primitives;
public API discovery/model list/detail routes; normalized provider-model,
alias, custom-model and disabled-model snapshot inputs. This PR does not add
inference POST handlers or an admin API/UI; key/model management is exposed as
Go runtime mutation primitives until the control API sprint. Request-body
limits and CORS policy are configured by `ROUTEWEFT_MAX_BODY_BYTES` and
`ROUTEWEFT_CORS_ORIGINS`; no cross-origin origin is allowed by default.

### PR 7: OpenAI Chat

Deliver native path first, then translation hooks.

### PR 8: OpenAI Responses + compact

Cover tools, parallel tool calls, multi-turn fields, stream terminal behavior.

### PR 9: Anthropic Messages + count_tokens

Cover tools/thinking/cache-control preservation.

### PR 10: System One + Gemini/Ollama compatibility

LLM-only compatibility; no standalone media endpoints.

## Sprint 3 — Routing

### PR 11: providers/accounts/fallback

Deliver:

- provider registry;
- shared protocol adapter binding;
- account strategies;
- `providerStrategies`;
- cooldown/error classification;
- quota state model;
- proxy transport integration.

### PR 12: full Combo

Deliver:

- CRUD/store/compiler;
- fallback;
- RR/sticky;
- `comboStrategies`;
- capability detection/reorder;
- capacity adapters;
- context trimming.

### PR 13: Fusion

Separate PR due fan-out/concurrency/cost behavior.

Deliver panel/judge/quorum/grace/hard-timeout/tool-history semantics.

## Sprint 4 — Prompt efficiency

### PR 14: RTK + Caveman + Ponytail

### PR 15: Headroom

### PR 16: PXPIPE

### PR 17: prompt-cache anchors + cache accounting

Gate Sprint 4 with N/N+1 cache stability fixtures.

## Sprint 5 — Provider breadth and credentials

Provider PRs are grouped by protocol/auth family, not one giant catalog PR.

Example groups:

1. generic OpenAI-compatible/API-key;
2. native OpenAI/Codex;
3. Anthropic/Claude;
4. Gemini/Google;
5. OAuth-specialized providers;
6. cookie/PAT/no-auth provider modules.

Each provider group needs:

- auth;
- refresh/import where relevant;
- model discovery;
- quota/reset;
- error mapping;
- proxy behavior;
- Usage extraction;
- fixtures.

## Sprint 6 — Telemetry and control plane

### PR: Usage/telemetry

Bounded queue, batching, critical accounting policy.

### PR: request details/logs

Redaction and retention.

### PR: control API

`/admin/v1` resources and overview read model.

### PR: auth/settings/backup

Admin login/session, settings, DB backup/restore API.

## Sprint 7 — Static UI

Port in this order:

1. app shell/theme/navigation;
2. Overview;
3. Endpoint & Key;
4. Providers;
5. Combo & Capability Adapter;
6. System One;
7. Usage;
8. Quota;
9. Token Saver;
10. Console Log;
11. Settings.

Every UI PR follows `UI_STYLE.md`.

Do not combine the port with a visual redesign.

## Sprint 8 — Hardening and daily-drive release

Deliver:

- security/SSRF suite;
- large-body tests;
- cancellation soak;
- 1/10/50/100 stream concurrency;
- Fusion concurrency;
- OAuth refresh stress;
- SQLite contention;
- backup/restore rehearsal;
- container-size budget;
- RSS/CPU benchmarks;
- browser smoke/a11y;
- upgrade/rollback rehearsal;
- operator runbook verification.

## Review protocol for every sprint

Before starting each PR, create or resume its dedicated worktree. Never implement two feature PRs from the same worktree.


1. one coherent push;
2. wait for exact-head review;
3. inspect all current/prior findings;
4. bundle valid fixes;
5. push once;
6. wait for new exact-head review;
7. merge;
8. advance dependency chain.

Do not keep pushing while the reviewer is still working through older heads.

## Definition of Sprint execution success

A sprint is complete when its merged PRs:

- satisfy their listed gates;
- update docs for any deliberate contract change;
- introduce no hidden architecture dependency;
- leave main buildable/runnable;
- do not require future PRs to "temporarily" violate an accepted BDR.
