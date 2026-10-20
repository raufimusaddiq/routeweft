package openai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/google/uuid"
	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// MaxTranslatedResponseBytes bounds buffered output and the total translated
// stream. Native streaming remains byte-preserving and does not use this limit.
// shortcut: 32 MiB translated output; revisit with measured long-output workloads.
const MaxTranslatedResponseBytes = concerns.MaxTranslatedResponseBytes

type eventWriter struct {
	ctx context.Context
	dst io.Writer
	seq int
}

func (w *eventWriter) write(event string, value map[string]any) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	if event != "" {
		value["type"], value["sequence_number"] = event, w.seq
		w.seq++
	}
	payload, err := json.Marshal(value)
	if err != nil {
		return err
	}
	var prefix string
	if event != "" {
		prefix = "event: " + event + "\n"
	}
	if _, err := fmt.Fprintf(w.dst, "%sdata: %s\n\n", prefix, payload); err != nil {
		return err
	}
	if flusher, ok := w.dst.(interface{ Flush() }); ok {
		flusher.Flush()
	}
	return nil
}

func (w *eventWriter) done() error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	_, err := io.WriteString(w.dst, "data: [DONE]\n\n")
	if flusher, ok := w.dst.(interface{ Flush() }); ok {
		flusher.Flush()
	}
	return err
}

// readEvents handles SSE framing (concerns.ReadEvents), shared with the other
// conversion legs.
func readEvents(ctx context.Context, src io.Reader, consume func(string, map[string]any) (bool, error)) error {
	return concerns.ReadEvents(ctx, src, consume)
}

func streamFailure(writer *eventWriter, responses bool, failure any) error {
	if failure == nil {
		failure = map[string]any{"code": "upstream_incomplete", "message": "upstream stream ended without a terminal event"}
	}
	if responses {
		return writer.write("response.failed", map[string]any{"response": map[string]any{"status": "failed", "error": failure}})
	}
	if err := writer.write("", map[string]any{"error": failure}); err != nil {
		return err
	}
	return writer.done()
}

type translatedItem struct {
	index     int
	item      map[string]any
	value     strings.Builder
	announced bool
	sent      int
}

// ChatStreamToResponses converts deltas incrementally, retaining only the
// bounded output needed by response.completed and SDK final-response helpers.
func ChatStreamToResponses(ctx context.Context, src io.Reader, dst io.Writer, model string, customTools map[string]bool) error {
	writer := &eventWriter{ctx: ctx, dst: dst, seq: 1}
	id := "resp_" + uuid.NewString()
	var created any = 0
	started, terminal := false, false
	var finish string
	var usage map[string]any
	var output []*translatedItem
	byKey := map[string]*translatedItem{}
	ensure := func(key, kind string) *translatedItem {
		if item := byKey[key]; item != nil {
			return item
		}
		item := &translatedItem{index: len(output), item: map[string]any{"id": fmt.Sprintf("item_%s_%d", id, len(output)), "type": kind}}
		if kind == "function_call" {
			item.item["arguments"] = ""
		}
		byKey[key] = item
		output = append(output, item)
		return item
	}
	announce := func(item *translatedItem) error {
		if item.announced {
			return nil
		}
		item.announced = true
		if item.item["type"] != "reasoning" {
			item.item["status"] = "in_progress"
		}
		if err := writer.write("response.output_item.added", map[string]any{"output_index": item.index, "item": item.item}); err != nil {
			return err
		}
		if item.item["type"] == "message" {
			return writer.write("response.content_part.added", map[string]any{"output_index": item.index, "item_id": item.item["id"], "content_index": 0, "part": items(item.item["content"])[0]})
		}
		if item.item["type"] == "reasoning" {
			return writer.write("response.reasoning_summary_part.added", map[string]any{"output_index": item.index, "item_id": item.item["id"], "summary_index": 0, "part": map[string]any{"type": "summary_text", "text": ""}})
		}
		return nil
	}
	emitText := func(key, kind, delta string) error {
		if delta == "" {
			return nil
		}
		item := ensure(key, "message")
		if kind == "reasoning_summary_text" {
			item.item["type"], item.item["summary"] = "reasoning", []any{}
		} else {
			part := map[string]any{"type": kind, "text": ""}
			if kind == "refusal" {
				part = map[string]any{"type": "refusal", "refusal": ""}
			}
			item.item["role"], item.item["content"] = "assistant", []any{part}
		}
		if err := announce(item); err != nil {
			return err
		}
		item.value.WriteString(delta)
		value := map[string]any{"output_index": item.index, "item_id": item.item["id"], "delta": delta}
		if kind == "reasoning_summary_text" {
			value["summary_index"] = 0
		} else {
			value["content_index"] = 0
		}
		return writer.write("response."+kind+".delta", value)
	}
	err := readEvents(ctx, src, func(kind string, chunk map[string]any) (bool, error) {
		if kind == "[DONE]" {
			if !started || finish == "" {
				return false, io.ErrUnexpectedEOF
			}
			completed := make([]any, 0, len(output))
			for _, item := range output {
				value := item.value.String()
				if (item.item["type"] == "function_call" || item.item["type"] == "custom_tool_call") && (text(item.item["name"]) == "" || text(item.item["call_id"]) == "") {
					return false, errors.New("upstream tool call lacks an id or name")
				}
				if item.item["type"] == "function_call" && customTools[text(item.item["name"])] {
					item.item["type"] = "custom_tool_call"
				}
				if err := announce(item); err != nil {
					return false, err
				}
				payload := map[string]any{"output_index": item.index, "item_id": item.item["id"]}
				switch item.item["type"] {
				case "message":
					part := object(items(item.item["content"])[0])
					field := "text"
					event := "output_text"
					if part["type"] == "refusal" {
						field, event = "refusal", "refusal"
					}
					part[field], payload[field], payload["content_index"] = value, value, 0
					if err := writer.write("response."+event+".done", payload); err != nil {
						return false, err
					}
					if err := writer.write("response.content_part.done", map[string]any{"output_index": item.index, "item_id": item.item["id"], "content_index": 0, "part": part}); err != nil {
						return false, err
					}
				case "reasoning":
					part := map[string]any{"type": "summary_text", "text": value}
					item.item["summary"] = []any{part}
					payload["text"], payload["summary_index"] = value, 0
					if err := writer.write("response.reasoning_summary_text.done", payload); err != nil {
						return false, err
					}
					if err := writer.write("response.reasoning_summary_part.done", map[string]any{"output_index": item.index, "item_id": item.item["id"], "summary_index": 0, "part": part}); err != nil {
						return false, err
					}
				case "function_call":
					if value == "" {
						value = "{}"
					}
					item.item["arguments"], payload["arguments"] = value, value
					if err := writer.write("response.function_call_arguments.done", payload); err != nil {
						return false, err
					}
				case "custom_tool_call":
					value = customInput(value)
					item.item["input"], payload["input"] = value, value
					delete(item.item, "arguments")
					if err := writer.write("response.custom_tool_call_input.delta", map[string]any{"output_index": item.index, "item_id": item.item["id"], "delta": value}); err != nil {
						return false, err
					}
					if err := writer.write("response.custom_tool_call_input.done", payload); err != nil {
						return false, err
					}
				}
				if item.item["type"] != "reasoning" {
					item.item["status"] = "completed"
				}
				if err := writer.write("response.output_item.done", map[string]any{"output_index": item.index, "item": item.item}); err != nil {
					return false, err
				}
				completed = append(completed, item.item)
			}
			result := map[string]any{"id": id, "object": "response", "created_at": created, "model": model, "status": "completed", "output": completed, "error": nil}
			event := "response.completed"
			if finish == "length" || finish == "content_filter" {
				event, result["status"] = "response.incomplete", "incomplete"
				reason := "max_output_tokens"
				if finish == "content_filter" {
					reason = "content_filter"
				}
				result["incomplete_details"] = map[string]any{"reason": reason}
			}
			if usage != nil {
				result["usage"] = translatedUsage(usage, true)
			}
			terminal = true
			return true, writer.write(event, map[string]any{"response": result})
		}
		if failure := chunk["error"]; failure != nil {
			terminal = true
			if err := streamFailure(writer, true, failure); err != nil {
				return true, err
			}
			return true, errors.New("upstream stream failed")
		}
		if value := object(chunk["usage"]); value != nil {
			usage = value
		}
		choices := items(chunk["choices"])
		if len(choices) == 0 {
			return false, nil
		}
		if !started {
			started = true
			if value := text(chunk["id"]); value != "" {
				id = "resp_" + value
			}
			if chunk["created"] != nil {
				created = chunk["created"]
			}
			if value := text(chunk["model"]); value != "" {
				model = value
			}
			response := map[string]any{"id": id, "object": "response", "created_at": created, "model": model, "status": "in_progress", "output": []any{}}
			if err := writer.write("response.created", map[string]any{"response": response}); err != nil {
				return false, err
			}
			if err := writer.write("response.in_progress", map[string]any{"response": response}); err != nil {
				return false, err
			}
		}
		choice := object(choices[0])
		delta := object(choice["delta"])
		if err := emitText("reasoning_content", "reasoning_summary_text", reasoningText(delta)); err != nil {
			return false, err
		}
		if encrypted := text(delta["encrypted_content"]); encrypted != "" {
			item := ensure("reasoning_content", "reasoning")
			item.item["summary"], item.item["encrypted_content"] = []any{}, encrypted
			if err := announce(item); err != nil {
				return false, err
			}
		}
		for _, field := range []struct{ key, kind string }{{"content", "output_text"}, {"refusal", "refusal"}} {
			if err := emitText(field.key, field.kind, text(delta[field.key])); err != nil {
				return false, err
			}
		}
		for _, raw := range items(delta["tool_calls"]) {
			call := object(raw)
			item := ensure(fmt.Sprint("tool_", integer(call["index"])), "function_call")
			item.item["call_id"] = text(item.item["call_id"]) + text(call["id"])
			function := object(call["function"])
			item.item["name"] = text(item.item["name"]) + text(function["name"])
			arguments := text(function["arguments"])
			item.value.WriteString(arguments)
			if customTools[text(item.item["name"])] {
				item.item["type"], item.item["input"] = "custom_tool_call", ""
				delete(item.item, "arguments")
			}
			if !item.announced && text(item.item["name"]) != "" && text(item.item["call_id"]) != "" && arguments != "" {
				if err := announce(item); err != nil {
					return false, err
				}
			}
			if item.item["type"] == "function_call" && item.announced && item.value.Len() > item.sent {
				if err := writer.write("response.function_call_arguments.delta", map[string]any{"output_index": item.index, "item_id": item.item["id"], "delta": item.value.String()[item.sent:]}); err != nil {
					return false, err
				}
				item.sent = item.value.Len()
			}
		}
		if value := text(choice["finish_reason"]); value != "" {
			finish = value
		}
		return false, nil
	})
	if err != nil && !terminal && ctx.Err() == nil {
		if writeErr := streamFailure(writer, true, nil); writeErr != nil {
			return writeErr
		}
	}
	return err
}

// ResponsesStreamToChat routes interleaved tool deltas by item_id, never by the
// most recently seen call. Custom tool input is wrapped once, after collection.
func ResponsesStreamToChat(ctx context.Context, src io.Reader, dst io.Writer, model string) error {
	writer := &eventWriter{ctx: ctx, dst: dst}
	id := "chatcmpl_" + uuid.NewString()
	var created any = 0
	terminal, started := false, false
	tools := map[string]int{}
	argumentsSeen := map[string]bool{}
	custom := map[string]*strings.Builder{}
	seen := map[string]bool{}
	encryptedSeen := map[string]bool{}
	chunk := func(delta map[string]any, finish any, usage map[string]any) error {
		value := map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model, "choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}}
		if usage != nil {
			value["usage"] = translatedUsage(usage, false)
		}
		return writer.write("", value)
	}
	var consumeItem func(map[string]any, bool) error
	consumeItem = func(item map[string]any, done bool) error {
		key := text(item["id"])
		switch item["type"] {
		case "function_call", "custom_tool_call":
			index, known := tools[key]
			if !known {
				index = len(tools)
				tools[key] = index
				if item["type"] == "custom_tool_call" {
					custom[key] = &strings.Builder{}
				}
				if err := chunk(map[string]any{"tool_calls": []any{map[string]any{"index": index, "id": item["call_id"], "type": "function", "function": map[string]any{"name": item["name"], "arguments": ""}}}}, nil, nil); err != nil {
					return err
				}
			}
			if done && !argumentsSeen[key] {
				arguments := text(item["arguments"])
				if input := custom[key]; input != nil {
					value := text(item["input"])
					if value == "" {
						value = input.String()
					}
					arguments = jsonText(map[string]any{"input": value})
				}
				argumentsSeen[key] = true
				return chunk(map[string]any{"tool_calls": []any{map[string]any{"index": index, "function": map[string]any{"arguments": arguments}}}}, nil, nil)
			}
		case "message", "reasoning":
			if done && item["type"] == "reasoning" && text(item["encrypted_content"]) != "" && !encryptedSeen[key] {
				encryptedSeen[key] = true
				if err := chunk(map[string]any{"encrypted_content": item["encrypted_content"]}, nil, nil); err != nil {
					return err
				}
			}
			if done {
				if item["type"] == "reasoning" {
					for index, raw := range items(item["summary"]) {
						partKey := fmt.Sprintf("%s:%d", key, index)
						if !seen[partKey] {
							seen[partKey] = true
							if err := chunk(map[string]any{"reasoning_content": text(object(raw)["text"])}, nil, nil); err != nil {
								return err
							}
						}
					}
					return nil
				}
				for index, raw := range items(item["content"]) {
					partKey := fmt.Sprintf("%s:%d", key, index)
					if seen[partKey] {
						continue
					}
					seen[partKey] = true
					block := object(raw)
					field := "content"
					value := text(block["text"])
					if block["type"] == "refusal" {
						field, value = "refusal", text(block["refusal"])
					}
					if err := chunk(map[string]any{field: value}, nil, nil); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}
	err := readEvents(ctx, src, func(kind string, value map[string]any) (bool, error) {
		if kind == "[DONE]" {
			return false, io.ErrUnexpectedEOF
		}
		response := object(value["response"])
		if !started {
			started = true
			if text(response["id"]) != "" {
				id = "chatcmpl_" + text(response["id"])
			}
			if response["created_at"] != nil {
				created = response["created_at"]
			}
			if text(response["model"]) != "" {
				model = text(response["model"])
			}
			if err := chunk(map[string]any{"role": "assistant"}, nil, nil); err != nil {
				return false, err
			}
		}
		switch kind {
		case "error", "response.failed":
			failure := value["error"]
			if failure == nil {
				failure = response["error"]
			}
			terminal = true
			if err := streamFailure(writer, false, failure); err != nil {
				return true, err
			}
			return true, errors.New("upstream stream failed")
		case "response.output_item.added", "response.output_item.done":
			return false, consumeItem(object(value["item"]), kind == "response.output_item.done")
		case "response.function_call_arguments.delta":
			key := text(value["item_id"])
			index, ok := tools[key]
			if !ok {
				return false, errors.New("tool arguments precede their output item")
			}
			argumentsSeen[key] = true
			return false, chunk(map[string]any{"tool_calls": []any{map[string]any{"index": index, "function": map[string]any{"arguments": value["delta"]}}}}, nil, nil)
		case "response.custom_tool_call_input.delta":
			input := custom[text(value["item_id"])]
			if input == nil {
				return false, errors.New("custom input precedes its output item")
			}
			input.WriteString(text(value["delta"]))
		case "response.function_call_arguments.done", "response.custom_tool_call_input.done":
			item := map[string]any{"id": value["item_id"], "type": "function_call", "arguments": value["arguments"]}
			if kind == "response.custom_tool_call_input.done" {
				item["type"], item["input"] = "custom_tool_call", value["input"]
			}
			if _, ok := tools[text(value["item_id"])]; !ok {
				return false, errors.New("tool arguments precede their output item")
			}
			return false, consumeItem(item, true)
		case "response.output_text.delta", "response.reasoning_summary_text.delta", "response.refusal.delta":
			index := integer(value["content_index"])
			if kind == "response.reasoning_summary_text.delta" {
				index = integer(value["summary_index"])
			}
			seen[fmt.Sprintf("%s:%d", text(value["item_id"]), index)] = true
			field := "content"
			if kind == "response.reasoning_summary_text.delta" {
				field = "reasoning_content"
			} else if kind == "response.refusal.delta" {
				field = "refusal"
			}
			return false, chunk(map[string]any{field: value["delta"]}, nil, nil)
		case "response.completed", "response.done", "response.incomplete":
			for _, item := range items(response["output"]) {
				if err := consumeItem(object(item), true); err != nil {
					return false, err
				}
			}
			finish := "stop"
			if len(tools) > 0 {
				finish = "tool_calls"
			}
			if kind == "response.incomplete" || response["status"] == "incomplete" {
				finish = "length"
				if object(response["incomplete_details"])["reason"] == "content_filter" {
					finish = "content_filter"
				}
			}
			terminal = true
			if err := chunk(map[string]any{}, finish, object(response["usage"])); err != nil {
				return false, err
			}
			return true, writer.done()
		}
		return false, nil
	})
	if err != nil && !terminal && ctx.Err() == nil {
		if writeErr := streamFailure(writer, false, nil); writeErr != nil {
			return writeErr
		}
	}
	return err
}
