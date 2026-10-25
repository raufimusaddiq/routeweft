# P08 test matrix (#76)

Task: per-provider deterministic evidence for authenticated listing, quota and
applicable unavailability, import and refresh, streaming, non-streaming, error
shapes and cached-versus-ordinary token accounting, for all 81 active providers.
The catalog listing a provider is not evidence that it works; a cell is evidence
only when a named test would fail if the behaviour were absent.

Oracle: `/opt/9router` is absent on this host, so the pinned reference was read from the
clone at `/home/ubuntu/9router` with `git -C /home/ubuntu/9router show 1c3b9f47:<path>`
(the pin in `compat/parity/oracle.mjs`). The clone's working tree has an unrelated
local edit to `scripts/deploy.sh`; it was not read. Every pin citation below is a
path and line at that commit. `ORACLE_DIR=/home/ubuntu/9router` drives the oracle-gated
tests.

This file is generated from `internal/parity/p08_matrix_test.go`. `TestP08MatrixCoversEveryActiveProviderWithRealEvidence`
fails if a row is missing, a cell names a test that does not exist, a not-applicable
cell cites no pin file, or a served provider claims a refusal. `TestP08MatrixDocIsCurrent`
fails if this file differs from the table; regenerate with
`P08_WRITE=1 go test ./internal/parity -run TestP08MatrixDocIsCurrent`.

## Kinds

| Kind | Meaning |
|---|---|
| `P` | a test that exercises a provider-specific branch, fixture or wire |
| `C` | a codec-level test only; the provider is unavailable (SPEC §36), so nothing reaches it from dispatch |
| `T` | this provider is one row of a table over the shared path: it checks its own URL, credential and body, but a bug in the shared path would fail every row alike |
| `S` | the shared path only; no test names this provider |
| `N` | not applicable, with the pin's reason and file:line |
| `U` | refused by its immutable readiness (unavailable); the refusal is asserted |
| `G` | not covered; the note says what is missing |

Readiness is a property of the build and separate from enabled and from a stored
credential (#176): a `U` or `C` cell says nothing about whether a credential exists.
An absent value reads as unknown, never as zero, and a failed read keeps the last
known good view (#173); the quota and token cells are written against that rule.

## Honest totals

Of the 81 active providers:

- **46** have at least one provider-specific test (a `P` cell).
- **0** have no `P` cell but a codec-level test (`C`) for a provider the build refuses: none.
- **32** are shared-path only: every evidenced cell is a table row (`T`) or the shared path (`S`), and the rest are not applicable, refused or gaps: `alicode-intl`, `alitp-intl`, `alicode`, `alims-intl`, `baidu`, `bazaarlink`, `blackbox`, `bluesminds`, `byteplus`, `cerebras`, `chutes`, `cohere`, `featherless`, `fireworks`, `hyperbolic`, `kilo-gateway`, `llm7`, `morph`, `nebius`, `nvidia`, `poolside`, `sambanova`, `siliconflow`, `tencent`, `together`, `venice`, `volcengine-ark`, `openai`, `ollama-local`, `tokenrouter`, `muse`, `xai`.
- **3** have no test of any kind: every cell is `N`, `U` or `G`: `vertex`, `grok-web`, `perplexity-web`.

Counting only the serving path (a `P` in streaming, non-streaming, error shapes or tokens), **28** providers have a provider-specific test: `api-airforce`, `groq`, `mistral`, `openrouter`, `codex`, `anthropic`, `gemini`, `deepseek`, `minimax`, `xiaomi-tokenplan`, `mimo-free`, `mmf`, `opencode`, `ollama`, `typesafe`, `vertex-partner`, `cloudflare-ai`, `iflow`, `kimi`, `xiaomi-mimo`, `cline`, `clinepass`, `codebuddy-cn`, `codebuddy-intl`, `grok-cli`, `kenari`, `zed`, `azure`. The other 53 have one only for listing, quota or import, or none.

Kinds count cells, not providers: a `P` row can still have `T`, `S` and `G` cells, and the per-column tables below say which.

Cells by kind and column:

| Column | P | C | T | S | U | G | N |
|---|---:|---:|---:|---:|---:|---:|---:|
| Authenticated listing | 22 | 0 | 20 | 2 | 0 | 3 | 34 |
| Quota / usage read | 23 | 0 | 0 | 0 | 0 | 1 | 57 |
| Applicable unavailability | 20 | 1 | 0 | 52 | 5 | 3 | 0 |
| Import / refresh | 18 | 0 | 3 | 0 | 0 | 3 | 57 |
| Streaming | 17 | 5 | 51 | 1 | 3 | 4 | 0 |
| Non-streaming | 18 | 5 | 50 | 1 | 3 | 4 | 0 |
| Error shapes | 24 | 6 | 1 | 47 | 3 | 0 | 0 |
| Cached vs ordinary tokens | 13 | 5 | 5 | 54 | 3 | 1 | 0 |
| **All cells** | **155** | **22** | **130** | **157** | **17** | **19** | **148** |

648 cells in all (81 providers x 8 behaviours). `N` cells carry a pin citation in the evidence tables below.

## The matrix

| Provider | Readiness | List | Quota | Unavail | Import | Stream | NonStr | Errors | Tokens |
|---|---|:-:|:-:|:-:|:-:|:-:|:-:|:-:|:-:|
| `alicode-intl` | ready | T | N | S | N | T | T | S | S |
| `alitp-intl` | ready | N | N | S | N | T | T | S | S |
| `alicode` | ready | T | N | S | N | T | T | S | S |
| `alims-intl` | ready | T | N | S | N | T | T | S | S |
| `api-airforce` | ready | P | N | S | N | P | P | P | P |
| `baidu` | ready | N | N | S | N | T | T | S | S |
| `bazaarlink` | ready | N | N | S | N | T | T | S | S |
| `blackbox` | ready | N | N | S | N | T | T | S | S |
| `bluesminds` | ready | N | N | S | N | T | T | S | S |
| `byteplus` | ready | T | N | S | N | T | T | S | S |
| `cerebras` | ready | T | N | S | N | T | T | S | S |
| `chutes` | ready | T | N | S | N | T | T | S | S |
| `cohere` | ready | T | N | S | N | T | T | S | S |
| `featherless` | ready | N | N | S | N | T | T | S | S |
| `fireworks` | ready | T | N | S | N | T | T | S | S |
| `groq` | ready | T | G | G | N | T | T | P | S |
| `hyperbolic` | ready | T | N | S | N | T | T | S | S |
| `kilo-gateway` | ready | N | N | S | N | T | T | S | S |
| `llm7` | ready | N | N | S | N | T | T | S | S |
| `mistral` | ready | T | N | S | N | T | T | P | T |
| `morph` | ready | N | N | S | N | T | T | S | S |
| `nebius` | ready | T | N | S | N | T | T | S | S |
| `nvidia` | ready | T | N | S | N | T | T | S | S |
| `openrouter` | ready | P | N | G | N | P | T | S | S |
| `perplexity` | ready | P | N | S | N | T | T | S | S |
| `poolside` | ready | N | N | S | N | T | T | S | S |
| `sambanova` | ready | N | N | S | N | T | T | S | S |
| `siliconflow` | ready | T | N | S | N | T | T | S | S |
| `tencent` | ready | N | N | S | N | T | T | S | S |
| `together` | ready | T | N | S | N | T | T | S | S |
| `venice` | ready | S | N | S | N | T | T | S | S |
| `vercel-ai-gateway` | ready | T | P | P | N | T | T | S | S |
| `volcengine-ark` | ready | T | N | S | N | T | T | S | S |
| `openai` | ready | T | N | S | N | T | T | S | S |
| `codex` | ready | P | P | P | P | G | G | P | T |
| `anthropic` | ready | P | N | S | N | T | T | P | P |
| `gemini` | ready | P | N | S | N | P | P | P | P |
| `claude` | ready | G | P | P | P | T | T | S | S |
| `deepseek` | ready | T | P | G | N | T | T | P | P |
| `glm` | ready | N | P | P | N | T | T | S | S |
| `glm-cn` | ready | N | P | S | N | T | T | S | S |
| `minimax` | ready | N | P | P | N | T | T | P | P |
| `minimax-cn` | ready | N | P | P | N | T | T | S | S |
| `xiaomi-tokenplan` | ready | N | N | S | N | P | P | P | S |
| `mimo-free` | ready | P | N | S | N | P | P | S | S |
| `mmf` | ready | N | N | S | N | P | P | S | S |
| `opencode` | ready | P | N | S | N | P | P | P | S |
| `ollama` | ready | P | P | P | N | P | P | P | P |
| `ollama-local` | experimental | G | N | S | N | S | S | S | S |
| `typesafe` | unavailable | N | N | U | N | P | P | P | P |
| `vertex-partner` | experimental | N | N | S | N | G | G | P | S |
| `tokenrouter` | ready | S | N | S | N | T | T | S | S |
| `perplexity-agent` | ready | P | N | S | N | T | T | S | S |
| `opencode-go` | experimental | N | P | P | N | T | T | S | S |
| `cloudflare-ai` | experimental | N | N | S | N | P | P | P | P |
| `muse` | ready | N | N | S | N | T | T | S | T |
| `xai` | ready | T | N | S | T | T | T | S | T |
| `github` | experimental | P | P | P | P | T | T | T | S |
| `gitlab` | ready | N | N | S | P | T | T | S | S |
| `iflow` | ready | N | N | S | P | T | T | P | S |
| `kimi` | ready | N | P | P | T | T | P | P | P |
| `xiaomi-mimo` | ready | N | P | P | P | T | P | S | S |
| `cline` | ready | P | N | S | P | P | P | P | P |
| `clinepass` | ready | P | N | S | P | P | P | P | P |
| `kilocode` | ready | P | N | S | P | T | T | S | S |
| `codebuddy-cn` | ready | N | P | P | P | P | P | P | P |
| `codebuddy-intl` | ready | N | P | P | P | P | P | P | P |
| `antigravity` | unavailable | P | P | P | P | C | C | C | C |
| `gemini-cli` | unavailable | P | P | U | P | C | C | C | C |
| `cursor` | unavailable | P | N | C | P | C | C | C | C |
| `qoder` | unavailable | P | P | P | T | G | G | C | G |
| `kiro` | unavailable | P | P | P | P | C | C | C | C |
| `vertex` | unavailable | N | N | U | G | U | U | U | U |
| `commandcode` | unavailable | N | P | P | N | C | C | C | C |
| `grok-cli` | ready | P | P | P | P | G | G | P | T |
| `grok-web` | unavailable | N | N | U | G | U | U | U | U |
| `perplexity-web` | unavailable | N | N | U | G | U | U | U | U |
| `kenari` | ready | P | P | P | N | P | P | P | S |
| `zed` | ready | G | P | P | P | P | P | P | S |
| `kimchi` | ready | P | N | S | P | T | T | S | S |
| `azure` | experimental | N | N | S | N | P | P | P | S |

## Evidence by column

Each group is one distinct claim. Test names are `internal/<dir>:<Test>`. `U/` is `open-sse/services/usage/` and `route.js` is `src/app/api/providers/[id]/models/route.js`, both at the pin.

### Authenticated listing

- **P** `api-airforce` — registry modelsFetcher airforce-free filter Tests: `control/api:TestSuggestionFiltersPinnedFixtures`.
- **P** `openrouter` — free-model filter; plain bearer URL checked against route.js:72 Tests: `control/api:TestSuggestionFiltersPinnedFixtures`, `parity:TestEveryPinnedPlainListingURLMatches`.
- **P** `perplexity` — listed at /v1/models (route.js:247) Tests: `providers/wire:TestListingRequestsMatchThePin`, `control/api:TestPerplexityListsUnderV1`.
- **P** `codex` — client_version gate, slug-keyed entries (route.js:149-164) Tests: `control/api:TestCodexListingSendsTheGateAndReadsSlugs`, `providers/wire:TestParseCodexListing`.
- **P** `anthropic` — x-api-key credential style (route.js:203-213) Tests: `providers/discovery:TestNativeDiscoveryAuthStyles`.
- **P** `gemini` — x-goog-api-key and the models[] shape (route.js:142-148); paging is not followed, as in the pin Tests: `providers/discovery:TestModelsSupportsApiKeyHeaderAndGeminiShape`.
- **P** `mimo-free` — registry modelsFetcher mimo-free filter Tests: `control/api:TestSuggestionFiltersPinnedFixtures`.
- **P** `opencode` — registry modelsFetcher opencode-free filter Tests: `control/api:TestSuggestionFiltersPinnedFixtures`.
- **P** `ollama` — GET /api/tags with the key (route.js:125) Tests: `control/api:TestWorkflowNativeRoutesDiscoveryAndProbe`, `parity:TestEveryPinnedPlainListingURLMatches`.
- **P** `perplexity-agent` — route.js:117 Tests: `control/api:TestPerplexityListsUnderV1`, `parity:TestEveryPinnedPlainListingURLMatches`.
- **P** `github` — chat models only (route.js:174-200) Tests: `control/api:TestGitHubListingDropsEmbeddingsAndDisabledModels`, `providers/wire:TestParseGitHubListing`.
- **P** `cline` — workos: token prefix Tests: `control/api:TestClineListingCarriesTheWorkOSToken`, `providers/wire:TestParseClineListings`.
- **P** `clinepass` — workos: prefix, cline-pass/ ids only Tests: `control/api:TestClineListingCarriesTheWorkOSToken`, `providers/wire:TestParseClineListings`.
- **P** `kilocode` — registry modelsFetcher kilo-free filter and cache Tests: `control/api:TestSuggestionFiltersPinnedFixtures`, `control/api:TestKiloCacheFreshExpiredFailureAndConcurrency`.
- **P** `antigravity` — POST models; response shape is a fixture, the pin returns it untouched (route.js:165-173) Tests: `control/api:TestGoogleInternalListingsArePosts`, `providers/wire:TestParseGoogleModelsListing`.
- **P** `gemini-cli` — POST fetchAvailableModels (route.js:440-460) Tests: `control/api:TestGoogleInternalListingsArePosts`, `providers/wire:TestParseGoogleModelsListing`.
- **P** `cursor` — unary proto POST, synthetic proto Tests: `control/api:TestCursorListingIsAUnaryProtoPost`, `providers/wire:TestParseCursorListing`.
- **P** `qoder` — signed GET; personal access tokens are an explicit gap (PARITY 6.15) Tests: `control/api:TestQoderListingIsSignedAndReadsTheChatCatalog`, `providers/wire:TestParseQoderListing`, `providers/wire:TestQoderPersonalTokenListingIsAnExplicitGap`.
- **P** `kiro` — ListAvailableModels, region from the ARN Tests: `control/api:TestKiroListingIsListAvailableModels`, `control/api:TestKiroListingRefusesAHostInTheRegion`, `providers/wire:TestParseKiroListing`.
- **P** `grok-cli` — grok-shell identity headers Tests: `control/api:TestGrokCLIListingUsesTheGrokShellIdentity`, `providers/wire:TestParseGrokCLIListing`.
- **P** `kenari` — registry modelsFetcher; chat-endpoint filter. The pin has no saved-connection listing for it (route.js:595-601) Tests: `control/api:TestSuggestionFiltersPinnedFixtures`.
- **P** `kimchi` — metadata catalog with include_in_cli Tests: `control/api:TestKimchiListingUsesTheMetadataCatalogWithItsFilter`, `providers/wire:TestParseKimchiListing`.
- **T** `alicode-intl`, `alicode`, `alims-intl`, `byteplus`, `cerebras`, `chutes`, `cohere`, `fireworks`, `groq`, `hyperbolic`, `mistral`, `nebius`, `nvidia`, `siliconflow`, `together`, `vercel-ai-gateway`, `volcengine-ark`, `openai`, `deepseek`, `xai` — bearer GET to the URL the pin lists (route.js:75-82 and the entry for this provider) Tests: `parity:TestEveryPinnedPlainListingURLMatches`, `providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs`.
- **S** `venice` — registry modelsFetcher catalog (registry/venice.js); no per-provider test Tests: `providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs`.
- **S** `tokenrouter` — registry modelsFetcher catalog (registry/tokenrouter.js); no per-provider test Tests: `providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs`.
- **G** `claude` — the pin lists api.anthropic.com/v1/models with x-api-key (route.js:132-141); the OAuth credential's listing is not exercised
- **G** `ollama-local` — the pin resolves the local host and GETs /api/tags (route.js:491-507); no test names ollama-local (provider is experimental)
- **G** `zed` — the pin lists through a Zed LLM-token exchange (route.js:293-326); Routeweft sends the shared bearer to cloud.zed.dev/models (PARITY 6.15, MISSING)
- **N** `alitp-intl`, `baidu`, `bazaarlink`, `blackbox`, `bluesminds`, `featherless`, `kilo-gateway`, `llm7`, `morph`, `poolside`, `sambanova`, `tencent`, `glm`, `glm-cn`, `minimax`, `minimax-cn`, `xiaomi-tokenplan`, `mmf`, `typesafe`, `vertex-partner`, `opencode-go`, `cloudflare-ai`, `muse`, `gitlab`, `iflow`, `kimi`, `xiaomi-mimo`, `codebuddy-cn`, `codebuddy-intl`, `vertex`, `commandcode`, `grok-web`, `perplexity-web` — the pin has no authenticated listing: no PROVIDER_MODELS_CONFIG entry (route.js:131-507), so the handler answers 400 "does not support models listing" (route.js:595-601). Routeweft still requests the shared GET {base}/models; unverified (PARITY 6.15)
- **N** `azure` — the pin has no listing for Azure (not in PROVIDER_MODELS_CONFIG, route.js:131-507; answered 400 at route.js:595-601); Routeweft's hook is Unlisted and sends nothing

### Quota / usage read

- **P** `vercel-ai-gateway` — misc.js:150-200 Tests: `providers/usage:TestVercelBalanceIsNotAUsagePercentage`.
- **P** `codex` — U/codex.js:107-217; rate_limits_by_limit_id aliases open (P05) Tests: `providers/codex:TestUsageNormalizesWindowsAndResetCredits`, `providers/codex:TestUsageOmittedResetCreditsAreUnknown`.
- **P** `claude` — U/claude.js:20-185; scoped weekly metadata open (P05) Tests: `providers/claude:TestOAuthUsageParsesWindowsAndCaches`, `providers/claude:TestOAuthUsage429CoolsDownThenFallsBackToLegacy`.
- **P** `deepseek` — U/deepseek.js:11-108. A zero balance has total 0, which the reader's window() reads as unlimited (P05 open) Tests: `providers/usage:TestDeepSeekReportsBalance`.
- **P** `glm` — U/glm.js:18-84 Tests: `providers/usage:TestGLMReportsBothLimitTypes`, `providers/usage:TestGLMRejectsExpiredKey`, `providers/usage:TestRegionalReadersUseTheirOwnHost`.
- **P** `glm-cn` — U/glm.js:18-84; China host Tests: `providers/usage:TestRegionalReadersUseTheirOwnHost`.
- **P** `minimax` — U/minimax.js:95-226; top-level camelCase envelope and 1004 auth text differ from the pin (P05) Tests: `providers/usage:TestMiniMaxUsageEndpointsRegionsAndCountSemantics`.
- **P** `minimax-cn` — U/minimax.js:95-226; China host Tests: `providers/usage:TestMiniMaxUsageEndpointsRegionsAndCountSemantics`.
- **P** `ollama` — misc.js:55-140; plan label from /api/me and the free-plan reset are not read Tests: `providers/usage:TestOllamaUsageRatioIsUsedFractionAndNullWindowsAreSkipped`.
- **P** `opencode-go` — U/opencode-go.js:11-100 Tests: `providers/usage:TestOpenCodeGoUsageUsedPercentAndAbsentPeriods`, `providers/usage:TestOpenCodeGoUsageFailuresAreErrorsNotHealthyAccounts`.
- **P** `github` — U/github.js:11-99 Tests: `providers/usage:TestGitHubPaidAndFreeShapes`.
- **P** `kimi` — U/kimi.js:109-208 Tests: `providers/usage:TestKimiUsesAPIKeyHeader`, `providers/usage:TestKimiOAuthSendsDeviceHeaders`, `providers/usage:TestKimiEntitlementIsNotExpiry`.
- **P** `xiaomi-mimo` — U/xiaomi-mimo.js:62-120; only the API-key path, the account-session path is not read Tests: `providers/usage:TestXiaomiMimoUsagePercentIsRemaining`.
- **P** `codebuddy-cn` — U/codebuddy-cn.js:46-145 Tests: `providers/usage:TestCodeBuddyUsageUsesRouteweftIdentity`, `providers/usage:TestRegionalReadersUseTheirOwnHost`, `ingress:TestCodeBuddyCNUsageFixtureReplays`.
- **P** `codebuddy-intl` — U/codebuddy-cn.js:46-145; International host Tests: `providers/usage:TestRegionalReadersUseTheirOwnHost`, `providers/wire:TestCodeBuddyIntlMessages`.
- **P** `antigravity` — U/google.js:120-249; the retrieveUserQuotaSummary shape is not qualified (P05) Tests: `providers/usage:TestAntigravityFreeTierSkipsPerModelQuota`.
- **P** `gemini-cli` — U/google.js:22-104; missing-project discovery open (P05) Tests: `providers/usage:TestGeminiUsesStoredProjectID`.
- **P** `qoder` — misc.js:226-290; an absent pool total reads as unlimited, isQuotaExceeded is not read Tests: `providers/usage:TestQoderUsageUserAndOrganizationPools`.
- **P** `kiro` — U/kiro.js:9-181; fallback hosts open (P05) Tests: `providers/usage:TestKiroAPIKeySendsTokenType`, `providers/usage:TestKiroFreeTrialIsSeparateWindow`.
- **P** `commandcode` — U/commandcode.js:60-135; the pin omits an empty window, Routeweft reports it unlimited Tests: `providers/usage:TestCommandCodeUsageCarriesOrgAndPlanCap`, `providers/usage:TestCommandCodeUsageAuthAndBillingFailuresAreErrors`.
- **P** `grok-cli` — U/grok-cli.js:141-239; gRPC fallback open (P05) Tests: `providers/usage:TestGrokPinnedRESTCredits`, `providers/usage:TestGrokRESTPaidFreeAndMonthlyShapes`.
- **P** `kenari` — U/kenari.js:9-51 Tests: `providers/usage:TestKenariPinnedZeroCapacity`, `providers/usage:TestKenariAbsentAmountsAreUnknownNotExhausted`.
- **P** `zed` — U/zed.js:54-120; the pin's {limited:N} limit form is not read, so such a window is omitted Tests: `providers/usage:TestZedUsageLimitedUnlimitedAndExhausted`.
- **G** `groq` — U/groq.js:44-129 reads x-ratelimit-* headers from GET /models; Routeweft's reader (usage/apikey.go groq) discards the headers and always returns the error "rate limits are only exposed on inference responses". No quota is read for Groq
- **N** `alicode-intl` — open-sse/providers/registry/alicode-intl.js:1-30 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `alitp-intl` — open-sse/providers/registry/alitp-intl.js:1-35 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `alicode` — open-sse/providers/registry/alicode.js:1-31 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `alims-intl` — open-sse/providers/registry/alims-intl.js:1-32 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `api-airforce` — open-sse/providers/registry/api-airforce.js:1-39 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `baidu` — open-sse/providers/registry/baidu.js:1-33 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `bazaarlink` — open-sse/providers/registry/bazaarlink.js:1-47 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `blackbox` — open-sse/providers/registry/blackbox.js:1-41 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `bluesminds` — open-sse/providers/registry/bluesminds.js:1-38 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `byteplus` — open-sse/providers/registry/byteplus.js:1-35 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `cerebras` — open-sse/providers/registry/cerebras.js:1-32 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `chutes` — open-sse/providers/registry/chutes.js:1-24 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `cohere` — open-sse/providers/registry/cohere.js:1-25 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `featherless` — open-sse/providers/registry/featherless.js:1-34 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `fireworks` — open-sse/providers/registry/fireworks.js:1-27 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `hyperbolic` — open-sse/providers/registry/hyperbolic.js:1-35 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `kilo-gateway` — open-sse/providers/registry/kilo-gateway.js:1-34 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `llm7` — open-sse/providers/registry/llm7.js:1-35 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `mistral` — open-sse/providers/registry/mistral.js:1-29 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `morph` — open-sse/providers/registry/morph.js:1-29 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `nebius` — open-sse/providers/registry/nebius.js:1-25 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `nvidia` — open-sse/providers/registry/nvidia.js:1-34 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `openrouter` — open-sse/providers/registry/openrouter.js:1-35 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `perplexity` — open-sse/providers/registry/perplexity.js:1-32 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `poolside` — open-sse/providers/registry/poolside.js:1-31 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `sambanova` — open-sse/providers/registry/sambanova.js:1-27 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `siliconflow` — open-sse/providers/registry/siliconflow.js:1-39 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `tencent` — open-sse/providers/registry/tencent.js:1-27 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `together` — open-sse/providers/registry/together.js:1-28 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `venice` — open-sse/providers/registry/venice.js:1-42 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `volcengine-ark` — open-sse/providers/registry/volcengine-ark.js:1-35 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `openai` — open-sse/providers/registry/openai.js:1-64 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `anthropic` — open-sse/providers/registry/anthropic.js:1-34 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `gemini` — open-sse/providers/registry/gemini.js:1-51 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `xiaomi-tokenplan` — open-sse/providers/registry/xiaomi-tokenplan.js:1-63 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `mimo-free` — open-sse/providers/registry/mimo-free.js:1-27 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `mmf` — open-sse/providers/registry/mmf.js:1-19 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `opencode` — open-sse/providers/registry/opencode.js:1-41 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `ollama-local` — open-sse/providers/registry/ollama-local.js:1-19 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `typesafe` — open-sse/providers/registry/typesafe.js:1-29 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `vertex-partner` — open-sse/providers/registry/vertex-partner.js:1-29 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `tokenrouter` — open-sse/providers/registry/tokenrouter.js:1-56 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `perplexity-agent` — open-sse/providers/registry/perplexity-agent.js:1-44 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `cloudflare-ai` — open-sse/providers/registry/cloudflare-ai.js:1-52 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `muse` — open-sse/providers/registry/muse.js:1-25 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `xai` — open-sse/providers/registry/xai.js:1-43 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `gitlab` — open-sse/providers/registry/gitlab.js:1-32 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `iflow` — open-sse/services/usage/misc.js:18-24 returns the fixed message "Usage tracked per request" and makes no request
- **N** `cline` — open-sse/providers/registry/cline.js:1-55 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `clinepass` — open-sse/providers/registry/clinepass.js:1-62 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `kilocode` — open-sse/providers/registry/kilocode.js:1-51 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `cursor` — open-sse/providers/registry/cursor.js:1-58 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `vertex` — open-sse/providers/registry/vertex.js:1-33 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `grok-web` — open-sse/providers/registry/grok-web.js:1-39 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `perplexity-web` — open-sse/providers/registry/perplexity-web.js:1-33 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `kimchi` — open-sse/providers/registry/kimchi.js:1-49 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)
- **N** `azure` — open-sse/providers/registry/azure.js:1-21 has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns "not implemented" (P05 audit row)

### Applicable unavailability

- **P** `vercel-ai-gateway` — a credit balance is not a usage percentage Tests: `providers/usage:TestVercelBalanceIsNotAUsagePercentage`.
- **P** `codex` — usage_limit_reached cools until resets_at / resets_in_seconds (executors/codex.js:388-410), landed in P02 Tests: `ingress:TestCodexBodyResetReachesDispatchCooldown`, `providers/wire:TestPinnedErrorBodyResets`, `providers/codex:TestUsageFailureIsUnavailableNotExhausted`.
- **P** `claude` — a 429 on the usage read cools and falls back; no organization reports an admin message Tests: `providers/claude:TestOAuthUsage429CoolsDownThenFallsBackToLegacy`, `providers/claude:TestLegacyWithoutOrganizationReportsAdminMessage`.
- **P** `glm` — an expired key is an error, not a healthy account Tests: `providers/usage:TestGLMRejectsExpiredKey`.
- **P** `minimax`, `minimax-cn` — a base_resp error is an error, not an empty quota Tests: `providers/usage:TestMiniMaxUsageEndpointsRegionsAndCountSemantics`.
- **P** `ollama` — usage 1.0 reads as nothing left; a null window is skipped Tests: `providers/usage:TestOllamaUsageRatioIsUsedFractionAndNullWindowsAreSkipped`.
- **P** `opencode-go` — 100% used reads as nothing left; a 403 EntitlementError is a subscription hint (opencode-go.js:54-76) Tests: `providers/usage:TestOpenCodeGoUsageUsedPercentAndAbsentPeriods`, `providers/usage:TestOpenCodeGoUsageFailuresAreErrorsNotHealthyAccounts`.
- **P** `github` — free-plan limited quotas versus paid entitlements Tests: `providers/usage:TestGitHubPaidAndFreeShapes`.
- **P** `kimi` — an entitlement date is not a credential expiry Tests: `providers/usage:TestKimiEntitlementIsNotExpiry`.
- **P** `xiaomi-mimo` — a 401 (key cannot read the account quota) is an error, not an empty or full window Tests: `providers/usage:TestXiaomiMimoUsagePercentIsRemaining`.
- **P** `codebuddy-cn` — the gateway's frequency-limit body cools the account until its reset (codebuddy-error.js) Tests: `ingress:TestCodeBuddyFrequencyLimitCoolsTheAccountAndRelaysTheGatewayError`, `providers/wire:TestCodeBuddyFrequencyLimit`, `ingress:TestCodeBuddyOtherUpstreamErrorsAreNotReadAsALimit`.
- **P** `codebuddy-intl` — same hook as codebuddy-cn Tests: `ingress:TestCodeBuddyFrequencyLimitCoolsTheAccountAndRelaysTheGatewayError`, `providers/wire:TestCodeBuddyFrequencyLimit`, `ingress:TestCodeBuddyOtherUpstreamErrorsAreNotReadAsALimit`.
- **P** `antigravity` — free tier has no per-model quota; provider unavailable for serving Tests: `providers/usage:TestAntigravityFreeTierSkipsPerModelQuota`.
- **P** `qoder` — a spent organization pool reads as nothing left; provider unavailable for serving Tests: `providers/usage:TestQoderUsageUserAndOrganizationPools`.
- **P** `kiro` — free trial is its own window; provider unavailable for serving Tests: `providers/usage:TestKiroFreeTrialIsSeparateWindow`.
- **P** `commandcode` — cap reached reads as nothing left; provider unavailable for serving Tests: `providers/usage:TestCommandCodeUsageCarriesOrgAndPlanCap`, `providers/wire:TestParseCommandCodeError`.
- **P** `grok-cli` — 402 spending-limit cools the account for two minutes and falls back (executors/grok-cli.js:400-416, errorConfig.js:77), landed in #184 Tests: `ingress:TestProviderErrorBodiesCoolTheAccountLikeThePin`, `providers/wire:TestGrokCLISpendingLimitFallsBackAndCoolsTheAccountForTwoMinutes`.
- **P** `kenari` — zero capacity is exhausted; absent amounts are unknown (U/kenari.js) Tests: `providers/usage:TestKenariPinnedZeroCapacity`.
- **P** `zed` — 500/500 reads as nothing left; trial_blocked 403 cools the account (the pin's message rewrite, zed.js:284-310, is not done) Tests: `providers/usage:TestZedUsageLimitedUnlimitedAndExhausted`, `ingress:TestProviderErrorBodiesCoolTheAccountLikeThePin`.
- **C** `cursor` — the resource_exhausted in-band failure (cursor.js:365-374); provider unavailable for serving Tests: `providers/wire:TestCursorDoReportsAFailureInBandNotAsSuccess`, `providers/wire:TestCursorDoPassesUpstreamRejectionsThrough`.
- **S** `alicode-intl`, `alitp-intl`, `alicode`, `alims-intl`, `api-airforce`, `baidu`, `bazaarlink`, `blackbox`, `bluesminds`, `byteplus`, `cerebras`, `chutes`, `cohere`, `featherless`, `fireworks`, `hyperbolic`, `kilo-gateway`, `llm7`, `mistral`, `morph`, `nebius`, `nvidia`, `perplexity`, `poolside`, `sambanova`, `siliconflow`, `tencent`, `together`, `venice`, `volcengine-ark`, `openai`, `anthropic`, `gemini`, `glm-cn`, `xiaomi-tokenplan`, `mimo-free`, `mmf`, `opencode`, `ollama-local`, `vertex-partner`, `tokenrouter`, `perplexity-agent`, `cloudflare-ai`, `muse`, `xai`, `gitlab`, `iflow`, `cline`, `clinepass`, `kilocode`, `kimchi`, `azure` — generic 429/5xx/401 classification; the pin has no provider-specific capacity contract for it Tests: `ingress:TestRecordUpstreamFailureMapsProviderErrToCooldown`, `ingress:TestProviderErrorBodiesCoolTheAccountLikeThePin`.
- **U** `typesafe`, `gemini-cli`, `vertex`, `grok-web`, `perplexity-web` — refused by its immutable readiness; no capacity behaviour is reachable Tests: `ingress:TestReadinessMatchesIngressReachability`.
- **G** `groq` — the quota reader returns an error for every key (see the quota column), so no exhausted-key state is ever observed Tests: `ingress:TestOpenP08ErrorClassificationGaps`.
- **G** `openrouter` — pin: 402 cools the account for two minutes and falls back (errorConfig.js:77). Routeweft treats 402 as a terminal client error Tests: `ingress:TestOpenP08ErrorClassificationGaps`.
- **G** `deepseek` — pin: 402 Insufficient Balance cools the account for two minutes and falls back (errorConfig.js:77, accountFallback.js:35-46), and the balance endpoint reports is_available:false. Routeweft treats 402 as a terminal client error (no cooldown, no fallback) and reads a zero balance as unlimited Tests: `ingress:TestOpenP08ErrorClassificationGaps`, `providers/usage:TestDeepSeekReportsBalance`.

### Import / refresh

- **P** `codex` — PKCE, account id, bulk import, rotation Tests: `providers/onboarding:TestCodexCapturesAccountID`, `providers/onboarding:TestCodexBulkExportedAccountDataWinsOverTheJWT`, `providers/onboarding:TestBulkImportKeepsAnUnknownExpiryUnknown`, `providers/onboarding:TestRefreshKeepsRotatedRefreshToken`.
- **P** `claude` — paste-code PKCE, rotating refresh Tests: `providers/onboarding:TestClaudePasteCodeState`, `providers/onboarding:TestRefreshCoversReferenceTable`, `providers/oauthrefresh:TestRotatedCredentialIsDurableAndKeepsProviderData`.
- **P** `github` — device flow, Copilot token re-minted without a refresh token Tests: `providers/onboarding:TestGitHubCapturesCopilotToken`, `providers/onboarding:TestGitHubRefreshRemintsCopilotToken`, `providers/onboarding:TestGitHubMissingExpiryBoundsSequentialMints`.
- **P** `gitlab` — PAT verified before it is stored Tests: `providers/onboarding:TestGitlabPATRequiresVerification`.
- **P** `iflow` — cookie import to the account key; refresh Tests: `providers/onboarding:TestIflowRequiresAPIKey`, `providers/onboarding:TestIflowCookieImportKeepsTheKeyExpiry`, `providers/oauthrefresh:TestResolverIssuesARealRefreshRequest`.
- **P** `xiaomi-mimo` — callback decrypt, key import; account credentials stored sealed and excluded from read models and logs Tests: `providers/onboarding:TestXiaomiCallbackDecrypts`, `providers/onboarding:TestXiaomiImportHoldsSealedAccountFields`, `providers/onboarding:TestMimoImportSealedAndAbsentFromReadModelsAndLogs`.
- **P** `cline`, `clinepass` — token bundle decode, refresh Tests: `providers/onboarding:TestClineDecodesTokenBundle`, `providers/onboarding:TestRefreshCoversReferenceTable`.
- **P** `kilocode` — device approval stores a non-expiring bearer; the pin has no refresh grant for it Tests: `control/api:TestOAuthDeviceApprovalStoresNonExpiringBearerOnce`.
- **P** `codebuddy-cn`, `codebuddy-intl` — device handshake, refresh Tests: `providers/onboarding:TestCodeBuddyPendingCodeIsNotFailure`, `providers/onboarding:TestCodeBuddyUsesRouteweftIdentityThroughoutHandshake`, `providers/onboarding:TestRefreshCoversReferenceTable`.
- **P** `antigravity`, `gemini-cli` — Google client from the environment; refresh grant Tests: `providers/onboarding:TestGoogleClientComesFromTheEnvironment`, `providers/onboarding:TestGoogleFlowsRefuseToStartWithoutClient`, `providers/onboarding:TestRefreshCoversReferenceTable`.
- **P** `cursor` — token + machine id import validated without a network call; auto-import is deliberately absent (PARITY row 29) Tests: `providers/onboarding:TestCursorImportValidatesShapeAndCallsNothing`.
- **P** `kiro` — device, token, CLI-proxy and API-key import; endpoint by credential shape Tests: `providers/onboarding:TestKiroDeviceFlowRegistersClient`, `providers/onboarding:TestKiroTokenImportProvesTheTokenByRefreshing`, `providers/onboarding:TestKiroCLIProxyImportNormalizesWithoutANetworkCall`, `providers/onboarding:TestKiroRefreshPicksTheEndpointFromTheCredentialShape`, `providers/onboarding:TestKiroAPIKeyIsProvenAgainstTheCatalog`.
- **P** `grok-cli` — bulk import (snake and camel case), xAI refresh grant Tests: `providers/onboarding:TestBulkImportKeepsAnUnknownExpiryUnknown`, `providers/onboarding:TestRefreshCoversReferenceTable`.
- **P** `zed` — RSA callback decrypt (zed.js oauth block); the pin has no refresh (zed.js refreshCredentials returns null), nor does Routeweft Tests: `providers/onboarding:TestZedCallbackDecrypts`.
- **P** `kimchi` — token validated on import Tests: `providers/onboarding:TestKimchiRejectsInvalidToken`.
- **T** `xai` — shares the xAI refresh grant with grok-cli; no xai-specific import test Tests: `providers/onboarding:TestRefreshCoversReferenceTable`, `providers/onboarding:TestRedirectURIsMatchRegisteredValues`.
- **T** `kimi` — device flow type and refresh grant checked in tables; no kimi-specific import test Tests: `providers/onboarding:TestRefreshCoversReferenceTable`, `providers/onboarding:TestFlowTypesMatchReference`.
- **T** `qoder` — flow type is checked against the pin; the PAT-to-job-token exchange is not implemented (PARITY 6.15) Tests: `providers/onboarding:TestFlowTypesMatchReference`, `providers/onboarding:TestEveryOracleFlowIsImplemented`.
- **G** `vertex` — service-account / ADC credential import is not built (readiness note); no test
- **G** `grok-web`, `perplexity-web` — cookie/session import has no implementation or test; the provider is unavailable
- **N** `alicode-intl` — open-sse/providers/registry/alicode-intl.js:1-30 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `alitp-intl` — open-sse/providers/registry/alitp-intl.js:1-35 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `alicode` — open-sse/providers/registry/alicode.js:1-31 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `alims-intl` — open-sse/providers/registry/alims-intl.js:1-32 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `api-airforce` — open-sse/providers/registry/api-airforce.js:1-39 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `baidu` — open-sse/providers/registry/baidu.js:1-33 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `bazaarlink` — open-sse/providers/registry/bazaarlink.js:1-47 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `blackbox` — open-sse/providers/registry/blackbox.js:1-41 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `bluesminds` — open-sse/providers/registry/bluesminds.js:1-38 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `byteplus` — open-sse/providers/registry/byteplus.js:1-35 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `cerebras` — open-sse/providers/registry/cerebras.js:1-32 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `chutes` — open-sse/providers/registry/chutes.js:1-24 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `cohere` — open-sse/providers/registry/cohere.js:1-25 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `featherless` — open-sse/providers/registry/featherless.js:1-34 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `fireworks` — open-sse/providers/registry/fireworks.js:1-27 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `groq` — open-sse/providers/registry/groq.js:1-38 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `hyperbolic` — open-sse/providers/registry/hyperbolic.js:1-35 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `kilo-gateway` — open-sse/providers/registry/kilo-gateway.js:1-34 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `llm7` — open-sse/providers/registry/llm7.js:1-35 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `mistral` — open-sse/providers/registry/mistral.js:1-29 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `morph` — open-sse/providers/registry/morph.js:1-29 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `nebius` — open-sse/providers/registry/nebius.js:1-25 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `nvidia` — open-sse/providers/registry/nvidia.js:1-34 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `openrouter` — open-sse/providers/registry/openrouter.js:1-35 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `perplexity` — open-sse/providers/registry/perplexity.js:1-32 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `poolside` — open-sse/providers/registry/poolside.js:1-31 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `sambanova` — open-sse/providers/registry/sambanova.js:1-27 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `siliconflow` — open-sse/providers/registry/siliconflow.js:1-39 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `tencent` — open-sse/providers/registry/tencent.js:1-27 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `together` — open-sse/providers/registry/together.js:1-28 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `venice` — open-sse/providers/registry/venice.js:1-42 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `vercel-ai-gateway` — open-sse/providers/registry/vercel-ai-gateway.js:1-38 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `volcengine-ark` — open-sse/providers/registry/volcengine-ark.js:1-35 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `openai` — open-sse/providers/registry/openai.js:1-64 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `anthropic` — open-sse/providers/registry/anthropic.js:1-34 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `gemini` — open-sse/providers/registry/gemini.js:1-51: the pin's oauth key is only the header scheme for the OAuth form of the API credential; Routeweft serves the API-key flow (PROVIDER_BASELINE)
- **N** `deepseek` — open-sse/providers/registry/deepseek.js:1-73 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `glm` — open-sse/providers/registry/glm.js:1-62 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `glm-cn` — open-sse/providers/registry/glm-cn.js:1-39 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `minimax` — open-sse/providers/registry/minimax.js:1-71 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `minimax-cn` — open-sse/providers/registry/minimax-cn.js:1-68 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `xiaomi-tokenplan` — open-sse/providers/registry/xiaomi-tokenplan.js:1-63 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `mimo-free` — open-sse/providers/registry/mimo-free.js:1-27 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `mmf` — open-sse/providers/registry/mmf.js:1-19 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `opencode` — open-sse/providers/registry/opencode.js:1-41 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `ollama` — open-sse/providers/registry/ollama.js:1-40 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `ollama-local` — open-sse/providers/registry/ollama-local.js:1-19 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `typesafe` — open-sse/providers/registry/typesafe.js:1-29 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `vertex-partner` — open-sse/providers/registry/vertex-partner.js:1-29 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `tokenrouter` — open-sse/providers/registry/tokenrouter.js:1-56 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `perplexity-agent` — open-sse/providers/registry/perplexity-agent.js:1-44 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `opencode-go` — open-sse/providers/registry/opencode-go.js:1-80 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `cloudflare-ai` — open-sse/providers/registry/cloudflare-ai.js:1-52 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `muse` — open-sse/providers/registry/muse.js:1-25 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `commandcode` — open-sse/providers/registry/commandcode.js:1-47 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `kenari` — open-sse/providers/registry/kenari.js:1-133 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)
- **N** `azure` — open-sse/providers/registry/azure.js:1-21 declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)

### Streaming

- **P** `api-airforce` — forced stream on Chat, Responses and Messages clients (registry/api-airforce.js:25-32) Tests: `ingress:TestAirforceForcedStreamAcrossClientProtocols`.
- **P** `openrouter` — passthrough stream relayed byte for byte Tests: `ingress:TestOpenRouterPassthroughStreamRelaysBytes`, `ingress:TestRegistryNativeTransportWires`.
- **P** `gemini` — every converted client over streamGenerateContent Tests: `ingress:TestGeminiServesEveryConvertedClient`, `ingress:TestGeminiArrayFramedStreamIsServedWithUnobservedUsage`.
- **P** `xiaomi-tokenplan` — Chat and Messages wires post to their own paths Tests: `ingress:TestConnectionScopedTargetsStreamAndNonStream`, `ingress:TestTokenplanRoutesPerWire`.
- **P** `mimo-free`, `mmf` — (mmf is the same executor under its alias, executors/index.js:68) bootstrap JWT with expiry buffer and one 401/403 re-bootstrap, the gateway's headers and the MiMoCode system marker (executors/mimo-free.js:24-33,79-159); a JWT that cannot be minted refuses the request Tests: `providers/wire:TestMimoFreeBootstrapsAJWTAndSendsTheGatewaysHeaders`, `providers/wire:TestMimoFreeRetriesOnceWithAFreshJWTOnAuthFailure`, `providers/wire:TestMimoFreeRefusesWhenTheJWTCannotBeMinted`, `ingress:TestMimoFreeAndMmfAreServedWithABootstrappedJWT`.
- **P** `opencode` — forced stream (registry/opencode.js:20), Zen target Tests: `ingress:TestOpenCodeForcedStreamFoldsToOneJSONBody`, `providers/wire:TestOpenCodeForcesStream`.
- **P** `ollama` — NDJSON stream conversion Tests: `ingress:TestOllamaFinalLineWithoutNewlineIsAccounted`, `ingress:TestTruncatedOllamaStreamIsAFailure`, `protocol/ollama:TestStreamIsConvertedLineByLine`.
- **P** `typesafe` — System One stream, usage at EOF Tests: `app:TestConfiguredSystemOneWireAndAccounting`, `ingress:TestSystemOneStreamUsageAtEOFAndAfterOversizedOpaqueLine`.
- **P** `cloudflare-ai` — the System One leg; the Chat leg is verified at the URL seam only (wire:TestCloudflareTargetURLs) Tests: `app:TestConfiguredSystemOneWireAndAccounting`, `app:TestConfiguredSystemOneCloudflareFallback`.
- **P** `cline`, `clinepass` — an SSE body is not unwrapped Tests: `ingress:TestClineEnvelopeOnlyUnwrapsNonStreamSuccess`.
- **P** `codebuddy-cn`, `codebuddy-intl` — gateway answers only streams; empty tool_calls stripped, cancellation reaches the upstream Tests: `ingress:TestCodeBuddyStreamingClientKeepsItsStreamMinusEmptyToolCalls`, `ingress:TestCodeBuddyForcedStreamCancellationReachesUpstream`, `ingress:TestCodeBuddyInbandStreamErrorRelaysParsedMessage`.
- **P** `kenari` — Chat, Responses and Messages natively, both modes; Responses custom-tool rewrite on the stream Tests: `app:TestConfiguredKenariNativeWire`, `providers/wire:TestResponsesStreamRewritesConvertedCalls`.
- **P** `zed` — LLM-token exchange, thread envelope, per-vendor request and NDJSON frames for Anthropic, OpenAI Responses, Google and xAI (executors/zed.js:60-282, shared/zedAuth.js:258-324); a failed status, a cut-off stream and a non-JSON line end as the pin ends them. Open: the listing still sends the shared bearer (see Authenticated listing); the thread id resolves the pin's client session from headers and the client's body, but not after the pin's token savers (PARITY 6.19) Tests: `providers/wire:TestZedExchangesTheTokenAndSendsTheEnvelope`, `providers/wire:TestZedServesEveryVendorShape`, `providers/wire:TestZedStreamEndsAsThePinEndsIt`, `providers/wire:TestZedBrokenUpstreamIsAnErrorOnEveryLeg`, `providers/wire:TestZedErrorChunkNamesTheRequestedModel`, `providers/wire:TestZedStopsUpstreamWhenTheClientLeaves`, `ingress:TestZedIsServedThroughItsExchangeAndEnvelope`.
- **P** `azure` — deployment path, api-version and Api-Key header, streamed Tests: `ingress:TestConnectionScopedTargetsStreamAndNonStream`.
- **C** `antigravity`, `gemini-cli` — Cloud Code stream codec; the provider is unavailable (URL, client headers and project id are not bound) Tests: `protocol/gemini:TestAntigravityStreamText`, `protocol/gemini:TestCloudCodeResponseLegsServeBothWires`.
- **C** `cursor` — Connect stream codec and cancellation; the provider is unavailable Tests: `providers/wire:TestCursorConsumeStreamsTextAndStops`, `providers/wire:TestCursorDoStopsUpstreamWhenTheClientLeaves`.
- **C** `kiro` — event-stream codec; the provider is unavailable Tests: `protocol/kiro:TestStreamText`, `protocol/kiro:TestMessagesStreamText`.
- **C** `commandcode` — stream codec, forced stream envelope, replay; the Chat translation arm is not built Tests: `protocol/commandcode:TestStreamIsConvertedLineByLine`, `providers/wire:TestCommandCodeForcesStreamOnTheEnvelope`, `providers/wire:TestCommandCodeDoRecoversOnRetryAndReplaysTheStreamExactly`.
- **T** `alicode-intl`, `alitp-intl`, `alicode`, `alims-intl`, `baidu`, `bazaarlink`, `blackbox`, `bluesminds`, `byteplus`, `cerebras`, `chutes`, `cohere`, `featherless`, `fireworks`, `groq`, `hyperbolic`, `kilo-gateway`, `llm7`, `mistral`, `morph`, `nebius`, `nvidia`, `perplexity`, `poolside`, `sambanova`, `siliconflow`, `tencent`, `together`, `venice`, `vercel-ai-gateway`, `volcengine-ark`, `glm-cn`, `tokenrouter`, `gitlab` — shared path, this provider's own URL and credential: Chat Tests: `ingress:TestRegistryChatWires`.
- **T** `openai` — shared path, this provider's own URL and credential: openai-chat, openai-responses Tests: `ingress:TestRegistryNativeTransportWires`.
- **T** `anthropic`, `claude` — shared path, this provider's own URL and credential: anthropic-messages Tests: `ingress:TestRegistryNativeTransportWires`.
- **T** `deepseek` — shared path, this provider's own URL and credential: Chat, anthropic-messages Tests: `ingress:TestRegistryChatWires`, `ingress:TestRegistryNativeTransportWires`.
- **T** `glm`, `minimax`, `minimax-cn` — shared path, this provider's own URL and credential: openai-chat, anthropic-messages Tests: `ingress:TestRegistryNativeTransportWires`.
- **T** `perplexity-agent`, `muse` — shared path, this provider's own URL and credential: openai-responses Tests: `ingress:TestRegistryNativeTransportWires`.
- **T** `opencode-go` — shared path, this provider's own URL and credential: openai-chat, openai-responses, anthropic-messages. Open: Responses-only models, the session header and body normalization (executors/opencode-go.js:128-210) are not replicated (experimental) Tests: `ingress:TestRegistryNativeTransportWires`.
- **T** `xai` — shared path, this provider's own URL and credential: Chat, openai-responses Tests: `ingress:TestRegistryChatWires`, `ingress:TestRegistryNativeTransportWires`.
- **T** `github` — shared path, this provider's own URL and credential: openai-chat, openai-responses, anthropic-messages. Open: Claude models via /v1/messages and the forced stream on Responses (executors/github.js:250-275) are not replicated (experimental) Tests: `ingress:TestRegistryNativeTransportWires`.
- **T** `iflow` — Chat both modes through the table; the signature over the final body is asserted by TestIflowRequestIsSigned Tests: `ingress:TestRegistryNativeTransportWires`, `ingress:TestIflowRequestIsSigned`.
- **T** `kimi` — shared path, this provider's own URL and credential: openai-chat, anthropic-messages, openai-responses Tests: `ingress:TestRegistryNativeTransportWires`.
- **T** `xiaomi-mimo` — shared path, this provider's own URL and credential: openai-chat, anthropic-messages. Open: the Preview models served through the account-session route (executors/xiaomi-mimo.js:21-99) are not replicated; Cloud API models are the shared path Tests: `ingress:TestRegistryNativeTransportWires`.
- **T** `kilocode`, `kimchi` — shared path, this provider's own URL and credential: openai-chat Tests: `ingress:TestRegistryNativeTransportWires`.
- **S** `ollama-local` — the shared Ollama codec; no test names ollama-local (experimental) Tests: `ingress:TestTruncatedOllamaStreamIsAFailure`, `protocol/ollama:TestBufferedBodyIsConverted`.
- **U** `vertex`, `grok-web`, `perplexity-web` — refused by its immutable readiness; no codec exists to test Tests: `ingress:TestReadinessMatchesIngressReachability`.
- **G** `codex` — the table row proves the URL, bearer and CLI headers only. The pin's executor also forces stream:true and store:false, injects default instructions, maps review models, drops unsupported fields and filters to an allowlist (executors/codex.js:415-515); Routeweft forwards the client's body (ready, unqualified; PARITY P02) Tests: `ingress:TestRegistryNativeTransportWires`, `ingress:TestCodexProviderHeadersCarryCLIFingerprint`.
- **G** `vertex-partner` — only the URL seam is tested; the target is an absolute Google host, so no request is dispatched in a test (provider is experimental) Tests: `providers/wire:TestVertexPartnerTargetUsesTheConnectionProject`.
- **G** `qoder` — only the request signing seam is tested; no response codec exists at the pin for dispatch (registry/qoder.js:19-26), so streamed and buffered bodies are untested Tests: `ingress:TestQoderDispatchSeamSignsFinalRequest`.
- **G** `grok-cli` — the table row proves the URL and bearer only. The pin's executor sends the grok-shell identity (x-grok-session-id, conv-id, req-id, turn-idx, model-override, email, userid), forces stream:true and store:false and normalizes input and tools (executors/grok-cli.js:362-470); Routeweft sends none of that at inference, though its listing needs the identity (ready, unqualified; PARITY P02) Tests: `ingress:TestRegistryNativeTransportWires`.

### Non-streaming

- **P** `api-airforce` — folded to one JSON body; a failed stream is never returned as partial JSON Tests: `ingress:TestAirforceForcedStreamAcrossClientProtocols`, `ingress:TestAirforceStreamFailureDoesNotReturnPartialJSON`.
- **P** `gemini` — every converted client over generateContent Tests: `ingress:TestGeminiServesEveryConvertedClient`, `protocol/gemini:TestBufferedResponse`.
- **P** `xiaomi-tokenplan` — Chat and Messages wires post to their own paths Tests: `ingress:TestConnectionScopedTargetsStreamAndNonStream`, `ingress:TestTokenplanRoutesPerWire`.
- **P** `mimo-free`, `mmf` — (mmf is the same executor under its alias) the same bootstrap and marker on a buffered request; Accept follows the body's stream; a JWT that cannot be minted sends nothing Tests: `providers/wire:TestMimoFreeAcceptFollowsTheBodysStream`, `providers/wire:TestMimoSystemMarker`, `ingress:TestMimoFreeAndMmfAreServedWithABootstrappedJWT`, `ingress:TestMimoFreeWithoutAJWTSendsNothingAndFails`.
- **P** `opencode` — folded to one JSON body Tests: `ingress:TestOpenCodeForcedStreamFoldsToOneJSONBody`.
- **P** `ollama` — buffered body conversion Tests: `protocol/ollama:TestBufferedBodyIsConverted`, `ingress:TestOllamaRefusalMovesToTheNextCandidate`.
- **P** `typesafe` — System One non-stream Tests: `app:TestConfiguredSystemOneWireAndAccounting`.
- **P** `cloudflare-ai` — the System One leg; the Chat leg is verified at the URL seam only Tests: `app:TestConfiguredSystemOneWireAndAccounting`.
- **P** `kimi` — Chat fixture replayed byte for byte; Messages beta query and device headers; Responses through the table Tests: `ingress:TestProviderKimiChatNativeReplaysFixture`, `ingress:TestMessagesBetaQueryAndKimiDeviceHeaders`, `ingress:TestRegistryNativeTransportWires`.
- **P** `xiaomi-mimo` — Messages fixture posts to the provider path; Chat through the table. Open: Preview models served through the account-session route (executors/xiaomi-mimo.js:21-99) are not replicated Tests: `ingress:TestProviderXiaomiMimoMessagesNativeUsesProviderPath`, `ingress:TestRegistryNativeTransportWires`.
- **P** `cline`, `clinepass` — the {success,data} envelope is unwrapped and accounted Tests: `ingress:TestClineEnvelopeFixtureIsUnwrappedAndAccounted`, `ingress:TestClineEnvelopeIsUnwrappedBeforeResponsesTranslation`, `ingress:TestClineAuthorizationPrefixReachesUpstream`.
- **P** `codebuddy-cn`, `codebuddy-intl` — a non-stream client gets one JSON body folded from the forced stream Tests: `ingress:TestCodeBuddyNonStreamClientsGetOneJSONBodyFromAForcedStream`, `ingress:TestCodeBuddyJSONAnswerToANonStreamRequestPassesThrough`, `ingress:TestCodeBuddyForcedStreamFailuresAreErrorsNotPartialAnswers`.
- **P** `kenari` — native wires and the Responses body rewrite Tests: `app:TestConfiguredKenariNativeWire`, `providers/wire:TestRewriteResponsesBody`.
- **P** `zed` — the forced stream folded to one body for the Chat, Responses and Messages clients (executors/zed.js:60-282); a failed status is the assistant's text with a clean stop and still cools the account and lets the next candidate serve, a cut-off stream is finished, as the pin does Tests: `ingress:TestZedIsServedThroughItsExchangeAndEnvelope`, `ingress:TestZedFailedStatusIsTheAssistantsTextWithACleanStop`, `ingress:TestZedFailedStatusCoolsTheAccountAndTheNextCandidateServes`, `ingress:TestZedQuotaLockOutlastsTheDefaultBackoffAndARateLimitDoesNot`, `ingress:TestZedCutOffStreamIsFinishedAsThePinDoes`.
- **P** `azure` — deployment path, api-version and Api-Key header Tests: `ingress:TestAzureRequestShape`, `ingress:TestConnectionScopedTargetsStreamAndNonStream`.
- **C** `antigravity`, `gemini-cli` — Cloud Code buffered codec; the provider is unavailable Tests: `protocol/gemini:TestAntigravityBufferedResponse`, `protocol/gemini:TestCloudCodeResponseLegsServeBothWires`.
- **C** `cursor` — buffered body codec; the provider is unavailable Tests: `protocol/cursor:TestBufferedBody`.
- **C** `kiro` — buffered codec; the provider is unavailable Tests: `protocol/kiro:TestBufferedMatchesStreamed`, `protocol/kiro:TestMessagesBufferedMatchesStream`.
- **C** `commandcode` — buffered aggregate codec; the provider is unavailable Tests: `protocol/commandcode:TestBufferedBodyIsAggregated`.
- **T** `alicode-intl`, `alitp-intl`, `alicode`, `alims-intl`, `baidu`, `bazaarlink`, `blackbox`, `bluesminds`, `byteplus`, `cerebras`, `chutes`, `cohere`, `featherless`, `fireworks`, `groq`, `hyperbolic`, `kilo-gateway`, `llm7`, `mistral`, `morph`, `nebius`, `nvidia`, `perplexity`, `poolside`, `sambanova`, `siliconflow`, `tencent`, `together`, `venice`, `vercel-ai-gateway`, `volcengine-ark`, `glm-cn`, `tokenrouter`, `gitlab` — shared path, this provider's own URL and credential: Chat Tests: `ingress:TestRegistryChatWires`.
- **T** `openrouter`, `kilocode`, `kimchi` — shared path, this provider's own URL and credential: openai-chat Tests: `ingress:TestRegistryNativeTransportWires`.
- **T** `openai` — shared path, this provider's own URL and credential: openai-chat, openai-responses Tests: `ingress:TestRegistryNativeTransportWires`.
- **T** `anthropic`, `claude` — shared path, this provider's own URL and credential: anthropic-messages Tests: `ingress:TestRegistryNativeTransportWires`.
- **T** `deepseek` — shared path, this provider's own URL and credential: Chat, anthropic-messages Tests: `ingress:TestRegistryChatWires`, `ingress:TestRegistryNativeTransportWires`.
- **T** `glm`, `minimax`, `minimax-cn` — shared path, this provider's own URL and credential: openai-chat, anthropic-messages Tests: `ingress:TestRegistryNativeTransportWires`.
- **T** `perplexity-agent`, `muse` — shared path, this provider's own URL and credential: openai-responses Tests: `ingress:TestRegistryNativeTransportWires`.
- **T** `opencode-go` — shared path, this provider's own URL and credential: openai-chat, openai-responses, anthropic-messages. Open: Responses-only models, the session header and body normalization (executors/opencode-go.js:128-210) are not replicated (experimental) Tests: `ingress:TestRegistryNativeTransportWires`.
- **T** `xai` — shared path, this provider's own URL and credential: Chat, openai-responses Tests: `ingress:TestRegistryChatWires`, `ingress:TestRegistryNativeTransportWires`.
- **T** `github` — shared path, this provider's own URL and credential: openai-chat, openai-responses, anthropic-messages. Open: Claude models via /v1/messages and the forced stream on Responses (executors/github.js:250-275) are not replicated (experimental) Tests: `ingress:TestRegistryNativeTransportWires`.
- **T** `iflow` — Chat both modes through the table; the signature over the final body is asserted by TestIflowRequestIsSigned Tests: `ingress:TestRegistryNativeTransportWires`, `ingress:TestIflowRequestIsSigned`.
- **S** `ollama-local` — the shared Ollama codec; no test names ollama-local (experimental) Tests: `ingress:TestTruncatedOllamaStreamIsAFailure`, `protocol/ollama:TestBufferedBodyIsConverted`.
- **U** `vertex`, `grok-web`, `perplexity-web` — refused by its immutable readiness; no codec exists to test Tests: `ingress:TestReadinessMatchesIngressReachability`.
- **G** `codex` — the table row proves the URL, bearer and CLI headers only. The pin's executor also forces stream:true and store:false, injects default instructions, maps review models, drops unsupported fields and filters to an allowlist (executors/codex.js:415-515); Routeweft forwards the client's body (ready, unqualified; PARITY P02) Tests: `ingress:TestRegistryNativeTransportWires`, `ingress:TestCodexProviderHeadersCarryCLIFingerprint`.
- **G** `vertex-partner` — only the URL seam is tested; the target is an absolute Google host, so no request is dispatched in a test (provider is experimental) Tests: `providers/wire:TestVertexPartnerTargetUsesTheConnectionProject`.
- **G** `qoder` — only the request signing seam is tested; no response codec exists at the pin for dispatch (registry/qoder.js:19-26), so streamed and buffered bodies are untested Tests: `ingress:TestQoderDispatchSeamSignsFinalRequest`.
- **G** `grok-cli` — the table row proves the URL and bearer only. The pin's executor sends the grok-shell identity (x-grok-session-id, conv-id, req-id, turn-idx, model-override, email, userid), forces stream:true and store:false and normalizes input and tools (executors/grok-cli.js:362-470); Routeweft sends none of that at inference, though its listing needs the identity (ready, unqualified; PARITY P02) Tests: `ingress:TestRegistryNativeTransportWires`.

### Error shapes

- **P** `api-airforce` — a failed forced stream is a 502, never partial JSON Tests: `ingress:TestAirforceStreamFailureDoesNotReturnPartialJSON`.
- **P** `groq` — rate_limit_exceeded body Tests: `ingress:TestProviderErrorBodiesCoolTheAccountLikeThePin`.
- **P** `mistral` — bare {message} body Tests: `ingress:TestProviderErrorBodiesCoolTheAccountLikeThePin`.
- **P** `codex` — usage_limit_reached body reaches the client unchanged Tests: `ingress:TestProviderErrorBodiesReachTheClientByteForByte`.
- **P** `anthropic` — {type:error,error:{type}} body: overloaded_error 529 and rate_limit_error 429 cool the account; the body reaches the client unchanged Tests: `ingress:TestProviderErrorBodiesCoolTheAccountLikeThePin`, `ingress:TestProviderErrorBodiesReachTheClientByteForByte`, `providers/shared:TestParseErrorNormalizesProviderErrBodies`.
- **P** `gemini` — translation failure moves on; RESOURCE_EXHAUSTED is rate-limited Tests: `ingress:TestGeminiTranslationFailureMovesOn`, `providers/shared:TestParseErrorNormalizesProviderErrBodies`.
- **P** `deepseek` — 401 and 400 bodies classified; the 402 Insufficient Balance body reaches the client unchanged (its classification is a gap, see Unavailability) Tests: `ingress:TestProviderErrorBodiesCoolTheAccountLikeThePin`, `ingress:TestProviderErrorBodiesReachTheClientByteForByte`.
- **P** `minimax` — Anthropic-shaped body on the Messages wire Tests: `ingress:TestProviderErrorBodiesCoolTheAccountLikeThePin`, `providers/shared:TestParseErrorNormalizesProviderErrBodies`.
- **P** `xiaomi-tokenplan` — a key is never sent to another region Tests: `providers/wire:TestTokenplanNeverMovesAKeyToAnotherRegionUnlessAsked`.
- **P** `opencode` — models served on another wire are refused, not posted a Chat body Tests: `providers/wire:TestOpenCodeRefusesModelsServedOnAnotherWire`.
- **P** `ollama` — a refusal moves on; a truncated stream is a failure Tests: `ingress:TestOllamaRefusalMovesToTheNextCandidate`, `ingress:TestTruncatedOllamaStreamIsAFailure`.
- **P** `typesafe` — validation errors and an opaque upstream error Tests: `app:TestConfiguredSystemOneValidationAndOpaqueErrorWithoutDatabase`, `ingress:TestSystemOneRelayDistinguishesReadFailureFromEOF`.
- **P** `vertex-partner` — no project or an unsafe project is refused before dispatch Tests: `providers/wire:TestVertexPartnerRefusesWithoutAProject`, `providers/wire:TestVertexPartnerRejectsAProjectThatIsNotAPathSegment`, `ingress:TestTargetRefusalFallsThroughToTheNextCandidate`.
- **P** `cloudflare-ai` — an unsafe or missing account id is refused; System One fallback Tests: `providers/wire:TestCloudflareTargetFailsClosed`, `app:TestConfiguredSystemOneCloudflareFallback`.
- **P** `iflow` — an empty credential is refused, not signed Tests: `ingress:TestIflowWithoutCredentialFailsClosed`, `providers/wire:TestIflowHeadersRefuseToSignAnEmptyKey`.
- **P** `kimi` — rate_limit_reached_error body Tests: `ingress:TestProviderErrorBodiesCoolTheAccountLikeThePin`.
- **P** `cline`, `clinepass` — a failure envelope (HTTP 500) is not unwrapped Tests: `ingress:TestClineEnvelopeOnlyUnwrapsNonStreamSuccess`.
- **P** `codebuddy-cn` — gateway frequency-limit body, in-band stream error, long error bodies Tests: `providers/wire:TestCodeBuddyFrequencyLimitMessageOfEveryType`, `ingress:TestCodeBuddyErrorBodySurvivesFailedFallbackBeyondPeekLimit`, `ingress:TestCodeBuddyInbandStreamErrorRelaysParsedMessage`.
- **P** `codebuddy-intl` — same hook as codebuddy-cn Tests: `providers/wire:TestCodeBuddyFrequencyLimitMessageOfEveryType`, `ingress:TestCodeBuddyErrorBodySurvivesFailedFallbackBeyondPeekLimit`, `ingress:TestCodeBuddyInbandStreamErrorRelaysParsedMessage`.
- **P** `grok-cli` — {code,error} body of the 402 spending-limit reaches the client unchanged Tests: `ingress:TestProviderErrorBodiesReachTheClientByteForByte`.
- **P** `kenari` — failed terminal and refusal events keep their sequence; unreadable arguments are refused Tests: `providers/wire:TestResponsesStreamRewritesAFailedTerminal`, `providers/wire:TestResponsesStreamRefusalContinuesTheSequence`, `providers/wire:TestRewriteResponsesBodyRefusesArgumentsItCannotRead`.
- **P** `zed` — trial_blocked 403 cools the account; the pin's message rewrite (zed.js:284-310) is not done, the client sees the upstream body Tests: `ingress:TestProviderErrorBodiesCoolTheAccountLikeThePin`.
- **P** `azure` — no endpoint or an unsafe deployment sends nothing and says why Tests: `ingress:TestAzureWithoutAnEndpointOrAValidDeploymentSendsNothing`, `ingress:TestTargetRefusalMessageReachesTheClient`.
- **C** `antigravity` — typed refusal and stream failures; the provider is unavailable Tests: `protocol/gemini:TestAntigravityStreamFailures`, `protocol/gemini:TestAntigravityRefusalIsTyped`.
- **C** `gemini-cli` — shares the Cloud Code response legs; the pin's RetryInfo retryDelay hint (gemini-cli.js:38-52) is not read; the provider is unavailable Tests: `protocol/gemini:TestAntigravityStreamFailures`, `protocol/gemini:TestCloudCodeResponseLegsServeBothWires`.
- **C** `cursor` — in-band failures and upstream rejections; the provider is unavailable Tests: `providers/wire:TestCursorDoReportsAFailureInBandNotAsSuccess`, `providers/wire:TestCursorDoPassesUpstreamRejectionsThrough`, `protocol/cursor:TestStreamFailures`.
- **C** `qoder` — refuses before the network without an identity; no response error codec; the provider is unavailable Tests: `ingress:TestQoderMissingIdentityFailsBeforeNetwork`.
- **C** `kiro` — exception frames; the provider is unavailable Tests: `protocol/kiro:TestUpstreamExceptionFrames`, `ingress:TestKiroWithoutCredentialDoesNotDispatch`.
- **C** `commandcode` — in-band error events (executors/commandcode.js:60-100); the provider is unavailable Tests: `providers/wire:TestParseCommandCodeError`, `providers/wire:TestCommandCodeDoRetriesAnErrorEventThenReturnsItAsHTTP`, `providers/wire:TestCommandCodeDoNeverRetriesAnHTTPError`.
- **T** `github` — no provider-specific error shape; the pin's Copilot executor has none either Tests: `providers/shared:TestParseErrorNormalizesProviderErrBodies`, `ingress:TestRecordUpstreamFailureMapsProviderErrToCooldown`.
- **S** `alicode-intl`, `alitp-intl`, `alicode`, `alims-intl`, `baidu`, `bazaarlink`, `blackbox`, `bluesminds`, `byteplus`, `cerebras`, `chutes`, `cohere`, `featherless`, `fireworks`, `hyperbolic`, `kilo-gateway`, `llm7`, `morph`, `nebius`, `nvidia`, `openrouter`, `perplexity`, `poolside`, `sambanova`, `siliconflow`, `tencent`, `together`, `venice`, `vercel-ai-gateway`, `volcengine-ark`, `openai`, `claude`, `glm`, `glm-cn`, `minimax-cn`, `mimo-free`, `mmf`, `ollama-local`, `tokenrouter`, `perplexity-agent`, `opencode-go`, `muse`, `xai`, `gitlab`, `xiaomi-mimo`, `kilocode`, `kimchi` — shared status/body classification; the pin's executor has no provider-specific parseError Tests: `providers/shared:TestParseErrorNormalizesProviderErrBodies`, `ingress:TestRecordUpstreamFailureMapsProviderErrToCooldown`.
- **U** `vertex`, `grok-web`, `perplexity-web` — refused by its immutable readiness; no error codec to test Tests: `ingress:TestReadinessMatchesIngressReachability`.

### Cached vs ordinary tokens

- **P** `api-airforce` — usage from the forced stream, both client modes Tests: `ingress:TestAirforceForcedStreamAcrossClientProtocols`.
- **P** `anthropic` — cache_read_input_tokens and cache_creation_input_tokens, stream (message_start + message_delta) and non-stream Tests: `ingress:TestProviderCachedAndOrdinaryTokensAreSeparateInTheAccountingRecord`, `providers/shared:TestParseUsagePerProtocolFamily`.
- **P** `gemini` — cachedContentTokenCount; one usage record per request Tests: `providers/shared:TestParseUsagePerProtocolFamily`, `ingress:TestGeminiServesEveryConvertedClient`.
- **P** `deepseek` — prompt_cache_hit_tokens beside prompt_tokens, both modes (not a pin shape; DeepSeek's documented one) Tests: `ingress:TestProviderCachedAndOrdinaryTokensAreSeparateInTheAccountingRecord`.
- **P** `minimax` — Messages cache_read_input_tokens, both modes Tests: `ingress:TestProviderCachedAndOrdinaryTokensAreSeparateInTheAccountingRecord`.
- **P** `ollama` — prompt_eval_cached_count is a subset of the prompt; a final line without a newline is accounted Tests: `providers/shared:TestParseUsagePerProtocolFamily`, `ingress:TestOllamaFinalLineWithoutNewlineIsAccounted`.
- **P** `typesafe` — System One usage Tests: `providers/shared:TestSystemOneUsageNativeAndCloudflare`, `app:TestConfiguredSystemOneWireAndAccounting`.
- **P** `cloudflare-ai` — Cloudflare System One usage; the Chat leg is the shared OpenAI shape Tests: `providers/shared:TestSystemOneUsageNativeAndCloudflare`.
- **P** `kimi` — top-level cached_tokens, both modes Tests: `ingress:TestProviderCachedAndOrdinaryTokensAreSeparateInTheAccountingRecord`, `ingress:TestProviderKimiChatNativeReplaysFixture`.
- **P** `cline`, `clinepass` — usage inside the unwrapped envelope is accounted Tests: `ingress:TestClineEnvelopeFixtureIsUnwrappedAndAccounted`.
- **P** `codebuddy-cn` — usage fixture replayed; missing usage stays unobserved, not zero Tests: `ingress:TestCodeBuddyCNUsageFixtureReplays`, `ingress:TestCodeBuddyMissingUsageStaysUnobserved`, `ingress:TestCodeBuddyTranslatedStreamsKeepUsageOptIn`.
- **P** `codebuddy-intl` — same hook as codebuddy-cn Tests: `ingress:TestCodeBuddyMissingUsageStaysUnobserved`, `ingress:TestCodeBuddyTranslatedStreamsKeepUsageOptIn`.
- **C** `antigravity` — Cloud Code usage metadata; the provider is unavailable Tests: `protocol/gemini:TestAntigravityUsage`.
- **C** `gemini-cli` — shares the Cloud Code usage metadata; the provider is unavailable Tests: `protocol/gemini:TestAntigravityUsage`, `protocol/gemini:TestCloudCodeResponseLegsServeBothWires`.
- **C** `cursor` — no usage on the Connect stream stays unobserved; the provider is unavailable Tests: `protocol/cursor:TestStreamIsPassedThroughWithoutUsage`.
- **C** `kiro` — cacheReadInputTokens kept apart (kiro.js:917); the provider is unavailable Tests: `protocol/kiro:TestCacheCountsAreSeparate`, `protocol/kiro:TestUsageOnlyWhenMeasured`, `protocol/kiro:TestKiroUsage`.
- **C** `commandcode` — finish usage; the provider is unavailable Tests: `protocol/commandcode:TestUsage`.
- **T** `mistral` — prompt_tokens_details.cached_tokens, both modes Tests: `ingress:TestProviderCachedAndOrdinaryTokensAreSeparateInTheAccountingRecord`.
- **T** `codex` — Responses input_tokens_details.cached_tokens, both modes Tests: `ingress:TestProviderCachedAndOrdinaryTokensAreSeparateInTheAccountingRecord`, `providers/shared:TestParseUsageNestedResponsesReadsCachedTokens`.
- **T** `muse`, `xai`, `grok-cli` — Responses input_tokens_details.cached_tokens, both modes Tests: `ingress:TestProviderCachedAndOrdinaryTokensAreSeparateInTheAccountingRecord`.
- **S** `alicode-intl`, `alitp-intl`, `alicode`, `alims-intl`, `baidu`, `bazaarlink`, `blackbox`, `bluesminds`, `byteplus`, `cerebras`, `chutes`, `cohere`, `featherless`, `fireworks`, `groq`, `hyperbolic`, `kilo-gateway`, `llm7`, `morph`, `nebius`, `nvidia`, `openrouter`, `perplexity`, `poolside`, `sambanova`, `siliconflow`, `tencent`, `together`, `venice`, `vercel-ai-gateway`, `volcengine-ark`, `openai`, `claude`, `glm`, `glm-cn`, `minimax-cn`, `xiaomi-tokenplan`, `mimo-free`, `mmf`, `opencode`, `ollama-local`, `vertex-partner`, `tokenrouter`, `perplexity-agent`, `opencode-go`, `github`, `gitlab`, `iflow`, `xiaomi-mimo`, `kilocode`, `kenari`, `zed`, `kimchi`, `azure` — shared OpenAI-shape accounting (prompt_tokens_details.cached_tokens); nothing names this provider Tests: `providers/shared:TestParseUsagePerProtocolFamily`, `ingress:TestNativeNonStreamEmitsExactlyOneReportedRecord`, `ingress:TestNativeStreamEmitsOneRecordFromFinalChunk`.
- **U** `vertex`, `grok-web`, `perplexity-web` — refused by its immutable readiness; no usage codec to test Tests: `ingress:TestReadinessMatchesIngressReachability`.
- **G** `qoder` — the pin maps cached counts from several spellings (shared/qoder/sse.js:21-54); no response codec exists, so none is tested; the provider is unavailable

## What P08 added

New deterministic tests (no network, sanitized synthetic bodies shaped like the pin):

- `internal/providers/usage/p08_test.go`: pin-shaped quota fixtures for the readers that had none, namely OpenCode Go, Ollama, Xiaomi MiMo, Zed, Command Code, MiniMax and MiniMax CN, Qoder, and the GLM CN and CodeBuddy International regional hosts. They assert used-versus-remaining semantics, that an absent window is skipped rather than invented, exhausted windows, and that failures are errors, not healthy accounts.
- `internal/ingress/p08_tokens_test.go`: cached versus ordinary tokens through real dispatch with each provider's real id, stream and non-stream: Anthropic and MiniMax Messages cache read/creation, DeepSeek `prompt_cache_hit_tokens`, Kimi top-level `cached_tokens`, Mistral details, and Responses `input_tokens_details` for Codex, Grok CLI, Muse and xAI.
- `internal/ingress/p08_wires_test.go`: stream and non-stream on every native transport of every served provider that `TestRegistryChatWires` (Chat only) did not already walk, the connection-scoped Azure and Token Plan targets, and OpenCode's forced stream folded to one JSON body. These are `T` evidence, not provider-specific behaviour.
- `internal/ingress/p08_errors_test.go`: provider-keyed non-OpenAI error bodies classified against the pin's rules and relayed to the client byte for byte, plus `TestOpenP08ErrorClassificationGaps`, which pins the two divergences below so closing one is a visible change.
- `internal/parity/listing_urls_test.go` (oracle-gated): the authenticated-listing URL Routeweft builds for each of the 23 providers the pin lists with a plain bearer GET equals the pinned URL.

## Gaps this audit found and did not fix

These are findings, not new work in this PR. Each is recorded in PARITY and stays open.

1. **402 Payment Required is not an unavailability signal.** The pin cools the account for two minutes and falls back (`errorConfig.js:77`, `accountFallback.js:35-46`). Routeweft classifies 402 as a terminal client error: no cooldown and no fallback, so an out-of-credit DeepSeek or OpenRouter account keeps (Grok CLI closed it in #184) being tried and keeps failing. `TestOpenP08ErrorClassificationGaps`.
2. **Codex `usage_limit_reached` ignored `resets_at` when this audit began.** P02 (#177) closed it (`wire/error_reset.go`); the gap guard for it was removed and the Codex cell is now P.
3. **Groq has no quota reader in effect.** The reader discards the `x-ratelimit-*` headers the pin reads (`usage/groq.js:44-129`) and always returns an error.
4. **A zero DeepSeek balance reads as unlimited** (the reader's `window()` treats a total of 0 as unlimited), so the account is never seen as out of credit. P05 listed this as open; no test exists for it because the expected behaviour would fail.
5. **Providers declared ready whose pin executor is not replicated.** The shared-path table proves their URL and credential only: Codex (`executors/codex.js:415-515`: forced stream, `store:false`, default instructions, allowlist) and Grok CLI (`executors/grok-cli.js:362-470`: grok-shell identity, forced stream). Their streaming and non-streaming cells are `G`. Whether their readiness should change is a product decision for #76 P02 and the readiness table (#176). Zed and MiMo Free with its alias mmf were in this list until #192, which built their executors (token exchange, thread envelope, NDJSON frames, bootstrap JWT); their cells are now evidence and their readiness stays `ready`.
6. Smaller, in the cell notes: Zed's `{limited:N}` quota form, Qoder's absent pool read as unlimited, Command Code's empty window, MiniMax's camelCase envelope, Zed's `trial_blocked` message rewrite, Gemini CLI's `retryDelay` hint, Claude's and Zed's authenticated listing.

## What stays open

P08 does not make any provider "work". It makes the evidence visible: every `G` cell above is a statement of what is not proven, and every `T` or `S` cell is a statement that only the shared path was exercised. The `C` and `U` rows are providers this build refuses; nothing here argues they should be served.
