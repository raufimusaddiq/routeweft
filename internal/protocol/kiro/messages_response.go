package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// The Messages response legs read the same EventStream, through the same machine,
// as the Chat legs, so the two cannot disagree about what an event means: which
// events are content, which end the answer, which fail it. What they add is the
// Messages framing the pin writes in translator/response/kiro-to-claude.js:57-285.
// The pin's executor first turns the EventStream into Chat chunks
// (executors/kiro.js:629-1190) and that file turns the chunks into Messages
// events, so this is the executor transform followed by it with no Chat chunk
// built in between.

// messageStopReason is kiro-to-claude.js:32-43 over the finish reasons the machine
// resolves. The pin maps a reason it does not know to end_turn; the machine
// fails every reason it does not know, so only these three arrive.
func messageStopReason(finish string) string {
	switch finish {
	case concerns.FinishToolCalls:
		return "tool_use"
	case concerns.FinishLength:
		return "max_tokens"
	}
	return "end_turn"
}

// messageID is the Chat completion id without its prefix, as kiro-to-claude.js:100-102.
func messageID(chatID string) string {
	return "msg_" + strings.TrimPrefix(chatID, "chatcmpl-")
}

func messageUsage(usage map[string]any) map[string]any {
	if usage == nil {
		return map[string]any{"input_tokens": 0, "output_tokens": 0}
	}
	return concerns.ClaudeUsageFromOpenAI(usage)
}

// messageBlocks folds the machine's deltas into content blocks, merging runs of
// one kind. A stream sends each block as it opens; a buffered answer is the same
// blocks, whole.
type messageBlocks struct {
	open  func(index int, block map[string]any) error
	delta func(index int, delta map[string]any) error
	close func(index int) error
	kind  string // "thinking", "text" or "" when no block is open
	index int
	next  int
	tool  int
}

func (b *messageBlocks) stop() error {
	if b.kind == "" {
		return nil
	}
	b.kind = ""
	return b.close(b.index)
}

func (b *messageBlocks) run(kind string, block map[string]any, delta map[string]any) error {
	if b.kind != kind {
		if err := b.stop(); err != nil {
			return err
		}
		b.kind, b.index = kind, b.next
		b.next++
		if err := b.open(b.index, block); err != nil {
			return err
		}
	}
	return b.delta(b.index, delta)
}

// accept routes one Chat delta from the machine.
func (b *messageBlocks) accept(delta map[string]any) error {
	if text := concerns.Text(delta["reasoning_content"]); text != "" {
		return b.run("thinking", map[string]any{"type": "thinking", "thinking": ""}, map[string]any{"type": "thinking_delta", "thinking": text})
	}
	if text := concerns.Text(delta["content"]); text != "" {
		return b.run("text", map[string]any{"type": "text", "text": ""}, map[string]any{"type": "text_delta", "text": text})
	}
	for _, raw := range concerns.Items(delta["tool_calls"]) {
		call := concerns.Object(raw)
		function := concerns.Object(call["function"])
		if call["id"] != nil {
			if err := b.stop(); err != nil {
				return err
			}
			index := b.next
			b.next++
			b.tool = index
			if err := b.open(index, map[string]any{"type": "tool_use", "id": call["id"], "name": function["name"], "input": map[string]any{}}); err != nil {
				return err
			}
			continue
		}
		arguments := concerns.Text(function["arguments"])
		if arguments == "" {
			arguments = "{}"
		}
		if err := b.delta(b.tool, map[string]any{"type": "input_json_delta", "partial_json": arguments}); err != nil {
			return err
		}
		if err := b.close(b.tool); err != nil {
			return err
		}
	}
	return nil
}

// failMessages ends a Messages stream with an error event. A cancelled client
// gets nothing (spec 9.3), and a failing write never replaces the cause.
func failMessages(ctx context.Context, writer *concerns.SSEWriter, cause error) error {
	if ctx.Err() != nil {
		return cause
	}
	value := map[string]any{"type": "api_error", "message": "upstream stream ended without a terminal event"}
	var upstream *concerns.StreamFailure
	if errors.As(cause, &upstream) && upstream.Value != nil {
		value = map[string]any{"type": "api_error", "message": concerns.Text(upstream.Value["message"])}
		if kind := concerns.Text(upstream.Value["type"]); kind != "" {
			value["type"] = kind
		}
	}
	_ = writer.Event("error", map[string]any{"type": "error", "error": value})
	return cause
}

// KiroStreamToMessages is the streamed response leg kiro>anthropic-messages.
// src is the raw EventStream body. Text and reasoning reach the client as frames
// arrive; tool calls and the closing events wait for the reader to confirm the
// body is whole, so a stream that was cut short, corrupt after its last event, or
// that never said it had stopped ends with an error event, never a success.
func KiroStreamToMessages(ctx context.Context, src io.Reader, dst io.Writer, options ResponseOptions) error {
	writer := &concerns.SSEWriter{Ctx: ctx, Dst: dst}
	var state *machine
	started := false
	start := func() error {
		if started {
			return nil
		}
		started = true
		return writer.Event("message_start", map[string]any{"type": "message_start", "message": map[string]any{
			"id": messageID(state.id), "type": "message", "role": "assistant", "model": state.model, "content": []any{},
			"stop_reason": nil, "stop_sequence": nil, "usage": map[string]any{"input_tokens": 0, "output_tokens": 0}}})
	}
	blocks := &messageBlocks{
		open: func(index int, block map[string]any) error {
			return writer.Event("content_block_start", map[string]any{"type": "content_block_start", "index": index, "content_block": block})
		},
		delta: func(index int, delta map[string]any) error {
			return writer.Event("content_block_delta", map[string]any{"type": "content_block_delta", "index": index, "delta": delta})
		},
		close: func(index int) error {
			return writer.Event("content_block_stop", map[string]any{"type": "content_block_stop", "index": index})
		},
	}
	state = newMachine(options, func(delta map[string]any) error {
		if err := start(); err != nil {
			return err
		}
		return blocks.accept(delta)
	})
	reason, err := state.run(ctx, src)
	if err == nil {
		if err = start(); err == nil {
			err = blocks.stop()
		}
		if err == nil && blocks.next == 0 {
			// An answer with no content is one empty text block, as the buffered body.
			if err = blocks.open(0, map[string]any{"type": "text", "text": ""}); err == nil {
				err = blocks.close(0)
			}
		}
	}
	if err != nil {
		return failMessages(ctx, writer, err)
	}
	if err := writer.Event("message_delta", map[string]any{"type": "message_delta",
		"delta": map[string]any{"stop_reason": messageStopReason(reason), "stop_sequence": nil}, "usage": messageUsage(state.usage)}); err != nil {
		return err
	}
	return writer.Event("message_stop", map[string]any{"type": "message_stop"})
}

// KiroResponseToMessages is the buffered response leg kiro>anthropic-messages
// for a client that did not ask for a stream. Kiro only streams, so body is the
// whole EventStream; it runs the same reader and the same machine as the stream.
func KiroResponseToMessages(body []byte, options ResponseOptions) ([]byte, error) {
	var content []any
	var current map[string]any
	var text strings.Builder
	var tool map[string]any
	flush := func() {
		if current != nil {
			key := "text"
			if current["type"] == "thinking" {
				key = "thinking"
			}
			current[key] = text.String()
			text.Reset()
			current = nil
		}
	}
	blocks := &messageBlocks{
		open: func(_ int, block map[string]any) error {
			flush()
			content = append(content, block)
			if block["type"] == "tool_use" {
				tool = block
			} else {
				current = block
			}
			return nil
		},
		delta: func(_ int, delta map[string]any) error {
			switch delta["type"] {
			case "text_delta":
				text.WriteString(concerns.Text(delta["text"]))
			case "thinking_delta":
				text.WriteString(concerns.Text(delta["thinking"]))
			case "input_json_delta":
				tool["input"] = concerns.ToolInput(concerns.Text(delta["partial_json"]))
			}
			return nil
		},
		close: func(int) error { flush(); return nil },
	}
	state := newMachine(options, blocks.accept)
	reason, err := state.run(context.Background(), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	if err := blocks.stop(); err != nil {
		return nil, err
	}
	if len(content) == 0 {
		content = []any{map[string]any{"type": "text", "text": ""}}
	}
	return json.Marshal(map[string]any{
		"id": messageID(state.id), "type": "message", "role": "assistant", "model": state.model, "content": content,
		"stop_reason": messageStopReason(reason), "stop_sequence": nil, "usage": messageUsage(state.usage),
	})
}
