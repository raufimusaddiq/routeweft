## Goal

Deliver the Sprint 6 **request details/logs** slice: a bounded, redacted request-detail store with retention, plus a reusable credential-redaction helper for logs and details (PRD-OBS-002, PRD-SEC-001, SPEC §21 diagnostic class).

## Non-goals

No `/requests` or `/logs` control API and no UI (later Sprint 6/7 PRs). This PR defines and persists the diagnostic record and its redaction/retention policy; the ingress producers that populate route-mode/transform diagnostics arrive with the control API and event-stream work.

## Contract references

- PRD: PRD-OBS-002 (request details bounded/redacted; never store auth/refresh/cookie/password/provider secret), PRD-SEC-001 (redact credentials from logs/details), §13 observability
- SPEC: §21 (diagnostic event class), §4 (request_details schema), §28.1 (redaction unit coverage)
- BDR: BDR-013 (bounded async telemetry)
- REQUIREMENTS_TRACEABILITY: Usage/request details row
- Sprint: Sprint 6, request details/logs PR

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

New in `internal/telemetry`:

- `Redact` / `RedactJSON` / `RedactHeaderValue` recursively replace credential-shaped keys (`authorization`, `api key`, `cookie`, `token`, `secret`, `password`, `credential`, `clientsecret`, …) with a fixed placeholder. Matching is substring-based on a normalised key so `Authorization`, `api_key`, `apiKey`, `X-Api-Key`, `refreshToken` all match. Malformed JSON is rejected rather than stored verbatim, because a blob that cannot be parsed cannot be proven free of secrets. Redaction never mutates its input.
- `SQLiteDetailStore` persists one redacted row per request id (`request_details`), bounding the document by the configured `observabilityMaxJsonSize` — an oversized payload is replaced with an `omitted` marker, never a truncated (possibly unredacted) prefix. `PruneNow` deletes rows older than the fixed `DetailRetentionDays` window.
- `Service.WithDetails` attaches a `DetailSink`; `RecordDetail` enqueues a diagnostic event that the batcher routes to the detail sink instead of the usage sink, so request details share the same bounded, shed-first queue. Detail-write failures advance a separate `LostDiagnostics` counter and never the critical `Lost` counter.

App wiring: `App.Initialize` builds the detail store from the compiled `observabilityMaxJsonSize` and attaches it to the telemetry service; `Serve` runs an hourly retention prune for the lifetime of the process.

## Storage / migration impact

- [x] No storage/schema/migration impact.

Uses the existing `request_details` table from migration v1; no schema change.

## Security impact

- [x] Security-boundary impact documented below.

This PR is the redaction boundary for stored/logged request diagnostics. Credential-shaped keys are removed before persistence; malformed payloads are dropped rather than stored; size overruns store an omission marker rather than a partial document. No prompt body is stored by this slice (no producer is wired yet), and no credential value can be reconstructed from a stored detail row.

## Performance impact

- [x] No inference hot-path impact.

Redaction and detail persistence run only in the background batcher on the diagnostic path; the inference request path is unchanged.

## Tests

`gofmt -l internal cmd`; `go build ./...`; `go vet ./...`; `go test -race ./...` (31 packages ok, no FAIL); `git diff --check`; UI `npm run typecheck`, `npm run build`. All pass.

New tests: `internal/telemetry/redact_test.go` (nested/key-variant redaction, non-mutation, malformed-JSON rejection, header redaction), `internal/telemetry/detail_test.go` (redacted persistence, oversized payload safety, upsert + retention prune), and `internal/telemetry/telemetry_test.go` (detail routed to the detail sink, never the usage sink; no-op without a detail sink).

Review fix: the detail upsert now also refreshes `created_at` (`created_at=excluded.created_at`), so a route decision written earlier and refreshed with its final outcome is not immediately deleted by the next retention prune. Covered by `TestDetailStoreRefreshedRowSurvivesPrune`.

## Rollback

Revert the PR. No schema or persisted state changes; the request path returns to the PR41 usage-only writer.
