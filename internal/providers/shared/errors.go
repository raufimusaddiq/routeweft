package shared

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

// ErrorKind is the normalized upstream failure category. Provider modules own
// error classification (SPEC §11); this keeps the shared mapping declarative so
// each protocol family does not reimplement it (SPEC §14).
type ErrorKind string

const (
	ErrorKindUnknown       ErrorKind = "unknown"
	ErrorKindAuth          ErrorKind = "auth"
	ErrorKindQuota         ErrorKind = "quota"
	ErrorKindOverloaded    ErrorKind = "overloaded"
	ErrorKindInvalidInput  ErrorKind = "invalid-input"
	ErrorKindRateLimited   ErrorKind = "rate-limited"
	ErrorKindNotFound      ErrorKind = "not-found"
	ErrorKindContextLength ErrorKind = "context-length"
)

// UpstreamError is a normalized provider failure. Type/Code/Message are the
// provider-reported values; they are never synthesized when upstream is silent.
type UpstreamError struct {
	Status  int
	Kind    ErrorKind
	Type    string
	Code    string
	Message string
}

// ParseError normalizes an upstream error body for one protocol family. Unknown
// or malformed bodies still yield a status-derived kind so classification never
// depends on body shape (SPEC §14).
func ParseError(family string, status int, body []byte) UpstreamError {
	parsed := decodeError(body)
	if parsed.Kind == "" {
		parsed.Kind = kindFromStatus(status)
	}
	return parsed
}

func decodeError(body []byte) UpstreamError {
	if len(body) == 0 {
		return UpstreamError{}
	}
	var generic struct {
		Type  string `json:"type"`
		Error struct {
			Type    string `json:"type"`
			Code    any    `json:"code"`
			Status  string `json:"status"`
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
		Detail  struct {
			Type string `json:"type"`
		} `json:"detail"`
	}
	if json.Unmarshal(body, &generic) != nil {
		return UpstreamError{}
	}
	info := UpstreamError{Message: strings.TrimSpace(generic.Error.Message), Code: codeString(generic.Error.Code), Type: strings.TrimSpace(generic.Error.Type)}
	if info.Type == "" {
		info.Type = strings.TrimSpace(generic.Type)
	}
	if info.Message == "" {
		info.Message = strings.TrimSpace(generic.Message)
	}
	info.Kind = kindFromErrorBody(info.Type, info.Code, strings.TrimSpace(generic.Error.Status), strings.TrimSpace(generic.Detail.Type))
	return info
}

// kindFromErrorBody maps provider error identifiers. It is intentionally
// permissive: matching any known token wins over falling back to status.
func kindFromErrorBody(tokens ...string) ErrorKind {
	for _, token := range tokens {
		lower := strings.ToLower(token)
		switch {
		case strings.Contains(lower, "context_length") || strings.Contains(lower, "context window") || strings.Contains(lower, "too many tokens"):
			return ErrorKindContextLength
		case strings.Contains(lower, "authentication") || strings.Contains(lower, "unauthorized") || strings.Contains(lower, "invalid_api_key") || strings.Contains(lower, "permission"):
			return ErrorKindAuth
		case strings.Contains(lower, "insufficient_quota") || strings.Contains(lower, "quota") || strings.Contains(lower, "billing") || strings.Contains(lower, "credit"):
			return ErrorKindQuota
		case strings.Contains(lower, "overloaded") || strings.Contains(lower, "unavailable"):
			return ErrorKindOverloaded
		case strings.Contains(lower, "rate_limit") || strings.Contains(lower, "resource_exhausted") || strings.Contains(lower, "too_many_requests") || strings.Contains(lower, "rate_limited"):
			return ErrorKindRateLimited
		case strings.Contains(lower, "not_found") || strings.Contains(lower, "does not exist"):
			return ErrorKindNotFound
		case strings.Contains(lower, "invalid") || strings.Contains(lower, "bad_request") || strings.Contains(lower, "failed_precondition"):
			return ErrorKindInvalidInput
		}
	}
	return ""
}

// kindFromStatus derives a bounded fallback kind when the body is silent.
func kindFromStatus(status int) ErrorKind {
	switch {
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return ErrorKindAuth
	case status == http.StatusTooManyRequests:
		return ErrorKindRateLimited
	case status == http.StatusRequestEntityTooLarge:
		return ErrorKindContextLength
	case status == http.StatusNotFound:
		return ErrorKindNotFound
	case status >= 500:
		return ErrorKindOverloaded
	default:
		return ErrorKindUnknown
	}
}

// ErrorKindString returns the stable lower-case wire label for an error kind.
func (k ErrorKind) String() string { return string(k) }

// codeString renders a provider error code, which may be a JSON string or a
// numeric status (Gemini uses numeric codes), as a stable string token.
func codeString(value any) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return strings.TrimSpace(typed)
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return ""
	}
}
