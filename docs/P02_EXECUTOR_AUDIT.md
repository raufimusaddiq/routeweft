# P02 specialized executor audit

Source: `/home/ubuntu/9router`, read only with `git show 1c3b9f47:<path>`.
`/opt/9router` is absent. All oracle citations below are at that pin;
`E/` means `open-sse/executors/`, `R/` means
`open-sse/providers/registry/`. This audits executor requirements, not live service
availability. PRD §6/§8, SPEC §11/§14/§36 and BDR-009/010 apply.

The ticket's provider list is not an executor inventory. `E/index.js:32-60`
selects specialized executors; `:63-68` also aliases `mmf` to `mimo-free`.
GitLab and Cline use DefaultExecutor. Conversely, Kimchi, OpenCode Go,
Perplexity Web and Vertex Partner need work even though the ticket does not name
them individually. “Free provider” describes neither an authentication scheme nor
a wire protocol. The MiMo free bootstrap is a fingerprint POST returning `jwt`
(`E/mimo-free.js:79-105`), not a separate challenge protocol; older PARITY gap
wording saying “challenge” is not evidence of one.

API.airforce forced streaming (#171), CommandCode (#170),
Cursor onboarding/import (#174), Kenari native protocols (#175) and listing
(#172) remain as landed.

## Per-provider comparison

Only differences from shared OpenAI dispatch are listed. No signing, cookie,
forced-stream or inspection requirement is inferred from a provider's name.
“Partial” means the listed local mechanisms exist but do not qualify the complete
executor. Existing wire hooks are in `internal/providers/wire/wire.go`;
protocol legs are under `internal/protocol/`; gating is in
`internal/ingress/translate.go` and registry readiness metadata.

| Provider | Pin behavior and source | Routeweft disposition after this slice |
|---|---|---|
| antigravity | Multi-host `/v1internal:{action}?alt=sse`, OAuth/UA (`E/antigravity.js:118-135`); project/model/request envelope, numeric session, thought signatures, tool-schema/name repair, output cap, prompt rewrite and requestType removal (`:137-311`); header/body reset hints, transient retry/backoff (`:356-447`). | PARTIAL: Gemini conversion/signature cache exists; transport/project/client binding and retry policy absent. Pin's branding rewrite is not ported. Remains unwired. Standalone image path is outside PRD §4.2. |
| gemini-cli | Cloud Code `:{action}?alt=sse`, bearer, model-sensitive UA/API-client headers, `{project,model,request}` (`E/gemini-cli.js:10-35`); 429 Google RetryInfo (`:38-52`). | PARTIAL: conversion legs exist; **RetryInfo body hint implemented here**; target/project/client binding remains absent and wire stays unwired. |
| cursor | Custom Connect-RPC bidirectional protobuf exchange, not OpenAI HTTP (`E/cursor.js:384-410,490-955`, selected at `E/index.js:42`). | PARTIAL: existing `wire/cursor.go` and `protocol/cursor` transport/codec remain; public dispatch stays unwired. No repeat of #174 import/onboarding. |
| qoder | Target query (`E/qoder.js:604-606`), PAT preflight/local credential guards (`:613-659`), agent envelope plus encoded body and COSY signing (`:661-703`), strict proxy/no replay and proprietary SSE wrapper (`:706-735`). | PARTIAL: final-byte COSY hook exists (`wire/qoder.go`); full envelope/encoding, PAT exchange and proprietary stream leg absent. Signing arbitrary Chat bytes is not a complete executor. Local config failures must remain local; pin's synthetic auth failures are not a requirement to cool an account. |
| kiro | AWS/IDE identity and profile/region headers (`E/kiro.js:235-297`), regional endpoint order/fallback (`:299-342`), conversation body (`:344-362`), eventstream decoding/integrity recovery (`:364-628`, `:629-1190`). | PARTIAL: existing header/profile/endpoint hooks and AWS eventstream legs; no public wiring or full integrity-recovery parity. Session replay remains deliberately absent (PARITY §6.12). |
| vertex | SA JSON/ADC/pre-set bearer/raw-key selection; project/location/model generateContent URL and SSE query (`E/vertex.js:66-110`), bearer headers (`:112-123`), token mint/refresh (`:125-160`). | PARTIAL: Gemini conversion exists; auth selection, project/location URL and minting absent from serving. Remains unwired. |
| vertex-partner | Same auth family, project-scoped `/locations/global/endpoints/openapi/chat/completions`; raw-key project inference (`E/vertex.js:76-81,158-174`). | PARTIAL: `wire/targets.go` project endpoint exists; SA/ADC minting, raw-key mode and project inference absent. |
| github | Copilot token/fingerprint/request id (`E/github.js:36-54`); Chat content/token sanitation (`:60-115`); 400-body-triggered Responses escalation with a known-model cache, or Claude Messages with forced streaming and stream folding (`:118-347`); two-stage token refresh (`:349-438`). | PARTIAL: fingerprint table, Chat body repair and Chat/Responses transports exist. Exact model-selected Messages dispatch/forced fold and request-id parity remain open. Credential refresh is outside this slice. |
| gitlab | **No specialized executor** (`E/index.js:32-60,87-91`); registry bearer endpoint `https://gitlab.com/api/v4/chat/completions` (`R/gitlab.js:16-23`). | Shared bearer Chat endpoint matches. PAT/OAuth helper is credential workflow (#174), not a missing signing/envelope/stream executor. Provider-specific qualification remains open. |
| iflow | HMAC over UA/session/timestamp; session/signature/timestamp headers (`E/iflow.js:29-85`); base URL override and identity request transform (`:87-103`). | Existing `wire/iflow.go` implements signing. Cookie import obtains credentials; inference does **not** send a web-session Cookie at the pin. No cookie executor should be invented for it. |
| codebuddy-cn | Forced stream, system-filter replacement, optional reasoning normalization (`E/codebuddy-cn.js:18-65`); frequency-limit/reset error parsing (`:68-70`); CLI identity (`R/codebuddy-cn.js:28-35`). | Existing `wire/codebuddy.go` forces/folds streams, strips empty tool deltas, parses limits. Deliberately keeps caller system text and Routeweft identity; no filter-prompt replacement. |
| codebuddy-intl | Forced stream/reasoning, fixed system prompt replacing caller system/developer and typed user content (`E/codebuddy-intl.js:17-40`); same error parser (`:43-45`). | Existing hooks implement wire shape/limits but preserve caller prompts rather than inject the pin's identity. These divergences remain explicit in PARITY. |
| cline | **No specialized executor**; declared auth hooks run before generic auth (`E/default.js:177-183`); Cline refresh (`:333-350`). Non-stream envelope is handled outside the executor (`open-sse/shared/clineEnvelope.js:14-18`). | Existing `providers/cline` dual-auth headers and success-envelope unwrapping, registered via `clineHook`. Ticket's general claim of an absent Cline executor is incorrect. Complete gateway qualification remains open. |
| clinepass | Same default path/auth and Cline envelope as Cline (`E/index.js:32-60,87-91`; `R/clinepass.js:22-36`). | Existing shared `clineHook`; dual-auth/onboarding stays with #174. No duplicate executor needed. |
| codex | Account/session/beta/originator headers and compact target (`E/codex.js:219-247`); initial SSE error inspection with replay/account fallback (`:280-386`); precise 429 reset (`:389-411`); forced stream/store=false/input/tool/reasoning/allowlist normalization (`:417-520`). | PARTIAL: existing account/originator header hook/native Responses; session/beta identity remains incomplete. **429 resets implemented here**. Compact-specific headers/body, initial SSE inspection/replay and full normalization remain open; no claim that a Responses label supplies them. |
| grok-cli | Responses target and CLI identity/turn/device/account headers (`E/grok-cli.js:349-398`); 402 spending-limit error fields (`:400-416`); input/tools/reasoning normalization, forced streaming/store=false (`:419-526`); stable agent id (`:528-550`). | PARTIAL: Responses protocol and OAuth exist, but executor identity/turn state, normalization and forced streaming are not implemented as a serving hook. Shared status mapping is not full 402 error parity. |
| grok-web | Web-message envelope/model mode and browser headers (`E/grok-web.js:226-283`); `sso` Cookie (`:285-290`); web stream codec (`:61-219`) and explicit HTTP error messages (`:307-326`). | MISSING: registry declaration is not a cookie session, envelope or codec. No implementation in this slice. |
| xiaomi-mimo | Only Desktop preview models switch to account `/api/route/chat/completions` with Cookie, default thinking/params (`E/xiaomi-mimo.js:4-7,30-68`); async session handshake and one 401 refresh (`:71-92`). Cloud models keep DefaultExecutor. | PARTIAL: cloud Chat/Messages exists; preview session, target switch and retry absent. A blanket Xiaomi cookie rule would break cloud API-key calls. |
| xiaomi-tokenplan | Region-selected Chat vs Anthropic path (`E/xiaomi-tokenplan.js:11-20`). | Existing `wire/targets.go` region/transport hook. Unknown region preserves configured host rather than silently selecting another credential destination. |
| zed | Model-to-provider resolution (`E/zed.js:226-244`); `/completions` envelope with provider_request, thread/prompt IDs and Zed capability headers (`:246-281`); token-mediated fetch, provider-specific stream wrapping (`:100-219`), trial_blocked error (`:284-305`). | MISSING: generic Chat declaration lacks token exchange, envelope, `/completions` and wrapper. This slice does not enable it. |
| azure | Deployment endpoint/api-version (`E/azure.js:8-24`), api-key/bearer selection (`:26-52`), identity body transform (`:54-56`). | Existing `wire/targets.go` target/api-key hook. Deliberately refuses missing endpoint/invalid deployment instead of pin's environment/OpenAI fallback; local configuration is not provider health. |
| ollama-local | **Only** connection-specific local host + `/api/chat` target override (`E/ollama-local.js:9-11`); remaining behavior is DefaultExecutor. | PARTIAL: native Ollama NDJSON leg exists; pin's `resolveOllamaLocalHost` setting precedence lacks a provider-specific serving fixture. No signing/cookie envelope exists to port. |
| ollama | **No specialized executor** (`E/index.js:32-60,87-91`); registry Ollama wire (`R/ollama.js:16-26`). | Native Ollama NDJSON protocol exists. Hosted bearer/endpoint qualification remains open, not a hidden special executor. |
| kimchi | Provider-specific Claude/thinking/system/tool/cache artifacts sanitation (`E/kimchi.js:4-93,100-120`). | PARTIAL: existing `wire/quirks_chat.go` sanitation uses model name rather than pin's live model metadata; unsupported system shapes remain intact. Ticket omitted this executor. |
| opencode | Session/request scoped headers, public bearer and model-selected Chat/Responses/Messages target (`E/opencode.js:417-426,471-507`); forced streaming, Responses tools/state/reasoning, fingerprint tools (`:428-464`). | PARTIAL: Chat target/forced-stream hook exists. Other transports are explicitly refused; session/fingerprint/model rules remain open. |
| opencode-go | Model-dependent Responses target (`E/opencode-go.js:133-136`), session headers (`:139-170`), prior-response stripping/forced Responses stream/store=false, Luna tools/input normalization (`:173-208`). | MISSING executor: declared protocols alone lack these mechanics. Ticket omitted this separate executor. |
| mimo-free | Fingerprint bootstrap JWT/cache helpers (`E/mimo-free.js:1-105`), source/affinity headers and system marker (`:113-129`), fresh JWT and one 401/403 bootstrap retry (`:131-163`). | MISSING executor; correct free Chat URL alone does not authenticate. No bootstrap/cache implemented here. |
| mmf | Alias to mimo-free (`E/index.js:68`). | Same missing executor; not a separate mechanism to implement. |
| perplexity-web | Model/thinking selection, conversation continuation, perplexity_ask envelope (`E/perplexity-web.js:396-436`), bearer **or** session cookie (`:438-449`), web answer/citation stream/fold (`:85-389,487-503`), HTTP error messages (`:467-478`). | MISSING: no cookie/envelope/codec executor; ticket omitted this provider. |
| byteplus, cloudflare-ai, nvidia | Registry free-tier entries, no specialized executors (`E/index.js:32-60,87-91`; `R/byteplus.js:20-24`, `R/cloudflare-ai.js:21-33`, `R/nvidia.js:17-23`). | Shared API-key mechanisms; Cloudflare has an existing account target hook and native System One result-envelope handling. “Free” does not imply no-auth/bootstrap or the MiMo executor. |

Hidden/non-product `trae`, `windsurf`, `devin-cli` also have specialized factories
(`E/index.js:58-60`). PRD-PROV-003 excludes them; this is not an excuse to remove
any active provider. CommandCode and API.airforce remain covered by PARITY
§6.13/§6.14; no changes to their executors or tests here.

All other active providers retain the per-provider `R/<id>.js:<line>`
comparison in PARITY §6.14. They use DefaultExecutor rather than a specialized
factory at this pin; registry-level target/header/body/forceStream requirements
still apply. In particular, the already-recorded OpenAI forced-stream gap and
OpenRouter referer/title gap remain open. A factory audit does not close them.

## Implemented mechanism: error-body cooldown hints

`wire/error_reset.go` registers both providers at the existing ClassifyError seam.
Codex's exact `usage_limit_reached` 429 reads a future numeric Unix-seconds
`resets_at`, then a positive numeric `resets_in_seconds`, matching
`E/codex.js:389-411`. Gemini CLI reads the typed Google RetryInfo seconds duration
on 429, matching `E/gemini-cli.js:38-52`. Durations retain fractional seconds.

Invalid, missing, null, zero, negative, overflowing and mistyped values produce
no hint. Google hints require protobuf seconds notation, not arbitrary Go units.
Codex absolute times beyond the int64 Unix-nanosecond range and delays beyond
Go's duration range are ignored; they never wrap into an immediate reset.
This is stricter than the pin's unvalidated truthy retryDelay forwarding.

The existing classification merger keeps header hints when no valid body hint
exists; a valid body reset has SPEC §14 precedence. Status and body bytes remain
unchanged. Only actual upstream 429 responses qualify: local config failures and
401/403 cannot become a quota lock through these hooks. No SQLite/schema,
credential storage, outbound target, transport, dependency, success-path parsing
or shared mutable state changes.

Gemini CLI still cannot dispatch publicly. This is a tested dormant hook for its
future binding, not evidence that the provider works. `ingress.unwired`, readiness
metadata, and all other routing decisions are unchanged. Native Codex dispatch
uses its hook immediately.

Evidence: `TestPinnedErrorBodyResets` asserts the pinned JSON wire fields and
negative cases through registered hooks. `TestCodexBodyResetReachesDispatchCooldown`
uses real mock HTTP/native Responses dispatch and checks precise cooldown plus
byte-identical upstream errors. `TestBodyResetRefinesWithoutErasingHeaderHints`
checks Google's classification merge and retention after malformed reads.
No live providers or real credentials were used.

## Verification and performance

Focused parser/dispatch/unwired tests, `go build ./...`, `go vet ./...`, the
pinned inventory and full-package targeted race tests for `internal/providers/wire`
and `internal/ingress` pass. The closed hook-registration fixture was updated
to include Gemini CLI after the first race run identified the required addition. Go is installed at `/home/ubuntu/go-toolchain/go/bin/go` on this
host; the brief's `/tmp/s115j-goroot/go/bin` is absent. GOPATH and GOCACHE are
isolated at `/tmp/p02-gopath` and `/tmp/p02-gocache`.

On this loaded AMD EPYC 9754 host (Go 1.27.3, `-benchmem`, default two CPUs),
`BenchmarkPinnedErrorBodyResets` measured Codex at 7.784 µs/op, 272 B/op,
9 allocations; Gemini CLI at 13.679 µs/op, 192 B/op, 5 allocations. These are
error-parser costs only, not end-to-end inference benchmarks. Successful
requests perform no additional parsing or hook call.
