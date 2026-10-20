package shared

import (
	"encoding/json"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
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
	case FamilySystemOne:
		var envelope struct {
			Result json.RawMessage `json:"result"`
		}
		if json.Unmarshal(body, &envelope) != nil {
			return promptcache.Usage{}, false
		}
		if len(envelope.Result) > 0 && string(envelope.Result) != "null" {
			body = envelope.Result
		}
		return parseOpenAIUsage(body)
	default:
		return promptcache.Usage{}, false
	}
}

// openAIWireUsage is one usage object in any of the shapes OpenAI-compatible
// upstreams send: Chat (prompt/completion), Responses (input/output) and the
// vendor cache extensions. Only the counts the accounting contract carries are
// read. reasoning_tokens (completion_tokens_details / output_tokens_details) is
// deliberately not read: upstreams already include it in the output count, so
// the reported output total is emitted unchanged, and how a reasoning token is
// represented on its own belongs to #119. total_tokens is likewise never used:
// the reported parts are emitted as reported, never summed or reconciled.
type openAIWireUsage struct {
	InputTokens      int64               `json:"input_tokens"`
	OutputTokens     int64               `json:"output_tokens"`
	PromptTokens     int64               `json:"prompt_tokens"`
	CompletionTokens int64               `json:"completion_tokens"`
	PromptCacheRead  int64               `json:"prompt_cache_hit_tokens"`
	CachedTokens     int64               `json:"cached_tokens"`
	CacheRead        int64               `json:"cache_read_input_tokens"`
	CacheWrite       int64               `json:"cache_creation_input_tokens"`
	InputDetails     *openAIUsageDetails `json:"input_tokens_details"`
	PromptDetails    *openAIUsageDetails `json:"prompt_tokens_details"`
}

func parseOpenAIUsage(body []byte) (promptcache.Usage, bool) {
	var decoded struct {
		Usage    *openAIWireUsage `json:"usage"`
		Response *struct {
			Usage *openAIWireUsage `json:"usage"`
		} `json:"response"`
	}
	if json.Unmarshal(body, &decoded) != nil {
		return promptcache.Usage{}, false
	}
	usage := decoded.Usage
	if usage == nil && decoded.Response != nil {
		// Responses stream events and response.* terminals nest usage here.
		usage = decoded.Response.Usage
	}
	if usage == nil {
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
	if usage.CachedTokens != 0 {
		cacheRead = usage.CachedTokens
	}
	if usage.CacheRead != 0 {
		cacheRead = usage.CacheRead
	}
	return promptcache.Usage{InputTokens: input, OutputTokens: output, CacheReadTokens: cacheRead, CacheWriteTokens: usage.CacheWrite}, true
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
	raw, err := concerns.WireObject(body)
	if err != nil {
		return promptcache.Usage{}, false
	}
	usage, observed := concerns.OllamaUsage(raw)
	if !observed {
		return promptcache.Usage{}, false
	}
	return promptcache.Usage{InputTokens: usage.Prompt, OutputTokens: usage.Completion, CacheReadTokens: usage.Cached}, true
}
