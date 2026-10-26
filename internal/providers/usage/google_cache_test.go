package usage

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/quota"
)

// #240 evidence for the weekly summary cache and the per-call timeouts, pinned
// to oracle 1c3b9f47: open-sse/services/usage/antigravity-weekly.js:15-26,
// 116-166, google.js:56,107,138,225-266,295, shared.js:62-70, xiaomi-mimo.js:62
// and open-sse/shared/mimoAccount.js:252-254.

// clock is a settable reader clock.
type clock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

// agReader builds one antigravity reader, as the app does once per process, so
// successive reads share its cache.
func agReader(t *testing.T, client HTTPClient, now func() time.Time) quota.UsageClient {
	t.Helper()
	readers, err := Readers(client, now)
	if err != nil {
		t.Fatal(err)
	}
	return readers["antigravity"]
}

func summaryCalls(client *cloudCodeClient) int {
	count := 0
	for _, request := range client.requests {
		if strings.HasSuffix(request.URL.Path, ":retrieveUserQuotaSummary") {
			count++
		}
	}
	return count
}

func windowObservedAt(t *testing.T, state quota.State, name string) *time.Time {
	t.Helper()
	window, ok := byName(state)[name]
	if !ok {
		t.Fatalf("window %q missing from %v", name, windowNames(state))
	}
	return window.ObservedAt
}

// antigravity-weekly.js:16,119-122,152-157: a summary with windows is served
// from the cache for three minutes and refetched once they pass. Per-model rows
// are read every time. A served row carries the time it was read upstream, so
// the read model never shows a cached value as fresh.
func TestAntigravityWeeklyCacheHitThenExpiry(t *testing.T) {
	client := cloudCodeFixture(t, "antigravity")
	clock := &clock{now: googleNow}
	reader := agReader(t, client, clock.Now)
	credential := quota.Credential{AccessToken: "fixture-token"}

	first, err := reader.Usage(context.Background(), credential)
	if err != nil {
		t.Fatal(err)
	}
	if summaryCalls(client) != 1 {
		t.Fatalf("summary calls=%d want 1", summaryCalls(client))
	}
	for _, window := range first.Windows {
		if window.ObservedAt != nil {
			t.Fatalf("a freshly read window %q is marked cached at %v", window.Name, window.ObservedAt)
		}
	}

	// Upstream changes; inside the TTL the old weekly value is served, marked
	// with the time it was read, while the per-model rows are fresh.
	client.replies["retrieveUserQuotaSummary"] = cloudCodeReply{status: http.StatusOK, body: `{"groups":[
		{"displayName":"Gemini Models","buckets":[{"window":"weekly","remainingFraction":0.1,"resetTime":"2026-09-15T00:00:00Z"}]}]}`}
	clock.advance(weeklyCacheTTL - time.Second)
	hit, err := reader.Usage(context.Background(), credential)
	if err != nil {
		t.Fatal(err)
	}
	if summaryCalls(client) != 1 {
		t.Fatalf("summary refetched inside the TTL: calls=%d", summaryCalls(client))
	}
	if hit.ObservedAt != googleNow.Add(weeklyCacheTTL-time.Second) {
		t.Fatalf("read observedAt=%v", hit.ObservedAt)
	}
	wantWindow(t, hit, "gemini_weekly", "Gemini (Weekly)", 0.75, "2026-09-15T00:00:00Z")
	for _, name := range []string{"gemini_weekly", "gemini_session", "claude_gpt_weekly"} {
		if observed := windowObservedAt(t, hit, name); observed == nil || !observed.Equal(googleNow) {
			t.Fatalf("cached window %q observedAt=%v want %v", name, observed, googleNow)
		}
	}
	if observed := windowObservedAt(t, hit, "gemini-3.8-flash-high"); observed != nil {
		t.Fatalf("per-model row is marked cached: %v", observed)
	}

	// At exactly three minutes the entry has expired (expiresAt > Date.now()).
	clock.advance(time.Second)
	fresh, err := reader.Usage(context.Background(), credential)
	if err != nil {
		t.Fatal(err)
	}
	if summaryCalls(client) != 2 {
		t.Fatalf("summary not refetched after the TTL: calls=%d", summaryCalls(client))
	}
	wantWindow(t, fresh, "gemini_weekly", "Gemini (Weekly)", 0.1, "2026-09-15T00:00:00Z")
	if observed := windowObservedAt(t, fresh, "gemini_weekly"); observed != nil {
		t.Fatalf("a refetched window is marked cached at %v", observed)
	}
	if _, ok := byName(fresh)["claude_gpt_weekly"]; ok {
		t.Fatal("a row from the expired summary survived the refetch")
	}
}

// antigravity-weekly.js:143-150,156-160: an HTTP failure, a transport failure
// or a summary with no windows is not cached; the next read asks again.
func TestAntigravityWeeklyCacheSkipsEmptyAndFailed(t *testing.T) {
	for name, reply := range map[string]cloudCodeReply{
		"http":      {status: http.StatusInternalServerError, body: `{}`},
		"transport": {err: context.DeadlineExceeded},
		"empty":     {status: http.StatusOK, body: `{"groups":[]}`},
	} {
		t.Run(name, func(t *testing.T) {
			client := cloudCodeFixture(t, "antigravity")
			good := client.replies["retrieveUserQuotaSummary"]
			client.replies["retrieveUserQuotaSummary"] = reply
			reader := agReader(t, client, func() time.Time { return googleNow })
			credential := quota.Credential{AccessToken: "fixture-token"}
			if _, err := reader.Usage(context.Background(), credential); err != nil {
				t.Fatal(err)
			}
			client.replies["retrieveUserQuotaSummary"] = good
			state, err := reader.Usage(context.Background(), credential)
			if err != nil {
				t.Fatal(err)
			}
			if summaryCalls(client) != 2 {
				t.Fatalf("summary calls=%d want 2", summaryCalls(client))
			}
			wantWindow(t, state, "gemini_weekly", "Gemini (Weekly)", 0.75, "2026-09-15T00:00:00Z")
			if observed := windowObservedAt(t, state, "gemini_weekly"); observed != nil {
				t.Fatalf("fresh window marked cached at %v", observed)
			}
		})
	}
}

// antigravity-weekly.js:19-21: the key is token and project, so another
// account or project never sees this one's summary.
func TestAntigravityWeeklyCacheKey(t *testing.T) {
	client := cloudCodeFixture(t, "antigravity")
	reader := agReader(t, client, func() time.Time { return googleNow })
	for _, token := range []string{"fixture-token", "fixture-other", "fixture-token"} {
		if _, err := reader.Usage(context.Background(), quota.Credential{AccessToken: token}); err != nil {
			t.Fatal(err)
		}
	}
	if summaryCalls(client) != 2 {
		t.Fatalf("summary calls=%d want 2 (one per token)", summaryCalls(client))
	}
	client.replies["loadCodeAssist"] = cloudCodeReply{status: http.StatusOK, body: `{"cloudaicompanionProject":"p2","currentTier":{"name":"Pro"},"paidTier":{"id":"g1-pro-tier"}}`}
	if _, err := reader.Usage(context.Background(), quota.Credential{AccessToken: "fixture-token"}); err != nil {
		t.Fatal(err)
	}
	if summaryCalls(client) != 3 {
		t.Fatalf("summary calls=%d want 3 (new project)", summaryCalls(client))
	}
	if key := weeklyKey("fixture-token", []byte(`"p1"`)); strings.Contains(key, "fixture-token") {
		t.Fatalf("cache key holds the credential: %s", key)
	}
	if weeklyKey("t", []byte(`"p1"`)) == weeklyKey("t", nil) || weeklyKey("t", nil) != weeklyKey("t", []byte(`""`)) {
		t.Fatal("project scoping differs from `${projectId || \"\"}`")
	}
}

// google.js:225-266 reconciles the object the cache holds, so a 5h row zeroed
// because every model in its family was exhausted stays zeroed on later hits
// inside the TTL, even once the models recover. A caller never aliases the
// cached windows.
func TestAntigravityWeeklyCacheKeepsReconciledSession(t *testing.T) {
	client := cloudCodeFixture(t, "antigravity")
	client.replies["fetchAvailableModels"] = cloudCodeReply{status: http.StatusOK, body: `{"models":{
		"gemini-3.8-flash-low":{"quotaInfo":{"remainingFraction":0,"resetTime":"2026-09-13T10:00:00Z"}}}}`}
	clock := &clock{now: googleNow}
	reader := agReader(t, client, clock.Now)
	credential := quota.Credential{AccessToken: "fixture-token"}
	first, err := reader.Usage(context.Background(), credential)
	if err != nil {
		t.Fatal(err)
	}
	wantWindow(t, first, "gemini_session", "Gemini (5h)", 0, "2026-09-13T10:00:00Z")
	for _, window := range first.Windows {
		if window.Name == "gemini_session" {
			*window.Remaining = 0.5 // a caller's edit must not reach the cache
		}
	}

	client.replies["fetchAvailableModels"] = cloudCodeReply{status: http.StatusOK, body: `{"models":{
		"gemini-3.8-flash-low":{"quotaInfo":{"remainingFraction":0.6,"resetTime":"2026-09-13T10:00:00Z"}}}}`}
	clock.advance(time.Minute)
	hit, err := reader.Usage(context.Background(), credential)
	if err != nil {
		t.Fatal(err)
	}
	wantWindow(t, hit, "gemini-3.8-flash-low", "gemini-3.8-flash-low", 0.6, "2026-09-13T10:00:00Z")
	wantWindow(t, hit, "gemini_session", "Gemini (5h)", 0, "2026-09-13T10:00:00Z")
	if observed := windowObservedAt(t, hit, "gemini_session"); observed == nil || !observed.Equal(googleNow) {
		t.Fatalf("cached session observedAt=%v", observed)
	}
}

// blockingSummary holds every summary call until released and counts them; the
// other Cloud Code methods answer from the fixture. Safe for concurrent use.
type blockingSummary struct {
	mu      sync.Mutex
	fixture *cloudCodeClient
	release chan struct{}
	entered chan struct{}
	calls   int
}

func (b *blockingSummary) Do(request *http.Request) (*http.Response, error) {
	if strings.HasSuffix(request.URL.Path, ":retrieveUserQuotaSummary") {
		b.mu.Lock()
		b.calls++
		b.mu.Unlock()
		b.entered <- struct{}{}
		<-b.release
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.fixture.Do(request)
}

// antigravity-weekly.js:119-121,152: concurrent reads share one in-flight
// summary fetch. Run with -race.
func TestAntigravityWeeklyCacheSharesInFlightFetch(t *testing.T) {
	client := &blockingSummary{fixture: cloudCodeFixture(t, "antigravity"), release: make(chan struct{}), entered: make(chan struct{}, 8)}
	reader := agReader(t, client, func() time.Time { return googleNow })
	credential := quota.Credential{AccessToken: "fixture-token"}
	const readers = 4
	states := make([]quota.State, readers)
	errs := make([]error, readers)
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		states[0], errs[0] = reader.Usage(context.Background(), credential)
	}()
	<-client.entered
	for i := 1; i < readers; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			states[i], errs[i] = reader.Usage(context.Background(), credential)
		}(i)
	}
	// Let the joiners reach the in-flight entry before the fetch finishes.
	time.Sleep(50 * time.Millisecond)
	close(client.release)
	wg.Wait()
	if client.calls != 1 {
		t.Fatalf("summary calls=%d want 1", client.calls)
	}
	for i := range states {
		if errs[i] != nil {
			t.Fatal(errs[i])
		}
		wantWindow(t, states[i], "gemini_weekly", "Gemini (Weekly)", 0.75, "2026-09-15T00:00:00Z")
	}
}

// A reader waiting on another read's fetch gives up with its own context and
// returns the per-model rows without the overlay, as a failed summary does.
func TestAntigravityWeeklyCacheWaiterHonoursContext(t *testing.T) {
	client := &blockingSummary{fixture: cloudCodeFixture(t, "antigravity"), release: make(chan struct{}), entered: make(chan struct{}, 8)}
	reader := agReader(t, client, func() time.Time { return googleNow })
	credential := quota.Credential{AccessToken: "fixture-token"}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = reader.Usage(context.Background(), credential)
	}()
	<-client.entered
	ctx, cancel := context.WithCancel(context.Background())
	waiter := make(chan quota.State, 1)
	go func() {
		state, _ := reader.Usage(ctx, credential)
		waiter <- state
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	state := <-waiter
	close(client.release)
	<-done
	if _, ok := byName(state)["gemini_weekly"]; ok {
		t.Fatalf("a cancelled waiter returned the overlay: %v", windowNames(state))
	}
}

// deadlineClient records how long each request had before its context deadline.
type deadlineClient struct {
	mu      sync.Mutex
	budgets map[string]time.Duration
	reply   func(*http.Request) (int, string)
}

func (d *deadlineClient) Do(request *http.Request) (*http.Response, error) {
	deadline, ok := request.Context().Deadline()
	d.mu.Lock()
	if d.budgets == nil {
		d.budgets = map[string]time.Duration{}
	}
	budget := time.Duration(0)
	if ok {
		budget = time.Until(deadline)
	}
	d.budgets[request.URL.Host+request.URL.Path] = budget
	d.mu.Unlock()
	status, body := http.StatusOK, `{}`
	if d.reply != nil {
		status, body = d.reply(request)
	}
	return &http.Response{StatusCode: status, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func wantBudget(t *testing.T, client *deadlineClient, endpoint string, want time.Duration) {
	t.Helper()
	budget, ok := client.budgets[endpoint]
	if !ok {
		t.Fatalf("no request to %s (saw %v)", endpoint, client.budgets)
	}
	if budget > want || budget < want-2*time.Second {
		t.Fatalf("%s deadline in %v want %v", endpoint, budget, want)
	}
}

// shared.js:62 and google.js:56,107,138,295, antigravity-weekly.js:141: every
// Cloud Code quota call is bounded at 10s, not the shared 15s.
func TestCloudCodeCallsUseTenSecondTimeout(t *testing.T) {
	fixture := cloudCodeFixture(t, "antigravity")
	ag := &deadlineClient{reply: func(request *http.Request) (int, string) {
		method := request.URL.Path[strings.LastIndex(request.URL.Path, ":")+1:]
		reply := fixture.replies[method]
		return reply.status, reply.body
	}}
	if _, err := agReader(t, ag, nil).Usage(context.Background(), quota.Credential{AccessToken: "fixture-token"}); err != nil {
		t.Fatal(err)
	}
	for _, endpoint := range []string{
		"cloudcode-pa.googleapis.com/v1internal:loadCodeAssist",
		"daily-cloudcode-pa.googleapis.com/v1internal:fetchAvailableModels",
		"daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary",
	} {
		wantBudget(t, ag, endpoint, cloudCodeTimeout)
	}

	gemini := &deadlineClient{reply: func(request *http.Request) (int, string) {
		if strings.HasSuffix(request.URL.Path, ":loadCodeAssist") {
			return http.StatusOK, `{"cloudaicompanionProject":"p1","currentTier":{"name":"Free"}}`
		}
		return http.StatusOK, `{"buckets":[]}`
	}}
	readers, err := Readers(gemini, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readers["gemini-cli"].Usage(context.Background(), quota.Credential{AccessToken: "fixture-token"}); err != nil {
		t.Fatal(err)
	}
	wantBudget(t, gemini, "cloudcode-pa.googleapis.com/v1internal:loadCodeAssist", cloudCodeTimeout)
	wantBudget(t, gemini, "cloudcode-pa.googleapis.com/v1internal:retrieveUserQuota", cloudCodeTimeout)
	if cloudCodeTimeout != 10*time.Second {
		t.Fatalf("cloudCodeTimeout=%v want 10s", cloudCodeTimeout)
	}
}

// xiaomi-mimo.js:62 bounds the API-key usage read at 10s; a provider whose
// handler sets no timeout keeps the shared 15s bound (GitHub, github.js:26).
func TestPerProviderUsageTimeouts(t *testing.T) {
	client := &deadlineClient{reply: func(*http.Request) (int, string) {
		return http.StatusOK, `{"code":0,"data":{"percent":50}}`
	}}
	readers, err := Readers(client, nil)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = readers["xiaomi-mimo"].Usage(context.Background(), quota.Credential{AccessToken: "fixture-key"})
	wantBudget(t, client, "aistudio.xiaomimimo.com/open-apis/v1/user/usage", 10*time.Second)
	_, _ = readers["github"].Usage(context.Background(), quota.Credential{AccessToken: "fixture-token"})
	wantBudget(t, client, "api.github.com/copilot_internal/user", requestTimeout)
	if requestTimeout != 15*time.Second {
		t.Fatalf("shared requestTimeout changed to %v", requestTimeout)
	}
}
