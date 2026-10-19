package transforms

import "errors"

// Helpers may replace text, never conversation identities or opaque wire blocks.
func ValidateReplacement(before Request, system []string, messages []Message) error {
	if len(system) != len(before.System) || len(messages) != len(before.Messages) {
		return errors.New("compression changed the number of text slots")
	}
	for i, message := range messages {
		if message.Role != before.Messages[i].Role || message.ToolResult != before.Messages[i].ToolResult {
			return errors.New("compression changed a conversation identity")
		}
	}
	return nil
}
