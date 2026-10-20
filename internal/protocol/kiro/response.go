package kiro

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// DefaultMaxToolBytes bounds the tool input held until the stream ends, the
// pin's KIRO_REPAIR_BUFFER_MAX_BYTES/2 (executors/kiro.js:14, :546).
const DefaultMaxToolBytes = 4 << 20

// fallbackModel is what the pin reports when the request names none.
const fallbackModel = "kiro"

// ResponseOptions are what a Kiro to Chat conversion needs besides the body.
type ResponseOptions struct {
	// Model is the request's model, reported on every chunk.
	Model string
	// Names puts back the tool names the request leg had to rewrite. It may be nil.
	Names *concerns.ToolNames
	// ThinkingTags is RequestInfo.ThinkingTags. Only then is <thinking>...</thinking>
	// in the answer text read as reasoning; otherwise it is the model's own text.
	ThinkingTags bool
	// MaxToolBytes bounds buffered tool input; zero is DefaultMaxToolBytes.
	MaxToolBytes int
}

func failure(code, message string) error {
	return &concerns.StreamFailure{Value: map[string]any{"type": "api_error", "code": code, "message": message}}
}

type toolBuffer struct {
	id, name string
	text     []string
	object   map[string]any
	kind     string // "", "string" or "object"
	bytes    int
}

// machine folds Kiro EventStream events into Chat deltas. The streamed leg
// feeds it one frame at a time and sends each delta as it appears; the buffered
// leg joins the same deltas, so the two cannot disagree about what an event means.
type machine struct {
	options ResponseOptions
	emit    func(delta map[string]any) error

	id      string
	model   string
	created int64

	counts       map[string]int
	tools        []*toolBuffer
	toolsByID    map[string]*toolBuffer
	toolBytes    int
	sawToolUse   bool
	stopReason   string
	terminal     bool
	usage        map[string]any
	delivered    bool // a content or reasoning delta has reached the caller
	hold         string
	inThinking   bool
	dropNewline  bool
	textReasonOn bool
}

func newMachine(options ResponseOptions, emit func(map[string]any) error) *machine {
	now := time.Now()
	model := options.Model
	if model == "" {
		model = fallbackModel
	}
	if options.MaxToolBytes <= 0 {
		options.MaxToolBytes = DefaultMaxToolBytes
	}
	return &machine{options: options, emit: emit, id: fmt.Sprintf("chatcmpl-%d", now.UnixMilli()), model: model, created: now.Unix(),
		counts: map[string]int{}, toolsByID: map[string]*toolBuffer{}, textReasonOn: options.ThinkingTags}
}

func decodeAny(payload []byte) (any, bool, error) {
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil, false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false, failure("upstream_invalid", "Kiro sent an event whose payload is not JSON")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, false, failure("upstream_invalid", "Kiro sent an event whose payload has data after the JSON")
	}
	return value, true, nil
}

// carriesContent reports whether a payload holds anything: an unknown event with
// something in it may be text the client was owed.
func carriesContent(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case string:
		return v != ""
	case map[string]any:
		for _, item := range v {
			if carriesContent(item) {
				return true
			}
		}
		return false
	case []any:
		for _, item := range v {
			if carriesContent(item) {
				return true
			}
		}
		return false
	}
	return true
}

// onlyKeys fails when an object carries a non-empty value under a key the leg
// does not read. The pin ignores such keys; here they may be content.
func onlyKeys(event string, object map[string]any, known ...string) error {
	for key, value := range object {
		allowed := false
		for _, name := range known {
			allowed = allowed || key == name
		}
		if !allowed && carriesContent(value) {
			return failure("upstream_unsupported_event", fmt.Sprintf("Kiro %s carries %q, which has no Chat equivalent", event, key))
		}
	}
	return nil
}

func (m *machine) deliver(delta map[string]any) error {
	m.delivered = true
	return m.emit(delta)
}

const (
	openTag  = "<thinking>"
	closeTag = "</thinking>"
)

// holdback is the length of the longest suffix of text that is a proper prefix
// of tag: the part that may be the start of a tag whose rest has not arrived.
func holdback(text, tag string) int {
	for n := min(len(tag)-1, len(text)); n > 0; n-- {
		if strings.HasSuffix(text, tag[:n]) {
			return n
		}
	}
	return 0
}

// text feeds answer text through the thinking-tag reader (executors/kiro.js:808-833).
// The pin drops what is inside the tags and leaks a tag that two frames split;
// here the inside is reasoning_content and an unfinished tag waits for the next
// frame.
func (m *machine) text(content string) error {
	if !m.textReasonOn {
		if content == "" {
			return nil
		}
		return m.deliver(map[string]any{"content": content})
	}
	buffer := m.hold + content
	m.hold = ""
	for buffer != "" {
		if m.dropNewline {
			m.dropNewline = false
			if buffer[0] == '\n' {
				buffer = buffer[1:]
				continue
			}
		}
		tag, key := openTag, "content"
		if m.inThinking {
			tag, key = closeTag, "reasoning_content"
		}
		if at := strings.Index(buffer, tag); at >= 0 {
			if at > 0 {
				if err := m.deliver(map[string]any{key: buffer[:at]}); err != nil {
					return err
				}
			}
			buffer = buffer[at+len(tag):]
			m.inThinking = !m.inThinking
			m.dropNewline = !m.inThinking // one newline after </thinking> belongs to the tag (:815)
			continue
		}
		keep := holdback(buffer, tag)
		if send := buffer[:len(buffer)-keep]; send != "" {
			if err := m.deliver(map[string]any{key: send}); err != nil {
				return err
			}
		}
		m.hold = buffer[len(buffer)-keep:]
		return nil
	}
	return nil
}

// flushText ends the tag reader: a held partial tag was never a tag.
func (m *machine) flushText() error {
	if m.hold == "" {
		return nil
	}
	key := "content"
	if m.inThinking {
		key = "reasoning_content"
	}
	held := m.hold
	m.hold = ""
	return m.deliver(map[string]any{key: held})
}

func stringField(object map[string]any, event, key string) (string, bool, error) {
	value, present := object[key]
	if !present || value == nil {
		return "", false, nil
	}
	text, isText := value.(string)
	if !isText {
		return "", false, failure("upstream_invalid", fmt.Sprintf("Kiro %s sent %s that is not a string", event, key))
	}
	return text, true, nil
}

func (m *machine) contentAfterStop(event string) error {
	if m.terminal {
		return failure("upstream_content_after_stop", fmt.Sprintf("Kiro sent %s after the response had stopped", event))
	}
	return nil
}

// normalizeStopReason is executors/kiro.js:145-151.
func normalizeStopReason(value string) string {
	var spaced strings.Builder
	runes := []rune(strings.TrimSpace(value))
	for i, r := range runes {
		if i > 0 && r >= 'A' && r <= 'Z' && runes[i-1] >= 'a' && runes[i-1] <= 'z' {
			spaced.WriteByte('_')
		}
		spaced.WriteRune(r)
	}
	reason := strings.NewReplacer(" ", "_", "-", "_").Replace(strings.ToLower(spaced.String()))
	switch reason {
	case "endturn", "end_turn", "stop", "stop_sequence":
		return "end_turn"
	case "tooluse", "tool_use", "tool_calls":
		return "tool_use"
	case "maxtokens", "max_tokens", "max_output_tokens", "length":
		return "max_tokens"
	}
	return reason
}

var refusalPattern = regexp.MustCompile(`content.*filter|guardrail|safety|policy|blocked`)

// severity orders stop reasons so a later, milder one never hides a failure
// (stopDisposition and mergeStopReason, executors/kiro.js:159-183).
func severity(reason string) int {
	switch {
	case reason == "refusal" || refusalPattern.MatchString(reason):
		return 6
	case reason == "cancelled" || reason == "pause_turn":
		return 5
	case reason == "malformed_model_output" || reason == "invalid_model_output":
		return 4
	case reason == "model_context_window_exceeded":
		return 3
	case reason == "max_tokens":
		return 2
	case reason == "end_turn" || reason == "tool_use" || reason == "":
		return 1
	}
	return 5
}

func (m *machine) stop(reason string) {
	m.terminal = true
	if reason == "" {
		return
	}
	if m.stopReason == "" || severity(reason) > severity(m.stopReason) || (reason == "tool_use" && m.stopReason == "end_turn") {
		m.stopReason = reason
	}
}

// handle reads one frame. terminal is true for the event that ends the answer.
func (m *machine) handle(event concerns.EventStreamEvent) (bool, error) {
	if kind, set := event.Headers[":message-type"]; set {
		text, isText := kind.(string)
		if !isText {
			return false, failure("upstream_invalid", "Kiro sent a frame whose :message-type is not a string")
		}
		switch text {
		case "event":
		case "exception", "error":
			message := "Kiro upstream sent an EventStream " + text
			if value, present, _ := decodeAny(event.Payload); present {
				if object := concerns.Object(value); concerns.Text(object["message"]) != "" {
					message = concerns.Text(object["message"])
				}
			}
			return false, failure("upstream_eventstream_error", message)
		default:
			return false, failure("upstream_invalid", fmt.Sprintf("Kiro sent a frame of message type %q", text))
		}
	}
	kind, isText := event.Headers[":event-type"].(string)
	if !isText || kind == "" {
		return false, failure("upstream_unsupported_event", "Kiro sent a frame with no event type")
	}
	m.counts[kind]++
	payload, present, err := decodeAny(event.Payload)
	if err != nil {
		return false, err
	}
	object := concerns.Object(payload)
	if payload != nil && object == nil {
		switch kind {
		case "metadataEvent", "MetadataEvent", "metricsEvent", "contextUsageEvent", "meteringEvent":
			return false, failure("upstream_invalid", "Kiro "+kind+" is not an object")
		}
	}
	switch kind {
	case "assistantResponseEvent":
		if err := m.contentAfterStop(kind); err != nil {
			return false, err
		}
		if present && object == nil {
			return false, failure("upstream_invalid", "Kiro assistantResponseEvent is not an object")
		}
		if err := onlyKeys(kind, object, "content"); err != nil {
			return false, err
		}
		content, _, err := stringField(object, kind, "content")
		if err != nil {
			return false, err
		}
		return false, m.text(content)
	case "codeEvent":
		if err := m.contentAfterStop(kind); err != nil {
			return false, err
		}
		if present && object == nil {
			return false, failure("upstream_invalid", "Kiro codeEvent is not an object")
		}
		if err := onlyKeys(kind, object, "content"); err != nil {
			return false, err
		}
		content, _, err := stringField(object, kind, "content")
		if err != nil || content == "" {
			return false, err
		}
		return false, m.deliver(map[string]any{"content": content})
	case "reasoningContentEvent":
		if err := m.contentAfterStop(kind); err != nil {
			return false, err
		}
		return false, m.reasoning(payload)
	case "toolUseEvent":
		if err := m.contentAfterStop(kind); err != nil {
			return false, err
		}
		return false, m.toolUse(payload, present)
	case "messageStopEvent":
		if present && object == nil {
			return false, failure("upstream_invalid", "Kiro messageStopEvent is not an object")
		}
		if err := onlyKeys(kind, object, "stopReason", "stop_reason"); err != nil {
			return false, err
		}
		reason, _, err := stringField(object, kind, "stopReason")
		if err != nil {
			return false, err
		}
		alias, _, err := stringField(object, kind, "stop_reason")
		if err != nil {
			return false, err
		}
		if alias != "" {
			m.stop(normalizeStopReason(alias))
		}
		reason = normalizeStopReason(reason)
		if reason == "" {
			reason = "end_turn"
			if m.sawToolUse {
				reason = "tool_use"
			}
		}
		m.stop(reason)
		return true, nil
	case "metadataEvent", "MetadataEvent":
		// Only stop reasons are read; tokenUsage is accounting bookkeeping.
		if err := onlyKeys(kind, object, "metadataEvent", "metadata", "stopReason", "stop_reason", "tokenUsage"); err != nil {
			return false, err
		}
		if object == nil && carriesContent(payload) {
			return false, failure("upstream_invalid", "Kiro metadata is not an object")
		}
		metadata := object
		for _, key := range []string{"metadataEvent", "metadata"} {
			if raw := object[key]; raw != nil {
				nested := concerns.Object(raw)
				if nested == nil || metadata != nil && len(metadata) > 1 {
					return false, failure("upstream_invalid", "Kiro metadata envelope is ambiguous or not an object")
				}
				metadata = nested
				break
			}
		}
		if err := onlyKeys(kind, metadata, "stopReason", "stop_reason", "tokenUsage"); err != nil {
			return false, err
		}
		terminal := false
		for _, key := range []string{"stopReason", "stop_reason"} {
			reason, _, err := stringField(metadata, kind, key)
			if err != nil {
				return false, err
			}
			if reason = normalizeStopReason(reason); reason != "" {
				m.stop(reason)
				terminal = true
			}
		}
		return terminal, nil
	case "metricsEvent":
		// Read counts only; no estimates or credit accounting.
		keys := []string{"inputTokens", "outputTokens", "cacheReadInputTokens", "cache_read_input_tokens", "cacheCreationInputTokens", "cache_creation_input_tokens"}
		if err := onlyKeys(kind, object, append(keys, "metricsEvent")...); err != nil {
			return false, err
		}
		metrics := object
		if raw := object["metricsEvent"]; raw != nil {
			nested := concerns.Object(raw)
			if nested == nil || len(object) != 1 {
				return false, failure("upstream_invalid", "Kiro metrics envelope is ambiguous or not an object")
			}
			metrics = nested
		}
		if err := onlyKeys(kind, metrics, keys...); err != nil {
			return false, err
		}
		if usage, observed := concerns.KiroUsage(metrics); observed {
			m.usage = usage.OpenAI()
		} else {
			m.counts["metricsEvent.unmeasured"]++
		}
		return false, nil
	case "contextUsageEvent":
		// The pin uses this percentage only for estimates, which are not reported.
		return false, onlyKeys(kind, object, "contextUsagePercentage")
	case "meteringEvent":
		// Credits are bookkeeping; neither usage nor unit enters Chat token usage.
		if err := onlyKeys(kind, object, "meteringEvent", "usage", "unit"); err != nil {
			return false, err
		}
		if raw := object["meteringEvent"]; raw != nil {
			nested := concerns.Object(raw)
			if nested == nil || len(object) != 1 {
				return false, failure("upstream_invalid", "Kiro metering envelope is ambiguous or not an object")
			}
			return false, onlyKeys(kind, nested, "usage", "unit")
		}
		return false, nil
	}
	return false, failure("upstream_unsupported_event", fmt.Sprintf("Kiro sent an unproven event type %q", kind))
}

func (m *machine) reasoning(payload any) error {
	var text string
	switch v := payload.(type) {
	case nil:
		return nil
	case string:
		text = v
	case map[string]any:
		if err := onlyKeys("reasoningContentEvent", v, "reasoningContentEvent", "text", "content"); err != nil {
			return err
		}
		if v["reasoningContentEvent"] != nil && (v["text"] != nil || v["content"] != nil) {
			return failure("upstream_invalid", "Kiro reasoning envelope has multiple text values")
		}
		value := v
		switch nested := v["reasoningContentEvent"].(type) {
		case string:
			text = nested
			value = nil
		case map[string]any:
			if err := onlyKeys("reasoningContentEvent", nested, "text", "content"); err != nil {
				return err
			}
			value = nested
		case nil:
		default:
			return failure("upstream_invalid", "Kiro reasoningContentEvent is neither text nor an object")
		}
		for _, key := range []string{"text", "content"} {
			if raw, present := value[key]; present && raw != nil {
				found, isText := raw.(string)
				if !isText {
					return failure("upstream_invalid", "Kiro reasoningContentEvent sent text that is not a string")
				}
				if text != "" && found != "" {
					return failure("upstream_invalid", "Kiro reasoning event has multiple text values")
				}
				if text == "" {
					text = found
				}
			}
		}
	default:
		return failure("upstream_invalid", "Kiro reasoningContentEvent is neither text nor an object")
	}
	if text == "" {
		return nil
	}
	return m.deliver(map[string]any{"reasoning_content": text})
}

func (m *machine) toolUse(payload any, present bool) error {
	m.sawToolUse = true
	var values []any
	switch v := payload.(type) {
	case []any:
		values = v
	case nil:
	default:
		values = []any{v}
	}
	if !present || len(values) == 0 || values[0] == nil {
		return failure("upstream_invalid", "Kiro toolUseEvent is empty")
	}
	for _, raw := range values {
		value := concerns.Object(raw)
		if value == nil {
			return failure("upstream_invalid", "Kiro toolUseEvent holds something that is not an object")
		}
		if err := onlyKeys("toolUseEvent", value, "toolUseId", "name", "input", "stop"); err != nil {
			return err
		}
		// stop is a fragment marker; the message stop event owns terminal semantics.
		if marker := value["stop"]; marker != nil && marker != true && marker != false {
			return failure("upstream_invalid", "Kiro tool stop marker is not a boolean")
		}
		name, isText := value["name"].(string)
		if !isText || strings.TrimSpace(name) == "" {
			return failure("upstream_invalid", "Kiro toolUseEvent is missing a tool name")
		}
		name = strings.TrimSpace(name)
		var id string
		switch v := value["toolUseId"].(type) {
		case nil:
			if _, set := value["toolUseId"]; set {
				return failure("upstream_invalid", "Kiro toolUseEvent has an invalid toolUseId")
			}
			id = concerns.GenerateToolCallID(0, len(m.tools), name)
		case string:
			if strings.TrimSpace(v) == "" {
				return failure("upstream_invalid", "Kiro toolUseEvent has an invalid toolUseId")
			}
			id = v
		default:
			return failure("upstream_invalid", "Kiro toolUseEvent has an invalid toolUseId")
		}
		tool := m.toolsByID[id]
		if tool == nil {
			tool = &toolBuffer{id: id, name: name}
			m.toolsByID[id] = tool
			m.tools = append(m.tools, tool)
			m.toolBytes += len(id) + len(name) + 32
		} else if tool.name != name {
			return failure("upstream_invalid", "Kiro tool name changed between fragments")
		}
		input, set := value["input"]
		if set {
			switch v := input.(type) {
			case string:
				if tool.kind != "" && tool.kind != "string" {
					return failure("upstream_invalid", "Kiro tool input changed fragment type")
				}
				tool.kind = "string"
				tool.text = append(tool.text, v)
				m.toolBytes += len(v)
			case map[string]any:
				if tool.kind != "" && tool.kind != "object" {
					return failure("upstream_invalid", "Kiro tool input changed fragment type")
				}
				if tool.object != nil && !reflect.DeepEqual(tool.object, v) {
					return failure("upstream_invalid", "Kiro tool input changed between object fragments")
				}
				tool.kind = "object"
				m.toolBytes -= tool.bytes
				tool.object = v
				tool.bytes = len(concerns.JSONText(v))
				m.toolBytes += tool.bytes
			default:
				return failure("upstream_invalid", "Kiro tool input must be a JSON object")
			}
		}
		if m.toolBytes > m.options.MaxToolBytes {
			return failure("upstream_buffer_exceeded", "Kiro buffered tool input exceeded its memory bound")
		}
	}
	return nil
}

// toolCalls validates every buffered call before any is emitted: one unusable
// call fails the stream, and no call of that turn reaches the client.
func (m *machine) toolCalls() ([]map[string]any, error) {
	var calls []map[string]any
	for _, tool := range m.tools {
		var input map[string]any
		switch tool.kind {
		case "":
			return nil, failure("invalid_kiro_tool_call", fmt.Sprintf("Kiro tool call %s is missing input", tool.name))
		case "object":
			input = tool.object
		default:
			joined := strings.Join(tool.text, "")
			input = map[string]any{}
			if strings.TrimSpace(joined) != "" {
				parsed, err := concerns.WireObject([]byte(joined))
				if err != nil {
					return nil, failure("invalid_kiro_tool_call", fmt.Sprintf("Kiro tool call %s input is not a JSON object", tool.name))
				}
				input = parsed
			}
		}
		if tool.name == "tool_call" {
			if nested, _ := input["name"].(string); strings.TrimSpace(nested) == "" {
				return nil, failure("invalid_kiro_tool_call", "Invalid Kiro tool_call payload: missing nested MCP tool name")
			}
			if _, set := input["arguments"]; !set {
				return nil, failure("invalid_kiro_tool_call", "Invalid Kiro tool_call payload: missing nested MCP tool arguments")
			}
		}
		calls = append(calls, map[string]any{"id": tool.id, "name": m.options.Names.Original(tool.name), "arguments": concerns.JSONText(input)})
	}
	return calls, nil
}

// finish ends the answer after the reader returned cleanly: held text, tool
// calls, then the finish reason (executors/kiro.js:996-1133). It returns the
// finish reason; an answer that did not finish is an error.
func (m *machine) finish() (string, error) {
	if !m.terminal {
		return "", io.ErrUnexpectedEOF
	}
	if err := m.flushText(); err != nil {
		return "", err
	}
	calls, err := m.toolCalls()
	if err != nil {
		return "", err
	}
	reason := m.stopReason
	switch {
	case reason == "end_turn", reason == "":
		if len(calls) > 0 {
			reason = concerns.FinishToolCalls
		} else {
			reason = concerns.FinishStop
		}
	case reason == "tool_use":
		resolved, err := concerns.ResolveFinish(concerns.FinishToolCalls, len(calls), false)
		if err != nil {
			return "", failure("kiro_tool_use_without_call", err.Error())
		}
		reason = resolved
	case reason == "max_tokens" && len(calls) == 0:
		reason = concerns.FinishLength
	case reason == "model_context_window_exceeded" && len(calls) == 0 && m.delivered:
		reason = concerns.FinishLength
	default:
		return "", failure("kiro_terminal_"+severityName(reason), "Kiro ended with non-success stop reason: "+reason)
	}
	for index, call := range calls {
		start := map[string]any{"index": index, "id": call["id"], "type": "function",
			"function": map[string]any{"name": call["name"], "arguments": ""}}
		if err := m.emit(map[string]any{"tool_calls": []any{start}}); err != nil {
			return "", err
		}
		more := map[string]any{"index": index, "function": map[string]any{"arguments": call["arguments"]}}
		if err := m.emit(map[string]any{"tool_calls": []any{more}}); err != nil {
			return "", err
		}
	}
	return reason, nil
}

func severityName(reason string) string {
	switch reason {
	case "cancelled", "pause_turn", "max_tokens", "model_context_window_exceeded":
		return "incomplete"
	case "malformed_model_output", "invalid_model_output":
		return "protocol_failure"
	}
	if reason == "refusal" || refusalPattern.MatchString(reason) {
		return "refusal"
	}
	return "unknown_stop_reason"
}

func chunk(m *machine, delta map[string]any, finish any, usage map[string]any) map[string]any {
	result := map[string]any{
		"id": m.id, "object": "chat.completion.chunk", "created": m.created, "model": m.model,
		"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}},
	}
	if usage != nil {
		result["usage"] = usage
	}
	return result
}

// KiroStreamToChat is the streamed response leg kiro>openai-chat
// (translator/response/kiro-to-openai.js, whose work the pin does in
// executors/kiro.js:629-1190). src is the raw EventStream body. Text and
// reasoning reach the client as frames arrive; tool calls and the finish chunk
// wait for the reader to confirm the body is whole, so a stream that was cut
// short, corrupt after its last event, or that never said it had stopped ends
// with a failure terminal, never a success.
func KiroStreamToChat(ctx context.Context, src io.Reader, dst io.Writer, options ResponseOptions) error {
	writer := &concerns.SSEWriter{Ctx: ctx, Dst: dst}
	var state *machine
	started := false
	send := func(delta map[string]any, finish any, usage map[string]any) error {
		if !started {
			started = true
			if err := writer.Event("", chunk(state, map[string]any{"role": "assistant"}, nil, nil)); err != nil {
				return err
			}
		}
		return writer.Event("", chunk(state, delta, finish, usage))
	}
	state = newMachine(options, func(delta map[string]any) error { return send(delta, nil, nil) })
	err := concerns.ReadEventStream(ctx, src, state.handle)
	var reason string
	if err == nil {
		reason, err = state.finish()
	}
	if err != nil {
		_ = concerns.FailChatStream(ctx, writer, err)
		return err
	}
	if err := send(map[string]any{}, reason, state.usage); err != nil {
		return err
	}
	return writer.Done()
}

// KiroResponseToChat is the buffered response leg kiro>openai-chat for a client
// that did not ask for a stream. Kiro only streams, so body is the whole
// EventStream; it runs the same reader and the same machine as the stream.
func KiroResponseToChat(body []byte, options ResponseOptions) ([]byte, error) {
	var content, reasoning strings.Builder
	var calls []any
	state := newMachine(options, func(delta map[string]any) error {
		content.WriteString(concerns.Text(delta["content"]))
		reasoning.WriteString(concerns.Text(delta["reasoning_content"]))
		for _, raw := range concerns.Items(delta["tool_calls"]) {
			call := concerns.Object(raw)
			function := concerns.Object(call["function"])
			if call["id"] != nil {
				calls = append(calls, map[string]any{"id": call["id"], "type": "function",
					"function": map[string]any{"name": function["name"], "arguments": ""}})
				continue
			}
			last := concerns.Object(calls[len(calls)-1])
			concerns.Object(last["function"])["arguments"] = concerns.Text(function["arguments"])
		}
		return nil
	})
	if err := concerns.ReadEventStream(context.Background(), bytes.NewReader(body), state.handle); err != nil {
		return nil, err
	}
	reason, err := state.finish()
	if err != nil {
		return nil, err
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
	if message["content"] == nil && message["tool_calls"] == nil {
		message["content"] = ""
	}
	result := map[string]any{
		"id": state.id, "object": "chat.completion", "created": state.created, "model": state.model,
		"choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": reason}},
	}
	if state.usage != nil {
		result["usage"] = state.usage
	}
	return json.Marshal(result)
}
