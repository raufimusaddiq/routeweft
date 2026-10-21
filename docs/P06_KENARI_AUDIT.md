# P06 Kenari wire qualification (#76)

Reference: `1c3b9f47837d42ea64992063309064af755a14a3`, read with
`git -C /home/ubuntu/9router show 1c3b9f47:<path>`. `/opt/9router` is absent.
The clone's working checkout is not the reference and was not changed.
Paths below are relative to the reference unless prefixed with `internal/`.

Contracts: PRD §5-6 and §21, SPEC §9-10 and §33.1, BDR-009/010/025.
This slice qualifies existing native dispatch and fixes refusal-event numbering.
It adds no credential flow, listing mechanism, quota mechanism or readiness claim.

## Comparison before implementation

| Area | Pin behavior and source | Routeweft at base `19e6d7b` | This slice / remaining work |
| --- | --- | --- | --- |
| Responses tools | `open-sse/providers/registry/kenari.js:27-34`; `open-sse/translator/concerns/responsesFunctionTools.js:25-122,140-261`: strip `text.format`, declare custom tools as functions, convert history and restore custom-tool replies | `internal/providers/wire/kenari_responses.go:98-120,355-399`; PR #154 supplied both halves; #162 preserves delta order. `internal/ingress/openai.go:201-209` retains the `ParseUsage` observed flag | Keep these behaviors and the documented refusal for malformed custom arguments. Regression tests exercise native HTTP replies in both modes. |
| System role | `open-sse/executors/default.js:21-42,97-99`: fold Responses instructions/system text into the first user message | `internal/providers/wire/quirks.go` `foldResponsesSystem`; existing input-preservation divergences are in the PARITY registry-quirk ledger | Exercise the fold with custom tools through the configured app. Chat system messages and Messages system blocks stay native, including existing prompt-cache normalization. |
| Native headers/endpoints | `open-sse/providers/registry/kenari.js:39-54`: Chat `/v1/chat/completions` and Responses `/v1/responses` use bearer auth; Messages `/v1/messages` uses raw `x-api-key`. `open-sse/executors/default.js:132-136,177-183` selects runtime transport URL/auth | `internal/providers/registry/builtins.go:126-130`; `internal/providers/wire/wire.go` `OpenAIHeaders`/`MessagesHeaders` already implement these bindings | Six configured-app cases cover all three protocols, stream and non-stream, credential isolation, unknown fields and system/tools. No additional Kenari header is declared by the pin. |
| Account quota | `open-sse/services/usage/kenari.js:15-51`: bearer GET `/v1/account/quota`; plan name and month/week IDR amounts/reset dates | `internal/providers/usage/apikey.go:469-529` already reads the endpoint. P05 tests cover zero capacity and absent/null/half-present amounts | Reuse P05 unchanged. Ratios/reset times are present; absolute IDR amounts remain absent from the quota read model (#77/U06). Missing amounts remain unknown, and failed reads must retain the last good view. |
| Live suggestions | `open-sse/providers/registry/kenari.js:128-132` declares `/v1/models` and passthrough IDs. `src/app/api/providers/suggested-models/route.js:20-28` accepts `data`, `models` or arrays; `filters.js:8-12` keeps only `endpoints` containing `chat` | `internal/control/api/suggestions.go:22-98,101-128` already implements the filtered advisory fetch; `suggestions_test.go` tests envelopes, filters and failures | Reuse existing suggestions. No new parser or listing hook. |
| Saved-connection listing | `src/app/api/providers/[id]/models/route.js:595-601`: Kenari has no `PROVIDER_MODELS_CONFIG` entry and returns 400 | Shared discovery uses the P03 mechanism (`internal/providers/wire/listing.go`, `internal/control/api/providers.go:727-807`), with no Kenari override. Its default `{base}/models` is not the pin's advertised `/v1/models`; multi-transport listing can select `x-api-key` | A finding, not a new pinned requirement. This fallback remains unverified; manual/seed models and suggestions remain available. Do not count it as certified live connection listing. |
| Connection authentication | `src/app/api/providers/[id]/test/testUtils.js:836-848`: validate via quota because models listing is auth-blind | SPEC §33.1 validation requires an inference probe, not a listing-only success | Preserve the stronger inference check. This slice tests the admin model-test route with the actual Kenari credential/endpoint selection. It does not change onboarding (#76/P04). |

## First-token contract and fixtures

The ticket's phrase resolves to SPEC §33.1, PARITY's R07 progress and incident
row, and `internal/control/api/probes_test.go` (`TestProbeBudgetContract`,
`TestProbeIndependentBudgetsAndFailureEnvelopes`, native cancellation tests).
The pin's `src/app/api/models/test/ping.js:99-136,148-164,256-280` treats content,
reasoning and tool deltas as reachability and cancels after the first output.

`TestKenariNativeModelProbesStopAtFirstOutput` sends real HTTP through
`POST /admin/v1/connections/{id}/test-models` using the built-in Kenari spec.
Chat returns reasoning, Responses a function-call item, Messages a text delta.
Each mock then waits for cancellation without completing generation. The test
checks native auth/path, streaming, tested/upstream model attribution and secret
omission. R07's existing tests retain the 200-second model / 600-second aggregate
budgets, empty/error stream rejection and own-timeout versus parent-cancel verdicts.

Fixtures are synthetic literals derived from the pin's protocol shapes and
`responsesFunctionTools.js`, not captured traffic. No live provider qualification
or per-model thinking certification is claimed.

## Sequence fix

The raw `noteSequence` scan still existed after #162. New tests fail against
`19e6d7b`: nested JSON or SSE comments can supply the wrong number; legal JSON
whitespace and multiline SSE data can hide the real one. JSON string content is
also covered, although that case already passes at the base.

The rewriter now reads only top-level `sequence_number` from joined SSE data.
Absent/null numbers leave the prior sequence intact. Unchanged events retain
their original bytes. No-custom-tool requests still bypass the wrapper entirely.
The existing 4 MiB bound, refusal code and tool-delta ordering stay in place.

Storage/migrations: none. Security: no new outbound destination or credential
handling; injected refusal numbering no longer trusts comments/nested fields.
The active custom-tool wrapper now decodes every SSE data object for numbering;
benchmark measurements and verification are recorded in PARITY §6.17.
