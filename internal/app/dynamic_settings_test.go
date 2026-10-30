package app

import (
	"context"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/telemetry"
)

func TestObservabilitySettingsApplyWithoutRestart(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	a := New(Config{DataDir: t.TempDir()}, nil)
	if err := a.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	go a.usage.Run(ctx)
	defer func() { cancel(); a.usage.Wait(); a.store.Close() }()
	flush := func() {
		t.Helper()
		if err := a.usage.FlushNow(ctx); err != nil {
			t.Fatal(err)
		}
	}
	a.usage.Record(telemetry.Event{RequestID: "disabled"})
	flush()
	setSavers(t, a, map[string]string{"enableObservability": "true", "observabilityBatchSize": "2", "observabilityMaxRecords": "4", "observabilityFlushIntervalMs": "10", "observabilityMaxJsonSize": "1024"})
	a.usage.Record(telemetry.Event{RequestID: "enabled"})
	a.usage.RecordDetail(telemetry.Detail{RequestID: "detail", Payload: map[string]any{"text": strings.Repeat("x", 100), "authorization": "secret"}})
	flush()
	if a.usage.Written() != 1 {
		t.Fatalf("enable after disabled startup: written=%d", a.usage.Written())
	}
	read := func() string {
		t.Helper()
		var body string
		if err := a.store.DB().QueryRow("SELECT detail FROM request_details WHERE request_id='detail'").Scan(&body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	if body := read(); !strings.Contains(body, strings.Repeat("x", 100)) || strings.Contains(body, "secret") {
		t.Fatalf("detail=%s", body)
	}
	setSavers(t, a, map[string]string{"observabilityMaxJsonSize": "10"})
	// The setting bounds each captured field; a document may hold four of them and
	// a little attribution, so the whole-document ceiling is 4x the limit plus 2 KiB.
	a.usage.RecordDetail(telemetry.Detail{RequestID: "detail", Payload: map[string]any{"text": strings.Repeat("x", 3000)}})
	flush()
	if body := read(); !strings.Contains(body, "omitted") || strings.Contains(body, strings.Repeat("x", 100)) {
		t.Fatalf("live size cap ignored: %s", body)
	}
	a.usage.Record(telemetry.Event{RequestID: "accepted-before-disable"})
	setSavers(t, a, map[string]string{"enableObservability": "false"})
	a.usage.Record(telemetry.Event{RequestID: "disabled-again"})
	a.usage.RecordDetail(telemetry.Detail{RequestID: "disabled-detail"})
	flush()
	if a.usage.Written() != 2 || a.usage.Enabled() {
		t.Fatalf("disabled gate lost accepted accounting or recorded new work: written=%d", a.usage.Written())
	}
	var count int
	if err := a.store.DB().QueryRow("SELECT count(*) FROM request_details WHERE request_id='disabled-detail'").Scan(&count); err != nil || count != 0 {
		t.Fatalf("disabled detail count=%d err=%v", count, err)
	}
}
