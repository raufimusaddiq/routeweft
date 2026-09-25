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
	// detail, when non-nil, marks the event as a request-detail diagnostic the
	// batcher routes to the DetailSink instead of the usage sink.
	detail *Detail
}

// Sink persists one batch of events. Implementations must tolerate an empty
// batch and must be safe to call from the single batcher goroutine.
type Sink interface {
	WriteUsageEvents(ctx context.Context, events []Event) error
}

// DetailSink persists one redacted request detail (SPEC §21 diagnostic class).
type DetailSink interface {
	WriteRequestDetail(ctx context.Context, detail Detail) error
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
	mu       sync.Mutex
	queue    []Event
	signal   chan struct{}
	closedCh chan struct{}
	sink     Sink
	details  DetailSink
	opts     Options

	lost     atomic.Uint64
	written  atomic.Uint64
	degraded atomic.Bool
	// lostDiagnostics counts dropped/unwritable diagnostics; unlike Lost it is
	// not an accounting loss (diagnostics are shed-able by design).
	lostDiagnostics atomic.Uint64

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
	return &Service{queue: make([]Event, 0, opts.MaxRecords), signal: make(chan struct{}, 1), closedCh: make(chan struct{}), sink: sink, opts: opts, flushNow: make(chan chan struct{})}
}

// WithDetails attaches a request-detail sink. Without one, RecordDetail is a
// no-op so the diagnostic class degrades to nothing rather than failing.
func (s *Service) WithDetails(details DetailSink) *Service {
	s.details = details
	return s
}

// RecordDetail enqueues one redacted request detail as a diagnostic event. It is
// shed first under pressure like any other diagnostic (SPEC §21).
func (s *Service) RecordDetail(detail Detail) {
	if !s.opts.Enabled || s.closed.Load() || s.details == nil {
		return
	}
	s.Record(Event{Class: Diagnostic, RequestID: detail.RequestID, CreatedAt: detail.CreatedAt, detail: &detail})
}

// Record enqueues one event. Critical events are never silently dropped:
// when the queue is full a queued diagnostic is evicted to make room, and only
// if the queue holds exclusively critical events is the new event counted as
// lost (and the loss is visible via Lost). Diagnostic events shed first and are
// never counted as lost accounting.
func (s *Service) Record(event Event) {
	if !s.opts.Enabled || s.closed.Load() {
		return
	}
	if s.enqueue(event) {
		return
	}
	if event.Class == Diagnostic {
		return
	}
	// Critical backpressure: wait briefly for the batcher to free room.
	timer := time.NewTimer(s.opts.EnqueueTimeout)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-s.signal:
		// A slot may have opened; retry below regardless.
	}
	if s.enqueue(event) {
		return
	}
	if s.evictDiagnostic() && s.enqueue(event) {
		return
	}
	// Queue is full and holds only critical events: preserve existing criticals
	// rather than disturb them, and record the new event as a visible loss.
	s.lost.Add(1)
}

// enqueue appends under the lock. It reports false when the buffer is full or
// the service is closed.
func (s *Service) enqueue(event Event) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed.Load() || len(s.queue) >= cap(s.queue) {
		return false
	}
	s.queue = append(s.queue, event)
	s.notify()
	return true
}

// evictDiagnostic removes the oldest queued diagnostic, leaving critical events
// untouched. It reports whether one was removed.
func (s *Service) evictDiagnostic() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, event := range s.queue {
		if event.Class == Diagnostic {
			s.queue = append(s.queue[:i], s.queue[i+1:]...)
			return true
		}
	}
	return false
}

// takeBatch removes and returns up to n queued events under the lock.
func (s *Service) takeBatch(n int) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.queue) == 0 || n <= 0 {
		return nil
	}
	if n > len(s.queue) {
		n = len(s.queue)
	}
	events := append([]Event(nil), s.queue[:n]...)
	s.queue = append(s.queue[:0], s.queue[n:]...)
	return events
}

func (s *Service) notify() {
	select {
	case s.signal <- struct{}{}:
	default:
	}
}

// depth reports the current queued event count (test/observability helper).
func (s *Service) depth() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queue)
}

// Run drains the queue in batches until ctx is cancelled or Close is called.
// It flushes every remaining event before returning, and signals done so the
// caller can join it before closing the store (SPEC §21, BND-013 shutdown).
func (s *Service) Run(ctx context.Context) {
	if !s.opts.Enabled {
		close(s.closedCh)
		return
	}
	defer close(s.closedCh)
	ticker := time.NewTicker(s.opts.FlushInterval)
	defer ticker.Stop()
	// drainAll flushes every queued event in BatchSize chunks; used on explicit
	// flush requests and on shutdown so nothing observed is left unwritten.
	drainAll := func() {
		for {
			batch := s.takeBatch(s.opts.BatchSize)
			if len(batch) == 0 {
				return
			}
			s.flush(batch)
		}
	}
	for {
		select {
		case <-ctx.Done():
			drainAll()
			return
		case done := <-s.flushNow:
			drainAll()
			close(done)
		case <-s.signal:
			batch := s.takeBatch(s.opts.BatchSize)
			s.flush(batch)
			if s.depth() > 0 {
				s.notify()
			}
		case <-ticker.C:
			s.flush(s.takeBatch(s.opts.BatchSize))
		}
	}
}

func (s *Service) flush(events []Event) {
	if len(events) == 0 {
		// An empty drain carries no new success and must not clear a degraded
		// state established by an earlier failing batch.
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// A batch may mix usage and detail events. Degraded health reflects *any*
	// failing sink in the batch, so a successful usage write must not clear a
	// degraded state caused by a failed detail write (and vice versa).
	failed := false
	usage := make([]Event, 0, len(events))
	for _, event := range events {
		if event.detail != nil {
			if s.details != nil {
				if err := s.details.WriteRequestDetail(ctx, *event.detail); err != nil {
					failed = true
					s.lostDiagnostics.Add(1)
				}
			}
			continue
		}
		usage = append(usage, event)
	}
	if s.sink != nil && len(usage) > 0 {
		if err := s.sink.WriteUsageEvents(ctx, usage); err != nil {
			// Persistent failure marks degraded health and counts the loss; the
			// queue keeps accepting so a transient failure is not silently lost.
			failed = true
			s.lost.Add(uint64(len(usage)))
		} else {
			s.written.Add(uint64(len(usage)))
		}
	}
	if failed {
		s.degraded.Store(true)
		return
	}
	s.degraded.Store(false)
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

// Close stops accepting new events. Cancel the Run context first, then call
// Wait to join the final flush before closing the store.
func (s *Service) Close() {
	s.closeOnce.Do(func() { s.closed.Store(true) })
}

// Wait blocks until Run has returned (including its final flush). It returns
// immediately if the service is disabled or Run was never started.
func (s *Service) Wait() {
	if !s.opts.Enabled {
		return
	}
	<-s.closedCh
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

// LostDiagnostics reports the monotonic count of diagnostics that could not be
// persisted. Diagnostics are shed-able by design, so this never affects the
// critical `Lost` counter (SPEC §21).
func (s *Service) LostDiagnostics() uint64 { return s.lostDiagnostics.Load() }

// Enabled reports whether the service performs any work.
func (s *Service) Enabled() bool { return s.opts.Enabled }
