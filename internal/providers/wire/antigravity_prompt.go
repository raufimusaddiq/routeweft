package wire

import (
	"encoding/json"
	"regexp"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/protocol/gemini"
)

// antigravityPrompts is the system-prompt rewrite of transformRequest
// (executors/antigravity.js:265-274): each text part of request.systemInstruction
// goes through ANTIGRAVITY_PROMPT_REWRITES in order (config/appConstants.js:175-183).
// The pin's comment gives the reason: a competing client's branding makes the
// backend answer a fake 429 Quota Exhausted. Only the system instruction is
// rewritten; the conversation is not.
//
// Each rule is written to match what JavaScript matches, not what Go's regexp
// would: `\s` is JavaScript's (Unicode spaces, not Go's ASCII set), /i folds ASCII
// only (letters), and the multiline `^` follows any JavaScript line terminator.
func antigravityPrompts(protocol string, body Body) bool {
	if protocol != gemini.AntigravityProtocol {
		return false
	}
	var request map[string]json.RawMessage
	var instruction map[string]json.RawMessage
	var parts []json.RawMessage
	if json.Unmarshal(body["request"], &request) != nil || json.Unmarshal(request["systemInstruction"], &instruction) != nil ||
		json.Unmarshal(instruction["parts"], &parts) != nil {
		return false
	}
	changed := false
	for index, raw := range parts {
		var part map[string]json.RawMessage
		var text string
		if json.Unmarshal(raw, &part) != nil || json.Unmarshal(part["text"], &text) != nil {
			continue
		}
		rewritten := rewriteAntigravityPrompt(text)
		if rewritten == text {
			continue
		}
		encoded, err := json.Marshal(rewritten)
		if err != nil {
			return false
		}
		part["text"] = encoded
		if parts[index], err = json.Marshal(part); err != nil {
			return false
		}
		changed = true
	}
	if !changed {
		return false
	}
	// Each level is written back over the one it came from; what the rewrite did
	// not touch stays as RawMessage, byte for byte.
	instruction["parts"], _ = json.Marshal(parts)
	request["systemInstruction"], _ = json.Marshal(instruction)
	return setJSON(body, "request", request)
}

const (
	claudeAgentPrompt = "You are a Claude agent, built on Anthropic's Claude Agent SDK."
	billingHeader     = "x-anthropic-billing-header:"
	// jsSpaceClass is JavaScript's \s.
	jsSpaceClass = `[\t\n\x0b\f\r \x{00a0}\x{1680}\x{2000}-\x{200a}\x{2028}\x{2029}\x{202f}\x{205f}\x{3000}\x{feff}]`
)

var (
	// You are Hermes(?: Agent)?(?:,\s*(?:an intelligent AI assistant|an AI assistant|an AI agent))?(?:,?\s*(?:built|created)\s+by\s+Nous Research)?\.
	hermesPrompt = regexp.MustCompile(letters("You are Hermes") + `(?:` + letters(" Agent") + `)?(?:,` + jsSpaceClass + `*(?:` +
		letters("an intelligent AI assistant") + `|` + letters("an AI assistant") + `|` + letters("an AI agent") + `))?(?:,?` + jsSpaceClass + `*(?:` +
		letters("built") + `|` + letters("created") + `)` + jsSpaceClass + `+` + letters("by") + jsSpaceClass + `+` + letters("Nous Research") + `)?\.`)
	opencodePrompt = regexp.MustCompile(letters("opencode"))
)

func rewriteAntigravityPrompt(text string) string {
	text = strings.ReplaceAll(text, claudeAgentPrompt, "")
	text = hermesPrompt.ReplaceAllLiteralString(text, "You are an AI assistant.")
	text = dropBillingHeaders(text)
	return opencodePrompt.ReplaceAllStringFunc(text, func(match string) string {
		switch match {
		case "OpenCode":
			return "Antigravity"
		case "OPENCODE":
			return "ANTIGRAVITY"
		}
		return "antigravity"
	})
}

// dropBillingHeaders is /^x-anthropic-billing-header:[^\n]*(?:\r?\n)*/gim replaced by
// nothing: a line that starts so goes, with the line breaks after it. A line starts
// at the text's start and after \n, \r, U+2028 and U+2029.
func dropBillingHeaders(text string) string {
	var kept strings.Builder
	start, resume, previous := 0, 0, '\n'
	for position, r := range text {
		if position < resume {
			continue
		}
		lineStart := previous == '\n' || previous == '\r' || previous == '\u2028' || previous == '\u2029'
		previous = r
		if !lineStart || len(text)-position < len(billingHeader) || !strings.EqualFold(text[position:position+len(billingHeader)], billingHeader) {
			continue
		}
		kept.WriteString(text[start:position])
		start = billingHeaderEnd(text, position+len(billingHeader))
		resume, previous = start, '\n'
	}
	kept.WriteString(text[start:])
	return kept.String()
}

// billingHeaderEnd is where `[^\n]*(?:\r?\n)*` stops from position.
func billingHeaderEnd(text string, position int) int {
	for position < len(text) && text[position] != '\n' {
		position++
	}
	for position < len(text) {
		switch {
		case text[position] == '\n':
			position++
		case text[position] == '\r' && position+1 < len(text) && text[position+1] == '\n':
			position += 2
		default:
			return position
		}
	}
	return position
}
