package gemini

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// turn is one Gemini content: a role and its parts.
type turn struct {
	role  string
	parts []any
}

type toolResult struct{ text string }

// conversation turns Chat messages into Gemini contents
// (openai-to-gemini.js:100-277).
type conversation struct {
	names   *concerns.ToolNames
	options RequestOptions

	contents []turn
	system   []string
	// results are the tool messages by tool_call_id, oldest first. An OpenAI
	// tool_call_id is only unique within its own assistant turn, so a long agent
	// session can reuse one id for two results; each call takes the oldest result
	// not yet taken (:114-130, #4273).
	results map[string][]toolResult
	taken   map[string]int
	// used makes every emitted functionCall id unique across the whole history,
	// which Gemini requires and a Chat id does not guarantee (:40-74, #4532).
	used map[string]bool
}

func (c *conversation) uniqueID(id string) string {
	if c.used == nil {
		c.used = map[string]bool{}
	}
	candidate := id
	for k := 2; c.used[candidate]; k++ {
		candidate = fmt.Sprintf("%s-%d", id, k)
	}
	c.used[candidate] = true
	return candidate
}

func (c *conversation) convert(messages []any) error {
	c.taken = map[string]int{}
	for _, raw := range messages {
		message := concerns.Object(raw)
		if message == nil {
			return unsupported("message is not an object")
		}
		if concerns.Text(message["role"]) != "tool" {
			continue
		}
		id := concerns.Text(message["tool_call_id"])
		if id == "" {
			return unsupported("a tool message has no tool_call_id")
		}
		text, err := toolText(message["content"])
		if err != nil {
			return err
		}
		c.results[id] = append(c.results[id], toolResult{text: text})
	}
	for index, raw := range messages {
		message := concerns.Object(raw)
		switch role := concerns.Text(message["role"]); role {
		case "system", "developer":
			if len(messages) == 1 {
				// A conversation of one system message is a user turn (:153).
				if err := c.user(message["content"]); err != nil {
					return err
				}
				continue
			}
			text, err := systemText(message["content"])
			if err != nil {
				return err
			}
			// The pin keeps only the LAST system message (:148-152); an earlier
			// one is silently gone, and a developer message was never read at all.
			if text != "" {
				c.system = append(c.system, text)
			}
		case "user":
			if err := c.user(message["content"]); err != nil {
				return err
			}
		case "assistant":
			if err := c.assistant(message, index == len(messages)-1); err != nil {
				return err
			}
		case "tool":
		default:
			return unsupported("message role %q", role)
		}
	}
	for id, queue := range c.results {
		if c.taken[id] < len(queue) {
			return unsupported("tool message %q answers no tool call", id)
		}
	}
	return nil
}

func (c *conversation) push(role string, parts []any) {
	if len(parts) > 0 {
		c.contents = append(c.contents, turn{role: role, parts: parts})
	}
}

func (c *conversation) user(content any) error {
	parts, err := userParts(content)
	if err != nil {
		return err
	}
	c.push("user", parts)
	return nil
}

func (c *conversation) assistant(message map[string]any, last bool) error {
	var parts []any
	if reasoning, ok := message["reasoning_content"].(string); ok && reasoning != "" {
		// A thought part and the signature it needs (:162-171). The signature is
		// the placeholder: the gateway never held one for replayed reasoning.
		parts = append(parts, map[string]any{"thought": true, "text": reasoning},
			map[string]any{"thoughtSignature": PlaceholderSignature, "text": ""})
	}
	text, err := assistantText(message["content"])
	if err != nil {
		return err
	}
	if text != "" {
		parts = append(parts, map[string]any{"text": text})
	}
	calls := concerns.Items(message["tool_calls"])
	if len(calls) == 0 {
		c.push("model", parts)
		return nil
	}
	type issued struct{ original, emitted, name string }
	var issuedCalls []issued
	for position, rawCall := range calls {
		call := concerns.Object(rawCall)
		if call == nil {
			return unsupported("a tool call is not an object")
		}
		// The pin skips a call that is not a function (:184).
		if kind := concerns.Text(call["type"]); kind != "" && kind != "function" {
			return unsupported("tool call type %q", kind)
		}
		function := concerns.Object(call["function"])
		name, id := concerns.Text(function["name"]), concerns.Text(call["id"])
		if name == "" {
			return unsupported("a tool call has no function name")
		}
		if id == "" {
			return unsupported("a tool call has no id")
		}
		safe, err := c.names.SafeChecked(name)
		if err != nil {
			return unsupported("%v", err)
		}
		arguments := concerns.ToolInput(function["arguments"])
		signature, cached := c.options.Signatures.Get(c.options.Scope, id, name, concerns.JSONText(arguments))
		if !cached && position == 0 && !c.options.OmitPlaceholderSignature {
			// The first call of a turn needs a signature; a sibling stays
			// unsigned when none was kept (:187-190).
			signature, cached = PlaceholderSignature, true
		}
		emitted := c.uniqueID(id)
		part := map[string]any{"functionCall": map[string]any{"id": emitted, "name": safe, "args": arguments}}
		if cached {
			part["thoughtSignature"] = signature
		}
		parts = append(parts, part)
		issuedCalls = append(issuedCalls, issued{original: id, emitted: emitted, name: name})
	}
	c.push("model", parts)
	answered := false
	for _, call := range issuedCalls {
		answered = answered || c.taken[call.original] < len(c.results[call.original])
	}
	// A call with no result still gets a reply when the conversation goes on, or
	// Gemini refuses the history; the pin sends an empty result (:224-237, V20).
	// A trailing turn with no result at all is closed by normalizeContents.
	if !answered && last {
		return nil
	}
	var replies []any
	for _, call := range issuedCalls {
		reply := ""
		if queue := c.results[call.original]; c.taken[call.original] < len(queue) {
			reply = queue[c.taken[call.original]].text
			c.taken[call.original]++
		}
		safe, _ := c.names.SafeChecked(call.name)
		replies = append(replies, map[string]any{"functionResponse": map[string]any{
			"id": call.emitted, "name": safe, "response": map[string]any{"result": responseValue(reply)},
		}})
	}
	c.push("user", replies)
	return nil
}

// responseValue is the pin's treatment of a tool result (:251-266): JSON that is
// an object is sent as it is, anything else as {result: value}. The caller wraps
// it in {result: ...} once more, as the pin does.
func responseValue(text string) any {
	decoder := json.NewDecoder(bytes.NewReader([]byte(text)))
	decoder.UseNumber()
	var parsed any
	if err := decoder.Decode(&parsed); err != nil || decoder.More() || parsed == nil {
		return map[string]any{"result": text}
	}
	if object, ok := parsed.(map[string]any); ok {
		return object
	}
	return map[string]any{"result": parsed}
}

// toolText is a tool message's content as text. A part that is not text would be
// a result the model cannot be shown.
func toolText(content any) (string, error) {
	switch value := content.(type) {
	case nil:
		return "", nil
	case string:
		return value, nil
	case []any:
		var texts []string
		for _, raw := range value {
			part := concerns.Object(raw)
			if concerns.Text(part["type"]) != "text" {
				return "", unsupported("tool result part %q: only text can be returned to Gemini", concerns.Text(part["type"]))
			}
			texts = append(texts, concerns.Text(part["text"]))
		}
		return strings.Join(texts, "\n"), nil
	}
	return "", unsupported("tool message content must be text")
}

// systemText is a system or developer message as text.
func systemText(content any) (string, error) {
	text, err := toolText(content)
	if err != nil {
		return "", unsupported("system content must be text")
	}
	return text, nil
}

// assistantText is an assistant message's text. A refusal part is text the model
// said; any other part is something Gemini would not be shown.
func assistantText(content any) (string, error) {
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
				texts = append(texts, concerns.Text(part[kind]))
			default:
				return "", unsupported("assistant content part %q", kind)
			}
		}
		return strings.Join(texts, ""), nil
	}
	return "", unsupported("assistant content must be text")
}

// userParts is convertOpenAIContentToParts (formats/gemini.js:56-111). Where the
// pin ignores a part it cannot convert, this refuses: the model would not see it.
func userParts(content any) ([]any, error) {
	switch value := content.(type) {
	case nil:
		return nil, nil
	case string:
		if value == "" {
			return nil, nil
		}
		return []any{map[string]any{"text": value}}, nil
	case []any:
		var parts []any
		for _, raw := range value {
			part := concerns.Object(raw)
			switch kind := concerns.Text(part["type"]); kind {
			case "text":
				if text := concerns.Text(part["text"]); text != "" {
					parts = append(parts, map[string]any{"text": text})
				}
			case "image_url":
				url := part["image_url"]
				if object := concerns.Object(url); object != nil {
					url = object["url"]
				}
				mediaType, data, ok := concerns.ParseDataURI(url)
				if !ok {
					if text, _ := url.(string); strings.HasPrefix(text, "http://") || strings.HasPrefix(text, "https://") {
						// The pin sends fileData with mimeType "image/*" (:77-80).
						return nil, unsupported("image_url that is a remote URL: fetching it is out of leg scope, send a base64 data URI")
					}
					return nil, unsupported("image_url that is not a base64 data URI")
				}
				parts = append(parts, inline(mediaType, data))
			case "input_audio":
				audio := concerns.Object(part["input_audio"])
				if concerns.Text(audio["data"]) == "" {
					return nil, unsupported("input_audio without data")
				}
				format := concerns.Text(audio["format"])
				if format == "" {
					format = "wav"
				}
				mediaType := "audio/" + format
				if format == "mp3" {
					mediaType = "audio/mpeg"
				}
				parts = append(parts, inline(mediaType, concerns.Text(audio["data"])))
			case "audio_url":
				url := part["audio_url"]
				if object := concerns.Object(url); object != nil {
					url = object["url"]
				}
				mediaType, data, ok := concerns.ParseDataURI(url)
				if !ok {
					return nil, unsupported("audio_url that is not a base64 data URI")
				}
				parts = append(parts, inline(mediaType, data))
			case "file":
				mediaType, data, ok := concerns.ParseDataURI(concerns.Object(part["file"])["file_data"])
				if !ok {
					return nil, unsupported("file that is not inline base64 data (file_id and URLs cannot be sent)")
				}
				parts = append(parts, inline(mediaType, data))
			default:
				return nil, unsupported("content part %q", kind)
			}
		}
		return parts, nil
	}
	return nil, unsupported("message content must be text or a list of parts")
}

func inline(mediaType, data string) map[string]any {
	// The data URI pattern leaves any ";param" in the media type (";charset=").
	mediaType, _, _ = strings.Cut(mediaType, ";")
	return map[string]any{"inlineData": map[string]any{"mimeType": mediaType, "data": data}}
}

// normalizeContents is normalizeGeminiContents (formats/gemini.js:481-516):
// empty parts are removed, adjacent turns of one role merge, and the history is
// closed so it starts and ends on a user turn. Two turns are written by the
// gateway, not the client, and PARITY 6.7 G3 says so: "..." when the history
// begins with a model turn, and "Continue." (or a functionResponse of
// "Continue." for each unanswered call) when it ends on one. They are a shape
// requirement of the wire.
func normalizeContents(contents []turn, model string) []any {
	functionResponsesLast := isGeminiFour(model)
	var merged []turn
	for _, content := range contents {
		var parts []any
		for _, raw := range content.parts {
			if part := concerns.Object(raw); len(part) > 0 {
				parts = append(parts, sanitizeFunctionResponse(part))
			}
		}
		if len(parts) == 0 {
			continue
		}
		if last := len(merged) - 1; last >= 0 && merged[last].role == content.role {
			merged[last].parts = append(merged[last].parts, parts...)
		} else {
			merged = append(merged, turn{role: content.role, parts: parts})
		}
	}
	if functionResponsesLast {
		for i := range merged {
			if merged[i].role == "user" {
				merged[i].parts = functionResponsesToEnd(merged[i].parts)
			}
		}
	}
	if len(merged) > 0 && merged[0].role != "user" {
		merged = append([]turn{{role: "user", parts: []any{map[string]any{"text": "..."}}}}, merged...)
	}
	if len(merged) > 0 && merged[len(merged)-1].role == "model" {
		var replies []any
		for _, raw := range merged[len(merged)-1].parts {
			if call := concerns.Object(concerns.Object(raw)["functionCall"]); call != nil {
				name := concerns.Text(call["name"])
				if name == "" {
					name = "tool"
				}
				reply := map[string]any{"name": name, "response": map[string]any{"result": "Continue."}}
				if id := concerns.Text(call["id"]); id != "" {
					reply["id"] = id
				}
				replies = append(replies, map[string]any{"functionResponse": reply})
			}
		}
		if len(replies) == 0 {
			replies = []any{map[string]any{"text": "Continue."}}
		}
		merged = append(merged, turn{role: "user", parts: replies})
	}
	out := make([]any, len(merged))
	for i, content := range merged {
		out[i] = map[string]any{"role": content.role, "parts": content.parts}
	}
	return out
}

// functionResponsesToEnd: Gemini 4 rejects a merged user turn whose last part is
// not a functionResponse (formats/gemini.js:474-479, #4802).
func functionResponsesToEnd(parts []any) []any {
	var others, responses []any
	for _, raw := range parts {
		if concerns.Object(raw)["functionResponse"] != nil {
			responses = append(responses, raw)
		} else {
			others = append(others, raw)
		}
	}
	return append(others, responses...)
}

// sanitizeFunctionResponse renames a "$ref" key inside a functionResponse, which
// Gemini reads as a pointer to the response's parts and rejects
// (formats/gemini.js:449-472).
func sanitizeFunctionResponse(part map[string]any) map[string]any {
	response := concerns.Object(part["functionResponse"])
	if response == nil {
		return part
	}
	payload, ok := response["response"].(map[string]any)
	if !ok {
		return part
	}
	cleaned := map[string]any{}
	for key, value := range response {
		cleaned[key] = value
	}
	cleaned["response"] = renameReserved(payload)
	out := map[string]any{}
	for key, value := range part {
		out[key] = value
	}
	out["functionResponse"] = cleaned
	return out
}

func renameReserved(value any) any {
	switch v := value.(type) {
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = renameReserved(item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, item := range v {
			if key == "$ref" {
				key = "_ref"
			}
			out[key] = renameReserved(item)
		}
		return out
	}
	return value
}

// stripIDs removes the ids Vertex rejects (openai-to-vertex.js:12-35). The
// pin also replaces every thoughtSignature with a captured Vertex blob; this
// keeps the signature the response carried or the placeholder (PARITY 6.7 G2).
func stripIDs(contents []any) {
	for _, raw := range contents {
		for _, rawPart := range concerns.Items(concerns.Object(raw)["parts"]) {
			part := concerns.Object(rawPart)
			for _, key := range []string{"functionCall", "functionResponse"} {
				if inner := concerns.Object(part[key]); inner != nil {
					delete(inner, "id")
				}
			}
		}
	}
}
