// Package oauthheaders owns the identity headers for OAuth-specialized
// providers whose upstream requires a client fingerprint in addition to the
// credential (docs/PROVIDER_BASELINE.md OAuth-specialized group). Values are
// pinned to the PROVIDER_BASELINE reference snapshot. Credentials themselves are
// never included here; the dispatch layer injects them.
package oauthheaders

import "strings"

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

// CodeBuddy has no entry on purpose. The reference sends a CodeBuddy CLI/IDE
// User-Agent; Routeweft does not present itself as another client, so CodeBuddy
// requests carry Routeweft's own User-Agent (docs/PARITY.md, CodeBuddy).

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
	default:
		return nil
	}
}

// KimiDevice is the X-Msh device identity every Kimi request carries
// (open-sse/config/appConstants.js:215-244 buildKimiHeaders). The device id is the
// connection's own and must stay stable for its OAuth session; an API-key
// connection has none and sends the shared placeholder. The identity is
// Routeweft's, not the reference's "9router", and the host name and OS are not
// disclosed: the same three headers onboarding and usage already send.
func KimiDevice(deviceID string) map[string]string {
	if strings.TrimSpace(deviceID) == "" {
		deviceID = "routeweft"
	}
	return map[string]string{
		"X-Msh-Platform":    "routeweft",
		"X-Msh-Device-Id":   strings.TrimSpace(deviceID),
		"X-Msh-Device-Name": "routeweft",
	}
}
