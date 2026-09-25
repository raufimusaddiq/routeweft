package claude

// CLI identity headers. Values are pinned to the PROVIDER_BASELINE reference
// snapshot so Claude OAuth traffic matches the Claude CLI fingerprint.
const (
	CLIUserAgent  = "claude-cli/" + CLIVersion + " (external, sdk-cli)"
	AnthropicBeta = "claude-code-20250219,oauth-2025-04-20,interleaved-thinking-2025-05-14"
)

// Headers returns the Claude CLI identity headers for OAuth requests. The
// credential itself is never included here.
func Headers() map[string]string {
	return map[string]string{
		"User-Agent":        CLIUserAgent,
		"X-App":             "cli",
		"anthropic-version": "2023-06-01",
		"anthropic-beta":    AnthropicBeta,
	}
}
