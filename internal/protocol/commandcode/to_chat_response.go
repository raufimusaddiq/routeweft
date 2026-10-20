package commandcode

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

const defaultModel = "commandcode"

// ignored are the AI SDK events that carry no client-visible content
// (commandcode-to-openai.js:174-176), plus tool-input-end, which only closes a
// tool input already streamed.
var ignored = map[string]bool{
	"start": true, "start-step": true, "reasoning-start": true, "reasoning-end": true,
	"text-start": true, "text-end": true, "tool-input-end": true, "provider-metadata": true, "message-metadata": true,
}

// contentKeys are the payload keys by which an event of a type this leg does not
// know could be carrying something the client should see.
var contentKeys = []string{"text", "delta", "textDelta", "inputTextDelta", "input", "toolName", "toolCallId"}

// converter turns CommandCode events into Chat chunks. It is the one place the
// event grammar is read, for both the streamed and the buffered leg.
type converter struct {
	model   string
	id      string
	created int64
	unknown func(string)

	started      bool // a chunk has been emitted, so the role has been sent
	calls        []*call
	byID         map[string]*call
	finishReason string
	usage        map[string]any
	pending      map[string]any // the event whose ids the next open aliases
}

// call is one tool call: the id the client was shown and what has been sent.
type call struct {
	index     int
	id        string
	name      string
	arguments int // bytes of arguments sent
	done      bool
}

func newConverter(model string, unknown func(string)) *converter {
	return &converter{model: model, id: fmt.Sprintf("chatcmpl-%d", time.Now().UnixMilli()), created: time.Now().Unix(), unknown: unknown, byID: map[string]*call{}}
}

func (c *converter) chunk(delta map[string]any, finish any) map[string]any {
	if !c.started && finish == nil {
		delta["role"] = "assistant"
	}
	if finish == nil {
		c.started = true
	}
	model := c.model
	if model == "" {
		model = defaultModel
	}
	return map[string]any{
		"id": c.id, "object": "chat.completion.chunk", "created": c.created, "model": model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
	}
}

func malformed(kind, field string) error {
	return fmt.Errorf("upstream %s event has a %s that is not text", kind, field)
}

// textField reads a text payload that may be absent, but not of another type.
func textField(event map[string]any, kind string, keys ...string) (string, error) {
	for _, key := range keys {
		switch value := event[key].(type) {
		case nil:
		case string:
			if value != "" {
				return value, nil
			}
		default:
			return "", malformed(kind, key)
		}
	}
	return "", nil
}

// handle converts one event. done is true once the terminal chunk was produced.
func (c *converter) handle(event map[string]any) (chunks []map[string]any, done bool, err error) {
	kind := concerns.Text(event["type"])
	switch kind {
	case "text-delta":
		text, err := textField(event, kind, "text", "delta")
		if err != nil || text == "" {
			return nil, false, err
		}
		return []map[string]any{c.chunk(map[string]any{"content": text}, nil)}, false, nil
	case "reasoning-delta":
		// The pin reads `text` only (:91); the AI SDK also names it `delta`.
		text, err := textField(event, kind, "text", "delta")
		if err != nil || text == "" {
			return nil, false, err
		}
		return []map[string]any{c.chunk(map[string]any{"reasoning_content": text}, nil)}, false, nil
	case "tool-input-start":
		// One id space (PARITY V36), resolved by ids/lookup for every branch below.
		if c.lookup(event) != nil {
			return nil, false, nil
		}
		return []map[string]any{c.open(c.register(event, c.primaryID(event, concerns.Text(event["toolName"]))), concerns.Text(event["toolName"]), "")}, false, nil
	case "tool-input-delta":
		text, err := textField(event, kind, "delta", "inputTextDelta")
		if err != nil || text == "" {
			return nil, false, err
		}
		target := c.lookup(event)
		if target == nil {
			// The pin ignores it (:120-123), which loses part of a call's arguments.
			return nil, false, fmt.Errorf("upstream sent tool input for an unknown call %q", c.primaryID(event, ""))
		}
		return []map[string]any{c.arguments(target, text)}, false, nil
	case "tool-call":
		name := concerns.Text(event["toolName"])
		input, err := consolidated(event["input"])
		if err != nil {
			return nil, false, err
		}
		target := c.lookup(event)
		if target == nil && len(ids(event)) > 0 {
			// A tool-call whose ids match no start is the same call under another
			// name, not a second call (PARITY V36): the oldest call started under
			// that tool name that is not yet consolidated.
			for _, open := range c.calls {
				if !open.done && open.name == name {
					target = open
					c.alias(event, open)
					break
				}
			}
		}
		if target == nil {
			chunk := c.open(c.register(event, c.primaryID(event, name)), name, input)
			c.calls[len(c.calls)-1].done = true
			return []map[string]any{chunk}, false, nil
		}
		c.alias(event, target)
		target.done = true
		// Arguments already streamed stand: the consolidated input is not compared
		// with them, so a client that trusts `input` over the deltas would see
		// the streamed fragment, which is what the upstream sent first.
		if target.arguments == 0 && input != "" {
			// The pin drops the consolidated input when only the start was seen
			// (:136), leaving the call with no arguments.
			return []map[string]any{c.arguments(target, input)}, false, nil
		}
		return nil, false, nil
	case "finish-step":
		if reason := event["finishReason"]; reason != nil {
			text, ok := reason.(string)
			if !ok {
				return nil, false, malformed(kind, "finishReason")
			}
			c.finishReason = concerns.ToOpenAIFinish(text, concerns.FinishFormatCommandCode)
		}
		if usage := concerns.Object(event["usage"]); usage != nil {
			c.usage = usage
		}
		return nil, false, nil
	case "finish":
		reason := c.finishReason
		if reason == "" {
			text, _ := event["finishReason"].(string)
			if text == "" {
				text = "stop"
			}
			reason = concerns.ToOpenAIFinish(text, concerns.FinishFormatCommandCode)
		}
		// A finish for tool calls with no call behind it is an error, not a
		// success the client acts on (the pin does not check, :153-166).
		reason, err := concerns.ResolveFinish(reason, len(c.calls), false)
		if err != nil {
			return nil, false, err
		}
		final := c.chunk(map[string]any{}, reason)
		raw := concerns.Object(event["totalUsage"])
		if raw == nil {
			raw = c.usage
		}
		if usage, extra, observed := concerns.CommandCodeUsage(raw); observed {
			final["usage"] = chatUsage(usage, extra)
		}
		return []map[string]any{final}, true, nil
	case "error":
		return nil, false, upstreamError(event)
	}
	if ignored[kind] {
		return nil, false, nil
	}
	if c.unknown != nil {
		c.unknown(kind)
	}
	// An event type this leg has never seen is tolerated until it could be
	// carrying content; then the client must not be told the answer is complete.
	for _, key := range contentKeys {
		if concerns.Present(event[key]) {
			return nil, false, fmt.Errorf("upstream sent a %q event with %s this leg cannot convert", kind, key)
		}
	}
	return nil, false, nil
}

// ids are the call ids an event carries: toolCallId, then id. Every branch that
// resolves a call goes through these helpers, so no branch can read the fields
// in another order (PARITY V36).
func ids(event map[string]any) []string {
	var found []string
	for _, key := range []string{"toolCallId", "id"} {
		if id := concerns.Text(event[key]); id != "" && (len(found) == 0 || found[0] != id) {
			found = append(found, id)
		}
	}
	return found
}

// lookup finds the call any of the event's ids names.
func (c *converter) lookup(event map[string]any) *call {
	for _, id := range ids(event) {
		if found := c.byID[id]; found != nil {
			return found
		}
	}
	return nil
}

// primaryID is the id a new call is opened under: toolCallId, else id, else a
// generated one.
func (c *converter) primaryID(event map[string]any, name string) string {
	if found := ids(event); len(found) > 0 {
		return found[0]
	}
	return concerns.GenerateToolCallID(0, len(c.calls), name)
}

// register records every id of the event as naming the call that open is about
// to create, and returns the primary id.
func (c *converter) register(event map[string]any, primary string) string {
	c.pending = event
	return primary
}

// alias makes every id of the event name an existing call. An id that already
// names another call is left alone: rebinding it would move that call's later
// events onto this one.
func (c *converter) alias(event map[string]any, target *call) {
	for _, id := range ids(event) {
		if c.byID[id] == nil {
			c.byID[id] = target
		}
	}
}

func (c *converter) open(id, name, arguments string) map[string]any {
	opened := &call{index: len(c.calls), id: id, name: name}
	c.calls = append(c.calls, opened)
	c.byID[id] = opened
	opened.arguments = len(arguments)
	if c.pending != nil {
		c.alias(c.pending, opened)
		c.pending = nil
	}
	return c.chunk(map[string]any{"tool_calls": []any{map[string]any{
		"index": opened.index, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": arguments},
	}}}, nil)
}

func (c *converter) arguments(target *call, text string) map[string]any {
	target.arguments += len(text)
	// The id is sent once, with the call's first chunk (PARITY V1).
	return c.chunk(map[string]any{"tool_calls": []any{map[string]any{
		"index": target.index, "function": map[string]any{"arguments": text},
	}}}, nil)
}

// consolidated is the `input` of a tool-call as Chat arguments: the pin keeps a
// string and encodes anything else, null as {} (:139).
func consolidated(input any) (string, error) {
	switch v := input.(type) {
	case string:
		return v, nil
	case nil:
		return "{}", nil
	}
	encoded, err := json.Marshal(input)
	return string(encoded), err
}

// chatUsage is the Chat usage object. cachedInputTokens and reasoningTokens are
// not put in prompt_tokens_details or completion_tokens_details, whose meaning
// is "a subset of the prompt/output": it is not established that inputTokens is
// cache-inclusive. They travel beside the counts, untouched (PARITY V37).
func chatUsage(usage concerns.Usage, extra concerns.CommandCodeObservations) map[string]any {
	result := usage.OpenAI()
	observed := map[string]any{}
	if extra.CachedInput > 0 {
		observed["cached_input_tokens"] = extra.CachedInput
	}
	if extra.Reasoning > 0 {
		observed["reasoning_tokens"] = extra.Reasoning
	}
	if len(observed) > 0 {
		result["commandcode_observed"] = observed
	}
	return result
}

// upstreamError is the error event (:167-173), shaped like the pin's own
// message so the client sees the same text.
func upstreamError(event map[string]any) error {
	value := event["error"]
	if value == nil {
		value = event["message"]
	}
	text, isText := value.(string)
	if !isText {
		if value == nil {
			text = "unknown"
		} else {
			text = concerns.JSONText(value)
		}
	}
	return &concerns.StreamFailure{Value: map[string]any{"type": "api_error", "message": "[CommandCode error: " + text + "]"}}
}

// CommandCodeStreamToChat is the streamed response leg commandcode>openai-chat
// (commandcode-to-openai.js:52-181, framed by executors/commandcode.js:274-324).
// CommandCode streams one AI SDK event per line. model is the request's model.
// unknown, when not nil, is called with the type of every event this leg does
// not know. A stream that ends before `finish`, or reports an error event, ends
// with a failure terminal where the pin ends it as a success (PARITY V28).
func CommandCodeStreamToChat(ctx context.Context, src io.Reader, dst io.Writer, model string, unknown func(string)) error {
	writer := &concerns.SSEWriter{Ctx: ctx, Dst: dst}
	state := newConverter(model, unknown)
	err := concerns.ReadLinesLenient(ctx, src, func(event map[string]any) (bool, error) {
		chunks, done, err := state.handle(event)
		if err != nil {
			return false, err
		}
		for _, chunk := range chunks {
			if err := writer.Event("", chunk); err != nil {
				return false, err
			}
		}
		return done, nil
	})
	if err != nil {
		_ = concerns.FailChatStream(ctx, writer, err)
		return err
	}
	return writer.Done()
}

// CommandCodeResponseToChat is the buffered leg: the complete NDJSON of a
// request the upstream streamed (it always does, executors/commandcode.js:24),
// aggregated into one chat.completion.
func CommandCodeResponseToChat(body []byte, model string, unknown func(string)) ([]byte, error) {
	state := newConverter(model, unknown)
	var content, reasoning string
	arguments := map[int]string{}
	var finish string
	var usage map[string]any
	var finished bool
	err := concerns.ReadLinesLenient(context.Background(), bytes.NewReader(body), func(event map[string]any) (bool, error) {
		chunks, done, err := state.handle(event)
		if err != nil {
			return false, err
		}
		for _, chunk := range chunks {
			choice := concerns.Object(concerns.Items(chunk["choices"])[0])
			delta := concerns.Object(choice["delta"])
			content += concerns.Text(delta["content"])
			reasoning += concerns.Text(delta["reasoning_content"])
			for _, raw := range concerns.Items(delta["tool_calls"]) {
				piece := concerns.Object(raw)
				index := int(concerns.Int(piece["index"]))
				arguments[index] += concerns.Text(concerns.Object(piece["function"])["arguments"])
			}
			if reason := concerns.Text(choice["finish_reason"]); reason != "" {
				finish = reason
			}
			if value := concerns.Object(chunk["usage"]); value != nil {
				usage = value
			}
		}
		finished = done
		return done, nil
	})
	if err != nil {
		return nil, err
	}
	if !finished {
		return nil, errors.New("upstream stream ended without a finish event")
	}
	message := map[string]any{"role": "assistant"}
	if content != "" || len(state.calls) == 0 {
		message["content"] = content
	}
	if reasoning != "" {
		message["reasoning_content"] = reasoning
	}
	if len(state.calls) > 0 {
		calls := make([]any, 0, len(state.calls))
		for _, made := range state.calls {
			args := arguments[made.index]
			if args == "" {
				args = "{}"
			}
			calls = append(calls, map[string]any{"id": made.id, "type": "function", "function": map[string]any{"name": made.name, "arguments": args}})
		}
		message["tool_calls"] = calls
	}
	result := map[string]any{
		"id": state.id, "object": "chat.completion", "created": state.created, "model": firstNonEmpty(model, defaultModel),
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}},
	}
	if usage != nil {
		result["usage"] = usage
	}
	return json.Marshal(result)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
