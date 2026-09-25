package shared

import (
	"encoding/json"

	"github.com/raufimusaddiq/routeweft/internal/transforms/promptcache"
)

// Protocol family identifiers used by the shared usage/error normalizers. They
// match the registry Protocol values (SPEC §10) without importing the registry
// so protocol adapters and providers can both reuse this package.
const (
	FamilyOpenAIChat      = "openai-chat"
	FamilyOpenAIResponses = "openai-responses"
	FamilyAnthropic       = "anthropic-messages"
	FamilyGemini          = "gemini"
	FamilyOllama          = "ollama"
	FamilySystemOne       = "systemone"
)

// ParseUsage extracts normalized token usage from one upstream response body
// (or one decoded stream event) for a protocol family. Cache-read/cache-create
// counts flow through when the upstream reports them; absent usage is reported
// as not present, never estimated (SPEC §9.3, §18).
func ParseUsage(family string, body []byte) (promptcache.Usage, bool) {
	if len(body) == 0 {
		return promptcache.Usage{}, false
	}
	switch family {
	case FamilyAnthropic:
		return promptcache.ParseAnthropicUsage(body)
	case FamilyOpenAIChat, FamilyOpenAIResponses:
		return parseOpenAIUsage(body)
	case FamilyGemini:
		return parseGeminiUsage(body)
	case FamilyOllama:
		return parseOllamaUsage(body)
	default:
		return promptcache.Usage{}, false
	}
}

func parseOpenAIUsage(body []byte) (promptcache.Usage, bool) {
	var decoded struct {
		Usage *struct {
			InputTokens      int64               `json:"input_tokens"`
			OutputTokens     int64               `json:"output_tokens"`
			PromptTokens     int64               `json:"prompt_tokens"`
			CompletionTokens int64               `json:"completion_tokens"`
			PromptCacheRead  int64               `json:"prompt_cache_hit_tokens"`
			PromptCacheMiss  int64               `json:"prompt_cache_miss_tokens"`
			InputDetails     *openAIUsageDetails `json:"input_tokens_details"`
			PromptDetails    *openAIUsageDetails `json:"prompt_tokens_details"`
		} `json:"usage"`
		Response *struct {
			Usage *struct {
				InputTokens  int64 `json:"input_tokens"`
				OutputTokens int64 `json:"output_tokens"`
			} `json:"usage"`
		} `json:"response"`
	}
	if json.Unmarshal(body, &decoded) != nil {
		return promptcache.Usage{}, false
	}
	usage := decoded.Usage
	if usage == nil {
		if decoded.Response != nil && decoded.Response.Usage != nil {
			return promptcache.Usage{InputTokens: decoded.Response.Usage.InputTokens, OutputTokens: decoded.Response.Usage.OutputTokens}, true
		}
		return promptcache.Usage{}, false
	}
	input := usage.InputTokens
	if input == 0 {
		input = usage.PromptTokens
	}
	output := usage.OutputTokens
	if output == 0 {
		output = usage.CompletionTokens
	}
	cacheRead := usage.PromptCacheRead
	if details := firstDetails(usage.InputDetails, usage.PromptDetails); details != nil {
		if details.CachedTokens != 0 {
			cacheRead = details.CachedTokens
		}
	}
	return promptcache.Usage{InputTokens: input, OutputTokens: output, CacheReadTokens: cacheRead}, true
}

type openAIUsageDetails struct {
	CachedTokens int64 `json:"cached_tokens"`
}

func firstDetails(values ...*openAIUsageDetails) *openAIUsageDetails {
	for _, value := range values {
		if value != nil {
			return value
		}
	}
	return nil
}

func parseGeminiUsage(body []byte) (promptcache.Usage, bool) {
	var decoded struct {
		UsageMetadata *struct {
			PromptTokenCount     int64 `json:"promptTokenCount"`
			CandidatesTokenCount int64 `json:"candidatesTokenCount"`
			CachedContentTokens  int64 `json:"cachedContentTokenCount"`
		} `json:"usageMetadata"`
	}
	if json.Unmarshal(body, &decoded) != nil || decoded.UsageMetadata == nil {
		return promptcache.Usage{}, false
	}
	return promptcache.Usage{
		InputTokens:     decoded.UsageMetadata.PromptTokenCount,
		OutputTokens:    decoded.UsageMetadata.CandidatesTokenCount,
		CacheReadTokens: decoded.UsageMetadata.CachedContentTokens,
	}, true
}

func parseOllamaUsage(body []byte) (promptcache.Usage, bool) {
	var decoded struct {
		PromptEvalCount int64 `json:"prompt_eval_count"`
		EvalCount       int64 `json:"eval_count"`
	}
	if json.Unmarshal(body, &decoded) != nil {
		return promptcache.Usage{}, false
	}
	if decoded.PromptEvalCount == 0 && decoded.EvalCount == 0 {
		return promptcache.Usage{}, false
	}
	return promptcache.Usage{InputTokens: decoded.PromptEvalCount, OutputTokens: decoded.EvalCount}, true
}
