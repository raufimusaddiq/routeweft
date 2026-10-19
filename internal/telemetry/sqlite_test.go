package telemetry

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

func TestSQLiteSinkPersistsEventsAndDailyRollup(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := migrations.NewRunner(store.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	sink := NewSQLiteSink(store.DB())
	now := time.Date(2026, 7, 3, 10, 0, 0, 0, time.UTC)
	events := []Event{
		{Class: Critical, RequestID: "r1", ProviderID: "openai", ModelID: "gpt", ConnectionID: "c1", Status: 200, InputTokens: 10, OutputTokens: 4, CacheRead: 3, CacheWrite: 1, DurationMS: 120, TTFTMS: 40, CreatedAt: now},
		{Class: Critical, RequestID: "r2", ProviderID: "openai", ModelID: "gpt", Status: 200, InputTokens: 5, OutputTokens: 2, CreatedAt: now},
		{Class: Critical, RequestID: "r3", ProviderID: "openai", ModelID: "gpt", Status: 500, InputTokens: 7, CreatedAt: now},
	}
	if err := sink.WriteUsageEvents(ctx, events); err != nil {
		t.Fatal(err)
	}
	var count int
	if err := store.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM usage_events").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 3 {
		t.Fatalf("usage_events=%d want 3", count)
	}
	var requests, input int64
	if err := store.DB().QueryRowContext(ctx, "SELECT requests,input_tokens FROM usage_daily WHERE day=? AND provider_id=? AND model_id=?", "2026-07-03", "openai", "gpt").Scan(&requests, &input); err != nil {
		t.Fatal(err)
	}
	if requests != 2 || input != 15 {
		t.Fatalf("usage_daily requests=%d input=%d want 2/15", requests, input)
	}
}

func TestSQLiteSinkEmptyBatchIsNoOp(t *testing.T) {
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := migrations.NewRunner(store.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	if err := NewSQLiteSink(store.DB()).WriteUsageEvents(ctx, nil); err != nil {
		t.Fatal(err)
	}
}
