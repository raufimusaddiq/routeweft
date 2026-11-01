package wire

// The legacy Cursor ChatService path (executors/cursor.js:894-950,
// utils/cursorProtobuf.js:596-625, :955-1250). The pin sends the Agent Run
// stream for an agent-capable Chat (isAgentCapableRequest, :81-90) and this
// unary POST for every other body, which in practice is a turn carrying an
// image: AgentService takes text only, and ChatService is the alternative the
// pin keeps for it. The executor buffers this reply and decodes
// protobuf:response frames into Chat SSE (stream) or one Chat body (:955-1190).
//
// The wire shape is the pin's, byte for byte, wherever the pin has a byte
// (cursorProtobuf.js:301-593): every field, its number and its order, the
// Connect frame around it, and the rejection rules for a tool that is not a
// function. Map keys inside a tool schema are written sorted because Go's JSON
// decoder has no insertion order; a protobuf map field is unordered, and the
// Agent encoder already records the same divergence (PARITY V40).

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"compress/zlib"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
	cursorleg "github.com/raufimusaddiq/routeweft/internal/protocol/cursor"
)

const (
	cursorChatPath = "aiserver.v1.ChatService/StreamUnifiedChatWithTools" // registry/cursor.js:18
	cursorChatBase = "https://api2.cursor.sh"                             // registry/cursor.js:17
)

// cursorLegacyBase is the host the ChatService POST goes to. The registry's base
// is the catalog host (api2.cursor.sh); the Run stream is on agent.api5. An
// operator base is kept, so a test or relay sees this path where it sent the
// other one.
func cursorLegacyBase(raw *url.URL) string {
	if raw == nil {
		return cursorChatBase
	}
	switch raw.Hostname() {
	case "agent.api5.cursor.sh", "agentn.api5.cursor.sh":
		return cursorChatBase
	}
	return raw.Scheme + "://" + raw.Host
}

// cursorAgentCapable is isAgentCapableRequest (cursor.js:81-90): a non-empty
// messages array whose every content is a string, null, or a list of text
// parts. Anything else — an image, audio, a file, an unknown part, a content of
// another type, no messages at all — goes to the ChatService path at the pin.
func cursorAgentCapable(request map[string]any) bool {
	messages, ok := request["messages"].([]any)
	if !ok || len(messages) == 0 {
		return false
	}
	for _, raw := range messages {
		message := concerns.Object(raw)
		if message == nil {
			return false
		}
		switch content := message["content"].(type) {
		case nil, string:
		case []any:
			for _, rawPart := range content {
				switch part := rawPart.(type) {
				case nil, string:
				case map[string]any:
					if concerns.Text(part["type"]) != "text" {
						return false
					}
				default:
					return false
				}
			}
		default:
			return false
		}
	}
	return true
}

// cursorChatRun builds the ChatService request frame (generateCursorBody,
// cursorProtobuf.js:616-625). The messages are the pin translator's output
// (openai-to-cursor.js:52-170), so a text part beside an image is carried and
// the image part is not: the pin's encoder has no image field, and neither does
// this one (PARITY V41).
func cursorChatRun(body []byte, now time.Time) (frame []byte, model string, err error) {
	request, err := concerns.WireObject(body)
	if err != nil {
		return nil, "", cursorError(ErrCursorUnsupported, "request is not a JSON object")
	}
	model = concerns.Text(request["model"])
	if model == "" {
		return nil, "", cursorError(ErrCursorUnsupported, "a model is required")
	}
	messages, err := cursorleg.ChatMessages(request)
	if err != nil {
		return nil, "", cursorError(ErrCursorUnsupported, "%s", err)
	}
	tools, err := cursorChatTools(request["tools"])
	if err != nil {
		return nil, "", err
	}
	frame = cursorFrame(pbBytes(1, cursorChatRequest(messages, model, tools, cursorEffort(request), now, uuid.NewString)))
	if len(frame) > cursorRequestLimit {
		return nil, "", cursorError(ErrCursorFraming, "request frame exceeds the limit")
	}
	return frame, model, nil
}

// cursorChatRequest is encodeRequest (cursorProtobuf.js:301-593).
func cursorChatRequest(messages []any, model string, tools []byte, effort string, now time.Time, newID func() string) []byte {
	hasTools := len(tools) > 0
	var agentic uint64
	if hasTools {
		agentic = 1
	}
	messageIDs := make([][]byte, 0, len(messages))
	fields := make([][]byte, 0, len(messages)+32)
	for i, raw := range messages {
		message := concerns.Object(raw)
		role := uint64(2) // ROLE.ASSISTANT (:13)
		if concerns.Text(message["role"]) == "user" {
			role = 1
		}
		id := newID()
		encoded := pbJoin(
			pbString(1, concerns.Text(message["content"])), // MSG_CONTENT
			pbVarint(2, role),       // MSG_ROLE
			pbString(13, id),        // MSG_ID
			pbVarint(29, agentic),   // MSG_IS_AGENTIC
			pbVarint(47, 1+agentic), // MSG_UNIFIED_MODE: CHAT 1, AGENT 2
		)
		if i == len(messages)-1 && hasTools {
			encoded = append(encoded, pbBytes(51, []byte{1})...) // MSG_SUPPORTED_TOOLS: encodeVarint(1)
		}
		fields = append(fields, pbBytes(1, encoded)) // MESSAGES
		messageIDs = append(messageIDs, pbJoin(pbString(1, id), pbVarint(3, role)))
	}
	level := uint64(0) // THINKING_LEVEL (:18-20)
	switch effort {
	case "medium":
		level = 1
	case "high":
		level = 2
	}
	setting := pbJoin(
		pbString(1, `cursor\aisettings`),
		pbBytes(3, nil),
		pbBytes(6, pbJoin(pbBytes(1, nil), pbBytes(2, nil))),
		pbVarint(8, 1),
		pbVarint(9, 1),
	)
	metadata := pbJoin(
		pbString(1, cursorPlatform()),
		pbString(2, cursorArch()),
		pbString(3, "v20.0.0"),
		pbString(4, "/"),
		pbString(5, now.UTC().Format("2006-01-02T15:04:05.000Z")),
	)
	modeName := "Ask"
	if hasTools {
		modeName = "Agent"
	}
	fields = append(fields,
		pbVarint(2, 1),
		pbBytes(3, nil), // INSTRUCTION, empty
		pbVarint(4, 1),
		pbBytes(5, pbJoin(pbString(1, model), pbBytes(4, nil))),
		pbBytes(8, nil), // WEB_TOOL
		pbVarint(13, 1),
		pbBytes(15, setting),
		pbVarint(19, 1),
		pbString(23, newID()),
		pbBytes(26, metadata),
		pbVarint(27, agentic),
	)
	if hasTools {
		fields = append(fields, pbBytes(29, []byte{1})) // SUPPORTED_TOOLS: encodeVarint(1)
	}
	for _, id := range messageIDs {
		fields = append(fields, pbBytes(30, id))
	}
	fields = append(fields, tools)
	fields = append(fields,
		pbVarint(35, 0),
		pbVarint(38, 0),
		pbVarint(46, 1+agentic),
		pbBytes(47, nil),
		pbVarint(48, 1-agentic),
		pbVarint(49, level),
		pbVarint(51, 0),
		pbVarint(53, 1),
		pbString(54, modeName),
	)
	return pbJoin(fields...)
}

func cursorPlatform() string {
	switch runtime.GOOS {
	case "windows":
		return "win32"
	case "darwin":
		return "darwin"
	}
	return runtime.GOOS
}

func cursorArch() string {
	switch runtime.GOARCH {
	case "amd64":
		return "x64"
	case "arm64":
		return "arm64"
	}
	return runtime.GOARCH
}

// cursorChatTools is the MCP tool list of encodeRequest (:534-542) with
// encodeMcpTool (:270-284): name 1, description 2, the input schema as JSON 3,
// server "custom" 4. A tool that is not a named function is refused, as on the
// Agent path.
func cursorChatTools(value any) ([]byte, error) {
	var tools []byte
	for _, raw := range concerns.Items(value) {
		tool := concerns.Object(raw)
		function := concerns.Object(tool["function"])
		if function == nil {
			return nil, cursorError(ErrCursorUnsupported, "tool is not a function definition")
		}
		name := concerns.Text(function["name"])
		if name == "" {
			return nil, cursorError(ErrCursorUnsupported, "tool has no name")
		}
		parts := [][]byte{pbString(1, name)}
		if description := concerns.Text(function["description"]); description != "" {
			parts = append(parts, pbString(2, description))
		}
		if schema := function["parameters"]; schema != nil {
			if concerns.Object(schema) == nil {
				return nil, cursorError(ErrCursorUnsupported, "tool parameters must be an object")
			}
			encoded, err := json.Marshal(schema)
			if err != nil {
				return nil, cursorError(ErrCursorUnsupported, "tool schema: %v", err)
			}
			parts = append(parts, pbBytes(3, encoded))
		}
		parts = append(parts, pbString(4, "custom"))
		tools = append(tools, pbBytes(34, pbJoin(parts...))...)
	}
	return tools, nil
}

// --- reply: transformProtobufToSSE (cursor.js:1086-1250) ---

// cursorChatError is the pin's createErrorResponse (:389-411): a protocol error
// reported before any content ends the exchange with a JSON body and the status
// the pin chooses, not an SSE stream.
type cursorChatError struct {
	status int
	body   []byte
}

func (e *cursorChatError) Error() string {
	var parsed struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(e.body, &parsed) == nil && parsed.Error.Message != "" {
		return parsed.Error.Message
	}
	return fmt.Sprintf("cursor chat error %d", e.status)
}

// cursorChatDo sends the ChatService POST and puts the pin's Chat SSE in the
// response body. Non-2xx answers are returned as received, for the dispatcher to
// classify, as the Run stream does.
func cursorChatDo(client *http.Client, request *http.Request, raw []byte) (*http.Response, error) {
	frame, model, err := cursorChatRun(raw, time.Now())
	if err != nil {
		return nil, err
	}
	sent, err := http.NewRequestWithContext(request.Context(), http.MethodPost,
		strings.TrimRight(cursorLegacyBase(request.URL), "/")+"/"+cursorChatPath, bytes.NewReader(frame))
	if err != nil {
		return nil, err
	}
	sent.Header = request.Header.Clone()
	sent.Header.Set("Content-Type", cursorContentType)
	sent.Header.Del("Accept")
	response, err := client.Do(sent)
	if err != nil {
		return nil, err
	}
	if response.StatusCode != http.StatusOK {
		return response, nil
	}
	body, err := io.ReadAll(io.LimitReader(response.Body, concerns.MaxTranslatedResponseBytes+1))
	response.Body.Close()
	if err != nil {
		return nil, err
	}
	if len(body) > concerns.MaxTranslatedResponseBytes {
		return nil, cursorError(ErrCursorFraming, "reply exceeds the translated-response limit")
	}
	var out bytes.Buffer
	if err := cursorChatStream(request.Context(), body, &out, model); err != nil {
		var report *cursorChatError
		if errors.As(err, &report) {
			return &http.Response{
				StatusCode: report.status,
				Status:     fmt.Sprintf("%d %s", report.status, http.StatusText(report.status)),
				Header:     http.Header{"Content-Type": []string{"application/json"}},
				Body:       io.NopCloser(bytes.NewReader(report.body)),
				Request:    request,
			}, nil
		}
		return nil, err
	}
	response.Body = io.NopCloser(bytes.NewReader(out.Bytes()))
	response.Header = response.Header.Clone()
	response.Header.Set("Content-Type", "text/event-stream")
	response.Header.Del("Content-Length")
	response.Header.Del("Content-Encoding")
	response.ContentLength, response.Uncompressed = -1, true
	return response, nil
}

// cursorChatStream decodes the buffered reply as the pin's
// transformProtobufToSSE does. Every frame is processed, a frame may be gzipped,
// zlib-deflated or raw-deflated (:299-357), a JSON error frame before content is
// the pin's error response (:1105-1120), and the end of the body is the end of
// the turn. The final chunk carries no usage: Routeweft makes none (PARITY V32
// and V8), where the pin estimates one.
func cursorChatStream(ctx context.Context, body []byte, dst io.Writer, model string) error {
	writer := &concerns.SSEWriter{Ctx: ctx, Dst: dst}
	id := fmt.Sprintf("chatcmpl-cursor-%d", time.Now().UnixMilli())
	created := time.Now().Unix()
	chunk := func(delta map[string]any, finish any) error {
		return writer.Event("", map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
	}
	composer := cursorComposer.MatchString(model[strings.LastIndex(model, "/")+1:])
	type call struct {
		id, name, arguments string
		index               int
	}
	var (
		order    []string
		calls    = map[string]*call{}
		text     strings.Builder
		thinking strings.Builder
		visible  int
		chunks   int
	)
	writeText := func(delta string) error {
		chunks++
		if chunks == 1 && len(order) == 0 {
			return chunk(map[string]any{"role": "assistant", "content": delta}, nil)
		}
		return chunk(map[string]any{"content": delta}, nil)
	}
	writeCall := func(c *call, arguments string) error {
		chunks++
		return chunk(map[string]any{"tool_calls": []any{map[string]any{
			"index": c.index, "id": c.id, "type": "function",
			"function": map[string]any{"name": c.name, "arguments": arguments},
		}}}, nil)
	}
	offset := 0
	for offset < len(body) {
		frame, ok, err := cursorChatFrame(body, offset)
		if err != nil {
			return err
		}
		if !ok {
			break
		}
		offset = frame.next
		if frame.json != nil {
			if chunks == 0 && text.Len() == 0 && len(order) == 0 {
				return frame.json
			}
			break
		}
		result := frame.result
		if result.call != nil {
			// The pin pushes the assistant role before the first tool call when
			// nothing has been emitted yet (:1160-1167).
			if chunks == 0 {
				chunks++
				if err := chunk(map[string]any{"role": "assistant", "content": ""}, nil); err != nil {
					return err
				}
			}
			incoming := result.call
			if existing, ok := calls[incoming.id]; ok {
				existing.arguments += incoming.arguments
				if incoming.arguments != "" {
					if err := writeCall(existing, incoming.arguments); err != nil {
						return err
					}
				}
			} else {
				calls[incoming.id] = &call{id: incoming.id, name: incoming.name, arguments: incoming.arguments, index: len(order)}
				order = append(order, incoming.id)
				if err := writeCall(calls[incoming.id], incoming.arguments); err != nil {
					return err
				}
			}
		}
		if result.text != "" {
			text.WriteString(result.text)
			if err := writeText(result.text); err != nil {
				return err
			}
		}
		if composer && result.thinking != "" {
			thinking.WriteString(result.thinking)
			visibleContent := cursorVisibleAfterThinking(thinking.String())
			if len(visibleContent) > visible {
				delta := visibleContent[visible:]
				visible = len(visibleContent)
				text.WriteString(delta)
				if err := writeText(delta); err != nil {
					return err
				}
			}
		}
	}
	if chunks == 0 && len(order) == 0 {
		if err := chunk(map[string]any{"role": "assistant", "content": ""}, nil); err != nil {
			return err
		}
	}
	reason := "stop"
	if len(order) > 0 {
		reason = "tool_calls"
	}
	if err := chunk(map[string]any{}, reason); err != nil {
		return err
	}
	return writer.Done()
}

type cursorChatFrameResult struct {
	next   int
	result cursorChatResult
	json   *cursorChatError
}

type cursorChatResult struct {
	text, thinking string
	call           *cursorChatCall
}

type cursorChatCall struct{ id, name, arguments string }

// cursorChatFrame is readCursorFrame (:299-357) plus the JSON error guard
// (:1105-1120). A short or incomplete frame ends the loop, as at the pin.
func cursorChatFrame(body []byte, offset int) (cursorChatFrameResult, bool, error) {
	if offset+5 > len(body) {
		return cursorChatFrameResult{}, false, nil
	}
	flags := body[offset]
	length := int(uint32(body[offset+1])<<24 | uint32(body[offset+2])<<16 | uint32(body[offset+3])<<8 | uint32(body[offset+4]))
	if length > cursorFrameLimit {
		return cursorChatFrameResult{}, false, cursorError(ErrCursorFraming, "frame of %d bytes exceeds the limit", length)
	}
	if offset+5+length > len(body) {
		return cursorChatFrameResult{}, false, nil
	}
	next := offset + 5 + length
	payload := cursorDecompress(body[offset+5:next], flags)
	// The pin builds an error response from any frame whose bytes start with '{' and
	// contain `"error"` when JSON.parse succeeds, and treats an unparseable one as a
	// normal frame (:1105-1120). So the parse decides, not the substring alone.
	if len(payload) > 0 && payload[0] == '{' && bytes.Contains(payload, []byte(`"error"`)) {
		var parsed any
		if json.Unmarshal(payload, &parsed) == nil {
			return cursorChatFrameResult{next: next, json: cursorChatJSONError(payload)}, true, nil
		}
	}
	return cursorChatFrameResult{next: next, result: cursorChatExtract(payload)}, true, nil
}

// cursorDecompress is decompressPayload (:299-357): gzip, then zlib, then raw
// deflate; the payload unchanged when none of them apply.
func cursorDecompress(payload []byte, flags byte) []byte {
	if len(payload) > 10 && payload[0] == '{' && payload[1] == '"' && bytes.HasPrefix(payload, []byte(`{"error"`)) {
		return payload
	}
	if flags&3 == 0 {
		return payload
	}
	for _, decode := range []func([]byte) ([]byte, error){gzipBytes, zlibBytes, flateBytes} {
		if data, err := decode(payload); err == nil {
			return data
		}
	}
	return payload
}

func gzipBytes(payload []byte) ([]byte, error) {
	reader, err := gzip.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(io.LimitReader(reader, cursorFrameLimit+1))
}

func zlibBytes(payload []byte) ([]byte, error) {
	reader, err := zlib.NewReader(bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	defer reader.Close()
	return io.ReadAll(io.LimitReader(reader, cursorFrameLimit+1))
}

func flateBytes(payload []byte) ([]byte, error) {
	return io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(payload)), cursorFrameLimit+1))
}

func cursorChatJSONError(payload []byte) *cursorChatError {
	var body struct {
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
			Details []struct {
				Debug struct {
					Error   string `json:"error"`
					Details struct {
						Title  string `json:"title"`
						Detail string `json:"detail"`
					} `json:"details"`
				} `json:"debug"`
			} `json:"details"`
		} `json:"error"`
	}
	_ = json.Unmarshal(payload, &body)
	message, code := body.Error.Message, "unknown"
	if len(body.Error.Details) > 0 {
		detail := body.Error.Details[0].Debug
		if detail.Details.Title != "" {
			message = detail.Details.Title
		} else if detail.Details.Detail != "" {
			message = detail.Details.Detail
		}
		if detail.Error != "" {
			code = detail.Error
		}
	}
	if message == "" {
		message = "API Error"
	}
	kind, status := "api_error", http.StatusBadRequest
	if body.Error.Code == "resource_exhausted" {
		kind, status = "rate_limit_error", http.StatusTooManyRequests
	}
	encoded, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message, "type": kind, "code": code}})
	return &cursorChatError{status: status, body: encoded}
}

// cursorChatExtract is extractTextFromResponse (cursorProtobuf.js:802-873):
// field 1 is a ClientSideToolV2Call, field 2 a StreamUnifiedChatResponse whose
// text is 1 and thinking 25{1}. Anything else is empty.
func cursorChatExtract(payload []byte) cursorChatResult {
	fields, err := pbParse(payload)
	if err != nil {
		return cursorChatResult{}
	}
	if raw, ok := fields.first(1); ok && raw.kind == pbLenType {
		if call := cursorChatCallFrom(raw.data); call != nil {
			return cursorChatResult{call: call}
		}
	}
	raw, ok := fields.first(2)
	if !ok || raw.kind != pbLenType {
		return cursorChatResult{}
	}
	response, err := pbParse(raw.data)
	if err != nil {
		return cursorChatResult{}
	}
	result := cursorChatResult{}
	if text, err := response.text(1); err == nil {
		result.text = text
	}
	if thinking, ok, err := response.message(25); err == nil && ok {
		if text, err := thinking.text(1); err == nil {
			result.thinking = text
		}
	}
	return result
}

// cursorChatCallFrom is extractToolCall (cursorProtobuf.js:721-782).
func cursorChatCallFrom(data []byte) *cursorChatCall {
	fields, err := pbParse(data)
	if err != nil {
		return nil
	}
	read := func(number uint64) string {
		value, err := fields.text(number)
		if err != nil {
			return ""
		}
		return value
	}
	id, name, arguments := read(3), read(9), ""
	if first, _, ok := strings.Cut(id, "\n"); ok {
		id = first
	}
	if mcp, ok, err := fields.message(27); err == nil && ok {
		if list, ok, err := mcp.message(1); err == nil && ok {
			if nested := list.textOrEmpty(1); nested != "" {
				name = nested
			}
			arguments = list.textOrEmpty(3)
		}
	}
	if arguments == "" {
		arguments = read(10)
	}
	if id == "" || name == "" {
		return nil
	}
	if arguments == "" {
		arguments = "{}"
	}
	return &cursorChatCall{id: id, name: name, arguments: arguments}
}

func (m pbMessage) textOrEmpty(number uint64) string {
	value, err := m.text(number)
	if err != nil {
		return ""
	}
	return value
}

// cursorChatDispatches reports whether a Chat body takes the ChatService path,
// and the parse failure the Run path would also report.
func cursorChatDispatches(raw []byte) (bool, error) {
	request, err := concerns.WireObject(raw)
	if err != nil {
		return false, cursorError(ErrCursorUnsupported, "request is not a JSON object")
	}
	return !cursorAgentCapable(request), nil
}
