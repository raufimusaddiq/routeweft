package gemini

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// VertexProtocol is Vertex AI's Gemini wire: the same GenerateContent body as
// Gemini with the ids Vertex rejects removed (openai-to-vertex.js:12-35).
const VertexProtocol = "vertex"

// RequestOptions are what a Chat to Gemini conversion needs besides the body.
type RequestOptions struct {
	// Model is the upstream model, named in the body and used for the Gemini 4
	// sampling rule.
	Model string
	// Vertex removes the ids Vertex rejects from function calls and responses.
	Vertex bool
	// Repair is true when the body is the client's own Chat body, because the pin
	// repairs tool ids only on the client's body (translator/index.js:82).
	Repair bool
	// Signatures and Scope replay the thought signatures this connection's
	// earlier responses carried. An empty Scope, or a nil cache, replays none.
	Signatures *Signatures
	Scope      string
	// OmitPlaceholderSignature leaves a call that holds no real signature
	// unsigned instead of sending PlaceholderSignature. The Claude models behind
	// Antigravity are signed by a different backend than Gemini's, and a signature
	// of the other family is rejected (PARITY 6.9 A6).
	OmitPlaceholderSignature bool
}

var (
	validGeminiName    = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_.:\-]{0,63}$`)
	geminiNameStrip    = regexp.MustCompile(`[^a-zA-Z0-9_.:\-]`)
	geminiNameFirst    = regexp.MustCompile(`^[a-zA-Z_]`)
	geminiFourModel    = regexp.MustCompile(`(?i)(?:^|/)gemini-4(?:$|[-.])`)
	geminiToolNameRule = concerns.ToolNameRule{
		Valid: validGeminiName.MatchString,
		// sanitizeGeminiFunctionName (openai-to-gemini.js:28-38).
		Sanitize: func(original string) string {
			if original == "" {
				return "_unknown"
			}
			name := geminiNameStrip.ReplaceAllString(original, "_")
			if !geminiNameFirst.MatchString(name) {
				name = "_" + name
			}
			if len(name) > 64 {
				name = name[:64]
			}
			return name
		},
		Max:    64,
		Strict: true,
	}
)

// defaultSafetySettings are sent on every request (openai-to-gemini.js:82,
// formats/gemini.js:47-53): the gateway turns the filters off, not the client.
// Whether Vertex accepts every category is not verified here (PARITY 6.7 G8).
var defaultSafetySettings = []any{
	map[string]any{"category": "HARM_CATEGORY_HATE_SPEECH", "threshold": "OFF"},
	map[string]any{"category": "HARM_CATEGORY_DANGEROUS_CONTENT", "threshold": "OFF"},
	map[string]any{"category": "HARM_CATEGORY_SEXUALLY_EXPLICIT", "threshold": "OFF"},
	map[string]any{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "OFF"},
	map[string]any{"category": "HARM_CATEGORY_CIVIC_INTEGRITY", "threshold": "OFF"},
}

func unsupported(format string, args ...any) error {
	return fmt.Errorf("unsupported: "+format, args...)
}

// ChatRequestToGemini is the request leg openai-chat>gemini
// (translator/request/openai-to-gemini.js:77-316) and, with Vertex set,
// openai-chat>vertex (openai-to-vertex.js:37-40). It returns the body for
// generateContent and the tool names it rewrote, so a response can put the
// client's own names back. It mutates body.
//
// What it does with every Chat field is the table in docs/PARITY.md 6.7. A field
// is carried, not carried for a stated reason (the request is still served and
// nothing it asks for is lost), or refused with an error, so the dispatcher
// tries the next candidate. Nothing is dropped silently.
func ChatRequestToGemini(body map[string]any, options RequestOptions) ([]byte, *concerns.ToolNames, error) {
	if reason := concerns.UncarriableChat(body, "Gemini"); reason != "" {
		return nil, nil, unsupported("%s", reason)
	}
	for _, field := range []string{"logit_bias", "top_logprobs", "web_search_options", "prediction"} {
		if concerns.Present(body[field]) {
			return nil, nil, unsupported("%s: Gemini has no equivalent", field)
		}
	}
	if body["parallel_tool_calls"] == false {
		return nil, nil, unsupported("parallel_tool_calls:false: Gemini cannot forbid parallel calls")
	}
	messages, ok := body["messages"].([]any)
	if !ok || len(messages) == 0 {
		return nil, nil, unsupported("messages must be a non-empty list")
	}
	if options.Repair {
		concerns.EnsureToolCallIDs(body)
	}
	names, err := concerns.NewToolNamesFor(geminiToolNameRule, concerns.ChatToolNames(body))
	if err != nil {
		return nil, nil, unsupported("%v", err)
	}
	result := map[string]any{"model": options.Model, "safetySettings": defaultSafetySettings}
	config, err := generationConfig(body, options.Model)
	if err != nil {
		return nil, nil, err
	}
	result["generationConfig"] = config
	tools, declared, err := functionDeclarations(body["tools"], names)
	if err != nil {
		return nil, nil, err
	}
	if len(tools) > 0 {
		result["tools"] = []any{map[string]any{"functionDeclarations": tools}}
	}
	// The choice is always checked; with no tools to choose among it is not
	// forwarded (PARITY V19).
	toolConfig, err := toolConfigFor(body["tool_choice"], declared, len(tools) > 0)
	if err != nil {
		return nil, nil, err
	}
	if toolConfig != nil && len(tools) > 0 {
		result["toolConfig"] = toolConfig
	}
	conv := &conversation{names: names, options: options, results: map[string][]toolResult{}}
	if err := conv.convert(messages); err != nil {
		return nil, nil, err
	}
	if len(conv.system) > 0 {
		result["systemInstruction"] = map[string]any{"role": "user", "parts": []any{map[string]any{"text": strings.Join(conv.system, "\n\n")}}}
	}
	result["contents"] = normalizeContents(conv.contents, options.Model)
	if options.Vertex {
		stripIDs(result["contents"].([]any))
	}
	encoded, err := json.Marshal(result)
	return encoded, names, err
}

func isGeminiFour(model string) bool { return geminiFourModel.MatchString(model) }

// number reads a numeric Chat field: absent is fine, a non-number is refused.
func number(body map[string]any, name string) (any, bool, error) {
	value, present := body[name]
	if !present || value == nil {
		return nil, false, nil
	}
	if !concerns.IsNumber(value) {
		return nil, false, unsupported("%s must be a number", name)
	}
	return value, true, nil
}

// generationConfig carries the sampling and output controls (:85-98).
func generationConfig(body map[string]any, model string) (map[string]any, error) {
	config := map[string]any{}
	// top_k is not read: OpenAI Chat defines no such field, and the family has one
	// rule for it (PARITY 6.9 F1). The pin forwarded it (:90-95).
	sampling := map[string]string{"temperature": "temperature", "top_p": "topP"}
	for field, target := range sampling {
		value, present, err := number(body, field)
		if err != nil {
			return nil, err
		}
		// Gemini 4 removed the sampling controls and rejects them (:85-95). The
		// request is served without them and PARITY 6.7 G6 says so; that the model
		// rejects them is the pin's comment, not something verified here.
		if present && !isGeminiFour(model) {
			config[target] = value
		}
	}
	limit, present, err := number(body, "max_tokens")
	if err != nil {
		return nil, err
	}
	if !present {
		// The pin reads only max_tokens (:96) and leaves a caller that used the
		// current name unbounded (PARITY V22).
		if limit, present, err = number(body, "max_completion_tokens"); err != nil {
			return nil, err
		}
	}
	if present {
		if concerns.Int(limit) <= 0 {
			return nil, unsupported("max_tokens must be positive")
		}
		config["maxOutputTokens"] = limit
	}
	for field, target := range map[string]string{"seed": "seed", "presence_penalty": "presencePenalty", "frequency_penalty": "frequencyPenalty"} {
		value, present, err := number(body, field)
		if err != nil {
			return nil, err
		}
		if present {
			config[target] = value
		}
	}
	stop, err := stopSequences(body["stop"])
	if err != nil {
		return nil, err
	}
	if len(stop) > 0 {
		config["stopSequences"] = stop
	}
	if err := responseFormat(body["response_format"], config); err != nil {
		return nil, err
	}
	return config, nil
}

// stopSequences accepts a Chat stop value, a string or a list of strings.
// Gemini takes at most five.
func stopSequences(stop any) ([]any, error) {
	var list []any
	switch value := stop.(type) {
	case nil:
		return nil, nil
	case string:
		if value == "" {
			return nil, nil
		}
		list = []any{value}
	case []any:
		for _, item := range value {
			if text, ok := item.(string); !ok || text == "" {
				return nil, unsupported("stop must be a string or a list of non-empty strings")
			}
		}
		list = value
	default:
		return nil, unsupported("stop must be a string or a list of strings")
	}
	if len(list) > 5 {
		return nil, unsupported("stop has %d sequences; Gemini takes at most 5", len(list))
	}
	return list, nil
}

// responseFormat is Chat's response_format as Gemini's responseMimeType and
// schema. Text asks for nothing. Anything else is refused: JSON the client asked
// for must not arrive as prose.
func responseFormat(value any, config map[string]any) error {
	if value == nil {
		return nil
	}
	format := concerns.Object(value)
	if format == nil {
		return unsupported("response_format must be an object")
	}
	switch kind := concerns.Text(format["type"]); kind {
	case "", "text":
		return nil
	case "json_object":
		config["responseMimeType"] = "application/json"
	case "json_schema":
		schema := concerns.Object(concerns.Object(format["json_schema"])["schema"])
		if schema == nil {
			return unsupported("response_format json_schema without a schema")
		}
		cleaned, err := CleanSchema(schema)
		if err != nil {
			return unsupported("response_format schema: %v", err)
		}
		if cleaned == nil {
			return unsupported("response_format schema declares no properties")
		}
		config["responseMimeType"] = "application/json"
		config["responseSchema"] = cleaned
	default:
		return unsupported("response_format type %q", kind)
	}
	return nil
}

// functionDeclarations converts the tools (:279-307). declared is the set of
// upstream names, for tool_choice.
func functionDeclarations(value any, names *concerns.ToolNames) ([]any, map[string]string, error) {
	declared := map[string]string{}
	var out []any
	if value == nil {
		return nil, declared, nil
	}
	list, ok := value.([]any)
	if !ok {
		return nil, nil, unsupported("tools must be a list")
	}
	for _, raw := range list {
		tool := concerns.Object(raw)
		var name, description string
		var parameters any
		switch {
		case tool == nil:
			return nil, nil, unsupported("a tool is not an object")
		case concerns.Text(tool["name"]) != "" && tool["input_schema"] != nil:
			name, description, parameters = concerns.Text(tool["name"]), concerns.Text(tool["description"]), tool["input_schema"]
		case concerns.Text(tool["type"]) == "function" && concerns.Object(tool["function"]) != nil:
			function := concerns.Object(tool["function"])
			name, description, parameters = concerns.Text(function["name"]), concerns.Text(function["description"]), function["parameters"]
		default:
			// The pin skips a tool it does not know (:293 has no else), so a
			// built-in tool is silently not offered.
			return nil, nil, unsupported("tool of type %q: Gemini can only be offered function tools here", concerns.Text(tool["type"]))
		}
		if name == "" {
			return nil, nil, unsupported("a function tool has no name")
		}
		safe, err := names.SafeChecked(name)
		if err != nil {
			return nil, nil, unsupported("%v", err)
		}
		if _, dup := declared[name]; dup {
			return nil, nil, unsupported("tool %q is declared twice", name)
		}
		declared[name] = safe
		declaration := map[string]any{"name": safe, "description": description}
		if parameters != nil {
			cleaned, err := CleanSchema(parameters)
			if err != nil {
				return nil, nil, unsupported("tool %q: %v", name, err)
			}
			if cleaned != nil {
				declaration["parameters"] = cleaned
			}
		}
		out = append(out, declaration)
	}
	return out, declared, nil
}

// toolConfigFor is tool_choice as functionCallingConfig. The pin sends none for
// Gemini (:279-307), so "none" and "required" are silently "auto" (the V19
// class). A choice that cannot be honoured is refused.
func toolConfigFor(choice any, declared map[string]string, haveTools bool) (map[string]any, error) {
	mode := func(mode string, allowed ...string) map[string]any {
		config := map[string]any{"mode": mode}
		if len(allowed) > 0 {
			list := make([]any, len(allowed))
			for i, name := range allowed {
				list[i] = name
			}
			config["allowedFunctionNames"] = list
		}
		return map[string]any{"functionCallingConfig": config}
	}
	switch value := choice.(type) {
	case nil:
		return nil, nil
	case string:
		switch value {
		case "", "auto":
			return nil, nil
		case "none":
			return mode("NONE"), nil
		case "required":
			return mode("ANY"), nil
		}
		return nil, unsupported("tool_choice %q", value)
	case map[string]any:
		name := concerns.Text(concerns.Object(value["function"])["name"])
		if name == "" {
			name = concerns.Text(value["name"])
		}
		if kind := concerns.Text(value["type"]); (kind != "function" && kind != "tool") || name == "" {
			return nil, unsupported("tool_choice of type %q", kind)
		}
		safe, ok := declared[name]
		if !ok && haveTools {
			return nil, unsupported("tool_choice names %q, which is not among the tools", name)
		}
		return mode("ANY", safe), nil
	}
	return nil, unsupported("tool_choice %s", concerns.JSONText(choice))
}
