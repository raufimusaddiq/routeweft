package openai

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// FoldChatStream folds an OpenAI Chat Completions SSE stream into one
// chat.completion document. It is for a gateway that only answers streams
// (CodeBuddy: HTTP 400 code 11101 to a non-stream request) when the client asked
// for a single JSON body (open-sse/handlers/chatCore/sseToJsonHandler.js:115-179,
// parseSSEToOpenAIResponse).
//
// The question for every field is "can this carry text the caller would lose",
// not "do I recognise it":
//
//   - transport metadata is enumerated (chunkMetadata): ignored
//     and counted in Folded.Ignored, or carried when the completion has a slot for
//     it (id, created, model, system_fingerprint, service_tier);
//   - everything else, in a chunk, a choice or a delta, fails the fold. A field
//     that is not on a list is not assumed harmless.
//
// Divergences from the pin, each so nothing is dropped or invented:
//   - every choice index is folded (the pin reads choices[0] only);
//   - a stream that ends with neither a finish_reason nor [DONE] is truncated and
//     fails (the pin answers finish_reason "stop");
//   - a malformed data payload fails the fold (the pin skips it);
//   - a content-bearing field the fold does not know fails it (the pin skips it).
//
// A [DONE] with no finish_reason on a choice is a clean close by the gateway: that
// choice reports "stop", as the pin does.
func FoldChatStream(source io.Reader, fallbackModel string, now func() time.Time) (Folded, error) {
	if now == nil {
		now = time.Now
	}
	state := &foldState{choices: map[int]*foldChoice{}, ignored: map[string]int{}}
	reader := bufio.NewReaderSize(io.LimitReader(source, MaxTranslatedResponseBytes+1), 64*1024)
	var event []byte
	total := 0
	flush := func() error {
		payload := event
		event = nil
		if len(payload) == 0 || state.done {
			return nil
		}
		return state.chunk(bytes.TrimSpace(payload))
	}
	for {
		line, err := reader.ReadBytes('\n')
		total += len(line)
		if total > MaxTranslatedResponseBytes {
			return Folded{}, errors.New("stream exceeds the size the fold accepts")
		}
		if len(line) > 0 {
			text := bytes.TrimRight(line, "\r\n")
			switch {
			case len(text) == 0:
				if ferr := flush(); ferr != nil {
					return Folded{}, ferr
				}
			case bytes.HasPrefix(text, []byte("data:")):
				data := bytes.TrimPrefix(text[len("data:"):], []byte(" "))
				if len(event) > 0 {
					event = append(event, '\n')
				}
				event = append(event, data...)
			}
			// event:, id:, retry: and ": comment" lines name or pace an event; they
			// carry no payload to lose.
		}
		if err != nil {
			if !errors.Is(err, io.EOF) {
				return Folded{}, fmt.Errorf("stream read failed: %w", err)
			}
			break
		}
	}
	if ferr := flush(); ferr != nil {
		return Folded{}, ferr
	}
	return state.finish(fallbackModel, now())
}

// Folded is a folded stream.
type Folded struct {
	// Body is the chat.completion document.
	Body []byte
	// Ignored counts the transport-metadata fields the fold read and did not
	// carry, by name.
	Ignored map[string]int
}

// StreamError is an in-band error chunk ({"error":...}) the gateway sent in the
// stream.
type StreamError struct{ Raw json.RawMessage }

func (e *StreamError) Error() string { return "upstream stream reported an error: " + string(e.Raw) }

// Message is the upstream error's own message, for a client-facing body; the
// raw JSON when it carries none. Raw is the error object (or a bare string), and
// an enclosing {"error": ...} envelope is tolerated.
func (e *StreamError) Message() string {
	raw := e.Raw
	var envelope struct {
		Error json.RawMessage `json:"error"`
	}
	if json.Unmarshal(raw, &envelope) == nil && len(envelope.Error) > 0 {
		raw = envelope.Error
	}
	var detail struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(raw, &detail) == nil && strings.TrimSpace(detail.Message) != "" {
		return detail.Message
	}
	var text string
	if json.Unmarshal(raw, &text) == nil && strings.TrimSpace(text) != "" {
		return text
	}
	return string(e.Raw)
}

// chunkMetadata are the chunk-level keys that carry no text: object is constant,
// obfuscation is padding the gateway adds against length side channels.
var chunkMetadata = map[string]bool{"object": true, "obfuscation": true}

// Choices and deltas have no ignorable key: index, delta, finish_reason,
// logprobs, role, content, reasoning_content, refusal and tool_calls are read,
// and any other key can carry text, so it fails the fold.

type foldChoice struct {
	role         string
	hasRole      bool
	content      strings.Builder
	hasContent   bool
	reasoning    strings.Builder
	hasReasoning bool
	refusal      strings.Builder
	hasRefusal   bool
	calls        map[int]*foldCall
	finish       string
}

type foldCall struct {
	id, name, arguments string
}

type foldState struct {
	done                         bool
	id, model, fingerprint, tier json.RawMessage
	created                      json.RawMessage
	usage                        json.RawMessage
	choices                      map[int]*foldChoice
	chunks                       int
	ignored                      map[string]int
}

func (s *foldState) chunk(payload []byte) error {
	if string(payload) == "[DONE]" {
		s.done = true
		return nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(payload, &fields); err != nil || fields == nil {
		return errors.New("stream carried a data payload that is not a JSON object")
	}
	if raw, ok := fields["error"]; ok && !isNull(raw) {
		return &StreamError{Raw: raw}
	}
	s.chunks++
	for key, raw := range fields {
		switch key {
		case "choices", "error":
		case "id":
			if s.id == nil && !isNull(raw) {
				s.id = raw
			}
		case "created":
			if s.created == nil && !isNull(raw) {
				s.created = raw
			}
		case "model":
			if s.model == nil && !isNull(raw) {
				s.model = raw
			}
		case "system_fingerprint":
			if !isNull(raw) {
				s.fingerprint = raw
			}
		case "service_tier":
			if !isNull(raw) {
				s.tier = raw
			}
		case "usage":
			if isNull(raw) {
				continue
			}
			var object map[string]json.RawMessage
			if json.Unmarshal(raw, &object) != nil || object == nil {
				return errors.New("stream carried a usage that is not an object")
			}
			s.usage = raw
		default:
			if !chunkMetadata[key] {
				return fmt.Errorf("stream chunk carried the unrecognised field %q", key)
			}
			s.ignored[key]++
		}
	}
	if raw, ok := fields["choices"]; ok && !isNull(raw) {
		var choices []json.RawMessage
		if json.Unmarshal(raw, &choices) != nil {
			return errors.New("stream chunk carried choices that are not an array")
		}
		for _, choice := range choices {
			if err := s.choice(choice); err != nil {
				return err
			}
		}
	}
	return nil
}

func (s *foldState) choice(raw json.RawMessage) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return errors.New("stream chunk carried a choice that is not an object")
	}
	index := 0
	if value, ok := fields["index"]; ok && !isNull(value) {
		if json.Unmarshal(value, &index) != nil || index < 0 {
			return errors.New("stream chunk carried a choice index that is not a non-negative integer")
		}
	}
	choice := s.choices[index]
	if choice == nil {
		choice = &foldChoice{calls: map[int]*foldCall{}}
		s.choices[index] = choice
	}
	for key, value := range fields {
		switch key {
		case "index":
		case "delta":
			if err := choice.delta(value, s.ignored); err != nil {
				return err
			}
		case "finish_reason":
			if isNull(value) {
				continue
			}
			var reason string
			if json.Unmarshal(value, &reason) != nil {
				return errors.New("stream chunk carried a finish_reason that is not a string")
			}
			if reason == "" {
				return errors.New("stream carried an empty finish_reason")
			}
			choice.finish = reason
		case "logprobs":
			// logprobs carry the sampled tokens' text; a non-null value is content
			// this fold cannot merge, so it is refused rather than dropped.
			if !isNull(value) {
				return errors.New("stream carried logprobs, which a folded response cannot carry")
			}
		default:
			return fmt.Errorf("stream choice carried the unrecognised field %q", key)
		}
	}
	return nil
}

func (c *foldChoice) delta(raw json.RawMessage, ignored map[string]int) error {
	if isNull(raw) {
		return nil
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(raw, &fields) != nil || fields == nil {
		return errors.New("stream choice carried a delta that is not an object")
	}
	for key, value := range fields {
		switch key {
		case "role":
			if isNull(value) {
				continue
			}
			var role string
			if json.Unmarshal(value, &role) != nil {
				return errors.New("stream delta carried a role that is not a string")
			}
			c.role = role
			c.hasRole = true
		case "content":
			text, ok, err := deltaText(value, "content")
			if err != nil {
				return err
			}
			if ok {
				c.content.WriteString(text)
				c.hasContent = true
			}
		case "reasoning_content":
			text, ok, err := deltaText(value, "reasoning_content")
			if err != nil {
				return err
			}
			if ok {
				c.reasoning.WriteString(text)
				c.hasReasoning = true
			}
		case "refusal":
			text, ok, err := deltaText(value, "refusal")
			if err != nil {
				return err
			}
			if ok {
				c.refusal.WriteString(text)
				c.hasRefusal = true
			}
		case "tool_calls":
			if err := c.toolCalls(value, ignored); err != nil {
				return err
			}
		default:
			return fmt.Errorf("stream delta carried the unrecognised field %q", key)
		}
	}
	return nil
}

// deltaText reads a delta text field: a string, or null (absent text). Any other
// type is text the fold cannot place, so it fails.
func deltaText(raw json.RawMessage, name string) (string, bool, error) {
	if isNull(raw) {
		return "", false, nil
	}
	var text string
	if json.Unmarshal(raw, &text) != nil {
		return "", false, fmt.Errorf("stream delta carried a %s that is not a string", name)
	}
	return text, true, nil
}

func (c *foldChoice) toolCalls(raw json.RawMessage, ignored map[string]int) error {
	if isNull(raw) {
		return nil
	}
	var calls []json.RawMessage
	if json.Unmarshal(raw, &calls) != nil {
		return errors.New("stream delta carried tool_calls that are not an array")
	}
	if len(calls) == 0 {
		ignored["delta.tool_calls(empty)"]++ // an empty array carries no call
		return nil
	}
	for _, rawCall := range calls {
		var call map[string]json.RawMessage
		if json.Unmarshal(rawCall, &call) != nil || call == nil {
			return errors.New("stream delta carried a tool call that is not an object")
		}
		index := 0
		var id, kind string
		hasID := false
		var function map[string]json.RawMessage
		for key, value := range call {
			switch key {
			case "index":
				if !isNull(value) && (json.Unmarshal(value, &index) != nil || index < 0) {
					return errors.New("stream tool call carried an index that is not a non-negative integer")
				}
			case "id":
				if !isNull(value) && json.Unmarshal(value, &id) != nil {
					return errors.New("stream tool call carried an id that is not a string")
				}
				hasID = !isNull(value)
			case "type":
				if !isNull(value) && json.Unmarshal(value, &kind) != nil {
					return errors.New("stream tool call carried a type that is not a string")
				}
				if !isNull(value) && kind != "function" {
					return fmt.Errorf("stream tool call has the type %q, which a folded response cannot carry", kind)
				}
			case "function":
				if !isNull(value) && (json.Unmarshal(value, &function) != nil || function == nil) {
					return errors.New("stream tool call carried a function that is not an object")
				}
			default:
				return fmt.Errorf("stream tool call carried the unrecognised field %q", key)
			}
		}
		target := c.calls[index]
		if target == nil {
			target = &foldCall{}
			c.calls[index] = target
		}
		if hasID {
			target.id = id
		}
		for key, value := range function {
			switch key {
			case "name", "arguments":
				if isNull(value) {
					continue
				}
				var text string
				if json.Unmarshal(value, &text) != nil {
					return fmt.Errorf("stream tool call carried a function %s that is not a string", key)
				}
				if key == "name" {
					target.name += text
				} else {
					target.arguments += text
				}
			default:
				return fmt.Errorf("stream tool call function carried the unrecognised field %q", key)
			}
		}
	}
	return nil
}

func (s *foldState) finish(fallbackModel string, now time.Time) (Folded, error) {
	if s.chunks == 0 {
		return Folded{}, errors.New("stream carried no chunks")
	}
	if len(s.choices) == 0 {
		return Folded{}, errors.New("stream carried no choices")
	}
	for _, choice := range s.choices {
		if !s.done && choice.finish == "" {
			return Folded{}, errors.New("stream ended before every choice finished or [DONE]")
		}
	}
	indexes := make([]int, 0, len(s.choices))
	for index := range s.choices {
		indexes = append(indexes, index)
	}
	sort.Ints(indexes)
	choices := make([]map[string]any, 0, len(indexes))
	for _, index := range indexes {
		choice := s.choices[index]
		role := choice.role
		if !choice.hasRole {
			role = "assistant"
		}
		message := map[string]any{"role": role}
		switch {
		case choice.hasContent:
			message["content"] = choice.content.String()
		case len(choice.calls) > 0:
			message["content"] = nil
		default:
			message["content"] = ""
		}
		if choice.hasReasoning {
			message["reasoning_content"] = choice.reasoning.String()
		}
		if choice.hasRefusal {
			message["refusal"] = choice.refusal.String()
		}
		if len(choice.calls) > 0 {
			order := make([]int, 0, len(choice.calls))
			for position := range choice.calls {
				order = append(order, position)
			}
			sort.Ints(order)
			calls := make([]map[string]any, 0, len(order))
			for _, position := range order {
				call := choice.calls[position]
				calls = append(calls, map[string]any{
					"id": call.id, "type": "function",
					"function": map[string]any{"name": call.name, "arguments": call.arguments},
				})
			}
			message["tool_calls"] = calls
		}
		finish := choice.finish
		if finish == "" {
			finish = "stop"
		}
		choices = append(choices, map[string]any{"index": index, "message": message, "finish_reason": finish})
	}
	result := map[string]any{"object": "chat.completion", "choices": choices}
	result["id"] = rawOr(s.id, fmt.Sprintf("chatcmpl-%d", now.UnixNano()))
	result["created"] = rawOr(s.created, now.Unix())
	result["model"] = rawOr(s.model, orString(fallbackModel, "unknown"))
	if s.fingerprint != nil {
		result["system_fingerprint"] = s.fingerprint
	}
	if s.tier != nil {
		result["service_tier"] = s.tier
	}
	if s.usage != nil {
		result["usage"] = s.usage
	}
	body, err := json.Marshal(result)
	if err != nil {
		return Folded{}, err
	}
	return Folded{Body: body, Ignored: s.ignored}, nil
}

func rawOr(raw json.RawMessage, fallback any) any {
	if raw != nil {
		return raw
	}
	return fallback
}

func orString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func isNull(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	return len(trimmed) == 0 || string(trimmed) == "null"
}
