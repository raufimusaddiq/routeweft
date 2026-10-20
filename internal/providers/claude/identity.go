package claude

import (
	"bytes"
	"encoding/json"
)

// IdentityPrompt is the first system block a Claude OAuth credential is used
// with. Anthropic's Claude Code OAuth traffic carries it, and the pinned oracle
// sends it on every request (translator/request/openai-to-claude.js:136), which
// is why a credential that works there keeps working here. It belongs to this
// provider's preparation, not to the Chat/Responses to Messages legs, which must
// not give other vendors' models this identity (PARITY V14).
const IdentityPrompt = "You are Claude Code, Anthropic's official CLI for Claude."

// WithIdentity returns body with IdentityPrompt as the first system block. A
// body that already starts with it is returned as is, so applying it twice is
// harmless; a string system prompt becomes the second block.
func WithIdentity(body []byte) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.UseNumber()
	var fields map[string]any
	if err := decoder.Decode(&fields); err != nil || fields == nil {
		return nil, err
	}
	identity := map[string]any{"type": "text", "text": IdentityPrompt}
	var system []any
	switch existing := fields["system"].(type) {
	case nil:
	case string:
		if existing != "" {
			system = []any{map[string]any{"type": "text", "text": existing}}
		}
	case []any:
		system = existing
	}
	if len(system) > 0 {
		if first, _ := system[0].(map[string]any); first != nil && first["text"] == IdentityPrompt {
			return body, nil
		}
	}
	fields["system"] = append([]any{identity}, system...)
	return json.Marshal(fields)
}
