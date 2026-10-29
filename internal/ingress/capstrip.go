package ingress

import (
	"encoding/json"
	"net/http"
	"unicode/utf16"
)

const (
	charsPerToken = 4 // capacityAdapter.js:98: a rough estimate, no tokenizer
	headKeep      = 6 // messages after system kept verbatim before the middle is dropped
	// defaultAdapterContext is the window assumed when the table has none
	// (capacityAdapter.js:136).
	defaultAdapterContext = 200000
)

// stripHistoryForContext ports capacityAdapter.js:113-152. It fits the history of
// a request to a (possibly smaller) context window by dropping the middle: every
// system/developer message and the trailing user run that carries the media are
// kept, the first six older messages are kept verbatim while they fit in 80% of
// the window at four characters a token, and the rest of the older turns go.
// Unknown top-level fields and the kept messages are copied byte for byte. A
// body with no message array, or nothing to drop, is returned unchanged.
func stripHistoryForContext(body []byte, contextWindow int) []byte {
	var root map[string]json.RawMessage
	if json.Unmarshal(body, &root) != nil {
		return body
	}
	key := ""
	var items []json.RawMessage
	for _, candidate := range []string{"messages", "input", "contents"} {
		if raw, ok := root[candidate]; ok && json.Unmarshal(raw, &items) == nil && items != nil {
			key = candidate
			break
		}
		items = nil
	}
	if key == "" || len(items) == 0 {
		return body
	}
	type turn struct {
		raw     json.RawMessage
		role    string
		content json.RawMessage
	}
	var system, rest []turn
	for _, raw := range items {
		var fields struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
			Parts   json.RawMessage `json:"parts"`
		}
		_ = json.Unmarshal(raw, &fields)
		// m.content ?? m.parts: only null or absent falls through to parts.
		content := fields.Content
		if len(content) == 0 || string(content) == "null" {
			content = fields.Parts
		}
		t := turn{raw: raw, role: fields.Role, content: content}
		if fields.Role == "system" || fields.Role == "developer" {
			system = append(system, t)
		} else {
			rest = append(rest, t)
		}
	}
	if len(rest) == 0 {
		return body
	}
	i := len(rest) - 1
	for i >= 0 && rest[i].role != "assistant" && rest[i].role != "model" {
		i--
	}
	tail := rest[i+1:]
	older := rest[:i+1]
	if len(older) == 0 {
		return body
	}
	if contextWindow <= 0 {
		contextWindow = defaultAdapterContext
	}
	budget := float64(contextWindow) * 0.8 * charsPerToken
	head := older
	if len(head) > headKeep {
		head = head[:headKeep]
	}
	total := 0
	for _, group := range [][]turn{system, head, tail} {
		for _, t := range group {
			total += blockLength(t.content)
		}
	}
	// If head + tail overflow, drop head turns from the end (closest to the
	// middle) first.
	for float64(total) > budget && len(head) > 0 {
		total -= blockLength(head[len(head)-1].content)
		head = head[:len(head)-1]
	}
	if len(head) == len(older) {
		return body
	}
	kept := make([]json.RawMessage, 0, len(system)+len(head)+len(tail))
	for _, group := range [][]turn{system, head, tail} {
		for _, t := range group {
			kept = append(kept, t.raw)
		}
	}
	encoded, err := json.Marshal(kept)
	if err != nil {
		return body
	}
	root[key] = encoded
	out, err := json.Marshal(root)
	if err != nil {
		return body
	}
	return out
}

// blockLength is capacityAdapter.js:101-107: a string counts its UTF-16 code
// units (JS .length), an array counts each block's text or 50 for a block
// without text, anything else counts 0.
func blockLength(content json.RawMessage) int {
	var text string
	if json.Unmarshal(content, &text) == nil && len(content) > 0 && content[0] == '"' {
		return len(utf16.Encode([]rune(text)))
	}
	var blocks []json.RawMessage
	if len(content) == 0 || content[0] != '[' || json.Unmarshal(content, &blocks) != nil {
		return 0
	}
	sum := 0
	for _, block := range blocks {
		var fields struct {
			Text *json.RawMessage `json:"text"`
		}
		var value string
		if json.Unmarshal(block, &fields) == nil && fields.Text != nil && json.Unmarshal(*fields.Text, &value) == nil {
			sum += len(utf16.Encode([]rune(value)))
		} else {
			sum += 50
		}
	}
	return sum
}

// bodyRewriter lets a handler swap the request it parsed for one parsed from a
// different body, and put the original back. A handler registers it so that the
// shared attempt loop can fit history to an adapter model without knowing the
// protocol's request type.
type bodyRewriter func(raw []byte) (restore func(), err error)

// swapRequest replaces *current with the request parse returns and gives back
// the function that puts the original back.
func swapRequest[T any](current *T, parse func() (*T, error)) (func(), error) {
	next, err := parse()
	if err != nil {
		return nil, err
	}
	saved := *current
	*current = *next
	return func() { *current = saved }, nil
}

// setBodyRewriter records the swap on the request's client record. It must be
// called on a request that went through withClientBody.
func setBodyRewriter(r *http.Request, rewrite bodyRewriter) {
	if recorded, ok := r.Context().Value(clientBodyKey{}).(*clientRequest); ok {
		recorded.rewrite = rewrite
	}
}

func bodyRewriterOf(r *http.Request) bodyRewriter {
	if recorded, ok := r.Context().Value(clientBodyKey{}).(*clientRequest); ok {
		return recorded.rewrite
	}
	return nil
}
