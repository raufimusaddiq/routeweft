package codex

import _ "embed"

// DefaultInstructions is the system prompt a Codex request carries when the
// client sent none or only whitespace: the endpoint rejects a request without
// instructions (open-sse/config/codexInstructions.js, injected at
// executors/codex.js:447-450). It is the pin's text, byte for byte.
//
//go:embed default_instructions.txt
var DefaultInstructions string
