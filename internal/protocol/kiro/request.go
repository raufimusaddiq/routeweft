package kiro

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// Protocol is the Kiro (AWS CodeWhisperer GenerateAssistantResponse) wire.
const Protocol = "kiro"

const (
	// toolResultsPlaceholder is KIRO_TOOL_RESULTS_PLACEHOLDER
	// (kiroConversation.js:15): a user turn that only carries tool results needs
	// some text, and it must not read as an instruction.
	toolResultsPlaceholder = "Tool results provided."
	// emptyAssistantContent is the pin's content for an assistant turn with no
	// text (openai-to-kiro.js:81); Kiro rejects an empty one.
	emptyAssistantContent = "..."
)

// RequestOptions are what a Chat to Kiro conversion needs besides the body.
type RequestOptions struct {
	// Model is the routed model id, with any -thinking, -agentic or (level)
	// suffix the catalog and the client use.
	Model string
	// Repair is true when the body is the client's own Chat body, because the pin
	// repairs tool ids only on the client's body (translator/index.js:82).
	Repair bool
	// NewConversationID names the upstream conversation. Every request gets its
	// own: nothing is kept between requests, so no client's conversation can be
	// continued by another (the pin's replay cache is not built). Nil is a random UUID.
	NewConversationID func() string
}

// RequestInfo is what the request leg learned that the response leg and the
// caller need.
type RequestInfo struct {
	// ThinkingTags is true when the request injected thinking mode into the
	// prompt, which is when the model may answer with inline <thinking> tags.
	ThinkingTags bool
	// Normalized counts the schema rewrites PARITY 6.8 lists.
	Normalized Normalized
	// UpstreamModel is the model Kiro is asked for.
	UpstreamModel string
}

func unsupported(format string, args ...any) error {
	return fmt.Errorf("unsupported: "+format, args...)
}

// advisoryFields are accepted and not carried: the answer is the same without
// them, and Kiro's request has nowhere to put them (PARITY 6.8).
var advisoryFields = map[string]bool{
	"model": true, "stream": true, "stream_options": true, "user": true, "store": true, "metadata": true,
	"service_tier": true, "prompt_cache_key": true, "prompt_cache_retention": true, "safety_identifier": true,
}

// carriedFields are read by the leg.
var carriedFields = map[string]bool{
	"messages": true, "tools": true, "tool_choice": true, "parallel_tool_calls": true,
	"temperature": true, "top_p": true, "max_tokens": true, "max_completion_tokens": true,
	"reasoning_effort": true, "reasoning": true, "thinking": true, "output_config": true,
	"enable_thinking": true, "thinking_budget": true, "thinkingConfig": true,
}

// checkedFields are decided by UncarriableChat or the checks below.
var checkedFields = map[string]bool{
	"n": true, "logprobs": true, "modalities": true, "audio": true, "functions": true, "function_call": true,
	"response_format": true, "presence_penalty": true, "frequency_penalty": true,
}

// messageFields are the keys a message may carry. Echoed reasoning, refusals and
// cache markers are advisory.
var messageFields = map[string]bool{
	"role": true, "content": true, "tool_calls": true, "tool_call_id": true, "is_error": true, "status": true,
	"name": true, "refusal": true, "reasoning_content": true, "reasoning": true, "reasoning_details": true,
	"cache_control": true, "annotations": true,
}

func checkFields(body map[string]any) error {
	if reason := concerns.UncarriableChat(body, "Kiro"); reason != "" {
		return unsupported("%s", reason)
	}
	keys := make([]string, 0, len(body))
	for key := range body {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		value := body[key]
		if value == nil || advisoryFields[key] || carriedFields[key] {
			continue
		}
		switch key {
		case "n":
			numeric, isNumber := value.(json.Number)
			n, err := numeric.Int64()
			if !isNumber || err != nil || n != 1 {
				return unsupported("n=%s: Kiro returns one completion", concerns.JSONText(value))
			}
		case "response_format":
			if format := concerns.Object(value); format == nil || format["type"] != "text" || len(format) != 1 {
				return unsupported("response_format: Kiro has no structured-output mode")
			}
		case "presence_penalty", "frequency_penalty":
			// Zero is the identity; any other value is a sampling request Kiro cannot honour.
			if !concerns.IsNumber(value) {
				return unsupported("%s must be a number", key)
			}
			f, ok := number(value)
			mantissa, _, _ := strings.Cut(strings.ToLower(fmt.Sprint(value)), "e")
			if !ok || f != 0 || strings.ContainsAny(mantissa, "123456789") {
				return unsupported("%s: Kiro has no equivalent", key)
			}
		case "logprobs":
			if value != false {
				return unsupported("logprobs must be false: Kiro reports none")
			}
		case "modalities":
			list, ok := value.([]any)
			if !ok || len(list) != 1 || list[0] != "text" {
				return unsupported("modalities: Kiro returns text only")
			}
		case "audio", "functions", "function_call":
			return unsupported("request field %q has no Kiro equivalent", key)
		default:
			if checkedFields[key] {
				continue
			}
			return unsupported("request field %q: Kiro has no equivalent", key)
		}
	}
	if value := body["parallel_tool_calls"]; value != nil && value != true {
		return unsupported("parallel_tool_calls must be true: Kiro cannot forbid parallel calls")
	}
	if choice := body["tool_choice"]; choice != nil && choice != "auto" {
		return unsupported("tool_choice %s: Kiro has no tool_choice", concerns.JSONText(choice))
	}
	return validateThinking(body)
}

func requestKeys(object map[string]any, path string, keys ...string) error {
	for key, value := range object {
		if value == nil {
			continue
		}
		known := false
		for _, allowed := range keys {
			known = known || key == allowed
		}
		if !known {
			return unsupported("%s.%s has no Kiro equivalent", path, key)
		}
	}
	return nil
}

// optionalNumber reads a sampling value, keeping an explicit zero distinct from
// an absent field.
func optionalNumber(body map[string]any, key string) (value json.Number, present bool, err error) {
	raw, set := body[key]
	if !set || raw == nil {
		return "", false, nil
	}
	numeric, isNumber := raw.(json.Number)
	if _, valid := number(raw); !isNumber || !valid {
		return "", false, unsupported("%s must be a finite number", key)
	}
	return numeric, true, nil
}

func maxTokens(body map[string]any) (value int64, present bool, err error) {
	for _, key := range []string{"max_tokens", "max_completion_tokens"} {
		raw := body[key]
		if raw == nil {
			continue
		}
		number, isNumber := raw.(json.Number)
		if !isNumber {
			return 0, false, unsupported("%s must be an integer", key)
		}
		parsed, parseErr := number.Int64()
		if parseErr != nil || parsed <= 0 {
			return 0, false, unsupported("%s must be a positive integer", key)
		}
		return parsed, true, nil
	}
	return 0, false, nil
}

type result struct {
	id, status, text string
}

type use struct {
	id, name string
	input    map[string]any
}

type turn struct {
	assistant bool
	pieces    []string
	images    []any
	results   []result
	uses      []use
}

func validateImage(url any) (map[string]any, error) {
	media, data, ok := concerns.ParseDataURI(url)
	if !ok {
		return nil, unsupported("image_url must be a base64 data URI: Kiro takes image bytes, not a link")
	}
	kind, format, found := strings.Cut(strings.ToLower(media), "/")
	if !found || kind != "image" || format == "" {
		return nil, unsupported("image media type %q is not an image", media)
	}
	if _, err := io.Copy(io.Discard, base64.NewDecoder(base64.StdEncoding, strings.NewReader(data))); err != nil {
		return nil, unsupported("image data is not valid base64")
	}
	return map[string]any{"format": format, "source": map[string]any{"bytes": data}}, nil
}

// contentParts reads a message's content. Text parts join with a newline
// (openai-to-kiro.js:138). A part the wire cannot carry is refused where the pin
// ignores it (:129-136).
func contentParts(role string, content any, index int) (string, []any, error) {
	switch v := content.(type) {
	case nil:
		return "", nil, nil
	case string:
		return v, nil, nil
	case []any:
		var texts []string
		var images []any
		for j, raw := range v {
			part, isObject := raw.(map[string]any)
			if !isObject {
				return "", nil, unsupported("messages[%d].content[%d] must be an object", index, j)
			}
			if err := requestKeys(part, fmt.Sprintf("messages[%d].content[%d]", index, j), "type", "text", "image_url", "refusal", "cache_control"); err != nil {
				return "", nil, err
			}
			switch part["type"] {
			case "text":
				if part["image_url"] != nil || part["refusal"] != nil {
					return "", nil, unsupported("messages[%d].content[%d]: text part carries other content", index, j)
				}
				text, isText := part["text"].(string)
				if !isText {
					return "", nil, unsupported("messages[%d].content[%d].text must be a string", index, j)
				}
				texts = append(texts, text)
			case "refusal":
				if role != "assistant" {
					return "", nil, unsupported("messages[%d].content[%d]: refusal part outside an assistant turn", index, j)
				}
			case "image_url":
				if role != "user" {
					return "", nil, unsupported("messages[%d].content[%d]: images are only carried in user turns", index, j)
				}
				object, isObject := part["image_url"].(map[string]any)
				if !isObject {
					return "", nil, unsupported("messages[%d].content[%d].image_url must be an object", index, j)
				}
				if part["text"] != nil || part["refusal"] != nil {
					return "", nil, unsupported("messages[%d].content[%d]: image part carries other content", index, j)
				}
				if err := requestKeys(object, "image_url", "url", "detail"); err != nil {
					return "", nil, err
				}
				image, err := validateImage(object["url"])
				if err != nil {
					return "", nil, fmt.Errorf("messages[%d].content[%d]: %w", index, j, err)
				}
				images = append(images, image)
			default:
				return "", nil, unsupported("messages[%d].content[%d]: part type %s has no Kiro equivalent", index, j, concerns.JSONText(part["type"]))
			}
		}
		return strings.Join(texts, "\n"), images, nil
	}
	return "", nil, unsupported("messages[%d].content must be a string or a list of parts", index)
}

// parseMessages groups messages into alternating turns (openai-to-kiro.js:43-291,
// kiroConversation.js:158-202 minus the invented turns).
func parseMessages(messages []any) ([]*turn, error) {
	var turns []*turn
	var current *turn
	start := func(assistant bool) *turn {
		if current == nil || current.assistant != assistant {
			current = &turn{assistant: assistant}
			turns = append(turns, current)
		}
		return current
	}
	for i, raw := range messages {
		message, isObject := raw.(map[string]any)
		if !isObject {
			return nil, unsupported("messages[%d] must be an object", i)
		}
		for key, value := range message {
			if !messageFields[key] && value != nil {
				return nil, unsupported("messages[%d]: field %q has no Kiro equivalent", i, key)
			}
		}
		role, _ := message["role"].(string)
		if message["name"] != nil && role != "tool" {
			return nil, unsupported("messages[%d]: a participant name has no Kiro equivalent", i)
		}
		if role != "assistant" && message["tool_calls"] != nil {
			return nil, unsupported("messages[%d]: tool_calls require an assistant role", i)
		}
		if role != "tool" && (message["tool_call_id"] != nil || message["is_error"] != nil || message["status"] != nil) {
			return nil, unsupported("messages[%d]: tool result fields require a tool role", i)
		}
		switch role {
		case "system", "developer":
			text, _, err := contentParts("system", message["content"], i)
			if err != nil {
				return nil, err
			}
			t := start(false)
			if text != "" {
				t.pieces = append(t.pieces, "<instructions>\n"+text+"\n</instructions>")
			}
		case "user":
			text, images, err := contentParts("user", message["content"], i)
			if err != nil {
				return nil, err
			}
			t := start(false)
			if text != "" {
				t.pieces = append(t.pieces, text)
			}
			t.images = append(t.images, images...)
		case "tool":
			id, _ := message["tool_call_id"].(string)
			if id == "" {
				return nil, unsupported("messages[%d]: a tool message needs tool_call_id", i)
			}
			text, images, err := contentParts("tool", message["content"], i)
			if err != nil {
				return nil, err
			}
			if len(images) > 0 {
				return nil, unsupported("messages[%d]: a tool result cannot carry images", i)
			}
			if value := message["is_error"]; value != nil && value != true && value != false {
				return nil, unsupported("messages[%d].is_error must be a boolean", i)
			}
			if value := message["status"]; value != nil && value != "success" && value != "error" {
				return nil, unsupported("messages[%d].status must be success or error", i)
			}
			status := "success"
			if message["is_error"] == true || message["status"] == "error" {
				status = "error"
			}
			t := start(false)
			t.results = append(t.results, result{id: id, status: status, text: text})
		case "assistant":
			text, _, err := contentParts("assistant", message["content"], i)
			if err != nil {
				return nil, err
			}
			t := start(true)
			if text = strings.TrimSpace(text); text != "" {
				t.pieces = append(t.pieces, text)
			}
			calls := message["tool_calls"]
			if calls != nil {
				list, isList := calls.([]any)
				if !isList {
					return nil, unsupported("messages[%d].tool_calls must be a list", i)
				}
				for j, rawCall := range list {
					call, isObject := rawCall.(map[string]any)
					if !isObject {
						return nil, unsupported("messages[%d].tool_calls[%d] must be an object", i, j)
					}
					if err := requestKeys(call, "tool_calls", "id", "type", "function"); err != nil {
						return nil, err
					}
					if kind := call["type"]; kind != nil && kind != "function" {
						return nil, unsupported("messages[%d].tool_calls[%d]: type %s is not a function", i, j, concerns.JSONText(kind))
					}
					function, isObject := call["function"].(map[string]any)
					if !isObject {
						return nil, unsupported("messages[%d].tool_calls[%d].function must be an object", i, j)
					}
					if err := requestKeys(function, "tool_calls.function", "name", "arguments"); err != nil {
						return nil, err
					}
					name, _ := function["name"].(string)
					id, _ := call["id"].(string)
					if name == "" || id == "" {
						return nil, unsupported("messages[%d].tool_calls[%d] needs an id and a function name", i, j)
					}
					input, err := strictArguments(function["arguments"])
					if err != nil {
						return nil, fmt.Errorf("messages[%d].tool_calls[%d]: %w", i, j, err)
					}
					t.uses = append(t.uses, use{id: id, name: name, input: input})
				}
			}
		default:
			return nil, unsupported("messages[%d]: role %s has no Kiro equivalent", i, concerns.JSONText(message["role"]))
		}
	}
	return turns, nil
}

func (t *turn) userContent() string {
	return strings.TrimSpace(strings.Join(t.pieces, "\n\n"))
}

// ChatRequestToKiro is the request leg openai-chat>kiro
// (translator/request/openai-to-kiro.js:308-447). It returns the body for
// GenerateAssistantResponse, the tool names it rewrote so the response can put
// the client's own back, and what the response leg needs to know. It mutates
// body (tool ids, when Repair is set).
//
// What it does with every Chat field is the table in docs/PARITY.md 6.8: carried,
// advisory (not carried, and the answer does not change), or refused with an
// error. Nothing is dropped silently, and no turn, tool result, description or
// schema constraint is invented or rewritten the way the pin's canonicalizer
// does (kiroConversation.js:129-134, :261-338, :373-391).
func ChatRequestToKiro(body map[string]any, options RequestOptions) ([]byte, *concerns.ToolNames, RequestInfo, error) {
	var info RequestInfo
	if err := checkFields(body); err != nil {
		return nil, nil, info, err
	}
	messages, isList := body["messages"].([]any)
	if !isList || len(messages) == 0 {
		return nil, nil, info, unsupported("messages must be a non-empty list")
	}
	intent := resolveModel(options.Model)
	if intent.override == nil && suffixPattern.MatchString(options.Model) {
		return nil, nil, info, unsupported("model %q has an unrecognized thinking override", options.Model)
	}
	if intent.override != nil && intent.override.mode == "budget" && intent.override.budget <= 0 {
		return nil, nil, info, unsupported("model thinking budget must be positive")
	}
	if strings.TrimSpace(intent.upstream) == "" {
		return nil, nil, info, unsupported("model %q names no Kiro model", options.Model)
	}
	info.UpstreamModel = intent.upstream
	temperature, hasTemperature, err := optionalNumber(body, "temperature")
	if err != nil {
		return nil, nil, info, err
	}
	topP, hasTopP, err := optionalNumber(body, "top_p")
	if err != nil {
		return nil, nil, info, err
	}
	limit, hasLimit, err := maxTokens(body)
	if err != nil {
		return nil, nil, info, err
	}
	if options.Repair {
		// Validate supplied ids before shared repair can replace them.
		for i, raw := range messages {
			message := concerns.Object(raw)
			ids := []any{message["tool_call_id"]}
			for _, rawCall := range concerns.Items(message["tool_calls"]) {
				call := concerns.Object(rawCall)
				if kind := call["type"]; kind != nil && kind != "function" {
					return nil, nil, info, unsupported("messages[%d]: tool call type must be function", i)
				}
				ids = append(ids, call["id"])
			}
			for _, rawID := range ids {
				if rawID == nil {
					continue
				}
				id, ok := rawID.(string)
				if !ok {
					return nil, nil, info, unsupported("messages[%d]: tool id must be a string", i)
				}
				if _, err := sanitizeID(id); err != nil {
					return nil, nil, info, err
				}
			}
		}
		concerns.EnsureToolCallIDs(body)
	}
	var tools []any
	if raw := body["tools"]; raw != nil {
		if tools, isList = raw.([]any); !isList {
			return nil, nil, info, unsupported("tools must be a list")
		}
	}
	turns, err := parseMessages(messages)
	if err != nil {
		return nil, nil, info, err
	}
	specs, names, err := toolSpecs(tools, nil, &info.Normalized)
	if err != nil {
		return nil, nil, info, err
	}
	declared := map[string]string{}
	for _, spec := range specs {
		declared[spec.original] = spec.name
	}
	if err := checkConversation(turns, declared); err != nil {
		return nil, nil, info, err
	}

	thinkingBody := applyOverride(body, intent.override)
	var prefixes []string
	if budget, on := resolveBudget(thinkingBody, intent.model); on && !usesNativeGPTEffort(thinkingBody, intent.upstream) {
		prefixes = append(prefixes, thinkingPrefix(budget))
		info.ThinkingTags = true
	}
	if intent.agentic {
		prefixes = append(prefixes, agenticSystemPrompt)
	}

	var history []any
	var current map[string]any
	for i, t := range turns {
		if t.assistant {
			content := strings.Join(t.pieces, "\n\n")
			if content == "" {
				content = emptyAssistantContent
			}
			message := map[string]any{"content": content}
			if len(t.uses) > 0 {
				uses := make([]any, len(t.uses))
				for k, u := range t.uses {
					id, _ := sanitizeID(u.id)
					uses[k] = map[string]any{"toolUseId": id, "name": declared[u.name], "input": u.input}
				}
				message["toolUses"] = uses
			}
			history = append(history, map[string]any{"assistantResponseMessage": message})
			continue
		}
		content := t.userContent()
		if content == "" {
			content = toolResultsPlaceholder
		}
		if i == 0 && len(prefixes) > 0 {
			content = strings.Join(append(append([]string{}, prefixes...), content), "\n\n")
		}
		message := map[string]any{"content": content, "modelId": intent.upstream}
		context := map[string]any{}
		if len(t.images) > 0 {
			message["images"] = t.images
		}
		if len(t.results) > 0 {
			results := make([]any, len(t.results))
			for k, r := range t.results {
				id, _ := sanitizeID(r.id)
				results[k] = map[string]any{"toolUseId": id, "status": r.status, "content": []any{map[string]any{"text": r.text}}}
			}
			context["toolResults"] = results
		}
		if i == len(turns)-1 {
			if len(specs) > 0 {
				list := make([]any, len(specs))
				for k, spec := range specs {
					list[k] = spec.body
				}
				context["tools"] = list
			}
			message["origin"] = "AI_EDITOR"
		}
		if len(context) > 0 {
			message["userInputMessageContext"] = context
		}
		if i == len(turns)-1 {
			current = map[string]any{"userInputMessage": message}
		} else {
			history = append(history, map[string]any{"userInputMessage": message})
		}
	}
	if history == nil {
		history = []any{}
	}
	newID := options.NewConversationID
	if newID == nil {
		newID = uuid.NewString
	}
	state := map[string]any{"chatTriggerType": "MANUAL", "conversationId": newID(), "currentMessage": current, "history": history}
	payload := map[string]any{"conversationState": state}
	if fields := additionalFields(thinkingBody, intent.upstream); fields != nil {
		payload["additionalModelRequestFields"] = fields
	}
	if hasTemperature || hasTopP || hasLimit {
		config := map[string]any{}
		if hasLimit {
			config["maxTokens"] = limit
		}
		if hasTemperature {
			config["temperature"] = temperature
		}
		if hasTopP {
			config["topP"] = topP
		}
		payload["inferenceConfig"] = config
	}
	encoded, err := json.Marshal(payload)
	return encoded, names, info, err
}

// checkConversation holds the rules the pin repairs by rewriting the
// conversation (kiroConversation.js:340-371): turns alternate and begin and end
// on the user, and each assistant turn's tool calls are answered one to one by
// the tool results of the next user turn, with ids Kiro accepts and none reused.
// A conversation that breaks one is refused with the turn named.
func checkConversation(turns []*turn, declared map[string]string) error {
	if len(turns) == 0 {
		return unsupported("messages hold no conversation")
	}
	if turns[0].assistant {
		return unsupported("the conversation starts with an assistant turn: Kiro needs it to start with the user")
	}
	if turns[len(turns)-1].assistant {
		return unsupported("the conversation ends with an assistant turn: Kiro cannot continue one")
	}
	used := map[string]string{}
	for i, t := range turns {
		if !t.assistant {
			if t.userContent() == "" && len(t.results) == 0 {
				return unsupported("turn %d is a user turn with no text", i)
			}
			if i == 0 && len(t.results) > 0 {
				return unsupported("turn 0 answers a tool call nothing made")
			}
			if i > 0 && len(turns[i-1].uses) == 0 && len(t.results) > 0 {
				return unsupported("turn %d holds tool results for tool calls nothing made", i)
			}
			continue
		}
		pending := map[string]bool{}
		for _, u := range t.uses {
			if _, known := declared[u.name]; !known {
				return unsupported("turn %d calls tool %q, which is not among the tools", i, u.name)
			}
			id, err := sanitizeID(u.id)
			if err != nil {
				return fmt.Errorf("turn %d: %w", i, err)
			}
			if other, taken := used[id]; taken {
				return unsupported("turn %d: tool call ids %q and %q are the same id (%q)", i, u.id, other, id)
			}
			used[id] = u.id
			pending[id] = true
		}
		answered := map[string]bool{}
		for _, r := range turns[i+1].results {
			id, err := sanitizeID(r.id)
			if err != nil {
				return fmt.Errorf("turn %d: %w", i+1, err)
			}
			if !pending[id] || answered[id] {
				return unsupported("turn %d answers tool call %q, which turn %d did not make or another result already answered", i+1, r.id, i)
			}
			answered[id] = true
		}
		if len(answered) != len(pending) {
			return unsupported("turn %d makes %d tool calls and turn %d answers %d", i, len(pending), i+1, len(answered))
		}
	}
	return nil
}
