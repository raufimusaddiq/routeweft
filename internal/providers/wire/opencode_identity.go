package wire

// OpenCode Zen's request identity (executors/opencode.js:417-426,492-510):
// the free tier reads a client fingerprint, not only the credential. The pin
// sends a desktop User-Agent unless the caller's own names a supported
// opencode version, a client kind, a session, a request id and a project. The
// free-tier fingerprint tools (:455-464, utils/opencodeFingerprint.js) are
// carried by opencode_fingerprint.go.

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

const (
	opencodeClientVersion = "opencode/1.18.31" // executors/opencode.js:18
	opencodeMaxIdentity   = 256                // MAX_SESSION_LENGTH
	opencodeBase62        = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
)

var (
	opencodeVersion = regexp.MustCompile(`(?i)opencode/(\d+)\.(\d+)(?:\.(\d+))?`)
	opencodeRequest = regexp.MustCompile(`^msg_[0-9a-f]{12}[0-9A-Za-z]{14}$`)
	opencodeSession = regexp.MustCompile(`\S`)
)

// opencodeIdentity is ClientRequestHeaders: x-opencode-* and the User-Agent.
// A value the client sent is kept when the pin would accept it; otherwise the
// pin's default or a stable per-connection id is sent.
func opencodeIdentity(provider routing.ProviderRef, client http.Header, _ []byte) map[string]string {
	userAgent := strings.TrimSpace(client.Get("User-Agent"))
	if !opencodeVersionAccepted(userAgent) {
		userAgent = opencodeClientVersion
	}
	return map[string]string{
		"User-Agent":         userAgent,
		"x-opencode-client":  opencodeForward(client.Get("x-opencode-client"), "desktop"),
		"x-opencode-project": opencodeForward(client.Get("x-opencode-project"), "global"),
		"x-opencode-session": opencodeForward(client.Get("x-opencode-session"), opencodeSessionID(provider.ConnectionID)),
		"x-opencode-request": opencodeForwardRequest(client.Get("x-opencode-request")),
	}
}

// opencodeVersionAccepted is hasValidOpencodeVersion: major > 1, or 1.17+.
func opencodeVersionAccepted(value string) bool {
	match := opencodeVersion.FindStringSubmatch(value)
	if match == nil {
		return false
	}
	var major, minor int
	for _, char := range match[1] {
		major = major*10 + int(char-'0')
	}
	for _, char := range match[2] {
		minor = minor*10 + int(char-'0')
	}
	return major > 1 || (major == 1 && minor >= 17)
}

func opencodeForward(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" || len(value) > opencodeMaxIdentity || !opencodeSession.MatchString(value) {
		return fallback
	}
	return value
}

func opencodeForwardRequest(value string) string {
	value = strings.TrimSpace(value)
	if opencodeRequest.MatchString(value) {
		return value
	}
	return opencodeNewRequestID(time.Now())
}

// opencodeSessionID is a stable id per connection, so prompt caching survives
// requests. The pin keeps a process-local random id per connection
// (utils/sessionManager.js deriveSessionId); a digest of the connection is the
// same value across restarts, which only makes the id more stable.
func opencodeSessionID(connectionID string) string {
	sum := sha256.Sum256([]byte("opencode-session\x00" + connectionID))
	return hex.EncodeToString(sum[:16])
}

// opencodeNewRequestID is generateRequestId (executors/opencode.js:71-80):
// `msg_` + the low 48 bits of the millisecond clock in hex + 14 base62 chars.
func opencodeNewRequestID(now time.Time) string {
	var clock [8]byte
	binary.BigEndian.PutUint64(clock[:], uint64(now.UnixMilli()))
	sum := sha256.Sum256(append([]byte("opencode-request\x00"), clock[:]...))
	var tail [14]byte
	for i := range tail {
		tail[i] = opencodeBase62[int(sum[i])%len(opencodeBase62)]
	}
	return "msg_" + hex.EncodeToString(clock[2:]) + string(tail[:])
}
