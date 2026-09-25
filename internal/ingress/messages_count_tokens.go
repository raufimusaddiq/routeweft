package ingress

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"unicode/utf16"
)

// handleCountTokens provides Anthropic-compatible count_tokens behavior: a
// local estimate over the same fields, without an upstream call (PRD-API-007).
func (h *Handler) handleCountTokens(w http.ResponseWriter, r *http.Request) {
	if !h.authorize(w, r) {
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, h.opts.MaxBodyBytes))
	if err != nil {
		h.writeError(w, http.StatusBadRequest, "request_read_failed", "request body could not be read")
		return
	}
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		h.writeError(w, http.StatusBadRequest, "invalid_request_error", "request body must be one JSON object")
		return
	}
	if payload == nil {
		h.writeError(w, http.StatusBadRequest, "invalid_request_error", "request body must be one JSON object")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"input_tokens": estimateAnthropicInputTokens(payload)})
}

func estimateAnthropicInputTokens(payload any) int {
	chars, ok := payload.(map[string]any)
	if !ok {
		return 0
	}
	total := countValueChars(chars["system"]) + countValueChars(chars["tools"])
	if messages, ok := chars["messages"].([]any); ok {
		for _, message := range messages {
			total += countMessageChars(message)
		}
	}
	return (total + 3) / 4
}

func countMessageChars(message any) int {
	m, ok := message.(map[string]any)
	if !ok {
		return countValueChars(message)
	}
	content := m["content"]
	if text, ok := content.(string); ok {
		return len(text)
	}
	blocks, ok := content.([]any)
	if !ok {
		return countValueChars(content)
	}
	total := 0
	for _, block := range blocks {
		total += countContentBlockChars(block)
	}
	return total
}

func countContentBlockChars(block any) int {
	b, ok := block.(map[string]any)
	if !ok {
		return countValueChars(block)
	}
	switch b["type"] {
	case "text":
		return countValueChars(b["text"])
	case "tool_use":
		return countValueChars(b["name"]) + countValueChars(b["input"])
	case "tool_result":
		return countValueChars(b["content"])
	case "thinking":
		return countValueChars(b["thinking"])
	default:
		return countValueChars(b)
	}
}

func countValueChars(value any) int {
	switch v := value.(type) {
	case nil:
		return 0
	case string:
		return utf16Length(v)
	case float64:
		return len(strconv.FormatFloat(v, 'f', -1, 64))
	case bool:
		if v {
			return 4
		}
		return 5
	case []any:
		total := 0
		for _, item := range v {
			total += countValueChars(item)
		}
		return total
	case map[string]any:
		total := 0
		for key, item := range v {
			total += utf16Length(key) + countValueChars(item)
		}
		return total
	default:
		return 0
	}
}

func utf16Length(value string) int { return len(utf16.Encode([]rune(value))) }
