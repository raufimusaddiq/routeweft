## Goal

Deliver the Sprint 6 **Usage/telemetry** slice: a bounded asynchronous Usage writer with critical-accounting protection (BDR-013, SPEC §21). The inference path only enqueues; a single background batcher writes `usage_events` (and the `usage_daily` rollup) so a normal success never synchronously writes SQLite.

## Non-goals

No `/usage` or `/requests` control API (later Sprint 6 PRs), no request-detail retention/redaction store, no SSE event stream, and no UI. Diagnostic large-body capture is a defined class here but its producers arrive with the request-details PR.

## Contract references

- PRD: §13 (Usage/Quota), §19 (performance)
- SPEC: §21 (telemetry classes, queue-pressure policy), §4 (schema), §8 (request lifecycle "enqueue critical Usage")
- BDR: BDR-004 (batched Usage writes), BDR-013 (bounded async telemetry with protected critical accounting)
- REQUIREMENTS_TRACEABILITY: Usage/request details row (seeded accounting tests)
- Sprint: Sprint 6, Usage/telemetry PR

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

New package `internal/telemetry`:

- `Service` owns a bounded event queue (`MaxRecords`), a batcher (`BatchSize`, `FlushInterval`, `FlushNow`) and the critical-accounting policy: **critical** events are never silently lost (short bounded backpressure, then eviction of a queued diagnostic, then a visible monotonic loss counter); **diagnostic** events shed first without touching the loss counter; a persistent sink failure marks `Health() == ErrDegraded` and advances the loss counter (SPEC §21 points 1-5).
- `SQLiteSink` writes each batch in one transaction to `usage_events` and maintains the per-day/per-provider/per-model `usage_daily` rollup. Only attributed 2xx successes roll up; failures are retained in `usage_events`.

Ingress change: `Options.OnUsage` (a token-only callback) is replaced by `Options.OnRequestComplete(RequestOutcome)`, one bounded record per finished upstream request carrying request id, provider/model/connection attribution, status, route duration, stream flag and upstream-reported token usage. It fires across Chat, Responses, Gemini, Ollama (native + translated) and Messages, and is never called on the client-cancelled path. `promptcache.Usage` remains the wire-usage type.

App wiring: `App.Initialize` builds the service from the compiled observability settings (`enableObservability`, `observabilityMaxRecords`, `observabilityBatchSize`, `observabilityFlushIntervalMs`) — opt-in, disabled by default — starts the batcher in `Serve` and closes it on shutdown.

## Storage / migration impact

- [x] No storage/schema/migration impact.

Uses the existing `usage_events` and `usage_daily` tables from migration v1; no schema change.

## Security impact

- [x] No security-boundary change.

Usage events carry attribution and token counts only; no prompt/response bodies, credentials, or headers are recorded by this slice.

## Performance impact

- [x] Inference hot-path impact measured/documented below.

The request path performs one non-blocking channel send per completed request; with observability disabled the hook is a nil check. No synchronous SQLite write on the success path (BDR-004/013). `go test -race ./...` stays green across all packages.

## Tests

`gofmt -l internal cmd`; `go build ./...`; `go vet ./...`; `go test -race ./...` (31 packages ok, no FAIL); `git diff --check`; UI `npm run typecheck`, `npm run build`. All pass.

New tests: `internal/telemetry/telemetry_test.go` (batch/flush, diagnostic-first shedding, degraded health + lost counter on persistent failure, disabled no-op), `internal/telemetry/sqlite_test.go` (durable `usage_events` + `usage_daily` rollup, empty-batch no-op), `internal/app/app_test.go` (telemetry wired and settings mapping), and updated ingress tests asserting `RequestOutcome` across Gemini streaming and both Ollama paths.

## Rollback

Revert the PR. No schema or persisted state changes; the request path returns to no accounting writer.
