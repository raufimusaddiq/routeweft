package gemini

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/protocol/anthropic"
	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// GeminiRequestToChat is the request leg gemini>openai-chat
// (translator/request/gemini-to-openai.js:9-147, registered for gemini and
// gemini-cli at :150-151): a Gemini client's generateContent body as a Chat
// body. It never reads the client's thought signatures; a Chat provider has no
// use for them.
//
// What it does with every Gemini field is the table in docs/PARITY.md 6.7. A
// field is carried, not carried for a stated reason, or refused. Nothing is
// dropped silently.
func GeminiRequestToChat(body map[string]any, model string, stream bool) ([]byte, error) {
	chat := map[string]any{"model": model, "messages": []any{}, "stream": stream}
	if raw := body["cachedContent"]; concerns.Present(raw) {
		return nil, unsupported("cachedContent refers to content held by Gemini")
	}
	config := concerns.Object(body["generationConfig"])
	if body["generationConfig"] != nil && config == nil {
		return nil, unsupported("generationConfig must be an object")
	}
	tools, hasTools, err := chatTools(body["tools"])
	if err != nil {
		return nil, err
	}
	if err := chatGeneration(config, tools, chat); err != nil {
		return nil, err
	}
	system := body["systemInstruction"]
	if system == nil {
		system = body["system_instruction"]
	}
	messages := []any{}
	if system != nil {
		text, err := systemInstructionText(system)
		if err != nil {
			return nil, err
		}
		if text != "" {
			messages = append(messages, map[string]any{"role": "system", "content": text})
		}
	}
	converter := &chatConverter{}
	contents, ok := body["contents"].([]any)
	if body["contents"] != nil && !ok {
		return nil, unsupported("contents must be a list")
	}
	for index, raw := range contents {
		content := concerns.Object(raw)
		if content == nil {
			return nil, unsupported("a content is not an object")
		}
		converted, err := converter.content(index, content)
		if err != nil {
			return nil, err
		}
		messages = append(messages, converted...)
	}
	chat["messages"] = messages
	if hasTools {
		chat["tools"] = tools
		choice, err := chatToolChoice(body["toolConfig"], tools)
		if err != nil {
			return nil, err
		}
		if choice != nil {
			chat["tool_choice"] = choice
		}
	}
	return json.Marshal(chat)
}

// chatGeneration carries generationConfig (:17-29).
func chatGeneration(config map[string]any, tools []any, chat map[string]any) error {
	if config == nil {
		return nil
	}
	if count := concerns.Int(config["candidateCount"]); count > 1 {
		return unsupported("candidateCount=%d: one completion is returned", count)
	}
	if concerns.Truthy(config["responseLogprobs"]) || concerns.Present(config["logprobs"]) {
		return unsupported("logprobs are not reported")
	}
	if limit := config["maxOutputTokens"]; concerns.Truthy(limit) {
		if !concerns.IsNumber(limit) {
			return unsupported("maxOutputTokens must be a number")
		}
		// The pin raises a tool-using request to 32000 (formats/maxTokens.js:15-20).
		chat["max_tokens"] = anthropic.AdjustMaxTokens(map[string]any{"max_tokens": limit, "tools": tools}, 0)
	}
	for field, target := range map[string]string{"temperature": "temperature", "topP": "top_p", "seed": "seed", "presencePenalty": "presence_penalty", "frequencyPenalty": "frequency_penalty"} {
		if value := config[field]; value != nil {
			if !concerns.IsNumber(value) {
				return unsupported("%s must be a number", field)
			}
			chat[target] = value
		}
	}
	// topK has no Chat field; thinkingConfig is deferred to #125 (PARITY 6.7).
	if raw := config["stopSequences"]; raw != nil {
		stop, err := stopSequences(raw)
		if err != nil {
			return err
		}
		if len(stop) > 0 {
			chat["stop"] = stop
		}
	}
	switch mime := concerns.Text(config["responseMimeType"]); mime {
	case "", "text/plain":
	case "application/json":
		schema := config["responseJsonSchema"]
		if schema == nil {
			schema = config["responseSchema"]
		}
		if schema == nil {
			chat["response_format"] = map[string]any{"type": "json_object"}
		} else {
			chat["response_format"] = map[string]any{"type": "json_schema", "json_schema": map[string]any{"name": "response", "schema": schema}}
		}
	default:
		return unsupported("responseMimeType %q", mime)
	}
	return nil
}

// chatTools converts functionDeclarations (:52-69). A tool of any other kind
// (googleSearch, codeExecution, urlContext, ...) is refused: the pin leaves it
// out and the model answers without a capability the client asked for.
func chatTools(value any) (tools []any, present bool, err error) {
	list, ok := value.([]any)
	if value != nil && !ok {
		return nil, false, unsupported("tools must be a list")
	}
	tools = []any{}
	for _, raw := range list {
		tool := concerns.Object(raw)
		for key := range tool {
			if key != "functionDeclarations" && key != "function_declarations" {
				return nil, false, unsupported("tool %q is not a function declaration", key)
			}
		}
		declarations := tool["functionDeclarations"]
		if declarations == nil {
			declarations = tool["function_declarations"]
		}
		for _, rawDeclaration := range concerns.Items(declarations) {
			declaration := concerns.Object(rawDeclaration)
			name := concerns.Text(declaration["name"])
			if name == "" {
				return nil, false, unsupported("a function declaration has no name")
			}
			parameters := declaration["parameters"]
			if parameters == nil {
				parameters = declaration["parametersJsonSchema"]
			}
			if parameters == nil {
				parameters = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{
				"name": name, "description": concerns.Text(declaration["description"]), "parameters": parameters,
			}})
		}
	}
	return tools, len(tools) > 0, nil
}

// chatToolChoice is toolConfig as tool_choice. The pin ignores toolConfig, so a
// client that forbade tools or required one is silently served with auto.
func chatToolChoice(value any, tools []any) (any, error) {
	if value == nil {
		return nil, nil
	}
	config := concerns.Object(value)
	if config == nil {
		return nil, unsupported("toolConfig must be an object")
	}
	for key := range config {
		if key != "functionCallingConfig" && key != "function_calling_config" {
			return nil, unsupported("toolConfig.%s", key)
		}
	}
	calling := concerns.Object(config["functionCallingConfig"])
	if calling == nil {
		calling = concerns.Object(config["function_calling_config"])
	}
	allowed := concerns.Items(calling["allowedFunctionNames"])
	switch mode := strings.ToUpper(concerns.Text(calling["mode"])); mode {
	case "", "AUTO", "MODE_UNSPECIFIED":
		if len(allowed) > 0 {
			return nil, unsupported("allowedFunctionNames without a mode that requires a call")
		}
		return nil, nil
	case "NONE":
		return "none", nil
	case "ANY":
		switch len(allowed) {
		case 0:
			return "required", nil
		case 1:
			name := concerns.Text(allowed[0])
			for _, raw := range tools {
				if concerns.Text(concerns.Object(concerns.Object(raw)["function"])["name"]) == name {
					return map[string]any{"type": "function", "function": map[string]any{"name": name}}, nil
				}
			}
			return nil, unsupported("allowedFunctionNames names %q, which is not among the tools", name)
		}
		return nil, unsupported("allowedFunctionNames with %d names: Chat can require one tool or any", len(allowed))
	default:
		return nil, unsupported("function calling mode %q", mode)
	}
}

func systemInstructionText(value any) (string, error) {
	switch v := value.(type) {
	case string:
		return v, nil
	case map[string]any:
		var texts []string
		for _, raw := range concerns.Items(v["parts"]) {
			part := concerns.Object(raw)
			for key := range part {
				if key != "text" {
					return "", unsupported("systemInstruction part %q: only text can be a system prompt", key)
				}
			}
			texts = append(texts, concerns.Text(part["text"]))
		}
		return strings.Join(texts, ""), nil
	}
	return "", unsupported("systemInstruction must be text or a content")
}

// chatConverter turns Gemini contents into Chat messages, pairing each
// functionResponse with the call it answers.
type chatConverter struct {
	// open are the calls not yet answered, oldest first.
	open []openCall
}

type openCall struct{ id, name string }

// content converts one Gemini content. A user content can hold text, images and
// any number of functionResponse parts; the pin returns at the FIRST
// functionResponse and discards everything else in the content (:112-118), so a
// parallel tool turn lost every result but one. Here each becomes a tool message.
func (c *chatConverter) content(index int, content map[string]any) ([]any, error) {
	role := concerns.Text(content["role"])
	if role != "" && role != "user" && role != "model" && role != "function" {
		return nil, unsupported("content role %q", role)
	}
	// A missing role is the user's turn, Gemini's default for a single-turn
	// request; the pin reads anything but "user" as the assistant (:76).
	parts, ok := content["parts"].([]any)
	if !ok {
		if content["parts"] == nil {
			return nil, nil
		}
		return nil, unsupported("parts must be a list")
	}
	var out []any
	var visible []any
	var texts []any
	var reasoning []string
	var calls []any
	flush := func() {
		if len(visible) == 0 {
			return
		}
		out = append(out, map[string]any{"role": "user", "content": collapse(visible)})
		visible = nil
	}
	for position, raw := range parts {
		part := concerns.Object(raw)
		if part == nil {
			return nil, unsupported("a part is not an object")
		}
		for key := range part {
			switch key {
			case "text", "thought", "thoughtSignature", "thought_signature", "inlineData", "inline_data", "functionCall", "functionResponse":
			default:
				return nil, unsupported("part field %q", key)
			}
		}
		if text, isText := part["text"].(string); isText {
			switch {
			case part["thought"] == true:
				reasoning = append(reasoning, text)
			case role == "model":
				texts = append(texts, map[string]any{"type": "text", "text": text})
			default:
				visible = append(visible, map[string]any{"type": "text", "text": text})
			}
		} else if part["text"] != nil {
			return nil, unsupported("text must be a string")
		}
		if data := firstObject(part, "inlineData", "inline_data"); data != nil {
			mediaType := concerns.Text(firstOf(data, "mimeType", "mime_type"))
			if !strings.HasPrefix(mediaType, "image/") || concerns.Text(data["data"]) == "" {
				return nil, unsupported("inlineData of type %q: only images can be carried", mediaType)
			}
			image := map[string]any{"type": "image_url", "image_url": map[string]any{"url": "data:" + mediaType + ";base64," + concerns.Text(data["data"])}}
			if role == "model" {
				return nil, unsupported("an image in a model turn")
			}
			visible = append(visible, image)
		}
		if call := concerns.Object(part["functionCall"]); call != nil {
			name := concerns.Text(call["name"])
			if name == "" {
				return nil, unsupported("a functionCall has no name")
			}
			// Gemini has no call id; the pin invents call_<name>, which two
			// parallel calls of one tool share (:103). Each occurrence gets its own.
			id := concerns.Text(call["id"])
			if id == "" {
				id = concerns.GenerateToolCallID(index, position, name)
			}
			arguments := call["args"]
			if arguments == nil {
				arguments = map[string]any{}
			}
			calls = append(calls, map[string]any{"id": id, "type": "function", "function": map[string]any{"name": name, "arguments": concerns.JSONText(arguments)}})
			c.open = append(c.open, openCall{id: id, name: name})
		}
		if response := concerns.Object(part["functionResponse"]); response != nil {
			flush()
			message, err := c.toolMessage(response)
			if err != nil {
				return nil, err
			}
			out = append(out, message)
		}
	}
	if role == "model" || len(calls) > 0 || len(reasoning) > 0 {
		message := map[string]any{"role": "assistant"}
		if len(texts) > 0 {
			message["content"] = collapse(texts)
		}
		if len(reasoning) > 0 {
			message["reasoning_content"] = strings.Join(reasoning, "")
		}
		if len(calls) > 0 {
			message["tool_calls"] = calls
		}
		if len(message) > 1 {
			out = append(out, message)
		}
		return out, nil
	}
	flush()
	return out, nil
}

// toolMessage answers a call by id, or by name when the response has no id.
func (c *chatConverter) toolMessage(response map[string]any) (map[string]any, error) {
	id, name := concerns.Text(response["id"]), concerns.Text(response["name"])
	position := -1
	for i, open := range c.open {
		if (id != "" && open.id == id) || (id == "" && open.name == name) {
			position = i
			break
		}
	}
	if position < 0 {
		return nil, unsupported("functionResponse %q answers no function call", fmt.Sprint(firstNonEmpty(id, name)))
	}
	answered := c.open[position]
	c.open = append(c.open[:position:position], c.open[position+1:]...)
	payload := response["response"]
	if object := concerns.Object(payload); object != nil && concerns.Truthy(object["result"]) {
		payload = object["result"]
	}
	if payload == nil {
		payload = map[string]any{}
	}
	return map[string]any{"role": "tool", "tool_call_id": answered.id, "content": concerns.JSONText(payload)}, nil
}

func collapse(parts []any) any {
	if len(parts) == 1 {
		if text := concerns.Object(parts[0]); concerns.Text(text["type"]) == "text" {
			return text["text"]
		}
	}
	return parts
}

func firstObject(part map[string]any, keys ...string) map[string]any {
	for _, key := range keys {
		if object := concerns.Object(part[key]); object != nil {
			return object
		}
	}
	return nil
}

func firstOf(object map[string]any, keys ...string) any {
	for _, key := range keys {
		if object[key] != nil {
			return object[key]
		}
	}
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}
