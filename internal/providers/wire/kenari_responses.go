package wire

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
)

// Kenari's /v1/responses honors only flat `function` tools. A `custom` (freeform)
// tool is silently dropped by the gateway and `text.format` is rejected with 400
// (open-sse/translator/concerns/responsesFunctionTools.js:1-6), yet Codex's shell
// tool is `custom` and its recap turns send `text.format`. The pin compensates in
// both directions (:8-13): the request turns each custom tool into a function tool
// with one freeform `input` string (:35-73), and the response turns the
// function_call the model answers with back into the custom_tool_call Codex
// expects. This file holds both halves; the response half is the point, the
// request half is what gives it the tool names it needs.

// ErrCustomToolArguments is the refusal for a function_call of a converted custom
// tool whose arguments are not a JSON object with a string `input`. The pin
// returns the raw arguments text as the freeform input then
// (responsesFunctionTools.js:160-167); a custom_tool_call that carries
// `{"foo":1}` as the text of a shell command is a wrong value that looks right,
// so Routeweft refuses it.
var ErrCustomToolArguments = errors.New("kenari_custom_tool_arguments_invalid")

// ErrResponsesEventTooLarge is the refusal for one Responses SSE event longer than
// maxResponsesEvent while a rewrite is pending: it cannot be inspected, and
// relaying it unchecked could pass a function_call through unconverted.
var ErrResponsesEventTooLarge = errors.New("kenari_responses_event_too_large")

// maxResponsesEvent is the largest SSE event the rewrite will buffer (the same
// bound the Responses relay applies, ingress copyTerminalSSE).
const maxResponsesEvent = 4 << 20

// ResponsesCustomToolNames reports the custom tools a Responses request declares,
// by trimmed name, for a provider whose Responses endpoint takes function tools
// only. It returns nil when the provider has no such quirk or the request has no
// custom tool, which is the signal not to wrap the response at all. The names are
// the ones responsesFunctionToolsOnly converts.
func ResponsesCustomToolNames(providerID string, request []byte) map[string]bool {
	if !For(providerID).ResponsesFunctionToolsOnly {
		return nil
	}
	var body Body
	if json.Unmarshal(request, &body) != nil {
		return nil
	}
	return customToolNames(body)
}

func customToolNames(body Body) map[string]bool {
	var tools []json.RawMessage
	if json.Unmarshal(body["tools"], &tools) != nil {
		return nil
	}
	// A malformed entry is skipped, as convertCustomTools skips it; it must not
	// disable the rewrite for the tools around it.
	var names map[string]bool
	for _, raw := range tools {
		var tool map[string]json.RawMessage
		if json.Unmarshal(raw, &tool) != nil || tool == nil {
			continue
		}
		if name, ok := customToolName(tool); ok {
			if names == nil {
				names = map[string]bool{}
			}
			names[name] = true
		}
	}
	return names
}

func customToolName(tool map[string]json.RawMessage) (string, bool) {
	var kind, name string
	if json.Unmarshal(tool["type"], &kind) != nil || kind != "custom" || json.Unmarshal(tool["name"], &name) != nil {
		return "", false
	}
	name = strings.TrimSpace(name)
	return name, name != ""
}

// responsesFunctionToolsOnly is the request half: quirks.responsesFunctionToolsOnly
// (kenari), pin chatCore.js:235-241 -> responsesFunctionTools.js:75-78. It strips
// text.format, replays custom_tool_call history as function_call history and
// declares each custom tool as a function tool. A custom tool with no name, or a
// history item whose freeform input is not a string and has no arguments to fall
// back on, is left as received (the pin writes `{}` there, which would erase the
// command the model ran).
func responsesFunctionToolsOnly(protocol string, body Body) bool {
	if protocol != openaiadapter.ResponsesProtocol {
		return false
	}
	changed := stripTextFormat(body)
	if convertCustomToolHistory(body) {
		changed = true
	}
	if convertCustomTools(body) {
		changed = true
	}
	return changed
}

// stripTextFormat: responsesFunctionTools.js:25-30.
func stripTextFormat(body Body) bool {
	var text map[string]json.RawMessage
	if json.Unmarshal(body["text"], &text) != nil || text == nil {
		return false
	}
	if _, ok := text["format"]; !ok {
		return false
	}
	delete(text, "format")
	return setJSON(body, "text", text)
}

// convertCustomTools: responsesFunctionTools.js:35-73.
func convertCustomTools(body Body) bool {
	var tools []json.RawMessage
	if json.Unmarshal(body["tools"], &tools) != nil {
		return false
	}
	changed := false
	for i, raw := range tools {
		var tool struct {
			Description string `json:"description"`
			Format      struct {
				Syntax     string `json:"syntax"`
				Definition string `json:"definition"`
			} `json:"format"`
		}
		var fields map[string]json.RawMessage
		if json.Unmarshal(raw, &fields) != nil || fields == nil {
			continue
		}
		name, ok := customToolName(fields)
		if !ok {
			continue
		}
		_ = json.Unmarshal(raw, &tool) // a non-string description or format is simply absent
		var description []string
		for _, part := range []string{tool.Description, joinNonEmpty("\n", tool.Format.Syntax, tool.Format.Definition)} {
			if part != "" {
				description = append(description, part)
			}
		}
		encoded, err := json.Marshal(map[string]any{
			"type":        "function",
			"name":        name,
			"description": strings.Join(description, "\n\n"),
			"parameters": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"input": map[string]any{"type": "string", "description": "Raw freeform input for this custom tool"},
				},
				"required":             []string{"input"},
				"additionalProperties": false,
			},
			"strict": false,
		})
		if err != nil {
			return false
		}
		tools[i], changed = encoded, true
	}
	if !changed {
		return false
	}
	return setJSON(body, "tools", tools)
}

// convertCustomToolHistory: responsesFunctionTools.js:89-122. The endpoint accepts
// function_call / function_call_output only, so the model must see its own earlier
// custom calls and their outputs in that shape.
func convertCustomToolHistory(body Body) bool {
	var input []json.RawMessage
	if json.Unmarshal(body["input"], &input) != nil {
		return false
	}
	changed := false
	for i, raw := range input {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil || item == nil {
			continue
		}
		var kind string
		_ = json.Unmarshal(item["type"], &kind)
		switch kind {
		case "custom_tool_call":
			arguments, ok := freeformArguments(item)
			if !ok {
				continue
			}
			delete(item, "input")
			item["type"] = json.RawMessage(`"function_call"`)
			item["arguments"] = arguments
			remapID(item, "ctc_", "fc_")
		case "custom_tool_call_output":
			output, err := json.Marshal(coerceOutput(item["output"]))
			if err != nil {
				continue
			}
			item["type"] = json.RawMessage(`"function_call_output"`)
			item["output"] = output
			remapID(item, "ctco_", "fco_")
		default:
			continue
		}
		encoded, err := json.Marshal(item)
		if err != nil {
			return false
		}
		input[i], changed = encoded, true
	}
	if !changed {
		return false
	}
	return setJSON(body, "input", input)
}

// freeformArguments is the `arguments` JSON the converted function tool declares:
// {"input": "<freeform>"}, or an arguments string the item already carries.
func freeformArguments(item map[string]json.RawMessage) (json.RawMessage, bool) {
	var freeform string
	if json.Unmarshal(item["input"], &freeform) == nil && len(item["input"]) > 0 && string(item["input"]) != "null" {
		encoded, err := json.Marshal(map[string]string{"input": freeform})
		if err != nil {
			return nil, false
		}
		quoted, err := json.Marshal(string(encoded))
		return quoted, err == nil
	}
	var existing string
	if json.Unmarshal(item["arguments"], &existing) == nil && existing != "" {
		return item["arguments"], true
	}
	return nil, false
}

// coerceOutput: open-sse/translator/formats/responsesApi.js:60-77, a
// function_call_output's output is always a string.
func coerceOutput(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return ""
	}
	var text string
	if json.Unmarshal(trimmed, &text) == nil {
		return text
	}
	var parts []json.RawMessage
	if json.Unmarshal(trimmed, &parts) != nil {
		return string(trimmed)
	}
	var out strings.Builder
	for _, part := range parts {
		var withText struct {
			Text *string `json:"text"`
		}
		if json.Unmarshal(part, &withText) == nil && withText.Text != nil {
			out.WriteString(*withText.Text)
			continue
		}
		out.Write(bytes.TrimSpace(part))
	}
	return out.String()
}

func remapID(item map[string]json.RawMessage, from, to string) {
	var id string
	if json.Unmarshal(item["id"], &id) == nil && strings.HasPrefix(id, from) {
		item["id"], _ = json.Marshal(to + strings.TrimPrefix(id, from))
	}
}

func joinNonEmpty(separator string, parts ...string) string {
	var kept []string
	for _, part := range parts {
		if part != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, separator)
}

// ---- response half ----

// customInput is the freeform text inside a converted tool's arguments
// (responsesFunctionTools.js:160-167), or ErrCustomToolArguments.
func customInput(name string, arguments json.RawMessage) (json.RawMessage, error) {
	var text string
	var parsed map[string]json.RawMessage
	if json.Unmarshal(arguments, &text) != nil || json.Unmarshal([]byte(text), &parsed) != nil {
		return nil, fmt.Errorf("%w: tool %q arguments are not a JSON object", ErrCustomToolArguments, name)
	}
	var input string
	// null unmarshals into a string without error, so the raw value must be a string.
	if raw := bytes.TrimSpace(parsed["input"]); len(raw) == 0 || raw[0] != '"' || json.Unmarshal(raw, &input) != nil {
		return nil, fmt.Errorf("%w: tool %q arguments carry no string \"input\"", ErrCustomToolArguments, name)
	}
	return parsed["input"], nil
}

// asCustomItem turns a function_call output item of a converted tool into the
// custom_tool_call item (:148-158). With parse false the input is empty, for the
// output_item.added event, which precedes the arguments.
func asCustomItem(item map[string]json.RawMessage, names map[string]bool, parse bool) (map[string]json.RawMessage, bool, error) {
	var kind, name string
	_ = json.Unmarshal(item["type"], &kind)
	_ = json.Unmarshal(item["name"], &name)
	if kind != "function_call" || !names[name] {
		return item, false, nil
	}
	input := json.RawMessage(`""`)
	if parse {
		var err error
		if input, err = customInput(name, item["arguments"]); err != nil {
			return nil, false, err
		}
	}
	out := make(map[string]json.RawMessage, len(item))
	for key, value := range item {
		out[key] = value
	}
	delete(out, "arguments")
	out["type"], out["input"] = json.RawMessage(`"custom_tool_call"`), input
	remapID(out, "fc_", "ctc_")
	return out, true, nil
}

func rewriteOutput(output json.RawMessage, names map[string]bool) (json.RawMessage, bool, error) {
	var entries []json.RawMessage
	if json.Unmarshal(output, &entries) != nil {
		return output, false, nil
	}
	changed := false
	for i, raw := range entries {
		var item map[string]json.RawMessage
		if json.Unmarshal(raw, &item) != nil || item == nil {
			continue
		}
		rewritten, ok, err := asCustomItem(item, names, true)
		if err != nil {
			return nil, false, err
		}
		if !ok {
			continue
		}
		encoded, err := json.Marshal(rewritten)
		if err != nil {
			return output, false, nil
		}
		entries[i], changed = encoded, true
	}
	if !changed {
		return output, false, nil
	}
	encoded, err := json.Marshal(entries)
	return encoded, err == nil, nil
}

// RewriteResponsesBody is the non-stream response half
// (rewriteResponsesCustomToolOutput, :253-261): function_call items of converted
// tools in `output` become custom_tool_call items. A body that is not a JSON
// object, or has none of them, is returned untouched; a converted call with
// unusable arguments is ErrCustomToolArguments.
func RewriteResponsesBody(names map[string]bool, body []byte) ([]byte, error) {
	if len(names) == 0 || !bytes.Contains(body, []byte("function_call")) {
		return body, nil
	}
	var response map[string]json.RawMessage
	if json.Unmarshal(body, &response) != nil || response == nil {
		return body, nil
	}
	output, changed, err := rewriteOutput(response["output"], names)
	if err != nil || !changed {
		return body, err
	}
	response["output"] = output
	return json.Marshal(response)
}

// ResponsesStream wraps a Responses SSE stream with the response half
// (createResponsesEventRewriter, :174-247). Events that mention no function_call
// are relayed byte for byte. A converted call with unusable arguments ends the
// stream with a response.failed event naming ErrCustomToolArguments.
func ResponsesStream(names map[string]bool, body io.ReadCloser) io.ReadCloser {
	if len(names) == 0 {
		return body
	}
	return &eventRewriter{source: body, reader: bufio.NewReaderSize(body, 32*1024), names: names, converted: map[string]bool{}}
}

type eventRewriter struct {
	source    io.Closer
	reader    *bufio.Reader
	names     map[string]bool
	converted map[string]bool // item ids announced as custom_tool_call
	sequence  int64           // last sequence_number relayed, for the injected terminal event
	pending   []byte
	err       error
}

func (r *eventRewriter) Close() error { return r.source.Close() }

func (r *eventRewriter) Read(out []byte) (int, error) {
	for len(r.pending) == 0 {
		if r.err != nil {
			return 0, r.err
		}
		r.pending, r.err = r.next()
	}
	n := copy(out, r.pending)
	r.pending = r.pending[n:]
	return n, nil
}

// next returns the next event, rewritten, and the error that follows it.
func (r *eventRewriter) next() ([]byte, error) {
	var event []byte
	for {
		line, err := r.reader.ReadSlice('\n')
		if len(event)+len(line) > maxResponsesEvent {
			return failedEvent(ErrResponsesEventTooLarge, r.sequence+1), io.EOF
		}
		event = append(event, line...)
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil || len(bytes.TrimRight(line, "\r\n")) == 0 && len(event) > len(line) {
			rewritten, rewriteErr := r.rewrite(event)
			if rewriteErr != nil {
				return failedEvent(rewriteErr, r.sequence+1), io.EOF
			}
			return rewritten, err
		}
	}
}

var sequenceKey = []byte(`"sequence_number":`)

// noteSequence remembers the last sequence_number an event carried.
func (r *eventRewriter) noteSequence(event []byte) {
	i := bytes.Index(event, sequenceKey)
	if i < 0 {
		return
	}
	rest := event[i+len(sequenceKey):]
	end := 0
	for end < len(rest) && (rest[end] == ' ' || rest[end] >= '0' && rest[end] <= '9') {
		end++
	}
	if n, err := strconv.ParseInt(string(bytes.TrimSpace(rest[:end])), 10, 64); err == nil {
		r.sequence = n
	}
}

func (r *eventRewriter) rewrite(event []byte) ([]byte, error) {
	r.noteSequence(event)
	if !bytes.Contains(event, []byte("function_call")) {
		return event, nil
	}
	var name string
	var data [][]byte
	for _, line := range bytes.Split(event, []byte("\n")) {
		line = bytes.TrimSuffix(line, []byte("\r"))
		switch {
		case bytes.HasPrefix(line, []byte("event:")):
			name = string(bytes.TrimSpace(line[len("event:"):]))
		case bytes.HasPrefix(line, []byte("data:")):
			data = append(data, bytes.TrimSpace(line[len("data:"):]))
		}
	}
	var payload map[string]json.RawMessage
	if len(data) == 0 || json.Unmarshal(bytes.Join(data, []byte("\n")), &payload) != nil || payload == nil {
		return event, nil
	}
	var kind string
	if json.Unmarshal(payload["type"], &kind) != nil || kind == "" {
		kind = name
	}
	hadEventLine := name != ""
	emit := func(eventName string) ([]byte, error) {
		encoded, err := json.Marshal(payload)
		if err != nil {
			return event, nil
		}
		var out bytes.Buffer
		if hadEventLine {
			out.WriteString("event: " + eventName + "\n")
		}
		out.WriteString("data: ")
		out.Write(encoded)
		out.WriteString("\n\n")
		return out.Bytes(), nil
	}
	itemID := func() string {
		var id string
		_ = json.Unmarshal(payload["item_id"], &id)
		return id
	}
	switch kind {
	case "response.output_item.added", "response.output_item.done":
		var item map[string]json.RawMessage
		if json.Unmarshal(payload["item"], &item) != nil || item == nil {
			return event, nil
		}
		var original string
		_ = json.Unmarshal(item["id"], &original)
		rewritten, ok, err := asCustomItem(item, r.names, kind == "response.output_item.done")
		if err != nil || !ok {
			return event, err
		}
		if original != "" {
			r.converted[original] = true
		}
		payload["item"], _ = json.Marshal(rewritten)
		return emit(name)
	case "response.function_call_arguments.delta":
		// The freeform input must arrive whole, so the JSON fragments are dropped
		// (:212-215).
		if r.converted[itemID()] {
			return nil, nil
		}
	case "response.function_call_arguments.done":
		id := itemID()
		var toolName string
		_ = json.Unmarshal(payload["name"], &toolName)
		if !r.converted[id] && !r.names[toolName] {
			return event, nil
		}
		input, err := customInput(toolName, payload["arguments"])
		if err != nil {
			return nil, err
		}
		const customDone = "response.custom_tool_call_input.done"
		delete(payload, "arguments")
		payload["input"] = input
		payload["type"], _ = json.Marshal(customDone)
		if strings.HasPrefix(id, "fc_") {
			payload["item_id"], _ = json.Marshal("ctc_" + strings.TrimPrefix(id, "fc_"))
		}
		return emit(customDone)
	case "response.completed", "response.done", "response.incomplete":
		// response.incomplete is what kenari sends on max_output_tokens, when a
		// call may be half emitted; it carries the same output as completed.
		var response map[string]json.RawMessage
		if json.Unmarshal(payload["response"], &response) != nil || response == nil {
			return event, nil
		}
		output, changed, err := rewriteOutput(response["output"], r.names)
		if err != nil || !changed {
			return event, err
		}
		response["output"] = output
		payload["response"], _ = json.Marshal(response)
		return emit(name)
	}
	return event, nil
}

// failedEvent is the terminal event a refused stream ends with, numbered after the last event relayed. The message names
// the tool, never the upstream's arguments.
func failedEvent(err error, sequence int64) []byte {
	code := ErrCustomToolArguments.Error()
	if errors.Is(err, ErrResponsesEventTooLarge) {
		code = ErrResponsesEventTooLarge.Error()
	}
	encoded, _ := json.Marshal(map[string]any{
		"type":            "response.failed",
		"sequence_number": sequence,
		"response":        map[string]any{"status": "failed", "error": map[string]any{"code": code, "message": err.Error()}},
	})
	return append(append([]byte("event: response.failed\ndata: "), encoded...), "\n\n"...)
}
