package wire

import (
	"bytes"
	"io"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// grokCLISleep is the retry delay, replaced by tests.
var grokCLISleep = codexSleep

// grokCLIRetry is one row of transport.retry (registry/grok-cli.js:63-67) merged
// over DEFAULT_RETRY_CONFIG (config/runtimeConfig.js:82-87): the pin's executor
// spreads the default under the entry (base.js:107), so 504 keeps its default.
type grokCLIRetry struct {
	attempts int
	delay    time.Duration
}

var grokCLIRetries = map[int]grokCLIRetry{
	http.StatusTooManyRequests:    {2, 2000 * time.Millisecond},
	http.StatusBadGateway:         {2, 1500 * time.Millisecond},
	http.StatusServiceUnavailable: {2, 1500 * time.Millisecond},
	http.StatusGatewayTimeout:     {2, 3000 * time.Millisecond},
}

// GrokCLIDo sends the request and repeats it after the wait the registry names
// for the status it was answered with (base.js:111-125,155). A failure to reach
// the endpoint counts as a 502 (:173). The attempts are one budget shared by all
// statuses (retryAttemptsByUrl, :104,113): the budget is the attempts of the row
// that applies when a retry is considered. Each attempt carries a fresh request
// id, since the pin builds its headers again (:130).
func GrokCLIDo(client *http.Client, request *http.Request) (*http.Response, error) {
	var payload []byte
	if request.Body != nil {
		var err error
		payload, err = io.ReadAll(request.Body)
		request.Body.Close()
		if err != nil {
			return nil, err
		}
	}
	used := 0
	for attempt := 0; ; attempt++ {
		attemptRequest := request.Clone(request.Context())
		attemptRequest.Body = io.NopCloser(bytes.NewReader(payload))
		attemptRequest.ContentLength = int64(len(payload))
		attemptRequest.GetBody = nil
		if attempt > 0 {
			attemptRequest.Header.Set("x-grok-req-id", uuid.NewString())
		}
		response, err := client.Do(attemptRequest)
		status := http.StatusBadGateway
		if err != nil {
			// A client that left is not an endpoint that failed (:170).
			if request.Context().Err() != nil {
				return nil, err
			}
		} else {
			status = response.StatusCode
		}
		row, retryable := grokCLIRetries[status]
		if !retryable || used >= row.attempts {
			return response, err
		}
		if response != nil && response.Body != nil {
			response.Body.Close()
		}
		used++
		if err := grokCLISleep(request.Context(), row.delay); err != nil {
			return nil, err
		}
	}
}

// grokCLIPaymentRequired is the 402 the pin names (parseError, :400-417): the
// account has hit its spending limit, which the next account may not have. The
// pin cools it for COOLDOWN.long, two minutes (config/errorConfig.js:46,77), and
// falls back (accountFallback.js:44). The body is not parsed for that: the status
// is the rule, and the pin's reading of the body (code, then error, message or the
// text) only fills the message of the error it rebuilds. Here the upstream body
// reaches the client as it is, so its `code` (personal-team-blocked:spending-limit)
// and `error` text are the client's to read; the pin's rebuilt envelope drops the code.
func grokCLIPaymentRequired(status int, _ []byte, now time.Time) (routing.Classification, bool) {
	if status != http.StatusPaymentRequired {
		return routing.Classification{}, false
	}
	return routing.Classification{
		Outcome: routing.OutcomeFallbackAccount,
		ResetAt: now.Add(2 * time.Minute).UTC().Format(time.RFC3339Nano),
	}, true
}
