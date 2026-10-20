package wire

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// Cursor AgentService (executors/cursor.js executeAgent, :584-892): one POST of
// agent.v1.AgentService/Run on agent.api5.cursor.sh (registry/cursor.js:49,
// cursor.js:49,597) carrying application/connect+proto, a Connect *streaming*
// exchange over HTTP/2 (cursor.js:486-488, 490-582): the request body stays open
// after the first frame because the server asks questions mid-turn (key-value
// blobs, IDE context, IDE tools) that the client answers on the same stream
// (cursor.js:727-779). The reply is a sequence of Connect frames, each a
// 5-byte header (flags, big-endian length) and an AgentServerMessage
// (cursor.js:187-201).
//
// No protobuf library is used. The only message the pin encodes beyond tagged
// scalars is google.protobuf.Value (cursorProtobuf.js:890-948), which is about
// sixty lines here; pulling a module in for it would be a dependency for one
// recursive function.

var (
	// ErrCursorFraming: the Connect framing or a protobuf message on the wire is
	// malformed, or the stream ended before the turn did. Nothing is guessed.
	ErrCursorFraming = errors.New("cursor_connect_framing")
	// ErrCursorUnsupported: the request or the server asks for something this
	// transport cannot carry or answer.
	ErrCursorUnsupported = errors.New("cursor_connect_unsupported")
	// ErrCursorHTTP2: the Run stream needs full-duplex HTTP/2 (cursor.js:486-488).
	ErrCursorHTTP2 = errors.New("cursor_connect_requires_http2")
	// ErrCursorEmptyTurn: the turn ended with no visible text (cursor.js:681).
	ErrCursorEmptyTurn = errors.New("cursor_connect_empty_turn")
)

const (
	cursorRunPath      = "agent.v1.AgentService/Run" // cursor.js:49
	cursorAgentBase    = "https://agent.api5.cursor.sh"
	cursorCatalogBase  = "https://api2.cursor.sh" // the registry's default base URL
	cursorFrameLimit   = 4 << 20                  // one reply frame, before and after gzip
	cursorRequestLimit = 32 << 20                 // the Chat body in, and the Run frame out
	cursorMaxDepth     = 64
	cursorContentType  = "application/connect+proto"
	cursorRejectedTool = "Tool not available in this environment. Use the MCP tools provided instead." // cursor.js:238
)

func cursorError(kind error, format string, args ...any) error {
	return fmt.Errorf("%w: %s", kind, fmt.Sprintf(format, args...))
}

// cursorTarget is the agent host and the Run path (cursor.js:597). The registry's
// default base is the catalog host api2.cursor.sh, which does not serve the
// agent; a base the operator set (a test or relay) is kept. The pin resolves
// "default"/"auto" through the account's model catalog (cursor.js:604-612), a
// network call this hook cannot make, so those are refused by name.
func cursorTarget(provider routing.ProviderRef) (Target, error) {
	if provider.UpstreamModel == "default" || provider.UpstreamModel == "auto" {
		return Target{}, cursorError(ErrCursorUnsupported, "model %q needs the account's model catalog; select an explicit model", provider.UpstreamModel)
	}
	base := provider.BaseURL
	if strings.TrimRight(base, "/") == cursorCatalogBase || strings.TrimSpace(base) == "" {
		base = cursorAgentBase
	}
	return Target{BaseURL: base, Endpoint: cursorRunPath}, nil
}

// cursorChecksum is the Jyh cipher (utils/cursorChecksum.js:43-85). The pin
// takes floor(ms/1e6) and shifts it by 40, 32, 24, 16, 8, 0; JavaScript shifts
// count modulo 32, so the first two bytes repeat the shifts 8 and 0.
func cursorChecksum(now time.Time, machineID string) string {
	stamp := uint32(now.UnixMilli() / 1000000)
	shifts := [6]uint{8, 0, 24, 16, 8, 0}
	var cipher [6]byte
	key := byte(165)
	for i, shift := range shifts {
		cipher[i] = (byte(stamp>>shift) ^ key) + byte(i)
		key = cipher[i]
	}
	return base64.RawURLEncoding.EncodeToString(cipher[:]) + machineID
}

// cursorHeaders is buildCursorHeaders (cursorChecksum.js:95-143). The pin
// refuses a connection with no machineId (cursor.js:393-395). x-ghost-mode is
// always "true": the connection settings carry no ghostMode flag, and "true" is
// the pin's default (cursor.js:391).
func cursorHeaders(provider routing.ProviderRef, _ []byte, _ string) (map[string]string, error) {
	machine := strings.TrimSpace(provider.Connection.Settings.MachineID)
	token := provider.APIToken
	if _, after, ok := strings.Cut(token, "::"); ok {
		token = after
	}
	if machine == "" || token == "" {
		return nil, &CredentialError{Err: errors.New("cursor requires an access token and a machineId")}
	}
	hash := sha256.Sum256([]byte(token))
	system, arch := "linux", "x64"
	switch runtime.GOOS {
	case "windows":
		system = "windows"
	case "darwin":
		system = "macos"
	}
	if runtime.GOARCH == "arm64" {
		arch = "aarch64"
	}
	zone := time.Now().Location().String()
	if zone == "" || zone == "Local" {
		zone = "UTC"
	}
	return map[string]string{
		"Authorization":               "Bearer " + token,
		"Connect-Accept-Encoding":     "gzip",
		"Connect-Protocol-Version":    "1",
		"Content-Type":                cursorContentType,
		"User-Agent":                  "connect-es/1.6.1",
		"X-Amzn-Trace-Id":             "Root=" + uuid.NewString(),
		"X-Client-Key":                hex.EncodeToString(hash[:]),
		"X-Cursor-Checksum":           cursorChecksum(time.Now(), machine),
		"X-Cursor-Client-Version":     "3.12.17",
		"X-Cursor-Client-Commit":      "0fb762053c34788bb7760d5673f8a6d4c8589d50",
		"X-Cursor-Client-Type":        "ide",
		"X-Cursor-Client-OS":          system,
		"X-Cursor-Client-Arch":        arch,
		"X-Cursor-Client-Device-Type": "desktop",
		"X-Cursor-Config-Version":     uuid.NewString(),
		"X-Cursor-Timezone":           zone,
		"X-Ghost-Mode":                "true",
		"X-Request-Id":                uuid.NewString(),
		"X-Session-Id":                uuid.NewSHA1(uuid.NameSpaceDNS, []byte(token)).String(),
	}, nil
}

// --- protobuf wire, the subset the pin uses (cursorProtobuf.js:191-228, 673-687) ---

const (
	pbVarintType  = 0
	pbFixed64Type = 1
	pbLenType     = 2
	pbFixed32Type = 5
)

func pbTag(number, kind uint64) []byte { return binary.AppendUvarint(nil, number<<3|kind) }

func pbVarint(number, value uint64) []byte {
	return binary.AppendUvarint(pbTag(number, pbVarintType), value)
}

func pbBytes(number uint64, data []byte) []byte {
	out := binary.AppendUvarint(pbTag(number, pbLenType), uint64(len(data)))
	return append(out, data...)
}

func pbString(number uint64, value string) []byte { return pbBytes(number, []byte(value)) }

func pbDouble(number uint64, value float64) []byte {
	return binary.LittleEndian.AppendUint64(pbTag(number, pbFixed64Type), math.Float64bits(value))
}

func pbJoin(parts ...[]byte) []byte { return bytes.Join(parts, nil) }

// pbField is one field as it appeared. Fixed-width values keep their bytes.
type pbField struct {
	number uint64
	kind   uint64
	value  uint64 // varint
	data   []byte // len, fixed32, fixed64
}

// pbMessage keeps wire order: the pin picks "the first field that is not 1 or
// 15" from a Map in insertion order (cursor.js:233).
type pbMessage []pbField

func pbParse(data []byte) (pbMessage, error) {
	var message pbMessage
	for len(data) > 0 {
		tag, size := binary.Uvarint(data)
		if size <= 0 || tag>>3 == 0 {
			return nil, cursorError(ErrCursorFraming, "bad field tag")
		}
		data = data[size:]
		field := pbField{number: tag >> 3, kind: tag & 7}
		switch field.kind {
		case pbVarintType:
			value, size := binary.Uvarint(data)
			if size <= 0 {
				return nil, cursorError(ErrCursorFraming, "bad varint")
			}
			field.value, data = value, data[size:]
		case pbFixed64Type, pbFixed32Type:
			width := 8
			if field.kind == pbFixed32Type {
				width = 4
			}
			if len(data) < width {
				return nil, cursorError(ErrCursorFraming, "short fixed field")
			}
			field.data, data = data[:width], data[width:]
		case pbLenType:
			length, size := binary.Uvarint(data)
			if size <= 0 || length > uint64(len(data)-size) {
				return nil, cursorError(ErrCursorFraming, "bad length-delimited field")
			}
			field.data, data = data[size:size+int(length)], data[size+int(length):]
		default:
			return nil, cursorError(ErrCursorFraming, "wire type %d", field.kind)
		}
		message = append(message, field)
	}
	return message, nil
}

func (m pbMessage) all(number uint64) []pbField {
	var found []pbField
	for _, field := range m {
		if field.number == number {
			found = append(found, field)
		}
	}
	return found
}

func (m pbMessage) first(number uint64) (pbField, bool) {
	for _, field := range m {
		if field.number == number {
			return field, true
		}
	}
	return pbField{}, false
}

func (m pbMessage) has(number uint64) bool { _, ok := m.first(number); return ok }

// message decodes a nested message; absent is (nil, false, nil).
func (m pbMessage) message(number uint64) (pbMessage, bool, error) {
	field, ok := m.first(number)
	if !ok {
		return nil, false, nil
	}
	if field.kind != pbLenType {
		return nil, true, cursorError(ErrCursorFraming, "field %d is not a message", number)
	}
	nested, err := pbParse(field.data)
	return nested, true, err
}

func (m pbMessage) text(number uint64) (string, error) {
	field, ok := m.first(number)
	if !ok {
		return "", nil
	}
	if field.kind != pbLenType || !utf8.Valid(field.data) {
		return "", cursorError(ErrCursorFraming, "field %d is not text", number)
	}
	return string(field.data), nil
}

// google.protobuf.Value, as the pin encodes it (cursorProtobuf.js:850-919):
// null 1, number 2 (double), string 3, bool 4, struct 5, list 6.

func cursorValue(value any, depth int) ([]byte, error) {
	if depth > cursorMaxDepth {
		return nil, cursorError(ErrCursorUnsupported, "tool schema nests deeper than %d", cursorMaxDepth)
	}
	switch v := value.(type) {
	case nil:
		return pbVarint(1, 0), nil
	case bool:
		if v {
			return pbVarint(4, 1), nil
		}
		return pbVarint(4, 0), nil
	case json.Number:
		number, err := v.Float64()
		if err != nil {
			return nil, cursorError(ErrCursorUnsupported, "tool schema number %q", v.String())
		}
		return pbDouble(2, number), nil
	case float64:
		return pbDouble(2, v), nil
	case string:
		return pbString(3, v), nil
	case []any:
		var items []byte
		for _, item := range v {
			encoded, err := cursorValue(item, depth+1)
			if err != nil {
				return nil, err
			}
			items = append(items, pbBytes(1, encoded)...)
		}
		return pbBytes(6, items), nil
	case map[string]any:
		// The pin writes keys in insertion order; protobuf maps are unordered, and
		// sorted keys make the bytes reproducible.
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		var entries []byte
		for _, key := range keys {
			encoded, err := cursorValue(v[key], depth+1)
			if err != nil {
				return nil, err
			}
			entries = append(entries, pbBytes(1, pbJoin(pbString(1, key), pbBytes(2, encoded)))...)
		}
		return pbBytes(5, entries), nil
	}
	return nil, cursorError(ErrCursorUnsupported, "tool schema value of type %T", value)
}

// cursorDecodeValue is decodeAgentValue (cursorProtobuf.js:924-948). A Value
// with no known arm is null, as at the pin.
func cursorDecodeValue(data []byte, depth int) (any, error) {
	if depth > cursorMaxDepth {
		return nil, cursorError(ErrCursorFraming, "tool arguments nest deeper than %d", cursorMaxDepth)
	}
	message, err := pbParse(data)
	if err != nil {
		return nil, err
	}
	switch {
	case message.has(1):
		return nil, nil
	case message.has(4):
		field, _ := message.first(4)
		return field.value != 0, nil
	case message.has(2):
		field, _ := message.first(2)
		if field.kind != pbFixed64Type {
			return nil, cursorError(ErrCursorFraming, "number is not a double")
		}
		number := math.Float64frombits(binary.LittleEndian.Uint64(field.data))
		if math.IsNaN(number) || math.IsInf(number, 0) {
			return nil, cursorError(ErrCursorFraming, "number is not finite")
		}
		return number, nil
	case message.has(3):
		return message.text(3)
	case message.has(5):
		structure, _, err := message.message(5)
		if err != nil {
			return nil, err
		}
		result := map[string]any{}
		for _, entry := range structure.all(1) {
			key, value, err := cursorMapEntry(entry, depth)
			if err != nil {
				return nil, err
			}
			if key != "" {
				result[key] = value
			}
		}
		return result, nil
	case message.has(6):
		list, _, err := message.message(6)
		if err != nil {
			return nil, err
		}
		result := []any{}
		for _, item := range list.all(1) {
			value, err := cursorDecodeValue(item.data, depth+1)
			if err != nil {
				return nil, err
			}
			result = append(result, value)
		}
		return result, nil
	}
	return nil, nil
}

// cursorMapEntry reads one map<string, Value> entry: key 1, value 2.
func cursorMapEntry(entry pbField, depth int) (string, any, error) {
	if entry.kind != pbLenType {
		return "", nil, cursorError(ErrCursorFraming, "map entry is not a message")
	}
	pair, err := pbParse(entry.data)
	if err != nil {
		return "", nil, err
	}
	key, err := pair.text(1)
	if err != nil {
		return "", nil, err
	}
	field, ok := pair.first(2)
	if !ok {
		return key, nil, nil
	}
	if field.kind != pbLenType {
		return "", nil, cursorError(ErrCursorFraming, "map value is not a message")
	}
	value, err := cursorDecodeValue(field.data, depth+1)
	return key, value, err
}

// --- Connect frames (cursorProtobuf.js:596-613, cursor.js:187-201) ---

func cursorFrame(payload []byte) []byte {
	frame := make([]byte, 5, len(payload)+5)
	binary.BigEndian.PutUint32(frame[1:], uint32(len(payload)))
	return append(frame, payload...)
}

// cursorReadFrame reads one reply frame. Flag bit 0 is gzip, bit 1 the end
// trailer; any other bit is a frame this transport does not understand. The pin
// returns a short or undecodable frame as if it were absent (cursor.js:333-357);
// here it is an error.
func cursorReadFrame(src io.Reader) (payload []byte, trailer bool, err error) {
	var header [5]byte
	if _, err := io.ReadFull(src, header[:]); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, false, cursorError(ErrCursorFraming, "stream ended before the turn ended")
		}
		return nil, false, cursorError(ErrCursorFraming, "reading frame header: %v", err)
	}
	flags, size := header[0], binary.BigEndian.Uint32(header[1:])
	if flags&^byte(3) != 0 {
		return nil, false, cursorError(ErrCursorFraming, "frame flags 0x%02x", flags)
	}
	if size > cursorFrameLimit {
		return nil, false, cursorError(ErrCursorFraming, "frame of %d bytes exceeds the limit", size)
	}
	payload = make([]byte, size)
	if _, err := io.ReadFull(src, payload); err != nil {
		return nil, false, cursorError(ErrCursorFraming, "frame truncated: %v", err)
	}
	if flags&1 != 0 {
		reader, err := gzip.NewReader(bytes.NewReader(payload))
		if err != nil {
			return nil, false, cursorError(ErrCursorFraming, "gzip frame: %v", err)
		}
		payload, err = io.ReadAll(io.LimitReader(reader, cursorFrameLimit+1))
		reader.Close()
		if err != nil || len(payload) > cursorFrameLimit {
			return nil, false, cursorError(ErrCursorFraming, "gzip frame does not inflate within the limit")
		}
	}
	return payload, flags&2 != 0, nil
}

// --- request: buildAgentRunFrame (cursor.js:114-180) ---

// cursorRun turns the Chat body the Cursor request leg produced into the first
// frame of the Run stream. That body has only user and assistant turns with
// string content (internal/protocol/cursor, convertMessages).
func cursorRun(body []byte, messageID string) (frame []byte, model string, err error) {
	request, err := concerns.WireObject(body)
	if err != nil {
		return nil, "", cursorError(ErrCursorUnsupported, "request is not a JSON object")
	}
	model = concerns.Text(request["model"])
	if model == "" || model == "auto" || model == "default" {
		return nil, "", cursorError(ErrCursorUnsupported, "an explicit model is required")
	}
	// The pin sends system text in the current user turn, because
	// custom_system_prompt makes the agent answer an empty turn (cursor.js:115-121).
	var system []string
	var chat []map[string]any
	for _, raw := range concerns.Items(request["messages"]) {
		message := concerns.Object(raw)
		if message == nil {
			return nil, "", cursorError(ErrCursorUnsupported, "message is not an object")
		}
		if content := message["content"]; content != nil {
			if _, ok := content.(string); !ok {
				return nil, "", cursorError(ErrCursorUnsupported, "message content must be text")
			}
		}
		switch role := concerns.Text(message["role"]); role {
		case "system":
			if text := concerns.Text(message["content"]); text != "" {
				system = append(system, text)
			}
		case "user", "assistant":
			chat = append(chat, message)
		default:
			return nil, "", cursorError(ErrCursorUnsupported, "message role %q", role)
		}
	}
	if len(chat) == 0 {
		return nil, "", cursorError(ErrCursorUnsupported, "request has no messages")
	}
	// The current turn is the last user turn (cursor.js:123-124). The pin keeps
	// only the turns before it and drops any after (:125-128); a turn that would
	// be dropped is refused instead.
	current := -1
	for i, message := range chat {
		if concerns.Text(message["role"]) == "user" {
			current = i
		}
	}
	if current < 0 {
		return nil, "", cursorError(ErrCursorUnsupported, "request has no user turn")
	}
	if current != len(chat)-1 {
		return nil, "", cursorError(ErrCursorUnsupported, "turns after the last user turn would be dropped")
	}
	var history []byte
	for _, message := range chat[:current] {
		text := cursorHistoryText(message)
		if text == "" {
			continue
		}
		// ConversationHistoryMessage: user 1, assistant 2, each 1{1{1: text}} (:92-112).
		entry := pbBytes(1, pbBytes(1, pbString(1, text)))
		if concerns.Text(message["role"]) == "assistant" {
			history = append(history, pbBytes(1, pbBytes(2, entry))...)
		} else {
			history = append(history, pbBytes(1, pbBytes(1, entry))...)
		}
	}
	text := concerns.Text(chat[current]["content"])
	if text == "" {
		text = "Continue."
	}
	if len(system) > 0 {
		text = strings.Join(system, "\n\n") + "\n\n" + text
	}
	// UserMessageAction.user_message: text 1, message id 2, selected_context 3
	// (empty), mode 4 = 1 (:135-140).
	user := pbJoin(pbString(1, text), pbString(2, messageID), pbBytes(3, nil), pbVarint(4, 1))
	action := pbBytes(1, user)
	if len(history) > 0 {
		action = append(action, pbBytes(7, history)...)
	}
	details := pbJoin(pbString(1, model), pbString(3, model), pbString(4, model))
	requested := pbJoin(pbString(1, model), pbVarint(7, 1))
	if value := cursorEffort(request); value != "" {
		key := "effort"
		if strings.Contains(strings.ToLower(model), "gpt-") {
			key = "reasoning"
		}
		requested = append(requested, pbBytes(3, pbJoin(pbString(1, key), pbString(2, value)))...)
	}
	tools, err := cursorTools(request["tools"])
	if err != nil {
		return nil, "", err
	}
	run := pbJoin(pbBytes(1, nil), pbBytes(2, pbBytes(1, action)), pbBytes(3, details))
	if len(tools) > 0 {
		run = append(run, pbBytes(4, tools)...)
	}
	run = append(run, pbBytes(9, requested)...)
	frame = cursorFrame(pbBytes(1, run))
	if len(frame) > cursorRequestLimit {
		return nil, "", cursorError(ErrCursorFraming, "request frame exceeds the limit")
	}
	return frame, model, nil
}

// cursorHistoryText is encodeHistoryMessage (cursor.js:92-103).
func cursorHistoryText(message map[string]any) string {
	parts := []string{}
	if content := concerns.Text(message["content"]); content != "" {
		parts = append(parts, content)
	}
	if concerns.Text(message["role"]) == "assistant" {
		for _, raw := range concerns.Items(message["tool_calls"]) {
			call := concerns.Object(raw)
			function := concerns.Object(call["function"])
			name, arguments := concerns.Text(function["name"]), concerns.Text(function["arguments"])
			if name == "" {
				name = "tool"
			}
			if arguments == "" {
				arguments = "{}"
			}
			parts = append(parts, "[tool_call id="+concerns.Text(call["id"])+" name="+name+" args="+arguments+"]")
		}
	}
	return strings.Join(parts, "\n")
}

// cursorEffort is cursor.js:149-156: ultra is max, minimal is low, none is no
// parameter. The pin reads reasoning_effort, else reasoning.effort (:618).
func cursorEffort(request map[string]any) string {
	effort := concerns.Text(request["reasoning_effort"])
	if effort == "" {
		effort = concerns.Text(concerns.Object(request["reasoning"])["effort"])
	}
	effort = strings.ToLower(effort)
	switch effort {
	case "ultra":
		return "max"
	case "minimal":
		return "low"
	case "none":
		return ""
	}
	return effort
}

// cursorTools is encodeMcpTools (cursorProtobuf.js:962-981): McpToolDefinition
// name 1, description 2, input schema as a Value 3, provider "9router" 4,
// tool name 5, each under field 1. A schema-less tool gets {} (:955).
func cursorTools(value any) ([]byte, error) {
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
		schema := function["parameters"]
		if schema == nil {
			schema = map[string]any{}
		}
		encoded, err := cursorValue(schema, 0)
		if err != nil {
			return nil, err
		}
		definition := pbJoin(pbString(1, name), pbString(2, concerns.Text(function["description"])), pbBytes(3, encoded), pbString(4, "9router"), pbString(5, name))
		tools = append(tools, pbBytes(1, definition)...)
	}
	return tools, nil
}

// --- reply: executeAgent's consume (cursor.js:676-787) ---

// cursorRejectable are the ExecServerMessage variants answered with a
// "not available" rejection under the same field number (EXEC_RESULT_FIELD,
// cursor.js:227-229); 9 (diagnostics) gets an empty success (:237).
var cursorRejectable = map[uint64]bool{2: true, 3: true, 4: true, 5: true, 7: true, 8: true, 16: true, 20: true, 23: true}

var cursorComposer = regexp.MustCompile(`(?i)^composer(?:-|$)`)

// cursorVisibleAfterThinking is visibleComposerContentFromThinking (:281-287).
func cursorVisibleAfterThinking(thinking string) string {
	const end = "</think>"
	index := strings.LastIndex(thinking, end)
	if index < 0 {
		return ""
	}
	return strings.TrimLeftFunc(thinking[index+len(end):], unicode.IsSpace)
}

func cursorExecReply(id uint64, execID string, result []byte) []byte {
	var parts [][]byte
	if id != 0 {
		parts = append(parts, pbVarint(1, id))
	}
	parts = append(parts, pbString(15, execID), result)
	return cursorFrame(pbBytes(2, pbJoin(parts...)))
}

func cursorKVReply(id uint64, result, metadata []byte) []byte {
	var parts [][]byte
	if id != 0 {
		parts = append(parts, pbVarint(1, id))
	}
	parts = append(parts, result)
	if len(metadata) > 0 {
		parts = append(parts, pbBytes(4, metadata))
	}
	return cursorFrame(pbBytes(3, pbJoin(parts...)))
}

// cursorConsume reads the reply until the turn ends, answers the server's
// questions on acks, and writes Chat SSE to dst. It returns nil only for a turn
// that ended with visible text or a tool call. Chunks are the pin's
// (cursor.js:846-877): content deltas, one tool call with finish "tool_calls",
// or finish "stop". The pin never forwards reasoning (:721), and neither does this.
func cursorConsume(ctx context.Context, src io.Reader, acks io.Writer, dst io.Writer, model string) error {
	writer := &concerns.SSEWriter{Ctx: ctx, Dst: dst}
	id := fmt.Sprintf("chatcmpl-msg_%d", time.Now().UnixMilli())
	created := time.Now().Unix()
	chunk := func(delta map[string]any, finish any) error {
		return writer.Event("", map[string]any{"id": id, "object": "chat.completion.chunk", "created": created, "model": model,
			"choices": []any{map[string]any{"index": 0, "delta": delta, "finish_reason": finish}}})
	}
	composer := cursorComposer.MatchString(model[strings.LastIndex(model, "/")+1:])
	var thinking string
	emittedVisible, total := 0, 0
	emittedText := false
	text := func(value string) error {
		emittedText = true
		return chunk(map[string]any{"content": value}, nil)
	}
	finish := func() error {
		if !emittedText && thinking != "" {
			fallback := strings.TrimSpace(thinking)
			if composer {
				fallback = cursorVisibleAfterThinking(thinking)
			}
			if fallback != "" {
				if err := text(fallback); err != nil {
					return err
				}
			}
		}
		if !emittedText {
			return cursorError(ErrCursorEmptyTurn, "Cursor AgentService returned an empty turn")
		}
		if err := chunk(map[string]any{}, "stop"); err != nil {
			return err
		}
		return writer.Done()
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		payload, trailer, err := cursorReadFrame(src)
		if err != nil {
			return err
		}
		if total += len(payload); total > concerns.MaxTranslatedResponseBytes {
			return cursorError(ErrCursorFraming, "reply exceeds the translated-response limit")
		}
		if trailer {
			// The pin skips the trailer (:198) and then treats the end of the stream
			// as the end of the turn. A trailer before turn_ended is an end the turn
			// did not reach; its error, if any, is named.
			var end struct {
				Error json.RawMessage `json:"error"`
			}
			if json.Unmarshal(payload, &end) == nil && len(end.Error) > 0 && string(end.Error) != "null" {
				return cursorError(ErrCursorFraming, "upstream ended the stream with an error: %s", end.Error)
			}
			return cursorError(ErrCursorFraming, "stream ended before the turn ended")
		}
		server, err := pbParse(payload)
		if err != nil {
			return err
		}
		// interaction_update (:694-725)
		if update, ok, err := server.message(1); err != nil {
			return err
		} else if ok {
			if part, ok, err := update.message(1); err != nil {
				return err
			} else if ok {
				delta, err := part.text(1)
				if err != nil {
					return err
				}
				if delta != "" {
					if err := text(delta); err != nil {
						return err
					}
				}
			}
			if part, ok, err := update.message(4); err != nil {
				return err
			} else if ok {
				delta, err := part.text(1)
				if err != nil {
					return err
				}
				thinking += delta
				if delta != "" && composer {
					if visible := cursorVisibleAfterThinking(thinking); len(visible) > emittedVisible {
						value := visible[emittedVisible:]
						emittedVisible = len(visible)
						if err := text(value); err != nil {
							return err
						}
					}
				}
			}
			if update.has(14) {
				return finish()
			}
		}
		// kv_server_message: acknowledge the blob get/set (:727-737).
		if kv, ok, err := server.message(4); err != nil {
			return err
		} else if ok {
			var kvID uint64
			if field, ok := kv.first(1); ok {
				kvID = field.value
			}
			var metadata []byte
			if field, ok := kv.first(4); ok {
				metadata = field.data
			}
			var result []byte
			switch {
			case kv.has(2):
				result = pbBytes(2, pbBytes(1, nil))
			case kv.has(3):
				result = pbBytes(3, nil)
			default:
				return cursorError(ErrCursorUnsupported, "key-value request is neither a get nor a set")
			}
			if _, err := acks.Write(cursorKVReply(kvID, result, metadata)); err != nil {
				return err
			}
		}
		// exec_server_message: IDE context, MCP tool call, or an IDE tool (:739-779).
		if exec, ok, err := server.message(2); err != nil {
			return err
		} else if ok {
			done, err := cursorExec(exec, acks, chunk, writer)
			if err != nil || done {
				return err
			}
		}
	}
}

// cursorExec answers one exec request. done reports a finished turn (an MCP tool call).
func cursorExec(exec pbMessage, acks io.Writer, chunk func(map[string]any, any) error, writer *concerns.SSEWriter) (done bool, err error) {
	var execID string
	if execID, err = exec.text(15); err != nil {
		return false, err
	}
	var execNumber uint64
	if field, ok := exec.first(1); ok {
		execNumber = field.value
	}
	if exec.has(10) {
		// request_context ack: tools already travel on the Run request, and echoing
		// them here stalls the stream (:217-224).
		_, err = acks.Write(cursorExecReply(execNumber, execID, pbBytes(10, pbBytes(1, pbBytes(1, nil)))))
		return false, err
	}
	if exec.has(11) {
		mcp, _, err := exec.message(11)
		if err != nil {
			return false, err
		}
		name, err := mcp.text(5) // tool name, else name (:747)
		if err != nil {
			return false, err
		}
		if name == "" {
			if name, err = mcp.text(1); err != nil {
				return false, err
			}
		}
		if name == "" {
			return false, cursorError(ErrCursorUnsupported, "Cursor AgentService requested an unsupported IDE tool")
		}
		arguments := map[string]any{}
		for _, entry := range mcp.all(2) {
			key, value, err := cursorMapEntry(entry, 0)
			if err != nil {
				return false, err
			}
			if key != "" {
				arguments[key] = value
			}
		}
		encoded, err := json.Marshal(arguments)
		if err != nil {
			return false, cursorError(ErrCursorFraming, "tool arguments: %v", err)
		}
		callID, err := mcp.text(3)
		if err != nil {
			return false, err
		}
		if callID == "" {
			callID = "call_" + uuid.NewString()
		}
		call := map[string]any{"index": 0, "id": callID, "type": "function", "function": map[string]any{"name": name, "arguments": string(encoded)}}
		if err := chunk(map[string]any{"tool_calls": []any{call}}, nil); err != nil {
			return false, err
		}
		if err := chunk(map[string]any{}, "tool_calls"); err != nil {
			return false, err
		}
		return true, writer.Done()
	}
	// Any other variant is an IDE builtin the pin rejects so the model can carry on
	// with its MCP tools (:766-772).
	for _, field := range exec {
		if field.number == 1 || field.number == 15 {
			continue
		}
		var result []byte
		switch {
		case field.number == 9:
			result = pbBytes(9, nil)
		case cursorRejectable[field.number]:
			result = pbBytes(field.number, pbBytes(2, pbString(2, cursorRejectedTool)))
		default:
			return false, cursorError(ErrCursorUnsupported, "Cursor AgentService requested an unsupported IDE tool (field %d)", field.number)
		}
		_, err = acks.Write(cursorExecReply(execNumber, execID, result))
		return false, err
	}
	return false, cursorError(ErrCursorUnsupported, "Cursor AgentService sent an empty exec request")
}

// --- transport ---

// cursorBody ties the request and the reply together: closing the reply stops
// the upstream request and releases the acknowledgement writer.
type cursorBody struct {
	io.ReadCloser
	cancel context.CancelFunc
	input  *io.PipeReader
	acks   *io.PipeWriter
}

func (b *cursorBody) Close() error {
	b.cancel()
	b.input.Close()
	b.acks.Close()
	return b.ReadCloser.Close()
}

// CursorDo sends the Run stream. request carries the Chat body the Cursor leg
// produced; the first Connect frame is built from it and the body stays open for
// the acknowledgements. The returned response is Chat SSE. A non-2xx reply is
// returned as received, for the dispatcher to classify. A failure after the
// stream has started ends with an in-band error object and [DONE], never with a
// truncated success.
func CursorDo(client *http.Client, request *http.Request) (*http.Response, error) {
	raw, err := io.ReadAll(io.LimitReader(request.Body, cursorRequestLimit+1))
	request.Body.Close()
	if err != nil {
		return nil, err
	}
	if len(raw) > cursorRequestLimit {
		return nil, cursorError(ErrCursorFraming, "request exceeds the limit")
	}
	frame, model, err := cursorRun(raw, uuid.NewString())
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(request.Context())
	request = request.Clone(ctx)
	request.Header.Del("Accept")
	input, acks := io.Pipe()
	request.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(frame), input), input}
	request.ContentLength, request.GetBody = -1, nil
	response, err := client.Do(request)
	if err != nil {
		cancel()
		input.Close()
		acks.Close()
		return nil, err
	}
	if response.ProtoMajor != 2 {
		cancel()
		input.Close()
		acks.Close()
		response.Body.Close()
		return nil, cursorError(ErrCursorHTTP2, "the endpoint answered %s", response.Proto)
	}
	if response.StatusCode != http.StatusOK {
		response.Body = &cursorBody{ReadCloser: response.Body, cancel: cancel, input: input, acks: acks}
		return response, nil
	}
	if mediaType := strings.ToLower(strings.TrimSpace(strings.SplitN(response.Header.Get("Content-Type"), ";", 2)[0])); mediaType != cursorContentType {
		cancel()
		input.Close()
		acks.Close()
		response.Body.Close()
		return nil, cursorError(ErrCursorFraming, "reply content type %q", mediaType)
	}
	reader, writer := io.Pipe()
	upstream := response.Body
	go func() {
		err := cursorConsume(ctx, upstream, acks, writer, model)
		if err != nil && ctx.Err() == nil {
			sse := &concerns.SSEWriter{Ctx: ctx, Dst: writer}
			if sse.Event("", map[string]any{"error": map[string]any{"message": err.Error(), "type": "api_error", "code": cursorCode(err)}}) == nil {
				sse.Done()
			}
		}
		acks.Close()
		upstream.Close()
		cancel()
		writer.Close()
	}()
	response.Body = &cursorBody{ReadCloser: reader, cancel: cancel, input: input, acks: acks}
	response.Header = response.Header.Clone()
	response.Header.Set("Content-Type", "text/event-stream")
	response.Header.Del("Content-Length")
	response.Header.Del("Content-Encoding")
	response.ContentLength, response.Uncompressed = -1, true
	return response, nil
}

func cursorCode(err error) string {
	for _, kind := range []error{ErrCursorEmptyTurn, ErrCursorUnsupported, ErrCursorFraming, ErrCursorHTTP2} {
		if errors.Is(err, kind) {
			return kind.Error()
		}
	}
	return "cursor_connect_error"
}
