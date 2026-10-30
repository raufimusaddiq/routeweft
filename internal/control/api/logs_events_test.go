package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	controlevents "github.com/raufimusaddiq/routeweft/internal/control/events"
)

// #277 (U08): console records ride the shared event channel as coalesced "console.log" batches and a
// "console.cleared" event, in the oracle's init/line/lines/clear shape (console-logs/stream/route.js:28-65).

// consoleFeedFixture attaches a buffer to a bus with the given batch window. A wide window makes a burst land
// in one batch even on a slow machine; the shipped window (consoleBatchDelay) is exercised by the app test.
func consoleFeedFixture(t *testing.T, delay time.Duration) (*slog.Logger, *LogBuffer, <-chan controlevents.Event) {
	t.Helper()
	bus := controlevents.New()
	buffer := NewLogBuffer(0)
	buffer.publishTo(bus, delay)
	events, cancel := bus.Subscribe()
	t.Cleanup(cancel)
	return slog.New(LogHandler(slog.NewTextHandler(io.Discard, nil), buffer)), buffer, events
}

func nextConsoleEvent(t *testing.T, events <-chan controlevents.Event, within time.Duration) (controlevents.Event, bool) {
	t.Helper()
	select {
	case event := <-events:
		return event, true
	case <-time.After(within):
		return controlevents.Event{}, false
	}
}

func consoleLines(t *testing.T, event controlevents.Event) (lines []map[string]any, truncated bool) {
	t.Helper()
	payload, ok := event.Data.(map[string]any)
	if !ok {
		t.Fatalf("payload %T", event.Data)
	}
	raw, _ := json.Marshal(payload["lines"])
	if err := json.Unmarshal(raw, &lines); err != nil {
		t.Fatal(err)
	}
	truncated, _ = payload["truncated"].(bool)
	return lines, truncated
}

func TestConsoleLinesAreAnnouncedAsOneCoalescedEvent(t *testing.T) {
	logger, _, events := consoleFeedFixture(t, 600*time.Millisecond)
	for i := 0; i < 3; i++ {
		logger.Info(fmt.Sprintf("line %d", i), "n", i)
	}
	event, ok := nextConsoleEvent(t, events, 3*time.Second)
	if !ok || event.Type != "console.log" {
		t.Fatalf("event %+v ok=%v", event, ok)
	}
	lines, truncated := consoleLines(t, event)
	if len(lines) != 3 || truncated || lines[0]["message"] != "line 0" || lines[2]["message"] != "line 2" || lines[0]["level"] != "INFO" {
		t.Fatalf("lines %v truncated=%v", lines, truncated)
	}
	if first, last := lines[0]["id"].(float64), lines[2]["id"].(float64); last != first+2 {
		t.Fatalf("ids %v..%v are not consecutive and ascending", first, last)
	}
	if _, more := nextConsoleEvent(t, events, 900*time.Millisecond); more {
		t.Fatal("a second event followed one burst")
	}

	// A burst bigger than one batch sends the newest lines and says older ones were left out.
	for i := 0; i < 250; i++ {
		logger.Info(fmt.Sprintf("burst %d", i))
	}
	event, ok = nextConsoleEvent(t, events, 3*time.Second)
	if !ok {
		t.Fatal("no event for the burst")
	}
	lines, truncated = consoleLines(t, event)
	if len(lines) != consoleBatchMax || !truncated || lines[len(lines)-1]["message"] != "burst 249" || lines[0]["message"] != "burst 150" {
		t.Fatalf("burst: %d lines truncated=%v first=%v last=%v", len(lines), truncated, lines[0]["message"], lines[len(lines)-1]["message"])
	}
}

func TestConsoleClearIsAnnouncedAndNothingOlderFollows(t *testing.T) {
	logger, buffer, events := consoleFeedFixture(t, 600*time.Millisecond)
	logger.Info("before the clear")
	logger.Info("also before")
	// The batch for those two is still pending; the clear must win and the stale batch must not resurrect them.
	if removed := buffer.clear(); removed != 2 {
		t.Fatalf("cleared %d", removed)
	}
	event, ok := nextConsoleEvent(t, events, time.Second)
	if !ok || event.Type != "console.cleared" {
		t.Fatalf("event %+v ok=%v", event, ok)
	}
	if got := event.Data.(map[string]any)["cleared"]; got != 2 {
		t.Fatalf("cleared %v", got)
	}
	if late, more := nextConsoleEvent(t, events, 1200*time.Millisecond); more {
		t.Fatalf("an event followed the clear: %+v", late)
	}
	logger.Info("after the clear")
	event, ok = nextConsoleEvent(t, events, 3*time.Second)
	if !ok || event.Type != "console.log" {
		t.Fatalf("event %+v ok=%v", event, ok)
	}
	if lines, _ := consoleLines(t, event); len(lines) != 1 || lines[0]["message"] != "after the clear" {
		t.Fatalf("lines %v", lines)
	}
}

func TestConsoleEventsCarryNoSecret(t *testing.T) {
	logger, _, events := consoleFeedFixture(t, 50*time.Millisecond)
	logger.Info("sent Authorization: Bearer abc123def to https://user:pw@host.example/v1?api_key=sk-live-9", "api_key", "sk-live-777", "note", "ok")
	event, ok := nextConsoleEvent(t, events, 2*time.Second)
	if !ok {
		t.Fatal("no event")
	}
	raw, _ := json.Marshal(event)
	for _, secret := range []string{"abc123def", "sk-live-9", "sk-live-777", "user:pw"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("event carries %q: %s", secret, raw)
		}
	}
}

func TestConsoleFeedIsOptionalAndDetachable(t *testing.T) {
	buffer := NewLogBuffer(0)
	logger := slog.New(LogHandler(slog.NewTextHandler(io.Discard, nil), buffer))
	logger.Info("no bus yet")
	if buffer.clear() != 1 {
		t.Fatal("clear without a bus")
	}
	bus := controlevents.New()
	buffer.PublishTo(bus)
	buffer.PublishTo(nil)
	logger.Info("detached")
	events, cancel := bus.Subscribe()
	defer cancel()
	if _, more := nextConsoleEvent(t, events, 400*time.Millisecond); more {
		t.Fatal("a detached buffer still announced")
	}
}
