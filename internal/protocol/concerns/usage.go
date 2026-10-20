package concerns

import (
	"encoding/json"
	"math"
	"math/big"
	"strconv"
	"strings"
)

// Usage is one request's token counts in the hub convention: Prompt is
// cache-inclusive, Cached and CacheCreation are subsets of it, and Reasoning is
// a subset of Completion (concerns/usage.js:1-14; utils/usageTracking.js:147-210).
type Usage struct {
	Prompt        int64
	Completion    int64
	Total         int64
	Cached        int64
	CacheCreation int64
	Reasoning     int64
}

// OpenAI renders the usage as a Chat usage object. Detail objects appear only
// when they carry a positive count (concerns/usage.js:3-13).
func (u Usage) OpenAI() map[string]any {
	usage := map[string]any{"prompt_tokens": u.Prompt, "completion_tokens": u.Completion, "total_tokens": u.Total}
	if u.Cached > 0 || u.CacheCreation > 0 {
		details := map[string]any{}
		if u.Cached > 0 {
			details["cached_tokens"] = u.Cached
		}
		if u.CacheCreation > 0 {
			details["cache_creation_tokens"] = u.CacheCreation
		}
		usage["prompt_tokens_details"] = details
	}
	if u.Reasoning > 0 {
		usage["completion_tokens_details"] = map[string]any{"reasoning_tokens": u.Reasoning}
	}
	return usage
}

// ClaudeUsage converts an Anthropic usage object to the hub convention
// (concerns/usage.js:21-26): the prompt is input plus cache read plus cache
// creation, and the total is prompt plus output.
func ClaudeUsage(raw map[string]any) Usage {
	input, output := Int(raw["input_tokens"]), Int(raw["output_tokens"])
	read, create := Int(raw["cache_read_input_tokens"]), Int(raw["cache_creation_input_tokens"])
	prompt := input + read + create
	return Usage{Prompt: prompt, Completion: output, Total: prompt + output, Cached: read, CacheCreation: create}
}

// ClaudeUsageFromOpenAI converts a Chat usage object to an Anthropic one
// (translator/response/openai-to-claude.js:80-106): input_tokens is the prompt
// minus cache read and creation, because Anthropic reports cache separately.
// Reasoning is already inside completion_tokens, so it is not added again
// (openai-to-claude.js:108). Like the pin it does not clamp a negative input.
func ClaudeUsageFromOpenAI(usage map[string]any) map[string]any {
	prompt, output := Int(usage["prompt_tokens"]), Int(usage["completion_tokens"])
	details := Object(usage["prompt_tokens_details"])
	read, create := Int(details["cached_tokens"]), Int(details["cache_creation_tokens"])
	result := map[string]any{"input_tokens": prompt - read - create, "output_tokens": output}
	if read > 0 {
		result["cache_read_input_tokens"] = read
	}
	if create > 0 {
		result["cache_creation_input_tokens"] = create
	}
	return result
}

// OllamaUsage reads the counts an Ollama response carries at its top level
// (concerns/usage.js:53-60): prompt_eval_count is the prompt, cache-inclusive;
// prompt_eval_cached_count is the cache-read subset of it; eval_count is the
// output. observed is false when the response reported no tokens at all, so an
// absent count is never recorded as a measured zero. This is the only place the
// three fields are read; the accounting parser calls it too.
func OllamaUsage(raw map[string]any) (usage Usage, observed bool) {
	prompt, output := Int(raw["prompt_eval_count"]), Int(raw["eval_count"])
	if prompt <= 0 && output <= 0 {
		return Usage{}, false
	}
	return Usage{Prompt: prompt, Completion: output, Total: prompt + output, Cached: Int(raw["prompt_eval_cached_count"])}, true
}

// CommandCodeObservations are the counts a CommandCode usage object carries
// beyond the three the pin reads. They are reported as the upstream gave them
// and never subtracted from anything: it is not established that inputTokens is
// cache-inclusive, and a wrong subtraction corrupts every cost figure.
type CommandCodeObservations struct {
	CachedInput int64
	Reasoning   int64
}

// CommandCodeUsage reads a CommandCode (AI SDK) usage object
// (concerns/usage.js:61-65): inputTokens is the prompt, outputTokens the output,
// and totalTokens the upstream total when it sent one. observed is false when no
// count is positive, so an unmeasured zero is never recorded. cachedInputTokens
// and reasoningTokens, which the pin ignores, come back as observations only.
func CommandCodeUsage(raw map[string]any) (usage Usage, extra CommandCodeObservations, observed bool) {
	input, output := Int(raw["inputTokens"]), Int(raw["outputTokens"])
	total := input + output
	if IsNumber(raw["totalTokens"]) {
		total = Int(raw["totalTokens"])
	}
	if input <= 0 && output <= 0 && total <= 0 {
		return Usage{}, CommandCodeObservations{}, false
	}
	return Usage{Prompt: input, Completion: output, Total: total},
		CommandCodeObservations{CachedInput: Int(raw["cachedInputTokens"]), Reasoning: Int(raw["reasoningTokens"])}, true
}

// GeminiUsage reads a Gemini usageMetadata object (concerns/usage.js:27-39):
// promptTokenCount is the prompt, cache-inclusive, with cachedContentTokenCount
// the cache-read subset; the completion is candidates plus thoughts, thoughts
// being a subset of it; the total is the upstream's. When the upstream omits
// candidatesTokenCount it is derived from the total. observed is false when no
// count is positive, so an absent count is never recorded as a measured zero.
// A total the upstream left out or zeroed is the sum of the parts, which the
// pin would report as 0 beside a positive prompt. This is the only place the
// fields are read.
func GeminiUsage(raw map[string]any) (usage Usage, observed bool) {
	prompt, thoughts, total := Int(raw["promptTokenCount"]), Int(raw["thoughtsTokenCount"]), Int(raw["totalTokenCount"])
	candidates := Int(raw["candidatesTokenCount"])
	if candidates == 0 && total > 0 {
		candidates = max(total-prompt-thoughts, 0)
	}
	completion := candidates + thoughts
	if prompt <= 0 && completion <= 0 && total <= 0 {
		return Usage{}, false
	}
	if total <= 0 {
		total = prompt + completion
	}
	return Usage{Prompt: prompt, Completion: completion, Total: total, Cached: Int(raw["cachedContentTokenCount"]), Reasoning: thoughts}, true
}

// KiroUsage reads measured counts with cache-exclusive inputTokens (PARITY 6.8).
// Explicit zeros survive; invalid, conflicting or overflowing counts are unobserved.
func KiroUsage(raw map[string]any) (usage Usage, observed bool) {
	measured := false
	first := func(names ...string) (int64, bool) {
		var count int64
		seen := false
		for _, name := range names {
			value := raw[name]
			if value == nil {
				continue
			}
			var next int64
			switch v := value.(type) {
			case json.Number:
				parsed, err := v.Int64()
				if err != nil {
					// Bound exact decimal parsing before allocating powers of ten.
					if len(v) > 128 {
						return 0, false
					}
					if _, exponent, found := strings.Cut(strings.ToLower(string(v)), "e"); found {
						e, err := strconv.Atoi(exponent)
						if err != nil || e < -128 || e > 128 {
							return 0, false
						}
					}
					exact, ok := new(big.Rat).SetString(string(v))
					if !ok || !exact.IsInt() || !exact.Num().IsInt64() {
						return 0, false
					}
					parsed = exact.Num().Int64()
				}
				next = parsed
			case int64:
				next = v
			case int:
				next = int64(v)
			case float64:
				if math.IsNaN(v) || math.IsInf(v, 0) || v < 0 || v >= math.Exp2(63) || math.Trunc(v) != v {
					return 0, false
				}
				next = int64(v)
			default:
				return 0, false
			}
			if next < 0 || (seen && next != count) {
				return 0, false
			}
			count, seen, measured = next, true, true
		}
		return count, true
	}
	input, okInput := first("inputTokens")
	output, okOutput := first("outputTokens")
	read, okRead := first("cacheReadInputTokens", "cache_read_input_tokens")
	create, okCreate := first("cacheCreationInputTokens", "cache_creation_input_tokens")
	if !okInput || !okOutput || !okRead || !okCreate || !measured {
		return Usage{}, false
	}
	remaining := int64(math.MaxInt64)
	for _, count := range []int64{input, output, read, create} {
		if count > remaining {
			return Usage{}, false
		}
		remaining -= count
	}
	return ClaudeUsage(map[string]any{
		"input_tokens": input, "output_tokens": output,
		"cache_read_input_tokens": read, "cache_creation_input_tokens": create,
	}), true
}
