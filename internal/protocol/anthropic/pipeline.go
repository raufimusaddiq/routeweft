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
// conversation does not end on a user turn, repair tool ids, answer unanswered
// tool calls, convert, then normalise the result for OpenAI-compatible upstreams.
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
	concerns.FixMissingToolResponses(body)
	chat := MessagesToChat(body, model, stream)
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
		concerns.FixMissingToolResponses(body)
	}
	result, names := ChatToMessages(body, model, stream)
	result["messages"] = EnsureTrailingUserTurn(result["messages"].([]any), clientEndsAssistant)
	encoded, err := json.Marshal(result)
	return encoded, names, err
}
