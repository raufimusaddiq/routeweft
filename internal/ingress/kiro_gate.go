package ingress

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	kiroadapter "github.com/raufimusaddiq/routeweft/internal/protocol/kiro"
	"github.com/raufimusaddiq/routeweft/internal/providers/wire"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// Kiro's integrity gate (executors/kiro.js:370-627) holds the whole answer until it
// is known to be usable, and repeats the request once when it is not. The wire
// attempt that produced the answer is the only place that can build that request,
// so dispatch leaves the means on the request's context for the relay to find.

type repairKey struct{}

// repairFunc sends the one repair request for the attempt a response answered.
type repairFunc func(ctx context.Context, kind string) (io.ReadCloser, int, error)

func withRepair(request *http.Request, repair repairFunc) *http.Request {
	return request.WithContext(context.WithValue(request.Context(), repairKey{}, repair))
}

// repairOf returns the repair request the response's attempt left, or nil.
func repairOf(response *http.Response) repairFunc {
	if response == nil || response.Request == nil {
		return nil
	}
	repair, _ := response.Request.Context().Value(repairKey{}).(repairFunc)
	return repair
}

// The repair request is BaseExecutor.prototype.execute (executors/kiro.js:459-463,
// executors/base.js:100-184), not the attempt again: it starts at the first host,
// retries a host in place on 502/503/504 (and a network error as a 502) with the
// pin's backoff, moves to the next host on 429 and on 401/403/404 while one
// remains, and otherwise returns whatever answered. The in-place counter is one
// per host, shared by the statuses (base.js:104, :113).
var repairBackoff = map[int]struct {
	attempts int
	delay    time.Duration
}{
	http.StatusBadGateway:         {3, 3 * time.Second},
	http.StatusServiceUnavailable: {3, 2 * time.Second},
	http.StatusGatewayTimeout:     {2, 3 * time.Second},
}

// repairSleep is the in-place retry delay; tests replace it.
var repairSleep = func(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (h *Handler) kiroRepairRequest(resolve func(index int) (wire.Target, error), build func(body []byte, target wire.Target) (*http.Request, error), send func(*http.Request) (*http.Response, error), body []byte) repairFunc {
	return func(ctx context.Context, kind string) (io.ReadCloser, int, error) {
		repaired, err := kiroadapter.RepairRequest(body, kind)
		if err != nil {
			return nil, 0, err
		}
		inPlace := map[int]int{}
		retry := func(index, status int) (bool, error) {
			policy, set := repairBackoff[status]
			if !set || inPlace[index] >= policy.attempts {
				return false, nil
			}
			inPlace[index]++
			return true, repairSleep(ctx, policy.delay)
		}
		count, lastErr := 1, error(nil)
		for index := 0; index < count; index++ {
			target, err := resolve(index)
			if err != nil {
				return nil, 0, err
			}
			if target.EndpointCount > 0 {
				count = target.EndpointCount
			}
			request, err := build(repaired, target)
			if err != nil {
				return nil, 0, err
			}
			response, err := send(request.WithContext(ctx))
			if err != nil {
				if ctx.Err() != nil {
					return nil, 0, ctx.Err()
				}
				lastErr = err
				again, waitErr := retry(index, http.StatusBadGateway)
				if waitErr != nil {
					return nil, 0, waitErr
				}
				if again {
					index--
					continue
				}
				if index+1 < count {
					continue
				}
				return nil, 0, err
			}
			again, waitErr := retry(index, response.StatusCode)
			if waitErr != nil {
				response.Body.Close()
				return nil, 0, waitErr
			}
			if again {
				response.Body.Close()
				index--
				continue
			}
			status := response.StatusCode
			if index+1 < count && (status == http.StatusTooManyRequests || status == 401 || status == 403 || status == 404) {
				response.Body.Close()
				continue
			}
			return response.Body, status, nil
		}
		if lastErr != nil {
			return nil, 0, lastErr
		}
		return nil, 0, fmt.Errorf("all %d URLs failed", count)
	}
}

// The pin's integrity environment (executors/kiro.js:372-377), under the
// Routeweft prefix.
const (
	envKiroRepair      = "ROUTEWEFT_KIRO_TOOL_CALL_REPAIR"
	envKiroBufferBytes = "ROUTEWEFT_KIRO_TOOL_CALL_REPAIR_BUFFER_MAX_BYTES"
	envKiroTimeoutMS   = "ROUTEWEFT_KIRO_TOOL_CALL_REPAIR_TIMEOUT_MS"
	envKiroTTFTMS      = "ROUTEWEFT_KIRO_TOOL_CALL_REPAIR_TTFT_TIMEOUT_MS"
	envKiroStallMS     = "ROUTEWEFT_KIRO_TOOL_CALL_REPAIR_STALL_TIMEOUT_MS"
)

// kiroHeartbeatEvery is the pin's 10 s (executors/kiro.js:15); zero keeps it. Tests shorten it.
var kiroHeartbeatEvery time.Duration

func envPositive(name string) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(name)))
	if err != nil || value <= 0 {
		return 0
	}
	return value
}

// kiroGateConfig is executors/kiro.js:372-377. The repair is on unless the
// connection's kiroToolCallRepair or the environment says "false". The legacy
// timeout is the default of both phase timeouts.
func kiroGateConfig(provider routing.ProviderRef) kiroadapter.GateConfig {
	off := func(value string) bool { return strings.EqualFold(strings.TrimSpace(value), "false") }
	legacy := time.Duration(envPositive(envKiroTimeoutMS)) * time.Millisecond
	ttft, stall := legacy, legacy
	if value := envPositive(envKiroTTFTMS); value > 0 {
		ttft = time.Duration(value) * time.Millisecond
	}
	if value := envPositive(envKiroStallMS); value > 0 {
		stall = time.Duration(value) * time.Millisecond
	}
	return kiroadapter.GateConfig{
		Repair:   !off(provider.Connection.Settings.KiroToolCallRepair) && !off(os.Getenv(envKiroRepair)),
		MaxBytes: envPositive(envKiroBufferBytes),
		TTFT:     ttft,
		Stall:    stall,

		HeartbeatEvery: kiroHeartbeatEvery,
	}
}

type failedReader struct{ err error }

func (r failedReader) Read([]byte) (int, error) { return 0, r.err }

// gateAnswer runs the gate on a 2xx Kiro answer. beat may be nil. A failure is
// returned as the reader the response leg will read, so each client wire reports
// it in its own shape; only cancellation is returned as an error.
func (h *Handler) gateAnswer(ctx context.Context, response *http.Response, provider routing.ProviderRef, options kiroadapter.ResponseOptions, beat func()) (io.Reader, error) {
	var retry kiroadapter.Retry
	if repair := repairOf(response); repair != nil {
		retry = kiroadapter.Retry(repair)
	}
	gated, err := kiroadapter.Gate(ctx, response.Body, kiroGateConfig(provider), options, retry, beat)
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if err != nil {
		return failedReader{err}, nil
	}
	return gated, nil
}

// kiroHeartbeat is the comment the pin sends while it validates (executors/kiro.js:388).
func kiroHeartbeat(w http.ResponseWriter) func() {
	flusher, canFlush := w.(http.Flusher)
	return func() {
		if _, err := fmt.Fprint(w, ": kiro-validation\n\n"); err == nil && canFlush {
			flusher.Flush()
		}
	}
}
