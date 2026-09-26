package ingress

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/compat/mockupstream"
	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

// TestIngressPostBurstRSS reports RSS for concurrent streamed requests. The Go
// test process includes other tests and race instrumentation; its absolute RSS
// is not a production-service budget gate (PRD §19). It skips without /proc.
func TestIngressPostBurstRSS(t *testing.T) {
	baseline, ok := processRSSKiB()
	if !ok {
		t.Skip("/proc/self/smaps_rollup unavailable; RSS budget not measurable here")
	}
	body := `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"x"}]}`
	var stream strings.Builder
	for i := 0; i < 32; i++ {
		stream.WriteString("data: {\"delta\":\"x\"}\n\n")
	}
	stream.WriteString("data: [DONE]\n\n")
	want := stream.String()
	server, err := mockupstream.Start(nil, mockupstream.WithMatcher(func(*http.Request, string) mockupstream.Decision {
		return mockupstream.Decision{Status: http.StatusOK, Headers: map[string]string{"Content-Type": "text/event-stream"}, Body: want}
	}))
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	mux, key := keyedHandler(t, Options{AllowPrivateUpstreams: true, ProviderResolver: fixedProvider(openaiadapter.ChatProtocol, server.URL())})
	front := httptest.NewServer(bearer(mux, key))
	defer front.Close()
	client := &http.Client{}

	post := func() error {
		response, err := client.Post(front.URL+"/v1/chat/completions", "application/json", strings.NewReader(body))
		if err != nil {
			return err
		}
		defer response.Body.Close()
		data, err := io.ReadAll(response.Body)
		if err != nil {
			return err
		}
		if response.StatusCode != http.StatusOK || string(data) != want {
			return fmt.Errorf("stream status=%d size=%d", response.StatusCode, len(data))
		}
		return nil
	}
	if err := post(); err != nil {
		t.Fatal(err)
	}

	const requests = 20
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < requests; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := post(); err != nil {
				t.Errorf("post-burst request: %v", err)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	after, ok := processRSSKiB()
	if !ok {
		t.Skip("/proc/self/smaps_rollup disappeared")
	}
	t.Logf("post-burst test-process RSS=%d KiB (growth=%d KiB), %d streams in %s; PRD production-service target <= 65536 KiB", after, after-baseline, requests, elapsed)
}

// BenchmarkStreamedChatCompletion reports per-request CPU/allocations for a
// streamed Chat pass through the ingress handler with a warm pooled transport.
func BenchmarkStreamedChatCompletion(b *testing.B) {
	body := `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"x"}]}`
	var stream strings.Builder
	for i := 0; i < 32; i++ {
		stream.WriteString("data: {\"delta\":\"x\"}\n\n")
	}
	stream.WriteString("data: [DONE]\n\n")
	want := stream.String()
	server, err := mockupstream.Start(nil, mockupstream.WithMatcher(func(*http.Request, string) mockupstream.Decision {
		return mockupstream.Decision{Status: http.StatusOK, Headers: map[string]string{"Content-Type": "text/event-stream"}, Body: want}
	}))
	if err != nil {
		b.Fatal(err)
	}
	defer server.Close()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, b.TempDir()+"/routeweft.sqlite")
	if err != nil {
		b.Fatal(err)
	}
	defer store.Close()
	if err := migrations.NewRunner(store.DB()).Apply(ctx); err != nil {
		b.Fatal(err)
	}
	manager, err := runtime.NewManager(ctx, store.DB())
	if err != nil {
		b.Fatal(err)
	}
	_, key, err := manager.CreateAPIKey(ctx, "resource-benchmark")
	if err != nil {
		b.Fatal(err)
	}
	mux := http.NewServeMux()
	New(manager, Options{AllowPrivateUpstreams: true, ProviderResolver: fixedProvider(openaiadapter.ChatProtocol, server.URL())}).Attach(mux)
	handler := bearer(mux, key)
	b.SetBytes(int64(len(body) + len(want)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		handler.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusOK {
			b.Fatalf("status %d", recorder.Code)
		}
	}
}

// processRSSKiB reads this process's resident set size from smaps_rollup.
func processRSSKiB() (int64, bool) {
	data, err := os.ReadFile("/proc/self/smaps_rollup")
	if err != nil {
		return 0, false
	}
	for _, line := range strings.Split(string(data), "\n") {
		rest, found := strings.CutPrefix(line, "Rss:")
		if !found {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return 0, false
		}
		value, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil {
			return 0, false
		}
		return value, true
	}
	return 0, false
}
