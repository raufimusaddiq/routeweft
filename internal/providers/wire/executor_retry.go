package wire

import (
	"context"
	"net/http"
	"time"
)

// baseRetry is DEFAULT_RETRY_CONFIG (open-sse/config/runtimeConfig.js:80-87):
// attempts are retries after the first answer, at a fixed delay. A network
// exception is retried under the 502 entry (:164-180). 429 is not retried here;
// a single-URL executor has no fallback to move to.
var baseRetry = map[int]struct {
	attempts int
	delay    time.Duration
}{
	http.StatusBadGateway:         {attempts: 3, delay: 3 * time.Second},
	http.StatusServiceUnavailable: {attempts: 3, delay: 2 * time.Second},
	http.StatusGatewayTimeout:     {attempts: 2, delay: 3 * time.Second},
}

const baseNetworkRetries = 3

// exchangeWithRetry is BaseExecutor.execute's same-URL retry (:100-180). The
// caller's request keeps its GetBody so every attempt sends the same bytes.
// sleep is injectable so the decision is tested without the oracle's delays.
func exchangeWithRetry(ctx context.Context, client *http.Client, request *http.Request, sleep func(context.Context, time.Duration) error) (*http.Response, error) {
	retries := map[int]int{}
	send := func(attempt int) (*http.Request, error) {
		if attempt == 0 {
			return request, nil
		}
		clone := request.Clone(ctx)
		if request.GetBody != nil {
			body, err := request.GetBody()
			if err != nil {
				return nil, err
			}
			clone.Body = body
		}
		return clone, nil
	}
	for attempt := 0; ; attempt++ {
		outbound, err := send(attempt)
		if err != nil {
			return nil, err
		}
		response, err := client.Do(outbound)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if request.GetBody == nil || retries[http.StatusBadGateway] >= baseNetworkRetries {
				return nil, err
			}
			retries[http.StatusBadGateway]++
			if err := sleep(ctx, baseRetry[http.StatusBadGateway].delay); err != nil {
				return nil, err
			}
			continue
		}
		retry, ok := baseRetry[response.StatusCode]
		if !ok || request.GetBody == nil || retries[response.StatusCode] >= retry.attempts {
			return response, nil
		}
		retries[response.StatusCode]++
		// The attempt is over: release the connection before waiting.
		response.Body.Close()
		if err := sleep(ctx, retry.delay); err != nil {
			return nil, err
		}
	}
}

// sleepContext waits for d, or ends early when the client is gone.
func sleepContext(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// geminiCLIExchange is GeminiCLIExecutor's use of BaseExecutor.execute
// (open-sse/executors/gemini-cli.js:5-54 has no execute override), so a
// 502/503/504 or a transport error is retried on the same URL.
func geminiCLIExchange(client *http.Client, request *http.Request) (*http.Response, error) {
	return exchangeWithRetry(request.Context(), client, request, geminiCLISleep)
}

// geminiCLISleep is the retry delay, replaced by tests.
var geminiCLISleep = sleepContext
