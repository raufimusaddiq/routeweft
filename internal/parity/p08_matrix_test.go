package parity

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
)

// The P08 test matrix (#76): one row per active provider, one cell per
// behaviour, each cell either evidence or an honest gap. docs/P08_TEST_MATRIX.md
// is generated from this table and compared byte for byte, so a claim cannot
// drift from the evidence behind it, and every named test must exist.
//
//	P  a test that exercises a provider-specific branch, fixture or wire
//	C  a codec-level test only; the provider is unavailable, so nothing reaches it from dispatch
//	T  this provider is one row of a table over the shared path (it checks its own
//	   URL/credential/body, but a bug in the shared path would fail every row alike)
//	S  the shared path only; nothing in the tests names this provider
//	N  not applicable, with the pin's reason and file:line
//	U  the provider is refused by its readiness (unavailable); the refusal is asserted
//	G  not covered; the note says what is missing and where it is tracked
type kind string

const (
	kindP kind = "P"
	kindC kind = "C"
	kindT kind = "T"
	kindS kind = "S"
	kindN kind = "N"
	kindU kind = "U"
	kindG kind = "G"
)

type cell struct {
	Kind kind
	Refs []string // "dir:TestName", dir relative to internal/
	Note string
}

const (
	colList = iota
	colQuota
	colUnavailable
	colImport
	colStream
	colNonStream
	colErrors
	colTokens
	columns
)

var columnTitle = [columns]string{
	"Authenticated listing", "Quota / usage read", "Applicable unavailability", "Import / refresh",
	"Streaming", "Non-streaming", "Error shapes", "Cached vs ordinary tokens",
}

var columnShort = [columns]string{"List", "Quota", "Unavail", "Import", "Stream", "NonStr", "Errors", "Tokens"}

// pinRegistryLines is the length of each provider's pinned registry file
// (open-sse/providers/registry/<id>.js at 1c3b9f47), the "1-N" of a not-applicable citation.
var pinRegistryLines = map[string]int{
	"alicode-intl":      30,
	"alicode":           31,
	"anthropic":         34,
	"antigravity":       91,
	"azure":             21,
	"blackbox":          41,
	"byteplus":          35,
	"cerebras":          32,
	"chutes":            24,
	"claude":            84,
	"cline":             55,
	"clinepass":         62,
	"cloudflare-ai":     52,
	"codebuddy-cn":      86,
	"codex":             101,
	"cohere":            25,
	"commandcode":       47,
	"cursor":            58,
	"deepseek":          73,
	"featherless":       34,
	"fireworks":         27,
	"gemini-cli":        58,
	"gemini":            51,
	"github":            88,
	"gitlab":            32,
	"glm-cn":            39,
	"glm":               62,
	"grok-cli":          96,
	"grok-web":          39,
	"groq":              38,
	"hyperbolic":        35,
	"iflow":             52,
	"kilocode":          51,
	"kimchi":            49,
	"kimi":              93,
	"kiro":              130,
	"mimo-free":         27,
	"minimax-cn":        68,
	"minimax":           71,
	"mistral":           29,
	"mmf":               19,
	"nebius":            25,
	"nvidia":            34,
	"ollama-local":      19,
	"ollama":            40,
	"openai":            64,
	"opencode-go":       80,
	"opencode":          41,
	"openrouter":        35,
	"perplexity-web":    33,
	"perplexity":        32,
	"perplexity-agent":  44,
	"qoder":             61,
	"siliconflow":       39,
	"together":          28,
	"venice":            42,
	"vercel-ai-gateway": 38,
	"vertex-partner":    29,
	"vertex":            33,
	"volcengine-ark":    35,
	"xai":               43,
	"xiaomi-mimo":       77,
	"xiaomi-tokenplan":  63,
	"alims-intl":        32,
	"codebuddy-intl":    79,
	"zed":               70,
	"api-airforce":      39,
	"baidu":             33,
	"bazaarlink":        47,
	"bluesminds":        38,
	"kilo-gateway":      34,
	"llm7":              35,
	"sambanova":         27,
	"tencent":           27,
	"morph":             29,
	"poolside":          31,
	"tokenrouter":       56,
	"alitp-intl":        35,
	"kenari":            133,
	"typesafe":          29,
	"muse":              25,
}

func pinFile(id string) string {
	return fmt.Sprintf("open-sse/providers/registry/%s.js:1-%d", id, pinRegistryLines[id])
}

func ref(refs ...string) []string { return refs }

const (
	genericErrors = "providers/shared:TestParseErrorNormalizesProviderErrBodies"
	cooldownTest  = "ingress:TestRecordUpstreamFailureMapsProviderErrToCooldown"
	readinessTest = "ingress:TestReadinessMatchesIngressReachability"
	gapGuard      = "ingress:TestP08ErrorClassificationClosedGaps"
)

// listingCells: route.js is src/app/api/providers/[id]/models/route.js at the pin.
func listingCell(spec registry.Spec) cell {
	id := spec.ID
	own := map[string]cell{
		"perplexity":       {kindP, ref("providers/wire:TestListingRequestsMatchThePin", "control/api:TestPerplexityListsUnderV1"), "listed at /v1/models (route.js:247)"},
		"perplexity-agent": {kindP, ref("control/api:TestPerplexityListsUnderV1", "parity:TestEveryPinnedPlainListingURLMatches"), "route.js:117"},
		"codex":            {kindP, ref("control/api:TestCodexListingSendsTheGateAndReadsSlugs", "providers/wire:TestParseCodexListing"), "client_version gate, slug-keyed entries (route.js:149-164)"},
		"gemini-cli":       {kindP, ref("control/api:TestGoogleInternalListingsArePosts", "providers/wire:TestParseGoogleModelsListing"), "POST fetchAvailableModels (route.js:440-460)"},
		"antigravity":      {kindP, ref("control/api:TestGoogleInternalListingsArePosts", "providers/wire:TestParseGoogleModelsListing"), "POST models; response shape is a fixture, the pin returns it untouched (route.js:165-173)"},
		"github":           {kindP, ref("control/api:TestGitHubListingDropsEmbeddingsAndDisabledModels", "providers/wire:TestParseGitHubListing"), "chat models only (route.js:174-200)"},
		"kimchi":           {kindP, ref("control/api:TestKimchiListingUsesTheMetadataCatalogWithItsFilter", "providers/wire:TestParseKimchiListing"), "metadata catalog with include_in_cli"},
		"kiro":             {kindP, ref("control/api:TestKiroListingIsListAvailableModels", "control/api:TestKiroListingRefusesAHostInTheRegion", "providers/wire:TestParseKiroListing"), "ListAvailableModels, region from the ARN"},
		"cline":            {kindP, ref("control/api:TestClineListingCarriesTheWorkOSToken", "providers/wire:TestParseClineListings"), "workos: token prefix"},
		"clinepass":        {kindP, ref("control/api:TestClineListingCarriesTheWorkOSToken", "providers/wire:TestParseClineListings"), "workos: prefix, cline-pass/ ids only"},
		"grok-cli":         {kindP, ref("control/api:TestGrokCLIListingUsesTheGrokShellIdentity", "providers/wire:TestParseGrokCLIListing"), "grok-shell identity headers"},
		"qoder":            {kindP, ref("control/api:TestQoderListingIsSignedAndReadsTheChatCatalog", "providers/wire:TestParseQoderListing", "providers/wire:TestQoderPersonalTokenListingIsAnExplicitGap"), "signed GET; personal access tokens are an explicit gap (PARITY 6.15)"},
		"cursor":           {kindP, ref("control/api:TestCursorListingIsAUnaryProtoPost", "providers/wire:TestParseCursorListing"), "unary proto POST, synthetic proto"},
		"gemini":           {kindP, ref("providers/discovery:TestModelsSupportsApiKeyHeaderAndGeminiShape"), "x-goog-api-key and the models[] shape (route.js:142-148); paging is not followed, as in the pin"},
		"anthropic":        {kindP, ref("providers/discovery:TestNativeDiscoveryAuthStyles"), "x-api-key credential style (route.js:203-213)"},
		"ollama":           {kindP, ref("control/api:TestWorkflowNativeRoutesDiscoveryAndProbe", "parity:TestEveryPinnedPlainListingURLMatches"), "GET /api/tags with the key (route.js:125)"},
		"kenari":           {kindP, ref("control/api:TestSuggestionFiltersPinnedFixtures"), "registry modelsFetcher; chat-endpoint filter. The pin has no saved-connection listing for it (route.js:595-601)"},
		"openrouter":       {kindP, ref("control/api:TestSuggestionFiltersPinnedFixtures", "parity:TestEveryPinnedPlainListingURLMatches"), "free-model filter; plain bearer URL checked against route.js:72"},
		"opencode":         {kindP, ref("control/api:TestSuggestionFiltersPinnedFixtures"), "registry modelsFetcher opencode-free filter"},
		"mimo-free":        {kindP, ref("control/api:TestSuggestionFiltersPinnedFixtures"), "registry modelsFetcher mimo-free filter"},
		"api-airforce":     {kindP, ref("control/api:TestSuggestionFiltersPinnedFixtures"), "registry modelsFetcher airforce-free filter"},
		"kilocode":         {kindP, ref("control/api:TestSuggestionFiltersPinnedFixtures", "control/api:TestKiloCacheFreshExpiredFailureAndConcurrency"), "registry modelsFetcher kilo-free filter and cache"},
		"zed":              {kindP, ref("control/api:TestZedListingExchangesItsAccountCredential", "providers/wire:TestZedListingExchangesTheAccountCredential", "providers/wire:TestZedListingRefreshesTheLLMTokenOnce", "control/api:TestListingRefreshesOAuthOnceAndPersistsTheRotation"), "the LLM-token exchange at /client/llm_tokens and the x-ai listing header (route.js:293-326, zedAuth.js:258-324); an OAuth 401 refreshes once and retries (route.js:93-128), PARITY 6.15"},
		"claude":           {kindP, ref("control/api:TestClaudeListingIsThePinsRequest"), "GET api.anthropic.com/v1/models (route.js:131-141): an API key goes as x-api-key with anthropic-version 2023-06-01 and no bearer; an OAuth token stays the registry's bearer (registry/claude.js:48-51), a recorded divergence from the route's blanket x-api-key (PARITY 6.15)"},
		"ollama-local":     {kindP, ref("control/api:TestOllamaLocalListingReadsApiTags"), "GET {host}/api/tags with no credential (route.js:491-507), the host the connection's own base or http://localhost:11434 (config/providers.js:5-10); ids are the tags' names"},
		"azure":            {kindN, nil, "the pin has no listing for Azure (not in PROVIDER_MODELS_CONFIG, route.js:131-507; answered 400 at route.js:595-601); Routeweft's hook is Unlisted and sends nothing"},
		"venice":           {kindS, ref("providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs"), "registry modelsFetcher catalog (registry/venice.js); no per-provider test"},
		"tokenrouter":      {kindS, ref("providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs"), "registry modelsFetcher catalog (registry/tokenrouter.js); no per-provider test"},
	}
	if c, ok := own[id]; ok {
		return c
	}
	plain := map[string]bool{
		"alicode": true, "alicode-intl": true, "alims-intl": true, "byteplus": true, "cerebras": true, "chutes": true, "cohere": true,
		"deepseek": true, "fireworks": true, "groq": true, "hyperbolic": true, "mistral": true, "nebius": true, "nvidia": true,
		"openai": true, "siliconflow": true, "together": true, "vercel-ai-gateway": true, "volcengine-ark": true, "xai": true,
	}
	if plain[id] {
		return cell{kindT, ref("parity:TestEveryPinnedPlainListingURLMatches", "providers/discovery:TestModelsFetchesBearerCatalogAndTrimsIDs"), "bearer GET to the URL the pin lists (route.js:75-82 and the entry for this provider)"}
	}
	return cell{kindN, nil, "the pin has no authenticated listing: no PROVIDER_MODELS_CONFIG entry (route.js:131-507), so the handler answers 400 \"does not support models listing\" (route.js:595-601). Routeweft still requests the shared GET {base}/models; unverified (PARITY 6.15)"}
}

func quotaCell(spec registry.Spec) cell {
	id := spec.ID
	readers := map[string]cell{
		"claude":            {kindP, ref("providers/claude:TestOAuthUsageParsesWindowsAndCaches", "providers/claude:TestOAuthUsage429CoolsDownThenFallsBackToLegacy"), "U/claude.js:20-185; scoped weekly metadata open (P05)"},
		"codex":             {kindP, ref("providers/codex:TestUsageNormalizesWindowsAndResetCredits", "providers/codex:TestUsageOmittedResetCreditsAreUnknown"), "U/codex.js:107-217; rate_limits_by_limit_id aliases open (P05)"},
		"github":            {kindP, ref("providers/usage:TestGitHubPaidAndFreeShapes"), "U/github.js:11-99"},
		"kimi":              {kindP, ref("providers/usage:TestKimiUsesAPIKeyHeader", "providers/usage:TestKimiOAuthSendsDeviceHeaders", "providers/usage:TestKimiEntitlementIsNotExpiry"), "U/kimi.js:109-208"},
		"kiro":              {kindP, ref("providers/usage:TestKiroAPIKeySendsTokenType", "providers/usage:TestKiroFreeTrialIsSeparateWindow", "providers/usage:TestKiroQuotaIsThePins"), "U/kiro.js:12-188, all three hosts and auth modes, every recorded pin answer (PARITY §6.30)"},
		"gemini-cli":        {kindP, ref("providers/usage:TestGeminiUsesStoredProjectID"), "U/google.js:22-104; missing-project discovery open (P05)"},
		"antigravity":       {kindP, ref("providers/usage:TestAntigravityFreeTierSkipsPerModelQuota"), "U/google.js:120-249; the retrieveUserQuotaSummary shape is not qualified (P05)"},
		"grok-cli":          {kindP, ref("providers/usage:TestGrokPinnedRESTCredits", "providers/usage:TestGrokRESTPaidFreeAndMonthlyShapes", "providers/usage:TestGrokCLIQuotaIsThePins"), "U/grok-cli.js:141-424 with the gRPC weekly fallback, every recorded pin answer (PARITY §6.30)"},
		"glm":               {kindP, ref("providers/usage:TestGLMReportsBothLimitTypes", "providers/usage:TestGLMRejectsExpiredKey", "providers/usage:TestRegionalReadersUseTheirOwnHost"), "U/glm.js:18-84"},
		"glm-cn":            {kindP, ref("providers/usage:TestRegionalReadersUseTheirOwnHost"), "U/glm.js:18-84; China host"},
		"minimax":           {kindP, ref("providers/usage:TestMiniMaxUsageEndpointsRegionsAndCountSemantics"), "U/minimax.js:95-226; top-level camelCase envelope and 1004 auth text differ from the pin (P05)"},
		"minimax-cn":        {kindP, ref("providers/usage:TestMiniMaxUsageEndpointsRegionsAndCountSemantics"), "U/minimax.js:95-226; China host"},
		"codebuddy-cn":      {kindP, ref("providers/usage:TestCodeBuddyUsageUsesRouteweftIdentity", "providers/usage:TestRegionalReadersUseTheirOwnHost", "ingress:TestCodeBuddyCNUsageFixtureReplays"), "U/codebuddy-cn.js:46-145"},
		"codebuddy-intl":    {kindP, ref("providers/usage:TestRegionalReadersUseTheirOwnHost", "providers/wire:TestCodeBuddyIntlMessages"), "U/codebuddy-cn.js:46-145; International host"},
		"deepseek":          {kindP, ref("providers/usage:TestDeepSeekPinnedBalanceKeepsCurrenciesApart", "providers/usage:TestDeepSeekBothZeroIsZeroNotUnlimited", "providers/usage:TestDeepSeekUnobservedAmountsStayDistinct", "providers/usage:TestDeepSeekUpstreamFailuresAreErrors"), "U/deepseek.js:11-108. One credit window per currency (#212): zero is zero, never unlimited; missing, malformed and negative amounts and an unreported is_available stay distinct; balances carry no remaining fraction, so they do not change routing"},
		"groq":              {kindP, ref("providers/usage:TestGroqPinnedHeadersBuildRequestAndTokenWindows", "providers/usage:TestGroqAbsentHeadersAreNotReported", "providers/usage:TestGroqMalformedHeadersNeverInventQuota", "providers/usage:TestGroqUpstreamErrorsAreExplicit"), "U/groq.js:44-129; x-ratelimit-* headers on GET /models become Requests and Tokens windows (#211). Absent buckets are an error, not an empty plan; a malformed bucket is unbounded beside a valid one"},
		"kenari":            {kindP, ref("providers/usage:TestKenariPinnedZeroCapacity", "providers/usage:TestKenariAbsentAmountsAreZeroLikeThePin"), "U/kenari.js:9-51"},
		"opencode-go":       {kindP, ref("providers/usage:TestOpenCodeGoUsageUsedPercentAndAbsentPeriods", "providers/usage:TestOpenCodeGoUsageFailuresAreErrorsNotHealthyAccounts"), "U/opencode-go.js:11-100"},
		"commandcode":       {kindP, ref("providers/usage:TestCommandCodeUsageCarriesOrgAndPlanCap", "providers/usage:TestCommandCodeUsageAuthAndBillingFailuresAreErrors"), "U/commandcode.js:60-135; the pin omits an empty window, Routeweft reports it unlimited"},
		"vercel-ai-gateway": {kindP, ref("providers/usage:TestVercelBalanceIsNotAUsagePercentage"), "misc.js:150-200"},
		"ollama":            {kindP, ref("providers/usage:TestOllamaUsageRatioIsUsedFractionAndNullWindowsAreSkipped"), "misc.js:55-140; plan label from /api/me and the free-plan reset are not read"},
		"qoder":             {kindP, ref("providers/usage:TestQoderUsageUserAndOrganizationPools", "providers/usage:TestQoderQuotaIsThePins"), "misc.js:230-290 with the PAT exchange, every recorded pin answer (PARITY §6.30)"},
		"xiaomi-mimo":       {kindP, ref("providers/usage:TestXiaomiMimoUsagePercentIsRemaining"), "U/xiaomi-mimo.js:62-120; only the API-key path, the account-session path is not read"},
		"zed":               {kindP, ref("providers/usage:TestZedUsageLimitedUnlimitedAndExhausted", "providers/usage:TestZedQuotaIsThePins"), "U/zed.js:39-222, every recorded pin answer (PARITY §6.30)"},
	}
	if c, ok := readers[id]; ok {
		return c
	}
	if id == "iflow" {
		return cell{kindN, nil, "open-sse/services/usage/misc.js:18-24 returns the fixed message \"Usage tracked per request\" and makes no request"}
	}
	return cell{kindN, nil, fmt.Sprintf("%s has no quota endpoint or header contract; open-sse/services/usage.js:38-69 has no handler for it and :78-79 returns \"not implemented\" (P05 audit row)", pinFile(id))}
}

func unavailableCell(spec registry.Spec) cell {
	id := spec.ID
	quotaP := map[string]cell{
		"kenari":            {kindP, ref("providers/usage:TestKenariPinnedZeroCapacity", "providers/usage:TestKenariAbsentAmountsAreZeroLikeThePin"), "zero capacity and absent amounts are 0%, exhausted, as in the pin (U/kenari.js:39-44)"},
		"opencode-go":       {kindP, ref("providers/usage:TestOpenCodeGoUsageUsedPercentAndAbsentPeriods", "providers/usage:TestOpenCodeGoUsageFailuresAreErrorsNotHealthyAccounts"), "100% used reads as nothing left; a 403 EntitlementError is a subscription hint (opencode-go.js:54-76)"},
		"ollama":            {kindP, ref("providers/usage:TestOllamaUsageRatioIsUsedFractionAndNullWindowsAreSkipped"), "usage 1.0 reads as nothing left; a null window is skipped"},
		"commandcode":       {kindP, ref("providers/usage:TestCommandCodeUsageCarriesOrgAndPlanCap", "providers/wire:TestParseCommandCodeError"), "cap reached reads as nothing left"},
		"zed":               {kindP, ref("providers/usage:TestZedUsageLimitedUnlimitedAndExhausted", "ingress:TestProviderErrorBodiesCoolTheAccountLikeThePin"), "500/500 reads as nothing left; trial_blocked 403 cools the account and its body is rewritten as the pin does it (zed.js:284-310)"},
		"qoder":             {kindP, ref("providers/usage:TestQoderUsageUserAndOrganizationPools", "providers/usage:TestQoderQuotaIsThePins"), "a spent organization pool reads as nothing left, an empty one is dropped; provider unavailable for serving"},
		"xiaomi-mimo":       {kindP, ref("providers/usage:TestXiaomiMimoUsagePercentIsRemaining"), "a 401 (key cannot read the account quota) is an error, not an empty or full window"},
		"github":            {kindP, ref("providers/usage:TestGitHubPaidAndFreeShapes"), "free-plan limited quotas versus paid entitlements"},
		"kimi":              {kindP, ref("providers/usage:TestKimiEntitlementIsNotExpiry"), "an entitlement date is not a credential expiry"},
		"kiro":              {kindP, ref("providers/usage:TestKiroFreeTrialIsSeparateWindow"), "free trial is its own window"},
		"antigravity":       {kindP, ref("providers/usage:TestAntigravityFreeTierSkipsPerModelQuota"), "free tier has no per-model quota"},
		"vercel-ai-gateway": {kindP, ref("providers/usage:TestVercelBalanceIsNotAUsagePercentage"), "a credit balance is not a usage percentage"},
		"codebuddy-cn":      {kindP, ref("ingress:TestCodeBuddyFrequencyLimitCoolsTheAccountAndRelaysTheGatewayError", "providers/wire:TestCodeBuddyFrequencyLimit", "ingress:TestCodeBuddyOtherUpstreamErrorsAreNotReadAsALimit"), "the gateway's frequency-limit body cools the account until its reset (codebuddy-error.js)"},
		"codebuddy-intl":    {kindP, ref("ingress:TestCodeBuddyFrequencyLimitCoolsTheAccountAndRelaysTheGatewayError", "providers/wire:TestCodeBuddyFrequencyLimit", "ingress:TestCodeBuddyOtherUpstreamErrorsAreNotReadAsALimit"), "same hook as codebuddy-cn"},
		"claude":            {kindP, ref("providers/claude:TestOAuthUsage429CoolsDownThenFallsBackToLegacy", "providers/claude:TestLegacyWithoutOrganizationReportsAdminMessage"), "a 429 on the usage read cools and falls back; no organization reports an admin message"},
		"glm":               {kindP, ref("providers/usage:TestGLMRejectsExpiredKey"), "an expired key is an error, not a healthy account"},
		"minimax":           {kindP, ref("providers/usage:TestMiniMaxUsageEndpointsRegionsAndCountSemantics"), "a base_resp error is an error, not an empty quota"},
		"minimax-cn":        {kindP, ref("providers/usage:TestMiniMaxUsageEndpointsRegionsAndCountSemantics"), "a base_resp error is an error, not an empty quota"},
		"groq":              {kindP, ref("providers/usage:TestGroqZeroRemainingIsExhaustedUntilReset"), "a reported zero remaining holds the account out until the header's reset duration (U/groq.js:44-62), landed in #211"},
		"codex":             {kindP, ref("ingress:TestCodexBodyResetReachesDispatchCooldown", "providers/wire:TestPinnedErrorBodyResets", "providers/codex:TestUsageFailureIsUnavailableNotExhausted"), "usage_limit_reached cools until resets_at / resets_in_seconds (executors/codex.js:388-410), landed in P02"},
		"grok-cli":          {kindP, ref("ingress:TestProviderErrorBodiesCoolTheAccountLikeThePin", "providers/wire:TestGrokCLISpendingLimitFallsBackAndCoolsTheAccountForTwoMinutes"), "402 spending-limit cools the account for two minutes and falls back (executors/grok-cli.js:400-416, errorConfig.js:77), landed in #184"},
		"cursor":            {kindC, ref("providers/wire:TestCursorDoReportsAFailureInBandNotAsSuccess", "providers/wire:TestCursorDoPassesUpstreamRejectionsThrough", "providers/wire:TestCursorChatStreamRejectsAnErrorFrameBeforeContent"), "the resource_exhausted in-band failure (cursor.js:365-374) and the ChatService JSON error frame (:389-411); provider unavailable for serving"},
	}
	if c, ok := quotaP[id]; ok {
		return c
	}
	paymentRequired := map[string]cell{
		"deepseek":   {kindP, ref(gapGuard, "ingress:TestPaymentRequiredAccountFallsBackToTheNextAccount", "routing:TestClassifyStatusPaymentRequiredFallsBackAndCools", "routing:TestClassifyErrorPaymentRequiredBeatsRequestScopedKinds", "providers/usage:TestDeepSeekBothZeroIsZeroNotUnlimited"), "402 Insufficient Balance (its real body wears invalid_request_error, which must not end the chain) cools the account for two minutes and falls back (errorConfig.js:77, accountFallback.js:35-46); the balance endpoint reports is_available:false (#212), though routing does not consume the balance"},
		"openrouter": {kindP, ref(gapGuard, "ingress:TestPaymentRequiredAccountFallsBackToTheNextAccount", "routing:TestClassifyStatusPaymentRequiredFallsBackAndCools"), "402 cools the account for two minutes and falls back (errorConfig.js:77)"},
	}
	if c, ok := paymentRequired[id]; ok {
		return c
	}
	if spec.Readiness.Status == registry.ProviderUnavailable {
		return cell{kindU, ref(readinessTest), "refused by its immutable readiness; no capacity behaviour is reachable"}
	}
	return cell{kindS, ref(cooldownTest, "ingress:TestProviderErrorBodiesCoolTheAccountLikeThePin"), "generic 429/5xx/401 classification; the pin has no provider-specific capacity contract for it"}
}

func importCell(spec registry.Spec) cell {
	id := spec.ID
	refresh := "onboarding:TestRefreshCoversReferenceTable"
	_ = refresh
	cells := map[string]cell{
		"claude":         {kindP, ref("providers/onboarding:TestClaudePasteCodeState", "providers/onboarding:TestRefreshCoversReferenceTable", "providers/oauthrefresh:TestRotatedCredentialIsDurableAndKeepsProviderData"), "paste-code PKCE, rotating refresh"},
		"codex":          {kindP, ref("providers/onboarding:TestCodexCapturesAccountID", "providers/onboarding:TestCodexBulkExportedAccountDataWinsOverTheJWT", "providers/onboarding:TestBulkImportKeepsAnUnknownExpiryUnknown", "providers/onboarding:TestRefreshKeepsRotatedRefreshToken"), "PKCE, account id, bulk import, rotation"},
		"grok-cli":       {kindP, ref("providers/onboarding:TestBulkImportKeepsAnUnknownExpiryUnknown", "providers/onboarding:TestRefreshCoversReferenceTable"), "bulk import (snake and camel case), xAI refresh grant"},
		"xai":            {kindT, ref("providers/onboarding:TestRefreshCoversReferenceTable", "providers/onboarding:TestRedirectURIsMatchRegisteredValues"), "shares the xAI refresh grant with grok-cli; no xai-specific import test"},
		"gemini-cli":     {kindP, ref("providers/onboarding:TestGoogleClientComesFromTheEnvironment", "providers/onboarding:TestGoogleFlowsRefuseToStartWithoutClient", "providers/onboarding:TestRefreshCoversReferenceTable"), "Google client from the environment; refresh grant"},
		"antigravity":    {kindP, ref("providers/onboarding:TestGoogleClientComesFromTheEnvironment", "providers/onboarding:TestGoogleFlowsRefuseToStartWithoutClient", "providers/onboarding:TestRefreshCoversReferenceTable"), "Google client from the environment; refresh grant"},
		"iflow":          {kindP, ref("providers/onboarding:TestIflowRequiresAPIKey", "providers/onboarding:TestIflowCookieImportKeepsTheKeyExpiry", "providers/oauthrefresh:TestResolverIssuesARealRefreshRequest"), "cookie import to the account key; refresh"},
		"github":         {kindP, ref("providers/onboarding:TestGitHubCapturesCopilotToken", "providers/onboarding:TestGitHubRefreshRemintsCopilotToken", "providers/onboarding:TestGitHubMissingExpiryBoundsSequentialMints"), "device flow, Copilot token re-minted without a refresh token"},
		"kimi":           {kindT, ref("providers/onboarding:TestRefreshCoversReferenceTable", "providers/onboarding:TestFlowTypesMatchReference"), "device flow type and refresh grant checked in tables; no kimi-specific import test"},
		"cline":          {kindP, ref("providers/onboarding:TestClineDecodesTokenBundle", "providers/onboarding:TestRefreshCoversReferenceTable"), "token bundle decode, refresh"},
		"clinepass":      {kindP, ref("providers/onboarding:TestClineDecodesTokenBundle", "providers/onboarding:TestRefreshCoversReferenceTable"), "token bundle decode, refresh"},
		"kiro":           {kindP, ref("providers/onboarding:TestKiroDeviceFlowRegistersClient", "providers/onboarding:TestKiroTokenImportProvesTheTokenByRefreshing", "providers/onboarding:TestKiroCLIProxyImportNormalizesWithoutANetworkCall", "providers/onboarding:TestKiroRefreshPicksTheEndpointFromTheCredentialShape", "providers/onboarding:TestKiroAPIKeyIsProvenAgainstTheCatalog"), "device, token, CLI-proxy and API-key import; endpoint by credential shape"},
		"codebuddy-cn":   {kindP, ref("providers/onboarding:TestCodeBuddyPendingCodeIsNotFailure", "providers/onboarding:TestCodeBuddyUsesRouteweftIdentityThroughoutHandshake", "providers/onboarding:TestRefreshCoversReferenceTable"), "device handshake, refresh"},
		"codebuddy-intl": {kindP, ref("providers/onboarding:TestCodeBuddyPendingCodeIsNotFailure", "providers/onboarding:TestCodeBuddyUsesRouteweftIdentityThroughoutHandshake", "providers/onboarding:TestRefreshCoversReferenceTable"), "device handshake, refresh"},
		"cursor":         {kindP, ref("providers/onboarding:TestCursorImportValidatesShapeAndCallsNothing"), "token + machine id import validated without a network call; auto-import is deliberately absent (PARITY row 29)"},
		"qoder":          {kindT, ref("providers/onboarding:TestFlowTypesMatchReference", "providers/onboarding:TestEveryOracleFlowIsImplemented", "providers/wire:TestQoderPATExchangeMatchesPin"), "flow type is checked against the pin; the PAT-to-job-token exchange is ported and checked against the pin's own run (PARITY 6.22) but is not called by discovery or serving (PARITY 6.15)"},
		"zed":            {kindP, ref("providers/onboarding:TestZedCallbackDecrypts"), "RSA callback decrypt (zed.js oauth block); the pin has no refresh (zed.js refreshCredentials returns null), nor does Routeweft"},
		"xiaomi-mimo":    {kindP, ref("providers/onboarding:TestXiaomiCallbackDecrypts", "providers/onboarding:TestXiaomiImportHoldsSealedAccountFields", "providers/onboarding:TestMimoImportSealedAndAbsentFromReadModelsAndLogs"), "callback decrypt, key import; account credentials stored sealed and excluded from read models and logs"},
		"gitlab":         {kindP, ref("providers/onboarding:TestGitlabPATRequiresVerification"), "PAT verified before it is stored"},
		"kilocode":       {kindP, ref("control/api:TestOAuthDeviceApprovalStoresNonExpiringBearerOnce"), "device approval stores a non-expiring bearer; the pin has no refresh grant for it"},
		"kimchi":         {kindP, ref("providers/onboarding:TestKimchiRejectsInvalidToken"), "token validated on import"},
		"grok-web":       {kindN, nil, "the connect-flow registry has no entry for it (src/lib/oauth/providers/index.js:31-54) and the refresh table has no handler for it (open-sse/services/tokenRefresh.js:135-160): a cookie typed in as the connection credential is neither imported nor refreshed. The pin's paste-time check of that cookie (src/app/api/providers/validate/route.js:425) is a credential check, not an import. Sealed storage and non-exposure of the cookie are tested in onboarding:TestWebSessionCookieSealedAndAbsentFromReadModelsLogsAndErrors"},
		"perplexity-web": {kindN, nil, "the connect-flow registry has no entry for it (src/lib/oauth/providers/index.js:31-54) and the refresh table has no handler for it (open-sse/services/tokenRefresh.js:135-160): a cookie typed in as the connection credential is neither imported nor refreshed. The pin's paste-time check of that cookie (src/app/api/providers/validate/route.js:479) is a credential check, not an import. Sealed storage and non-exposure of the cookie are tested in onboarding:TestWebSessionCookieSealedAndAbsentFromReadModelsLogsAndErrors"},
		"vertex":         {kindP, ref("providers/wire:TestVertexMintsAndCachesAServiceAccountToken", "providers/wire:TestVertexRefreshesAnADCToken", "providers/wire:TestVertexSendsNothingWhenTheTokenCannotBeMinted"), "refresh: the service-account JWT is minted and cached with the pin's five-minute margin, an ADC token is refreshed, and nothing is sent when no token can be minted (open-sse/services/tokenRefresh.js:77-133, handlers :158-159). Import: the connect-flow registry has no vertex entry (src/lib/oauth/providers/index.js:31-54), so the service account or ADC JSON is typed in as the connection key; its paste-time check is src/app/api/providers/validate/route.js:393-409"},
		"gemini":         {kindN, nil, fmt.Sprintf("%s: the pin's oauth key is only the header scheme for the OAuth form of the API credential; Routeweft serves the API-key flow (PROVIDER_BASELINE)", pinFile(id))},
	}
	if c, ok := cells[id]; ok {
		return c
	}
	return cell{kindN, nil, fmt.Sprintf("%s declares no oauth block and no refresh handler: the credential is a key typed in, never imported or refreshed (open-sse/services/tokenRefresh/providers.js lists none)", pinFile(id))}
}

// transportsWalkedByTables reads the ingress tests so the matrix cannot claim a
// table row the tests do not have.
func chatWires(t *testing.T) map[string]bool {
	t.Helper()
	source, err := os.ReadFile("../ingress/registry_wire_test.go")
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\t\{"([a-z0-9-]+)", "https://`).FindAllStringSubmatch(string(source), -1) {
		out[m[1]] = true
	}
	if len(out) < 30 {
		t.Fatalf("read only %d Chat wires from registry_wire_test.go", len(out))
	}
	return out
}

func nativeWireSkips(t *testing.T) map[string]bool {
	t.Helper()
	source, err := os.ReadFile("../ingress/p08_wires_test.go")
	if err != nil {
		t.Fatal(err)
	}
	block := regexp.MustCompile(`(?s)var nativeWireSkips = map\[string\]string\{(.*?)\n\}`).FindStringSubmatch(string(source))
	if block == nil {
		t.Fatal("nativeWireSkips not found in p08_wires_test.go")
	}
	out := map[string]bool{}
	for _, m := range regexp.MustCompile(`(?m)^\t"([a-z0-9-]+)":`).FindAllStringSubmatch(block[1], -1) {
		out[m[1]] = true
	}
	return out
}

// plainProviderWires are the providers whose pinned entry is a plain API-key
// wire. TestPinnedPlainProviderWires names each id and proves its own pinned
// wire URL, bearer form, static catalog and models listing; the shared
// transport table (TestRegistryChatWires) stays the T evidence for the seam.
var plainProviderWires = map[string]bool{
	"alicode-intl": true, "alitp-intl": true, "alicode": true, "alims-intl": true, "baidu": true,
	"bazaarlink": true, "blackbox": true, "bluesminds": true, "byteplus": true, "cerebras": true,
	"chutes": true, "cohere": true, "featherless": true, "fireworks": true, "hyperbolic": true,
	"kilo-gateway": true, "llm7": true, "morph": true, "nebius": true, "nvidia": true,
	"poolside": true, "sambanova": true, "siliconflow": true, "tencent": true, "together": true,
	"venice": true, "volcengine-ark": true, "openai": true, "tokenrouter": true, "muse": true, "xai": true,
}

const plainProviderWireTest = "ingress:TestPinnedPlainProviderWires"

// modeCell is the streaming (stream=true) or non-streaming cell.
func modeCell(t *testing.T, spec registry.Spec, stream bool, chat, skips map[string]bool) cell {
	id := spec.ID
	pair := func(s, ns []string, sn, nsn string) cell {
		if stream {
			return cell{kindP, s, sn}
		}
		return cell{kindP, ns, nsn}
	}
	if plainProviderWires[id] {
		mode := "non-streaming"
		if stream {
			mode = "streaming"
		}
		return cell{kindP, ref(plainProviderWireTest), "its own row of a parameterised test: the pin's wire URL, bearer auth and static catalog, plus its models listing where the pin has one, " + mode + " (registry/" + id + ".js)"}
	}
	switch id {
	case "codebuddy-cn", "codebuddy-intl":
		return pair(ref("ingress:TestCodeBuddyStreamingClientKeepsItsStreamMinusEmptyToolCalls", "ingress:TestCodeBuddyForcedStreamCancellationReachesUpstream", "ingress:TestCodeBuddyInbandStreamErrorRelaysParsedMessage"),
			ref("ingress:TestCodeBuddyNonStreamClientsGetOneJSONBodyFromAForcedStream", "ingress:TestCodeBuddyJSONAnswerToANonStreamRequestPassesThrough", "ingress:TestCodeBuddyForcedStreamFailuresAreErrorsNotPartialAnswers"),
			"gateway answers only streams; empty tool_calls stripped, cancellation reaches the upstream", "a non-stream client gets one JSON body folded from the forced stream")
	case "api-airforce":
		return pair(ref("ingress:TestAirforceForcedStreamAcrossClientProtocols"), ref("ingress:TestAirforceForcedStreamAcrossClientProtocols", "ingress:TestAirforceStreamFailureDoesNotReturnPartialJSON"),
			"forced stream on Chat, Responses and Messages clients (registry/api-airforce.js:25-32)", "folded to one JSON body; a failed stream is never returned as partial JSON")
	case "opencode":
		return pair(ref("ingress:TestOpenCodeForcedStreamFoldsToOneJSONBody", "providers/wire:TestOpenCodeForcesStream"), ref("ingress:TestOpenCodeForcedStreamFoldsToOneJSONBody"),
			"forced stream (registry/opencode.js:20), Zen target", "folded to one JSON body")
	case "cline", "clinepass":
		return pair(ref("ingress:TestClineEnvelopeOnlyUnwrapsNonStreamSuccess"), ref("ingress:TestClineEnvelopeFixtureIsUnwrappedAndAccounted", "ingress:TestClineEnvelopeIsUnwrappedBeforeResponsesTranslation", "ingress:TestClineAuthorizationPrefixReachesUpstream"),
			"an SSE body is not unwrapped", "the {success,data} envelope is unwrapped and accounted")
	case "kenari":
		return pair(ref("app:TestConfiguredKenariNativeWire", "providers/wire:TestResponsesStreamRewritesConvertedCalls"), ref("app:TestConfiguredKenariNativeWire", "providers/wire:TestRewriteResponsesBody"),
			"Chat, Responses and Messages natively, both modes; Responses custom-tool rewrite on the stream", "native wires and the Responses body rewrite")
	case "azure":
		return pair(ref("ingress:TestConnectionScopedTargetsStreamAndNonStream"), ref("ingress:TestAzureRequestShape", "ingress:TestConnectionScopedTargetsStreamAndNonStream"),
			"deployment path, api-version and Api-Key header, streamed", "deployment path, api-version and Api-Key header")
	case "xiaomi-tokenplan":
		return pair(ref("ingress:TestConnectionScopedTargetsStreamAndNonStream", "ingress:TestTokenplanRoutesPerWire"), ref("ingress:TestConnectionScopedTargetsStreamAndNonStream", "ingress:TestTokenplanRoutesPerWire"),
			"Chat and Messages wires post to their own paths", "Chat and Messages wires post to their own paths")
	case "gemini":
		return pair(ref("ingress:TestGeminiServesEveryConvertedClient", "ingress:TestGeminiArrayFramedStreamIsServedWithUnobservedUsage"), ref("ingress:TestGeminiServesEveryConvertedClient", "protocol/gemini:TestBufferedResponse"),
			"every converted client over streamGenerateContent", "every converted client over generateContent")
	case "ollama":
		return pair(ref("ingress:TestOllamaFinalLineWithoutNewlineIsAccounted", "ingress:TestTruncatedOllamaStreamIsAFailure", "protocol/ollama:TestStreamIsConvertedLineByLine"), ref("protocol/ollama:TestBufferedBodyIsConverted", "ingress:TestOllamaRefusalMovesToTheNextCandidate"),
			"NDJSON stream conversion", "buffered body conversion")
	case "typesafe":
		return pair(ref("app:TestConfiguredSystemOneWireAndAccounting", "ingress:TestSystemOneStreamUsageAtEOFAndAfterOversizedOpaqueLine"), ref("app:TestConfiguredSystemOneWireAndAccounting"),
			"System One stream, usage at EOF", "System One non-stream")
	case "cloudflare-ai":
		return pair(ref("app:TestConfiguredSystemOneWireAndAccounting", "app:TestConfiguredSystemOneCloudflareFallback"), ref("app:TestConfiguredSystemOneWireAndAccounting"),
			"the System One leg; the Chat leg is verified at the URL seam only (wire:TestCloudflareTargetURLs)", "the System One leg; the Chat leg is verified at the URL seam only")
	case "vertex-partner":
		return pair(ref("ingress:TestVertexPartnerRawKeyIsProbedAndServed", "providers/wire:TestVertexPartnerProbesTheProjectOfARawKey", "providers/wire:TestVertexPartnerProbeEndsWithTheRequest"),
			ref("ingress:TestVertexPartnerRawKeyIsProbedAndServed", "providers/wire:TestVertexPartnerProjectPrecedence", "providers/wire:TestVertexPartnerAuthorizesWithAMintedToken"),
			"a raw key with no project is probed for it (vertex.js:35-49,158-164), then streamed with the key in X-Goog-Api-Key; unverified against a live gateway (provider is experimental)",
			"project from the service account, the ADC or projectId (vertex.js:71-74); SA/ADC minted into a bearer; unverified against a live gateway (provider is experimental)")
	case "cursor":
		if stream {
			return cell{kindC, ref("providers/wire:TestCursorConsumeStreamsTextAndStops", "providers/wire:TestCursorDoStopsUpstreamWhenTheClientLeaves", "providers/wire:TestCursorChatServiceServesAnImageTurn"), "the Connect stream codec and cancellation, and the legacy ChatService decoder (image-bearing turns, cursor.js:955-1250); the provider is unavailable"}
		}
		return cell{kindC, ref("protocol/cursor:TestBufferedBody", "providers/wire:TestCursorChatServiceServesAnImageTurn"), "the buffered Chat body, and the pinned bytes and forced-stream fold of the ChatService path; the provider is unavailable"}
	case "commandcode":
		return pair(
			ref("ingress:TestCommandCodeIsServedToEveryClientWire", "protocol/commandcode:TestStreamIsConvertedLineByLine", "providers/wire:TestCommandCodeForcesStreamOnTheEnvelope", "providers/wire:TestCommandCodeDoRecoversOnRetryAndReplaysTheStreamExactly"),
			ref("ingress:TestCommandCodeIsServedToEveryClientWire", "protocol/commandcode:TestBufferedBodyIsAggregated"),
			"every client wire, NDJSON converted line by line, the stream forced on the envelope, a retried error event replayed",
			"every client wire, the forced NDJSON aggregated into one body")
	case "kiro":
		return pair(
			ref("ingress:TestKiroIsServedToEveryClientWire", "protocol/kiro:TestStreamText", "protocol/kiro:TestMessagesStreamText"),
			ref("ingress:TestKiroIsServedToEveryClientWire", "protocol/kiro:TestBufferedMatchesStreamed", "protocol/kiro:TestMessagesBufferedMatchesStream"),
			"every client wire through the 403 to second-host fallback, event stream converted",
			"every client wire through the 403 to second-host fallback, event stream aggregated")
	case "antigravity", "gemini-cli", "vertex":
		return pair(
			ref("ingress:TestGeminiFamilyIsServedToEveryClientWire", "protocol/gemini:TestCloudCodeResponseLegsServeBothWires", "providers/wire:TestCloudCodeURLs", "providers/wire:TestVertexURLs"),
			ref("ingress:TestGeminiFamilyIsServedToEveryClientWire", "protocol/gemini:TestAntigravityBufferedResponse", "providers/wire:TestCloudCodeURLs", "providers/wire:TestVertexURLs"),
			"every client wire over streamGenerateContent?alt=sse, the Cloud Code envelope and its response unwrapped (executors/gemini-cli.js:10-35, antigravity.js:118-135, vertex.js:83-123)",
			"every client wire over generateContent, the Cloud Code envelope and its response unwrapped")
	case "qoder":
		if stream {
			return cell{kindC, ref("providers/wire:TestQoderEnvelopeMatchesPin", "providers/wire:TestQoderEncodeBodyMatchesPin", "providers/wire:TestQoderAttachmentsMatchPin", "providers/wire:TestQoderPrepareChatMatchesPin", "providers/wire:TestQoderPrepareChatSignsTheEncodedBody", "providers/wire:TestQoderUnwrapSSEMatchesPin", "providers/wire:TestQoderUnwrapSSEStopsWhenTheClientLeaves", "ingress:TestQoderDispatchUsesThePreparedEnvelope"), "the pin converts in its executor, not a translator leg (registry/qoder.js:19-26): envelope, encoding, model_config, attachments (executors/qoder.js:210-335, shared/qoder/*.js) and the SSE unwrap with the socket closed at the terminal frame (:426-597) are ported and checked against the pin's own run (PARITY 6.22); the provider is unavailable, so nothing reaches them from dispatch"}
		}
		return cell{kindC, ref("ingress:TestQoderServesNonStreamAndStreamClientsOnEveryWire", "ingress:TestQoderNonStreamFoldUnwrapsBeforeFolding", "providers/wire:TestQoderUnwrapSSEMatchesPin"), "the pin always asks for a stream and folds it into one body above the executor (qoder.js:613-734); the fold is wired (PreFoldStream, dispatch.go) and a Chat, Responses or Messages client that did not stream gets one document, with the fence lifted for the test run only; the provider is unavailable, so nothing reaches it from dispatch"}
	case "grok-web", "perplexity-web":
		return cell{kindC, ref(readinessTest, "ingress:TestGrokWebIsServedToEveryClientWire", "ingress:TestPerplexityWebIsServedToEveryClientWire", "ingress:TestPerplexityWebThreadIsContinuedOnlyOnTheSameConnection", "providers/wire:TestGrokWebStreamToChat", "providers/wire:TestGrokWebBufferedAnswer", "providers/wire:TestPerplexityWebStreamToChat", "providers/wire:TestPerplexityWebBufferedAnswer"), "refused by its immutable readiness, so nothing reaches it from dispatch; the hook Codec serves every client wire, streaming and not, with the fence lifted for the test run only, and the wire tests read the site's stream and whole answer as Chat"}
	case "iflow":
		return cell{kindT, ref("ingress:TestRegistryNativeTransportWires", "ingress:TestIflowRequestIsSigned"), "Chat both modes through the table; the signature over the final body is asserted by TestIflowRequestIsSigned"}
	case "kimi":
		if !stream {
			return cell{kindP, ref("ingress:TestProviderKimiChatNativeReplaysFixture", "ingress:TestMessagesBetaQueryAndKimiDeviceHeaders", "ingress:TestRegistryNativeTransportWires"), "Chat fixture replayed byte for byte; Messages beta query and device headers; Responses through the table"}
		}
	case "xiaomi-mimo":
		if !stream {
			return cell{kindP, ref("ingress:TestProviderXiaomiMimoMessagesNativeUsesProviderPath", "ingress:TestRegistryNativeTransportWires", "providers/wire:TestMimoPreviewDoSendsTheAccountRouteAndRetriesOnce"), "Messages fixture posts to the provider path; Chat through the table; Preview models take the account route with the sealed session (executors/xiaomi-mimo.js:21-99)"}
		}
	case "openrouter":
		if stream {
			return cell{kindP, ref("ingress:TestOpenRouterPassthroughStreamRelaysBytes", "ingress:TestRegistryNativeTransportWires"), "passthrough stream relayed byte for byte"}
		}
	case "codex":
		return pair(
			ref("ingress:TestCodexStreamingClientKeepsItsStreamByteForByte", "ingress:TestCodexDispatchSendsTheEndpointsContractNotTheClientsBody", "providers/wire:TestCodexBodyIsAlwaysStreamedAndNeverStored", "providers/wire:TestCodexInstructionsDefaultOnlyWhenBlank", "providers/wire:TestCodexOnlyTheEndpointsFieldsAreSent", "providers/wire:TestCodexModelIsTheUpstreamOne", "ingress:TestCodexProviderHeadersCarryCLIFingerprint"),
			ref("ingress:TestCodexNonStreamResponsesClientGetsOneJSONFromAForcedStream", "ingress:TestCodexForcedStreamReachesTranslatedClients", "ingress:TestCodexDispatchSendsTheEndpointsContractNotTheClientsBody", "ingress:TestCodexInBandFailureOfAForcedStreamIsAnAccountFailure"),
			"the endpoint's contract (executors/codex.js:417-519): stream forced, store:false, default instructions only when blank, review twin sent as its base model, unsupported fields dropped and the allowlist applied (P02 second slice, PARITY 6.19)",
			"a non-stream Chat, Responses or Messages client gets one body folded from the forced stream; an in-band failure of the stream is an account failure, not a partial answer")
	case "grok-cli":
		return pair(
			ref("ingress:TestGrokCLIStreamingClientKeepsItsStreamByteForByte", "providers/wire:TestGrokCLIBodyIsStreamedNotStoredAndListsOnlyItsFields", "providers/wire:TestGrokCLIHeadersCarryTheCLIIdentityAndSession"),
			ref("ingress:TestGrokCLINonStreamClientGetsOneJSONFromAForcedStream", "providers/wire:TestGrokCLIInputBecomesATurnAndKeepsSystemTurns", "providers/wire:TestGrokCLIToolsAreFlattenedAndHostedToolsKept"),
			"the grok-shell identity and session headers, stream:true and store:false, and the endpoint's field list (executors/grok-cli.js:362-470; P02 second slice, PARITY 6.19)",
			"a non-stream client gets one body folded from the forced stream; input and tools normalized as the pin does")
	case "zed":
		return pair(
			ref("providers/wire:TestZedExchangesTheTokenAndSendsTheEnvelope", "providers/wire:TestZedServesEveryVendorShape", "providers/wire:TestZedStreamEndsAsThePinEndsIt", "providers/wire:TestZedBrokenUpstreamIsAnErrorOnEveryLeg", "providers/wire:TestZedErrorChunkNamesTheRequestedModel", "providers/wire:TestZedStopsUpstreamWhenTheClientLeaves", "ingress:TestZedIsServedThroughItsExchangeAndEnvelope"),
			ref("ingress:TestZedIsServedThroughItsExchangeAndEnvelope", "ingress:TestZedFailedStatusIsTheAssistantsTextWithACleanStop", "ingress:TestZedFailedStatusCoolsTheAccountAndTheNextCandidateServes", "ingress:TestZedQuotaLockOutlastsTheDefaultBackoffAndARateLimitDoesNot", "ingress:TestZedCutOffStreamIsFinishedAsThePinDoes"),
			"LLM-token exchange, thread envelope, per-vendor request and NDJSON frames for Anthropic, OpenAI Responses, Google and xAI (executors/zed.js:60-282, shared/zedAuth.js:258-324); a failed status, a cut-off stream and a non-JSON line end as the pin ends them. Open: the listing still sends the shared bearer (see Authenticated listing); the thread id resolves the pin's client session from headers and the client's body, but not after the pin's token savers (PARITY 6.19)",
			"the forced stream folded to one body for the Chat, Responses and Messages clients (executors/zed.js:60-282); a failed status is the assistant's text with a clean stop and still cools the account and lets the next candidate serve, a cut-off stream is finished, as the pin does")
	case "mimo-free", "mmf":
		return pair(
			ref("providers/wire:TestMimoFreeBootstrapsAJWTAndSendsTheGatewaysHeaders", "providers/wire:TestMimoFreeRetriesOnceWithAFreshJWTOnAuthFailure", "providers/wire:TestMimoFreeRefusesWhenTheJWTCannotBeMinted", "ingress:TestMimoFreeAndMmfAreServedWithABootstrappedJWT"),
			ref("providers/wire:TestMimoFreeAcceptFollowsTheBodysStream", "providers/wire:TestMimoSystemMarker", "ingress:TestMimoFreeAndMmfAreServedWithABootstrappedJWT", "ingress:TestMimoFreeWithoutAJWTSendsNothingAndFails"),
			"(mmf is the same executor under its alias, executors/index.js:68) bootstrap JWT with expiry buffer and one 401/403 re-bootstrap, the gateway's headers and the MiMoCode system marker (executors/mimo-free.js:24-33,79-159); a JWT that cannot be minted refuses the request",
			"(mmf is the same executor under its alias) the same bootstrap and marker on a buffered request; Accept follows the body's stream; a JWT that cannot be minted sends nothing")
	case "ollama-local":
		return cell{kindS, ref("ingress:TestTruncatedOllamaStreamIsAFailure", "protocol/ollama:TestBufferedBodyIsConverted"), "the shared Ollama codec; no test names ollama-local (experimental)"}
	}
	var refs []string
	var notes []string
	if chat[id] {
		refs = append(refs, "ingress:TestRegistryChatWires")
		notes = append(notes, "Chat")
	}
	if !skips[id] {
		for _, transport := range spec.Transports {
			if transport == registry.TransportOpenAIChat && chat[id] {
				continue
			}
			switch transport {
			case registry.TransportOpenAIChat, registry.TransportOpenAIResponses, registry.TransportAnthropic:
				refs = append(refs, "ingress:TestRegistryNativeTransportWires")
				notes = append(notes, string(transport))
			}
		}
	}
	if len(refs) == 0 {
		return cell{kindG, nil, "no test walks this provider's wire"}
	}
	seen := map[string]bool{}
	var unique []string
	for _, r := range refs {
		if !seen[r] {
			seen[r] = true
			unique = append(unique, r)
		}
	}
	note := "shared path, this provider's own URL and credential: " + strings.Join(notes, ", ")
	switch id {
	case "opencode-go":
		note += ". Open: Responses-only models, the session header and body normalization (executors/opencode-go.js:128-210) are not replicated (experimental)"
	case "xiaomi-mimo":
		note += "; Preview models take the account-session route with the sealed passToken and one 401 retry (executors/xiaomi-mimo.js:21-99, TestMimoPreviewDoSendsTheAccountRouteAndRetriesOnce)"
	case "github":
		note += ". Open: Claude models via /v1/messages and the forced stream on Responses (executors/github.js:250-275) are not replicated (experimental)"
	}
	return cell{kindT, unique, note}
}

func errorsCell(spec registry.Spec) cell {
	id := spec.ID
	cells := map[string]cell{
		"codebuddy-cn":     {kindP, ref("providers/wire:TestCodeBuddyFrequencyLimitMessageOfEveryType", "ingress:TestCodeBuddyErrorBodySurvivesFailedFallbackBeyondPeekLimit", "ingress:TestCodeBuddyInbandStreamErrorRelaysParsedMessage"), "gateway frequency-limit body, in-band stream error, long error bodies"},
		"codebuddy-intl":   {kindP, ref("providers/wire:TestCodeBuddyFrequencyLimitMessageOfEveryType", "ingress:TestCodeBuddyErrorBodySurvivesFailedFallbackBeyondPeekLimit", "ingress:TestCodeBuddyInbandStreamErrorRelaysParsedMessage"), "same hook as codebuddy-cn"},
		"anthropic":        {kindP, ref("ingress:TestProviderErrorBodiesCoolTheAccountLikeThePin", "ingress:TestProviderErrorBodiesReachTheClientByteForByte", genericErrors), "{type:error,error:{type}} body: overloaded_error 529 and rate_limit_error 429 cool the account; the body reaches the client unchanged"},
		"minimax":          {kindP, ref("ingress:TestProviderErrorBodiesCoolTheAccountLikeThePin", genericErrors), "Anthropic-shaped body on the Messages wire"},
		"kimi":             {kindP, ref("ingress:TestProviderErrorBodiesCoolTheAccountLikeThePin"), "rate_limit_reached_error body"},
		"mistral":          {kindP, ref("ingress:TestProviderErrorBodiesCoolTheAccountLikeThePin"), "bare {message} body"},
		"groq":             {kindP, ref("ingress:TestProviderErrorBodiesCoolTheAccountLikeThePin"), "rate_limit_exceeded body"},
		"deepseek":         {kindP, ref("ingress:TestProviderErrorBodiesCoolTheAccountLikeThePin", "ingress:TestProviderErrorBodiesReachTheClientByteForByte"), "401 and 400 bodies classified; the 402 Insufficient Balance body reaches the client unchanged and its classification is the pin's (see Unavailability)"},
		"codex":            {kindP, ref("ingress:TestProviderErrorBodiesReachTheClientByteForByte"), "usage_limit_reached body reaches the client unchanged"},
		"grok-cli":         {kindP, ref("ingress:TestProviderErrorBodiesReachTheClientByteForByte"), "{code,error} body of the 402 spending-limit reaches the client unchanged"},
		"zed":              {kindP, ref("ingress:TestProviderErrorBodiesCoolTheAccountLikeThePin"), "trial_blocked 403 cools the account; the body is rewritten with the pin's sentence (zed.js:284-310, TestZedTrialBlockedBodyIsRewritten)"},
		"commandcode":      {kindP, ref("ingress:TestCommandCodeErrorEventReachesEveryClientAsAnHTTPError", "providers/wire:TestParseCommandCodeError", "providers/wire:TestCommandCodeDoRetriesAnErrorEventThenReturnsItAsHTTP", "providers/wire:TestCommandCodeDoRetriesOnlyTheBaseGatewayStatuses", "providers/wire:TestBaseRetryStatusesAndDelaysMatchTheOracle"), "an in-band error event reaches every client wire as its HTTP status (executors/commandcode.js:60-100), and 502/503/504 are retried by the base executor before the wrapper sees them (base.js:100-180); the pin's non-2xx parseError classification (:63-78) is open"},
		"cursor":           {kindC, ref("providers/wire:TestCursorDoReportsAFailureInBandNotAsSuccess", "providers/wire:TestCursorDoPassesUpstreamRejectionsThrough", "protocol/cursor:TestStreamFailures", "providers/wire:TestCursorChatStreamRejectsAnErrorFrameBeforeContent"), "in-band failures and upstream rejections, and the ChatService error frame; the provider is unavailable"},
		"kiro":             {kindP, ref("ingress:TestKiroExhaustedHostsAnswerTheClientWithTheUpstreamStatus", "ingress:TestDispatchEndpointFallback", "protocol/kiro:TestUpstreamExceptionFrames", "ingress:TestKiroWithoutCredentialDoesNotDispatch"), "three hosts then the last host's status to every client wire; 401/403/404 fall through, other statuses do not; exception frames"},
		"vertex":           {kindP, ref("providers/wire:TestVertexRefusesWhatItCannotBind", "providers/wire:TestVertexSendsNothingWhenTheTokenCannotBeMinted"), "an unsafe project or location, a missing project for an OAuth credential, or a token that cannot be minted sends nothing"},
		"antigravity":      {kindP, ref("ingress:TestCloudCodeWithoutAProjectSendsNothing", "protocol/gemini:TestAntigravityStreamFailures", "protocol/gemini:TestAntigravityRefusalIsTyped", "providers/wire:TestAntigravityRetriesAsThePinDoes", "providers/wire:TestAntigravityRetryDelayIsThePins", "providers/wire:TestAntigravityParsersAreThePins", "providers/wire:TestAntigravityWaitEndsWhenTheClientLeaves", "providers/wire:TestAntigravityWaitsAreBounded", "providers/wire:TestAntigravityRequestIDIsTheIDEFormThePinBuilds", "providers/wire:TestAntigravityDateParseIsV8s"), "a connection with no project is refused before anything is sent; typed refusal and stream failures. The pin's retry with backoff (base.js:100-181, antigravity.js:356-446) is repeated on 429/500/502/503/504 and a thrown error, checked against the pin's own executor run offline (#202)"},
		"gemini-cli":       {kindP, ref("ingress:TestCloudCodeWithoutAProjectSendsNothing", "providers/wire:TestPinnedErrorBodyResets", "protocol/gemini:TestAntigravityStreamFailures", "protocol/gemini:TestCloudCodeResponseLegsServeBothWires"), "shares the Cloud Code response legs; the RetryInfo retryDelay hint (gemini-cli.js:38-52) sets the cooldown; a connection with no project is refused before anything is sent"},
		"qoder":            {kindC, ref("ingress:TestQoderMissingIdentityFailsBeforeNetwork", "providers/wire:TestQoderUnwrapSSEMatchesPin", "providers/wire:TestQoderBillingBlockMatchesPin", "providers/wire:TestQoderFirstFrameErrorWritesNothing", "providers/wire:TestQoderPrepareChatMatchesPin"), "refuses before the network without an identity; first-frame error envelopes are returned as an HTTP status before any byte (billing 110, 112, 10605 and pricingUrl as 403), a later one becomes the pin's synthetic chunk (qoder.js:339-444,458-518); the pin's local 400/401 answers are typed errors. Mapping them to dispatch errors and account cooldowns is wiring that is not done; the provider is unavailable"},
		"typesafe":         {kindP, ref("app:TestConfiguredSystemOneValidationAndOpaqueErrorWithoutDatabase", "ingress:TestSystemOneRelayDistinguishesReadFailureFromEOF"), "validation errors and an opaque upstream error"},
		"cloudflare-ai":    {kindP, ref("providers/wire:TestCloudflareTargetFailsClosed", "app:TestConfiguredSystemOneCloudflareFallback"), "an unsafe or missing account id is refused; System One fallback"},
		"azure":            {kindP, ref("ingress:TestAzureWithoutAnEndpointOrAValidDeploymentSendsNothing", "ingress:TestTargetRefusalMessageReachesTheClient"), "no endpoint or an unsafe deployment sends nothing and says why"},
		"vertex-partner":   {kindP, ref("providers/wire:TestVertexPartnerRefusesWithoutAProject", "providers/wire:TestVertexPartnerRejectsAProjectThatIsNotAPathSegment", "providers/wire:TestVertexPartnerSendsNothingWhenTheProbeFindsNoProject", "ingress:TestTargetRefusalFallsThroughToTheNextCandidate"), "an OAuth credential with no project, an unsafe project, or a key whose probe names none is refused before dispatch"},
		"xiaomi-tokenplan": {kindP, ref("providers/wire:TestTokenplanNeverMovesAKeyToAnotherRegionUnlessAsked"), "a key is never sent to another region"},
		"iflow":            {kindP, ref("ingress:TestIflowWithoutCredentialFailsClosed", "providers/wire:TestIflowHeadersRefuseToSignAnEmptyKey"), "an empty credential is refused, not signed"},
		"api-airforce":     {kindP, ref("ingress:TestAirforceStreamFailureDoesNotReturnPartialJSON"), "a failed forced stream is a 502, never partial JSON"},
		"cline":            {kindP, ref("ingress:TestClineEnvelopeOnlyUnwrapsNonStreamSuccess"), "a failure envelope (HTTP 500) is not unwrapped"},
		"clinepass":        {kindP, ref("ingress:TestClineEnvelopeOnlyUnwrapsNonStreamSuccess"), "a failure envelope (HTTP 500) is not unwrapped"},
		"kenari":           {kindP, ref("providers/wire:TestResponsesStreamRewritesAFailedTerminal", "providers/wire:TestResponsesStreamRefusalContinuesTheSequence", "providers/wire:TestRewriteResponsesBodyRefusesArgumentsItCannotRead"), "failed terminal and refusal events keep their sequence; unreadable arguments are refused"},
		"ollama":           {kindP, ref("ingress:TestOllamaRefusalMovesToTheNextCandidate", "ingress:TestTruncatedOllamaStreamIsAFailure"), "a refusal moves on; a truncated stream is a failure"},
		"gemini":           {kindP, ref("ingress:TestGeminiTranslationFailureMovesOn", genericErrors), "translation failure moves on; RESOURCE_EXHAUSTED is rate-limited"},
		"opencode":         {kindP, ref("providers/wire:TestOpenCodeRefusesModelsServedOnAnotherWire"), "models served on another wire are refused, not posted a Chat body"},
		"github":           {kindT, ref(genericErrors, cooldownTest), "no provider-specific error shape; the pin's Copilot executor has none either"},
	}
	if c, ok := cells[id]; ok {
		return c
	}
	if spec.Readiness.Status == registry.ProviderUnavailable {
		return cell{kindC, ref(readinessTest, "ingress:TestWebSiteFailureIsRecordedAsFailedWhileTheClientSeesThePinsFrame", "ingress:TestWebRefusedSessionIsThePinsErrorDocumentWithTheStatusKept", "providers/wire:TestWebHTTPFailuresArePinsErrorDocuments", "providers/wire:TestGrokWebStreamFailureIsThePinsFrameAndIsReported", "providers/wire:TestPerplexityWebFailureIsThePinsFrameAndIsReported"), "refused by its immutable readiness; the pin's error frames and documents are proved with the fence lifted: a site failure is recorded as failed while the client sees the pin's frame, and a refused session keeps its status without echoing the cookie"}
	}
	return cell{kindS, ref(genericErrors, cooldownTest), "shared status/body classification; the pin's executor has no provider-specific parseError"}
}

func tokensCell(spec registry.Spec) cell {
	id := spec.ID
	tokenTable := "ingress:TestProviderCachedAndOrdinaryTokensAreSeparateInTheAccountingRecord"
	cells := map[string]cell{
		"anthropic":      {kindP, ref(tokenTable, "providers/shared:TestParseUsagePerProtocolFamily"), "cache_read_input_tokens and cache_creation_input_tokens, stream (message_start + message_delta) and non-stream"},
		"minimax":        {kindP, ref(tokenTable), "Messages cache_read_input_tokens, both modes"},
		"deepseek":       {kindP, ref(tokenTable), "prompt_cache_hit_tokens beside prompt_tokens, both modes (not a pin shape; DeepSeek's documented one)"},
		"kimi":           {kindP, ref(tokenTable, "ingress:TestProviderKimiChatNativeReplaysFixture"), "top-level cached_tokens, both modes"},
		"mistral":        {kindT, ref(tokenTable), "prompt_tokens_details.cached_tokens, both modes"},
		"codex":          {kindT, ref(tokenTable, "providers/shared:TestParseUsageNestedResponsesReadsCachedTokens"), "Responses input_tokens_details.cached_tokens, both modes"},
		"grok-cli":       {kindT, ref(tokenTable), "Responses input_tokens_details.cached_tokens, both modes"},
		"muse":           {kindT, ref(tokenTable), "Responses input_tokens_details.cached_tokens, both modes"},
		"xai":            {kindT, ref(tokenTable), "Responses input_tokens_details.cached_tokens, both modes"},
		"gemini":         {kindP, ref("providers/shared:TestParseUsagePerProtocolFamily", "ingress:TestGeminiServesEveryConvertedClient"), "cachedContentTokenCount; one usage record per request"},
		"ollama":         {kindP, ref("providers/shared:TestParseUsagePerProtocolFamily", "ingress:TestOllamaFinalLineWithoutNewlineIsAccounted"), "prompt_eval_cached_count is a subset of the prompt; a final line without a newline is accounted"},
		"typesafe":       {kindP, ref("providers/shared:TestSystemOneUsageNativeAndCloudflare", "app:TestConfiguredSystemOneWireAndAccounting"), "System One usage"},
		"cloudflare-ai":  {kindP, ref("providers/shared:TestSystemOneUsageNativeAndCloudflare"), "Cloudflare System One usage; the Chat leg is the shared OpenAI shape"},
		"codebuddy-cn":   {kindP, ref("ingress:TestCodeBuddyCNUsageFixtureReplays", "ingress:TestCodeBuddyMissingUsageStaysUnobserved", "ingress:TestCodeBuddyTranslatedStreamsKeepUsageOptIn"), "usage fixture replayed; missing usage stays unobserved, not zero"},
		"codebuddy-intl": {kindP, ref("ingress:TestCodeBuddyMissingUsageStaysUnobserved", "ingress:TestCodeBuddyTranslatedStreamsKeepUsageOptIn"), "same hook as codebuddy-cn"},
		"cline":          {kindP, ref("ingress:TestClineEnvelopeFixtureIsUnwrappedAndAccounted"), "usage inside the unwrapped envelope is accounted"},
		"api-airforce":   {kindP, ref("ingress:TestAirforceForcedStreamAcrossClientProtocols"), "usage from the forced stream, both client modes"},
		"clinepass":      {kindP, ref("ingress:TestClineEnvelopeFixtureIsUnwrappedAndAccounted"), "usage inside the unwrapped envelope is accounted"},
		"kiro":           {kindP, ref("ingress:TestKiroIsServedToEveryClientWire", "providers/shared:TestKiroUsageScannerReadsMetricsEvents", "protocol/kiro:TestCacheCountsAreSeparate", "protocol/kiro:TestUsageOnlyWhenMeasured", "protocol/kiro:TestKiroUsage"), "the metricsEvent's counts, cache apart from the prompt (kiro.js:917), streamed and buffered, for every client wire"},
		"commandcode":    {kindP, ref("ingress:TestCommandCodeIsServedToEveryClientWire", "providers/shared:TestCommandCodeUsageIsReplacedWholesaleAndAgreesWithTheLeg", "protocol/commandcode:TestUsage"), "the finish event's prompt and output as the pin reads them (commandcode-to-openai.js:155-163); cachedInputTokens is an observation, not a cache read (PARITY V37)"},
		"cursor":         {kindC, ref("protocol/cursor:TestStreamIsPassedThroughWithoutUsage"), "no usage on the Connect stream stays unobserved; the provider is unavailable"},
		"antigravity":    {kindP, ref("ingress:TestGeminiFamilyIsServedToEveryClientWire", "providers/shared:TestGeminiFamilyUsageIsReadInsideTheCloudCodeEnvelope", "protocol/gemini:TestAntigravityUsage"), "usageMetadata inside the response envelope, cache read apart"},
		"gemini-cli":     {kindP, ref("ingress:TestGeminiFamilyIsServedToEveryClientWire", "providers/shared:TestGeminiFamilyUsageIsReadInsideTheCloudCodeEnvelope", "protocol/gemini:TestAntigravityUsage"), "usageMetadata inside the response envelope, cache read apart"},
		"vertex":         {kindP, ref("ingress:TestGeminiFamilyIsServedToEveryClientWire", "providers/shared:TestGeminiFamilyUsageIsReadInsideTheCloudCodeEnvelope"), "usageMetadata of the Gemini body, cache read apart"},
		"qoder":          {kindC, ref("providers/wire:TestQoderUsageMatchesPin", "providers/wire:TestQoderUnwrapSSEMatchesPin"), "cached counts from five spellings and the finish and usage frames coalesced into one chunk (shared/qoder/sse.js:23-208), checked against the pin's own run (PARITY 6.22); the provider is unavailable, so no usage record is produced from dispatch"},
	}
	if c, ok := cells[id]; ok {
		return c
	}
	if spec.Readiness.Status == registry.ProviderUnavailable {
		return cell{kindC, ref(readinessTest, "ingress:TestGrokWebIsServedToEveryClientWire", "ingress:TestGrokWebBufferedChatCarriesThePinsEstimateOnly", "ingress:TestPerplexityWebIsServedToEveryClientWire"), "refused by its immutable readiness; the site reports no usage, so accounting is unobserved (the outcome books no tokens) and the pin's length/4 estimate stays client-visible"}
	}
	return cell{kindS, ref("providers/shared:TestParseUsagePerProtocolFamily", "ingress:TestNativeNonStreamEmitsExactlyOneReportedRecord", "ingress:TestNativeStreamEmitsOneRecordFromFinalChunk"), "shared OpenAI-shape accounting (prompt_tokens_details.cached_tokens); nothing names this provider"}
}

// buildMatrix evaluates every cell for the 81 active providers, in the order of
// docs/PROVIDER_BASELINE.md (the registry order).
func buildMatrix(t *testing.T) (specs []registry.Spec, rows map[string][columns]cell) {
	t.Helper()
	specs = registry.Builtins()
	chat, skips := chatWires(t), nativeWireSkips(t)
	rows = map[string][columns]cell{}
	for _, spec := range specs {
		var row [columns]cell
		row[colList] = listingCell(spec)
		row[colQuota] = quotaCell(spec)
		row[colUnavailable] = unavailableCell(spec)
		row[colImport] = importCell(spec)
		row[colStream] = modeCell(t, spec, true, chat, skips)
		row[colNonStream] = modeCell(t, spec, false, chat, skips)
		row[colErrors] = errorsCell(spec)
		row[colTokens] = tokensCell(spec)
		rows[spec.ID] = row
	}
	return specs, rows
}

// testFunctions maps "dir" (relative to internal/) to the Test functions in it.
func testFunctions(t *testing.T) map[string]map[string]bool {
	t.Helper()
	out := map[string]map[string]bool{}
	fset := token.NewFileSet()
	err := filepath.Walk("..", func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(path, "_test.go") {
			return err
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		dir := filepath.ToSlash(filepath.Dir(path))
		dir = strings.TrimPrefix(dir, "../")
		if dir == ".." {
			dir = ""
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Recv == nil && strings.HasPrefix(fn.Name.Name, "Test") {
				if out[dir] == nil {
					out[dir] = map[string]bool{}
				}
				out[dir][fn.Name.Name] = true
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func TestP08MatrixCoversEveryActiveProviderWithRealEvidence(t *testing.T) {
	specs, rows := buildMatrix(t)
	if len(specs) != 81 || len(rows) != 81 {
		t.Fatalf("matrix has %d providers (%d rows), want the 81 active ones", len(specs), len(rows))
	}
	tests := testFunctions(t)
	for _, spec := range specs {
		for column, c := range rows[spec.ID] {
			where := fmt.Sprintf("%s / %s", spec.ID, columnTitle[column])
			switch c.Kind {
			case kindP, kindC, kindT, kindS, kindU:
				if len(c.Refs) == 0 {
					t.Errorf("%s: kind %s names no test", where, c.Kind)
				}
			case kindN:
				if !regexp.MustCompile(`(?:open-sse/|route\.js)[^ ]*:\d`).MatchString(c.Note) {
					t.Errorf("%s: a not-applicable cell must cite a pin file and line: %q", where, c.Note)
				}
				if len(strings.TrimSpace(c.Note)) < 40 {
					t.Errorf("%s: a not-applicable cell must give the pin's reason: %q", where, c.Note)
				}
			case kindG:
				if c.Note == "" {
					t.Errorf("%s: a gap must say what is missing", where)
				}
			default:
				t.Errorf("%s: unknown kind %q", where, c.Kind)
			}
			for _, r := range c.Refs {
				dir, name, ok := strings.Cut(r, ":")
				if !ok || !tests[dir][name] {
					t.Errorf("%s: %q is not a Test function in internal/%s", where, r, dir)
				}
			}
		}
	}
	// Every active provider must name at least one test of its own: a P cell, or
	// a C cell for a provider the build refuses (its codec is still tested under
	// its id). A row whose evidence is only the shared path (T/S), only a
	// not-applicable pin (N), or only its readiness refusal (U) is exactly the
	// regression the P08 breadth review found.
	for _, spec := range specs {
		hasP, hasC := false, false
		for _, c := range rows[spec.ID] {
			hasP = hasP || c.Kind == kindP
			hasC = hasC || c.Kind == kindC
		}
		if !hasP && !hasC {
			t.Errorf("%s has no provider-specific test (no P or C cell); the shared path is not evidence for this provider", spec.ID)
		}
	}
	// A provider the build refuses must never claim served evidence.
	for _, spec := range specs {
		if spec.Readiness.Status != registry.ProviderUnavailable {
			continue
		}
		for _, column := range []int{colStream, colNonStream} {
			if k := rows[spec.ID][column].Kind; k == kindT || k == kindS {
				t.Errorf("%s is unavailable but its %s cell claims %s evidence", spec.ID, columnTitle[column], k)
			}
		}
	}
	// A served provider is never "refused by readiness".
	for _, spec := range specs {
		if !spec.Readiness.Served() {
			continue
		}
		for column, c := range rows[spec.ID] {
			if c.Kind == kindU || c.Kind == kindC {
				t.Errorf("%s is served but its %s cell is %s", spec.ID, columnTitle[column], c.Kind)
			}
		}
	}
}

var kindOrder = []kind{kindP, kindC, kindT, kindS, kindU, kindG, kindN}

func kindIndex(k kind) int {
	for i, candidate := range kindOrder {
		if candidate == k {
			return i
		}
	}
	return len(kindOrder)
}

func renderMatrix(specs []registry.Spec, rows map[string][columns]cell) string {
	var b strings.Builder
	b.WriteString(matrixIntro)

	// Totals.
	var withP, withProviderTest, codecOnly, sharedOnly, noEvidence []string
	for _, spec := range specs {
		hasP, hasC, hasShared := false, false, false
		for _, c := range rows[spec.ID] {
			hasP = hasP || c.Kind == kindP
			hasC = hasC || c.Kind == kindC
			hasShared = hasShared || c.Kind == kindT || c.Kind == kindS
		}
		if hasP {
			withP = append(withP, spec.ID)
		}
		if !hasP && hasC {
			codecOnly = append(codecOnly, spec.ID)
		}
		switch {
		case hasP || hasC:
			withProviderTest = append(withProviderTest, spec.ID)
		case hasShared:
			sharedOnly = append(sharedOnly, spec.ID)
		default:
			noEvidence = append(noEvidence, spec.ID)
		}
	}
	b.WriteString("## Honest totals\n\n")
	fmt.Fprintf(&b, "Of the %d active providers:\n\n", len(specs))
	fmt.Fprintf(&b, "- **%d** have at least one provider-specific test (a `P` cell): %s.\n", len(withP), codeList(withP))
	fmt.Fprintf(&b, "- **%d** have any provider-specific evidence (a `P` or `C` cell); **%d** of them only through a codec-level test (`C`) for a provider the build refuses: %s.\n", len(withProviderTest), len(withProviderTest)-len(withP), codeList(codecOnly))
	fmt.Fprintf(&b, "- **%d** are shared-path only: every evidenced cell is a table row (`T`) or the shared path (`S`), and the rest are not applicable, refused or gaps: %s.\n", len(sharedOnly), codeList(sharedOnly))
	fmt.Fprintf(&b, "- **%d** have no test of any kind: every cell is `N`, `U` or `G`: %s.\n\n", len(noEvidence), codeList(noEvidence))
	var servingPath []string
	for _, spec := range specs {
		for _, column := range []int{colStream, colNonStream, colErrors, colTokens} {
			if rows[spec.ID][column].Kind == kindP {
				servingPath = append(servingPath, spec.ID)
				break
			}
		}
	}
	fmt.Fprintf(&b, "Counting only the serving path (a `P` in streaming, non-streaming, error shapes or tokens), **%d** providers have a provider-specific test: %s. The other %d have one only for listing, quota or import, or none.\n\n", len(servingPath), codeList(servingPath), len(specs)-len(servingPath))
	b.WriteString("Kinds count cells, not providers: a `P` row can still have `T`, `S` and `G` cells, and the per-column tables below say which.\n\n")
	b.WriteString("Cells by kind and column:\n\n| Column |")
	for _, k := range kindOrder {
		fmt.Fprintf(&b, " %s |", k)
	}
	b.WriteString("\n|---|")
	for range kindOrder {
		b.WriteString("---:|")
	}
	b.WriteString("\n")
	totals := map[kind]int{}
	for column := 0; column < columns; column++ {
		counts := map[kind]int{}
		for _, spec := range specs {
			counts[rows[spec.ID][column].Kind]++
			totals[rows[spec.ID][column].Kind]++
		}
		fmt.Fprintf(&b, "| %s |", columnTitle[column])
		for _, k := range kindOrder {
			fmt.Fprintf(&b, " %d |", counts[k])
		}
		b.WriteString("\n")
	}
	b.WriteString("| **All cells** |")
	for _, k := range kindOrder {
		fmt.Fprintf(&b, " **%d** |", totals[k])
	}
	fmt.Fprintf(&b, "\n\n%d cells in all (%d providers x %d behaviours). `N` cells carry a pin citation in the evidence tables below.\n\n", len(specs)*columns, len(specs), columns)

	// Matrix.
	b.WriteString("## The matrix\n\n| Provider | Readiness |")
	for column := 0; column < columns; column++ {
		fmt.Fprintf(&b, " %s |", columnShort[column])
	}
	b.WriteString("\n|---|---|")
	for column := 0; column < columns; column++ {
		b.WriteString(":-:|")
	}
	b.WriteString("\n")
	for _, spec := range specs {
		fmt.Fprintf(&b, "| `%s` | %s |", spec.ID, spec.Readiness.Status)
		for _, c := range rows[spec.ID] {
			fmt.Fprintf(&b, " %s |", c.Kind)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")

	// Evidence per column.
	b.WriteString("## Evidence by column\n\nEach group is one distinct claim. Test names are `internal/<dir>:<Test>`. `U/` is `open-sse/services/usage/` and `route.js` is `src/app/api/providers/[id]/models/route.js`, both at the pin.\n\n")
	for column := 0; column < columns; column++ {
		fmt.Fprintf(&b, "### %s\n\n", columnTitle[column])
		type group struct {
			c   cell
			ids []string
		}
		var groups []*group
		index := map[string]*group{}
		for _, spec := range specs {
			c := rows[spec.ID][column]
			key := string(c.Kind) + "|" + strings.Join(c.Refs, ",") + "|" + c.Note
			g := index[key]
			if g == nil {
				g = &group{c: c}
				index[key] = g
				groups = append(groups, g)
			}
			g.ids = append(g.ids, spec.ID)
		}
		sort.SliceStable(groups, func(i, j int) bool { return kindIndex(groups[i].c.Kind) < kindIndex(groups[j].c.Kind) })
		for _, g := range groups {
			fmt.Fprintf(&b, "- **%s** %s", g.c.Kind, codeList(g.ids))
			if g.c.Note != "" {
				fmt.Fprintf(&b, " — %s", g.c.Note)
			}
			if len(g.c.Refs) > 0 {
				fmt.Fprintf(&b, " Tests: %s.", codeList(g.c.Refs))
			}
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}
	b.WriteString(matrixFindings)
	return b.String()
}

func codeList(items []string) string {
	if len(items) == 0 {
		return "none"
	}
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = "`" + item + "`"
	}
	return strings.Join(quoted, ", ")
}

func TestP08MatrixDocIsCurrent(t *testing.T) {
	specs, rows := buildMatrix(t)
	want := renderMatrix(specs, rows)
	path := "../../docs/P08_TEST_MATRIX.md"
	if os.Getenv("P08_WRITE") != "" {
		if err := os.WriteFile(path, []byte(want), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != want {
		t.Fatalf("docs/P08_TEST_MATRIX.md is stale; regenerate it with P08_WRITE=1 go test ./internal/parity -run TestP08MatrixDocIsCurrent")
	}
}

const matrixIntro = `# P08 test matrix (#76)

Task: per-provider deterministic evidence for authenticated listing, quota and
applicable unavailability, import and refresh, streaming, non-streaming, error
shapes and cached-versus-ordinary token accounting, for all 81 active providers.
The catalog listing a provider is not evidence that it works; a cell is evidence
only when a named test would fail if the behaviour were absent.

Oracle: the matrix was written against the reference at ` + "`1c3b9f47`" + `, read with
` + "`git show 1c3b9f47:<path>`" + `. The current oracle is LiteRouter ` + "`main`" + `, resolved once per run
(` + "`compat/parity/resolve-oracle.mjs`" + `, #289); the oracle-gated tests read the commit in
` + "`ORACLE_SHA`" + `. Every pin citation below is a path and line at ` + "`1c3b9f47`" + `.

This file is generated from ` + "`internal/parity/p08_matrix_test.go`" + `. ` + "`TestP08MatrixCoversEveryActiveProviderWithRealEvidence`" + `
fails if a row is missing, a cell names a test that does not exist, an active
provider's evidence is only the shared path (no ` + "`P`" + ` or codec ` + "`C`" + ` cell), a
not-applicable cell cites no pin file and line, or a served provider claims a
refusal. A ` + "`P`" + ` may be one parameterised test that lists the provider's id and
asserts that provider's own pinned values. ` + "`TestP08MatrixDocIsCurrent`" + `
fails if this file differs from the table; regenerate with
` + "`P08_WRITE=1 go test ./internal/parity -run TestP08MatrixDocIsCurrent`" + `.

The per-provider onboarding/quota acceptance and residual owner is
` + "`docs/PROVIDER_ACCEPTANCE_MATRIX.md`" + ` (#230). This file stays the per-behaviour
evidence; neither document alone is a live or production claim.

## Kinds

| Kind | Meaning |
|---|---|
| ` + "`P`" + ` | a test that exercises a provider-specific branch, fixture or wire |
| ` + "`C`" + ` | a codec-level test only; the provider is unavailable (SPEC §36), so nothing reaches it from dispatch |
| ` + "`T`" + ` | this provider is one row of a table over the shared path: it checks its own URL, credential and body, but a bug in the shared path would fail every row alike |
| ` + "`S`" + ` | the shared path only; no test names this provider |
| ` + "`N`" + ` | not applicable, with the pin's reason and file:line |
| ` + "`U`" + ` | refused by its immutable readiness (unavailable); the refusal is asserted |
| ` + "`G`" + ` | not covered; the note says what is missing |

Readiness is a property of the build and separate from enabled and from a stored
credential (#176): a ` + "`U`" + ` or ` + "`C`" + ` cell says nothing about whether a credential exists.
An absent value reads as unknown, never as zero, and a failed read keeps the last
known good view (#173); the quota and token cells are written against that rule.

`

const matrixFindings = `## What P08 added

New deterministic tests (no network, sanitized synthetic bodies shaped like the pin):

- ` + "`internal/providers/usage/p08_test.go`" + `: pin-shaped quota fixtures for the readers that had none, namely OpenCode Go, Ollama, Xiaomi MiMo, Zed, Command Code, MiniMax and MiniMax CN, Qoder, and the GLM CN and CodeBuddy International regional hosts. They assert used-versus-remaining semantics, that an absent window is skipped rather than invented, exhausted windows, and that failures are errors, not healthy accounts.
- ` + "`internal/ingress/p08_tokens_test.go`" + `: cached versus ordinary tokens through real dispatch with each provider's real id, stream and non-stream: Anthropic and MiniMax Messages cache read/creation, DeepSeek ` + "`prompt_cache_hit_tokens`" + `, Kimi top-level ` + "`cached_tokens`" + `, Mistral details, and Responses ` + "`input_tokens_details`" + ` for Codex, Grok CLI, Muse and xAI.
- ` + "`internal/ingress/p08_breadth_test.go`" + `: the 31 providers whose pinned entry is a plain API-key wire each have a named row in one parameterised test. It drives real dispatch and asserts that provider's own pinned wire URL, bearer form, static catalog and models listing, in both modes; ` + "`TestRegistryChatWires`" + ` remains the ` + "`T`" + ` evidence for the shared seam.
- ` + "`internal/ingress/p08_wires_test.go`" + `: stream and non-stream on every native transport of every served provider that ` + "`TestRegistryChatWires`" + ` (Chat only) did not already walk, the connection-scoped Azure and Token Plan targets, and OpenCode's forced stream folded to one JSON body. These are ` + "`T`" + ` evidence, not provider-specific behaviour.
- ` + "`internal/ingress/p08_errors_test.go`" + `: provider-keyed non-OpenAI error bodies classified against the pin's rules and relayed to the client byte for byte, plus ` + "`TestP08ErrorClassificationClosedGaps`" + `, which now asserts the 402 cooldown (it pinned the divergence before it was closed) and ` + "`TestPaymentRequiredAccountFallsBackToTheNextAccount`" + `, which proves the next account serves.
- ` + "`internal/parity/listing_urls_test.go`" + ` (oracle-gated): the authenticated-listing URL Routeweft builds for each of the 23 providers the pin lists with a plain bearer GET equals the pinned URL.

## Gaps this audit found

These were findings of the audit. Each is recorded with its disposition; none is left open without a reason.

1. **402 Payment Required was not an unavailability signal when this audit began.** The pin cools the account for two minutes and falls back (` + "`errorConfig.js:77`" + `, ` + "`accountFallback.js:35-46`" + `). Routeweft classified 402 as a terminal client error, so an out-of-credit DeepSeek or OpenRouter account kept being tried. Closed in #76 P08: ` + "`routing.ClassifyStatus`" + ` reads 402 as an account failure, a two-minute cooldown unless the provider sends ` + "`Retry-After`" + `, and the next account serves (` + "`TestP08ErrorClassificationClosedGaps`" + `, ` + "`TestPaymentRequiredAccountFallsBackToTheNextAccount`" + `, ` + "`routing:TestClassifyStatusPaymentRequiredFallsBackAndCools`" + `). Grok CLI keeps its own rule from #184.
2. **Codex ` + "`usage_limit_reached`" + ` ignored ` + "`resets_at`" + ` when this audit began.** P02 (#177) closed it (` + "`wire/error_reset.go`" + `); the gap guard for it was removed and the Codex cell is now P.
3. **Groq had no quota reader in effect when this audit began.** #211 closed it: the reader now parses the ` + "`x-ratelimit-*`" + ` headers the pin reads (` + "`usage/groq.js:44-129`" + `) and the Groq quota and unavailability cells are now P.
4. **A zero DeepSeek balance read as unlimited when this audit began.** #212 closed the read side: each currency is its own credit window, a zero stays zero and ` + "`is_available`" + ` is explicit (` + "`TestDeepSeekPinnedBalanceKeepsCurrenciesApart`" + `). Balances still do not take an out-of-credit account out of rotation; that is gap 1.
5. **Codex and Grok CLI forced-stream executors were not replicated when this audit began.** Closed by the P02 second slice (PARITY 6.19) and now evidenced here: Codex (` + "`executors/codex.js:415-515`" + `: forced stream, ` + "`store:false`" + `, default instructions, allowlist) and Grok CLI (` + "`executors/grok-cli.js:362-470`" + `: grok-shell identity, forced stream) have stream and non-stream cells backed by dispatch-level tests (` + "`TestCodexDispatchSendsTheEndpointsContractNotTheClientsBody`" + `, ` + "`TestGrokCLIStreamingClientKeepsItsStreamByteForByte`" + `, ` + "`TestCodexNonStreamResponsesClientGetsOneJSONFromAForcedStream`" + `, ` + "`TestGrokCLINonStreamClientGetsOneJSONFromAForcedStream`" + `). Readiness is unchanged. Zed and MiMo Free with its alias mmf were closed earlier by #192.
6. Smaller, in the cell notes: Command Code's empty window, MiniMax's camelCase envelope, Zed's ` + "`trial_blocked`" + ` message rewrite, Gemini CLI's ` + "`retryDelay`" + ` hint. Claude's and ollama-local's authenticated listing, Qoder's non-stream fold and the Vertex refresh were the last ` + "`G`" + ` cells and are now evidenced; the Vertex, Grok Web and Perplexity Web import cells are pin-cited ` + "`N`" + `.

## Storage and telemetry impact of the P03 upstream-id rewrite

The registry rewrite of Blackbox and DeepSeek ids (` + "`registry.UpstreamModelID`" + `) changes only the ` + "`model`" + ` on the wire. ` + "`ProviderRef.CatalogModel`" + ` carries the catalog id and ` + "`RecordedModel()`" + ` returns it, so request outcomes, request logs, usage rollups, live activity and operator price overrides stay keyed on the catalog id. No schema change and no migration; the storage rows for rewritten models are keyed on the catalog id by design (` + "`TestRewrittenUpstreamModelIDStaysOnTheWireNotInTelemetry`" + `).

## What stays open

P08 does not make any provider "work". The matrix has no ` + "`G`" + ` cell, and every active row now names at least one test of its own: 79 have a ` + "`P`" + ` cell and the two providers this build refuses (` + "`grok-web`" + `, ` + "`perplexity-web`" + `) have provider-named codec ` + "`C`" + ` cells. No row is shared-path only (0) and no row has no test (0). A ` + "`T`" + ` or ` + "`S`" + ` cell still means only the shared path was exercised and a bug in it would fail every row alike; an ` + "`N`" + ` cell carries the pin's file and line. That is deterministic evidence against synthetic upstreams, not a live or production claim; no request has reached a real provider from this repository.
`
