package wire

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// grokUpstream answers with the statuses in order, then 200, and records what it was sent.
func grokUpstream(t *testing.T, statuses ...int) (*httptest.Server, *[]http.Header, *[]string) {
	t.Helper()
	var mu sync.Mutex
	var headers []http.Header
	var bodies []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		payload, _ := io.ReadAll(r.Body)
		headers = append(headers, r.Header.Clone())
		bodies = append(bodies, string(payload))
		status := 200
		if len(headers) <= len(statuses) {
			status = statuses[len(headers)-1]
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(server.Close)
	return server, &headers, &bodies
}

func grokSleeps(t *testing.T) *[]time.Duration {
	t.Helper()
	var waits []time.Duration
	previous := grokCLISleep
	grokCLISleep = func(_ context.Context, delay time.Duration) error { waits = append(waits, delay); return nil }
	t.Cleanup(func() { grokCLISleep = previous })
	return &waits
}

func grokSend(t *testing.T, url string) (*http.Response, error) {
	t.Helper()
	request, _ := http.NewRequest(http.MethodPost, url, strings.NewReader(`{"input":"x"}`))
	request.Header.Set("x-grok-req-id", "first")
	return GrokCLIDo(http.DefaultClient, request)
}

func TestGrokCLIRetriesEachStatusAfterItsOwnWait(t *testing.T) {
	for _, tc := range []struct {
		status int
		wait   time.Duration
	}{{429, 2 * time.Second}, {502, 1500 * time.Millisecond}, {503, 1500 * time.Millisecond}, {504, 3 * time.Second}} {
		waits := grokSleeps(t)
		server, headers, bodies := grokUpstream(t, tc.status, tc.status)
		response, err := grokSend(t, server.URL)
		if err != nil || response.StatusCode != 200 {
			t.Fatalf("%d: %v %v", tc.status, response, err)
		}
		if len(*headers) != 3 || len(*waits) != 2 || (*waits)[0] != tc.wait {
			t.Errorf("%d: %d sends, waits %v", tc.status, len(*headers), *waits)
		}
		for _, body := range *bodies {
			if body != `{"input":"x"}` {
				t.Errorf("%d: body %q", tc.status, body)
			}
		}
		if (*headers)[1].Get("x-grok-req-id") == "first" || (*headers)[1].Get("x-grok-req-id") == (*headers)[2].Get("x-grok-req-id") {
			t.Errorf("%d: retries reuse a request id", tc.status)
		}
	}
}

func TestGrokCLIRetryStopsAfterTwoAndReturnsTheLastAnswer(t *testing.T) {
	waits := grokSleeps(t)
	server, headers, _ := grokUpstream(t, 429, 429, 429, 429)
	response, err := grokSend(t, server.URL)
	if err != nil || response.StatusCode != 429 || len(*headers) != 3 || len(*waits) != 2 {
		t.Fatalf("%v %v sends=%d waits=%v", response, err, len(*headers), *waits)
	}
}

func TestGrokCLIRetryBudgetIsSharedAcrossStatuses(t *testing.T) {
	waits := grokSleeps(t)
	server, headers, _ := grokUpstream(t, 429, 502, 503)
	response, err := grokSend(t, server.URL)
	if err != nil || response.StatusCode != 503 || len(*headers) != 3 || len(*waits) != 2 {
		t.Fatalf("%v %v sends=%d waits=%v", response, err, len(*headers), *waits)
	}
}

func TestGrokCLIDoesNotRepeatWhatTheRegistryDoesNotName(t *testing.T) {
	for _, status := range []int{200, 400, 401, 402, 500} {
		waits := grokSleeps(t)
		server, headers, _ := grokUpstream(t, status)
		response, err := grokSend(t, server.URL)
		if err != nil || response.StatusCode != status || len(*headers) != 1 || len(*waits) != 0 {
			t.Errorf("%d: %v %v sends=%d", status, response, err, len(*headers))
		}
	}
}

func TestGrokCLIUnreachableEndpointIsRetriedAs502ThenFails(t *testing.T) {
	waits := grokSleeps(t)
	server := httptest.NewServer(http.NotFoundHandler())
	url := server.URL
	server.Close()
	response, err := grokSend(t, url)
	if err == nil || response != nil || len(*waits) != 2 || (*waits)[0] != 1500*time.Millisecond {
		t.Fatalf("%v %v waits=%v", response, err, *waits)
	}
}

func TestGrokCLIRetryWaitEndsWithTheRequest(t *testing.T) {
	previous := grokCLISleep
	grokCLISleep = codexSleep
	t.Cleanup(func() { grokCLISleep = previous })
	server, headers, _ := grokUpstream(t, 429, 429)
	ctx, cancel := context.WithCancel(context.Background())
	request, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL, strings.NewReader(`{}`))
	go func() { time.Sleep(50 * time.Millisecond); cancel() }()
	started := time.Now()
	response, err := GrokCLIDo(http.DefaultClient, request)
	if err == nil || response != nil || time.Since(started) > time.Second || len(*headers) != 1 {
		t.Fatalf("%v %v %v sends=%d", response, err, time.Since(started), len(*headers))
	}
}

func TestGrokCLISpendingLimitFallsBackAndCoolsTheAccountForTwoMinutes(t *testing.T) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, time.UTC)
	for _, body := range []string{
		`{"code":"personal-team-blocked:spending-limit","error":"You have run out of credits"}`,
		`{"error":{"message":"nested"}}`, `not json`, ``,
	} {
		got, ok := ClassifyError("grok-cli", 402, []byte(body), now)
		if !ok || got.Outcome != routing.OutcomeFallbackAccount || got.ResetAt != now.Add(2*time.Minute).Format(time.RFC3339Nano) || !got.AllowsFallback() {
			t.Errorf("%q: %+v %v", body, got, ok)
		}
	}
	for _, status := range []int{200, 400, 401, 403, 429, 500} {
		if _, ok := ClassifyError("grok-cli", status, []byte(`{"code":"personal-team-blocked:spending-limit"}`), now); ok {
			t.Errorf("status %d was classified as a spending limit", status)
		}
	}
}
