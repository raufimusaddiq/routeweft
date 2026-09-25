package routing

import (
	"net/http"
	"strings"
)

// Outcome is the retry classification for one upstream attempt (SPEC §14).
type Outcome string

const (
	OutcomeSuccess             Outcome = "success"
	OutcomeRetrySameAccount    Outcome = "retry-same-account"
	OutcomeFallbackAccount     Outcome = "fallback-account"
	OutcomeFallbackProvider    Outcome = "fallback-provider"
	OutcomeTerminalClientError Outcome = "terminal-client-error"
	OutcomeAuthRefreshRequired Outcome = "auth-refresh-required"
	OutcomeQuotaLock           Outcome = "quota-lock"
)

// Classification is one classified upstream response.
type Classification struct {
	Outcome           Outcome
	RetryAfterSeconds int
	ResetAt           string
}

// ClassifyStatus maps an upstream status and Retry-After header to a bounded
// retry classification. Unknown 4xx is terminal so client errors cannot create
// provider storms; 429/5xx may advance the bounded chain.
func ClassifyStatus(status int, header http.Header) Classification {
	retryAfter := parseRetryAfter(header.Get("Retry-After"))
	switch {
	case status >= 200 && status < 300:
		return Classification{Outcome: OutcomeSuccess}
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return Classification{Outcome: OutcomeAuthRefreshRequired}
	case status == http.StatusTooManyRequests:
		return Classification{Outcome: OutcomeQuotaLock, RetryAfterSeconds: retryAfter}
	case status == http.StatusRequestTimeout || status == http.StatusConflict:
		return Classification{Outcome: OutcomeRetrySameAccount, RetryAfterSeconds: retryAfter}
	case status >= 500:
		return Classification{Outcome: OutcomeFallbackAccount, RetryAfterSeconds: retryAfter}
	default:
		return Classification{Outcome: OutcomeTerminalClientError}
	}
}

// ClassifyError refines a status-based classification with a normalized
// provider error kind (SPEC §11 error classification, §14). It returns the
// zero Classification for an empty or unrecognized kind so callers keep the
// status-derived result; Retry-After/reset hints from the status path survive
// because only the outcome is replaced.
func ClassifyError(kind string, status int) Classification {
	switch kind {
	case "auth":
		return Classification{Outcome: OutcomeAuthRefreshRequired}
	case "quota":
		return Classification{Outcome: OutcomeQuotaLock}
	case "rate-limited":
		return Classification{Outcome: OutcomeQuotaLock}
	case "overloaded":
		return Classification{Outcome: OutcomeFallbackAccount}
	case "context-length":
		return Classification{Outcome: OutcomeTerminalClientError}
	case "not-found":
		return Classification{Outcome: OutcomeFallbackProvider}
	case "invalid-input":
		return Classification{Outcome: OutcomeTerminalClientError}
	default:
		if status >= 500 {
			return Classification{Outcome: OutcomeFallbackAccount}
		}
		return Classification{}
	}
}

// AllowsFallback reports whether another candidate may be attempted.
func (c Classification) AllowsFallback() bool {
	switch c.Outcome {
	case OutcomeFallbackAccount, OutcomeFallbackProvider:
		return true
	default:
		return false
	}
}

// ErrNoEligibleAccounts is returned when every candidate is excluded, cooling
// down or quota-locked.
var ErrNoEligibleAccounts = errNoEligibleAccounts()

func errNoEligibleAccounts() error {
	return &noEligibleError{}
}

type noEligibleError struct{}

func (*noEligibleError) Error() string { return "no eligible provider/account" }

func parseRetryAfter(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	seconds := 0
	for _, char := range raw {
		if char < '0' || char > '9' {
			return 0
		}
		seconds = seconds*10 + int(char-'0')
		if seconds > 86400 {
			return 86400
		}
	}
	return seconds
}
