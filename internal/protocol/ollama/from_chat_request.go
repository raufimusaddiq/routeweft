package ollama

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// ChatRequestToOllama is the request leg openai-chat>ollama
// (translator/request/openai-to-ollama.js:21-57, messages :65-145). It returns
// the body for Ollama's /api/chat. repair is true when body is the client's own
// Chat body, because the pin repairs tool ids only on the client's body
// (translator/index.js:82); a Chat body produced from another format arrives
// repaired. It mutates body.
//
// What it does with every Chat field is the table in docs/PARITY.md 6.4. A field
// is carried, not carried for a stated reason (the request is still served and
// nothing it asks for is lost), or refused with an error, so the dispatcher
// tries the next candidate. Nothing is dropped silently.
func ChatRequestToOllama(body map[string]any, model string, stream, repair bool) ([]byte, error) {
	if reason := concerns.UncarriableChat(body, "Ollama"); reason != "" {
		return nil, unsupported("%s", reason)
	}
	if repair {
		concerns.EnsureToolCallIDs(body)
	}
	messages, err := ollamaMessages(concerns.Items(body["messages"]))
	if err != nil {
		return nil, err
	}
	if len(messages) == 0 {
		// The pin would send an empty list, which Ollama answers with an empty
		// completion. That is a wrong answer, not a served request.
		return nil, unsupported("no message survived conversion")
	}
	result := map[string]any{"model": model, "messages": messages, "stream": stream}
	options := map[string]any{}
	if value := body["temperature"]; value != nil {
		options["temperature"] = value
	}
	// The pin reads only max_tokens (:35) and leaves a caller that used the
	// current name, max_completion_tokens, unbounded (PARITY V22).
	if value := body["max_tokens"]; value != nil {
		options["num_predict"] = value
	} else if value := body["max_completion_tokens"]; value != nil {
		options["num_predict"] = value
	}
	if value := body["top_p"]; value != nil {
		options["top_p"] = value
	}
	// The pin sends none of these (:28-44) although /api/chat takes each in
	// options: a stop string ignored runs generation past it (PARITY V29).
	for _, name := range []string{"seed", "presence_penalty", "frequency_penalty"} {
		if value := body[name]; value != nil {
			options[name] = value
		}
	}
	stop, err := stopSequences(body["stop"])
	if err != nil {
		return nil, err
	}
	if len(stop) > 0 {
		options["stop"] = stop
	}
	if len(options) > 0 {
		result["options"] = options
	}
	format, err := responseFormat(body["response_format"])
	if err != nil {
		return nil, err
	}
	if format != nil {
		result["format"] = format
	}
	if concerns.Present(body["logit_bias"]) {
		return nil, unsupported("logit_bias: Ollama has no equivalent")
	}
	tools, hasTools := body["tools"].([]any)
	switch choice := body["tool_choice"]; {
	case !concerns.Truthy(choice), choice == "auto":
		// Absent already means auto to Ollama, which has no tool_choice field, so
		// the pin's verbatim forward (:51-54) is not sent (PARITY V30).
		if hasTools {
			result["tools"] = tools
		}
	case choice == "none":
		// Ollama has no tool_choice, so the pin's passthrough (:51-54) lets a
		// model call a tool the client forbade. Withholding the tools is how this
		// wire honours "none" (PARITY V30).
	default:
		return nil, unsupported("tool_choice %s: Ollama cannot require a tool", concerns.JSONText(choice))
	}
	return json.Marshal(result)
}

// stopSequences accepts a Chat stop value, a string or a list of strings.
func stopSequences(stop any) ([]any, error) {
	switch value := stop.(type) {
	case nil:
		return nil, nil
	case string:
		if value == "" {
			return nil, nil
		}
		return []any{value}, nil
	case []any:
		for _, item := range value {
			if _, ok := item.(string); !ok {
				return nil, unsupported("stop must be a string or a list of strings")
			}
		}
		return value, nil
	}
	return nil, unsupported("stop must be a string or a list of strings")
}

// responseFormat is Chat's response_format as Ollama's `format`: the string
// "json" for json_object, the schema itself for json_schema. Text asks for
// nothing. Anything else is refused: JSON the client asked for must not arrive
// as prose.
func responseFormat(value any) (any, error) {
	format := concerns.Object(value)
	if value == nil {
		return nil, nil
	}
	if format == nil {
		return nil, unsupported("response_format must be an object")
	}
	switch kind := concerns.Text(format["type"]); kind {
	case "", "text":
		return nil, nil
	case "json_object":
		return "json", nil
	case "json_schema":
		if schema := concerns.Object(concerns.Object(format["json_schema"])["schema"]); schema != nil {
			return schema, nil
		}
		return nil, unsupported("response_format json_schema without a schema")
	default:
		return nil, unsupported("response_format type %q", kind)
	}
}

func unsupported(format string, args ...any) error {
	return fmt.Errorf("unsupported: "+format, args...)
}

// ollamaMessages is normalizeMessages (openai-to-ollama.js:65-145).
func ollamaMessages(source []any) ([]any, error) {
	// tool_call_id to tool name, from every assistant turn (:71-80).
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
	result := make([]any, 0, len(source))
	for _, raw := range source {
		message := concerns.Object(raw)
		if message == nil {
			return nil, unsupported("message is not an object")
		}
		role := concerns.Text(message["role"])
		switch role {
		case "developer":
			// Ollama has no such role; a developer message is a system message
			// (PARITY V21). The pin forwards the role unchanged.
			role = "system"
		case "system", "user", "assistant", "tool":
		default:
			return nil, unsupported("message role %q", role)
		}
		calls := concerns.Items(message["tool_calls"])
		switch {
		case role == "tool":
			text, _, err := splitContent(message["content"], false)
			if err != nil {
				return nil, err
			}
			if text == "" {
				// Mirrored (:87): an empty tool result is dropped, which leaves its
				// call unanswered. PARITY M2.
				continue
			}
			name := names[concerns.Text(message["tool_call_id"])]
			if name == "" {
				name = concerns.Text(message["name"])
			}
			if name == "" {
				name = "unknown_tool"
			}
			result = append(result, map[string]any{"role": "tool", "tool_name": name, "content": text})
		case role == "assistant" && len(calls) > 0:
			text, _, err := splitContent(message["content"], false)
			if err != nil {
				return nil, err
			}
			converted := make([]any, 0, len(calls))
			for position, rawCall := range calls {
				call := concerns.Object(rawCall)
				if kind := concerns.Text(call["type"]); kind != "" && kind != "function" {
					return nil, unsupported("tool call type %q", kind)
				}
				function := concerns.Object(call["function"])
				// The pin gives every call index 0 unless the client sent one
				// (:108); the position in the turn is the index Ollama reads.
				index := any(position)
				if given := call["index"]; concerns.IsNumber(given) {
					index = given
				}
				converted = append(converted, map[string]any{"type": "function", "function": map[string]any{
					"index": index, "name": concerns.Text(function["name"]), "arguments": concerns.ToolInput(function["arguments"]),
				}})
			}
			result = append(result, map[string]any{"role": "assistant", "content": text, "tool_calls": converted})
		default:
			text, images, err := splitContent(message["content"], true)
			if err != nil {
				return nil, err
			}
			// Mirrored (:130): a turn with no text is dropped even when it holds
			// an image, and the image goes with it. PARITY M1.
			if text == "" && role != "assistant" {
				continue
			}
			out := map[string]any{"role": role, "content": text}
			if len(images) > 0 {
				out["images"] = images
			}
			result = append(result, out)
		}
	}
	return result, nil
}

// splitContent separates a Chat content value into the text Ollama takes (parts
// joined with a newline, :156-163) and raw base64 images (:174-192). Anything the
// model would not see is refused: a remote image (fetching one is an outbound
// request this wire does not make; PARITY V23), audio, files and unknown parts.
func splitContent(content any, allowImages bool) (text string, images []string, err error) {
	if value, ok := content.(string); ok {
		return value, nil, nil
	}
	var texts []string
	for _, raw := range concerns.Items(content) {
		part := concerns.Object(raw)
		switch kind := concerns.Text(part["type"]); kind {
		case "text", "refusal":
			key := kind
			if concerns.Text(part[key]) != "" {
				texts = append(texts, concerns.Text(part[key]))
			}
		case "image_url":
			if !allowImages {
				return "", nil, unsupported("image in a tool call turn or tool result")
			}
			url := part["image_url"]
			if object := concerns.Object(url); object != nil {
				url = object["url"]
			}
			_, data, ok := concerns.ParseDataURI(url)
			if !ok {
				return "", nil, unsupported("image_url that is not a base64 data URI")
			}
			images = append(images, data)
		default:
			return "", nil, unsupported("content part %q", kind)
		}
	}
	return strings.Join(texts, "\n"), images, nil
}
