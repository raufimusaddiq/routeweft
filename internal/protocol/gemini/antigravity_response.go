package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

const (
	// maxAntigravityCalls bounds the tool calls one answer may open.
	maxAntigravityCalls = 128
	// maxAntigravityArgumentBytes bounds the argument text one stream may hold.
	maxAntigravityArgumentBytes = 4 << 20
	// antigravityNoResponseID stands in for a response id the upstream never gave:
	// the pin's resp_<clock> differs on every run (openai-to-antigravity.js:25).
	antigravityNoResponseID = "resp_unknown"
)

// antigravityFinish is the finish_reason mapping of openai-to-antigravity.js:85-94.
// A reason it does not list is a failure, not STOP (PARITY 6.8 R3).
func antigravityFinish(reason string) (string, error) {
	switch reason {
	case "stop", "tool_calls", "function_call":
		return "STOP", nil
	case "length":
		return "MAX_TOKENS", nil
	case "content_filter":
		return "SAFETY", nil
	}
	return "", fmt.Errorf("the upstream ended the answer with finish_reason %q", reason)
}

// antigravityCall is a tool call being assembled from argument fragments.
type antigravityCall struct {
	id, name string
	args     strings.Builder
}

type antigravityStream struct {
	id, model string
	calls     map[int]*antigravityCall
	argBytes  int
	usage     map[string]any
	finish    string
	held      []any
}

// arguments decodes a call's argument text. Absent or empty is no arguments;
// anything that is not one JSON object is a failure, never {} (:62).
func (c *antigravityCall) arguments() (map[string]any, error) {
	text := strings.TrimSpace(c.args.String())
	if text == "" {
		return map[string]any{}, nil
	}
	decoder := json.NewDecoder(strings.NewReader(text))
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("the arguments of tool call %q are not valid JSON", c.name)
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("the arguments of tool call %q are not one JSON value", c.name)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("the arguments of tool call %q are not a JSON object", c.name)
	}
	return object, nil
}

// antigravityUsage is usageMetadata for a Chat usage object (:103-117). Chat
// counts reasoning inside completion_tokens, Gemini beside it, so the
// candidates are completion minus reasoning (concerns/usage.js:38 folds them the
// other way); the pin sent completion_tokens whole and double-counted.
func antigravityUsage(usage map[string]any) map[string]any {
	prompt, completion := concerns.Int(usage["prompt_tokens"]), concerns.Int(usage["completion_tokens"])
	reasoning := concerns.Int(concerns.Object(usage["completion_tokens_details"])["reasoning_tokens"])
	cached := concerns.Int(concerns.Object(usage["prompt_tokens_details"])["cached_tokens"])
	total := concerns.Int(usage["total_tokens"])
	if total <= 0 {
		total = prompt + completion
	}
	candidates := completion - reasoning
	if candidates < 0 {
		candidates = 0
	}
	meta := map[string]any{"promptTokenCount": prompt, "candidatesTokenCount": candidates, "totalTokenCount": total}
	if reasoning > 0 {
		meta["thoughtsTokenCount"] = reasoning
	}
	if cached > 0 {
		meta["cachedContentTokenCount"] = cached
	}
	return meta
}

func (s *antigravityStream) response(parts []any, finish string) map[string]any {
	candidate := map[string]any{"content": map[string]any{"role": "model", "parts": parts}}
	if finish != "" {
		candidate["finishReason"] = finish
	}
	id := s.id
	if id == "" {
		id = antigravityNoResponseID
	}
	response := map[string]any{"candidates": []any{candidate}, "modelVersion": s.model, "responseId": id}
	if finish != "" && s.usage != nil {
		response["usageMetadata"] = antigravityUsage(s.usage)
	}
	return map[string]any{"response": response}
}

// consume folds one Chat chunk into the state and returns the parts that can be
// sent now. Tool calls and the finish are held to the end.
func (s *antigravityStream) consume(chunk map[string]any) ([]any, error) {
	if raw, present := chunk["error"]; present && raw != nil {
		if object := concerns.Object(raw); object != nil {
			return nil, &concerns.StreamFailure{Value: object}
		}
		return nil, errors.New(concerns.JSONText(raw))
	}
	if id := concerns.Text(chunk["id"]); id != "" && s.id == "" {
		s.id = id
	}
	if model := concerns.Text(chunk["model"]); model != "" && s.model == "" {
		s.model = model
	}
	if usage := concerns.Object(chunk["usage"]); usage != nil {
		s.usage = usage
	}
	choices := concerns.Items(chunk["choices"])
	if len(choices) == 0 {
		return nil, nil
	}
	if len(choices) > 1 {
		return nil, errors.New("the upstream sent more than one choice; the wire carries one candidate")
	}
	choice := concerns.Object(choices[0])
	if choice == nil {
		return nil, errors.New("a choice is not an object")
	}
	if index, set := choice["index"]; set && index != nil && concerns.Int(index) != 0 {
		return nil, errors.New("the upstream sent a choice other than the first")
	}
	delta := concerns.Object(choice["delta"])
	if choice["delta"] != nil && delta == nil {
		return nil, errors.New("a delta is not an object")
	}
	var parts []any
	if reasoning := concerns.ReasoningText(delta); reasoning != "" {
		parts = append(parts, map[string]any{"thought": true, "text": reasoning})
	}
	switch content := delta["content"].(type) {
	case nil:
	case string:
		if content != "" {
			parts = append(parts, map[string]any{"text": content})
		}
	default:
		return nil, errors.New("delta content is not text")
	}
	if refusal := delta["refusal"]; refusal != nil && refusal != "" {
		return nil, errors.New("the model refused: " + concerns.JSONText(refusal))
	}
	for _, raw := range concerns.Items(delta["images"]) {
		url := concerns.Text(concerns.Object(concerns.Object(raw)["image_url"])["url"])
		mediaType, data, ok := concerns.ParseDataURI(url)
		if !ok {
			return nil, errors.New("a generated image is not a data URI")
		}
		parts = append(parts, map[string]any{"inlineData": map[string]any{"mimeType": mediaType, "data": data}})
	}
	if err := s.accumulate(delta["tool_calls"]); err != nil {
		return nil, err
	}
	switch reason := choice["finish_reason"].(type) {
	case nil:
	case string:
		if reason != "" {
			finish, err := antigravityFinish(reason)
			if err != nil {
				return nil, err
			}
			s.finish = finish
			s.held = parts
			return nil, nil
		}
	default:
		return nil, errors.New("finish_reason is not text")
	}
	if s.finish != "" && len(parts) > 0 {
		return nil, errors.New("content arrived after the finish")
	}
	return parts, nil
}

func (s *antigravityStream) accumulate(raw any) error {
	if raw == nil {
		return nil
	}
	list, ok := raw.([]any)
	if !ok {
		return errors.New("tool_calls is not a list")
	}
	for _, item := range list {
		call := concerns.Object(item)
		if call == nil {
			return errors.New("a tool call is not an object")
		}
		index := 0
		if value, set := call["index"]; set && value != nil {
			if !concerns.IsNumber(value) || concerns.Int(value) < 0 || concerns.Int(value) >= maxAntigravityCalls {
				return errors.New("a tool call index is out of range")
			}
			index = int(concerns.Int(value))
		}
		if s.calls == nil {
			s.calls = map[int]*antigravityCall{}
		}
		accum := s.calls[index]
		if accum == nil {
			accum = &antigravityCall{}
			s.calls[index] = accum
		}
		if id, set := call["id"]; set && id != nil {
			text, isText := id.(string)
			if !isText {
				return errors.New("a tool call id is not text")
			}
			if text != "" {
				accum.id = text
			}
		}
		function := concerns.Object(call["function"])
		if call["function"] != nil && function == nil {
			return errors.New("a tool call function is not an object")
		}
		for field, target := range map[string]func(string){
			"name":      func(v string) { accum.name += v },
			"arguments": func(v string) { accum.args.WriteString(v) },
		} {
			value, set := function[field]
			if !set || value == nil {
				continue
			}
			text, isText := value.(string)
			if !isText {
				return fmt.Errorf("a tool call %s is not text", field)
			}
			if field == "arguments" {
				if s.argBytes += len(text); s.argBytes > maxAntigravityArgumentBytes {
					return errors.New("tool call arguments exceed the limit")
				}
			}
			target(text)
		}
	}
	return nil
}

// finalResponse is the held finish with the assembled calls and the usage that
// may have come after it.
func (s *antigravityStream) finalResponse() (map[string]any, error) {
	parts := append([]any(nil), s.held...)
	indices := make([]int, 0, len(s.calls))
	for index := range s.calls {
		indices = append(indices, index)
	}
	sort.Ints(indices)
	for _, index := range indices {
		call := s.calls[index]
		if call.name == "" {
			return nil, errors.New("a tool call has no name")
		}
		args, err := call.arguments()
		if err != nil {
			return nil, err
		}
		id := call.id
		if id == "" {
			id = concerns.GenerateToolCallID(0, index, call.name)
		}
		parts = append(parts, map[string]any{"functionCall": map[string]any{"id": id, "name": call.name, "args": args}})
	}
	if len(parts) == 0 {
		parts = []any{map[string]any{"text": ""}}
	}
	return s.response(parts, s.finish), nil
}

// ChatStreamToAntigravity is the response leg openai-chat>antigravity
// (translator/response/openai-to-antigravity.js:9-120, registered :123): a Chat
// stream as the Cloud Code SSE an Antigravity client reads, one
// {"response": {...}} object per event and no [DONE] (utils/stream.js:467).
// Tool calls are sent whole at the finish, with the id the model gave them. A
// stream that breaks ends with an error object the client can read, never a
// clean finish; a cancelled client gets nothing.
func ChatStreamToAntigravity(ctx context.Context, src io.Reader, dst io.Writer) error {
	writer := &concerns.SSEWriter{Ctx: ctx, Dst: dst}
	state := &antigravityStream{}
	finalize := func() error {
		if state.finish == "" {
			return io.ErrUnexpectedEOF
		}
		response, err := state.finalResponse()
		if err != nil {
			return err
		}
		return writer.Event("", response)
	}
	err := concerns.ReadEvents(ctx, src, func(kind string, chunk map[string]any) (bool, error) {
		if kind == "[DONE]" {
			return true, finalize()
		}
		parts, err := state.consume(chunk)
		if err != nil {
			return false, err
		}
		if len(parts) > 0 {
			return false, writer.Event("", state.response(parts, ""))
		}
		return false, nil
	})
	if errors.Is(err, io.ErrUnexpectedEOF) && state.finish != "" {
		// The body ended without [DONE] after the finish: the answer is complete.
		err = finalize()
	}
	if err != nil {
		return failAntigravityStream(ctx, writer, err)
	}
	return nil
}

// failAntigravityStream ends the stream with a Google-shaped error object.
func failAntigravityStream(ctx context.Context, writer *concerns.SSEWriter, cause error) error {
	if ctx.Err() != nil {
		return cause
	}
	value := map[string]any{"code": 502, "status": "UNAVAILABLE", "message": "upstream stream ended without a terminal event"}
	var upstream *concerns.StreamFailure
	switch {
	case errors.As(cause, &upstream) && upstream.Value != nil:
		value = upstream.Value
	case !errors.Is(cause, io.ErrUnexpectedEOF):
		value = map[string]any{"code": 502, "status": "UNAVAILABLE", "message": cause.Error()}
	}
	_ = writer.Event("", map[string]any{"error": value})
	return cause
}
