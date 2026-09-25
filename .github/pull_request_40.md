## Goal

Deliver the Sprint 5 **provider error mapping** and **Usage extraction** slice as one shared, declarative module that every provider group reuses, plus request-path wiring: normalized upstream-error classification recorded into `RuntimeState` cooldown visibility, and normalized token usage (including cache read/create) extracted for every retained protocol family.

## Non-goals

No retry loop / attempt-chain execution (that is the routing-fallback slice) and no telemetry queue, batching, `/usage` API, or request-details storage (Sprint 6). Provider-specific error taxonomy is limited to the categories SPEC §14 names; no new provider behavior is invented.

## Contract references

- PRD: §13 (Usage/Quota), §8 (provider credentials), §10 (routing)
- SPEC: §11 (provider modules own error classification), §14 (cooldown and error classification), §9.3 (token Usage where available), §18 (cache-read/cache-create flow into Usage)
- BDR: BDR-009 (provider identity separate from protocol adapters)
- REQUIREMENTS_TRACEABILITY: built-in providers row (error mapping + Usage extraction)
- Sprint: Sprint 5, per-provider-group "error mapping" and "Usage extraction" deliverables

## Behavior / compatibility

- [x] Public behavior changed intentionally and documented, or no public behavior change.
- [x] Relevant tests/evidence added.
- [x] No retained behavior was silently simplified.

New shared package `internal/providers/shared`:

- `ParseError(family, status, body)` normalizes OpenAI/Responses/Anthropic/Gemini error bodies into `ErrorKind` (auth, quota, rate-limited, overloaded, context-length, not-found, invalid-input, unknown). Numeric error codes (Gemini) and string codes (OpenAI/Anthropic) are both parsed; a silent or malformed body falls back to a status-derived kind so classification never depends on body shape.
- `ParseUsage(family, body)` extracts `promptcache.Usage` for all retained families (OpenAI Chat/Responses `prompt_tokens`/`input_tokens` + `*_details.cached_tokens`, Gemini `usageMetadata`, Ollama `prompt_eval_count`/`eval_count`, Anthropic via the existing parser). Absent usage is reported as not present, never estimated.

`routing.ClassifyError(kind, status)` refines a status classification with a normalized kind while preserving any Retry-After/reset hint from the status path.

Ingress wiring: every non-2xx upstream response is classified through the shared mapping and recorded into `RuntimeState` cooldown visibility (SPEC §14 "cooldown is visible immediately") while the provider error body is still relayed to the client unchanged. The streamed-usage scanner is now protocol-family aware (Anthropic `message_stop`, OpenAI-family `[DONE]`) and non-stream success paths capture usage for all families, replacing the Anthropic-only extraction.

## Storage / migration impact

- [x] No storage/schema/migration impact.

## Security impact

- [x] No security-boundary change.

Error bodies are read only for a bounded prefix (32 KiB) for classification and are relayed verbatim; no upstream credential, header, or body content is logged. Parsing never mutates the response sent to the client.

## Performance impact

- [x] No inference hot-path impact.

Classification and usage extraction are allocation-light JSON decodes over a bounded prefix or a success body already being relayed; the stream scanner reuses the existing tee path and adds no per-chunk buffering beyond one line.

## Tests

`gofmt -l internal cmd`; `go build ./...`; `go vet ./...`; `go test -race ./...` (30 packages ok, no FAIL); `git diff --check`; UI `npm run typecheck`, `npm run build`. All pass.

New tests: `internal/providers/shared/errors_test.go` (provider error-body normalization and status fallback), `internal/providers/shared/usage_test.go` (per-family usage extraction and absent-usage cases), `internal/routing/accounts_test.go` `TestClassifyErrorRefinesNormalizedKinds`, and `internal/ingress/providererror_test.go` (upstream failure maps to cooldown visibility).

## Rollback

Revert the PR. No schema or persisted state changes; the request path returns to the previous Anthropic-only usage extraction and status-only classification.
