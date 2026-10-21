package telemetry

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

func TestLiveQueueLimitPreservesAcceptedEvents(t *testing.T) {
	var current atomic.Pointer[Options]
	opts := Options{Enabled: true, MaxRecords: 3, BatchSize: 3, FlushInterval: time.Hour, EnqueueTimeout: time.Millisecond}
	current.Store(&opts)
	service := New(&recordingSink{}, opts)
	service.OptionsSource = func() Options { return *current.Load() }
	for i := 0; i < 3; i++ {
		service.Record(Event{Class: Critical})
	}
	smaller := opts
	smaller.MaxRecords = 1
	current.Store(&smaller)
	service.Record(Event{Class: Critical})
	if service.depth() != 3 || service.Lost() != 1 {
		t.Fatalf("shrink discarded accepted events or ignored bound: depth=%d lost=%d", service.depth(), service.Lost())
	}
	larger := opts
	larger.MaxRecords = 4
	current.Store(&larger)
	service.Record(Event{Class: Critical})
	if service.depth() != 4 || service.Lost() != 1 {
		t.Fatal("increased queue bound ignored")
	}
}

func TestLiveBatchThresholdAndInterval(t *testing.T) {
	var current atomic.Pointer[Options]
	opts := Options{Enabled: true, MaxRecords: 10, BatchSize: 3, FlushInterval: time.Hour, EnqueueTimeout: time.Millisecond}
	current.Store(&opts)
	sink := &recordingSink{}
	service := New(sink, opts)
	service.OptionsSource = func() Options { return *current.Load() }
	ctx, cancel := context.WithCancel(context.Background())
	go service.Run(ctx)
	defer func() { cancel(); service.Wait() }()
	service.Record(Event{Class: Critical})
	time.Sleep(20 * time.Millisecond)
	if sink.count() != 0 {
		t.Fatal("partial batch flushed before its interval")
	}
	changed := opts
	changed.BatchSize = 2
	current.Store(&changed)
	service.Record(Event{Class: Critical})
	deadline := time.After(time.Second)
	for sink.count() != 2 {
		select {
		case <-deadline:
			t.Fatal("live batch threshold ignored")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	changed.FlushInterval = time.Millisecond
	current.Store(&changed)
	service.Record(Event{Class: Critical})
	deadline = time.After(time.Second)
	for sink.count() != 3 {
		select {
		case <-deadline:
			t.Fatal("live partial-batch interval ignored")
		default:
			time.Sleep(time.Millisecond)
		}
	}
}

func TestQueuedEventsKeepAcceptanceTime(t *testing.T) {
	service := New(&recordingSink{}, DefaultOptions()).WithDetails(&recordingDetails{})
	before := time.Now().UTC()
	service.Record(Event{RequestID: "usage"})
	service.RecordDetail(Detail{RequestID: "detail"})
	after := time.Now().UTC()
	explicit := before.Add(-time.Hour)
	service.Record(Event{RequestID: "explicit", CreatedAt: explicit})
	events := service.takeBatch(3)
	for _, event := range events[:2] {
		if event.CreatedAt.Before(before) || event.CreatedAt.After(after) {
			t.Fatalf("acceptance timestamp=%s", event.CreatedAt)
		}
		if event.detail != nil && !event.detail.CreatedAt.Equal(event.CreatedAt) {
			t.Fatal("detail timestamp differs from acceptance")
		}
	}
	if !events[2].CreatedAt.Equal(explicit) {
		t.Fatal("explicit timestamp replaced")
	}
}
