package registry

// Builtins returns the first built-in provider group: OpenAI Chat
// Completions providers that authenticate with API keys and use a fixed base
// URL (docs/PROVIDER_BASELINE.md group 1). Base URLs were verified against the
// reference snapshot named by PROVIDER_BASELINE.md
// (open-sse/providers/registry at commit
// 2ffb7922954112b30425cd487d686758e519397e). Providers with native
// multi-protocol transports, OAuth/cookie auth, specialized wire formats or
// runtime endpoints are added by their own groups.
func Builtins() []Spec {
	return []Spec{

		{ID: "alicode-intl", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://coding-intl.dashscope.aliyuncs.com/v1", ModelCatalog: CatalogStatic, Quirks: []Quirk{QuirkCacheControl}},
		{ID: "alitp-intl", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1", ModelCatalog: CatalogStatic, Quirks: []Quirk{QuirkCacheControl}},
		{ID: "alicode", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://coding.dashscope.aliyuncs.com/v1", ModelCatalog: CatalogStatic},
		{ID: "alims-intl", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://dashscope-intl.aliyuncs.com/compatible-mode/v1", ModelCatalog: CatalogStatic},
		{ID: "api-airforce", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.airforce/v1", ModelCatalog: CatalogDynamic, PassthroughModels: true},
		{ID: "baidu", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://qianfan.baidubce.com/v2", ModelCatalog: CatalogStatic},
		{ID: "bazaarlink", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://bazaarlink.ai/api/v1", ModelCatalog: CatalogStatic},
		{ID: "blackbox", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.blackbox.ai/v1", ModelCatalog: CatalogStatic},
		{ID: "bluesminds", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.bluesminds.com/v1", ModelCatalog: CatalogStatic},
		{ID: "byteplus", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://ark.ap-southeast.bytepluses.com/api/coding/v3", ModelCatalog: CatalogStatic},
		{ID: "cerebras", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.cerebras.ai/v1", ModelCatalog: CatalogStatic},
		{ID: "chutes", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://llm.chutes.ai/v1", ModelCatalog: CatalogStatic},
		{ID: "cohere", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.cohere.ai/v1", ModelCatalog: CatalogStatic},
		{ID: "featherless", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.featherless.ai/v1", ModelCatalog: CatalogStatic},
		{ID: "fireworks", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.fireworks.ai/inference/v1", ModelCatalog: CatalogStatic},
		{ID: "groq", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.groq.com/openai/v1", ModelCatalog: CatalogStatic, ReportsUsage: true},
		{ID: "hyperbolic", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.hyperbolic.xyz/v1", ModelCatalog: CatalogStatic},
		{ID: "kilo-gateway", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.kilo.ai/api/gateway", ModelCatalog: CatalogStatic},
		{ID: "llm7", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.llm7.io/v1", ModelCatalog: CatalogPassthrough, PassthroughModels: true},
		{ID: "mistral", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.mistral.ai/v1", ModelCatalog: CatalogStatic},
		{ID: "morph", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.morphllm.com/v1", ModelCatalog: CatalogStatic},
		{ID: "nebius", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.studio.nebius.ai/v1", ModelCatalog: CatalogStatic},
		{ID: "nvidia", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://integrate.api.nvidia.com/v1", ModelCatalog: CatalogStatic},
		{ID: "openrouter", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://openrouter.ai/api/v1", ModelCatalog: CatalogDynamic, PassthroughModels: true},
		{ID: "perplexity", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.perplexity.ai", ModelCatalog: CatalogStatic},
		{ID: "poolside", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://inference.poolside.ai/v1", ModelCatalog: CatalogStatic},
		{ID: "sambanova", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.sambanova.ai/v1", ModelCatalog: CatalogStatic},
		{ID: "siliconflow", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.siliconflow.com/v1", ModelCatalog: CatalogStatic},
		{ID: "tencent", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.hunyuan.cloud.tencent.com/v1", ModelCatalog: CatalogStatic},
		{ID: "together", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.together.xyz/v1", ModelCatalog: CatalogStatic},
		{ID: "venice", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.venice.ai/api/v1", ModelCatalog: CatalogDynamic, PassthroughModels: true},
		{ID: "vercel-ai-gateway", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://ai-gateway.vercel.sh/v1", ModelCatalog: CatalogDynamic, PassthroughModels: true, ReportsUsage: true},
		{ID: "volcengine-ark", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://ark.cn-beijing.volces.com/api/coding/v3", ModelCatalog: CatalogStatic},
	}
}

// NewBuiltinCatalog validates and indexes the built-in provider group.
func NewBuiltinCatalog() (*Catalog, error) { return NewCatalog(Builtins()) }
