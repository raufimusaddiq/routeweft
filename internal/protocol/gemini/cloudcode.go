package gemini

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// GeminiCLIProtocol and AntigravityProtocol are the two Cloud Code Assist wires:
// the Gemini GenerateContent body inside an envelope that names a project
// (translator/request/openai-to-gemini.js:343-376). Neither is routable yet; the
// legs land behind ingress.unwired (PARITY 6.9).
const (
	GeminiCLIProtocol   = "gemini-cli"
	AntigravityProtocol = "antigravity"
)

// MaxAntigravityOutputTokens is the ceiling the pin's executor clamps to
// (executors/antigravity.js:22,277-279). The leg refuses above it: a silent
// clamp answers a different request than the one asked (PARITY 6.9 A5).
const MaxAntigravityOutputTokens = 64000

// CloudCodeOptions are the envelope fields a leg cannot know. The caller
// supplies them; the leg never invents one.
type CloudCodeOptions struct {
	// Project is the Code Assist project the request is billed to and routed
	// under. It is required: the pin fabricates a random one when the credential
	// has none (formats/gemini.js:137-143, openai-to-gemini.js:344), which sends a
	// request under an identifier nobody owns.
	Project string
	// SessionID is the client session. A numeric id is sent as it is, any other is
	// hashed to the numeric form (utils/sessionManager.js:18-25). Empty derives one
	// from "routeweft:" + RequestOptions.Scope; with no scope the field is omitted.
	SessionID string
	// RequestID labels the request. Empty gets "agent-<uuid>" (:127-129,350).
	RequestID string
}

var (
	projectPattern    = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,128}$`)
	identifierPattern = regexp.MustCompile(`^[\x21-\x7e]{1,256}$`)
	numericSession    = regexp.MustCompile(`^-?[0-9]{1,19}$`)
)

func (o CloudCodeOptions) check() error {
	if o.Project == "" {
		return unsupported("the Cloud Code project id is required: the gateway does not invent one")
	}
	if !projectPattern.MatchString(o.Project) {
		return unsupported("the Cloud Code project id is not a valid identifier")
	}
	if o.SessionID != "" && !identifierPattern.MatchString(o.SessionID) {
		return unsupported("the Cloud Code session id is not a valid identifier")
	}
	if o.RequestID != "" && !identifierPattern.MatchString(o.RequestID) {
		return unsupported("the Cloud Code request id is not a valid identifier")
	}
	return nil
}

// toNumericSession is toNumericSessionId (sessionManager.js:18-25): a numeric id
// passes through, anything else becomes "-<63-bit hash>".
func toNumericSession(id string) string {
	if numericSession.MatchString(id) {
		return id
	}
	sum := sha256.Sum256([]byte(id))
	return fmt.Sprintf("-%d", binary.BigEndian.Uint64(sum[:8])&0x7fffffffffffffff)
}

func newRequestID() (string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}
	return "agent-" + id.String(), nil
}

// FamilyScope is the signature scope for a model behind a wire that serves two
// model families (Antigravity: Gemini and Claude, each of which rejects the
// other's signature, thoughtSignatureStore.js:14-24). The CLIENT/CONNECTION
// scope stays the leading, length-prefixed part, so no family can reach another
// tenant's entry, and an empty scope stays empty: with no tenant dimension
// nothing is stored and nothing is found, whatever the family.
func FamilyScope(scope, model string) string {
	if scope == "" {
		return ""
	}
	return fmt.Sprintf("%d:%s%s", len(scope), scope, modelFamily(model))
}

func modelFamily(model string) string {
	m := strings.ToLower(strings.TrimSpace(model))
	switch {
	case strings.Contains(m, "claude"):
		return "claude"
	case strings.Contains(m, "gemini"):
		return "gemini"
	}
	return m
}

func isClaudeModel(model string) bool { return modelFamily(model) == "claude" }

// ChatRequestToGeminiCLI is the request leg openai-chat>gemini-cli
// (translator/request/openai-to-gemini.js:319-340, :343-376, registered :530):
// the Gemini body of ChatRequestToGemini inside the Cloud Code envelope. It
// returns the envelope and the tool names it rewrote. It mutates body.
func ChatRequestToGeminiCLI(body map[string]any, options RequestOptions, cloud CloudCodeOptions) ([]byte, *concerns.ToolNames, error) {
	return chatRequestToCloudCode(body, options, cloud, GeminiCLIProtocol)
}

// ChatRequestToAntigravity is the request leg openai-chat>antigravity
// (openai-to-gemini.js:518-526, registered :531), for the Gemini models and the
// Claude models behind Antigravity alike. The pin builds the Claude models'
// body by a different route (:379-509), which drops images, documents, thinking,
// the tool choice and every sampling control but the temperature, and
// overrides an explicit temperature of 0 with 1; here one path serves both and
// what the pin dropped is refused (PARITY 6.9 A4). It mutates body.
func ChatRequestToAntigravity(body map[string]any, options RequestOptions, cloud CloudCodeOptions) ([]byte, *concerns.ToolNames, error) {
	return chatRequestToCloudCode(body, options, cloud, AntigravityProtocol)
}

func chatRequestToCloudCode(body map[string]any, options RequestOptions, cloud CloudCodeOptions, wire string) ([]byte, *concerns.ToolNames, error) {
	if err := cloud.check(); err != nil {
		return nil, nil, err
	}
	antigravity := wire == AntigravityProtocol
	claude := antigravity && isClaudeModel(options.Model)
	if antigravity {
		if err := antigravityPreflight(body, claude); err != nil {
			return nil, nil, err
		}
		options.OmitPlaceholderSignature = claude
	}
	options.Vertex = false
	signatureOptions := options
	signatureOptions.Scope = FamilyScope(options.Scope, options.Model)
	encoded, names, err := ChatRequestToGemini(body, signatureOptions)
	if err != nil {
		return nil, nil, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		return nil, nil, err
	}
	request := map[string]any{}
	for _, key := range []string{"contents", "systemInstruction", "tools"} {
		if raw, ok := fields[key]; ok {
			request[key] = raw
		}
	}
	config := map[string]any{}
	if raw, ok := fields["generationConfig"]; ok {
		if err := json.Unmarshal(raw, &config); err != nil {
			return nil, nil, err
		}
	}
	if antigravity {
		if limit := concerns.Int(config["maxOutputTokens"]); limit > MaxAntigravityOutputTokens {
			return nil, nil, unsupported("max_tokens %d: Antigravity serves at most %d output tokens", limit, MaxAntigravityOutputTokens)
		}
		if claude {
			// The pin's `temperature || 1` (:393) also replaced an explicit 0. The
			// default applies only to a request that named none.
			if _, set := config["temperature"]; !set {
				config["temperature"] = 1
			}
		}
	}
	request["generationConfig"] = config
	if !antigravity {
		// Antigravity sends none (:364-367, executors/antigravity.js:287). The CLI
		// carries the Gemini body's list; an absent one is not sent as null.
		if raw, ok := fields["safetySettings"]; ok && string(raw) != "null" {
			request["safetySettings"] = raw
		}
	}
	switch {
	case fields["toolConfig"] != nil:
		// A tool choice the client made is kept (PARITY V19); the pin's
		// VALIDATED overrode it (:369-373).
		request["toolConfig"] = fields["toolConfig"]
	case fields["tools"] != nil:
		request["toolConfig"] = map[string]any{"functionCallingConfig": map[string]any{"mode": "VALIDATED"}}
	}
	session := cloud.SessionID
	if session == "" && options.Scope != "" {
		session = "routeweft:" + options.Scope
	}
	if session != "" {
		request["sessionId"] = toNumericSession(session)
	}
	requestID := cloud.RequestID
	if requestID == "" {
		if requestID, err = newRequestID(); err != nil {
			return nil, nil, err
		}
	}
	envelope := map[string]any{
		"project": cloud.Project, "model": options.Model, "userAgent": wire,
		"requestId": requestID, "request": request,
	}
	out, err := json.Marshal(envelope)
	return out, names, err
}

// antigravityPreflight refuses what the Antigravity wire is known not to carry
// and strips what its executor strips. reasoning_content is removed from replayed
// assistant turns because the pin's executor deletes every thought part and
// signature-only part before sending (executors/antigravity.js:203-207): the
// wire never sees them, so the leg does not pretend to send them (PARITY 6.9 A2).
func antigravityPreflight(body map[string]any, claude bool) error {
	messages, _ := body["messages"].([]any)
	// Refusals run first and over the whole body: a request that is rejected
	// must not leave the caller's map stripped (only matters on retry, but the
	// mutation is ours, not the caller's).
	if claude {
		for _, raw := range messages {
			message := concerns.Object(raw)
			if message == nil {
				continue
			}
			for _, part := range concerns.Items(message["content"]) {
				item := concerns.Object(part)
				if item == nil {
					continue
				}
				if kind := concerns.Text(item["type"]); kind != "text" {
					return unsupported("%q content: the Claude models behind Antigravity are sent text only", kind)
				}
			}
		}
		if format := body["response_format"]; format != nil {
			object := concerns.Object(format)
			if object == nil || (concerns.Text(object["type"]) != "" && concerns.Text(object["type"]) != "text") {
				return unsupported("response_format: the Claude models behind Antigravity cannot enforce it")
			}
		}
	}
	for _, raw := range messages {
		if message := concerns.Object(raw); concerns.Text(message["role"]) == "assistant" {
			delete(message, "reasoning_content")
		}
	}
	return nil
}

// cloudCodeRouting are envelope fields that say where a request goes or who sent
// it. The gateway has already decided both, so a client's value is not used;
// that is a stated reason, not a drop (PARITY 6.9 C1).
var cloudCodeRouting = map[string]bool{"project": true, "model": true, "userAgent": true, "requestId": true, "user_prompt_id": true}

// cloudCodeInner are the keys of the Gemini body. In an envelope they live under
// "request"; a body that carries them in both places is ambiguous.
var cloudCodeInner = []string{"contents", "systemInstruction", "system_instruction", "generationConfig", "tools", "toolConfig", "cachedContent", "safetySettings", "labels"}

// unwrapCloudCode returns the Gemini body of a bare or request-wrapped body.
func unwrapCloudCode(body map[string]any) (map[string]any, error) {
	inner := body
	if raw, present := body["request"]; present && raw != nil {
		wrapped := concerns.Object(raw)
		if wrapped == nil {
			return nil, unsupported("request must be an object")
		}
		for _, key := range cloudCodeInner {
			if _, dup := body[key]; dup {
				return nil, unsupported("%q is in the envelope and in request: the body is ambiguous", key)
			}
		}
		inner = wrapped
	}
	for _, fields := range []map[string]any{body, inner} {
		for key, value := range fields {
			switch {
			case key == "request" || cloudCodeRouting[key] || key == "sessionId" || key == "session_id":
			case key == "requestType":
				if value != nil && value != "agent" {
					return nil, unsupported("requestType %s: only the agent path is served; image generation is out of scope", concerns.JSONText(value))
				}
			case slices.Contains(cloudCodeInner, key):
			default:
				return nil, unsupported("envelope field %q", key)
			}
		}
	}
	copied := make(map[string]any, len(inner))
	for key, value := range inner {
		// The session is the client's own and the gateway assigns another.
		// requestType was checked above: only absent, null or "agent" got here,
		// and all three mean the same thing, so none is copied.
		if key == "sessionId" || key == "session_id" || cloudCodeRouting[key] || key == "request" || key == "requestType" {
			continue
		}
		copied[key] = value
	}
	if contents, ok := copied["contents"].([]any); !ok || len(contents) == 0 {
		return nil, unsupported("contents must be a non-empty array")
	}
	return copied, nil
}

// GeminiCLIRequestToChat is the request leg gemini-cli>openai-chat
// (gemini-to-openai.js:9-147, registered :151): the same conversion as a Gemini
// client's, which the pin shares. It also accepts the Cloud Code envelope, which
// the pin reads as an empty conversation (:43). It is not routable on its own:
// the pin has no response leg back (PARITY 6.9).
func GeminiCLIRequestToChat(body map[string]any, model string, stream bool) ([]byte, error) {
	inner, err := unwrapCloudCode(body)
	if err != nil {
		return nil, err
	}
	return GeminiRequestToChat(inner, model, stream)
}

// AntigravityRequestToChat is the request leg antigravity>openai-chat
// (translator/request/antigravity-to-openai.js:11-230, registered :242). On top of
// the Gemini conversion it carries thinkingConfig as reasoning_effort (:36-40),
// lowercases a tool schema's types and drops enumDescriptions (:89-116). topK is
// not carried, as for every wire in the family (PARITY 6.9 F1).
func AntigravityRequestToChat(body map[string]any, model string, stream bool) ([]byte, error) {
	inner, err := unwrapCloudCode(body)
	if err != nil {
		return nil, err
	}
	// thinkingEffort reads the client's thinkingConfig, which GeminiRequestToChat
	// below turns into thinkingBudget and consumes. Read before, not after.
	effort, err := thinkingEffort(inner)
	if err != nil {
		return nil, err
	}
	encoded, err := GeminiRequestToChat(inner, model, stream)
	if err != nil {
		return nil, err
	}
	chat, err := concerns.WireObject(encoded)
	if err != nil {
		return nil, err
	}
	for _, tool := range concerns.Items(chat["tools"]) {
		function := concerns.Object(concerns.Object(tool)["function"])
		if function != nil {
			function["parameters"] = normalizeSchemaTypes(function["parameters"])
		}
	}
	if effort != "" {
		chat["reasoning_effort"] = effort
	}
	return json.Marshal(chat)
}

// thinkingEffort is budgetToEffort (concerns/thinking.js:12-17) over the
// thinkingConfig an Antigravity client sends. A budget that has no effort
// (dynamic, negative) or a key with no Chat equivalent is refused; a budget of 0
// asks for no thinking, which Chat expresses by saying nothing.
func thinkingEffort(body map[string]any) (string, error) {
	config := concerns.Object(body["generationConfig"])
	raw, present := config["thinkingConfig"]
	if !present || raw == nil {
		return "", nil
	}
	thinking := concerns.Object(raw)
	if thinking == nil {
		return "", unsupported("thinkingConfig must be an object")
	}
	for key, value := range thinking {
		switch key {
		case "includeThoughts":
			// true is carried: Chat surfaces reasoning as reasoning_content whenever
			// the model produces it. false has no Chat equivalent and is not carried.
			if _, ok := value.(bool); value != nil && !ok {
				return "", unsupported("thinkingConfig.includeThoughts must be a boolean")
			}
		case "thinkingBudget":
		default:
			return "", unsupported("thinkingConfig.%s has no Chat equivalent", key)
		}
	}
	budget, present := thinking["thinkingBudget"]
	if !present || budget == nil {
		return "", nil
	}
	if !concerns.IsNumber(budget) {
		return "", unsupported("thinkingConfig.thinkingBudget must be a number")
	}
	switch value := concerns.Int(budget); {
	case value == 0:
		return "", nil
	case value == -1:
		return "", unsupported("thinkingBudget -1 (dynamic) has no reasoning_effort")
	case value < 0:
		return "", unsupported("thinkingBudget %d", value)
	case value <= 2048:
		return "low", nil
	case value <= 16384:
		return "medium", nil
	}
	return "high", nil
}

// normalizeSchemaTypes is antigravity-to-openai.js:89-116: type names lowercased
// and enumDescriptions removed, at the schema, each property and items. It works
// in place.
func normalizeSchemaTypes(schema any) any {
	node := concerns.Object(schema)
	if node == nil {
		return schema
	}
	if kind, ok := node["type"].(string); ok {
		node["type"] = strings.ToLower(kind)
	}
	delete(node, "enumDescriptions")
	for name, property := range concerns.Object(node["properties"]) {
		concerns.Object(node["properties"])[name] = normalizeSchemaTypes(property)
	}
	if items, ok := node["items"]; ok {
		node["items"] = normalizeSchemaTypes(items)
	}
	return node
}
