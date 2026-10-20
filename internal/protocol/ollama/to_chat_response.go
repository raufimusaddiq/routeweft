package ollama

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// bufferedModel is what the leg reports as the model when a buffered body names none
// (translator/response/ollama-to-openai.js:127; a stream falls back to the
// request model, :28).
const bufferedModel = "ollama"

// OllamaResponseToChat is the buffered response leg ollama>openai-chat
// (ollama-to-openai.js:108-131, wired at handlers/chatCore/nonStreamingHandler.js:313-316).
// An upstream error body that carries no message is an error, not an empty
// completion.
func OllamaResponseToChat(body []byte) ([]byte, error) {
	response, err := concerns.WireObject(body)
	if err != nil {
		return nil, err
	}
	if failure := upstreamFailure(response); failure != nil && response["message"] == nil {
		return nil, failure
	}
	message := concerns.Object(response["message"])
	var calls int
	delta := messageDelta(message, &calls)
	out := map[string]any{"role": "assistant"}
	for key, value := range delta {
		out[key] = value
	}
	if out["content"] == nil && out["tool_calls"] == nil {
		out["content"] = ""
	}
	finish := concerns.ToOpenAIFinish(concerns.Text(response["done_reason"]), concerns.FinishFormatOllama)
	if calls > 0 {
		finish = concerns.FinishToolCalls
	}
	model := response["model"]
	if !concerns.Truthy(model) {
		model = bufferedModel
	}
	result := map[string]any{
		"id": fmt.Sprintf("chatcmpl-%d", time.Now().UnixMilli()), "object": "chat.completion", "created": time.Now().Unix(), "model": model,
		"choices": []any{map[string]any{"index": 0, "message": out, "finish_reason": finish}},
	}
	if usage, observed := concerns.OllamaUsage(response); observed {
		result["usage"] = usage.OpenAI()
	}
	return json.Marshal(result)
}

// OllamaStreamToChat is the streamed response leg ollama>openai-chat
// (ollama-to-openai.js:20-79). Ollama streams one JSON object per line; the last
// has done:true and carries the finish reason and the token counts. model is the
// request's model, used when the upstream names none. A stream that ends before
// done:true, or reports an error line, ends with a failure terminal.
func OllamaStreamToChat(ctx context.Context, src io.Reader, dst io.Writer, model string) error {
	writer := &concerns.SSEWriter{Ctx: ctx, Dst: dst}
	id := fmt.Sprintf("chatcmpl-%d", time.Now().UnixMilli())
	created := time.Now().Unix()
	var upstreamModel any = model
	var calls int
	emit := func(delta map[string]any, finish any, usage map[string]any) error {
		chunk := map[string]any{
			"id": id, "object": "chat.completion.chunk", "created": created, "model": upstreamModel,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
		}
		if usage != nil {
			chunk["usage"] = usage
		}
		return writer.Event("", chunk)
	}
	err := concerns.ReadLines(ctx, src, func(line map[string]any) (bool, error) {
		if failure := upstreamFailure(line); failure != nil && line["message"] == nil {
			return false, failure
		}
		if value := line["model"]; concerns.Truthy(value) {
			upstreamModel = value
		}
		// The final line may carry text of its own; the pin returns before reading
		// it (:35-47) and the text is lost (PARITY V27).
		if delta := messageDelta(concerns.Object(line["message"]), &calls); len(delta) > 0 {
			if err := emit(delta, nil, nil); err != nil {
				return false, err
			}
		}
		if done, _ := line["done"].(bool); !done {
			return false, nil
		}
		finish := concerns.ToOpenAIFinish(concerns.Text(line["done_reason"]), concerns.FinishFormatOllama)
		if calls > 0 {
			finish = concerns.FinishToolCalls
		}
		var usage map[string]any
		if counted, observed := concerns.OllamaUsage(line); observed {
			usage = counted.OpenAI()
		}
		return true, emit(map[string]any{}, finish, usage)
	})
	if err != nil {
		_ = concerns.FailChatStream(ctx, writer, err)
		return err
	}
	return writer.Done()
}

// messageDelta converts an Ollama message to the fields of a Chat delta or
// message: content, thinking as reasoning_content (:53-70, :115-116) and tool
// calls. calls counts the tool calls converted so far across the whole response
// and is what numbers them.
func messageDelta(message map[string]any, calls *int) map[string]any {
	delta := map[string]any{}
	if text := concerns.Text(message["content"]); text != "" {
		delta["content"] = text
	}
	if thinking := concerns.Text(message["thinking"]); thinking != "" {
		delta["reasoning_content"] = thinking
	}
	if list := concerns.Items(message["tool_calls"]); len(list) > 0 {
		converted := make([]any, 0, len(list))
		for _, raw := range list {
			converted = append(converted, toolCall(concerns.Object(raw), *calls))
			*calls++
		}
		delta["tool_calls"] = converted
	}
	return delta
}

// toolCall is convertToolCalls (:91-103). Ollama sends each call whole, so the
// arguments are one string. The pin numbers a call by function.index or by its
// place in the line (:93), which restarts at 0 on every line, so two calls on
// two lines would share an index and a client would merge them; position is the
// count of calls so far in the response (PARITY V24, V25). The pin's fallback id
// embeds the clock (concerns/toolCall.js:9); this one is stable.
func toolCall(call map[string]any, position int) map[string]any {
	function := concerns.Object(call["function"])
	name := concerns.Text(function["name"])
	id := concerns.Text(call["id"])
	if id == "" {
		id = concerns.GenerateToolCallID(0, position, name)
	}
	arguments := "{}"
	switch value := function["arguments"].(type) {
	case string:
		arguments = value
	case nil:
	default:
		arguments = concerns.JSONText(value)
	}
	return map[string]any{"index": position, "id": id, "type": "function", "function": map[string]any{"name": name, "arguments": arguments}}
}

// upstreamFailure reads Ollama's {"error": "..."} line or body.
func upstreamFailure(line map[string]any) error {
	switch value := line["error"].(type) {
	case string:
		if value != "" {
			return &concerns.StreamFailure{Value: map[string]any{"type": "api_error", "message": value}}
		}
	case map[string]any:
		return &concerns.StreamFailure{Value: value}
	}
	return nil
}
