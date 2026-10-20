package cursor

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// ChatRequestToCursor is the request leg openai-chat>cursor
// (translator/request/openai-to-cursor.js:172-183, messages :52-170). It returns
// the Chat-shaped body the pin hands its executor, which reads only messages,
// tools and the reasoning effort (executors/cursor.js:403-405, 615-618).
// repair is true when body is the client's own Chat body, because the pin
// repairs tool ids only on the client's body (translator/index.js:82). It
// mutates body.
//
// What it does with every Chat field is the table in docs/PARITY.md 6.5. A field
// is carried, not carried for a stated reason (the request is still served and
// nothing it asks for is lost), or refused with an error, so the dispatcher
// tries the next candidate. Nothing is dropped silently.
func ChatRequestToCursor(body map[string]any, model string, stream, repair bool) ([]byte, error) {
	if reason := concerns.UncarriableChat(body, "Cursor"); reason != "" {
		return nil, unsupported("%s", reason)
	}
	// Refused rather than dropped. The pin's executor sends none of them, but its
	// protobuf field list is the only schema available offline, so whether
	// Cursor has a place for them is not verifiable here (PARITY 6.5).
	for _, name := range []string{"temperature", "top_p", "seed", "presence_penalty", "frequency_penalty"} {
		if body[name] != nil {
			return nil, unsupported("%s: not verifiable offline for Cursor", name)
		}
	}
	for _, name := range []string{"stop", "logit_bias"} {
		if concerns.Present(body[name]) {
			return nil, unsupported("%s: not verifiable offline for Cursor", name)
		}
	}
	if err := textResponseFormat(body["response_format"]); err != nil {
		return nil, err
	}
	effort, err := reasoningEffort(body)
	if err != nil {
		return nil, err
	}
	if repair {
		concerns.EnsureToolCallIDs(body)
	}
	messages, err := cursorMessages(concerns.Items(body["messages"]))
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		return nil, unsupported("no message survived conversion")
	}
	tools, err := cursorTools(body["tools"])
	if err != nil {
		return nil, err
	}
	switch choice := body["tool_choice"]; {
	case !concerns.Truthy(choice), choice == "auto":
		// Absent already means auto, and the pin strips tool_choice (:176).
	case choice == "none":
		// The wire has no tool_choice, so the only way to honour "none" is to
		// withhold the tools. Nothing else about the request is touched.
		tools = nil
	default:
		return nil, unsupported("tool_choice %s: Cursor cannot require a tool", concerns.JSONText(choice))
	}
	result := map[string]any{"model": model, "stream": stream, "messages": messages}
	if len(tools) > 0 {
		result["tools"] = tools
	}
	if effort != "" {
		result["reasoning_effort"] = effort
	}
	// max_tokens, max_completion_tokens: not carried. The pin overwrites the field
	// with a constant (:181) and its executor never reads it, and the pin's
	// protobuf has no field for a token limit. A client that always sends one
	// must still be served, so the field is not refused (PARITY 6.5, V31).
	return json.Marshal(result)
}

// textResponseFormat accepts the response formats that ask for plain text.
func textResponseFormat(value any) error {
	if value == nil {
		return nil
	}
	format := concerns.Object(value)
	if format == nil {
		return unsupported("response_format must be an object")
	}
	if kind := concerns.Text(format["type"]); kind != "" && kind != "text" {
		return unsupported("response_format %q: not verifiable offline for Cursor", kind)
	}
	return nil
}

// reasoningEffort reads the effort the executor reads: reasoning_effort, else
// reasoning.effort (executors/cursor.js:618). Other keys of `reasoning` are
// display hints at most, so only `summary` is tolerated beside `effort`.
func reasoningEffort(body map[string]any) (string, error) {
	effort := body["reasoning_effort"]
	if reasoning := body["reasoning"]; reasoning != nil {
		object := concerns.Object(reasoning)
		if object == nil {
			return "", unsupported("reasoning must be an object")
		}
		for key := range object {
			if key != "effort" && key != "summary" {
				return "", unsupported("reasoning.%s: not verifiable offline for Cursor", key)
			}
		}
		if !concerns.Truthy(effort) {
			effort = object["effort"]
		}
	}
	switch value := effort.(type) {
	case nil:
		return "", nil
	case string:
		return value, nil
	}
	return "", unsupported("reasoning effort must be a string")
}

// cursorTools validates the function tools the executor turns into MCP tools.
// A tool it cannot express is refused: the pin's executor would encode garbage
// for it (executors/cursor.js:168).
func cursorTools(value any) ([]any, error) {
	if value == nil {
		return nil, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, unsupported("tools must be a list")
	}
	for _, raw := range list {
		tool := concerns.Object(raw)
		function := concerns.Object(tool["function"])
		if tool == nil || concerns.Text(tool["type"]) != "function" || function == nil || concerns.Text(function["name"]) == "" {
			return nil, unsupported("tool that is not a named function")
		}
		if concerns.Truthy(function["strict"]) {
			return nil, unsupported("strict tool schema: not verifiable offline for Cursor")
		}
		// A tool with no parameters is a valid function tool, and the executor's
		// encoder defaults them (cursorProtobuf.js:443); parameters that are not an
		// object cannot be encoded as a schema.
		if parameters := function["parameters"]; parameters != nil && concerns.Object(parameters) == nil {
			return nil, unsupported("tool parameters must be an object")
		}
	}
	return list, nil
}

func unsupported(format string, args ...any) error {
	return fmt.Errorf("unsupported: "+format, args...)
}

var controlCharacters = regexp.MustCompile("[\x00-\x08\x0B\x0C\x0E-\x1F\x7F]")

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")

// toolResultBlock is buildToolResultBlock (:28-46): the pin sends a tool output
// as structured text in a user turn, because protobuf tool results made Cursor
// loop (:3-7).
func toolResultBlock(name, id, result string) string {
	if name == "" {
		name = "tool"
	}
	return strings.Join([]string{
		"<tool_result>",
		"<tool_name>" + xmlEscaper.Replace(name) + "</tool_name>",
		"<tool_call_id>" + xmlEscaper.Replace(id) + "</tool_call_id>",
		"<result>" + xmlEscaper.Replace(controlCharacters.ReplaceAllString(result, "")) + "</result>",
		"</tool_result>",
	}, "\n")
}

// cursorMessages is convertMessages (:52-170).
func cursorMessages(source []any) ([]any, error) {
	// tool_call_id to name, from every assistant turn (:55-79). The pin also
	// looks an id up by its first line (:48-50, :117-119).
	names := map[string]string{}
	for _, raw := range source {
		message := concerns.Object(raw)
		if concerns.Text(message["role"]) != "assistant" {
			continue
		}
		for _, rawCall := range concerns.Items(message["tool_calls"]) {
			call := concerns.Object(rawCall)
			id, name := concerns.Text(call["id"]), concerns.Text(concerns.Object(call["function"])["name"])
			if id == "" {
				continue
			}
			if name == "" {
				name = "tool"
			}
			names[id] = name
			if first, _, _ := strings.Cut(id, "\n"); first != id {
				names[first] = name
			}
		}
	}
	result := make([]any, 0, len(source))
	for _, raw := range source {
		message := concerns.Object(raw)
		if message == nil {
			return nil, unsupported("message is not an object")
		}
		role := concerns.Text(message["role"])
		switch role {
		case "system", "developer":
			// A system turn is a user turn (:84-90). The pin drops a developer
			// message without a word (it matches no role at :104); it is an
			// instruction, so it is treated as a system one (PARITY V21).
			text, err := contentText(message["content"], "")
			if err != nil {
				return nil, err
			}
			result = append(result, map[string]any{"role": "user", "content": "[System Instructions]\n" + text})
		case "tool":
			text, err := contentText(message["content"], "")
			if err != nil {
				return nil, err
			}
			id := concerns.Text(message["tool_call_id"])
			if id == "" {
				// The pin would send an empty <tool_call_id> (:94); the model cannot
				// tell which call the result answers.
				return nil, unsupported("tool result without a tool_call_id")
			}
			name := concerns.Text(message["name"])
			if name == "" {
				name = names[id]
			}
			result = append(result, map[string]any{"role": "user", "content": toolResultBlock(name, id, text)})
		case "user":
			// Parts of a user turn are joined with a newline (:105-127).
			text, err := contentText(message["content"], "\n")
			if err != nil {
				return nil, err
			}
			if text != "" {
				result = append(result, map[string]any{"role": "user", "content": text})
			}
		case "assistant":
			text, err := contentText(message["content"], "")
			if err != nil {
				return nil, err
			}
			calls, err := assistantCalls(message["tool_calls"])
			if err != nil {
				return nil, err
			}
			switch {
			case len(calls) > 0:
				result = append(result, map[string]any{"role": "assistant", "content": text, "tool_calls": calls})
			case text != "":
				result = append(result, map[string]any{"role": "assistant", "content": text})
			}
		default:
			return nil, unsupported("message role %q", role)
		}
	}
	return result, nil
}

// assistantCalls keeps a turn's calls as Chat tool calls (:132-138). The
// executor flattens them into text, so the arguments only need to be text.
func assistantCalls(value any) ([]any, error) {
	if value != nil {
		if _, ok := value.([]any); !ok {
			return nil, unsupported("tool_calls must be a list")
		}
	}
	var calls []any
	for _, raw := range concerns.Items(value) {
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
		arguments := "{}"
		switch v := function["arguments"].(type) {
		case string:
			if v != "" {
				arguments = v
			}
		case nil:
		default:
			arguments = concerns.JSONText(v)
		}
		calls = append(calls, map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": arguments}})
	}
	return calls, nil
}

// contentText is extractContent (:14-26): the text of a content value. Anything
// the model would not see is refused, because the pin drops it silently:
// images and audio (the agent path takes text only, executors/cursor.js:81-90),
// files and unknown parts.
func contentText(content any, separator string) (string, error) {
	switch value := content.(type) {
	case nil:
		return "", nil
	case string:
		return value, nil
	case []any:
		var texts []string
		for _, raw := range value {
			part := concerns.Object(raw)
			switch kind := concerns.Text(part["type"]); kind {
			case "text", "refusal":
				text, isText := part[kind].(string)
				if !isText {
					return "", unsupported("%s part without text", kind)
				}
				texts = append(texts, text)
			default:
				return "", unsupported("content part %q: Cursor takes text only", kind)
			}
		}
		return strings.Join(texts, separator), nil
	}
	return "", unsupported("content must be a string or a list of parts")
}
