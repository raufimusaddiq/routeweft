// Package telemetry owns bounded asynchronous Usage accounting and diagnostics
// (SPEC §21, BDR-013). The inference path only enqueues; a background batcher
// writes SQLite, so a normal success never synchronously writes storage.
package telemetry

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// Class separates protected accounting from shed-able diagnostics.
type Class uint8

const (
	// Critical is request completion/failure, attribution, token usage, cost
	// inputs and route outcome. It is never silently dropped.
	Critical Class = iota
	// Diagnostic is verbose detail (large bodies, transport detail, transform
	// diagnostics). It is shed first under pressure.
	Diagnostic
)

// Event is one usage/accounting record.
type Event struct {
	Class        Class
	RequestID    string
	ProviderID   string
	ModelID      string
	ConnectionID string
	APIKeyID     string
	Status       int
	InputTokens  int64
	OutputTokens int64
	CacheRead    int64
	CacheWrite   int64
	DurationMS   int64
	TTFTMS       int64
	RouteOutcome string
	CreatedAt    time.Time
}

// Sink persists one batch of events. Implementations must tolerate an empty
// batch and must be safe to call from the single batcher goroutine.
type Sink interface {
	WriteUsageEvents(ctx context.Context, events []Event) error
}

// Options configure the bounded queue and batcher.
type Options struct {
	// MaxRecords bounds the in-memory queue depth.
	MaxRecords int
	// BatchSize is the maximum events written per flush.
	BatchSize int
	// FlushInterval is the maximum delay before a partial batch is flushed.
	FlushInterval time.Duration
	// EnqueueTimeout bounds critical backpressure when the queue is full.
	EnqueueTimeout time.Duration
	// Enabled is false to run as a no-op (observability disabled).
	Enabled bool
}

// Default options mirror the compiled observability settings.
func DefaultOptions() Options {
	return Options{MaxRecords: 1000, BatchSize: 20, FlushInterval: 5 * time.Second, EnqueueTimeout: 50 * time.Millisecond, Enabled: true}
}

// ErrDegraded is reported by Health when persistent storage failure has marked
// the telemetry writer unhealthy.
var ErrDegraded = errors.New("telemetry writer degraded")

// Service is the bounded async telemetry writer.
type Service struct {
	queue chan Event
	sink  Sink
	opts  Options

	lost     atomic.Uint64
	written  atomic.Uint64
	degraded atomic.Bool

	closeOnce sync.Once
	closed    atomic.Bool
	flushNow  chan chan struct{}
}

// New builds a telemetry service. A nil sink with Enabled=true is rejected by
// Start rather than silently losing accounting.
func New(sink Sink, opts Options) *Service {
	if opts.MaxRecords <= 0 {
		opts.MaxRecords = DefaultOptions().MaxRecords
	}
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultOptions().BatchSize
	}
	if opts.FlushInterval <= 0 {
		opts.FlushInterval = DefaultOptions().FlushInterval
	}
	if opts.EnqueueTimeout <= 0 {
		opts.EnqueueTimeout = DefaultOptions().EnqueueTimeout
	}
	return &Service{queue: make(chan Event, opts.MaxRecords), sink: sink, opts: opts, flushNow: make(chan chan struct{})}
}

// Record enqueues one event. Critical events get short bounded backpressure and
// then, if still full, an emergency inline flush; diagnostic events are shed
// first. A lost critical event increments the visible lost counter.
func (s *Service) Record(event Event) {
	if !s.opts.Enabled || s.closed.Load() {
		return
	}
	if s.tryEnqueue(event) {
		return
	}
	if event.Class == Diagnostic {
		// Diagnostic events shed first and are not counted as lost accounting.
		return
	}
	// Critical backpressure: wait briefly for room.
	timer := time.NewTimer(s.opts.EnqueueTimeout)
	defer timer.Stop()
	select {
	case s.queue <- event:
		return
	case <-timer.C:
	}
	// Emergency: drop the oldest diagnostic to make room, else count the loss.
	if s.evictDiagnostic() {
		if s.tryEnqueue(event) {
			return
		}
	}
	s.lost.Add(1)
}

func (s *Service) tryEnqueue(event Event) bool {
	select {
	case s.queue <- event:
		return true
	default:
		return false
	}
}

// evictDiagnostic removes one queued diagnostic event to make room for a
// critical one, preserving critical ordering. It reports whether it evicted.
func (s *Service) evictDiagnostic() bool {
	for i := 0; i < cap(s.queue); i++ {
		select {
		case event := <-s.queue:
			if event.Class == Diagnostic {
				return true
			}
			// Preserve critical events, but only if there is room to re-queue.
			select {
			case s.queue <- event:
			default:
				return true
			}
		default:
			return false
		}
	}
	return false
}

// Run drains the queue in batches until ctx is cancelled or Close is called.
// It flushes any remaining events before returning.
func (s *Service) Run(ctx context.Context) {
	if !s.opts.Enabled {
		return
	}
	ticker := time.NewTicker(s.opts.FlushInterval)
	defer ticker.Stop()
	pending := make([]Event, 0, s.opts.BatchSize)
	flush := func() {
		if len(pending) == 0 {
			return
		}
		s.flush(pending)
		pending = pending[:0]
	}
	for {
		select {
		case <-ctx.Done():
			pending = s.drain(pending)
			flush()
			return
		case done := <-s.flushNow:
			pending = s.drain(pending)
			flush()
			close(done)
		case event := <-s.queue:
			pending = append(pending, event)
			if len(pending) >= s.opts.BatchSize {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// drain empties the queue into pending (bounded by MaxRecords) for a final flush.
func (s *Service) drain(pending []Event) []Event {
	for {
		select {
		case event := <-s.queue:
			pending = append(pending, event)
		default:
			return pending
		}
	}
}

func (s *Service) flush(events []Event) {
	if s.sink == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.sink.WriteUsageEvents(ctx, events); err != nil {
		// Persistent failure marks degraded health and counts the loss; the queue
		// keeps accepting so a transient failure does not silently lose the kind.
		s.degraded.Store(true)
		s.lost.Add(uint64(len(events)))
		return
	}
	s.degraded.Store(false)
	s.written.Add(uint64(len(events)))
}

// FlushNow requests one synchronous flush of queued events. It blocks until the
// batcher has written them or ctx expires. It is used for emergency/direct flush
// and by tests.
func (s *Service) FlushNow(ctx context.Context) error {
	if !s.opts.Enabled || s.closed.Load() {
		return nil
	}
	done := make(chan struct{})
	select {
	case s.flushNow <- done:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Close stops accepting new events. Run must already be draining for a clean
// shutdown; the process closes the context first, then calls Close.
func (s *Service) Close() {
	s.closeOnce.Do(func() { s.closed.Store(true) })
}

// Health reports nil while healthy, or ErrDegraded after a persistent storage
// failure (SPEC §21 point 4).
func (s *Service) Health() error {
	if !s.opts.Enabled {
		return nil
	}
	if s.degraded.Load() {
		return ErrDegraded
	}
	return nil
}

// Lost reports the monotonic count of critical accounting records that could
// not be persisted (SPEC §21 point 5).
func (s *Service) Lost() uint64 { return s.lost.Load() }

// Written reports the monotonic count of persisted events.
func (s *Service) Written() uint64 { return s.written.Load() }

// Enabled reports whether the service performs any work.
func (s *Service) Enabled() bool { return s.opts.Enabled }
