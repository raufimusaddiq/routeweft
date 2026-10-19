package promptcache

import "encoding/json"

// Usage is the token accounting understood by the retained Anthropic Messages
// response protocol. Missing upstream fields remain zero; no estimates are
// substituted for reported cache-read/cache-create usage.
type Usage struct {
	InputTokens      int64 `json:"input_tokens"`
	OutputTokens     int64 `json:"output_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

// ParseAnthropicUsage extracts response usage without changing the response
// bytes. Malformed or absent usage is reported as not present, not an error.
func ParseAnthropicUsage(body []byte) (Usage, bool) {
	var response struct {
		Usage   *wireUsage `json:"usage"`
		Message *struct {
			Usage *wireUsage `json:"usage"`
		} `json:"message"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return Usage{}, false
	}
	usage := response.Usage
	if usage == nil && response.Message != nil {
		usage = response.Message.Usage
	}
	if usage == nil {
		return Usage{}, false
	}
	return Usage{InputTokens: usage.Input, OutputTokens: usage.Output, CacheReadTokens: usage.CacheRead, CacheWriteTokens: usage.CacheWrite}, true
}

type wireUsage struct {
	Input      int64 `json:"input_tokens"`
	Output     int64 `json:"output_tokens"`
	CacheRead  int64 `json:"cache_read_input_tokens"`
	CacheWrite int64 `json:"cache_creation_input_tokens"`
}

// Merge folds non-zero fields of a later usage report into u. Anthropic streams
// input/cache counts on message_start and output counts on message_delta, so a
// streamed response yields one merged record.
func (u Usage) Merge(later Usage) Usage {
	if later.InputTokens != 0 {
		u.InputTokens = later.InputTokens
	}
	if later.OutputTokens != 0 {
		u.OutputTokens = later.OutputTokens
	}
	if later.CacheReadTokens != 0 {
		u.CacheReadTokens = later.CacheReadTokens
	}
	if later.CacheWriteTokens != 0 {
		u.CacheWriteTokens = later.CacheWriteTokens
	}
	return u
}
