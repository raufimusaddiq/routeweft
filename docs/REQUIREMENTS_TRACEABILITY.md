# Routeweft Product Requirements Traceability

Status: **Implementation-ready gate**

This file proves that every major product area has a requirement owner, technical destination and implementation/test phase.

| Product area | Requirement source | Technical source | Planned sprint/PR family | Release-blocking evidence |
|---|---|---|---|---|
| Public API + aliases | PRD §5 | SPEC request/protocol sections | Sprint 2 | protocol fixtures, auth/CORS/body/h2c/cancel tests |
| Built-in providers (80) | PRD §6 + PROVIDER_BASELINE | SPEC provider modules (identity, auth, discovery, quota, error mapping, Usage extraction) | Sprint 5 | every provider row implemented/tested for broad-provider GA; shared error-map/Usage fixtures per protocol family |
| Generic Provider | PRD §7 | SPEC Generic Provider section | Sprint 5 | native Chat/Responses/Messages + validation/SSRF/model fallback fixtures |
| Credentials/import/refresh | PRD §8 | SPEC OAuth/provider modules | Sprint 5 | sealed-at-rest store, rotation/singleflight/import fixtures |
| Models/aliases/pricing | PRD §9 | SPEC RuntimeSnapshot/provider catalog | Sprint 2/5 | model list/alias/custom/disabled/dynamic catalog tests |
| Provider/account routing | PRD §10 | SPEC routing/runtime state | Sprint 3 | fill-first/RR/sticky/fallback/quota/cooldown tests; proxy pool binding/policy tests |
| Combo | PRD §11 | SPEC Combo | Sprint 3 | ordered/RR/sticky/capability fixtures |
| Fusion | PRD §11 | SPEC Fusion | Sprint 3 | fan-out/quorum/grace/timeout/judge fixtures |
| Capacity adapters | PRD §11 | SPEC Combo capability | Sprint 3 | empty/deselect/RR/context-trim fixtures |
| RTK/Caveman/Ponytail | PRD §12 | SPEC token saver | Sprint 4 | individual + combination fixtures |
| Headroom | PRD §12 | SPEC token saver | Sprint 4 | fail-open/status/timeout/diagnostic tests |
| PXPIPE | PRD §12 | SPEC token saver | Sprint 4 | transform/health/log/stats/fail-open tests |
| Prompt cache | PRD §12 | SPEC prompt cache | Sprint 4 | N/N+1 outbound-body/cache accounting fixtures |
| Usage/request details | PRD §13, PRD-OBS-002 | SPEC telemetry/control | Sprint 6 | seeded accounting tests (PR41); bounded/redacted request-detail store + retention tests (PR42) |
| Quota | PRD §13 | SPEC runtime/provider usage | Sprint 5/6 | normalized quota states, failure-not-exhaustion, reset/action tests |
| Admin/control API | PRD §16 | SPEC /admin/v1 | Sprint 6 | admin auth/session + writable settings (PR43); session-gated read models, live events, console logs (PR44); backup download/restore-check/activation tests (PR45); client key create/pause/resume/revoke + requireApiKey tests (Sprint 7 Endpoint & Key) |
| UI shell/workflows | PRD §14-15 + UI_STYLE | SPEC UI architecture | Sprint 7 | browser smoke/a11y/theme/mobile tests |
| Security/trusted proxy/SSRF | PRD §17 | SPEC security/transport | Sprint 1/5/8 | negative security fixtures |
| SQLite/snapshot | PRD §18 | SPEC store/runtime | Sprint 1 | migration/mutation/race/restore tests |
| Backup/restore | PRD §18 | SPEC backup/restore | Sprint 1/6/8 | candidate validation + rollback rehearsal |
| Performance | PRD §19 | SPEC/bench | Sprint 1/8 | benchmark report |
| Operations/rollback | PRD §18-20 | RUNBOOK | Sprint 8 | install/upgrade/rollback rehearsal |

## Product blocker check

The following must be zero before implementation begins:

- unclassified public ingress;
- unexplained provider `TBD`;
- ambiguous first-class vs Generic Provider behavior;
- ambiguous Combo/Fusion/capability scope;
- ambiguous token-saver/cache ordering;
- unowned Usage/Quota workflow;
- unresolved standalone storage decision;
- unresolved UI design-system decision.

Technical implementation choices explicitly marked constrained/deferred in BDR are allowed to remain open until their named decision PR.
