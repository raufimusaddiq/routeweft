package telemetry

import (
	"encoding/json"
	"regexp"
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

// SensitiveKey reports whether a field name holds a credential, by the same
// rule the log and event redaction uses.
func SensitiveKey(key string) bool { return sensitiveKey(key) }

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

// DetailRedactor removes what a request detail must never carry (PRD-OBS-002): the
// value of every credential-shaped key, and, wherever they appear in a string, the
// credentials the request itself used and any "Bearer" token. It differs from Redact
// in one way: a token COUNT is kept (max_tokens, prompt_tokens), because a number
// under a key ending in "tokens" is a quantity, not a secret.
type DetailRedactor struct {
	// Secrets are credential values this request used (an API key, an access
	// token). Each occurrence in any string is replaced, whatever key held it.
	Secrets []string
}

var bearerPattern = regexp.MustCompile(`(?i)\b(bearer|basic)\s+[A-Za-z0-9._~+/=\-]{8,}`)

// minSecret keeps a one-character "secret" from blanking every letter it matches.
const minSecret = 8

// Redact returns a redacted copy of a decoded JSON value.
func (d DetailRedactor) Redact(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if sensitiveKey(key) && !quantity(key, item) {
				out[key] = redactedPlaceholder
				continue
			}
			out[key] = d.Redact(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for i, item := range typed {
			out[i] = d.Redact(item)
		}
		return out
	case string:
		return d.scrub(typed)
	default:
		return value
	}
}

// scrub replaces the request's own credentials and Bearer tokens inside a string.
func (d DetailRedactor) scrub(text string) string {
	for _, secret := range d.Secrets {
		if len(secret) >= minSecret {
			text = strings.ReplaceAll(text, secret, redactedPlaceholder)
		}
	}
	return bearerPattern.ReplaceAllString(text, "$1 "+redactedPlaceholder)
}

// Text redacts a string that is not JSON.
func (d DetailRedactor) Text(text string) string { return d.scrub(text) }

// quantity reports whether the value under a credential-shaped key is a count: a
// number under a key ending in "tokens" (max_tokens, prompt_tokens, cached_tokens).
func quantity(key string, value any) bool {
	switch value.(type) {
	case float64, json.Number, int, int64:
		return strings.HasSuffix(normaliseKey(key), "tokens")
	case map[string]any:
		// the usage object ("tokens": {prompt_tokens: n, ...}) holds counts; its own
		// keys are redacted individually on the way down.
		return normaliseKey(key) == "tokens"
	}
	return false
}
