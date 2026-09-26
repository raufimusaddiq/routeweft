package ingress

import (
	"context"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/compat/mockupstream"
	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
)

// TestStreamingCancellationSoak repeatedly drives streamed Chat requests and
// disconnects the client mid-stream, asserting the handler returns promptly, no
// completed-outcome telemetry is emitted for cancelled work, and the upstream
// reader stops rather than draining every panel (PRD-API-005 cancellation,
// SPEC §34, SPEC §28.2 shutdown/telemetry behaviour).
func TestStreamingCancellationSoak(t *testing.T) {
	const iterations = 200
	body := `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"x"}]}`

	// Every stream is long enough that a cancelled client cannot consume it. If
	// cancellation did not propagate, each iteration would block on the reader.
	streamBody := strings.Repeat("data: {\"delta\":\"x\"}\n\n", 1<<16) + "data: [DONE]\n\n"
	server, err := mockupstream.Start(nil, mockupstream.WithMatcher(func(*http.Request, string) mockupstream.Decision {
		return mockupstream.Decision{Status: http.StatusOK, Headers: map[string]string{"Content-Type": "text/event-stream"}, Body: streamBody}
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()

	var outcomes atomic.Int64
	mux, key := keyedHandler(t, Options{
		AllowPrivateUpstreams: true,
		ProviderResolver:      fixedProvider(openaiadapter.ChatProtocol, server.URL()),
		OnRequestComplete:     func(RequestOutcome) { outcomes.Add(1) },
	})
	authed := bearer(mux, key)

	before := runtime.NumGoroutine()
	var slowest time.Duration
	var slowestMu sync.Mutex
	start := time.Now()
	for i := 0; i < iterations; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)).WithContext(ctx)
		request.Header.Set("Content-Type", "application/json")
		writer := &cancellingWriter{cancel: cancel, after: 1}
		iterationStart := time.Now()
		done := make(chan struct{})
		go func() {
			authed.ServeHTTP(writer, request)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			cancel()
			t.Fatalf("iteration %d did not return after client cancellation", i)
		}
		elapsed := time.Since(iterationStart)
		slowestMu.Lock()
		if elapsed > slowest {
			slowest = elapsed
		}
		slowestMu.Unlock()
		if writer.status != http.StatusOK {
			t.Fatalf("iteration %d status %d", i, writer.status)
		}
		if got := outcomes.Load(); got != 0 {
			t.Fatalf("iteration %d emitted %d completed outcomes for cancelled streams", i, got)
		}
	}
	t.Logf("%d cancelled streams in %s (slowest %s)", iterations, time.Since(start), slowest)

	// Goroutines may take a moment to unwind; poll rather than sleeping once.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if runtime.NumGoroutine() <= before+iterations/10 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("goroutine leak after soak: before=%d after=%d", before, runtime.NumGoroutine())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if outcomes.Load() != 0 {
		t.Fatalf("%d completed outcomes emitted for cancelled streams", outcomes.Load())
	}
}

// TestNonStreamingCancellationSoak proves cancellation also unwinds promptly for
// buffered responses whose upstream never terminates, so the non-streaming path
// cannot pin a request goroutine on a stalled upstream (PRD-API-005).
func TestNonStreamingCancellationSoak(t *testing.T) {
	const iterations = 100
	body := `{"model":"gpt-4o-mini","messages":[{"role":"user","content":"x"}]}`
	server, err := mockupstream.Start(nil, mockupstream.WithMatcher(func(*http.Request, string) mockupstream.Decision {
		return mockupstream.Decision{Status: http.StatusOK, Headers: map[string]string{"Content-Type": "application/json"}, Body: `{"ok":true}`, Delay: 5 * time.Second}
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	mux, key := keyedHandler(t, Options{
		AllowPrivateUpstreams: true,
		ProviderResolver:      fixedProvider(openaiadapter.ChatProtocol, server.URL()),
	})
	authed := bearer(mux, key)
	for i := 0; i < iterations; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body)).WithContext(ctx)
		request.Header.Set("Content-Type", "application/json")
		done := make(chan struct{})
		go func() {
			authed.ServeHTTP(httptest.NewRecorder(), request)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("iteration %d did not return after deadline cancellation", i)
		}
		cancel()
	}
	if got := len(server.Requests()); got != iterations {
		t.Fatalf("upstream saw %d requests, want %d", got, iterations)
	}
}
