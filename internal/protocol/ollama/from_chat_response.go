package ollama

import (
	"encoding/json"
	"fmt"

	"github.com/raufimusaddiq/routeweft/internal/protocol/concerns"
)

// ChatResponseToOllama turns one Chat Completions body into the single JSON
// document /api/chat returns for a non-streaming call. The /v1/api/chat surface
// routes and translates as a Chat client and reshapes only the answer: the
// oracle runs its Chat handler and converts the result afterwards
// (src/app/api/v1/api/chat/route.js:26-27), so the text is Chat's
// choices[0].message.content.
//
// ponytail: text only, matching copyOllamaStream; pass tool_calls through when
// the streamed surface starts doing so.
func ChatResponseToOllama(body []byte, model string) ([]byte, error) {
	response, err := concerns.WireObject(body)
	if err != nil {
		return nil, err
	}
	choices := concerns.Items(response["choices"])
	if len(choices) == 0 {
		return nil, fmt.Errorf("upstream response has no choices")
	}
	message := concerns.Object(concerns.Object(choices[0])["message"])
	if model == "" {
		model = concerns.Text(response["model"])
	}
	out := map[string]any{
		"model":   model,
		"message": map[string]any{"role": "assistant", "content": concerns.Text(message["content"])},
		"done":    true,
	}
	return json.Marshal(out)
}
