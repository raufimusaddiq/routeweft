package telemetry

import (
	"encoding/json"
	"strings"
)

// redactedPlaceholder replaces any sensitive value. It is a fixed marker so an
// operator can tell redaction happened without learning the original value.
const redactedPlaceholder = "[redacted]"

// sensitiveKeys are the normalised (lower-case, separator-stripped) key names
// whose values are never stored or logged (PRD-OBS-002, PRD-SEC-001). Matching
// is substring-based on the normalised key so variants like Authorization,
// api_key, apiKey, refreshToken and X-Api-Key all match.
var sensitiveKeys = []string{
	"authorization", "proxyauthorization", "cookie", "setcookie",
	"apikey", "xapikey", "xgoogapikey", "key", "secret", "clientsecret",
	"token", "accesstoken", "refreshtoken", "idtoken", "bearer",
	"password", "passwd", "credential", "privatekey", "sessionkey",
}

// sensitiveKey reports whether a JSON/log field name holds a credential.
func sensitiveKey(key string) bool {
	normalised := normaliseKey(key)
	if normalised == "" {
		return false
	}
	for _, candidate := range sensitiveKeys {
		if strings.Contains(normalised, candidate) {
			return true
		}
	}
	return false
}

func normaliseKey(key string) string {
	var builder strings.Builder
	for _, char := range strings.ToLower(strings.TrimSpace(key)) {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			builder.WriteRune(char)
		}
	}
	return builder.String()
}

// Redact walks an arbitrary decoded JSON value and replaces the value of every
// sensitive key with the placeholder. It returns the redacted copy; the input is
// never mutated.
func Redact(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if sensitiveKey(key) {
				out[key] = redactedPlaceholder
				continue
			}
			out[key] = Redact(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = Redact(item)
		}
		return out
	default:
		return value
	}
}

// RedactJSON redacts a JSON document. Malformed or non-JSON input is rejected
// rather than stored verbatim, because a detail blob that cannot be parsed
// cannot be proven free of credentials.
func RedactJSON(document []byte) ([]byte, bool) {
	if len(document) == 0 {
		return nil, false
	}
	var decoded any
	if err := json.Unmarshal(document, &decoded); err != nil {
		return nil, false
	}
	encoded, err := json.Marshal(Redact(decoded))
	if err != nil {
		return nil, false
	}
	return encoded, true
}

// RedactHeaderValue returns a log-safe header value: credential-bearing headers
// are replaced outright, everything else is returned unchanged.
func RedactHeaderValue(name, value string) string {
	if sensitiveKey(name) {
		return redactedPlaceholder
	}
	return value
}
