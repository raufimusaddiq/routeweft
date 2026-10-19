// Package anthropic owns first-party Anthropic Messages provider metadata.
package anthropic

const (
	ID             = "anthropic"
	DefaultBaseURL = "https://api.anthropic.com/v1"
	Version        = "2023-06-01"
	Beta           = "claude-code-20250219,interleaved-thinking-2025-05-14"
)

var staticModels = []string{
	"claude-sonnet-4-20250514",
	"claude-opus-4-20250514",
	"claude-3-5-sonnet-20241022",
}

// Models returns the first-party static catalog as a defensive copy.
func Models() []string { return append([]string(nil), staticModels...) }

// Headers returns Anthropic's first-party Messages API headers.
func Headers() map[string]string {
	return map[string]string{
		"anthropic-version": Version,
		"Anthropic-Beta":    Beta,
	}
}
