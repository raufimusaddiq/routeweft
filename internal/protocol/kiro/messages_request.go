package kiro

import (
	"fmt"
	"sort"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// messagesAdvisory are Messages fields accepted and not carried: the answer is
// the same without them and Kiro's request has nowhere to put them (PARITY 6.11).
var messagesAdvisory = map[string]bool{"model": true, "stream": true, "metadata": true, "service_tier": true}

// messagesCarried are read by the leg or checked by it.
var messagesCarried = map[string]bool{
	"messages": true, "system": true, "tools": true, "tool_choice": true, "temperature": true, "top_p": true,
	"max_tokens": true, "thinking": true, "output_config": true, "cache_control": true, "stop_sequences": true,
}

// checkCacheControl accepts the marker that asks for nothing Kiro could do
// differently: ephemeral, with the two lifetimes Anthropic defines. Kiro has no
// cache field, so it is advisory (the pin drops it too, claude-to-kiro.js:91-126);
// a marker that names anything else is not one Routeweft understands.
func checkCacheControl(value any, path string) error {
	if value == nil {
		return nil
	}
	control := concerns.Object(value)
	if control == nil {
		return unsupported("%s.cache_control must be an object", path)
	}
	if err := requestKeys(control, path+".cache_control", "type", "ttl"); err != nil {
		return err
	}
	if control["type"] != "ephemeral" {
		return unsupported("%s.cache_control.type %s is not ephemeral", path, concerns.JSONText(control["type"]))
	}
	if ttl := control["ttl"]; ttl != nil && ttl != "5m" && ttl != "1h" {
		return unsupported("%s.cache_control.ttl %s is not 5m or 1h", path, concerns.JSONText(ttl))
	}
	return nil
}

func checkMessagesFields(body map[string]any) error {
	keys := make([]string, 0, len(body))
	for key := range body {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := body[key]
		if value == nil || messagesAdvisory[key] || messagesCarried[key] {
			continue
		}
		switch key {
		case "top_k":
			return unsupported("top_k: Kiro has no top_k")
		case "mcp_servers", "container", "context_management":
			return unsupported("request field %q changes what the server does with the request, which Kiro cannot", key)
		}
		return unsupported("request field %q: Kiro has no equivalent", key)
	}
	if stop := body["stop_sequences"]; stop != nil {
		if list, isList := stop.([]any); !isList || len(list) > 0 {
			return unsupported("stop_sequences: Kiro cannot stop on a sequence")
		}
	}
	if err := checkCacheControl(body["cache_control"], "request"); err != nil {
		return err
	}
	if choice := body["tool_choice"]; choice != nil {
		object := concerns.Object(choice)
		if object == nil || object["type"] != "auto" {
			return unsupported("tool_choice %s: Kiro has no tool_choice", concerns.JSONText(choice))
		}
		if err := requestKeys(object, "tool_choice", "type", "disable_parallel_tool_use"); err != nil {
			return err
		}
		if value := object["disable_parallel_tool_use"]; value != nil && value != false {
			return unsupported("tool_choice.disable_parallel_tool_use: Kiro cannot forbid parallel calls")
		}
	}
	if config := concerns.Object(body["output_config"]); config != nil && config["format"] != nil {
		return unsupported("output_config.format: Kiro has no structured-output mode")
	}
	return validateThinking(body)
}

// systemText is extractClaudeSystemText (claude-to-kiro.js:216-226): text blocks
// join with a newline. A block that is not text is refused where the pin reads
// its text field and drops the rest.
func systemText(system any) (string, error) {
	switch v := system.(type) {
	case nil:
		return "", nil
	case string:
		return v, nil
	case []any:
		var texts []string
		for i, raw := range v {
			block := concerns.Object(raw)
			if block == nil || block["type"] != "text" {
				return "", unsupported("system[%d] must be a text block", i)
			}
			if err := requestKeys(block, fmt.Sprintf("system[%d]", i), "type", "text", "cache_control"); err != nil {
				return "", err
			}
			if err := checkCacheControl(block["cache_control"], fmt.Sprintf("system[%d]", i)); err != nil {
				return "", err
			}
			text, isText := block["text"].(string)
			if !isText {
				return "", unsupported("system[%d].text must be a string", i)
			}
			if text != "" {
				texts = append(texts, text)
			}
		}
		return strings.Join(texts, "\n"), nil
	}
	return "", unsupported("system must be a string or a list of text blocks")
}

// toolResult reads a tool_result block (claude-to-kiro.js:97-125). The pin sends
// the JSON of content it does not understand and attaches a tool's images to the
// user turn; both are refused here, since Kiro's tool result is text and an image
// moved off its result no longer says what it answers.
func toolResult(block map[string]any, path string) (result, error) {
	if err := requestKeys(block, path, "type", "tool_use_id", "content", "is_error", "cache_control"); err != nil {
		return result{}, err
	}
	if err := checkCacheControl(block["cache_control"], path); err != nil {
		return result{}, err
	}
	id, _ := block["tool_use_id"].(string)
	if id == "" {
		return result{}, unsupported("%s needs tool_use_id", path)
	}
	status := "success"
	switch block["is_error"] {
	case nil, false:
	case true:
		status = "error"
	default:
		return result{}, unsupported("%s.is_error must be a boolean", path)
	}
	var text string
	switch content := block["content"].(type) {
	case nil:
	case string:
		text = content
	case []any:
		var texts []string
		for j, raw := range content {
			part := concerns.Object(raw)
			switch {
			case part == nil:
				return result{}, unsupported("%s.content[%d] must be an object", path, j)
			case part["type"] == "image":
				return result{}, unsupported("%s.content[%d]: a tool result cannot carry images", path, j)
			case part["type"] != "text":
				return result{}, unsupported("%s.content[%d]: block type %s has no Kiro equivalent", path, j, concerns.JSONText(part["type"]))
			}
			if err := requestKeys(part, fmt.Sprintf("%s.content[%d]", path, j), "type", "text", "cache_control"); err != nil {
				return result{}, err
			}
			if err := checkCacheControl(part["cache_control"], fmt.Sprintf("%s.content[%d]", path, j)); err != nil {
				return result{}, err
			}
			partText, isText := part["text"].(string)
			if !isText {
				return result{}, unsupported("%s.content[%d].text must be a string", path, j)
			}
			texts = append(texts, partText)
		}
		text = strings.Join(texts, "\n")
	default:
		return result{}, unsupported("%s.content must be a string or a list of text blocks", path)
	}
	return result{id: id, status: status, text: text}, nil
}

func userBlocks(t *turn, content any, index int) error {
	switch v := content.(type) {
	case nil:
		return nil
	case string:
		if v != "" {
			t.pieces = append(t.pieces, v)
		}
		return nil
	case []any:
		for j, raw := range v {
			path := fmt.Sprintf("messages[%d].content[%d]", index, j)
			block := concerns.Object(raw)
			if block == nil {
				return unsupported("%s must be an object", path)
			}
			switch block["type"] {
			case "text":
				if err := requestKeys(block, path, "type", "text", "cache_control"); err != nil {
					return err
				}
				if err := checkCacheControl(block["cache_control"], path); err != nil {
					return err
				}
				text, isText := block["text"].(string)
				if !isText {
					return unsupported("%s.text must be a string", path)
				}
				if text != "" {
					t.pieces = append(t.pieces, text)
				}
			case "image":
				if err := requestKeys(block, path, "type", "source", "cache_control"); err != nil {
					return err
				}
				if err := checkCacheControl(block["cache_control"], path); err != nil {
					return err
				}
				source := concerns.Object(block["source"])
				if source == nil {
					return unsupported("%s.source must be an object", path)
				}
				if source["type"] != "base64" {
					return unsupported("%s: an image must be base64: Kiro takes image bytes, not a link", path)
				}
				if err := requestKeys(source, path+".source", "type", "media_type", "data"); err != nil {
					return err
				}
				media, _ := source["media_type"].(string)
				data, _ := source["data"].(string)
				image, err := kiroImage(media, data)
				if err != nil {
					return fmt.Errorf("%s: %w", path, err)
				}
				t.images = append(t.images, image)
			case "tool_result":
				r, err := toolResult(block, path)
				if err != nil {
					return err
				}
				t.results = append(t.results, r)
			default:
				return unsupported("%s: block type %s has no Kiro equivalent", path, concerns.JSONText(block["type"]))
			}
		}
		return nil
	}
	return unsupported("messages[%d].content must be a string or a list of blocks", index)
}

func assistantBlocks(t *turn, content any, index int) error {
	switch v := content.(type) {
	case nil:
		return nil
	case string:
		if text := strings.TrimSpace(v); text != "" {
			t.pieces = append(t.pieces, text)
		}
		return nil
	case []any:
		var texts []string
		for j, raw := range v {
			path := fmt.Sprintf("messages[%d].content[%d]", index, j)
			block := concerns.Object(raw)
			if block == nil {
				return unsupported("%s must be an object", path)
			}
			switch block["type"] {
			case "text":
				if err := requestKeys(block, path, "type", "text", "cache_control"); err != nil {
					return err
				}
				if err := checkCacheControl(block["cache_control"], path); err != nil {
					return err
				}
				text, isText := block["text"].(string)
				if !isText {
					return unsupported("%s.text must be a string", path)
				}
				texts = append(texts, text)
			case "thinking", "redacted_thinking":
				// Kiro has no reasoning history to put them in. They are the
				// model's own earlier thoughts, not the client's instructions, and
				// the answer does not depend on them being replayed (the pin drops
				// them, claude-to-kiro.js:135-143).
				if err := requestKeys(block, path, "type", "thinking", "signature", "data"); err != nil {
					return err
				}
			case "tool_use":
				if err := requestKeys(block, path, "type", "id", "name", "input", "cache_control"); err != nil {
					return err
				}
				if err := checkCacheControl(block["cache_control"], path); err != nil {
					return err
				}
				id, _ := block["id"].(string)
				name, _ := block["name"].(string)
				if id == "" || name == "" {
					return unsupported("%s needs an id and a name", path)
				}
				input, err := strictArguments(block["input"])
				if err != nil {
					return fmt.Errorf("%s: %w", path, err)
				}
				t.uses = append(t.uses, use{id: id, name: name, input: input})
			default:
				return unsupported("%s: block type %s has no Kiro equivalent", path, concerns.JSONText(block["type"]))
			}
		}
		if text := strings.TrimSpace(strings.Join(texts, "\n")); text != "" {
			t.pieces = append(t.pieces, text)
		}
		return nil
	}
	return unsupported("messages[%d].content must be a string or a list of blocks", index)
}

// messageTurns groups Messages into alternating turns (claude-to-kiro.js:45-214,
// kiroConversation.js:158-202 minus the invented turns).
func messageTurns(messages []any) ([]*turn, error) {
	var turns []*turn
	var current *turn
	for i, raw := range messages {
		message := concerns.Object(raw)
		if message == nil {
			return nil, unsupported("messages[%d] must be an object", i)
		}
		if err := requestKeys(message, fmt.Sprintf("messages[%d]", i), "role", "content"); err != nil {
			return nil, err
		}
		role, _ := message["role"].(string)
		if role != "user" && role != "assistant" {
			return nil, unsupported("messages[%d]: role %s has no Kiro equivalent", i, concerns.JSONText(message["role"]))
		}
		assistant := role == "assistant"
		if current == nil || current.assistant != assistant {
			current = &turn{assistant: assistant}
			turns = append(turns, current)
		}
		var err error
		if assistant {
			err = assistantBlocks(current, message["content"], i)
		} else {
			err = userBlocks(current, message["content"], i)
		}
		if err != nil {
			return nil, err
		}
	}
	return turns, nil
}

// messagesTools reads Anthropic tools as the Chat function tools toolSpecs
// validates: the two carry the same name, description and JSON schema.
func messagesTools(raw any) ([]any, error) {
	if raw == nil {
		return nil, nil
	}
	list, isList := raw.([]any)
	if !isList {
		return nil, unsupported("tools must be a list")
	}
	tools := make([]any, len(list))
	for i, entry := range list {
		tool := concerns.Object(entry)
		if tool == nil {
			return nil, unsupported("tools[%d] must be an object", i)
		}
		if kind := tool["type"]; kind != nil && kind != "custom" {
			return nil, unsupported("tools[%d]: type %s is a server tool: Kiro runs none", i, concerns.JSONText(kind))
		}
		if err := requestKeys(tool, fmt.Sprintf("tools[%d]", i), "type", "name", "description", "input_schema", "cache_control", "strict"); err != nil {
			return nil, err
		}
		if err := checkCacheControl(tool["cache_control"], fmt.Sprintf("tools[%d]", i)); err != nil {
			return nil, err
		}
		function := map[string]any{"name": tool["name"], "description": tool["description"], "parameters": tool["input_schema"]}
		if tool["strict"] != nil {
			function["strict"] = tool["strict"]
		}
		tools[i] = map[string]any{"type": "function", "function": function}
	}
	return tools, nil
}

// MessagesRequestToKiro is the request leg anthropic-messages>kiro
// (translator/request/claude-to-kiro.js:231-366, registered :366). The pin
// dispatches it directly, with no Chat pivot (translator/index.js:106-108).
//
// It returns the body for GenerateAssistantResponse, the tool names it rewrote
// so the response can put the client's own back, and what the response leg
// needs to know. Every Messages field, block and tool is carried, advisory or
// refused with an error (docs/PARITY.md 6.11); nothing is dropped silently. The
// conversation is built and checked by the code the Chat leg uses, so the two
// legs refuse the same malformed histories.
func MessagesRequestToKiro(body map[string]any, options RequestOptions) ([]byte, *concerns.ToolNames, RequestInfo, error) {
	var info RequestInfo
	if err := checkMessagesFields(body); err != nil {
		return nil, nil, info, err
	}
	messages, isList := body["messages"].([]any)
	if !isList || len(messages) == 0 {
		return nil, nil, info, unsupported("messages must be a non-empty list")
	}
	intent, err := intentOf(options.Model)
	if err != nil {
		return nil, nil, info, err
	}
	info.UpstreamModel = intent.upstream
	sampling, err := samplingOf(body)
	if err != nil {
		return nil, nil, info, err
	}
	system, err := systemText(body["system"])
	if err != nil {
		return nil, nil, info, err
	}
	tools, err := messagesTools(body["tools"])
	if err != nil {
		return nil, nil, info, err
	}
	turns, err := messageTurns(messages)
	if err != nil {
		return nil, nil, info, err
	}
	specs, names, err := toolSpecs(tools, nil, &info.Normalized)
	if err != nil {
		return nil, nil, info, err
	}
	built := assembly{turns: turns, specs: specs, intent: intent, thinkingBody: applyOverride(body, intent.override),
		beta: options.AnthropicBeta, system: system, sampling: sampling, newID: options.NewConversationID}
	encoded, err := built.payload(&info)
	return encoded, names, info, err
}
