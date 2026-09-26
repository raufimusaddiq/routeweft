# Routeweft Product Requirements Traceability

Status: **Implementation-ready gate**

This file proves that every major product area has a requirement owner, technical destination and implementation/test phase.

| Product area | Requirement source | Technical source | Planned sprint/PR family | Release-blocking evidence |
|---|---|---|---|---|
| Public API + aliases | PRD §5 | SPEC request/protocol sections | Sprint 2/8 | protocol fixtures, auth/CORS/body/h2c/cancel tests; large-body suite (`internal/ingress/largebody_test.go`) proving 128 MiB-class exact-limit acceptance with byte-exact forwarding, one-byte-over 413 with no upstream leak, inclusive default-limit guard, and streaming cancellation propagation; cancellation soak (`internal/ingress/cancelsoak_test.go`) proving prompt unwinding, no completed-outcome telemetry for cancelled work, and no goroutine/upstream drain leak across repeated streaming and non-streaming disconnects; stream-concurrency suite (`internal/ingress/streamconcurrency_test.go`) proving 1/10/50/100 simultaneous streams each complete byte-intact with a single terminal marker, peak concurrency is tracked within range, and in-flight request accounting returns to zero after the burst |
| Built-in providers (80) | PRD §6 + PROVIDER_BASELINE | SPEC provider modules (identity, auth, discovery, quota, error mapping, Usage extraction) | Sprint 5 | every provider row implemented/tested for broad-provider GA; shared error-map/Usage fixtures per protocol family |
| Generic Provider | PRD §7 | SPEC Generic Provider section | Sprint 5 | native Chat/Responses/Messages + validation/SSRF/model fallback fixtures |
| Credentials/import/refresh | PRD §8 | SPEC OAuth/provider modules | Sprint 5/8 | sealed-at-rest store, rotation/singleflight/import fixtures; OAuth refresh stress suite (`internal/credentials/registry_stress_test.go`) proving singleflight collapses each refresh cycle to one exchange under many callers, memory stays equal to durable state, concurrent imports never lose to a stale exchange, and a superseded refresh serves the newer import |
| Models/aliases/pricing | PRD §9 | SPEC RuntimeSnapshot/provider catalog | Sprint 2/5 | model list/alias/custom/disabled/dynamic catalog tests |
| Provider/account routing | PRD §10 | SPEC routing/runtime state | Sprint 3 | fill-first/RR/sticky/fallback/quota/cooldown tests; proxy pool binding/policy tests |
| Combo | PRD §11 | SPEC Combo | Sprint 3 | ordered/RR/sticky/capability fixtures |
| Fusion | PRD §11 | SPEC Fusion | Sprint 3/8 | fan-out/quorum/grace/timeout/judge fixtures; Fusion concurrency suite (`internal/routing/fusionconcurrency_test.go`) proving the MaxConcurrent cap is enforced (excess runs return `ErrFusionBusy`), per-run panel answers stay isolated with no cross-talk, admission slots release after the burst, and concurrent runs share one frozen config race-free |
| Capacity adapters | PRD §11 | SPEC Combo capability | Sprint 3 | empty/deselect/RR/context-trim fixtures |
| RTK/Caveman/Ponytail | PRD §12 | SPEC token saver | Sprint 4 | individual + combination fixtures |
| Headroom | PRD §12 | SPEC token saver | Sprint 4 | fail-open/status/timeout/diagnostic tests |
| PXPIPE | PRD §12 | SPEC token saver | Sprint 4 | transform/health/log/stats/fail-open tests |
| Prompt cache | PRD §12 | SPEC prompt cache | Sprint 4 | N/N+1 outbound-body/cache accounting fixtures |
| Usage/request details | PRD §13, PRD-OBS-002 | SPEC telemetry/control | Sprint 6/7 | seeded accounting tests (PR41); bounded/redacted request-detail store + retention tests (PR42); bounded usage summary (totals/daily series/provider/model/status, period window) + Usage page (Sprint 7 Usage) |
| Quota | PRD §13 | SPEC runtime/provider usage | Sprint 5/6 | normalized quota states, failure-not-exhaustion, reset/action tests; Quota Tracker page + session-gated background refresh (Sprint 7 Quota) |
| Admin/control API | PRD §16 | SPEC /admin/v1 | Sprint 6/7 | admin auth/session + writable settings (PR43); session-gated read models, live events, console logs (PR44); backup download/restore-check/activation tests (PR45); client key create/pause/resume/revoke + requireApiKey tests (Sprint 7 Endpoint & Key); provider node/connection CRUD, Generic Provider SSRF validation, discovery/manual model, aliases/pricing, proxy assignment, bounded model-test probes (Sprint 7 Providers); Combo CRUD/order + capability-adapter enable/pool validation, empty-pool no-op (Sprint 7 Combo); System One/Jev native typed-endpoint model probe (Sprint 7 System One); bounded usage summary read model (Sprint 7 Usage); session-gated async quota refresh (Sprint 7 Quota); current-password verification and all-session invalidation on admin password rotation, typed settings validation (Sprint 7 Settings) |
| UI shell/workflows | PRD §14-15 + UI_STYLE | SPEC UI architecture | Sprint 7/8 | browser smoke/a11y/theme/mobile tests (`ui/scripts/shell-smoke.cjs`: semantic labels, contrast, landmarks, heading order, focus/skip link, reduced motion); Overview, Endpoint & Key, Providers, Combo & Capability Adapter, System One, Usage, Quota Tracker, Token Saver, paginated/filtered Console Log, and Settings including password rotation and checked backup/restore workflows |
| Security/trusted proxy/SSRF | PRD §17 | SPEC security/transport | Sprint 1/5/8 | negative security fixtures, including global outbound-proxy URL policy; cross-package SSRF suite (`internal/security/suite_test.go`) covering loopback/RFC1918/link-local/metadata/IPv6 corpus, strict vs trusted-local split, redirect non-following, Generic Provider + discovery + Headroom/PXPIPE entry points, spoofed-`X-Forwarded-For` throttle isolation, and malformed-URL shape rejection |
| SQLite/snapshot | PRD §18 | SPEC store/runtime | Sprint 1/8 | migration/mutation/race/restore tests; SQLite contention suite (`internal/store/sqlite/contention_test.go`) proving concurrent readers/writers and independent connection pools complete without busy errors, lost writes, or integrity failures |
| Backup/restore | PRD §18 | SPEC backup/restore | Sprint 1/6/8 | candidate validation + rollback rehearsal (`internal/app/restore_test.go`: backup, non-mutating restore-check, activation, restart persistence, and intact pre-restore rollback artifact) |
| Performance | PRD §19 | SPEC/bench | Sprint 1/8 | benchmark report (`docs/S8_RESOURCE_BENCHMARK.md`) with native/stream CPU, test-process post-burst RSS and live-service health RSS; CI production container-size gate reports actual bytes and enforces the 100 MiB image goal (`.github/workflows/ci.yml`) |
| Operations/rollback | PRD §18-20 | RUNBOOK | Sprint 8 | install/upgrade/rollback rehearsal (`internal/app/upgrade_test.go`: pre-upgrade backup, additive schema upgrade preserving records, older-binary rejection, and rollback to the pre-upgrade backup) |

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
