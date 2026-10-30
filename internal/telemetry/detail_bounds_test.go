package telemetry

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// A field longer than the limit is replaced by its size and the first 200
// characters of its JSON (requestDetailsRepo.js:94-100).
func TestTruncateFieldKeepsAPreviewOfTheOversizeOnly(t *testing.T) {
	small := map[string]any{"a": "b"}
	if got := TruncateField(small, 100); fmt.Sprint(got) != fmt.Sprint(small) {
		t.Fatalf("a field within the limit must be unchanged: %v", got)
	}
	big := map[string]any{"text": strings.Repeat("x", 500)}
	got, ok := TruncateField(big, 100).(map[string]any)
	if !ok || got["_truncated"] != true {
		t.Fatalf("not truncated: %v", got)
	}
	encoded, _ := json.Marshal(big)
	if got["_originalSize"] != len(encoded) {
		t.Fatalf("_originalSize %v, want %d", got["_originalSize"], len(encoded))
	}
	if preview := got["_preview"].(string); len([]rune(preview)) != 200 || preview != string(encoded)[:200] {
		t.Fatalf("preview is not the first 200 characters: %q", preview)
	}
	// Characters, not bytes: a multi-byte character is never cut in half.
	wide := TruncateField(map[string]any{"t": strings.Repeat("é", 400)}, 50).(map[string]any)
	if preview := wide["_preview"].(string); len([]rune(preview)) != 200 || !strings.HasSuffix(preview, "é") {
		t.Fatalf("preview cut a character: %q", preview)
	}
}

func TestDetailRedactorKeepsCountsAndRemovesCredentials(t *testing.T) {
	redactor := DetailRedactor{Secrets: []string{"sk-live-0123456789abcdef"}}
	in := map[string]any{
		"max_tokens": float64(100), "max_completion_tokens": json.Number("7"), "prompt_tokens": float64(3),
		"Authorization": "Bearer abcdefghijkl", "api_key": "k", "x-api-key": "k", "refresh_token": "r", "access_token": "a",
		"cookie": "c", "password": "p", "client_secret": "s", "token": float64(12345), "password_tokens": "text",
		"messages": []any{map[string]any{"role": "user", "content": "my key is sk-live-0123456789abcdef, header Bearer zzzzzzzzzzzz12 ok"}},
		"nested":   map[string]any{"tokens": []any{"a", "b"}, "headers": map[string]any{"Cookie": "x", "X-Request-Id": "r1"}},
	}
	out := redactor.Redact(in).(map[string]any)
	for _, kept := range []string{"max_tokens", "max_completion_tokens", "prompt_tokens"} {
		if out[kept] == "[redacted]" {
			t.Errorf("%s is a count and must be kept: %v", kept, out[kept])
		}
	}
	for _, removed := range []string{"Authorization", "api_key", "x-api-key", "refresh_token", "access_token", "cookie", "password", "client_secret", "token", "password_tokens"} {
		if out[removed] != "[redacted]" {
			t.Errorf("%s must be redacted: %v", removed, out[removed])
		}
	}
	nested := out["nested"].(map[string]any)
	if nested["tokens"] != "[redacted]" {
		t.Errorf("a list of strings under tokens is not a count: %v", nested["tokens"])
	}
	headers := nested["headers"].(map[string]any)
	if headers["Cookie"] != "[redacted]" || headers["X-Request-Id"] != "r1" {
		t.Errorf("headers: %v", headers)
	}
	content := out["messages"].([]any)[0].(map[string]any)["content"].(string)
	if strings.Contains(content, "sk-live") || strings.Contains(content, "zzzzzzzzzzzz12") || !strings.Contains(content, "my key is [redacted]") {
		t.Errorf("credentials inside text must go, the rest stays: %q", content)
	}
	// The input is never changed.
	if in["Authorization"] != "Bearer abcdefghijkl" {
		t.Error("the input was mutated")
	}
	// A short secret would blank ordinary letters, so it is not scrubbed by value.
	if got := (DetailRedactor{Secrets: []string{"ab"}}).Redact("abc").(string); got != "abc" {
		t.Errorf("short secret scrubbed: %q", got)
	}
}

func rowIDs(t *testing.T, store *SQLiteDetailStore) []string {
	t.Helper()
	rows, err := store.db.Query("SELECT request_id FROM request_details ORDER BY created_at, rowid")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	return ids
}

// Retention by count: after every write only the newest MaxRows details remain,
// and rewriting a request id never grows the table.
func TestDetailStoreKeepsOnlyTheNewestRows(t *testing.T) {
	ctx := context.Background()
	store, sqlStore := newDetailStore(t, 0)
	defer sqlStore.Close()
	limit := 3
	store.MaxRows = func() int { return limit }
	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 7; i++ {
		if err := store.WriteRequestDetail(ctx, Detail{RequestID: fmt.Sprintf("r%d", i), Provider: "openai", Status: "success", Payload: map[string]any{"i": i}, CreatedAt: base.Add(time.Duration(i) * time.Second)}); err != nil {
			t.Fatal(err)
		}
	}
	if got := strings.Join(rowIDs(t, store), ","); got != "r4,r5,r6" {
		t.Fatalf("kept %s, want the newest three r4,r5,r6", got)
	}
	// Writing an id that exists replaces it and moves it to the newest end.
	if err := store.WriteRequestDetail(ctx, Detail{RequestID: "r4", Status: "error", Payload: map[string]any{"again": true}, CreatedAt: base.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rowIDs(t, store), ","); got != "r5,r6,r4" {
		t.Fatalf("kept %s", got)
	}
	// A late-arriving older detail does not displace newer ones: it is the one deleted.
	if err := store.WriteRequestDetail(ctx, Detail{RequestID: "ancient", Payload: map[string]any{}, CreatedAt: base.Add(-time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rowIDs(t, store), ","); got != "r5,r6,r4" {
		t.Fatalf("an older detail displaced newer ones: %s", got)
	}
	// The limit is read at every write, so lowering it takes effect on the next one.
	limit = 1
	if err := store.WriteRequestDetail(ctx, Detail{RequestID: "last", Payload: map[string]any{}, CreatedAt: base.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rowIDs(t, store), ","); got != "last" {
		t.Fatalf("kept %s after lowering the limit", got)
	}
	// The default applies when no limit is configured.
	store.MaxRows = nil
	for i := 0; i < DefaultMaxDetailRows+5; i++ {
		if err := store.WriteRequestDetail(ctx, Detail{RequestID: fmt.Sprintf("d%d", i), Payload: map[string]any{}, CreatedAt: base.Add(3*time.Hour + time.Duration(i)*time.Millisecond)}); err != nil {
			t.Fatal(err)
		}
	}
	if got := len(rowIDs(t, store)); got != DefaultMaxDetailRows {
		t.Fatalf("%d rows, want the default %d", got, DefaultMaxDetailRows)
	}
}

// The attribution columns are what a list of details is filtered by.
func TestDetailStoreRecordsAttributionColumns(t *testing.T) {
	ctx := context.Background()
	store, sqlStore := newDetailStore(t, 0)
	defer sqlStore.Close()
	if err := store.WriteRequestDetail(ctx, Detail{RequestID: "a", Provider: "openai", Model: "gpt", Connection: "c1", Status: "error", Payload: map[string]any{}}); err != nil {
		t.Fatal(err)
	}
	var provider, model, connection, status string
	if err := sqlStore.DB().QueryRow("SELECT provider_id,model_id,connection_id,status FROM request_details WHERE request_id='a'").Scan(&provider, &model, &connection, &status); err != nil {
		t.Fatal(err)
	}
	if provider != "openai" || model != "gpt" || connection != "c1" || status != "error" {
		t.Fatalf("%s %s %s %s", provider, model, connection, status)
	}
}

// A detail made by the lazy path is built by the batcher, once, and written.
func TestRecordDetailLazyBuildsOnTheBatcher(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store, sqlStore := newDetailStore(t, 0)
	defer sqlStore.Close()
	options := DefaultOptions()
	options.FlushInterval = 20 * time.Millisecond
	service := New(NewSQLiteSink(sqlStore.DB()), options).WithDetails(store)
	done := make(chan struct{})
	go func() { service.Run(ctx); close(done) }()
	built := make(chan struct{}, 4)
	service.RecordDetailLazy("lazy-1", func() Detail {
		built <- struct{}{}
		return Detail{RequestID: "lazy-1", Status: "success", Payload: map[string]any{"ok": true}}
	})
	select {
	case <-built:
	case <-time.After(3 * time.Second):
		t.Fatal("the detail was never built")
	}
	if err := service.FlushNow(ctx); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(rowIDs(t, store), ","); got != "lazy-1" {
		t.Fatalf("rows %s", got)
	}
	cancel()
	<-done
	if len(built) != 0 {
		t.Fatal("the builder ran more than once")
	}
}
