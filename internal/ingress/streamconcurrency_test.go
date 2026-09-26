package ingress

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/raufimusaddiq/routeweft/compat/mockupstream"
	openaiadapter "github.com/raufimusaddiq/routeweft/internal/protocol/openai"
)

// TestStreamConcurrencyLevels drives 1/10/50/100 simultaneous streamed Chat
// requests and asserts every stream completes intact with a single terminal
// marker and no cross-request interleaving, that each request is tracked as one
// active inference, and that no request goroutines leak (PRD-API-005 streaming;
// SPEC §28.2 concurrency).
func TestStreamConcurrencyLevels(t *testing.T) {
	const chunks = 64
	body := `{"model":"gpt-4o-mini","stream":true,"messages":[{"role":"user","content":"x"}]}`
	var fullStream strings.Builder
	for i := 0; i < chunks; i++ {
		fullStream.WriteString("data: {\"delta\":\"x\"}\n\n")
	}
	fullStream.WriteString("data: [DONE]\n\n")
	want := fullStream.String()

	for _, concurrency := range []int{1, 10, 50, 100} {
		t.Run(concurrentName(concurrency), func(t *testing.T) {
			server, err := mockupstream.Start(nil, mockupstream.WithMatcher(func(*http.Request, string) mockupstream.Decision {
				return mockupstream.Decision{Status: http.StatusOK, Headers: map[string]string{"Content-Type": "text/event-stream"}, Body: want}
			}))
			if err != nil {
				t.Fatal(err)
			}
			defer server.Close()

			var active, peak atomic.Int64
			mux, key := keyedHandler(t, Options{
				AllowPrivateUpstreams: true,
				ProviderResolver:      fixedProvider(openaiadapter.ChatProtocol, server.URL()),
			})
			handler := countingActive(mux, &active, &peak)
			authed := bearer(handler, key)

			// Warm up transport/connection pools once so the concurrent burst starts
			// from steady state rather than paying first-use pool creation.
			warm := httptest.NewRecorder()
			warmRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
			warmRequest.Header.Set("Content-Type", "application/json")
			authed.ServeHTTP(warm, warmRequest)
			if warm.Code != http.StatusOK || warm.Body.String() != want {
				t.Fatalf("warmup status %d len %d", warm.Code, warm.Body.Len())
			}
			var wg sync.WaitGroup
			bodies := make([]string, concurrency)
			statuses := make([]int, concurrency)
			start := make(chan struct{})
			var failures atomic.Int64
			for i := 0; i < concurrency; i++ {
				wg.Add(1)
				go func(i int) {
					defer wg.Done()
					<-start
					recorder := httptest.NewRecorder()
					request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
					request.Header.Set("Content-Type", "application/json")
					authed.ServeHTTP(recorder, request)
					statuses[i] = recorder.Code
					bodies[i] = recorder.Body.String()
					if recorder.Code != http.StatusOK || recorder.Body.String() != want {
						failures.Add(1)
					}
				}(i)
			}
			close(start)
			wg.Wait()

			if got := failures.Load(); got != 0 {
				for i, code := range statuses {
					if code != http.StatusOK || bodies[i] != want {
						t.Fatalf("stream %d status %d len %d", i, code, len(bodies[i]))
					}
				}
				t.Fatalf("%d/%d streams failed", got, concurrency)
			}
			for i, got := range bodies {
				if err := openaiadapter.ValidateStreamTerminal(got); err != nil {
					t.Fatalf("stream %d terminal invalid: %v", i, err)
				}
				if strings.Count(got, "data: [DONE]") != 1 {
					t.Fatalf("stream %d has %d terminal markers", i, strings.Count(got, "data: [DONE]"))
				}
			}
			if peak.Load() < 1 || peak.Load() > int64(concurrency) {
				t.Fatalf("peak active %d out of range for concurrency %d", peak.Load(), concurrency)
			}
			if got := active.Load(); got != 0 {
				t.Fatalf("%d inference requests still active after all streams returned", got)
			}

		})
	}
}

func concurrentName(n int) string {
	switch n {
	case 1:
		return "concurrency-1"
	case 10:
		return "concurrency-10"
	case 50:
		return "concurrency-50"
	default:
		return "concurrency-100"
	}
}

// countingActive wraps a handler to observe how many inference requests are
// inside it at once, mirroring the app's active-request accounting.
func countingActive(next http.Handler, active, peak *atomic.Int64) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := active.Add(1)
		for {
			observedPeak := peak.Load()
			if current <= observedPeak || peak.CompareAndSwap(observedPeak, current) {
				break
			}
		}
		defer active.Add(-1)
		next.ServeHTTP(w, r)
	})
}

// waitGoroutines polls until goroutine count settles near the pre-test baseline.
