// Package cline owns the Cline gateway wire quirks shared by the cline and
// clinepass providers: the credential prefix, the per-request identity headers
// and the non-stream response envelope. The reference is the pinned LiteRouter
// open-sse/shared/clineAuth.js and open-sse/shared/clineEnvelope.js.
package cline

import (
	"bytes"
	"encoding/json"
	"regexp"
	"runtime"
	"strings"

	"github.com/raufimusaddiq/routeweft/internal/buildinfo"
	"github.com/raufimusaddiq/routeweft/internal/providers/oauthheaders"
)

// Applies reports whether the provider id opts in to the Cline wire (the
// reference's transport.quirks.clineEnvelope plus the clineHeaders auth hook,
// declared by exactly these two providers).
func Applies(providerID string) bool {
	return providerID == "cline" || providerID == "clinepass"
}

// workosJWT is the reference's JWT test: a base64url `eyJ` header and a payload.
var workosJWT = regexp.MustCompile(`^eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)

// AccessToken returns the token as Cline expects it (clineAuth.js
// getClineAccessToken). A token already starting with `workos:` (any case) is
// kept as is; a WorkOS JWT, which is what a Cline OAuth access token is, gets the
// `workos:` prefix; anything else, such as a ClinePass `clp_` key, is sent
// verbatim, because prefixing it makes Cline answer HTTP 401. An empty or
// blank token yields "".
func AccessToken(token string) string {
	trimmed := strings.TrimSpace(token)
	if trimmed == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(trimmed), "workos:") {
		return trimmed
	}
	if workosJWT.MatchString(trimmed) {
		return "workos:" + trimmed
	}
	return trimmed
}

// Authorization returns the Authorization header value, or "" for a blank token.
func Authorization(token string) string {
	if access := AccessToken(token); access != "" {
		return "Bearer " + access
	}
	return ""
}

// Headers returns the full clineHeaders set (clineAuth.js buildClineHeaders) for
// a credential, including Authorization when the token is not blank.
//
// Identity decision (parity with the reference's shape, not its name): the
// reference sends its own product identity for User-Agent and X-CLIENT-TYPE
// ("9Router/<version>", "9router"), not Cline's, so the gateway accepts a
// third-party client. Routeweft therefore sends its own ("Routeweft/<version>",
// "routeweft") rather than impersonating LiteRouter. X-PLATFORM uses Node's
// vocabulary (win32 for Windows) and X-PLATFORM-VERSION the runtime version.
func Headers(token string) map[string]string {
	version := buildinfo.Current().Version
	if version == "" {
		version = "0.0.0"
	}
	platform := runtime.GOOS
	if platform == "windows" {
		platform = "win32"
	}
	headers := map[string]string{
		oauthheaders.ClineRefererKey: oauthheaders.ClineReferer,
		oauthheaders.ClineTitleKey:   oauthheaders.ClineTitle,
		"User-Agent":                 "Routeweft/" + version,
		"X-PLATFORM":                 platform,
		"X-PLATFORM-VERSION":         runtime.Version(),
		"X-CLIENT-TYPE":              "routeweft",
		"X-CLIENT-VERSION":           version,
		"X-CORE-VERSION":             version,
		"X-IS-MULTIROOT":             "false",
	}
	if authorization := Authorization(token); authorization != "" {
		headers["Authorization"] = authorization
	}
	return headers
}

// UnwrapEnvelope returns the inner object of Cline's non-stream envelope
// {"success":true,"data":{...}} (clineEnvelope.js unwrapClineEnvelope). `data`
// must be a non-null, non-array object. Anything else, including the error
// envelope {"success":false,...}, invalid JSON and non-object bodies, is
// returned unchanged. The inner bytes are returned as received, so unknown
// fields and their order survive. Streams are never unwrapped.
func UnwrapEnvelope(body []byte) ([]byte, bool) {
	var envelope struct {
		Success json.RawMessage `json:"success"`
		Data    json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil {
		return body, false
	}
	if !bytes.Equal(bytes.TrimSpace(envelope.Success), []byte("true")) {
		return body, false
	}
	data := bytes.TrimSpace(envelope.Data)
	if len(data) == 0 || data[0] != '{' {
		return body, false
	}
	return data, true
}
