package wire

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// Cloud Code Assist (Gemini CLI and Antigravity): the Gemini body in an envelope
// at {base}/v1internal:{action}. The two executors differ in the headers and the
// default host, and share everything else.

const (
	// cloudCodeVersionPath is the pin's path segment before the action
	// (executors/antigravity.js:124; gemini-cli.js:12 keeps it in the base URL).
	cloudCodeVersionPath = "v1internal"
	// geminiCLIVersion and geminiCLIAPIClient are registry/gemini-cli.js:24-25.
	geminiCLIVersion   = "0.34.0"
	geminiCLIAPIClient = "google-genai-sdk/1.41.0 gl-node/v22.19.0"
	// antigravityUserAgent is ANTIGRAVITY_IDE_USER_AGENT (providers/shared.js:94),
	// which the pin keeps static whatever host it runs on.
	antigravityUserAgent = "antigravity/ide/2.11.0 darwin/arm64"
)

// cloudCodeRoute is buildUrl of both executors (gemini-cli.js:10-13,
// antigravity.js:118-125): the action is joined to v1internal with a colon, and a
// stream asks for SSE. The connection must name its Code Assist project, which
// the envelope carries; the pin makes one up when Antigravity has none
// (antigravity.js:138), and Routeweft refuses (protocol/gemini CloudCodeOptions).
func cloudCodeRoute(provider routing.ProviderRef, endpoint string) (Target, error) {
	if strings.TrimSpace(provider.Connection.Settings.ProjectID) == "" {
		return Target{}, errors.New(provider.ProviderID + " connection has no projectId: reconnect the account so the Code Assist project is discovered, or set providerData.projectId")
	}
	action, found := strings.CutPrefix(endpoint, cloudCodeVersionPath+":")
	if !found || (action != "generateContent" && action != "streamGenerateContent") {
		return Target{}, errors.New(provider.ProviderID + " cannot be called at " + endpoint)
	}
	target := Target{BaseURL: strings.TrimSuffix(strings.TrimRight(provider.BaseURL, "/"), "/"+cloudCodeVersionPath)}
	if action == "streamGenerateContent" {
		target.Query = "alt=sse"
	}
	return target, nil
}

// geminiCLIHeaders is buildHeaders (gemini-cli.js:15-23). The model in the user
// agent is the envelope's. The platform is the pin's host-derived one, fixed.
func geminiCLIHeaders(_ routing.ProviderRef, body []byte, requestURL string) (map[string]string, error) {
	var envelope struct {
		Model string `json:"model"`
	}
	_ = json.Unmarshal(body, &envelope)
	model := strings.TrimSpace(envelope.Model)
	if model == "" {
		model = "unknown"
	}
	return map[string]string{
		"User-Agent":        "GeminiCLI/" + geminiCLIVersion + "/" + model + " (linux; x64; terminal)",
		"X-Goog-Api-Client": geminiCLIAPIClient,
		"Accept":            cloudCodeAccept(requestURL),
	}, nil
}

// antigravityHeaders is buildHeaders (antigravity.js:129-135): the bearer and the
// IDE user agent, which the shared path sets and this overrides.
func antigravityHeaders(_ routing.ProviderRef, _ []byte, _ string) (map[string]string, error) {
	return map[string]string{"User-Agent": antigravityUserAgent}, nil
}

func cloudCodeAccept(requestURL string) string {
	if strings.Contains(requestURL, ":streamGenerateContent") {
		return "text/event-stream"
	}
	return "application/json"
}
