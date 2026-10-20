package anthropic

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// modelFallback is schema/defaults.js:4.
const modelFallback = "unknown"

// ChatStreamToMessages is the response leg openai-chat>anthropic-messages for a
// stream (translator/response/openai-to-claude.js:71-260): the provider spoke
// Chat, the client speaks Messages.
//
// The pin drops any chunk with no choices (:72), which loses the usage-only
// chunk a Chat provider sends after finish_reason when include_usage is set,
// and it closes the message the moment finish_reason arrives. Both would report
// zero usage to the Messages client, so the closing message_delta and
// message_stop are held until the stream ends and carry the latest usage. The
// block events are unchanged and still flow as they arrive.
func ChatStreamToMessages(ctx context.Context, src io.Reader, dst io.Writer) error {
	writer := &concerns.SSEWriter{Ctx: ctx, Dst: dst}
	state := &chatStreamState{toolCalls: map[int64]*chatToolBlock{}, args: map[int64]string{}}
	closeMessage := func() error {
		if !state.started || state.closed {
			return nil
		}
		state.closed = true
		if !state.finishSeen {
			// The provider ended without saying why. Close what is open and report
			// what happened: a message with tool calls stopped for tool use, anything
			// else stopped normally. It must not leave a block open or claim a reason
			// it never had.
			var events []map[string]any
			finish := concerns.FinishStop
			if len(state.toolCalls) > 0 {
				finish = concerns.FinishToolCalls
			}
			state.closeBlocks(&events)
			state.finish = finish
			for _, event := range events {
				if err := writer.Event(concerns.Text(event["type"]), event); err != nil {
					return err
				}
			}
		}
		usage := state.usage
		if usage == nil {
			usage = map[string]any{"input_tokens": 0, "output_tokens": 0}
		}
		if err := writer.Event("message_delta", map[string]any{"type": "message_delta",
			"delta": map[string]any{"stop_reason": concerns.FromOpenAIFinish(state.finish, concerns.FinishFormatClaude)}, "usage": usage}); err != nil {
			return err
		}
		return writer.Event("message_stop", map[string]any{"type": "message_stop"})
	}
	err := concerns.ReadEvents(ctx, src, func(kind string, chunk map[string]any) (bool, error) {
		if kind == "[DONE]" {
			if !state.started {
				// The provider said it was done without saying anything. That is a
				// failed request, not an empty answer.
				return false, io.ErrUnexpectedEOF
			}
			return true, closeMessage()
		}
		if failure := concerns.Object(chunk["error"]); failure != nil {
			return false, &upstreamError{value: failure}
		}
		if usage := concerns.Object(chunk["usage"]); usage != nil {
			state.usage = concerns.ClaudeUsageFromOpenAI(usage)
		}
		choices := concerns.Items(chunk["choices"])
		if len(choices) == 0 {
			return false, nil
		}
		choice := concerns.Object(choices[0])
		delta := concerns.Object(choice["delta"])
		events := state.events(chunk, delta, choice)
		for _, event := range events {
			if err := writer.Event(concerns.Text(event["type"]), event); err != nil {
				return false, err
			}
		}
		return false, nil
	})
	if err != nil && state.finishSeen && err == io.ErrUnexpectedEOF {
		// Some Chat providers end the body without [DONE] once finish_reason was
		// sent; the answer is complete.
		err = nil
	}
	if err != nil {
		return failMessagesStream(writer, ctx, err)
	}
	return closeMessage()
}

// failMessagesStream ends a Messages stream with an error event. A cancelled
// client gets nothing (spec 9.3).
func failMessagesStream(writer *concerns.SSEWriter, ctx context.Context, cause error) error {
	if ctx.Err() != nil {
		return cause
	}
	failure := streamFailureValue(cause)
	if concerns.Text(failure["type"]) == "" {
		failure = map[string]any{"type": "api_error", "message": concerns.Text(failure["message"])}
	}
	if err := writer.Event("error", map[string]any{"type": "error", "error": failure}); err != nil {
		return err
	}
	return cause
}

type chatToolBlock struct {
	id    string
	name  string
	block int
}

type chatStreamState struct {
	started, closed, finishSeen bool
	id                          string
	model                       any
	nextBlock                   int
	thinkingOpen                bool
	thinkingBlock               int
	textOpen, textClosed        bool
	textBlock                   int
	toolCalls                   map[int64]*chatToolBlock
	toolOrder                   []int64
	args                        map[int64]string
	usage                       map[string]any
	finish                      string
}

func (s *chatStreamState) stopThinking(events *[]map[string]any) {
	if s.thinkingOpen {
		*events = append(*events, map[string]any{"type": "content_block_stop", "index": s.thinkingBlock})
		s.thinkingOpen = false
	}
}

func (s *chatStreamState) stopText(events *[]map[string]any) {
	if s.textOpen && !s.textClosed {
		s.textClosed = true
		*events = append(*events, map[string]any{"type": "content_block_stop", "index": s.textBlock})
		s.textOpen = false
	}
}

// events converts one Chat chunk with a choice into Messages block events.
func (s *chatStreamState) events(chunk, delta, choice map[string]any) []map[string]any {
	var events []map[string]any
	if !s.started {
		s.started = true
		s.id = strings.Replace(concerns.Text(chunk["id"]), "chatcmpl-", "", 1)
		if s.id == "" || s.id == "chat" || len(s.id) < 8 {
			extend := concerns.Object(chunk["extend_fields"])
			s.id = concerns.Text(extend["requestId"])
			if s.id == "" {
				s.id = concerns.Text(extend["traceId"])
			}
			if s.id == "" {
				s.id = fmt.Sprintf("msg_%d", time.Now().UnixMilli())
			}
		}
		s.model = chunk["model"]
		if !concerns.Truthy(s.model) {
			s.model = modelFallback
		}
		events = append(events, map[string]any{"type": "message_start", "message": map[string]any{
			"id": s.id, "type": "message", "role": "assistant", "model": s.model, "content": []any{},
			"stop_reason": nil, "stop_sequence": nil, "usage": map[string]any{"input_tokens": 0, "output_tokens": 0}}})
	}
	if reasoning := concerns.ReasoningText(delta); reasoning != "" {
		s.stopText(&events)
		if !s.thinkingOpen {
			s.thinkingBlock, s.thinkingOpen = s.nextBlock, true
			s.nextBlock++
			events = append(events, map[string]any{"type": "content_block_start", "index": s.thinkingBlock,
				"content_block": map[string]any{"type": "thinking", "thinking": ""}})
		}
		events = append(events, map[string]any{"type": "content_block_delta", "index": s.thinkingBlock,
			"delta": map[string]any{"type": "thinking_delta", "thinking": reasoning}})
	}
	if content := concerns.Text(delta["content"]); content != "" {
		s.stopThinking(&events)
		if !s.textOpen {
			s.textBlock, s.textOpen, s.textClosed = s.nextBlock, true, false
			s.nextBlock++
			events = append(events, map[string]any{"type": "content_block_start", "index": s.textBlock,
				"content_block": map[string]any{"type": "text", "text": ""}})
		}
		events = append(events, map[string]any{"type": "content_block_delta", "index": s.textBlock,
			"delta": map[string]any{"type": "text_delta", "text": content}})
	}
	for _, raw := range concerns.Items(delta["tool_calls"]) {
		call := concerns.Object(raw)
		index := int64(0)
		if call["index"] != nil {
			index = concerns.Int(call["index"])
		}
		function := concerns.Object(call["function"])
		// GLM and Fireworks repeat the id with a null name on every argument
		// chunk; the block opens once per index.
		if id := concerns.Text(call["id"]); id != "" && s.toolCalls[index] == nil {
			s.stopThinking(&events)
			s.stopText(&events)
			block := &chatToolBlock{id: id, name: concerns.Text(function["name"]), block: s.nextBlock}
			s.nextBlock++
			s.toolCalls[index] = block
			s.toolOrder = append(s.toolOrder, index)
			events = append(events, map[string]any{"type": "content_block_start", "index": block.block,
				"content_block": map[string]any{"type": "tool_use", "id": id, "name": strings.TrimPrefix(block.name, legacyToolPrefix), "input": map[string]any{}}})
		}
		if arguments := concerns.Text(function["arguments"]); arguments != "" && s.toolCalls[index] != nil {
			s.args[index] += arguments
		}
	}
	if finish := concerns.Text(choice["finish_reason"]); finish != "" {
		s.closeBlocks(&events)
		s.finish, s.finishSeen = finish, true
	}
	return events
}

// closeBlocks ends every open content block: thinking and text stop, and each
// tool block flushes its buffered, sanitised arguments before it stops.
func (s *chatStreamState) closeBlocks(events *[]map[string]any) {
	s.stopThinking(events)
	s.stopText(events)
	for _, index := range s.toolOrder {
		tool := s.toolCalls[index]
		if buffered := s.args[index]; buffered != "" {
			*events = append(*events, map[string]any{"type": "content_block_delta", "index": tool.block,
				"delta": map[string]any{"type": "input_json_delta", "partial_json": sanitizeToolArgs(tool.name, buffered)}})
		}
		*events = append(*events, map[string]any{"type": "content_block_stop", "index": tool.block})
	}
	s.args = map[int64]string{}
	s.toolCalls, s.toolOrder = map[int64]*chatToolBlock{}, nil
}

// legacyToolPrefix is the "proxy_" prefix older request translators emitted; the
// response strips it defensively (openai-to-claude.js:7-11).
const legacyToolPrefix = "proxy_"

var integerText = regexp.MustCompile(`^\d+$`)
var signedIntegerText = regexp.MustCompile(`^-?\d+$`)
var pdfPages = regexp.MustCompile(`^\d+(?:-\d+)?$`)

// sanitizeToolArgs repairs arguments non-Anthropic models get wrong for the
// Read tool (openai-to-claude.js:14-47). Invalid JSON is returned untouched.
func sanitizeToolArgs(name, arguments string) string {
	var args map[string]any
	decoder := json.NewDecoder(strings.NewReader(arguments))
	decoder.UseNumber()
	if decoder.Decode(&args) != nil || args == nil {
		return arguments
	}
	if strings.TrimPrefix(name, legacyToolPrefix) == "Read" {
		sanitizeReadArgs(args)
	}
	encoded, err := json.Marshal(args)
	if err != nil {
		return arguments
	}
	return string(encoded)
}

func sanitizeReadArgs(args map[string]any) {
	if text, ok := args["limit"].(string); ok && integerText.MatchString(text) {
		args["limit"] = json.Number(text)
	}
	if text, ok := args["offset"].(string); ok && signedIntegerText.MatchString(text) {
		args["offset"] = json.Number(text)
	}
	if limit, ok := args["limit"].(json.Number); ok {
		value, _ := limit.Float64()
		if value > 2000 {
			args["limit"] = json.Number("2000")
		}
		if value < 1 {
			delete(args, "limit")
		}
	}
	if offset, ok := args["offset"].(json.Number); ok {
		if value, _ := offset.Float64(); value < 0 {
			args["offset"] = json.Number("0")
		}
	}
	if pages, present := args["pages"]; present {
		path, _ := args["file_path"].(string)
		text, _ := pages.(string)
		if !strings.HasSuffix(strings.ToLower(path), ".pdf") || !pdfPages.MatchString(text) {
			delete(args, "pages")
		}
	}
}

var responsePrefix = regexp.MustCompile(`^(chatcmpl|resp)_`)

// ChatResponseToMessages converts a non-stream Chat completion to a Messages
// body (translator/response/nonstream-to-claude.js:15-58). A body without
// choices is returned unchanged.
//
// The pin sets input_tokens to the whole prompt_tokens (:26) and drops cache
// counts, while its stream leg subtracts cache (openai-to-claude.js:89); this
// uses the stream arithmetic so one request reports the same usage either way.
func ChatResponseToMessages(body []byte) ([]byte, error) {
	response, err := concerns.WireObject(body)
	if err != nil {
		return nil, err
	}
	choices := concerns.Items(response["choices"])
	if len(choices) == 0 {
		if concerns.Text(response["type"]) == "message" {
			return body, nil // already a Messages body
		}
		return nil, errors.New("upstream response has no choices")
	}
	choice := concerns.Object(choices[0])
	message := concerns.Object(choice["message"])
	var content []any
	reasoning := concerns.Text(message["reasoning_content"])
	if reasoning == "" {
		reasoning = concerns.Text(concerns.Object(message["provider_specific_fields"])["reasoning_content"])
	}
	if reasoning != "" {
		content = append(content, map[string]any{"type": "thinking", "thinking": reasoning})
	}
	if text, ok := message["content"].(string); ok && text != "" {
		content = append(content, map[string]any{"type": "text", "text": text})
	}
	for _, raw := range concerns.Items(message["tool_calls"]) {
		call := concerns.Object(raw)
		function := concerns.Object(call["function"])
		id := concerns.Text(call["id"])
		if id == "" {
			id = fmt.Sprintf("toolu_%d_%d", time.Now().UnixMilli(), len(content))
		}
		name := concerns.Text(function["name"])
		if name == "" {
			name = concerns.Text(call["name"])
		}
		arguments := function["arguments"]
		if !concerns.Truthy(arguments) {
			arguments = call["arguments"]
		}
		content = append(content, map[string]any{"type": "tool_use", "id": id, "name": name, "input": parseToolArguments(arguments)})
	}
	if len(content) == 0 {
		content = append(content, map[string]any{"type": "text", "text": ""})
	}
	id := concerns.Text(response["id"])
	if id == "" {
		id = "msg_" + strconv.FormatInt(time.Now().UnixMilli(), 10)
	}
	model := response["model"]
	if !concerns.Truthy(model) {
		model = "unknown"
	}
	usage := map[string]any{"input_tokens": 0, "output_tokens": 0}
	if raw := concerns.Object(response["usage"]); raw != nil {
		usage = concerns.ClaudeUsageFromOpenAI(raw)
	}
	return json.Marshal(map[string]any{
		"id": responsePrefix.ReplaceAllString(id, "msg_"), "type": "message", "role": "assistant", "model": model,
		"content": content, "stop_reason": concerns.FromOpenAIFinish(concerns.Text(choice["finish_reason"]), concerns.FinishFormatClaude),
		"stop_sequence": nil, "usage": usage,
	})
}

func parseToolArguments(value any) any {
	if !concerns.Truthy(value) {
		return map[string]any{}
	}
	text, ok := value.(string)
	if !ok {
		return value
	}
	var parsed any
	if json.Unmarshal([]byte(text), &parsed) != nil {
		return map[string]any{}
	}
	return parsed
}
