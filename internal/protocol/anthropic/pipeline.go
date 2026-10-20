package anthropic

import (
	"encoding/json"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
	"github.com/raufimusaddiq/routeweft/internal/protocol/openai"
)

// lastRoleIsUser is services/provider.js:181-186.
func lastRoleIsUser(body map[string]any) bool {
	messages := concerns.Items(body["messages"])
	if len(messages) == 0 {
		return true
	}
	return concerns.Text(concerns.Object(messages[len(messages)-1])["role"]) == "user"
}

// MessagesRequestToChat runs the pin's request pipeline for a Messages client
// served by a Chat provider (translator/index.js:69-154): drop thinking when the
// conversation does not end on a user turn, repair tool ids, convert (the leg
// answers unanswered tool calls once, on the Chat messages it produces), then
// normalise the result for OpenAI-compatible upstreams.
//
// The pin's preserveCacheControl quirk (translator/index.js:152) is not a
// parameter here: the leg rebuilds every block without cache_control, so there
// is nothing for the filter to keep on this path.
func MessagesRequestToChat(raw []byte, model string, stream bool) ([]byte, error) {
	body, err := concerns.WireObject(raw)
	if err != nil {
		return nil, err
	}
	if !lastRoleIsUser(body) {
		delete(body, "thinking")
	}
	concerns.EnsureToolCallIDs(body)
	chat, err := MessagesToChat(body, model, stream)
	if err != nil {
		return nil, err
	}
	if options, set := body["stream_options"]; set {
		// A client that set stream_options chose; its value is forwarded as is.
		chat["stream_options"] = options
	} else if stream {
		// Without this a Chat provider omits the usage chunk and the Messages
		// client would see zero tokens. A deliberate divergence from the pin
		// (SPEC 9.3, PARITY V9); Responses->Chat does the same.
		chat["stream_options"] = map[string]any{"include_usage": true}
	}
	openai.FilterChatBody(chat, false)
	if len(concerns.Items(chat["messages"])) == 0 {
		// Nothing survived conversion. An empty list is not a valid Chat request
		// and must not be sent as if it were one.
		chat["messages"] = []any{map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": ""}}}}
	}
	return json.Marshal(chat)
}

// ChatRequestToMessages runs the pin's request pipeline for a Chat client served
// by a Messages provider. repair is false when the Chat body was itself produced
// from another format (Responses), because the pin repairs tool ids only on the
// client's own body. clientEndsAssistant is the role the client's conversation
// really ended on, before conversion.
func ChatRequestToMessages(body map[string]any, model string, stream, repair, clientEndsAssistant bool) ([]byte, *concerns.ToolNames, error) {
	if repair {
		if !lastRoleIsUser(body) {
			delete(body, "thinking")
		}
		concerns.EnsureToolCallIDs(body)
	}
	// Anthropic rejects a tool_use that nothing answers, whichever format the
	// client spoke, so this is not limited to the client's own Chat body.
	concerns.FixMissingToolResponses(body)
	result, names, err := ChatToMessages(body, model, stream)
	if err != nil {
		return nil, nil, err
	}
	messages := result["messages"].([]any)
	if len(messages) == 0 {
		// Anthropic needs at least one message; say so with the pin's own
		// placeholder instead of sending an empty list.
		messages = append(messages, map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": TrailingUserPlaceholder}}})
	}
	result["messages"] = EnsureTrailingUserTurn(messages, clientEndsAssistant)
	encoded, err := json.Marshal(result)
	return encoded, names, err
}
