# Provider acceptance matrix (#230)

Status: **source-evidence matrix**. It is not a live, credential, streaming or
production claim. "Ready" in the catalog is build wiring only (PARITY §6.15,
PROVIDER_BASELINE §3); a supervised live canary needs separate owner consent
and is never implied by a row here.

This file is generated from `internal/parity/provider_acceptance_test.go`. `TestProviderAcceptanceMatrixIsCurrent` compares it byte for byte and fails if a provider is missing, a class is unknown, a census count drifts, a named test does not exist, or a residual is empty. Regenerate with `ACCEPTANCE_WRITE=1 go test ./internal/parity -run TestProviderAcceptanceMatrixIsCurrent`.

Read with `docs/P08_TEST_MATRIX.md`: P08 is the per-behaviour evidence (listing, quota, unavailability, import, streaming, non-streaming, errors, cached tokens); this file is the per-provider onboarding/quota acceptance and the residual owner.

The audit's historic 26-gap count was a snapshot of `3e3facd`, not a current test result; the current per-behaviour counts are P08's "Honest totals", and every gap here names its owner. This change delivers the matrix and the shared O01/O02 acceptance; provider-family onboarding stays with the residual tickets (#76, #225, #228, #231), not with a broad rewrite in this PR.

## Census

- **81 active** providers; **3 hidden** remain absent: trae, devin-cli, windsurf (PROVIDER_BASELINE §3, "hidden" rows).
- **52** shared API-key (O01), **3** no-auth (O02), **26** providers with a non-shared auth method (O03-O06) in **21** families.
- **24** dedicated quota readers (Q01), **57** providers with no dedicated quota surface (Q02; reported as *not reported*, never zero/unlimited).
- **5** refused by immutable readiness: `typesafe`, `cursor`, `qoder`, `grok-web`, `perplexity-web`. Storing a credential never makes them routable or a Ready claim.

## Classes

| Class | Meaning | Oracle |
|---|---|---|
| O01 | Shared API-key form: create/edit/revoke, test, discovery | `src/shared/constants/providers.js:67`, `src/app/api/providers/route.js:1-60`, `[id]/route.js:63,87,175` |
| O02 | No-auth/free connection: no secret is required or invented | `src/shared/constants/providers.js:46` |
| O03 | Browser OAuth with a pasted callback | `src/app/api/oauth/[provider]/[action]/route.js:88-139,317-490` |
| O04 | Device-code approval and poll | `src/app/api/oauth/[provider]/[action]/route.js:238-292,492-540` |
| O05 | Cookie/session credential | `src/shared/constants/providers.js:70` |
| O06 | Specialized token/PAT/project import (incl. bulk and auto-import) | `src/app/api/oauth/codex/import-token/route.js`, `src/app/api/oauth/grok-cli/bulk-import/route.js`, `src/app/api/oauth/cursor/auto-import/route.js` |
| Q01 | Dedicated quota reader: a pinned response parser | `internal/providers/usage`, PARITY §6.28-6.31 |
| Q02 | No dedicated quota surface | P08 quota cell `N` and the pin's registry entry |

The O class is the **acceptance workflow**, not a claim that the provider is
routable. Providers refused by immutable readiness are named in the census; a
stored credential never makes one routable, and no row here claims it serves.

## Acceptance methods → fixture/evidence

| Method | Evidence | Fixture / cleanup |
|---|---|---|
| Create, edit, revoke | `control/api:TestSharedAPIKeyProviderAcceptance` | in-process admin mux, per-test temp SQLite |
| Wrong key (401), 429, timeout | `control/api:TestSharedAPIKeyProviderAcceptance` (502, class named, credential never echoed) | injected `probeHTTPFunc`; no network |
| Provider-native test and discovery | P08 listing/stream/non-stream cells per row | sanitized fixtures in `internal/ingress`, `internal/providers/wire` |
| No-secret create and discovery | `control/api:TestNoSecretProviderAcceptance` | injected upstream asserts no Authorization/key header |
| Passthrough and cached IDs survive a failed discovery | `control/api:TestDiscoveryFailureKeepsTheSavedPassthroughModels` | temp SQLite; 503 fixture then read-back |
| Quota read / no dedicated surface | P08 quota cells; reader tests under `internal/providers/usage` | pinned sanitized responses |
| Special-auth onboarding | P08 import cells; residual `#231` for workflows still short | sealed/sanitized fixtures only |

Every Go fixture is an in-process `httptest`/`probeHTTPFunc` with a `t.TempDir`
database, so it is removed even when a test fails. Browser smokes are checked by
`ui/scripts/smoke-cleanup-check.sh`; a new smoke must be added there in the
same change.

## Matrix

| Provider | O | Family | Q | Lifecycle & errors | Discovery | Quota | Import | Residual |
|---|---|:-:|:-:|---|---|---|---|---|
| `alicode-intl` | O01 | alicode-intl | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | `parity:TestEveryPinnedPlainListingURLMatches`, `providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs` | N (pin) | N (pin) | #76 |
| `alitp-intl` | O01 | alitp-intl | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | N (pin) | N (pin) | N (pin) | #76 |
| `alicode` | O01 | alicode | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | `parity:TestEveryPinnedPlainListingURLMatches`, `providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs` | N (pin) | N (pin) | #76 |
| `alims-intl` | O01 | alims-intl | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | `parity:TestEveryPinnedPlainListingURLMatches`, `providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs` | N (pin) | N (pin) | #76 |
| `api-airforce` | O01 | api-airforce | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | `control/api:TestSuggestionFiltersPinnedFixtures`, `control/api:TestDiscoveryFailureKeepsTheSavedPassthroughModels` | N (pin) | N (pin) | #76 |
| `baidu` | O01 | baidu | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | N (pin) | N (pin) | N (pin) | #76 |
| `bazaarlink` | O01 | bazaarlink | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | N (pin) | N (pin) | N (pin) | #76 |
| `blackbox` | O01 | blackbox | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | N (pin) | N (pin) | N (pin) | #76 |
| `bluesminds` | O01 | bluesminds | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | N (pin) | N (pin) | N (pin) | #76 |
| `byteplus` | O01 | byteplus | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | `parity:TestEveryPinnedPlainListingURLMatches`, `providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs` | N (pin) | N (pin) | #76 |
| `cerebras` | O01 | cerebras | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | `parity:TestEveryPinnedPlainListingURLMatches`, `providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs` | N (pin) | N (pin) | #76 |
| `chutes` | O01 | chutes | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | `parity:TestEveryPinnedPlainListingURLMatches`, `providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs` | N (pin) | N (pin) | #76 |
| `cohere` | O01 | cohere | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | `parity:TestEveryPinnedPlainListingURLMatches`, `providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs` | N (pin) | N (pin) | #76 |
| `featherless` | O01 | featherless | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | N (pin) | N (pin) | N (pin) | #76 |
| `fireworks` | O01 | fireworks | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | `parity:TestEveryPinnedPlainListingURLMatches`, `providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs` | N (pin) | N (pin) | #76 |
| `groq` | O01 | groq | Q01 | `control/api:TestSharedAPIKeyProviderAcceptance` | `parity:TestEveryPinnedPlainListingURLMatches`, `providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs` | `providers/usage:TestGroqPinnedHeadersBuildRequestAndTokenWindows`, `providers/usage:TestGroqAbsentHeadersAreNotReported`, `providers/usage:TestGroqMalformedHeadersNeverInventQuota`, `providers/usage:TestGroqUpstreamErrorsAreExplicit` | N (pin) | #76, #225 |
| `hyperbolic` | O01 | hyperbolic | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | `parity:TestEveryPinnedPlainListingURLMatches`, `providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs` | N (pin) | N (pin) | #76 |
| `kilo-gateway` | O01 | kilo-gateway | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | N (pin) | N (pin) | N (pin) | #76 |
| `llm7` | O01 | llm7 | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | N (pin), `control/api:TestDiscoveryFailureKeepsTheSavedPassthroughModels` | N (pin) | N (pin) | #76 |
| `mistral` | O01 | mistral | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | `parity:TestEveryPinnedPlainListingURLMatches`, `providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs` | N (pin) | N (pin) | #76 |
| `morph` | O01 | morph | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | N (pin) | N (pin) | N (pin) | #76 |
| `nebius` | O01 | nebius | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | `parity:TestEveryPinnedPlainListingURLMatches`, `providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs` | N (pin) | N (pin) | #76 |
| `nvidia` | O01 | nvidia | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | `parity:TestEveryPinnedPlainListingURLMatches`, `providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs` | N (pin) | N (pin) | #76 |
| `openrouter` | O01 | openrouter | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | `control/api:TestSuggestionFiltersPinnedFixtures`, `parity:TestEveryPinnedPlainListingURLMatches`, `control/api:TestDiscoveryFailureKeepsTheSavedPassthroughModels` | N (pin) | N (pin) | #76 |
| `perplexity` | O01 | perplexity | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | `providers/wire:TestListingRequestsMatchThePin`, `control/api:TestPerplexityListsUnderV1` | N (pin) | N (pin) | #76 |
| `poolside` | O01 | poolside | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | N (pin) | N (pin) | N (pin) | #76 |
| `sambanova` | O01 | sambanova | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | N (pin) | N (pin) | N (pin) | #76 |
| `siliconflow` | O01 | siliconflow | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | `parity:TestEveryPinnedPlainListingURLMatches`, `providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs` | N (pin) | N (pin) | #76 |
| `tencent` | O01 | tencent | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | N (pin) | N (pin) | N (pin) | #76 |
| `together` | O01 | together | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | `parity:TestEveryPinnedPlainListingURLMatches`, `providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs` | N (pin) | N (pin) | #76 |
| `venice` | O01 | venice | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | `providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs`, `control/api:TestDiscoveryFailureKeepsTheSavedPassthroughModels` | N (pin) | N (pin) | #76 |
| `vercel-ai-gateway` | O01 | vercel-ai-gateway | Q01 | `control/api:TestSharedAPIKeyProviderAcceptance` | `parity:TestEveryPinnedPlainListingURLMatches`, `providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs`, `control/api:TestDiscoveryFailureKeepsTheSavedPassthroughModels` | `providers/usage:TestVercelBalanceIsNotAUsagePercentage` | N (pin) | #76, #225 |
| `volcengine-ark` | O01 | volcengine-ark | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | `parity:TestEveryPinnedPlainListingURLMatches`, `providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs` | N (pin) | N (pin) | #76 |
| `openai` | O01 | openai | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | `parity:TestEveryPinnedPlainListingURLMatches`, `providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs` | N (pin) | N (pin) | #76 |
| `codex` | O06 | codex | Q01 | gap (`#231`) | `control/api:TestCodexListingSendsTheGateAndReadsSlugs`, `providers/wire:TestParseCodexListing` | `providers/codex:TestUsageNormalizesWindowsAndResetCredits`, `providers/codex:TestUsageOmittedResetCreditsAreUnknown` | `providers/onboarding:TestCodexCapturesAccountID`, `providers/onboarding:TestCodexBulkExportedAccountDataWinsOverTheJWT`, `providers/onboarding:TestBulkImportKeepsAnUnknownExpiryUnknown`, `providers/onboarding:TestRefreshKeepsRotatedRefreshToken` | #76, #225, #231 |
| `anthropic` | O01 | anthropic | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | `providers/discovery:TestNativeDiscoveryAuthStyles` | N (pin) | N (pin) | #76 |
| `gemini` | O01 | gemini | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | `providers/discovery:TestModelsSupportsApiKeyHeaderAndGeminiShape` | N (pin) | N (pin) | #76 |
| `claude` | O03 | claude | Q01 | gap (`#231`) | G: the pin lists api.anthropic.com/v1/models with x-api-key (route.js:132-141) | `providers/claude:TestOAuthUsageParsesWindowsAndCaches`, `providers/claude:TestOAuthUsage429CoolsDownThenFallsBackToLegacy` | `providers/onboarding:TestClaudePasteCodeState`, `providers/onboarding:TestRefreshCoversReferenceTable`, `providers/oauthrefresh:TestRotatedCredentialIsDurableAndKeepsProviderData` | #76, #225, #231 |
| `deepseek` | O01 | deepseek | Q01 | `control/api:TestSharedAPIKeyProviderAcceptance` | `parity:TestEveryPinnedPlainListingURLMatches`, `providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs` | `providers/usage:TestDeepSeekPinnedBalanceKeepsCurrenciesApart`, `providers/usage:TestDeepSeekBothZeroIsZeroNotUnlimited`, `providers/usage:TestDeepSeekUnobservedAmountsStayDistinct`, `providers/usage:TestDeepSeekUpstreamFailuresAreErrors` | N (pin) | #76, #225 |
| `glm` | O01 | glm | Q01 | `control/api:TestSharedAPIKeyProviderAcceptance` | N (pin) | `providers/usage:TestGLMReportsBothLimitTypes`, `providers/usage:TestGLMRejectsExpiredKey`, `providers/usage:TestRegionalReadersUseTheirOwnHost` | N (pin) | #76, #225 |
| `glm-cn` | O01 | glm-cn | Q01 | `control/api:TestSharedAPIKeyProviderAcceptance` | N (pin) | `providers/usage:TestRegionalReadersUseTheirOwnHost` | N (pin) | #76, #225 |
| `minimax` | O01 | minimax | Q01 | `control/api:TestSharedAPIKeyProviderAcceptance` | N (pin) | `providers/usage:TestMiniMaxUsageEndpointsRegionsAndCountSemantics` | N (pin) | #76, #225 |
| `minimax-cn` | O01 | minimax-cn | Q01 | `control/api:TestSharedAPIKeyProviderAcceptance` | N (pin) | `providers/usage:TestMiniMaxUsageEndpointsRegionsAndCountSemantics` | N (pin) | #76, #225 |
| `xiaomi-tokenplan` | O01 | xiaomi-tokenplan | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | N (pin) | N (pin) | N (pin) | #76 |
| `mimo-free` | O02 | mimo-free | Q02 | `control/api:TestNoSecretProviderAcceptance` | `control/api:TestSuggestionFiltersPinnedFixtures`, `control/api:TestDiscoveryFailureKeepsTheSavedPassthroughModels` | N (pin) | N (pin) | #76 |
| `mmf` | O02 | mmf | Q02 | `control/api:TestNoSecretProviderAcceptance` | N (pin) | N (pin) | N (pin) | #76 |
| `opencode` | O02 | opencode | Q02 | `control/api:TestNoSecretProviderAcceptance` | `control/api:TestSuggestionFiltersPinnedFixtures`, `control/api:TestDiscoveryFailureKeepsTheSavedPassthroughModels` | N (pin) | N (pin) | #76 |
| `ollama` | O01 | ollama | Q01 | `control/api:TestSharedAPIKeyProviderAcceptance` | `control/api:TestWorkflowNativeRoutesDiscoveryAndProbe`, `parity:TestEveryPinnedPlainListingURLMatches` | `providers/usage:TestOllamaUsageRatioIsUsedFractionAndNullWindowsAreSkipped` | N (pin) | #76, #225 |
| `ollama-local` | O06 | ollama-local | Q02 | gap (`#231`) | G: the pin resolves the local host and GETs /api/tags (route.js:491-507) | N (pin) | N (pin) | #76, #231, #228 |
| `typesafe` | O01 | typesafe | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | N (pin) | N (pin) | N (pin) | #76 |
| `vertex-partner` | O06 | vertex-partner | Q02 | gap (`#231`) | N (pin) | N (pin) | N (pin) | #76, #231, #228 |
| `tokenrouter` | O01 | tokenrouter | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | `providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs`, `control/api:TestDiscoveryFailureKeepsTheSavedPassthroughModels` | N (pin) | N (pin) | #76 |
| `perplexity-agent` | O01 | perplexity-agent | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | `control/api:TestPerplexityListsUnderV1`, `parity:TestEveryPinnedPlainListingURLMatches`, `control/api:TestDiscoveryFailureKeepsTheSavedPassthroughModels` | N (pin) | N (pin) | #76 |
| `opencode-go` | O01 | opencode-go | Q01 | `control/api:TestSharedAPIKeyProviderAcceptance` | N (pin) | `providers/usage:TestOpenCodeGoUsageUsedPercentAndAbsentPeriods`, `providers/usage:TestOpenCodeGoUsageFailuresAreErrorsNotHealthyAccounts` | N (pin) | #76, #225 |
| `cloudflare-ai` | O01 | cloudflare-ai | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | N (pin) | N (pin) | N (pin) | #76, #228 |
| `muse` | O01 | muse | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | N (pin) | N (pin) | N (pin) | #76 |
| `xai` | O03 | xai | Q02 | gap (`#231`) | `parity:TestEveryPinnedPlainListingURLMatches`, `providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs` | N (pin) | `providers/onboarding:TestRefreshCoversReferenceTable`, `providers/onboarding:TestRedirectURIsMatchRegisteredValues` | #76, #231 |
| `github` | O04 | github | Q01 | gap (`#231`) | `control/api:TestGitHubListingDropsEmbeddingsAndDisabledModels`, `providers/wire:TestParseGitHubListing` | `providers/usage:TestGitHubPaidAndFreeShapes` | `providers/onboarding:TestGitHubCapturesCopilotToken`, `providers/onboarding:TestGitHubRefreshRemintsCopilotToken`, `providers/onboarding:TestGitHubMissingExpiryBoundsSequentialMints` | #76, #225, #231 |
| `gitlab` | O06 | gitlab | Q02 | gap (`#231`) | N (pin) | N (pin) | `providers/onboarding:TestGitlabPATRequiresVerification` | #76, #231, #228 |
| `iflow` | O06 | iflow | Q02 | gap (`#231`) | N (pin) | N (pin) | `providers/onboarding:TestIflowRequiresAPIKey`, `providers/onboarding:TestIflowCookieImportKeepsTheKeyExpiry`, `providers/oauthrefresh:TestResolverIssuesARealRefreshRequest` | #76, #231 |
| `kimi` | O04 | kimi | Q01 | gap (`#231`) | N (pin) | `providers/usage:TestKimiUsesAPIKeyHeader`, `providers/usage:TestKimiOAuthSendsDeviceHeaders`, `providers/usage:TestKimiEntitlementIsNotExpiry` | `providers/onboarding:TestRefreshCoversReferenceTable`, `providers/onboarding:TestFlowTypesMatchReference` | #76, #225, #231 |
| `xiaomi-mimo` | O03 | xiaomi-mimo | Q01 | gap (`#231`) | N (pin) | `providers/usage:TestXiaomiMimoUsagePercentIsRemaining` | `providers/onboarding:TestXiaomiCallbackDecrypts`, `providers/onboarding:TestXiaomiImportHoldsSealedAccountFields`, `providers/onboarding:TestMimoImportSealedAndAbsentFromReadModelsAndLogs` | #76, #225, #231, #228 |
| `cline` | O03 | cline | Q02 | gap (`#231`) | `control/api:TestClineListingCarriesTheWorkOSToken`, `providers/wire:TestParseClineListings` | N (pin) | `providers/onboarding:TestClineDecodesTokenBundle`, `providers/onboarding:TestRefreshCoversReferenceTable` | #76, #231 |
| `clinepass` | O06 | cline | Q02 | gap (`#231`) | `control/api:TestClineListingCarriesTheWorkOSToken`, `providers/wire:TestParseClineListings` | N (pin) | `providers/onboarding:TestClineDecodesTokenBundle`, `providers/onboarding:TestRefreshCoversReferenceTable` | #76, #231 |
| `kilocode` | O04 | kilocode | Q02 | gap (`#231`) | `control/api:TestSuggestionFiltersPinnedFixtures`, `control/api:TestKiloCacheFreshExpiredFailureAndConcurrency`, `control/api:TestDiscoveryFailureKeepsTheSavedPassthroughModels` | N (pin) | `control/api:TestOAuthDeviceApprovalStoresNonExpiringBearerOnce` | #76, #231 |
| `codebuddy-cn` | O04 | codebuddy | Q01 | gap (`#231`) | N (pin) | `providers/usage:TestCodeBuddyUsageUsesRouteweftIdentity`, `providers/usage:TestRegionalReadersUseTheirOwnHost`, `ingress:TestCodeBuddyCNUsageFixtureReplays` | `providers/onboarding:TestCodeBuddyPendingCodeIsNotFailure`, `providers/onboarding:TestCodeBuddyUsesRouteweftIdentityThroughoutHandshake`, `providers/onboarding:TestRefreshCoversReferenceTable` | #76, #225, #231 |
| `codebuddy-intl` | O04 | codebuddy | Q01 | gap (`#231`) | N (pin) | `providers/usage:TestRegionalReadersUseTheirOwnHost`, `providers/wire:TestCodeBuddyIntlMessages` | `providers/onboarding:TestCodeBuddyPendingCodeIsNotFailure`, `providers/onboarding:TestCodeBuddyUsesRouteweftIdentityThroughoutHandshake`, `providers/onboarding:TestRefreshCoversReferenceTable` | #76, #225, #231 |
| `antigravity` | O03 | google | Q01 | gap (`#231`) | `control/api:TestGoogleInternalListingsArePosts`, `providers/wire:TestParseGoogleModelsListing` | `providers/usage:TestAntigravityFreeTierSkipsPerModelQuota` | `providers/onboarding:TestGoogleClientComesFromTheEnvironment`, `providers/onboarding:TestGoogleFlowsRefuseToStartWithoutClient`, `providers/onboarding:TestRefreshCoversReferenceTable` | #76, #225, #231 |
| `gemini-cli` | O03 | google | Q01 | gap (`#231`) | `control/api:TestGoogleInternalListingsArePosts`, `providers/wire:TestParseGoogleModelsListing` | `providers/usage:TestGeminiUsesStoredProjectID` | `providers/onboarding:TestGoogleClientComesFromTheEnvironment`, `providers/onboarding:TestGoogleFlowsRefuseToStartWithoutClient`, `providers/onboarding:TestRefreshCoversReferenceTable` | #76, #225, #231 |
| `cursor` | O06 | cursor | Q02 | gap (`#231`) | `control/api:TestCursorListingIsAUnaryProtoPost`, `providers/wire:TestParseCursorListing` | N (pin) | `providers/onboarding:TestCursorImportValidatesShapeAndCallsNothing` | #76, #231 |
| `qoder` | O04 | qoder | Q01 | gap (`#231`) | `control/api:TestQoderListingIsSignedAndReadsTheChatCatalog`, `providers/wire:TestParseQoderListing`, `providers/wire:TestQoderPersonalTokenListingIsAnExplicitGap` | `providers/usage:TestQoderUsageUserAndOrganizationPools`, `providers/usage:TestQoderQuotaIsThePins` | `providers/onboarding:TestFlowTypesMatchReference`, `providers/onboarding:TestEveryOracleFlowIsImplemented`, `providers/wire:TestQoderPATExchangeMatchesPin` | #76, #225, #231 |
| `kiro` | O06 | kiro | Q01 | gap (`#231`) | `control/api:TestKiroListingIsListAvailableModels`, `control/api:TestKiroListingRefusesAHostInTheRegion`, `providers/wire:TestParseKiroListing` | `providers/usage:TestKiroAPIKeySendsTokenType`, `providers/usage:TestKiroFreeTrialIsSeparateWindow`, `providers/usage:TestKiroQuotaIsThePins` | `providers/onboarding:TestKiroDeviceFlowRegistersClient`, `providers/onboarding:TestKiroTokenImportProvesTheTokenByRefreshing`, `providers/onboarding:TestKiroCLIProxyImportNormalizesWithoutANetworkCall`, `providers/onboarding:TestKiroRefreshPicksTheEndpointFromTheCredentialShape`, `providers/onboarding:TestKiroAPIKeyIsProvenAgainstTheCatalog` | #76, #225, #231 |
| `vertex` | O06 | google | Q02 | gap (`#231`) | N (pin) | N (pin) | G: the service account or ADC JSON is typed in as the connection key and used at serving (TestVertexMintsAndCachesAServiceAccountToken) | #76, #231, #228 |
| `commandcode` | O01 | commandcode | Q01 | `control/api:TestSharedAPIKeyProviderAcceptance` | N (pin) | `providers/usage:TestCommandCodeUsageCarriesOrgAndPlanCap`, `providers/usage:TestCommandCodeUsageAuthAndBillingFailuresAreErrors` | N (pin) | #76, #225 |
| `grok-cli` | O04 | grok | Q01 | gap (`#231`) | `control/api:TestGrokCLIListingUsesTheGrokShellIdentity`, `providers/wire:TestParseGrokCLIListing` | `providers/usage:TestGrokPinnedRESTCredits`, `providers/usage:TestGrokRESTPaidFreeAndMonthlyShapes`, `providers/usage:TestGrokCLIQuotaIsThePins` | `providers/onboarding:TestBulkImportKeepsAnUnknownExpiryUnknown`, `providers/onboarding:TestRefreshCoversReferenceTable` | #76, #225, #231 |
| `grok-web` | O05 | grok | Q02 | gap (`#231`) | N (pin), `control/api:TestDiscoveryFailureKeepsTheSavedPassthroughModels` | N (pin) | G: no import flow, as at the pin (a cookie is entered as the connection credential) | #76, #231 |
| `perplexity-web` | O05 | perplexity-web | Q02 | gap (`#231`) | N (pin) | N (pin) | G: no import flow, as at the pin (a cookie is entered as the connection credential) | #76, #231 |
| `kenari` | O01 | kenari | Q01 | `control/api:TestSharedAPIKeyProviderAcceptance` | `control/api:TestSuggestionFiltersPinnedFixtures`, `control/api:TestDiscoveryFailureKeepsTheSavedPassthroughModels` | `providers/usage:TestKenariPinnedZeroCapacity`, `providers/usage:TestKenariAbsentAmountsAreZeroLikeThePin` | N (pin) | #76, #225 |
| `zed` | O03 | zed | Q01 | gap (`#231`) | `control/api:TestZedListingExchangesItsAccountCredential`, `providers/wire:TestZedListingExchangesTheAccountCredential`, `providers/wire:TestZedListingRefreshesTheLLMTokenOnce`, `control/api:TestListingRefreshesOAuthOnceAndPersistsTheRotation`, `control/api:TestDiscoveryFailureKeepsTheSavedPassthroughModels` | `providers/usage:TestZedUsageLimitedUnlimitedAndExhausted`, `providers/usage:TestZedQuotaIsThePins` | `providers/onboarding:TestZedCallbackDecrypts` | #76, #225, #231 |
| `kimchi` | O06 | kimchi | Q02 | gap (`#231`) | `control/api:TestKimchiListingUsesTheMetadataCatalogWithItsFilter`, `providers/wire:TestParseKimchiListing`, `control/api:TestDiscoveryFailureKeepsTheSavedPassthroughModels` | N (pin) | `providers/onboarding:TestKimchiRejectsInvalidToken` | #76, #231, #228 |
| `azure` | O01 | azure | Q02 | `control/api:TestSharedAPIKeyProviderAcceptance` | N (pin) | N (pin) | N (pin) | #76, #228 |
