package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// MessagesStreamToChat is the response leg anthropic-messages>openai-chat for a
// stream (translator/response/claude-to-openai.js:19-198): the provider spoke
// Messages, the client speaks Chat. names undoes any tool-name rewriting the
// request leg did. A stream that ends before message_stop, or carries an
// upstream error event, ends with a failure terminal instead of a normal one.
func MessagesStreamToChat(ctx context.Context, src io.Reader, dst io.Writer, names *concerns.ToolNames) error {
	writer := &concerns.SSEWriter{Ctx: ctx, Dst: dst}
	state := &messagesStreamState{created: time.Now().Unix(), toolCalls: map[int64]*streamToolCall{}, serverBlock: -1}
	emit := func(delta map[string]any, finish any, usage map[string]any) error {
		return writer.Event("", state.chunk(delta, finish, usage))
	}
	err := concerns.ReadEvents(ctx, src, func(kind string, event map[string]any) (bool, error) {
		switch kind {
		case "message_start":
			message := concerns.Object(event["message"])
			state.id = concerns.Text(message["id"])
			if state.id == "" {
				state.id = fmt.Sprintf("msg_%d", time.Now().UnixMilli())
			}
			state.model = message["model"]
			state.nextToolIndex = 0
			if usage := concerns.Object(message["usage"]); usage != nil {
				state.input = concerns.Int(usage["input_tokens"])
				state.cacheRead = concerns.Int(usage["cache_read_input_tokens"])
				state.cacheCreate = concerns.Int(usage["cache_creation_input_tokens"])
				state.output = 0
				state.hasUsage = true
			}
			return false, emit(map[string]any{"role": "assistant"}, nil, nil)
		case "content_block_start":
			block := concerns.Object(event["content_block"])
			index := concerns.Int(event["index"])
			switch concerns.Text(block["type"]) {
			case "server_tool_use":
				state.serverBlock = index
			case "tool_use":
				call := &streamToolCall{index: state.nextToolIndex, id: block["id"], name: names.Original(concerns.Text(block["name"]))}
				state.nextToolIndex++
				state.toolCalls[index] = call
				return false, emit(map[string]any{"tool_calls": []any{call.start()}}, nil, nil)
			}
		case "content_block_delta":
			index := concerns.Int(event["index"])
			if index == state.serverBlock {
				return false, nil
			}
			delta := concerns.Object(event["delta"])
			switch concerns.Text(delta["type"]) {
			case "text_delta":
				if text := concerns.Text(delta["text"]); text != "" {
					return false, emit(map[string]any{"content": text}, nil, nil)
				}
			case "thinking_delta":
				// Thinking travels only in reasoning_content; "<think>" markers in
				// content would render as literal text in Chat clients.
				if text := concerns.Text(delta["thinking"]); text != "" {
					return false, emit(map[string]any{"reasoning_content": text}, nil, nil)
				}
			case "input_json_delta":
				if partial := concerns.Text(delta["partial_json"]); partial != "" {
					if call := state.toolCalls[index]; call != nil {
						call.arguments += partial
						return false, emit(map[string]any{"tool_calls": []any{call.argumentsDelta(partial)}}, nil, nil)
					}
				}
			}
		case "content_block_stop":
			index := concerns.Int(event["index"])
			if index == state.serverBlock {
				state.serverBlock = -1
				return false, nil
			}
			// A no-argument tool never receives an input_json_delta; clients still
			// need valid JSON.
			if call := state.toolCalls[index]; call != nil && call.arguments == "" {
				call.arguments = "{}"
				return false, emit(map[string]any{"tool_calls": []any{call.argumentsDelta("{}")}}, nil, nil)
			}
		case "message_delta":
			if usage := concerns.Object(event["usage"]); usage != nil {
				// Anthropic sends input and cache in message_start and only output
				// here, so a count the delta omits keeps the value captured earlier.
				if concerns.IsNumber(usage["input_tokens"]) {
					state.input = concerns.Int(usage["input_tokens"])
				}
				if concerns.IsNumber(usage["cache_read_input_tokens"]) {
					state.cacheRead = concerns.Int(usage["cache_read_input_tokens"])
				}
				if concerns.IsNumber(usage["cache_creation_input_tokens"]) {
					state.cacheCreate = concerns.Int(usage["cache_creation_input_tokens"])
				}
				state.output = concerns.Int(usage["output_tokens"])
				state.hasUsage = true
			}
			delta := concerns.Object(event["delta"])
			if reason := concerns.Text(delta["stop_reason"]); reason != "" {
				state.finish = concerns.ToOpenAIFinish(reason, concerns.FinishFormatClaude)
				if state.finish == concerns.FinishToolCalls && state.nextToolIndex == 0 {
					return false, &upstreamError{value: map[string]any{"type": "api_error", "message": "upstream stopped for tool use but sent no tool call"}}
				}
				// A refusal produces no content blocks. Surface Anthropic's own
				// explanation so the client shows why the turn is empty.
				if note := concerns.Text(concerns.Object(delta["stop_details"])["explanation"]); reason == "refusal" && note != "" {
					if err := emit(map[string]any{"content": note}, nil, nil); err != nil {
						return false, err
					}
				}
				state.finishSent = true
				return false, emit(map[string]any{}, state.finish, state.usage())
			}
		case "message_stop":
			if !state.finishSent {
				finish := state.finish
				if finish == "" {
					finish = concerns.FinishStop
					if len(state.toolCalls) > 0 {
						finish = concerns.FinishToolCalls
					}
				}
				state.finishSent = true
				if err := emit(map[string]any{}, finish, state.usage()); err != nil {
					return false, err
				}
			}
			return true, nil
		case "error":
			return false, upstreamStreamError(event)
		}
		return false, nil
	})
	if err != nil {
		return failChatStream(writer, ctx, err)
	}
	return writer.Done()
}

type upstreamError struct{ value any }

func (e *upstreamError) Error() string { return "upstream stream error" }

func upstreamStreamError(event map[string]any) error {
	failure := concerns.Object(event["error"])
	if failure == nil {
		failure = map[string]any{"type": "api_error", "message": "upstream stream error"}
	}
	return &upstreamError{value: failure}
}

// streamFailureValue is the error object a failed stream reports: the upstream's
// own when it sent one, otherwise the generic incomplete-stream error. Both
// protocols' failure terminals use it, so neither can disagree about a payload.
func streamFailureValue(cause error) map[string]any {
	var upstream *upstreamError
	if errors.As(cause, &upstream) {
		if value, ok := upstream.value.(map[string]any); ok {
			return value
		}
		return map[string]any{"type": "api_error", "message": "upstream stream error"}
	}
	return map[string]any{"code": "upstream_incomplete", "message": "upstream stream ended without a terminal event"}
}

// failChatStream ends a Chat stream with an error object and the terminal
// marker. A cancelled client gets nothing (spec 9.3).
func failChatStream(writer *concerns.SSEWriter, ctx context.Context, cause error) error {
	if ctx.Err() != nil {
		return cause
	}
	if err := writer.Event("", map[string]any{"error": streamFailureValue(cause)}); err != nil {
		return err
	}
	if err := writer.Done(); err != nil {
		return err
	}
	return cause
}

type streamToolCall struct {
	index     int
	id        any
	name      string
	arguments string
}

func (c *streamToolCall) start() map[string]any {
	return map[string]any{"index": c.index, "id": c.id, "type": "function", "function": map[string]any{"name": c.name, "arguments": ""}}
}

// argumentsDelta carries no id. The pin repeats the id on every argument chunk
// (claude-to-openai.js:96-99), but OpenAI clients and SDKs concatenate string
// fields while accumulating a stream, so a repeated id reads back as "call_1call_1".
func (c *streamToolCall) argumentsDelta(arguments string) map[string]any {
	return map[string]any{"index": c.index, "function": map[string]any{"arguments": arguments}}
}

type messagesStreamState struct {
	id            string
	model         any
	created       int64
	nextToolIndex int
	toolCalls     map[int64]*streamToolCall
	serverBlock   int64
	input         int64
	output        int64
	cacheRead     int64
	cacheCreate   int64
	hasUsage      bool
	finish        string
	finishSent    bool
}

func (s *messagesStreamState) chunk(delta map[string]any, finish any, usage map[string]any) map[string]any {
	chunk := map[string]any{
		"id": "chatcmpl-" + s.id, "object": "chat.completion.chunk", "created": s.created, "model": s.model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
	}
	if usage != nil {
		chunk["usage"] = usage
	}
	return chunk
}

// usage builds the Chat usage from everything captured so far: cache from
// message_start plus output from message_delta. The pin's message_stop branch
// (claude-to-openai.js:183-187) reports input_tokens alone as the prompt, while
// its message_delta branch (:127-170) folds cache in; both paths use the latter.
func (s *messagesStreamState) usage() map[string]any {
	if !s.hasUsage {
		return nil
	}
	return concerns.ClaudeUsage(map[string]any{
		"input_tokens": s.input, "output_tokens": s.output,
		"cache_read_input_tokens": s.cacheRead, "cache_creation_input_tokens": s.cacheCreate,
	}).OpenAI()
}

var fenceOpen = regexp.MustCompile("(?is)^\\s*```\\s*json\\s*\n?")
var fenceClose = regexp.MustCompile("(?is)\n?\\s*```\\s*$")

// MessagesResponseToChat converts a non-stream Messages body to a Chat
// completion (handlers/chatCore/nonStreamingHandler.js:257-311, plus the
// handler's finish-reason and reasoning clean-up at :397-405 and :428-434). A
// body already in Chat shape is returned unchanged, as the pin does.
func MessagesResponseToChat(body []byte, names *concerns.ToolNames) ([]byte, error) {
	response, err := concerns.WireObject(body)
	if err != nil {
		return nil, err
	}
	if response["choices"] != nil {
		return body, nil
	}
	if content := response["content"]; content != nil && concerns.Items(content) == nil {
		return body, nil
	}
	var text, thinking strings.Builder
	var calls []any
	for _, raw := range concerns.Items(response["content"]) {
		block := concerns.Object(raw)
		switch concerns.Text(block["type"]) {
		case "text":
			// Some gateways wrap JSON in a markdown fence; the pin strips it.
			value := fenceClose.ReplaceAllString(fenceOpen.ReplaceAllString(concerns.Text(block["text"]), ""), "")
			text.WriteString(value)
		case "thinking":
			thinking.WriteString(concerns.Text(block["thinking"]))
		case "tool_use":
			input := block["input"]
			if !concerns.Truthy(input) {
				input = map[string]any{}
			}
			calls = append(calls, map[string]any{"id": block["id"], "type": "function", "function": map[string]any{
				"name": names.Original(concerns.Text(block["name"])), "arguments": concerns.JSONText(input)}})
		}
	}
	message := map[string]any{"role": "assistant"}
	if text.Len() > 0 {
		message["content"] = text.String()
	}
	if thinking.Len() > 0 {
		message["reasoning_content"] = thinking.String()
	}
	if len(calls) > 0 {
		message["tool_calls"] = calls
	}
	if message["content"] == nil && message["tool_calls"] == nil {
		message["content"] = ""
	}
	reason := concerns.Text(response["stop_reason"])
	if reason == "" {
		reason = "end_turn"
	}
	finish := concerns.ToOpenAIFinish(reason, concerns.FinishFormatClaude)
	if len(calls) > 0 {
		finish = concerns.FinishToolCalls
	} else if finish == concerns.FinishToolCalls {
		return nil, errors.New("upstream stopped for tool use but sent no tool call")
	}
	// The pin strips reasoning only when there is content to show instead.
	if message["content"] != "" && message["content"] != nil {
		delete(message, "reasoning_content")
	}
	id := concerns.Text(response["id"])
	if id == "" {
		id = fmt.Sprint(time.Now().UnixMilli())
	}
	model := response["model"]
	if !concerns.Truthy(model) {
		model = "claude"
	}
	result := map[string]any{
		"id": "chatcmpl-" + id, "object": "chat.completion", "created": time.Now().Unix(), "model": model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
	}
	if usage := concerns.Object(response["usage"]); usage != nil {
		result["usage"] = concerns.ClaudeUsage(usage).OpenAI()
	}
	return json.Marshal(result)
}
