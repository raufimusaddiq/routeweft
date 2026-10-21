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
(#172) remain as landed. The first slice (#177) added the Codex and Gemini CLI
error-body hints. The second slice (this file's last two sections) enumerates the
pin by mechanism, corrects the ticket where the pin disagrees, and lands one
mechanism for every provider that needs it and can be routed: the registry's
`forceStream` flag with the request contract and stream-head inspection of the
stream-only Responses endpoints (Codex, Grok CLI).

The second slice read the pin from `/home/ubuntu/9router` with
`git -C /home/ubuntu/9router show 1c3b9f47:<path>` (`/opt/9router` is absent
here); its working tree has an unrelated local edit to `scripts/deploy.sh`, which
was not read.

## Verification of the ticket's list (second slice)

The ticket's list was checked against the pin, not trusted. What the pin says,
and what follows from it:

1. **A listed provider that needs nothing special is a finding.** GitLab, Cline
   and ClinePass have no executor (`E/index.js:32-60` lists the factories; `:87-91`
   falls through to `DefaultExecutor`). Hosted `ollama` has none either; only
   `ollama-local` does, and it is a 14-line override of the target (`E/ollama-local.js:9-11`).
   iFlow signs a bearer request with HMAC and never sends a cookie
   (`E/iflow.js:29-85`). "Free provider" is not a mechanism: the free entries
   (`byteplus`, `cloudflare-ai`, `nvidia`) have no executor, and the one free
   provider with an executor, MiMo free, authenticates with a bootstrapped JWT
   (`E/mimo-free.js:79-105`), not a cookie and not a challenge.
2. **An unlisted provider that needs something is a bigger finding.** Kimchi,
   OpenCode, OpenCode Go, MiMo free (and its alias `mmf`), Xiaomi MiMo's preview
   models, Vertex Partner and Perplexity Web need executor work and are not in the
   ticket. Grok CLI and Zed are in it only by the word "Grok" and "Zed".
3. **The biggest miss is that the ticket's mechanisms are not all in executors.**
   `forceStream` is a registry transport field that `chatCore.js:146` reads, so it
   appears in no executor audit and no per-executor list. Ten registry entries
   declare it (table below); of them Routeweft enforced five before this slice
   (API.airforce, CodeBuddy CN and international, OpenCode, and CommandCode on its
   own, unwired wire). Codex and Grok CLI are the two it did not, and
   both are served, "ready" providers.
4. **Codex's request contract was entirely absent.** Routeweft forwarded a client's
   Responses body to the ChatGPT backend as it came: no `store:false`, no
   `stream:true`, no default instructions, no field allowlist, no `-review` model
   mapping. `codexprovider.UpstreamModel` existed and was never called at serving, so
   the `-review` twins that the model listing (#172) adds were sent upstream under
   their own name. A Chat-shaped or non-stream client could not have been served at all.
5. **Antigravity's tool cloaking is dead code at the pin.** `AntigravityExecutor.cloakTools`
   is a static method nothing calls; the call in `translator/index.js:180` is a
   comment. The prompt-branding rewrite (`E/antigravity.js:265-274`) is applied. Neither
   is ported: both exist to keep the provider from recognizing the client.
6. **Cookie sessions are three executors, and the cookie is the small part.** Grok Web
   (`E/grok-web.js:285-290`) and Perplexity Web (`E/perplexity-web.js:446-449`) send a
   cookie together with a browser identity (`E/grok-web.js:263-283`: sec-ch-ua,
   x-statsig-id, Sentry baggage) and their own stream codecs; Xiaomi MiMo's preview models
   mint a session from another application's local Chromium cookie database
   (`shared/mimoAccount.js:36-90,119-174`). The header is a one-liner; the rest is a
   browser impersonation and a desktop-profile reader, which is not an inference-API
   contract. They stay MISSING, with that reason, and are not chosen as the mechanism.
7. **Signing is one provider short of a mechanism.** iFlow's HMAC and Qoder's COSY
   signature are implemented (`wire/iflow.go`, `wire/qoder.go`); Vertex's service-account
   JWT (`E/vertex.js:125-160`) and MiMo's SSO `clientSign` (`shared/mimoAccount.js:96`) are
   credential mints, which belong with onboarding/refresh, not with the wire hooks.
8. **Readiness contradicts the wire for three providers (found, not changed).** Zed,
   MiMo free and `mmf` are declared `ready`, and the pin needs for each a wire
   Routeweft does not have: Zed's LLM-token exchange, `{thread_id, provider,
   provider_request}` envelope and NDJSON frames (`E/zed.js:246-305`); MiMo free's
   bootstrap JWT and system marker (`E/mimo-free.js:63-105`). A request to them reaches
   the upstream unauthenticated or in the wrong shape. The readiness column is pinned
   by the P08 matrix (`docs/P08_TEST_MATRIX.md`, generated from
   `internal/parity/p08_matrix_test.go`), which this task does not edit, so the
   metadata is left as it is; the correction is a `registry.notReady` entry and a
   regenerated matrix. Making them `unavailable` also needs a transport change, which is
   a routing decision. See PARITY §6.19.

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
| codex | Account/session/beta/originator headers and compact target (`E/codex.js:219-247`); initial SSE error inspection with replay/account fallback (`:280-386`); precise 429 reset (`:389-411`); forced stream/store=false/input/tool/reasoning/allowlist normalization (`:417-520`). | PARTIAL: **429 resets (#177); request contract, forced stream with fold, compact document, session_id and beta headers, stream-head inspection and review-model mapping (second slice)**. Open: remote image download (SSRF policy), session from client headers or an assistant-text hash (`:129-138`). |
| grok-cli | Responses target and CLI identity/turn/device/account headers (`E/grok-cli.js:349-398`); 402 spending-limit error fields (`:400-416`); input/tools/reasoning normalization, forced streaming/store=false (`:419-526`); stable agent id (`:528-550`); registry retry of 429/502/503 (`R/grok-cli.js:63-67`). | PARTIAL: **identity, session, request, turn, model and account headers, input/tool/reasoning normalization, forced stream with fold (second slice)**. Open: 402 spending-limit fields, the process-wide monotonic turn index, the machine-derived agent id, and the registry retry (Routeweft's dispatcher retries by its own policy). |
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

## Mechanism matrix (second slice)

Every provider with a specialized executor, plus the ticket's providers that have
none, against the mechanisms the ticket names. Cells: **does** = Routeweft does what
the pin does; **differs** = it does it differently (the divergence is in the rows
above or in PARITY); **not** = it does not; **-** = the pin has none. Citations are
pin lines (`E/` executors, `R/` registry, `S/` shared) at 1c3b9f47. "unwired" means
`ingress.unwired` keeps the provider unreachable, so a "does" there is a tested hook, not a
served provider.

| Provider | Envelope / body | Signing, token | Cookie session | Forced stream | In-band inspection, retry | Error body | Headers | Target URL |
|---|---|---|---|---|---|---|---|---|
| antigravity | differs: legs, unwired (`E/antigravity.js:137-311`) | - | - | - | not (`:356-446`) | not (`:356-401`) | not (`:129-135`) | not (`:118-125`) |
| gemini-cli | differs: legs, unwired (`E/gemini-cli.js:25-35`) | - | - | - | - | does (#177, `:37-54`) | not (`:15-23`) | not (`:10-13`) |
| vertex | - | not: SA JWT, ADC (`E/vertex.js:125-160`) | - | - | - | - | not (`:112-123`) | not (`:66-110`) |
| vertex-partner | - | not (same mint) | - | - | - | - | does | differs: project path only; no raw key, no probe (`:76-81`) |
| github | differs: Chat sanitation does; Responses and Messages routes not (`E/github.js:56-347`) | not: Copilot token (`:349-438`) | - | not: Messages route (`:259`) | not: 400-triggered Responses escalation (`:154-162`) | - | differs: fingerprint does, request id not (`:36-54`) | differs |
| iflow | - | does: HMAC (`E/iflow.js:29-55`) | - (none at the pin) | - | - | - | does | does |
| codebuddy-cn | differs: no prompt substitution, by decision (`E/codebuddy-cn.js:18-65`) | - | - | does | - | does (`:68-70`) | differs: no CLI identity, by decision | does |
| codebuddy-intl | differs (`E/codebuddy-intl.js:17-40`) | - | - | does | - | does (`:43-45`) | differs | does |
| commandcode | does: legs, unwired | - | - | does: its own wire | does (#170, `E/commandcode.js:42-61,140-241`) | not: non-2xx parse | does (`:28-40`) | does |
| cursor | does: Connect legs, unwired | - | - | - | - | - | does | does |
| qoder | not: agent envelope, encoding (`E/qoder.js:661-703`) | differs: COSY hook only (`:668-690`) | - | - | not: first-frame peek (`:357-448`) | not: billing codes (`:339`) | does | not (`:604-606`) |
| kiro | differs: legs, unwired | - | - | - | not: inspect, repair (`E/kiro.js:208-224`) | - | does | differs |
| codex | **does (second slice)** (`E/codex.js:417-519`) | - | - | **does (second slice)** (`R/codex.js:40`) | **does (second slice)** (`E/codex.js:280-386`) | does (#177, `:389-411`) | differs: session_id, beta, account, originator do; remote images not (`:219-247,263-278`) | does: compact (`:244-247`) |
| grok-cli | **does (second slice)** (`E/grok-cli.js:419-526`) | - | - | **does (second slice)** (`R/grok-cli.js:46`) | not: registry retry (`R/grok-cli.js:63-67`) | not: 402 (`E/grok-cli.js:400-417`) | differs: identity, session, turn, model, account do; machine agent id, monotonic turn not (`:362-398,528-550`) | does |
| grok-web | not (`E/grok-web.js:226-283`) | - | not: sso cookie (`:285-290`) | - | - | not (`:307-317`) | not: browser identity (`:263-283`) | not |
| perplexity-web | not (`E/perplexity-web.js:396-436`) | - | not (`:446-449`) | - | - | not (`:467-478`) | not | not |
| xiaomi-mimo | not: preview defaults (`E/xiaomi-mimo.js:54-68`) | not: SSO `clientSign` (`S/mimoAccount.js:96-174`) | not: preview only (`:41-52,71-92`) | - | not: one 401 refresh (`:83-92`) | - | does (cloud models) | not: preview route (`:30-34`) |
| xiaomi-tokenplan | - | - | - | - | - | - | - | does (`E/xiaomi-tokenplan.js:11-20`) |
| mimo-free (`mmf`) | not: system marker (`E/mimo-free.js:63-72`) | not: bootstrap JWT (`:79-105`) | - (a bearer JWT) | - | not: re-bootstrap on 401/403 (`:148-156`) | - | not (`:117-125`) | does: URL |
| zed | not (`E/zed.js:246-281`) | not: LLM-token exchange (`S/zedAuth.js`) | - | does on the declared Chat seam (#185, PARITY §6.19; `R/zed.js:27`) | not: NDJSON status frames (`E/zed.js:100-219`) | differs: trial_blocked cools the account; message not rewritten (`:284-305`) | not | not |
| opencode | not: Responses, Messages models (`E/opencode.js:471-507`) | - | - | does | - | - | not: session, request ids (`:417-426`) | differs: Chat models only |
| opencode-go | not: Responses models (`E/opencode-go.js:173-208`) | - | - | does for Responses-only models (#185, PARITY §6.19; `R/opencode-go.js:41`, `E/opencode-go.js:179-200`) | - | - | not (`:139-170`) | does: Responses-only models (`:133-136`) |
| kimchi | differs: model-name sanitation (`E/kimchi.js:4-93`) | - | - | - | - | - | - | - |
| azure | - | - | - | - | - | - | does (`E/azure.js:26-52`) | differs: no environment or OpenAI fallback (`:8-24`) |
| ollama-local | - | - | - | - | - | - | - | differs: unverified host precedence (`E/ollama-local.js:9-11`) |
| cline, clinepass, gitlab, ollama, byteplus, cloudflare-ai, nvidia | no executor (`E/index.js:32-60`); cline's envelope unwrap is shared (`S/clineEnvelope.js:14-18`), does | - | - | - | - | - | does (cline dual auth) | cloudflare-ai does: account URL |

### The forceStream registry flag at the pin

`R/` is `open-sse/providers/registry/`. The flag is read at `open-sse/handlers/chatCore.js:146`
(provider) and `open-sse/config/providerModels.js:72-76` (model).

| Registry entry | Declared at | Routeweft |
|---|---|---|
| api-airforce | `R/api-airforce.js:32` | does (#171) |
| codebuddy-cn, codebuddy-intl | `R/codebuddy-cn.js:23`, `R/codebuddy-intl.js:25` | does |
| opencode | `R/opencode.js:20` | does (Chat models) |
| commandcode | `R/commandcode.js:24` | does, on its own unwired wire (#170) |
| **codex** | `R/codex.js:40` | **does (second slice)** |
| **grok-cli** | `R/grok-cli.js:46` | **does (second slice)** |
| openai | `R/openai.js:28` | **not, by decision**: the fold refuses `logprobs` and annotations, which api.openai.com answers on a non-stream request, so forcing would turn a working request into a 502. A usage-preserving fold of those fields is the precondition, and is open. |
| zed | `R/zed.js:27` | does on the declared Chat seam (#185, PARITY §6.19); token exchange/envelope/NDJSON remain open |
| opencode-go (model `gpt-5.6-luna`) | `R/opencode-go.js:41` | does (#185, PARITY §6.19); the executor also forces the other Responses-only models (`E/opencode-go.js:179-200`) |

## Implemented mechanism: stream-only Responses endpoints

A request to the Codex or Grok CLI endpoint is a Responses request that must be
streamed, must not be stored, and must list only fields the endpoint knows. The pin
supplies this in two places that Routeweft had not: the registry flag (the response is
folded back to JSON for a client that asked for one, `chatCore.js:146`,
`sseToJsonHandler.js:185-208`, `streamToJsonConverter.js:53-114`) and each executor's
`transformRequest` (`E/codex.js:417-519`, `E/grok-cli.js:419-525`). Routeweft now does both
for both providers, by the same hook seam the other forced-stream providers use.

What changed, by mechanism rather than by provider:

- **Forced stream and fold.** `Hook.ForceStream` already existed for Chat. A Responses
  provider's non-stream client now gets one JSON document folded from the stream
  (`openai.FoldResponsesStream`, `ingress.foldForcedStream` chooses by wire). The document is
  the terminal event's own response, so the id, model and usage (cached-token detail
  included) are the endpoint's, not rebuilt; an absent usage stays absent; a stream with no
  terminal event, a skipped output index, or a `response.failed`/`error` event fails the
  fold instead of answering 200 (the pin answers a 200 `status:"failed"` document, and
  zero usage, `streamToJsonConverter.js:46,106-113`).
- **Request contract.** `wire/codex_body.go` and `wire/grokcli_body.go` rewrite the final
  outbound body (AGENTS §6 step 8) to the endpoint's contract: stream, store false, input
  as an array, stored-item references removed, flat function tools (hosted tools kept, the
  rest dropped), a `tool_choice` that still names a declared function, reasoning effort and
  summary, `include` for encrypted reasoning, and an allowlist of fields. Codex also gets the
  pin's default instructions when the client sent none, the schema `pattern`s its validator
  cannot parse removed (`utils/codexToolSchema.js`), review models sent as their base model, and
  the compact endpoint's smaller document. Grok CLI gets its custom-tool and reasoning-item
  repair. Both are idempotent, fail open (a body that is not a JSON object is untouched) and
  leave every other wire alone.
- **Request-side identity.** `RequestHeaders` sets Codex's `session_id` and Grok CLI's
  `x-grok-*` identity, session, request, turn, model and account headers, and an
  event-stream `Accept` that a forced stream needs whatever the client sent. These are
  request headers, not `OpenAIHeaders`, because model discovery reads that hook
  (#171). A new `Hook.ClientHeaders` forwards the one client header Codex reads,
  `x-codex-beta-features` (`E/codex.js:222-225`), and only when the client sent it.
- **Stream-head inspection.** `CodexDo` reads the head of a Codex stream before the client
  sees a byte. A capacity error becomes a 503 so dispatch moves to another account; an
  overload error is repeated three times, two seconds apart, and then becomes a 503; a stream
  that has produced output is never repeated. The inspected bytes are replayed ahead of the
  rest, so a relayed stream is byte-identical.

Divergences from the pin, each deliberate and tested:

1. The pin matches its phrases anywhere in the first 256 KiB. The first event of every
   Codex stream, `response.created`, echoes the request's instructions and tools, so a
   prompt that quotes `model_at_capacity` would be answered 503 on every account. The
   phrases are read only in the error of an `error` or `response.failed` event
   (`TestCodexStreamIsNotJudgedByTextItOnlyCarries`, `TestCodexStreamLineJudgement`).
2. The pin folds a failed stream into a 200 document and reports zero usage when none was
   sent. Both are refused (`TestFoldResponsesStreamDoesNotInventUsage`,
   `TestFoldResponsesStreamReportsInBandFailures`).
3. Remote images are not downloaded (`E/codex.js:263-278`): that is a server-side fetch of a
   client-supplied URL. They reach the endpoint as `input_image` URLs.
4. The prompt cache key falls back to a digest of the connection, not the connection id, and
   the client-header and assistant-text sources of the pin's session id are not ported.
5. Grok CLI's turn index is the number of user turns in the input, the pin's first term,
   not a per-process monotonic counter; no agent id is derived from the host's machine id.
6. `include` gets `reasoning.encrypted_content` merged in for Codex when reasoning is on; the pin
   replaces the client's list (`:486-489`), which silently drops entries such as
   `file_search_call.results`. Grok CLI merges as the pin does (`grok-cli.js:496-502`).
7. OpenAI is not forced (table above).

Not changed, and why: `ingress.unwired` and `registry.notReady` are untouched because no
provider's wiring state changed (Codex and Grok CLI were served and are served). Enabling
anything is a separate routing decision.

Evidence (no live provider, no credential): `wire/codex_body_test.go`,
`wire/codex_stream_test.go` (a real HTTP server per scenario, including the 256 KiB window,
the retry wait ending with the request, and byte-identical replay), `wire/grokcli_body_test.go`,
`wire/forcedstream_test.go` (the set of forced providers is closed and every pin entry is
accounted for), `protocol/openai/fold_responses_test.go`, and
`ingress/responses_forced_stream_test.go` (non-stream, streaming, translated Chat and
Messages clients, account fallback, compact, Grok CLI, an unrelated provider untouched,
and cancellation while the head is being read).

### Second slice verification and performance

`go build ./...`, `go vet ./...`, `go test -race` on `internal/providers/wire`,
`internal/ingress`, `internal/protocol/openai` and `internal/providers/codex`, and
`ORACLE_DIR=/home/ubuntu/9router node compat/parity/inventory.mjs` pass. Go is at
`/home/ubuntu/go-toolchain/go/bin/go`; the brief's `/tmp/s115j-goroot` is absent. The
full-repository `go test ./...` fails only `TestStaticUIUnknownPathsAre404` (known).

On this loaded AMD EPYC 9754 host (Go 1.27.3, two CPUs, `-benchmem`): rewriting a 40 KB
agent turn (40 tools, 60 input items) costs 1.68 ms and 3,979 allocations for Codex, 1.56 ms
and 3,902 for Grok CLI; reading a ten-event Codex stream head costs 24 µs and 81 allocations.
This is once per request for these two providers, against an upstream that answers in
seconds; no other provider's request pays more than a hook lookup and a suffix check.

