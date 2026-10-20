package onboarding

import (
	"errors"
	"os"
	"strings"
)

// Google-family OAuth clients are supplied by the operator, never baked in.
//
// The reference ships a shared Google client id and secret in its own public
// tree (open-sse/providers/shared.js GOOGLE_OAUTH_CLIENT and
// ANTIGRAVITY_OAUTH_CLIENT). Routeweft deliberately does not republish them:
//
//   - it is a third-party credential, and this repository is public, so copying
//     it would put it in permanent public git history;
//   - using it would make Routeweft impersonate the reference's OAuth app, which
//     is a different failure than a parity gap.
//
// This is a named divergence recorded in docs/SPEC.md 19.1, not a silent gap. An
// internal test fails if a Google OAuth client secret literal appears in the tree.
const (
	// GoogleClientIDEnv supplies the OAuth client id for gemini-cli and antigravity.
	GoogleClientIDEnv = "ROUTEWEFT_GOOGLE_OAUTH_CLIENT_ID"
	// GoogleClientSecretEnv supplies the matching client secret.
	GoogleClientSecretEnv = "ROUTEWEFT_GOOGLE_OAUTH_CLIENT_SECRET"
)

// ErrGoogleClientUnset means a Google-family provider was enabled without the
// operator-supplied OAuth client.
var ErrGoogleClientUnset = errors.New("google oauth client is not configured; set " +
	GoogleClientIDEnv + " and " + GoogleClientSecretEnv)

// GoogleClientFromEnvironment reports the operator-supplied OAuth client, so
// startup can warn when a Google-family provider is enabled without it.
func GoogleClientFromEnvironment() (clientID, clientSecret string, err error) {
	return googleClient()
}

// iflow's OAuth client is third-party as well, and is supplied the same way.
// The reference ships it in its own public tree; Routeweft does not republish it.
const (
	// IflowClientIDEnv supplies the OAuth client id for iflow.
	IflowClientIDEnv = "ROUTEWEFT_IFLOW_OAUTH_CLIENT_ID"
	// IflowClientSecretEnv supplies the matching client secret.
	IflowClientSecretEnv = "ROUTEWEFT_IFLOW_OAUTH_CLIENT_SECRET"
)

// ErrIflowClientUnset means iflow was used without its operator-supplied client.
var ErrIflowClientUnset = errors.New("iflow oauth client is not configured; set " +
	IflowClientIDEnv + " and " + IflowClientSecretEnv)

// iflowClient returns the operator-supplied iflow OAuth client. It never falls
// back to a built-in value.
func iflowClient() (clientID, clientSecret string, err error) {
	clientID = strings.TrimSpace(os.Getenv(IflowClientIDEnv))
	clientSecret = strings.TrimSpace(os.Getenv(IflowClientSecretEnv))
	if clientID == "" || clientSecret == "" {
		return "", "", ErrIflowClientUnset
	}
	return clientID, clientSecret, nil
}

// googleClient returns the operator-supplied OAuth client for the Google-family
// providers (gemini-cli, antigravity). It never falls back to a built-in value.
func googleClient() (clientID, clientSecret string, err error) {
	clientID = strings.TrimSpace(os.Getenv(GoogleClientIDEnv))
	clientSecret = strings.TrimSpace(os.Getenv(GoogleClientSecretEnv))
	if clientID == "" || clientSecret == "" {
		return "", "", ErrGoogleClientUnset
	}
	return clientID, clientSecret, nil
}
