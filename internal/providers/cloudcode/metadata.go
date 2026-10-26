// Package cloudcode holds the Cloud Code Assist client identity that every
// loadCodeAssist caller sends, so sign-in and quota reads describe the host the
// same way.
package cloudcode

import (
	"fmt"
	"runtime"
)

// ClientMetadata is the reference's client metadata object: CLIENT_METADATA for
// quota reads (open-sse/config/appConstants.js:35-70) and getOAuthClientMetadata
// for sign-in (src/lib/oauth/constants/oauth.js:12-19,54-56). Both fix the IDE
// and plugin enums and derive the platform enum from the host.
func ClientMetadata() string {
	return fmt.Sprintf(`{"ideType":9,"platform":%d,"pluginType":2}`, platform(runtime.GOOS, runtime.GOARCH))
}

// platform is the ClientMetadata.Platform enum: darwin 1/2, linux 3/4 (amd64 or
// any other arch, arm64), windows 5, anything else unspecified.
func platform(goos, goarch string) int {
	switch goos {
	case "darwin":
		if goarch == "arm64" {
			return 2
		}
		return 1
	case "linux":
		if goarch == "arm64" {
			return 4
		}
		return 3
	case "windows":
		return 5
	}
	return 0
}

// AntigravityUserAgent is ANTIGRAVITY_IDE_USER_AGENT
// (open-sse/providers/shared.js:89-94): a fixed IDE fingerprint, not the host.
const AntigravityUserAgent = "antigravity/ide/2.11.0 darwin/arm64"
