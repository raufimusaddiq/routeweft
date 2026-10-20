package concerns

import "strings"

// ReasoningText extracts reasoning from a streamed OpenAI-compatible delta
// across vendor shapes (concerns/reasoning.js:15-24): reasoning_content,
// reasoning, or reasoning_details[] (MiniMax). It returns "" when there is none.
func ReasoningText(delta map[string]any) string {
	if delta == nil {
		return ""
	}
	if text := Text(delta["reasoning_content"]); text != "" {
		return text
	}
	if text := Text(delta["reasoning"]); text != "" {
		return text
	}
	details := Items(delta["reasoning_details"])
	if details == nil {
		return ""
	}
	var joined strings.Builder
	for _, detail := range details {
		if text, ok := detail.(string); ok {
			joined.WriteString(text)
			continue
		}
		part := Object(detail)
		if text := Text(part["text"]); text != "" {
			joined.WriteString(text)
		} else {
			joined.WriteString(Text(part["content"]))
		}
	}
	return joined.String()
}
