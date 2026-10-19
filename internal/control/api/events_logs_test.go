package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/adminauth"
	controlevents "github.com/raufimusaddiq/routeweft/internal/control/events"
)

func TestEventsStreamRequiresSessionAndStreams(t *testing.T) {
	handler, _, store := newReadAPI(t)
	defer store.Close()
	handler.opts.Events = controlevents.New()
	ctx := context.Background()
	if _, err := handler.opts.Accounts.Bootstrap(ctx, "a1", "operator", "s3cret"); err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Attach(mux)

	// Unauthenticated requests are rejected before any stream is opened.
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/admin/v1/events", nil))
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("events status=%d want 401", recorder.Code)
	}

	session, err := handler.opts.Sessions.Create(adminauth.Account{ID: "a1", Username: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(mux)
	defer server.Close()

	request, _ := http.NewRequest(http.MethodGet, server.URL+"/admin/v1/events", nil)
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
	response, err := server.Client().Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("events status=%d content-type=%s", response.StatusCode, response.Header.Get("Content-Type"))
	}
	reader := bufio.NewReader(response.Body)
	// Consume the connection preamble, then publish and read one event frame.
	if line, err := reader.ReadString('\n'); err != nil || !strings.HasPrefix(line, ": connected") {
		t.Fatalf("unexpected preamble %q err=%v", line, err)
	}
	handler.opts.Events.Publish("request.completed", map[string]any{"requestId": "r1", "Authorization": "leak"})
	deadline := time.Now().Add(5 * time.Second)
	var frame strings.Builder
	for time.Now().Before(deadline) {
		line, err := reader.ReadString('\n')
		if err != nil {
			break
		}
		frame.WriteString(line)
		if strings.HasPrefix(line, "data: ") {
			break
		}
	}
	got := frame.String()
	if !strings.Contains(got, "event: request.completed") || !strings.Contains(got, "[redacted]") || strings.Contains(got, "leak") {
		t.Fatalf("unexpected event frame: %q", got)
	}
}

func TestLogBufferRedactsAndBounds(t *testing.T) {
	buffer := NewLogBuffer(3)
	var sink bytes.Buffer
	logger := slog.New(LogHandler(slog.NewTextHandler(&sink, nil), buffer))
	logger.Info("server started", "token", "super-secret", "addr", "127.0.0.1:21128", "providerURL", "https://user:pass@example.invalid/v1?token=leak")
	initial, _ := buffer.list(page{Number: 1, Size: 3}, "", "")
	if initial[0].Attributes["token"] != "[redacted]" || initial[0].Attributes["addr"] != "127.0.0.1:21128" || contains(toString(initial[0].Attributes), "leak") || contains(toString(initial[0].Attributes), "user:pass") {
		t.Fatalf("attributes not redacted/bounded: %+v", initial[0].Attributes)
	}
	logger.Warn("forced auth failure", "error", "authorization rejected", "attempts", 3)
	filtered, filteredTotal := buffer.list(page{Number: 1, Size: 3}, "warn", "")
	if filteredTotal != 1 || filtered[0].Attributes["error"] != "[redacted]" || filtered[0].Attributes["attempts"] != int64(3) {
		t.Fatalf("error attribute leaked: %+v", filtered)
	}
	logger.Error("plain message")
	logger.Info("fourth")
	logger.Info("fifth")
	items, total := buffer.list(page{Number: 1, Size: 10}, "", "")
	if total != 3 || len(items) != 3 {
		t.Fatalf("log buffer bound total=%d items=%d", total, len(items))
	}
	if items[0].Message != "fifth" || items[1].Message != "fourth" || items[2].Message != "plain message" {
		t.Fatalf("unexpected order: %+v", items)
	}
	filtered, filteredTotal = buffer.list(page{Number: 1, Size: 10}, "info", "")
	if filteredTotal != 2 || len(filtered) != 2 || filtered[0].Message != "fifth" || filtered[1].Message != "fourth" {
		t.Fatalf("level filter failed: %+v total=%d", filtered, filteredTotal)
	}
	for _, secret := range []string{"super-secret", "user:pass", "token=leak", "authorization rejected"} {
		if strings.Contains(sink.String(), secret) {
			t.Fatalf("underlying log handler received %q: %s", secret, sink.String())
		}
	}
	if got := safeLogText("dial failed at https://user:pass@example.invalid/v1"); strings.Contains(got, "user:pass") {
		t.Fatalf("embedded URL credentials survived: %q", got)
	}
}

func toString(value any) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
