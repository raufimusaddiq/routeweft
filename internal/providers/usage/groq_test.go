package usage

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/quota"
)

// groqNow is the clock reader() installs; reset durations are relative to it.
var groqNow = time.Date(2026, 10, 20, 12, 0, 0, 0, time.UTC)

// groqFixture loads testdata/groq.json, the oracle's pinned models response.
func groqFixture(t *testing.T) *stubClient {
	t.Helper()
	raw, err := os.ReadFile("testdata/groq.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Status  int               `json:"status"`
		Headers map[string]string `json:"headers"`
		Body    json.RawMessage   `json:"body"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	headers := http.Header{}
	for key, value := range fixture.Headers {
		headers.Set(key, value)
	}
	return &stubClient{status: fixture.Status, body: string(fixture.Body), headers: headers}
}

func groqUsage(t *testing.T, stub quota.UsageClient) (quota.State, error) {
	t.Helper()
	return stub.Usage(context.Background(), quota.Credential{AccessToken: "fixture-key"})
}

// TestGroqPinnedHeadersBuildRequestAndTokenWindows is the #211 regression: a
// successful models read must become both rate-limit windows, not a failure.
func TestGroqPinnedHeadersBuildRequestAndTokenWindows(t *testing.T) {
	stub := groqFixture(t)
	state, err := groqUsage(t, reader(t, "groq", stub))
	if err != nil {
		t.Fatalf("pinned success fixture failed: %v", err)
	}
	if stub.request.Method != http.MethodGet || stub.request.URL.String() != "https://api.groq.com/openai/v1/models" {
		t.Fatalf("request=%s %s", stub.request.Method, stub.request.URL)
	}
	if got := stub.request.Header.Get("Authorization"); got != "Bearer fixture-key" {
		t.Fatalf("authorization=%q", got)
	}
	if state.Plan != "Groq" || len(state.Windows) != 2 {
		t.Fatalf("state=%+v", state)
	}
	for i, want := range []struct {
		name      string
		remaining float64
		reset     time.Duration
	}{
		{"Requests", 14370.0 / 14400, 2*time.Minute + 59560*time.Millisecond},
		{"Tokens", 17997.0 / 18000, 7660 * time.Millisecond},
	} {
		w := state.Windows[i]
		if w.Name != want.name || w.Unlimited || w.Remaining == nil || *w.Remaining != want.remaining {
			t.Fatalf("window %d=%+v want %s %v", i, w, want.name, want.remaining)
		}
		if w.ResetAt == nil || !w.ResetAt.Equal(groqNow.Add(want.reset)) {
			t.Fatalf("%s reset=%v want %v", want.name, w.ResetAt, groqNow.Add(want.reset))
		}
	}
	if got := state.DeriveStatus(groqNow); got != quota.StatusAvailable {
		t.Fatalf("status=%s", got)
	}
}

// TestGroqZeroRemainingIsExhaustedUntilReset: a reported zero is real data and
// holds the account out only until the reported reset.
func TestGroqZeroRemainingIsExhaustedUntilReset(t *testing.T) {
	stub := groqFixture(t)
	stub.headers.Set("x-ratelimit-remaining-tokens", "0")
	state, err := groqUsage(t, reader(t, "groq", stub))
	if err != nil {
		t.Fatal(err)
	}
	tokens := state.Windows[1]
	if tokens.Remaining == nil || *tokens.Remaining != 0 || tokens.Unlimited {
		t.Fatalf("tokens=%+v", tokens)
	}
	if got := state.DeriveStatus(groqNow); got != quota.StatusExhausted {
		t.Fatalf("status=%s", got)
	}
	if got := state.DeriveStatus(groqNow.Add(8 * time.Second)); got != quota.StatusAvailable {
		t.Fatalf("status after reset=%s", got)
	}
}

// TestGroqAbsentHeadersAreNotReported: a working key with no bucket is an
// explicit "not reported" error, never an empty healthy or unlimited plan.
func TestGroqAbsentHeadersAreNotReported(t *testing.T) {
	_, err := groqUsage(t, reader(t, "groq", &stubClient{status: http.StatusOK, body: `{"data":[]}`}))
	if err == nil || !strings.Contains(err.Error(), "no rate-limit headers") {
		t.Fatalf("err=%v", err)
	}

	// One absent bucket drops only that bucket, as the oracle does.
	stub := groqFixture(t)
	stub.headers.Del("x-ratelimit-limit-tokens")
	stub.headers.Del("x-ratelimit-remaining-tokens")
	state, err := groqUsage(t, reader(t, "groq", stub))
	if err != nil || len(state.Windows) != 1 || state.Windows[0].Name != "Requests" {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

// TestGroqMalformedHeadersNeverInventQuota: empty, non-numeric, negative, zero
// limits and half-present buckets are malformed. With no valid bucket the read
// fails; beside a valid one the bad bucket stays unbounded, never zero/unlimited.
func TestGroqMalformedHeadersNeverInventQuota(t *testing.T) {
	for name, mutate := range map[string]func(http.Header){
		"empty":        func(h http.Header) { h.Set("x-ratelimit-remaining-requests", "") },
		"non-numeric":  func(h http.Header) { h.Set("x-ratelimit-limit-requests", "lots") },
		"negative":     func(h http.Header) { h.Set("x-ratelimit-remaining-requests", "-1") },
		"zero limit":   func(h http.Header) { h.Set("x-ratelimit-limit-requests", "0") },
		"infinite":     func(h http.Header) { h.Set("x-ratelimit-limit-requests", "Inf") },
		"half present": func(h http.Header) { h.Del("x-ratelimit-remaining-requests") },
	} {
		t.Run(name, func(t *testing.T) {
			stub := groqFixture(t)
			mutate(stub.headers)
			state, err := groqUsage(t, reader(t, "groq", stub))
			if err != nil {
				t.Fatal(err)
			}
			requests := state.Windows[0]
			if requests.Name != "Requests" || requests.Remaining != nil || requests.Unlimited {
				t.Fatalf("malformed bucket=%+v", requests)
			}
			if tokens := state.Windows[1]; tokens.Remaining == nil {
				t.Fatalf("valid bucket lost: %+v", tokens)
			}

			stub.headers.Del("x-ratelimit-limit-tokens")
			stub.headers.Del("x-ratelimit-remaining-tokens")
			if _, err := groqUsage(t, reader(t, "groq", stub)); err == nil || !strings.Contains(err.Error(), "malformed") {
				t.Fatalf("only a malformed bucket: err=%v", err)
			}
		})
	}

	// A malformed reset keeps the counts but invents no reset time.
	stub := groqFixture(t)
	stub.headers.Set("x-ratelimit-reset-requests", "soon")
	state, err := groqUsage(t, reader(t, "groq", stub))
	if err != nil || state.Windows[0].Remaining == nil || state.Windows[0].ResetAt != nil {
		t.Fatalf("state=%+v err=%v", state, err)
	}
}

type failingClient struct{ err error }

func (f failingClient) Do(*http.Request) (*http.Response, error) { return nil, f.err }

// TestGroqUpstreamErrorsAreExplicit: auth, HTTP and transport failures (a
// timeout included) are errors even when rate-limit headers ride along.
func TestGroqUpstreamErrorsAreExplicit(t *testing.T) {
	for status, want := range map[int]string{
		http.StatusUnauthorized:        "authentication failed",
		http.StatusForbidden:           "authentication failed",
		http.StatusTooManyRequests:     "HTTP 429",
		http.StatusInternalServerError: "HTTP 500",
	} {
		stub := groqFixture(t)
		stub.status = status
		if _, err := groqUsage(t, reader(t, "groq", stub)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("status %d: err=%v", status, err)
		}
	}

	readers, err := Readers(failingClient{err: context.DeadlineExceeded}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := groqUsage(t, readers["groq"]); err == nil || !strings.Contains(err.Error(), context.DeadlineExceeded.Error()) {
		t.Fatalf("timeout err=%v", err)
	}
}
