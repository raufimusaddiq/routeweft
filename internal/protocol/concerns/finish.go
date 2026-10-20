package concerns

import "strings"

// OpenAI finish_reason values: the hub vocabulary (schema/finishReasons.js).
const (
	FinishStop          = "stop"
	FinishLength        = "length"
	FinishToolCalls     = "tool_calls"
	FinishContentFilter = "content_filter"
)

// Format names accepted by ToOpenAIFinish and FromOpenAIFinish, the oracle's
// own switch keys (concerns/finishReason.js).
const (
	FinishFormatClaude      = "claude"
	FinishFormatCommandCode = "commandcode"
	FinishFormatGemini      = "gemini"
	FinishFormatKiro        = "kiro"
	FinishFormatOllama      = "ollama"
)

// ToOpenAIFinish maps an upstream stop reason to an OpenAI finish_reason
// (concerns/finishReason.js:6-52). An unknown reason of a format with its own
// table is "stop"; a format without one passes the reason through.
func ToOpenAIFinish(reason, format string) string {
	switch format {
	case FinishFormatClaude:
		switch reason {
		case "max_tokens":
			return FinishLength
		case "tool_use":
			return FinishToolCalls
		case "refusal":
			return FinishContentFilter
		}
		return FinishStop
	case FinishFormatCommandCode:
		switch reason {
		case "stop", "error":
			return FinishStop
		case "length":
			return FinishLength
		case "tool-calls", "tool_use":
			return FinishToolCalls
		case "content-filter":
			return FinishContentFilter
		}
		if reason == "" {
			return FinishStop
		}
		return reason
	case FinishFormatGemini:
		switch strings.ToUpper(reason) {
		case "MAX_TOKENS":
			return FinishLength
		case "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT":
			return FinishContentFilter
		}
		return FinishStop
	case FinishFormatKiro, FinishFormatOllama:
		switch reason {
		case "tool_calls", "tool_use":
			return FinishToolCalls
		case "length", "max_tokens":
			return FinishLength
		}
		return FinishStop
	}
	if reason == "" {
		return FinishStop
	}
	return reason
}

// FromOpenAIFinish maps an OpenAI finish_reason to an upstream stop reason
// (concerns/finishReason.js:55-68). Only Claude has its own table.
func FromOpenAIFinish(reason, format string) string {
	if format != FinishFormatClaude {
		return reason
	}
	switch reason {
	case FinishLength:
		return "max_tokens"
	case FinishToolCalls:
		return "tool_use"
	case FinishContentFilter:
		return "refusal"
	}
	return "end_turn"
}
