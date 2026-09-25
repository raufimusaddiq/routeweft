package telemetry

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

func newDetailStore(t *testing.T, maxSize int) (*SQLiteDetailStore, *sqlite.Store) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.NewRunner(store.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	return NewSQLiteDetailStore(store.DB(), maxSize), store
}

func TestDetailStorePersistsRedactedDetail(t *testing.T) {
	ctx := context.Background()
	store, sqlStore := newDetailStore(t, 0)
	defer sqlStore.Close()
	err := store.WriteRequestDetail(ctx, Detail{RequestID: "req_1", RouteMode: "native", Payload: map[string]any{
		"provider":      "openai",
		"authorization": "Bearer leak",
	}})
	if err != nil {
		t.Fatal(err)
	}
	var detail, routeMode string
	if err := sqlStore.DB().QueryRowContext(ctx, "SELECT detail,route_mode FROM request_details WHERE request_id=?", "req_1").Scan(&detail, &routeMode); err != nil {
		t.Fatal(err)
	}
	if routeMode != "native" {
		t.Fatalf("route_mode=%s", routeMode)
	}
	if contains(detail, "Bearer leak") {
		t.Fatalf("credential leaked into stored detail: %s", detail)
	}
	if !contains(detail, "openai") {
		t.Fatalf("non-sensitive detail dropped: %s", detail)
	}
}

func TestDetailStoreDropsOversizedPayloadSafely(t *testing.T) {
	ctx := context.Background()
	store, sqlStore := newDetailStore(t, 64)
	defer sqlStore.Close()
	big := make([]any, 0, 100)
	for i := 0; i < 100; i++ {
		big = append(big, "filler-value")
	}
	if err := store.WriteRequestDetail(ctx, Detail{RequestID: "req_big", Payload: map[string]any{"blob": big, "authorization": "Bearer leak"}}); err != nil {
		t.Fatal(err)
	}
	var detail string
	if err := sqlStore.DB().QueryRowContext(ctx, "SELECT detail FROM request_details WHERE request_id=?", "req_big").Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if contains(detail, "Bearer leak") || !contains(detail, "exceeded") {
		t.Fatalf("oversized payload not safely replaced: %s", detail)
	}
}

func TestDetailStoreUpsertsAndPrunes(t *testing.T) {
	ctx := context.Background()
	store, sqlStore := newDetailStore(t, 0)
	defer sqlStore.Close()
	old := time.Now().AddDate(0, 0, -30)
	if err := store.WriteRequestDetail(ctx, Detail{RequestID: "req_old", CreatedAt: old, Payload: map[string]any{"a": 1}}); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteRequestDetail(ctx, Detail{RequestID: "req_new", Payload: map[string]any{"a": 2}}); err != nil {
		t.Fatal(err)
	}
	// Upsert replaces the same request id rather than duplicating.
	if err := store.WriteRequestDetail(ctx, Detail{RequestID: "req_new", RouteMode: "translated", Payload: map[string]any{"a": 3}}); err != nil {
		t.Fatal(err)
	}
	removed, err := store.PruneNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("pruned=%d want 1", removed)
	}
	var count int
	if err := sqlStore.DB().QueryRowContext(ctx, "SELECT COUNT(*) FROM request_details").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("request_details=%d want 1", count)
	}
}

func TestDetailStoreRefreshedRowSurvivesPrune(t *testing.T) {
	ctx := context.Background()
	store, sqlStore := newDetailStore(t, 0)
	defer sqlStore.Close()
	// A route decision written long ago is refreshed with its final outcome; the
	// refreshed row must carry a fresh created_at or the next prune deletes it.
	old := time.Now().AddDate(0, 0, -30)
	if err := store.WriteRequestDetail(ctx, Detail{RequestID: "req_1", CreatedAt: old, Payload: map[string]any{"stage": "planned"}}); err != nil {
		t.Fatal(err)
	}
	if err := store.WriteRequestDetail(ctx, Detail{RequestID: "req_1", RouteMode: "native", Payload: map[string]any{"stage": "final"}}); err != nil {
		t.Fatal(err)
	}
	removed, err := store.PruneNow(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 0 {
		t.Fatalf("pruned=%d want 0 for a freshly refreshed row", removed)
	}
	var detail string
	if err := sqlStore.DB().QueryRowContext(ctx, "SELECT detail FROM request_details WHERE request_id=?", "req_1").Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if !contains(detail, "final") {
		t.Fatalf("refreshed detail lost: %s", detail)
	}
}

func contains(haystack, needle string) bool { return strings.Contains(haystack, needle) }
