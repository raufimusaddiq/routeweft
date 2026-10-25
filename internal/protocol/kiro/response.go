package kiro

import (
	"bytes"
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
	unmeasured   int // metricsEvents that carried no count
	usage        map[string]any
	delivered    bool // a content or reasoning delta has reached the caller
	frames       int  // validated frames handled
	deltas       int  // deltas handed to emit
	emittedCalls int  // tool calls emitted by finish
	toolError    string
	provenance   string // what ended the answer, as the pin names it (executors/kiro.js:878,886)
	explicitStop bool
	onFrame      func()
	pinDelta     func(map[string]any) error // sizes what the pin would have written
	pinThinking  bool
	pinReasoned  bool
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

// corruptFrameError is a frame whose payload is not one JSON document: the pin's
// parseEventFrame throws and the answer ends as a corrupt frame (executors/kiro.js:1295-1299, :964-967).
type corruptFrameError struct{ message string }

func (e *corruptFrameError) Error() string { return e.message }

func decodeAny(payload []byte) (any, bool, error) {
	if len(bytes.TrimSpace(payload)) == 0 {
		return nil, false, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, false, &corruptFrameError{"AWS EventStream payload is not valid JSON (" + err.Error() + ")"}
	}
	if decoder.Decode(new(any)) != io.EOF {
		return nil, false, &corruptFrameError{"AWS EventStream payload is not valid JSON (data after the document)"}
	}
	return value, true, nil
}

func (m *machine) deliver(delta map[string]any) error {
	m.delivered = true
	m.deltas++
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

// Dispositions of a stop reason (stopDisposition, executors/kiro.js:159-168).
const (
	dispComplete   = "complete"
	dispToolUse    = "tool_use"
	dispLength     = "length"
	dispRetryable  = "retryable_protocol_failure"
	dispIncomplete = "terminal_incomplete"
	dispRefusal    = "terminal_refusal"
	dispUnknown    = "unknown_failure"
)

func disposition(reason string, hasTools bool) string {
	switch {
	case reason == "malformed_model_output" || reason == "invalid_model_output":
		return dispRetryable
	case reason == "cancelled" || reason == "pause_turn" || reason == "model_context_window_exceeded":
		return dispIncomplete
	case reason == "refusal" || refusalPattern.MatchString(reason):
		return dispRefusal
	case reason == "max_tokens":
		if hasTools {
			return dispIncomplete
		}
		return dispLength
	case reason != "" && reason != "end_turn" && reason != "tool_use":
		return dispUnknown
	case hasTools || reason == "tool_use":
		return dispToolUse
	}
	return dispComplete
}

// severity orders stop reasons so a later, milder one never hides a failure
// (mergeStopReason, executors/kiro.js:170-183).
func severity(reason string) int {
	switch disposition(reason, false) {
	case dispRefusal:
		return 6
	case dispIncomplete:
		return 5
	case dispUnknown:
		return 4
	case dispRetryable:
		return 3
	case dispLength:
		return 2
	}
	return 1
}

func (m *machine) stop(reason, provenance string) {
	if reason == "" {
		return
	}
	if m.stopReason == "" || severity(reason) > severity(m.stopReason) || (reason == "tool_use" && m.stopReason == "end_turn") {
		m.stopReason = reason
		m.provenance = provenance
	}
}

// jsTruthy and jsText are JavaScript's truthiness and String(value || "") on a decoded
// JSON value, which is how the pin reads loosely typed fields (executors/kiro.js:146, :880-882).
func jsTruthy(value any) bool {
	switch v := value.(type) {
	case nil:
		return false
	case bool:
		return v
	case string:
		return v != ""
	case json.Number:
		f, err := v.Float64()
		return err != nil || f != 0
	}
	return true
}

func jsText(value any) string {
	if !jsTruthy(value) {
		return ""
	}
	switch v := value.(type) {
	case string:
		return v
	case bool:
		return "true"
	case json.Number:
		return v.String()
	case []any:
		parts := make([]string, len(v))
		for i, item := range v {
			if item != nil {
				parts[i] = jsText(item)
			}
		}
		return strings.Join(parts, ",")
	}
	return "[object Object]"
}

// firstSet is JavaScript's a ?? b.
func firstSet(object map[string]any, keys ...string) any {
	for _, key := range keys {
		if value := object[key]; value != nil {
			return value
		}
	}
	return nil
}

// handle reads one frame the way the pin's processEvent does (executors/kiro.js:792-923):
// fields are read loosely, an event it has no branch for is counted under "other" and
// ignored, and a payload that is not one JSON document ends the answer as a corrupt
// frame. terminal is true for the event that carries a stop reason.
func (m *machine) handle(event concerns.EventStreamEvent) (bool, error) {
	payload, _, err := decodeAny(event.Payload)
	if err != nil {
		return false, err
	}
	object := concerns.Object(payload)
	if kind, _ := event.Headers[":message-type"].(string); kind == "error" || kind == "exception" {
		message := "Kiro upstream sent an EventStream " + kind
		if text, _ := object["message"].(string); text != "" {
			message = text
		}
		return false, failure("upstream_eventstream_error", message)
	}
	kind, _ := event.Headers[":event-type"].(string)
	m.frames++
	if m.onFrame != nil {
		m.onFrame()
	}
	m.counts[eventCountKey(kind)]++
	switch kind {
	case "assistantResponseEvent":
		if content, isText := object["content"].(string); isText {
			return false, m.answerText(content)
		}
	case "reasoningContentEvent":
		return false, m.reasoning(payload)
	case "codeEvent":
		if content, isText := object["content"].(string); isText {
			// Any string content makes this output, an empty one included: the pin sets
			// hasCode before it looks at the length (executors/kiro.js:842-845), and
			// hasOutput counts it (:1065), so an answer of one empty codeEvent completes.
			m.delivered = true
			if m.pinDelta != nil {
				if err := m.pinDelta(map[string]any{"content": content}); err != nil {
					return false, err
				}
			}
			if content == "" {
				return false, nil
			}
			return false, m.deliver(map[string]any{"content": content})
		}
	case "toolUseEvent":
		err := m.toolUse(payload)
		var fragment *toolFragmentError
		if errors.As(err, &fragment) {
			// One rejected fragment belongs to one tool: the pin logs it and keeps the
			// rest of the turn (executors/kiro.js:972-980).
			if m.toolError == "" {
				m.toolError = fragment.message
			}
			return false, nil
		}
		return false, err
	case "messageStopEvent":
		reason := normalizeStopReason(jsText(firstSet(object, "stopReason", "stop_reason")))
		if reason == "" {
			reason = "end_turn"
			if m.sawToolUse {
				reason = "tool_use"
			}
		}
		m.stop(reason, "message_stop_event")
		m.explicitStop = true
		return true, nil
	case "metadataEvent", "MetadataEvent":
		metadata := object
		for _, key := range []string{"metadataEvent", "metadata"} {
			if jsTruthy(object[key]) {
				metadata = concerns.Object(object[key])
				break
			}
		}
		if reason := normalizeStopReason(jsText(firstSet(metadata, "stopReason", "stop_reason"))); reason != "" {
			m.stop(reason, "metadata_stop_reason")
			m.explicitStop = true
			return true, nil
		}
	case "metricsEvent":
		// Counts only; no estimates or credit accounting (PARITY 6.8).
		metrics := object
		if jsTruthy(object["metricsEvent"]) {
			metrics = concerns.Object(object["metricsEvent"])
		}
		if usage, observed := concerns.KiroUsage(metrics); observed {
			m.usage = usage.OpenAI()
		} else {
			m.unmeasured++
		}
	}
	// contextUsageEvent, meteringEvent and every other type: counted, never content.
	return false, nil
}

// answerText is an assistantResponseEvent's text. What reaches the client is text();
// what the pin would have written is pinText(), which the integrity bound is counted on.
func (m *machine) answerText(content string) error {
	if m.pinDelta != nil {
		if err := m.pinText(content); err != nil {
			return err
		}
	}
	return m.text(content)
}

// pinText is the pin's tag reader (executors/kiro.js:808-833), kept only to size the
// converted output it would have produced: it always drops what is inside
// <thinking> tags (the request does not matter), keeps a tag that two frames split,
// and writes an empty content delta when nothing has been reasoned yet.
func (m *machine) pinText(content string) error {
	trimNewline := func(s string) string { return strings.TrimPrefix(s, "\n") }
	if m.pinThinking {
		end := strings.Index(content, closeTag)
		if end < 0 {
			content = ""
		} else {
			m.pinThinking = false
			content = trimNewline(content[end+len(closeTag):])
		}
	} else if start := strings.Index(content, openTag); start >= 0 {
		end := strings.Index(content[start+len(openTag):], closeTag)
		if end < 0 {
			m.pinThinking = true
			content = content[:start]
		} else {
			content = content[:start] + trimNewline(content[start+len(openTag)+end+len(closeTag):])
		}
	}
	if content != "" || !m.pinReasoned {
		return m.pinDelta(map[string]any{"content": content})
	}
	return nil
}

// reasoning is the pin's reasoningContentEvent (executors/kiro.js:834-841): the text is
// payload.reasoningContentEvent or the payload itself, as a string or its text/content.
func (m *machine) reasoning(payload any) error {
	object := concerns.Object(payload)
	value := payload
	if jsTruthy(object["reasoningContentEvent"]) {
		value = object["reasoningContentEvent"]
	}
	var text string
	switch v := value.(type) {
	case string:
		text = v
	case map[string]any:
		for _, key := range []string{"text", "content"} {
			if found, isText := v[key].(string); isText && found != "" {
				text = found
				break
			}
		}
	}
	if text == "" {
		return nil
	}
	m.pinReasoned = true
	if m.pinDelta != nil {
		if err := m.pinDelta(map[string]any{"reasoning_content": text}); err != nil {
			return err
		}
	}
	return m.deliver(map[string]any{"reasoning_content": text})
}

func (m *machine) toolUse(payload any) error {
	m.sawToolUse = true
	var values []any
	switch v := payload.(type) {
	case []any:
		values = v
	case nil:
	default:
		values = []any{v}
	}
	if len(values) == 0 || values[0] == nil {
		return fragment("Kiro toolUseEvent is empty")
	}
	for _, raw := range values {
		value := concerns.Object(raw)
		if value == nil {
			return fragment("Kiro toolUseEvent holds something that is not an object")
		}
		// stop is a fragment marker the pin never reads; the message stop event owns
		// terminal semantics.
		name, isText := value["name"].(string)
		if !isText || strings.TrimSpace(name) == "" {
			return fragment("Kiro toolUseEvent is missing a tool name")
		}
		name = strings.TrimSpace(name)
		var id string
		switch v := value["toolUseId"].(type) {
		case nil:
			// An absent and a null id are the same to the pin (executors/kiro.js:854).
			id = concerns.GenerateToolCallID(0, len(m.tools), name)
		case string:
			if strings.TrimSpace(v) == "" {
				return fragment("Kiro toolUseEvent has an invalid toolUseId")
			}
			id = v
		default:
			return fragment("Kiro toolUseEvent has an invalid toolUseId")
		}
		tool := m.toolsByID[id]
		if tool == nil {
			tool = &toolBuffer{id: id, name: name}
			m.toolsByID[id] = tool
			m.tools = append(m.tools, tool)
			m.toolBytes += len(id) + len(name) + 32
		} else if tool.name != name {
			return fragment("Kiro tool name changed between fragments")
		}
		input, set := value["input"]
		if set {
			switch v := input.(type) {
			case string:
				if tool.kind != "" && tool.kind != "string" {
					return fragment("Kiro tool input changed fragment type")
				}
				tool.kind = "string"
				tool.text = append(tool.text, v)
				m.toolBytes += len(v)
			case map[string]any:
				if tool.kind != "" && tool.kind != "object" {
					return fragment("Kiro tool input changed fragment type")
				}
				tool.kind = "object"
				m.toolBytes -= tool.bytes
				tool.object = v
				tool.bytes = len(concerns.JSONText(v))
				m.toolBytes += tool.bytes
			default:
				return fragment("Kiro tool input must be a JSON object")
			}
		}
		if m.toolBytes > m.options.MaxToolBytes {
			return failure("upstream_buffer_exceeded", "Kiro buffered tool input exceeded its memory bound")
		}
	}
	return nil
}

// toolFragmentError is a tool fragment the pin rejects without ending the turn
// (executors/kiro.js:972-980): the fragment belongs to one tool.
type toolFragmentError struct{ message string }

func (e *toolFragmentError) Error() string { return e.message }

func fragment(message string) error { return &toolFragmentError{message} }

// toolCalls validates the buffered calls one by one. An unusable call is dropped
// and remembered, and the usable ones are kept: the pin validates per tool, not
// per turn, so one bad fragment never takes the others with it
// (executors/kiro.js:738-759).
func (m *machine) toolCalls() []map[string]any {
	var calls []map[string]any
	for _, tool := range m.tools {
		call, err := m.toolCall(tool)
		if err != nil {
			if m.toolError == "" {
				m.toolError = err.Error()
			}
			continue
		}
		calls = append(calls, call)
	}
	return calls
}

func (m *machine) toolCall(tool *toolBuffer) (map[string]any, error) {
	var input map[string]any
	switch tool.kind {
	case "":
		return nil, errors.New("Kiro tool call is missing input")
	case "object":
		input = tool.object
	default:
		joined := strings.Join(tool.text, "")
		// A blank input is not JSON: the pin's JSON.parse("") throws and the call is
		// dropped (executors/kiro.js:731-735).
		parsed, err := concerns.WireObject([]byte(joined))
		if err != nil {
			return nil, fmt.Errorf("Kiro tool input must be valid object JSON (%v)", err)
		}
		input = parsed
	}
	if tool.name == "tool_call" {
		if nested, _ := input["name"].(string); strings.TrimSpace(nested) == "" {
			return nil, errors.New("Invalid Kiro tool_call payload: missing nested MCP tool name")
		}
		if _, set := input["arguments"]; !set {
			return nil, errors.New("Invalid Kiro tool_call payload: missing nested MCP tool arguments")
		}
	}
	return map[string]any{"id": tool.id, "name": m.options.Names.Original(tool.name), "arguments": concerns.JSONText(input)}, nil
}

// truncationReasons are the stops that mean "usable as far as it got, then the
// budget ran out"; cancelled and pause_turn are abandoned turns whose partial
// content stays private (executors/kiro.js:153-157).
func truncationReason(reason string) bool {
	return reason == "model_context_window_exceeded" || reason == "max_tokens"
}

// finish ends the answer after the reader returned cleanly: held text, tool
// calls, then the finish reason, in the order the pin decides them
// (executors/kiro.js:996-1133). It returns the finish reason; an answer that did
// not finish is an error. The stop reason is judged before the tool calls are
// validated, so an authoritative failure is never reported as a tool defect.
func (m *machine) finish() (string, error) {
	// A stop event is not required: the pin completes an answer that ends cleanly
	// without a stop event (executors/kiro.js:1008-1075); it fails only one that
	// produced nothing at all (below).
	if err := m.flushText(); err != nil {
		return "", err
	}
	reason := m.stopReason
	declared := disposition(reason, m.sawToolUse)
	// A truncation after output keeps what was streamed and closes as length (:1014-1018).
	keptTruncation := truncationReason(reason) && m.deltas > 0
	switch declared {
	case dispRetryable, dispIncomplete, dispRefusal, dispUnknown:
		if !keptTruncation {
			return "", m.stopFailure(reason, declared)
		}
	}
	calls := m.toolCalls()
	// A declared tool turn that produced nothing usable is fatal only when the
	// turn produced nothing else (:787-790, :1053-1063).
	if len(calls) == 0 && !m.delivered {
		if reason == "tool_use" {
			return "", m.toolFailure("Kiro tool_use stop reason did not include a complete tool call")
		}
		if m.toolError != "" {
			return "", m.toolFailure(m.toolError)
		}
	}
	if len(calls) == 0 && !m.delivered && !m.explicitStop {
		return "", m.fail(pinFailure{code: "kiro_missing_terminal", message: "Kiro EventStream ended without model output", provenance: "empty_response_eof", transport: "clean_eof", disposition: dispIncomplete})
	}
	final := disposition(reason, len(calls) > 0)
	truncated := final == dispIncomplete && truncationReason(reason) && (m.deltas > 0 || len(calls) > 0)
	switch final {
	case dispRetryable, dispIncomplete, dispRefusal, dispUnknown:
		if !truncated {
			return "", m.stopFailure(reason, final)
		}
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
		m.emittedCalls++
	}
	switch {
	case truncated:
		return concerns.FinishLength, nil
	case len(calls) > 0:
		return concerns.FinishToolCalls, nil
	case final == dispLength:
		return concerns.FinishLength, nil
	}
	return concerns.FinishStop, nil
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
	reason, err := state.run(ctx, src)
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
	reason, err := state.run(context.Background(), bytes.NewReader(body))
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
