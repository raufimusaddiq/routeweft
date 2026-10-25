package routing

import "time"

// DefaultFallbackBackoff is the cooldown a failed attempt gets when the provider
// named no Retry-After and no reset (the pin's TRANSIENT_COOLDOWN_MS,
// config/errorConfig.js). Serving and the connection probe both use it.
const DefaultFallbackBackoff = 30 * time.Second

// CooldownDeadline returns the deadline for a classification, honoring
// Retry-After over generic backoff and any trusted reset timestamp.
func CooldownDeadline(classification Classification, now time.Time, defaultBackoff time.Duration) time.Time {
	if reset, ok := parseResetTimestamp(classification.ResetAt); ok && reset.After(now) {
		return reset
	}
	if classification.RetryAfterSeconds > 0 {
		return now.Add(time.Duration(classification.RetryAfterSeconds) * time.Second)
	}
	if classification.Outcome == OutcomeAuthRefreshRequired {
		// Pinned v1 cools invalid credentials for two minutes.
		return now.Add(2 * time.Minute)
	}
	if classification.Outcome == OutcomeRetrySameAccount {
		return now.Add(time.Second)
	}
	if classification.AllowsFallback() {
		return now.Add(defaultBackoff)
	}
	return time.Time{}
}

func parseResetTimestamp(raw string) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, false
	}
	return parsed, true
}
