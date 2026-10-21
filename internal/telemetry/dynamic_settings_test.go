package telemetry_test

import (
	"context"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/runtime"
	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
	"github.com/raufimusaddiq/routeweft/internal/telemetry"
)

func TestLiveBatchLargerThanQueueDoesNotLoseCriticalEvents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "telemetry.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := migrations.NewRunner(store.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	manager, err := runtime.NewManagerObserved(ctx, store.DB(), nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	options := func() telemetry.Options {
		snapshot, _ := manager.Load()
		return snapshot.TelemetryOptions()
	}
	service := telemetry.New(telemetry.NewSQLiteSink(store.DB()), options())
	service.OptionsSource = options
	go service.Run(ctx)
	defer func() { cancel(); service.Wait() }()
	if err := service.FlushNow(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.SetSettings(ctx, map[string]string{
		"enableObservability":          "true",
		"observabilityMaxRecords":      "2",
		"observabilityBatchSize":       "50",
		"observabilityFlushIntervalMs": "60000",
	}, nil); err != nil {
		t.Fatal(err)
	}
	if options().BatchSize != 2 || manager.Settings()["observabilityBatchSize"] != "50" {
		t.Fatal("effective batch size must fit the queue without rewriting the saved setting")
	}
	for i := 0; i < 10; i++ {
		service.Record(telemetry.Event{Class: telemetry.Critical, RequestID: strconv.Itoa(i)})
	}
	deadline := time.After(3 * time.Second)
	for service.Written() != 10 && service.Lost() == 0 {
		select {
		case <-deadline:
			t.Fatalf("batch did not drain: written=%d lost=%d", service.Written(), service.Lost())
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if service.Written() == 0 || service.Lost() != 0 {
		t.Fatalf("critical accounting lost: written=%d lost=%d", service.Written(), service.Lost())
	}
}
