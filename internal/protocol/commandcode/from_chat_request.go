package commandcode

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// defaultMaxTokens is the pin's default (runtimeConfig.js:64). CommandCode's
// wire takes Anthropic-style params, where max_tokens is required, so unlike a
// sampling value it cannot be left to the upstream.
const defaultMaxTokens = 64000

// The envelope's `config` describes the caller's workspace to the upstream. The
// pin fills it from the router process (process.cwd(), process.platform,
// openai-to-commandcode.js:206-216), which leaks the host path and OS and differs
// between deployments. These constants are sent instead (PARITY V35).
const (
	neutralWorkingDir  = "/"
	neutralEnvironment = "linux"
)

var (
	now         = time.Now
	newThreadID = randomUUID
)

// SetClock replaces the clock the request envelope's date is read from and returns a
// function that restores it. It exists so a parity harness can run the conversion at the
// instant the oracle's recording was made; the serving path never calls it.
func SetClock(clock func() time.Time) (restore func()) {
	previous := now
	now = clock
	return func() { now = previous }
}

func randomUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:])
}

// ChatRequestToCommandCode is the request leg openai-chat>commandcode
// (translator/request/openai-to-commandcode.js:185-219, messages :106-161). It
// returns the envelope for CommandCode's /alpha/generate. repair is true when
// body is the client's own Chat body, because the pin repairs tool ids only on
// the client's body (translator/index.js:82). It mutates body.
//
// What it does with every Chat field is the table in docs/PARITY.md 6.5. A field
// is carried, not carried for a stated reason (the request is still served and
// nothing it asks for is lost), or refused with an error, so the dispatcher
// tries the next candidate. Nothing is dropped silently.
func ChatRequestToCommandCode(body map[string]any, model string, stream, repair bool) ([]byte, error) {
	if reason := concerns.UncarriableChat(body, "CommandCode"); reason != "" {
		return nil, unsupported("%s", reason)
	}
	// Refused rather than dropped: the pin sends none of them and nothing offline
	// shows whether `params` takes them (PARITY 6.5).
	for _, name := range []string{"seed", "presence_penalty", "frequency_penalty"} {
		if body[name] != nil {
			return nil, unsupported("%s: not verifiable offline for CommandCode", name)
		}
	}
	for _, name := range []string{"stop", "logit_bias", "reasoning_effort", "reasoning"} {
		if concerns.Present(body[name]) {
			return nil, unsupported("%s: not verifiable offline for CommandCode", name)
		}
	}
	if format := body["response_format"]; format != nil {
		object := concerns.Object(format)
		if object == nil {
			return nil, unsupported("response_format must be an object")
		}
		if kind := concerns.Text(object["type"]); kind != "" && kind != "text" {
			return nil, unsupported("response_format %q: not verifiable offline for CommandCode", kind)
		}
	}
	if repair {
		concerns.EnsureToolCallIDs(body)
	}
	messages, system, err := commandCodeMessages(concerns.Items(body["messages"]))
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return nil, unsupported("no message survived conversion")
	}
	tools, err := commandCodeTools(body["tools"])
	if err != nil {
		return nil, err
	}
	switch choice := body["tool_choice"]; {
	case !concerns.Truthy(choice), choice == "auto":
		// The pin drops tool_choice (:185-219); absent already means auto.
	case choice == "none":
		// The wire has no tool_choice, so "none" is honoured by withholding the
		// tools; the pin would let the model call a tool the client forbade.
		tools = nil
	default:
		return nil, unsupported("tool_choice %s: CommandCode cannot require a tool", concerns.JSONText(choice))
	}
	params := map[string]any{"model": model, "messages": messages, "stream": stream}
	switch {
	case body["max_tokens"] != nil:
		params["max_tokens"] = body["max_tokens"]
	case body["max_completion_tokens"] != nil:
		// The pin reads max_tokens and max_output_tokens (:191) and ignores the
		// current Chat name (PARITY V22).
		params["max_tokens"] = body["max_completion_tokens"]
	case body["max_output_tokens"] != nil:
		params["max_tokens"] = body["max_output_tokens"]
	default:
		params["max_tokens"] = defaultMaxTokens
	}
	// The pin sends temperature 0.3 when the client sent none (:192). Omitted: the
	// upstream's own default applies (PARITY V34).
	if body["temperature"] != nil {
		params["temperature"] = body["temperature"]
	}
	if body["top_p"] != nil {
		params["top_p"] = body["top_p"]
	}
	if system != "" {
		params["system"] = system
	}
	if len(tools) > 0 {
		params["tools"] = tools
	}
	return json.Marshal(map[string]any{
		"threadId": newThreadID(),
		"memory":   "",
		"config": map[string]any{
			"workingDir": neutralWorkingDir, "date": now().UTC().Format("2006-01-02"), "environment": neutralEnvironment,
			"structure": []any{}, "isGitRepo": false, "currentBranch": "", "mainBranch": "", "gitStatus": "", "recentCommits": []any{},
		},
		"params": params,
	})
}

func unsupported(format string, args ...any) error {
	return fmt.Errorf("unsupported: "+format, args...)
}

// commandCodeTools is convertTools (:163-183). The pin skips a tool it cannot
// convert; here it is refused. A function tool is {type:"function", function};
// the pin also takes a bare {name, input_schema|parameters}.
func commandCodeTools(value any) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, unsupported("tools must be a list")
	}
	result := make([]any, 0, len(list))
	for _, raw := range list {
		tool := concerns.Object(raw)
		if tool == nil {
			return nil, unsupported("tool is not an object")
		}
		var name, description, schema any
		switch function := concerns.Object(tool["function"]); {
		case concerns.Text(tool["type"]) == "function" && function != nil:
			if concerns.Truthy(function["strict"]) {
				return nil, unsupported("strict tool schema: not verifiable offline for CommandCode")
			}
			name, description, schema = function["name"], function["description"], function["parameters"]
		case concerns.Text(tool["type"]) == "" && tool["function"] == nil:
			name, description, schema = tool["name"], tool["description"], tool["input_schema"]
			if schema == nil {
				schema = tool["parameters"]
			}
			if schema == nil {
				return nil, unsupported("tool without a schema")
			}
		default:
			return nil, unsupported("tool that is not a function")
		}
		if concerns.Text(name) == "" {
			return nil, unsupported("tool without a name")
		}
		if schema != nil && concerns.Object(schema) == nil {
			return nil, unsupported("tool parameters must be an object")
		}
		if len(concerns.Object(schema)) == 0 {
			// A tool with no parameters is a valid function tool.
			schema = map[string]any{"type": "object"}
		}
		converted := map[string]any{"name": name, "input_schema": schema}
		if description != nil {
			converted["description"] = description
		}
		result = append(result, converted)
	}
	return result, nil
}

// commandCodeMessages is convertMessages (:106-161): system turns are hoisted
// into one string, every content is a list of blocks.
func commandCodeMessages(source []any) (messages []any, system string, err error) {
	names := map[string]string{}
	for _, raw := range source {
		message := concerns.Object(raw)
		if concerns.Text(message["role"]) != "assistant" {
			continue
		}
		for _, rawCall := range concerns.Items(message["tool_calls"]) {
			call := concerns.Object(rawCall)
			if id, name := concerns.Text(call["id"]), concerns.Text(concerns.Object(call["function"])["name"]); id != "" && name != "" {
				names[id] = name
			}
		}
	}
	var systems []string
	for _, raw := range source {
		message := concerns.Object(raw)
		if message == nil {
			return nil, "", unsupported("message is not an object")
		}
		switch role := concerns.Text(message["role"]); role {
		case "system", "developer":
			// The pin turns a developer message into a user turn (:157), changing
			// who is speaking; it is an instruction (PARITY V21).
			blocks, err := contentBlocks(message["content"], false)
			if err != nil {
				return nil, "", err
			}
			if text := joinText(blocks); text != "" {
				systems = append(systems, text)
			}
		case "tool":
			blocks, err := contentBlocks(message["content"], false)
			if err != nil {
				return nil, "", err
			}
			id := concerns.Text(message["tool_call_id"])
			if id == "" {
				return nil, "", unsupported("tool result without a tool_call_id")
			}
			name := concerns.Text(message["name"])
			if name == "" {
				name = names[id]
			}
			if name == "" {
				// The pin sends "" (:127). Same fallback as the Ollama leg (M3).
				name = "unknown_tool"
			}
			messages = append(messages, map[string]any{"role": "tool", "content": []any{map[string]any{
				"type": "tool-result", "toolCallId": id, "toolName": name, "output": map[string]any{"type": "text", "value": joinText(blocks)},
			}}})
		case "assistant":
			out, err := assistantBlocks(message)
			if err != nil {
				return nil, "", err
			}
			messages = append(messages, map[string]any{"role": "assistant", "content": out})
		case "user":
			blocks, err := contentBlocks(message["content"], true)
			if err != nil {
				return nil, "", err
			}
			messages = append(messages, map[string]any{"role": "user", "content": blocks})
		default:
			return nil, "", unsupported("message role %q", role)
		}
	}
	for i, text := range systems {
		if i > 0 {
			system += "\n\n"
		}
		system += text
	}
	return messages, system, nil
}

func assistantBlocks(message map[string]any) ([]any, error) {
	if value := message["tool_calls"]; value != nil {
		if _, ok := value.([]any); !ok {
			return nil, unsupported("tool_calls must be a list")
		}
	}
	calls := concerns.Items(message["tool_calls"])
	blocks := []any{}
	reasoning := ""
	for _, key := range []string{"reasoning_content", "thought", "reasoning"} {
		if text := concerns.Text(message[key]); text != "" {
			reasoning = text
			break
		}
	}
	// A turn that made calls carries a reasoning block even with no text, as the
	// pin does (:137-139).
	if reasoning != "" || len(calls) > 0 {
		if reasoning == "" {
			reasoning = " "
		}
		blocks = append(blocks, map[string]any{"type": "reasoning", "text": reasoning})
	}
	content, err := contentBlocks(message["content"], false)
	if err != nil {
		return nil, err
	}
	if text := joinText(content); text != "" {
		blocks = append(blocks, map[string]any{"type": "text", "text": text})
	}
	for _, raw := range calls {
		call := concerns.Object(raw)
		if call == nil {
			return nil, unsupported("tool call is not an object")
		}
		if kind := concerns.Text(call["type"]); kind != "" && kind != "function" {
			return nil, unsupported("tool call type %q", kind)
		}
		function := concerns.Object(call["function"])
		id, name := concerns.Text(call["id"]), concerns.Text(function["name"])
		if id == "" || name == "" {
			return nil, unsupported("tool call without an id or a name")
		}
		input, err := callInput(function["arguments"])
		if err != nil {
			return nil, err
		}
		blocks = append(blocks, map[string]any{"type": "tool-call", "toolCallId": id, "toolName": name, "input": input})
	}
	if len(blocks) == 0 {
		blocks = append(blocks, map[string]any{"type": "text", "text": ""})
	}
	return blocks, nil
}

// callInput is a call's arguments as the object CommandCode takes. The pin
// replaces arguments it cannot parse with {} (safeParseJson, :100-104), which
// rewrites the history the model reads; they are refused instead.
func callInput(value any) (map[string]any, error) {
	switch v := value.(type) {
	case nil:
		return map[string]any{}, nil
	case map[string]any:
		return v, nil
	case string:
		if v == "" {
			return map[string]any{}, nil
		}
		if parsed, err := concerns.WireObject([]byte(v)); err == nil {
			return parsed, nil
		}
	}
	return nil, unsupported("tool call arguments that are not a JSON object")
}

// contentBlocks is toContentBlocks (:75-98). Text and, for a user turn, data-URI
// images are carried. Everything else the pin drops silently and is refused: a
// remote image (fetching it is an outbound request this wire does not make,
// PARITY V23), audio, files and unknown parts, and an image in any turn but a
// user's.
func contentBlocks(content any, allowImages bool) ([]any, error) {
	switch value := content.(type) {
	case nil:
		return []any{map[string]any{"type": "text", "text": ""}}, nil
	case string:
		return []any{map[string]any{"type": "text", "text": value}}, nil
	case []any:
		blocks := []any{}
		for _, raw := range value {
			part := concerns.Object(raw)
			switch kind := concerns.Text(part["type"]); kind {
			case "text", "refusal":
				text, ok := part[kind].(string)
				if !ok {
					return nil, unsupported("%s part without text", kind)
				}
				blocks = append(blocks, map[string]any{"type": "text", "text": text})
			case "image_url":
				if !allowImages {
					return nil, unsupported("image outside a user turn")
				}
				url := part["image_url"]
				if object := concerns.Object(url); object != nil {
					url = object["url"]
				}
				mediaType, _, ok := concerns.ParseDataURI(url)
				if !ok {
					return nil, unsupported("image_url that is not a base64 data URI")
				}
				blocks = append(blocks, map[string]any{"type": "image", "image": url, "mimeType": mediaType, "mediaType": mediaType})
			default:
				return nil, unsupported("content part %q", kind)
			}
		}
		if len(blocks) == 0 {
			return []any{map[string]any{"type": "text", "text": ""}}, nil
		}
		return blocks, nil
	}
	return nil, unsupported("content must be a string or a list of parts")
}

// joinText joins the text blocks of a content value with a newline (flattenText,
// :20-32).
func joinText(blocks []any) string {
	text := ""
	first := true
	for _, raw := range blocks {
		block := concerns.Object(raw)
		if concerns.Text(block["type"]) != "text" {
			continue
		}
		if !first {
			text += "\n"
		}
		text += concerns.Text(block["text"])
		first = false
	}
	return text
}
