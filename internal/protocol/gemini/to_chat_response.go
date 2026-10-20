package gemini

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// ResponseOptions are what a Gemini to Chat conversion needs besides the body.
type ResponseOptions struct {
	// Model is the request's model, reported when the upstream names none.
	Model string
	// Names puts back the tool names the request leg had to rewrite. It may be nil.
	Names *concerns.ToolNames
	// Signatures and Scope keep the thought signatures the calls carry, for the
	// request that continues this conversation. An empty Scope keeps none.
	Signatures *Signatures
	Scope      string
}

// fallbackModel is what the pin reports when neither the response nor the
// request names a model (gemini-to-openai.js:55).
const fallbackModel = "gemini"

// responseState folds Gemini response objects into Chat deltas. The streamed leg
// feeds it one object at a time and sends each delta as it appears; the buffered
// leg feeds it the one object and joins the deltas, so the two cannot disagree
// about what a part means.
type responseState struct {
	options ResponseOptions
	id      string
	model   string
	created int64

	calls      int
	pendingSig string
	usage      map[string]any
	finish     string
	finished   bool
}

func newResponseState(options ResponseOptions) *responseState {
	now := time.Now()
	return &responseState{options: options, id: fmt.Sprintf("chatcmpl-%d", now.UnixMilli()), model: options.Model, created: now.Unix()}
}

// upstreamError is an error object Gemini sent where a response should be.
func failure(code, message string) error {
	return &concerns.StreamFailure{Value: map[string]any{"type": "api_error", "code": code, "message": message}}
}

// finishFor maps a candidate's finishReason (concerns/finishReason.js:30-39). The
// pin maps every reason it does not know to "stop", so MALFORMED_FUNCTION_CALL
// (a call the model could not form) and a reason added next year both read as a
// clean answer. Here only the reasons that mean the answer is complete, cut for
// length, or withheld by a filter are an answer; anything else is a failure.
func finishFor(reason, message string) (string, error) {
	switch strings.ToUpper(reason) {
	case "STOP", "MAX_TOKENS", "SAFETY", "RECITATION", "BLOCKLIST", "PROHIBITED_CONTENT", "OTHER":
		return concerns.ToOpenAIFinish(reason, concerns.FinishFormatGemini), nil
	}
	text := fmt.Sprintf("Gemini ended the response with finishReason %s", reason)
	if message != "" {
		text += ": " + message
	}
	return "", failure("upstream_finish_reason", text)
}

// consume reads one response object and returns the Chat deltas it holds, in
// order.
func (s *responseState) consume(chunk map[string]any) ([]map[string]any, error) {
	response := chunk
	scope := s.options.Scope
	if wrapped := concerns.Object(chunk["response"]); wrapped != nil {
		// Cloud Code wraps every chunk (:45); the envelope selects family-scoped storage.
		response = wrapped
		scope = FamilyScope(scope, s.options.Model)
	}
	if raw, present := response["error"]; present && raw != nil {
		if object := concerns.Object(raw); object != nil {
			return nil, &concerns.StreamFailure{Value: object}
		}
		return nil, failure("upstream_error", concerns.JSONText(raw))
	}
	if id := concerns.Text(response["responseId"]); id != "" {
		s.id = "chatcmpl-" + id
	}
	if model := concerns.Text(response["modelVersion"]); model != "" {
		s.model = model
	}
	if s.model == "" {
		s.model = fallbackModel
	}
	metadata := concerns.Object(response["usageMetadata"])
	if metadata == nil {
		metadata = concerns.Object(chunk["usageMetadata"])
	}
	if usage, observed := concerns.GeminiUsage(metadata); observed {
		s.usage = usage.OpenAI()
	}
	candidates := concerns.Items(response["candidates"])
	if len(candidates) == 0 {
		// A blocked prompt has no candidate. The pin returns nothing (:46), so the
		// stream never ends; it is a filtered answer.
		if feedback := concerns.Object(response["promptFeedback"]); concerns.Text(feedback["blockReason"]) != "" {
			s.finish, s.finished = concerns.FinishContentFilter, true
		}
		return nil, nil
	}
	candidate := concerns.Object(candidates[0])
	if candidate == nil {
		return nil, failure("upstream_invalid", "Gemini sent a candidate that is not an object")
	}
	var deltas []map[string]any
	for _, raw := range concerns.Items(concerns.Object(candidate["content"])["parts"]) {
		part := concerns.Object(raw)
		if part == nil {
			return nil, failure("upstream_invalid", "Gemini sent a part that is not an object")
		}
		partDeltas, err := s.part(part, scope)
		if err != nil {
			return nil, err
		}
		deltas = append(deltas, partDeltas...)
	}
	if reason := concerns.Text(candidate["finishReason"]); reason != "" {
		finish, err := finishFor(reason, concerns.Text(candidate["finishMessage"]))
		if err != nil {
			return nil, err
		}
		s.finish, s.finished = finish, true
	}
	return deltas, nil
}

func (s *responseState) part(part map[string]any, scope string) ([]map[string]any, error) {
	for key := range part {
		switch key {
		case "text", "thought", "thoughtSignature", "thought_signature", "functionCall", "inlineData", "inline_data":
		default:
			// executableCode, codeExecutionResult, fileData, functionResponse: the
			// pin ignores them. The request never asks for a server-side tool, so a
			// part like this is output the client would not see.
			return nil, failure("upstream_unsupported_part", fmt.Sprintf("Gemini sent a %q part, which has no Chat equivalent", key))
		}
	}
	signature := concerns.Text(part["thoughtSignature"])
	if signature == "" {
		signature = concerns.Text(part["thought_signature"])
	}
	if signature != "" {
		s.pendingSig = signature
	}
	var deltas []map[string]any
	if text, _ := part["text"].(string); text != "" {
		if part["thought"] == true {
			deltas = append(deltas, map[string]any{"reasoning_content": text})
		} else {
			deltas = append(deltas, map[string]any{"content": text})
		}
	}
	if call := concerns.Object(part["functionCall"]); call != nil {
		name := concerns.Text(call["name"])
		if name == "" {
			return nil, failure("upstream_invalid", "Gemini sent a function call with no name")
		}
		original := s.options.Names.Original(name)
		arguments := call["args"]
		if arguments == nil {
			arguments = map[string]any{}
		}
		argumentText := concerns.JSONText(arguments)
		// The id of a call Gemini did not number is the same one every time, as
		// for Ollama (PARITY V25); the pin's embeds the clock.
		id := concerns.Text(call["id"])
		if id == "" {
			id = concerns.GenerateToolCallID(0, s.calls, original)
		}
		if s.pendingSig != "" {
			s.options.Signatures.Put(scope, id, original, concerns.JSONText(concerns.ToolInput(argumentText)), s.pendingSig)
			s.pendingSig = ""
		}
		deltas = append(deltas, map[string]any{"tool_calls": []any{map[string]any{
			"index": s.calls, "id": id, "type": "function", "function": map[string]any{"name": original, "arguments": argumentText},
		}}})
		s.calls++
	}
	data := firstObject(part, "inlineData", "inline_data")
	if data != nil && concerns.Text(data["data"]) != "" {
		mediaType := concerns.Text(firstOf(data, "mimeType", "mime_type"))
		if mediaType == "" {
			mediaType = "image/png"
		}
		deltas = append(deltas, map[string]any{"images": []any{map[string]any{
			"type": "image_url", "image_url": map[string]any{"url": "data:" + mediaType + ";base64," + concerns.Text(data["data"])},
		}}})
	}
	return deltas, nil
}

// finishReason is the Chat finish: tool_calls when the model stopped to call
// (gemini-to-openai.js:140), otherwise what the candidate said.
func (s *responseState) finishReason() string {
	if s.finish == "" {
		s.finish = concerns.FinishStop
	}
	if s.finish == concerns.FinishStop && s.calls > 0 {
		return concerns.FinishToolCalls
	}
	return s.finish
}

// GeminiResponseToChat is the buffered response leg gemini>openai-chat
// (gemini-to-openai.js:41-156, wired for buffered bodies at
// handlers/chatCore/nonStreamingHandler.js:196-256). The pin's buffered path and
// its stream disagree, and this follows the stream (PARITY 6.7 G10): usage is
// the shared Gemini arithmetic, and the finish reason is the mapped one, not the
// raw lowercased reason.
func GeminiResponseToChat(body []byte, options ResponseOptions) ([]byte, error) {
	response, err := concerns.WireObject(body)
	if err != nil {
		return nil, err
	}
	state := newResponseState(options)
	deltas, err := state.consume(response)
	if err != nil {
		return nil, err
	}
	inner := response
	if wrapped := concerns.Object(response["response"]); wrapped != nil {
		inner = wrapped
	}
	if len(concerns.Items(inner["candidates"])) == 0 && !state.finished {
		return nil, failure("upstream_empty", "Gemini returned no candidates")
	}
	var content, reasoning strings.Builder
	var calls, images []any
	for _, delta := range deltas {
		content.WriteString(concerns.Text(delta["content"]))
		reasoning.WriteString(concerns.Text(delta["reasoning_content"]))
		for _, raw := range concerns.Items(delta["tool_calls"]) {
			call := concerns.Object(raw)
			delete(call, "index")
			calls = append(calls, call)
		}
		images = append(images, concerns.Items(delta["images"])...)
	}
	message := map[string]any{"role": "assistant"}
	if content.Len() > 0 {
		message["content"] = content.String()
	}
	if reasoning.Len() > 0 {
		message["reasoning_content"] = reasoning.String()
	}
	if len(calls) > 0 {
		message["tool_calls"] = calls
	}
	if len(images) > 0 {
		message["images"] = images
	}
	if message["content"] == nil && message["tool_calls"] == nil {
		message["content"] = ""
	}
	result := map[string]any{
		"id": state.id, "object": "chat.completion", "created": state.created, "model": state.model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": state.finishReason()}},
	}
	if state.usage != nil {
		result["usage"] = state.usage
	}
	return json.Marshal(result)
}

// GeminiStreamToChat is the streamed response leg gemini>openai-chat
// (gemini-to-openai.js:41-156), also the leg for vertex (:162). The finish chunk
// waits for the end of the body so it carries the last usage Gemini sent; a body
// that ends before any finishReason, or reports an error, ends with a failure
// terminal.
func GeminiStreamToChat(ctx context.Context, src io.Reader, dst io.Writer, options ResponseOptions) error {
	writer := &concerns.SSEWriter{Ctx: ctx, Dst: dst}
	state := newResponseState(options)
	started := false
	emit := func(delta map[string]any, finish any, usage map[string]any) error {
		if !started {
			started = true
			if err := emitChunk(writer, state, map[string]any{"role": "assistant"}, nil, nil); err != nil {
				return err
			}
		}
		return emitChunk(writer, state, delta, finish, usage)
	}
	err := readChunks(ctx, src, func(chunk map[string]any) error {
		deltas, err := state.consume(chunk)
		if err != nil {
			return err
		}
		for _, delta := range deltas {
			if err := emit(delta, nil, nil); err != nil {
				return err
			}
		}
		return nil
	})
	if err == nil && !state.finished {
		err = io.ErrUnexpectedEOF
	}
	if err != nil {
		_ = concerns.FailChatStream(ctx, writer, err)
		return err
	}
	if err := emit(map[string]any{}, state.finishReason(), state.usage); err != nil {
		return err
	}
	return writer.Done()
}

func emitChunk(writer *concerns.SSEWriter, state *responseState, delta map[string]any, finish any, usage map[string]any) error {
	chunk := map[string]any{
		"id": state.id, "object": "chat.completion.chunk", "created": state.created, "model": state.model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
	}
	if usage != nil {
		chunk["usage"] = usage
	}
	return writer.Event("", chunk)
}
