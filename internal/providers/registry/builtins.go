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
	specs := []Spec{

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
	specs = append(specs, NativeOpenAI()...)
	specs = append(specs, Codex()...)
	specs = append(specs, Anthropic()...)
	specs = append(specs, Gemini()...)
	specs = append(specs, Claude()...)
	specs = append(specs, MultiTransport()...)
	specs = append(specs, LocalAndNoAuth()...)
	specs = append(specs, APIGateways()...)
	specs = append(specs, OAuthSpecialized()...)
	return append(specs, SpecializedWire()...)
}

// SpecializedWire returns providers from PROVIDER_BASELINE's Specialized
// transport class plus the remaining dual-auth/passthrough identities. Each
// entry names its provider-specific wire protocol (BDR-009); the wire adapter,
// Google/AWS/RSA credential flows, import helpers and quota clients land in
// their own assigned Sprint 5 slices. Cookie/session providers keep arbitrary
// current model ids routable.
//
// kenari advertises Chat, Responses and Messages natively so a source-matching
// route skips translation (BDR-010). `azure` is operator-configurable: its base
// URL is supplied per connection, so the seeded origin stays empty and the
// endpoint is resolved from provider-specific data at request time.
func SpecializedWire() []Spec {
	return []Spec{
		{ID: "antigravity", Transports: []Protocol{TransportAntigravity}, Auth: AuthOAuth, DefaultBaseURL: "https://cloudcode-pa.googleapis.com", ModelCatalog: CatalogStatic, ReportsUsage: true, StaticModels: []string{
			"gemini-3.8-flash", "gemini-3.7-flash", "gemini-3.6-flash", "gemini-3.1-pro-preview",
		}},
		{ID: "gemini-cli", Transports: []Protocol{TransportGeminiCLI}, Auth: AuthOAuth, DefaultBaseURL: "https://cloudcode-pa.googleapis.com/v1internal", ModelCatalog: CatalogStatic, ReportsUsage: true, StaticModels: []string{
			"gemini-3.1-pro-preview", "gemini-3-pro-preview", "gemini-3-flash-preview", "gemini-3.1-flash-lite-preview",
			"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.5-flash-lite",
		}},
		{ID: "cursor", Transports: []Protocol{TransportCursor}, Auth: AuthOAuth, DefaultBaseURL: "https://api2.cursor.sh", ModelCatalog: CatalogStatic, StaticModels: []string{
			"default", "claude-4.5-opus-high-thinking", "claude-4.5-opus-high", "claude-4.5-sonnet-thinking", "claude-4.5-sonnet", "claude-4.5-haiku",
			"claude-4.5-opus", "gpt-5.2-codex", "claude-4.6-opus-max", "claude-4.6-sonnet-medium-thinking", "kimi-k2.5", "gemini-3-flash-preview", "gpt-5.2", "gpt-5.3-codex",
		}},
		{ID: "qoder", Transports: []Protocol{TransportQoder}, Auth: AuthAPIKey, AuthModes: []AuthKind{AuthAPIKey, AuthOAuth}, DefaultBaseURL: "https://api3.qoder.sh", ModelCatalog: CatalogStatic, ReportsUsage: true, StaticModels: []string{
			"ultimate", "auto", "performance", "efficient", "lite", "qmodel_38max", "qmodel_latest", "qmodel", "qfmodel", "kmodel_latest", "kmodel", "gmodel", "gfmodel", "dmodel", "dfmodel", "mmodel",
		}},
		{ID: "kiro", Transports: []Protocol{TransportKiro}, Auth: AuthOAuth, AuthModes: []AuthKind{AuthOAuth, AuthAPIKey}, DefaultBaseURL: "https://runtime.us-east-1.kiro.dev", ModelCatalog: CatalogStatic, ReportsUsage: true, StaticModels: []string{
			"claude-opus-5", "claude-opus-5-thinking", "claude-opus-4.8", "claude-opus-4.7", "claude-sonnet-4.6", "claude-sonnet-4.5",
		}},
		{ID: "vertex", Transports: []Protocol{TransportVertex}, Auth: AuthAPIKey, DefaultBaseURL: "https://aiplatform.googleapis.com", ModelCatalog: CatalogStatic, StaticModels: []string{
			"gemini-3.1-pro-preview", "gemini-3.1-flash-lite-preview", "gemini-3-flash-preview", "gemini-2.5-flash",
		}},
		{ID: "commandcode", Transports: []Protocol{TransportCommandCode}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.commandcode.ai/alpha", ModelCatalog: CatalogStatic, ReportsUsage: true, StaticModels: []string{
			"deepseek/deepseek-v4-pro", "deepseek/deepseek-v4-flash", "moonshotai/Kimi-K2.6", "moonshotai/Kimi-K2.5", "zai-org/GLM-5.1", "zai-org/GLM-5",
			"MiniMaxAI/MiniMax-M2.7", "MiniMaxAI/MiniMax-M2.5", "Qwen/Qwen3.6-Max-Preview", "Qwen/Qwen3.6-Plus", "stepfun/Step-3.5-Flash",
		}},
		{ID: "grok-cli", Transports: []Protocol{TransportOpenAIResponses}, Auth: AuthOAuth, DefaultBaseURL: "https://cli-chat-proxy.grok.com", ModelCatalog: CatalogStatic, ReportsUsage: true},
		{ID: "grok-web", Transports: []Protocol{TransportGrokWeb}, Auth: AuthCookie, DefaultBaseURL: "https://grok.com", ModelCatalog: CatalogPassthrough, PassthroughModels: true},
		{ID: "perplexity-web", Transports: []Protocol{TransportPerplexityWeb}, Auth: AuthCookie, DefaultBaseURL: "https://www.perplexity.ai", ModelCatalog: CatalogStatic, StaticModels: []string{
			"pplx-auto", "pplx-sonar", "pplx-gpt", "pplx-gemini", "pplx-sonnet", "pplx-opus", "pplx-nemotron",
		}},
		{ID: "kenari", Transports: []Protocol{TransportOpenAIChat, TransportOpenAIResponses, TransportAnthropic}, Auth: AuthAPIKey, DefaultBaseURL: "https://kenari.id", ModelCatalog: CatalogDynamic, PassthroughModels: true, ReportsUsage: true},
		{ID: "zed", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthOAuth, DefaultBaseURL: "https://cloud.zed.dev", ModelCatalog: CatalogPassthrough, PassthroughModels: true, ReportsUsage: true},
		{ID: "kimchi", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, AuthModes: []AuthKind{AuthAPIKey, AuthOAuth}, DefaultBaseURL: "https://llm.kimchi.dev/openai/v1", ModelCatalog: CatalogPassthrough, PassthroughModels: true},
		{ID: "azure", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "", ModelCatalog: CatalogStatic},
	}
}

// OAuthSpecialized returns the OAuth/PAT/cookie credential providers that
// authenticate differently from the first-party Codex/Claude identities
// (docs/PROVIDER_BASELINE.md OAuth-specialized group). Provider identity stays
// separate from the shared protocol adapters (BDR-009): each entry names only
// its transports, auth kind, base URL and catalog class. Credential flows,
// device/PAT/import helpers and provider-specific header hooks live in provider
// modules and land in their own assigned slices.
//
// cline/clinepass share the cline.bot gateway endpoint and respond with the
// cline envelope; xai/github/kimi/xiaomi-mimo are dual-auth (api-key+oauth) and
// advertise both a native Chat and Responses/Messages transport where the
// baseline matrix requires it. kilocode proxies the OpenRouter catalog, so its
// arbitrary IDs stay routable.
func OAuthSpecialized() []Spec {
	return []Spec{
		{ID: "xai", Transports: []Protocol{TransportOpenAIChat, TransportOpenAIResponses}, Auth: AuthAPIKey, AuthModes: []AuthKind{AuthAPIKey, AuthOAuth}, DefaultBaseURL: "https://api.x.ai/v1", ModelCatalog: CatalogStatic, StaticModels: []string{
			"grok-4.6", "grok-4.5", "grok-4", "grok-4-fast-reasoning", "grok-code-fast-1", "grok-3",
		}},
		{ID: "github", Transports: []Protocol{TransportOpenAIChat, TransportOpenAIResponses, TransportAnthropic}, Auth: AuthOAuth, DefaultBaseURL: "https://api.githubcopilot.com", ModelCatalog: CatalogStatic, ReportsUsage: true, StaticModels: []string{
			"gpt-5.2", "gpt-5.2-codex", "gpt-5.3-codex", "gpt-5.4", "gpt-5.4-mini",
			"claude-haiku-4.5", "claude-opus-4.5", "claude-sonnet-4.5", "claude-sonnet-4.6", "claude-opus-4.6", "claude-opus-4.7",
			"gemini-2.5-pro", "gemini-3-flash-preview", "gemini-3.1-pro-preview", "grok-code-fast-1", "oswe-vscode-prime", "goldeneye-free-auto",
		}},
		{ID: "gitlab", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthOAuth, DefaultBaseURL: "https://gitlab.com", ModelCatalog: CatalogStatic},
		{ID: "iflow", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthOAuth, DefaultBaseURL: "https://apis.iflow.cn/v1", ModelCatalog: CatalogStatic, StaticModels: []string{
			"qwen3-coder-plus", "qwen3-max", "qwen3-vl-plus", "qwen3-max-preview", "qwen3-235b", "qwen3-235b-a22b-instruct",
			"qwen3-235b-a22b-thinking-2507", "qwen3-32b", "kimi-k2", "deepseek-v3.2", "deepseek-v3.1", "deepseek-v3", "deepseek-r1", "glm-4.7",
			"iflow-rome-30ba3b",
		}},
		{ID: "kimi", Transports: []Protocol{TransportOpenAIChat, TransportAnthropic}, Auth: AuthAPIKey, AuthModes: []AuthKind{AuthAPIKey, AuthOAuth}, DefaultBaseURL: "https://api.kimi.com/coding/v1", ModelCatalog: CatalogStatic, ReportsUsage: true, StaticModels: []string{
			"kimi-k3", "k3", "kimi-for-coding", "kimi-for-coding-highspeed", "kimi-k2.7-code", "kimi-k2.7-code-highspeed",
			"kimi-k2.6", "kimi-k2.5", "kimi-k2.5-thinking", "kimi-latest",
		}},
		{ID: "xiaomi-mimo", Transports: []Protocol{TransportOpenAIChat, TransportAnthropic}, Auth: AuthAPIKey, AuthModes: []AuthKind{AuthAPIKey, AuthOAuth}, DefaultBaseURL: "https://api.xiaomimimo.com/v1", ModelCatalog: CatalogStatic, ReportsUsage: true, StaticModels: []string{
			"mimo-x-pro-preview", "mimo-x-flash-preview", "mimo-v2.5-pro", "mimo-v2.5", "mimo-v2-omni", "mimo-v2-flash",
		}},
		{ID: "cline", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthOAuth, DefaultBaseURL: "https://api.cline.bot", ModelCatalog: CatalogStatic, StaticModels: []string{
			"anthropic/claude-opus-4.7", "anthropic/claude-sonnet-4.6", "anthropic/claude-opus-4.6", "openai/gpt-5.3-codex", "openai/gpt-5.4",
			"google/gemini-3.1-pro-preview", "google/gemini-3.1-flash-lite-preview", "kwaipilot/kat-coder-pro",
		}},
		{ID: "clinepass", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, AuthModes: []AuthKind{AuthAPIKey, AuthOAuth}, DefaultBaseURL: "https://api.cline.bot", ModelCatalog: CatalogStatic, StaticModels: []string{
			"cline-pass/glm-5.2", "cline-pass/kimi-k2.7-code", "cline-pass/kimi-k2.6", "cline-pass/deepseek-v4-pro", "cline-pass/deepseek-v4-flash",
			"cline-pass/mimo-v2.5", "cline-pass/mimo-v2.5-pro", "cline-pass/minimax-m3", "cline-pass/qwen3.7-max", "cline-pass/qwen3.7-plus",
		}},
		{ID: "kilocode", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthOAuth, DefaultBaseURL: "https://api.kilo.ai/api/openrouter", ModelCatalog: CatalogDynamic, PassthroughModels: true},
		{ID: "codebuddy-cn", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, AuthModes: []AuthKind{AuthAPIKey, AuthOAuth}, DefaultBaseURL: "https://copilot.tencent.com/v2", ModelCatalog: CatalogStatic, ReportsUsage: true, StaticModels: []string{
			"glm-5.2", "glm-5.1", "glm-5v-turbo", "minimax-m3", "kimi-k2.7", "kimi-k2.6", "hy3", "hy4-preview", "glm-5.3", "glm-5.3-flash",
			"kimi-k3-1", "deepseek-v4-pro", "deepseek-v4.1-flash",
		}},
		{ID: "codebuddy-intl", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, AuthModes: []AuthKind{AuthAPIKey, AuthOAuth}, DefaultBaseURL: "https://www.codebuddy.ai/v2", ModelCatalog: CatalogStatic, ReportsUsage: true, StaticModels: []string{
			"glm-5.2", "glm-5.1", "glm-5.0", "glm-5.0-turbo", "glm-5v-turbo", "glm-4.7", "minimax-m3", "minimax-m2.7", "kimi-k2.7", "kimi-k2.6",
			"kimi-k2.5", "hy3-preview", "deepseek-v4-pro", "deepseek-v4.1-flash", "deepseek-v3-2-volc",
		}},
	}
}

// APIGateways returns remaining API-key gateway/aggregator providers. Dynamic
// providers keep arbitrary IDs routable via passthrough; cloudflare-ai takes
// the account id from provider-specific data, so its base URL stays the
// template origin and the account path is resolved by its provider module.
func APIGateways() []Spec {
	return []Spec{
		{ID: "vertex-partner", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://aiplatform.googleapis.com", ModelCatalog: CatalogStatic},
		{ID: "tokenrouter", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.tokenrouter.com/v1", ModelCatalog: CatalogDynamic, PassthroughModels: true},
		{ID: "perplexity-agent", Transports: []Protocol{TransportOpenAIResponses}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.perplexity.ai/v1", ModelCatalog: CatalogDynamic, PassthroughModels: true},
		{ID: "opencode-go", Transports: []Protocol{TransportOpenAIChat, TransportOpenAIResponses, TransportAnthropic}, Auth: AuthAPIKey, DefaultBaseURL: "https://opencode.ai/zen/go/v1", ModelCatalog: CatalogStatic, ReportsUsage: true},
		{ID: "cloudflare-ai", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.cloudflare.com/client/v4/accounts", ModelCatalog: CatalogStatic},
	}
}

// LocalAndNoAuth returns no-auth/passthrough, hosted Ollama, local Ollama and
// SystemOne-served providers. ollama-local targets a loopback address, which is
// first-class per the provider baseline and requires the explicit trusted-local
// operator policy at request time.
func LocalAndNoAuth() []Spec {
	return []Spec{
		{ID: "mimo-free", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthNone, DefaultBaseURL: "https://api.xiaomimimo.com/api/free-ai/openai/chat", ModelCatalog: CatalogDynamic, PassthroughModels: true},
		{ID: "mmf", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthNone, DefaultBaseURL: "https://api.xiaomimimo.com/api/free-ai/openai/chat", ModelCatalog: CatalogStatic},
		{ID: "opencode", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthNone, DefaultBaseURL: "https://opencode.ai", ModelCatalog: CatalogDynamic, PassthroughModels: true},
		{ID: "ollama", Transports: []Protocol{TransportOllama}, Auth: AuthAPIKey, DefaultBaseURL: "https://ollama.com", ModelCatalog: CatalogStatic},
		{ID: "ollama-local", Transports: []Protocol{TransportOllama}, Auth: AuthNone, DefaultBaseURL: "http://localhost:11434", ModelCatalog: CatalogStatic},
		{ID: "typesafe", Transports: []Protocol{TransportSystemOne}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.typesafe.ai/v1/systemone", ModelCatalog: CatalogStatic},
	}
}

// MultiTransport returns providers that advertise more than one native LLM
// transport so a source-matching route can skip translation (BDR-010). The
// source-matching transport is chosen at request time; the shared base URL is
// the provider origin, and per-transport endpoints live in provider modules.
func MultiTransport() []Spec {
	return []Spec{
		{ID: "deepseek", Transports: []Protocol{TransportOpenAIChat, TransportAnthropic}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.deepseek.com", ModelCatalog: CatalogStatic, ReportsUsage: true, Quirks: []Quirk{QuirkCacheControl}},
		{ID: "glm", Transports: []Protocol{TransportOpenAIChat, TransportAnthropic}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.z.ai", ModelCatalog: CatalogStatic, ReportsUsage: true},
		{ID: "glm-cn", Transports: []Protocol{TransportOpenAIChat}, Auth: AuthAPIKey, DefaultBaseURL: "https://open.bigmodel.cn/api/coding/paas/v4", ModelCatalog: CatalogStatic, ReportsUsage: true},
		{ID: "minimax", Transports: []Protocol{TransportOpenAIChat, TransportAnthropic}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.minimax.io", ModelCatalog: CatalogStatic, ReportsUsage: true},
		{ID: "minimax-cn", Transports: []Protocol{TransportOpenAIChat, TransportAnthropic}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.minimaxi.com", ModelCatalog: CatalogStatic, ReportsUsage: true},
		{ID: "xiaomi-tokenplan", Transports: []Protocol{TransportOpenAIChat, TransportAnthropic}, Auth: AuthAPIKey, DefaultBaseURL: "https://token-plan-sgp.xiaomimimo.com/v1", ModelCatalog: CatalogStatic},
	}
}

// Claude returns the Claude Code OAuth identity using shared Anthropic Messages.
func Claude() []Spec {
	return []Spec{{ID: "claude", Transports: []Protocol{TransportAnthropic}, Auth: AuthOAuth, DefaultBaseURL: "https://api.anthropic.com/v1", ModelCatalog: CatalogStatic, ReportsUsage: true, StaticModels: []string{
		"claude-opus-5", "claude-fable-5-1", "claude-fable-5", "claude-sonnet-5", "claude-haiku-4-5-20251001",
	}}}
}

// Gemini returns the first-party Gemini GenerateContent identity.
func Gemini() []Spec {
	return []Spec{{ID: "gemini", Transports: []Protocol{TransportGemini}, Auth: AuthAPIKey, DefaultBaseURL: "https://generativelanguage.googleapis.com/v1beta/models", ModelCatalog: CatalogStatic, StaticModels: []string{
		"gemini-3.8-flash", "gemini-3.7-flash", "gemini-3.6-flash", "gemini-3.5-flash-lite",
		"gemini-3.1-pro-preview", "gemini-3.1-flash-lite-preview", "gemini-3-flash-preview",
		"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.5-flash-lite", "gemma-4-31b-it",
	}}}
}

// Anthropic returns the first-party Anthropic Messages identity. Message
// headers/beta behavior live in the anthropic provider module.
func Anthropic() []Spec {
	return []Spec{{ID: "anthropic", Transports: []Protocol{TransportAnthropic}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.anthropic.com/v1", ModelCatalog: CatalogStatic, StaticModels: []string{
		"claude-sonnet-4-20250514", "claude-opus-4-20250514", "claude-3-5-sonnet-20241022",
	}}}
}

// Codex returns the OpenAI Codex OAuth provider identity. Its rotating
// credentials and request path are implemented by the Codex provider module.
func Codex() []Spec {
	return []Spec{{ID: "codex", Transports: []Protocol{TransportOpenAIResponses}, Auth: AuthOAuth, DefaultBaseURL: "https://chatgpt.com/backend-api/codex", ModelCatalog: CatalogStatic, ReportsUsage: true, StaticModels: []string{
		"gpt-6-astra", "gpt-6-sol", "gpt-6-luna", "gpt-5.6-sol", "gpt-5.6-sol-review", "gpt-5.6-terra", "gpt-5.6-terra-review",
		"gpt-5.6-luna", "gpt-5.6-luna-review", "gpt-5.5", "gpt-5.5-review", "gpt-5.4", "gpt-5.4-review", "gpt-5.4-mini",
		"gpt-5.4-mini-review", "gpt-5.3-codex-spark", "gpt-5.3-codex-spark-review", "codex-auto-review",
	}}}
}

// NativeOpenAI returns the first-party provider with both source-matching
// OpenAI transports, as required by PRD-PROV-001.
func NativeOpenAI() []Spec {
	return []Spec{{ID: "openai", Transports: []Protocol{TransportOpenAIChat, TransportOpenAIResponses}, Auth: AuthAPIKey, DefaultBaseURL: "https://api.openai.com/v1", ModelCatalog: CatalogStatic, StaticModels: []string{
		"gpt-5.4", "gpt-5.4-mini", "gpt-5.4-nano", "gpt-5.2", "gpt-5.1", "gpt-5", "gpt-5-mini", "gpt-5-nano",
		"gpt-4o", "gpt-4o-mini", "gpt-4-turbo", "gpt-4.1", "gpt-4.1-mini", "gpt-4.1-nano",
		"o3", "o3-mini", "o3-pro", "o4-mini", "o1", "o1-mini",
	}}}
}

// NewBuiltinCatalog validates and indexes the built-in provider group.
func NewBuiltinCatalog() (*Catalog, error) { return NewCatalog(Builtins()) }
