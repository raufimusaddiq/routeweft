package wire

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func retryRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	request, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.invalid/generate", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return request
}

func retryResponse(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}
}

// The oracle retries 502 three times at 3 s, 503 three times at 2 s and 504
// twice at 3 s, on the same URL (runtimeConfig.js:80-87, base.js:100-180).
func TestBaseRetryStatusesAndDelaysMatchTheOracle(t *testing.T) {
	for _, c := range []struct {
		status  int
		retries int
		delay   time.Duration
	}{
		{http.StatusBadGateway, 3, 3 * time.Second},
		{http.StatusServiceUnavailable, 3, 2 * time.Second},
		{http.StatusGatewayTimeout, 2, 3 * time.Second},
	} {
		calls := 0
		var sleeps []time.Duration
		client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			payload, _ := io.ReadAll(r.Body)
			if string(payload) != `{"model":"m"}` {
				t.Errorf("%d: body replay %q", c.status, payload)
			}
			if calls <= c.retries {
				return retryResponse(c.status, "again"), nil
			}
			return retryResponse(http.StatusOK, "ok"), nil
		})}
		response, err := exchangeWithRetry(context.Background(), client, retryRequest(t, `{"model":"m"}`), func(_ context.Context, delay time.Duration) error {
			sleeps = append(sleeps, delay)
			return nil
		})
		if err != nil || response.StatusCode != http.StatusOK {
			t.Fatalf("%d: response %v err %v", c.status, response, err)
		}
		if calls != c.retries+1 || len(sleeps) != c.retries {
			t.Fatalf("%d: calls=%d sleeps=%v", c.status, calls, sleeps)
		}
		for _, delay := range sleeps {
			if delay != c.delay {
				t.Fatalf("%d: delay %v, want %v", c.status, delay, c.delay)
			}
		}
		response.Body.Close()
	}
}

// A network failure is the 502 entry's retries, and the last answer is returned
// once they are spent, not turned into an error.
func TestBaseRetryNetworkAndExhaustion(t *testing.T) {
	calls := 0
	var sleeps []time.Duration
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		if calls <= 3 {
			return nil, errors.New("connection reset")
		}
		return retryResponse(http.StatusOK, "ok"), nil
	})}
	response, err := exchangeWithRetry(context.Background(), client, retryRequest(t, `{}`), func(_ context.Context, delay time.Duration) error {
		sleeps = append(sleeps, delay)
		return nil
	})
	if err != nil || response.StatusCode != http.StatusOK || calls != 4 || len(sleeps) != 3 {
		t.Fatalf("network retry: calls=%d sleeps=%v response=%v err=%v", calls, sleeps, response, err)
	}
	if sleeps[0] != 3*time.Second {
		t.Fatalf("network delay %v", sleeps[0])
	}
	response.Body.Close()

	calls = 0
	sleeps = nil
	client = &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return retryResponse(http.StatusServiceUnavailable, "down"), nil
	})}
	response, err = exchangeWithRetry(context.Background(), client, retryRequest(t, `{}`), func(_ context.Context, delay time.Duration) error {
		sleeps = append(sleeps, delay)
		return nil
	})
	if err != nil || response.StatusCode != http.StatusServiceUnavailable || calls != 4 || len(sleeps) != 3 {
		t.Fatalf("exhaustion: calls=%d sleeps=%v response=%v err=%v", calls, sleeps, response, err)
	}
	response.Body.Close()
}

// A request that cannot be replayed is not retried blind.
func TestBaseRetryNeedsAReplayableBody(t *testing.T) {
	calls := 0
	request := retryRequest(t, `{}`)
	request.GetBody = nil
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return retryResponse(http.StatusBadGateway, "no"), nil
	})}
	response, err := exchangeWithRetry(context.Background(), client, request, func(context.Context, time.Duration) error { return nil })
	if err != nil || response.StatusCode != http.StatusBadGateway || calls != 1 {
		t.Fatalf("calls=%d response=%v err=%v", calls, response, err)
	}
	response.Body.Close()
}

// Cancellation ends the wait instead of retrying behind a client that is gone.
func TestBaseRetryStopsWhenTheClientLeaves(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return retryResponse(http.StatusBadGateway, "again"), nil
	})}
	request := retryRequest(t, `{}`).WithContext(ctx)
	_, err := exchangeWithRetry(ctx, client, request, func(ctx context.Context, _ time.Duration) error {
		cancel()
		return ctx.Err()
	})
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

// Both providers with no execute override use the base exchange, and the
// gemini-cli seam actually retries a 503 on the same URL.
func TestBaseRetryIsWiredToItsExecutors(t *testing.T) {
	if For("gemini-cli").Do == nil {
		t.Fatal("gemini-cli does not use the base exchange")
	}
	if For("commandcode").Do == nil {
		t.Fatal("commandcode does not use its wrapper exchange")
	}
	previous := geminiCLISleep
	geminiCLISleep = func(context.Context, time.Duration) error { return nil }
	t.Cleanup(func() { geminiCLISleep = previous })
	for _, status := range []int{http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout} {
		calls := 0
		client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
			calls++
			if calls == 1 {
				return retryResponse(status, "again"), nil
			}
			return retryResponse(http.StatusOK, "ok"), nil
		})}
		response, err := geminiCLIExchange(client, retryRequest(t, `{}`))
		if err != nil || response.StatusCode != http.StatusOK || calls != 2 {
			t.Fatalf("%d: calls=%d response=%v err=%v", status, calls, response, err)
		}
		response.Body.Close()
	}
}
