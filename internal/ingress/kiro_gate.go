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

// kiroRepairRequest builds the repair for one Kiro attempt: the same account, the
// same surface, the body with the repair instruction in its current user turn.
func (h *Handler) kiroRepairRequest(build func(body []byte) (*http.Request, error), send func(*http.Request) (*http.Response, error), body []byte) repairFunc {
	return func(ctx context.Context, kind string) (io.ReadCloser, int, error) {
		repaired, err := kiroadapter.RepairRequest(body, kind)
		if err != nil {
			return nil, 0, err
		}
		request, err := build(repaired)
		if err != nil {
			return nil, 0, err
		}
		request = request.WithContext(ctx)
		response, err := send(request)
		if err != nil {
			return nil, 0, err
		}
		return response.Body, response.StatusCode, nil
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
