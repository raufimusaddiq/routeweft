package api

import (
	"bufio"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/adminauth"
	"github.com/raufimusaddiq/routeweft/internal/control/events"
)

func streamFixture(t *testing.T) (*Handler, *http.ServeMux, *http.Cookie) {
	t.Helper()
	sessions := adminauth.NewSessionManager(0)
	session, err := sessions.Create(adminauth.Account{ID: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	h := New(Options{UsageFeed: events.NewUsageFeed(), Sessions: sessions})
	mux := http.NewServeMux()
	h.Attach(mux)
	return h, mux, &http.Cookie{Name: sessionCookieName, Value: session.ID}
}

func waitSubscribers(t *testing.T, feed *events.UsageFeed, want int) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if feed.Subscribers() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("subscribers=%d want %d", feed.Subscribers(), want)
}

// openStream connects and returns a frame reader plus a close function.
func openStream(t *testing.T, server *httptest.Server, cookie *http.Cookie, query string) (*bufio.Reader, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	r, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/admin/v1/usage/stream"+query, nil)
	r.AddCookie(cookie)
	response, err := server.Client().Do(r)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "text/event-stream" {
		cancel()
		t.Fatalf("status=%d type=%q", response.StatusCode, response.Header.Get("Content-Type"))
	}
	return bufio.NewReader(response.Body), func() { cancel(); response.Body.Close() }
}

// readUntil reads lines until one has the prefix, returning everything read.
func readUntil(t *testing.T, reader *bufio.Reader, prefix string) string {
	t.Helper()
	var got strings.Builder
	for {
		line, err := reader.ReadString('\n')
		got.WriteString(line)
		if err != nil {
			t.Fatalf("stream ended before %q: %v\n%s", prefix, err, got.String())
		}
		if strings.HasPrefix(line, prefix) {
			return got.String()
		}
	}
}

func TestUsageStreamAuthFilterAndCap(t *testing.T) {
	h, mux, cookie := streamFixture(t)
	for _, tc := range []struct {
		path   string
		auth   bool
		status int
	}{
		{"/admin/v1/usage/stream", false, 401},
		{"/admin/v1/usage/stream?status=oops", true, 400},
		{"/admin/v1/usage/stream?startDate=bad", true, 400},
		{"/admin/v1/usage/stream?startDate=2026-02-01T00:00:00Z&endDate=2026-01-01T00:00:00Z", true, 400},
	} {
		r := httptest.NewRequest("GET", tc.path, nil)
		if tc.auth {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		if w.Code != tc.status {
			t.Fatalf("%s: %d want %d", tc.path, w.Code, tc.status)
		}
	}
	if h.opts.UsageFeed.Subscribers() != 0 {
		t.Fatal("rejected request left a subscriber")
	}

	var held []*events.UsageSubscription
	for i := 0; i < events.MaxUsageSubscribers; i++ {
		sub, err := h.opts.UsageFeed.Subscribe()
		if err != nil {
			t.Fatal(err)
		}
		held = append(held, sub)
	}
	r := httptest.NewRequest("GET", "/admin/v1/usage/stream", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 503 || w.Header().Get("Retry-After") == "" || !strings.Contains(w.Body.String(), "stream_limit") {
		t.Fatalf("cap response %d %q retry=%q", w.Code, w.Body.String(), w.Header().Get("Retry-After"))
	}
	held[0].Close()
	server := httptest.NewServer(mux)
	defer server.Close()
	_, closeStream := openStream(t, server, cookie, "")
	closeStream()
	for _, sub := range held {
		sub.Close()
	}
}

func TestUsageStreamUnavailableWithoutFeed(t *testing.T) {
	h, mux, cookie := streamFixture(t)
	h.opts.UsageFeed = nil
	r := httptest.NewRequest("GET", "/admin/v1/usage/stream", nil)
	r.AddCookie(cookie)
	w := httptest.NewRecorder()
	mux.ServeHTTP(w, r)
	if w.Code != 503 {
		t.Fatalf("status=%d", w.Code)
	}
}

func TestUsageStreamDeliversOnlyMatchingMetadata(t *testing.T) {
	h, mux, cookie := streamFixture(t)
	server := httptest.NewServer(mux)
	defer server.Close()
	reader, closeStream := openStream(t, server, cookie, "?apiKeyId=allowed&providerId=p&status=200")
	defer closeStream()
	readUntil(t, reader, ": connected")
	waitSubscribers(t, h.opts.UsageFeed, 1)

	now := time.Now().UTC()
	h.opts.UsageFeed.Publish(events.UsageEvent{RequestID: "wrong-key", APIKeyID: "other", ProviderID: "p", Status: 200, CreatedAt: now})
	h.opts.UsageFeed.Publish(events.UsageEvent{RequestID: "wrong-provider", APIKeyID: "allowed", ProviderID: "q", Status: 200, CreatedAt: now})
	h.opts.UsageFeed.Publish(events.UsageEvent{RequestID: "wrong-status", APIKeyID: "allowed", ProviderID: "p", Status: 500, CreatedAt: now})
	h.opts.UsageFeed.Publish(events.UsageEvent{RequestID: "safe", APIKeyID: "allowed", ProviderID: "p", ModelID: "m", Status: 200, DurationMS: 42, InputTokens: 12, OutputTokens: 3, CreatedAt: now})

	got := readUntil(t, reader, "data:")
	for _, leaked := range []string{"wrong-key", "wrong-provider", "wrong-status"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("filter leaked %s: %s", leaked, got)
		}
	}
	for _, want := range []string{"event: request.completed", `"requestId":"safe"`, `"inputTokens":12`, `"outputTokens":3`, `"durationMs":42`, `"status":200`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in %s", want, got)
		}
	}
	for _, forbidden := range []string{"body", "prompt", "authorization", "token\"", "secret", "content"} {
		if strings.Contains(strings.ToLower(got), forbidden) {
			t.Fatalf("event carries %q: %s", forbidden, got)
		}
	}
}

func TestUsageStreamDateBoundsFilter(t *testing.T) {
	h, mux, cookie := streamFixture(t)
	server := httptest.NewServer(mux)
	defer server.Close()
	reader, closeStream := openStream(t, server, cookie, "?startDate=2026-06-01T00:00:00Z")
	defer closeStream()
	readUntil(t, reader, ": connected")
	waitSubscribers(t, h.opts.UsageFeed, 1)
	h.opts.UsageFeed.Publish(events.UsageEvent{RequestID: "old", Status: 200, CreatedAt: time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)})
	h.opts.UsageFeed.Publish(events.UsageEvent{RequestID: "new", Status: 200, CreatedAt: time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)})
	got := readUntil(t, reader, "data:")
	if strings.Contains(got, `"old"`) || !strings.Contains(got, `"new"`) {
		t.Fatalf("date bound: %s", got)
	}
}

func TestUsageStreamHeartbeat(t *testing.T) {
	h, _, cookie := streamFixture(t)
	server := httptest.NewServer(h.requireSession(func(w http.ResponseWriter, r *http.Request) {
		h.streamUsage(w, r, 10*time.Millisecond, time.Second)
	}))
	defer server.Close()
	r, _ := http.NewRequest("GET", server.URL, nil)
	r.AddCookie(cookie)
	response, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	readUntil(t, reader, ": heartbeat")
	readUntil(t, reader, ": heartbeat") // periodic, not a one-off
}

func TestUsageStreamEndsWhenSessionRevoked(t *testing.T) {
	h, _, cookie := streamFixture(t)
	server := httptest.NewServer(h.requireSession(func(w http.ResponseWriter, r *http.Request) {
		h.streamUsage(w, r, 10*time.Millisecond, time.Second)
	}))
	defer server.Close()
	r, _ := http.NewRequest("GET", server.URL, nil)
	r.AddCookie(cookie)
	response, err := server.Client().Do(r)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	reader := bufio.NewReader(response.Body)
	readUntil(t, reader, ": connected")
	h.opts.Sessions.Revoke(cookie.Value)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, err := reader.ReadString('\n'); err != nil {
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stream outlived its session")
	}
	waitSubscribers(t, h.opts.UsageFeed, 0)
}

// The goroutine-leak test: clients that connect and vanish, with and without
// ever receiving a frame, must leave no subscriber and no goroutine behind.
func TestUsageStreamDisconnectLeavesNoSubscriberOrGoroutine(t *testing.T) {
	h, mux, cookie := streamFixture(t)
	server := httptest.NewServer(mux)
	defer server.Close()

	// Warm the server and client pools so the baseline is steady.
	_, closeWarm := openStream(t, server, cookie, "")
	closeWarm()
	waitSubscribers(t, h.opts.UsageFeed, 0)
	server.Client().CloseIdleConnections()
	time.Sleep(50 * time.Millisecond)
	baseline := runtime.NumGoroutine()

	// 100 clients in batches under the subscriber cap; half drop mid-stream
	// after a frame, half before reading anything.
	for batch := 0; batch < 4; batch++ {
		var closers []func()
		for i := 0; i < 25; i++ {
			reader, closeStream := openStream(t, server, cookie, "")
			if i%2 == 0 {
				readUntil(t, reader, ": connected")
			}
			closers = append(closers, closeStream)
		}
		if h.opts.UsageFeed.Subscribers() == 0 {
			t.Fatal("no subscribers while clients are connected")
		}
		for _, closeStream := range closers {
			closeStream()
		}
		waitSubscribers(t, h.opts.UsageFeed, 0)
	}
	waitSubscribers(t, h.opts.UsageFeed, 0)
	server.Client().CloseIdleConnections()

	deadline := time.Now().Add(3 * time.Second)
	for runtime.NumGoroutine() > baseline+2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if n := runtime.NumGoroutine(); n > baseline+2 {
		buf := make([]byte, 1<<16)
		t.Fatalf("goroutines baseline=%d after=%d\n%s", baseline, n, buf[:runtime.Stack(buf, true)])
	}
}

// blockedWriter models a client whose socket never drains: after the first
// frame every write blocks until its deadline, then fails.
type blockedWriter struct {
	header   http.Header
	mu       sync.Mutex
	deadline time.Time
	writes   int
}

func (w *blockedWriter) Header() http.Header { return w.header }
func (w *blockedWriter) WriteHeader(int)     {}
func (w *blockedWriter) Flush()              {}
func (w *blockedWriter) SetWriteDeadline(d time.Time) error {
	w.mu.Lock()
	w.deadline = d
	w.mu.Unlock()
	return nil
}
func (w *blockedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	w.writes++
	first, deadline := w.writes == 1, w.deadline
	w.mu.Unlock()
	if first {
		return len(p), nil
	}
	time.Sleep(time.Until(deadline))
	return 0, errors.New("write deadline exceeded")
}

// A stalled heartbeat write must free the handler, not hold it forever.
func TestUsageStreamStalledClientIsReleasedByHeartbeat(t *testing.T) {
	h, _, cookie := streamFixture(t)
	r := httptest.NewRequest("GET", "/admin/v1/usage/stream", nil)
	r.AddCookie(cookie)
	w := &blockedWriter{header: make(http.Header)}
	done := make(chan struct{})
	go func() { defer close(done); h.streamUsage(w, r, time.Millisecond, 20*time.Millisecond) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("stalled client kept the handler")
	}
	waitSubscribers(t, h.opts.UsageFeed, 0)
}

// The slow-client path end to end: a handler stuck writing to a dead socket
// must not slow Publish, and the subscriber is cut once its buffer fills.
func TestUsageStreamSlowClientDoesNotDelayPublisher(t *testing.T) {
	h, _, cookie := streamFixture(t)
	r := httptest.NewRequest("GET", "/admin/v1/usage/stream", nil)
	r.AddCookie(cookie)
	// A long write timeout keeps the handler wedged inside Write.
	w := &blockedWriter{header: make(http.Header)}
	done := make(chan struct{})
	go func() { defer close(done); h.streamUsage(w, r, time.Hour, 30*time.Second) }()
	waitSubscribers(t, h.opts.UsageFeed, 1)

	start := time.Now()
	for i := 0; i < 1000; i++ {
		h.opts.UsageFeed.Publish(events.UsageEvent{RequestID: "r", Status: 200, CreatedAt: time.Now()})
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("1000 publishes took %s behind a wedged client", elapsed)
	}
	waitSubscribers(t, h.opts.UsageFeed, 0)
	if h.opts.UsageFeed.Dropped() != 1 {
		t.Fatalf("dropped=%d", h.opts.UsageFeed.Dropped())
	}
	// The wedged handler is still stuck in Write (its own goroutine only); once
	// its write fails it drains the closed channel and exits.
	select {
	case <-done:
		t.Fatal("handler returned before its write deadline; test did not model a wedged socket")
	case <-time.After(20 * time.Millisecond):
	}
}
