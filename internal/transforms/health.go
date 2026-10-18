package transforms

import "fmt"

// HealthStatusError reports a helper health probe that reached the service but
// got a non-2xx answer, so callers can classify it without matching text.
type HealthStatusError struct {
	Service    string
	StatusCode int
}

func (e *HealthStatusError) Error() string {
	return fmt.Sprintf("%s health returned HTTP %d", e.Service, e.StatusCode)
}
