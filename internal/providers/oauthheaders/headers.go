// Package oauthheaders owns the identity headers for OAuth-specialized
// providers whose upstream requires a client fingerprint in addition to the
// credential (docs/PROVIDER_BASELINE.md OAuth-specialized group). Values are
// pinned to the PROVIDER_BASELINE reference snapshot. Credentials themselves are
// never included here; the dispatch layer injects them.
package oauthheaders

// iFlow identifies as its CLI on every request.
const (
	iflowUserAgent = "iFlow-Cli"
)

// GitHub Copilot requires the VS Code chat fingerprint on both the chat and
// responses endpoints; the credential token is added by the dispatch layer.
const (
	CopilotIntegrationID   = "vscode-chat"
	CopilotEditorVersion   = "vscode/1.110.0"
	CopilotPluginVersion   = "copilot-chat/0.38.0"
	CopilotUserAgent       = "GitHubCopilotChat/0.38.0"
	CopilotOpenAIIntent    = "conversation-panel"
	CopilotAPIVersion      = "2025-04-01"
	CopilotFetcherVersion  = "electron-fetch"
	CopilotInitiatorHeader = "X-Initiator"
	CopilotInitiatorValue  = "user"
)

// Cline/ClinePass send the Cline referer/title pair; the gateway answers with
// the cline envelope, which the response path unwraps.
const (
	ClineReferer    = "https://cline.bot"
	ClineTitle      = "Cline"
	ClineRefererKey = "HTTP-Referer"
	ClineTitleKey   = "X-Title"
)

// CodeBuddy identifies as its CLI/IDE product on the unified gateway.
const (
	CodeBuddyCNUserAgent   = "CLI/2.108.1 CodeBuddy/2.108.1"
	CodeBuddyIntlUserAgent = "IDE/2.108.1 CodeBuddy/2.108.1"
)

// Headers returns the provider identity headers for a known OAuth-specialized
// provider id. An unknown id returns nil so callers add no fingerprint.
func Headers(providerID string) map[string]string {
	switch providerID {
	case "iflow":
		return map[string]string{"User-Agent": iflowUserAgent}
	case "github":
		return map[string]string{
			"copilot-integration-id":              CopilotIntegrationID,
			"editor-version":                      CopilotEditorVersion,
			"editor-plugin-version":               CopilotPluginVersion,
			"user-agent":                          CopilotUserAgent,
			"openai-intent":                       CopilotOpenAIIntent,
			"x-github-api-version":                CopilotAPIVersion,
			"x-vscode-user-agent-library-version": CopilotFetcherVersion,
			CopilotInitiatorHeader:                CopilotInitiatorValue,
		}
	case "cline", "clinepass":
		return map[string]string{
			ClineRefererKey: ClineReferer,
			ClineTitleKey:   ClineTitle,
		}
	case "codebuddy-cn":
		return map[string]string{"User-Agent": CodeBuddyCNUserAgent}
	case "codebuddy-intl":
		return map[string]string{"User-Agent": CodeBuddyIntlUserAgent}
	default:
		return nil
	}
}
