package concerns

import (
	"context"
	"errors"
)

// StreamFailure is a failure the upstream itself reported inside a stream. Value
// is the error object a Chat client is shown.
type StreamFailure struct{ Value map[string]any }

func (e *StreamFailure) Error() string { return "upstream stream error" }

// FailChatStream ends a Chat stream that broke with an error object and the
// terminal marker, and returns the cause. The object is the upstream's own when
// it sent one (StreamFailure), otherwise the generic incomplete-stream error. A
// cancelled client gets nothing (spec 9.3), and a failing write never replaces
// the cause.
func FailChatStream(ctx context.Context, writer *SSEWriter, cause error) error {
	if ctx.Err() != nil {
		return cause
	}
	value := map[string]any{"code": "upstream_incomplete", "message": "upstream stream ended without a terminal event"}
	var upstream *StreamFailure
	if errors.As(cause, &upstream) && upstream.Value != nil {
		value = upstream.Value
	}
	if err := writer.Event("", map[string]any{"error": value}); err != nil {
		return cause
	}
	_ = writer.Done()
	return cause
}

// UncarriableChat names what a Chat request asks for that a provider wire that
// returns one completion of text and tool calls cannot do, or "" when there is
// none. Refusing is the rule: answering without it is a wrong answer.
func UncarriableChat(body map[string]any) string {
	if n := Int(body["n"]); n > 1 {
		return "n>1: one completion is returned"
	}
	if Truthy(body["logprobs"]) {
		return "logprobs: none are reported"
	}
	for _, modality := range Items(body["modalities"]) {
		if Text(modality) == "audio" {
			return "audio output"
		}
	}
	for _, field := range []string{"audio", "functions", "function_call"} {
		if Truthy(body[field]) {
			return "request field \"" + field + "\" (use tools)"
		}
	}
	return ""
}
