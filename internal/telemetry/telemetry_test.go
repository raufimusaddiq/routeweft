package telemetry

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

type recordingSink struct {
	mu     sync.Mutex
	events []Event
	err    error
}

func (s *recordingSink) WriteUsageEvents(_ context.Context, events []Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.events = append(s.events, events...)
	return nil
}

func (s *recordingSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

func TestServiceBatchesAndFlushesCriticalEvents(t *testing.T) {
	sink := &recordingSink{}
	service := New(sink, Options{MaxRecords: 8, BatchSize: 2, FlushInterval: time.Hour, EnqueueTimeout: time.Millisecond, Enabled: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go service.Run(ctx)
	for i := 0; i < 5; i++ {
		service.Record(Event{Class: Critical, RequestID: "r", ProviderID: "p", ModelID: "m", Status: 200, InputTokens: 1})
	}
	if err := service.FlushNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := sink.count(); got != 5 {
		t.Fatalf("written=%d want 5", got)
	}
	if service.Lost() != 0 || service.Health() != nil {
		t.Fatalf("lost=%d health=%v", service.Lost(), service.Health())
	}
}

func TestServiceShedsDiagnosticsBeforeCritical(t *testing.T) {
	// No Run: nothing drains, so the bounded queue fills deterministically.
	service := New(&recordingSink{}, Options{MaxRecords: 2, BatchSize: 10, FlushInterval: time.Hour, EnqueueTimeout: time.Millisecond, Enabled: true})
	service.Record(Event{Class: Diagnostic, RequestID: "d1"})
	service.Record(Event{Class: Diagnostic, RequestID: "d2"})
	service.Record(Event{Class: Diagnostic, RequestID: "d3"})
	if service.Lost() != 0 {
		t.Fatalf("diagnostic shedding must not count as lost critical accounting: %d", service.Lost())
	}
	service.Record(Event{Class: Critical, RequestID: "c1"})
	if service.Lost() != 0 {
		t.Fatalf("critical event should evict a diagnostic, lost=%d", service.Lost())
	}
	// Fill the queue with critical events, then force a loss.
	service.Record(Event{Class: Critical, RequestID: "c2"})
	service.Record(Event{Class: Critical, RequestID: "c3"})
	if service.Lost() == 0 {
		t.Fatal("expected lost critical accounting under saturation")
	}
}

func TestEvictionNeverDropsQueuedCriticalEvents(t *testing.T) {
	sink := &recordingSink{}
	service := New(sink, Options{MaxRecords: 4, BatchSize: 100, FlushInterval: time.Hour, EnqueueTimeout: time.Millisecond, Enabled: true})
	// Fill with critical events, then flood with more critical events while a
	// concurrent producer adds diagnostics that must be the ones evicted.
	for i := 0; i < 4; i++ {
		service.Record(Event{Class: Critical, RequestID: "kept"})
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			service.Record(Event{Class: Diagnostic, RequestID: "shed"})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 50; i++ {
			service.Record(Event{Class: Critical, RequestID: "overflow"})
		}
	}()
	wg.Wait()
	// Every queued event must still be critical: no diagnostic should have
	// displaced a critical one, and the queue never exceeds its bound.
	if service.depth() > 4 {
		t.Fatalf("queue exceeded bound: %d", service.depth())
	}
	for _, event := range service.takeBatch(100) {
		if event.Class != Critical {
			t.Fatalf("diagnostic survived while criticals were present: %+v", event)
		}
	}
}

func TestRunFlushesOnShutdown(t *testing.T) {
	sink := &recordingSink{}
	service := New(sink, Options{MaxRecords: 64, BatchSize: 8, FlushInterval: time.Hour, EnqueueTimeout: time.Millisecond, Enabled: true})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { service.Run(ctx); close(done) }()
	for i := 0; i < 20; i++ {
		service.Record(Event{Class: Critical, RequestID: "r", Status: 200})
	}
	cancel()
	if err := service.FlushNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancellation")
	}
	service.Wait()
	if got := sink.count(); got != 20 {
		t.Fatalf("written=%d want 20", got)
	}
}

func TestServiceMarksDegradedOnPersistentFailure(t *testing.T) {
	sink := &recordingSink{err: errors.New("disk full")}
	service := New(sink, Options{MaxRecords: 4, BatchSize: 1, FlushInterval: time.Hour, EnqueueTimeout: time.Millisecond, Enabled: true})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go service.Run(ctx)
	service.Record(Event{Class: Critical, RequestID: "r", Status: 200})
	if err := service.FlushNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(service.Health(), ErrDegraded) {
		t.Fatalf("health=%v want degraded", service.Health())
	}
	if service.Lost() == 0 {
		t.Fatal("lost counter did not advance on failed batch")
	}
}

func TestDisabledServiceIsNoOp(t *testing.T) {
	service := New(&recordingSink{}, Options{Enabled: false})
	service.Record(Event{Class: Critical, RequestID: "r"})
	if service.Lost() != 0 || service.Written() != 0 || service.Enabled() {
		t.Fatalf("disabled service performed work: %+v", service)
	}
}

type recordingDetails struct {
	mu      sync.Mutex
	written []Detail
}

func (d *recordingDetails) WriteRequestDetail(_ context.Context, detail Detail) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.written = append(d.written, detail)
	return nil
}

func TestRecordDetailRoutesToDetailSink(t *testing.T) {
	sink := &recordingSink{}
	details := &recordingDetails{}
	service := New(sink, Options{MaxRecords: 8, BatchSize: 4, FlushInterval: time.Hour, EnqueueTimeout: time.Millisecond, Enabled: true}).WithDetails(details)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go service.Run(ctx)
	service.RecordDetail(Detail{RequestID: "req_1", RouteMode: "native", Payload: map[string]any{"a": 1}})
	service.Record(Event{Class: Critical, RequestID: "req_1", Status: 200})
	if err := service.FlushNow(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(details.written) != 1 || details.written[0].RequestID != "req_1" {
		t.Fatalf("details=%+v", details.written)
	}
	if sink.count() != 1 {
		t.Fatalf("usage sink got %d events, want 1 (detail must not go to the usage sink)", sink.count())
	}
}

func TestRecordDetailIsNoOpWithoutDetailSink(t *testing.T) {
	service := New(&recordingSink{}, Options{MaxRecords: 4, BatchSize: 4, FlushInterval: time.Hour, Enabled: true})
	service.RecordDetail(Detail{RequestID: "req_1"})
	if service.depth() != 0 {
		t.Fatalf("detail without a sink should not enqueue: depth=%d", service.depth())
	}
}
