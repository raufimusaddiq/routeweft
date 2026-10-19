// Package gemini owns first-party Gemini API-key provider metadata.
package gemini

const (
	ID             = "gemini"
	DefaultBaseURL = "https://generativelanguage.googleapis.com/v1beta/models"
	APIKeyHeader   = "x-goog-api-key"
)

var staticModels = []string{
	"gemini-3.8-flash", "gemini-3.7-flash", "gemini-3.6-flash",
	"gemini-3.5-flash-lite", "gemini-3.1-pro-preview", "gemini-3.1-flash-lite-preview",
	"gemini-3-flash-preview", "gemini-2.5-pro", "gemini-2.5-flash",
	"gemini-2.5-flash-lite", "gemma-4-31b-it",
}

// Models returns a defensive copy of the built-in model IDs.
func Models() []string { return append([]string(nil), staticModels...) }

// Headers returns provider-required non-secret headers. API keys are supplied
// per connection and must never be included here.
func Headers() map[string]string { return map[string]string{} }
