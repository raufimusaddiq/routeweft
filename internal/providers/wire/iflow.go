package wire

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/providers/oauthheaders"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// iflowCanonical builds the exact string iFlow signs. The pin's canonical input
// is `${userAgent}:${sessionID}:${timestamp}` (open-sse/executors/iflow.js:31)
// with the session id minted as `session-${uuidv4}` (:45) and the timestamp from
// `Date.now()`, i.e. unix milliseconds (:46). The separator is a single colon and
// there is no trailing one; the order is user agent, session id, timestamp.
func iflowCanonical(userAgent, sessionID string, timestamp int64) string {
	return userAgent + ":" + sessionID + ":" + strconv.FormatInt(timestamp, 10)
}

// iflowSignature is HMAC-SHA256 over iflowCanonical, hex encoded, keyed with the
// API key (open-sse/executors/iflow.js:32-34: createHmac("sha256", apiKey),
// update(payload), digest("hex")). The pin prefers `apiKey` and falls back to
// `accessToken` (:52); Routeweft stores the account key as the connection's
// access token, so provider.APIToken is that value.
func iflowSignature(userAgent, sessionID string, timestamp int64, apiKey string) string {
	mac := hmac.New(sha256.New, []byte(apiKey))
	mac.Write([]byte(iflowCanonical(userAgent, sessionID, timestamp)))
	return hex.EncodeToString(mac.Sum(nil))
}

// iflowHeaders is the request signature every iFlow call carries
// (open-sse/executors/iflow.js:57-64): the identity User-Agent, a fresh
// `session-id`, its millisecond `x-iflow-timestamp`, and the signature over all
// three. The user agent is the one actually sent (oauthheaders sets the same
// constant), because the canonical string covers it.
//
// A missing credential is refused before this runs (RequireCredential); the
// signature is only ever produced for a request that has one. A caller that
// reaches here with no credential (the model-listing route builds its headers
// before it reads the secret) gets no signature headers at all rather than a
// signature over an empty key, which is a wrong signature rather than an absent
// one and would be rejected as a corrupt request.
func iflowHeaders(provider routing.ProviderRef) map[string]string {
	if strings.TrimSpace(provider.APIToken) == "" {
		return nil
	}
	userAgent := oauthheaders.IflowUserAgent
	sessionID := "session-" + uuid.NewString()
	timestamp := time.Now().UnixMilli()
	return map[string]string{
		"User-Agent":        userAgent,
		"session-id":        sessionID,
		"x-iflow-timestamp": strconv.FormatInt(timestamp, 10),
		"x-iflow-signature": iflowSignature(userAgent, sessionID, timestamp, provider.APIToken),
	}
}

// iflowStreamOptions injects stream_options.include_usage for a streaming Chat
// request (open-sse/executors/iflow.js:99-104): the gateway then reports usage
// on the stream, so a streamed request is accounted instead of unobserved. An
// existing stream_options (whatever it says) is the client's and is kept.
func iflowStreamOptions(protocol string, body Body) bool {
	if protocol != openaiadapter.ChatProtocol || string(bytes.TrimSpace(body["stream"])) != "true" {
		return false
	}
	if _, present := body["stream_options"]; present {
		return false
	}
	return setJSON(body, "stream_options", map[string]any{"include_usage": true})
}
