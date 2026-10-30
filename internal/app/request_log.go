package app

import (
	"context"
	"log/slog"
	"sync/atomic"

	"github.com/raufimusaddiq/routeweft/internal/ingress"
)

// logRequest writes the operator log line of one finished request with the three
// names it went by: the model the client asked for, the provider/model the router
// selected, and the model the provider was called with (the reference's
// "▶ client model -> provider/model" line, chatCore). Attribute names avoid the
// words the console log redacts on sight ("token", "apikey"), and none of the
// values is a credential: the account and the client are opaque ids.
func logRequest(log *slog.Logger, outcome ingress.RequestOutcome) {
	level := slog.LevelInfo
	if outcome.Status >= 400 {
		level = slog.LevelWarn
	}
	attrs := []any{
		"requestId", outcome.RequestID,
		"requested", outcome.RequestedModel,
		"selected", outcome.SelectedModel,
		"upstream", outcome.ModelID,
		"provider", outcome.ProviderID,
		"account", outcome.ConnectionID,
		"client", outcome.APIKeyID,
		"status", outcome.Status,
		"stream", outcome.Stream,
		"durationMs", outcome.Duration.Milliseconds(),
		"ttftMs", outcome.TTFT.Milliseconds(),
		"in", outcome.InputTokens,
		"out", outcome.OutputTokens,
		"cacheRead", outcome.CacheRead,
		"cacheWrite", outcome.CacheWrite,
	}
	if outcome.Termination != "" {
		attrs = append(attrs, "termination", outcome.Termination)
	}
	if outcome.UsageUnobserved {
		attrs = append(attrs, "usageObserved", false)
	}
	log.Log(context.Background(), level, "request completed", attrs...)
}

// requestLogQueue is how many finished requests wait to be logged.
const requestLogQueue = 512

// requestLogger writes the request log line off the request path: the console log
// redacts and indexes every attribute, which costs far more than the line is worth
// to the client waiting for its answer. The queue is bounded and best-effort (a line
// is dropped when it is full, like diagnostics under queue pressure, SPEC 21); the
// drain goroutine exists only while there is something to write.
type requestLogger struct {
	log     *slog.Logger
	queue   chan ingress.RequestOutcome
	running atomic.Bool
}

func newRequestLogger(log *slog.Logger) *requestLogger {
	return &requestLogger{log: log, queue: make(chan ingress.RequestOutcome, requestLogQueue)}
}

// Log queues one finished request. The captured bodies are not kept: the line
// never uses them.
func (l *requestLogger) Log(outcome ingress.RequestOutcome) {
	outcome.Capture = nil
	select {
	case l.queue <- outcome:
	default:
		return
	}
	if l.running.CompareAndSwap(false, true) {
		go l.drain()
	}
}

func (l *requestLogger) drain() {
	for {
		select {
		case outcome := <-l.queue:
			logRequest(l.log, outcome)
		default:
			l.running.Store(false)
			if len(l.queue) == 0 || !l.running.CompareAndSwap(false, true) {
				return
			}
		}
	}
}
