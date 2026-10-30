package live

import (
	"testing"
	"time"
)

func fixed() (*Tracker, *time.Time) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	tracker := New()
	tracker.SetClock(func() time.Time { return now })
	return tracker, &now
}

func TestInFlightRequestsAreCountedPerAccountAndModel(t *testing.T) {
	tracker, _ := fixed()
	endA := tracker.Begin("openai", "gpt-4o", "acct-1")
	endB := tracker.Begin("openai", "gpt-4o", "acct-1")
	endC := tracker.Begin("claude", "sonnet", "acct-2")
	snapshot := tracker.Snapshot(func(id string) string { return "name-of-" + id })
	if len(snapshot.ActiveRequests) != 2 || snapshot.ActiveRequests[0] != (Active{"sonnet", "claude", "name-of-acct-2", 1}) || snapshot.ActiveRequests[1] != (Active{"gpt-4o", "openai", "name-of-acct-1", 2}) {
		t.Fatalf("active: %+v", snapshot.ActiveRequests)
	}
	endA(false)
	endA(false) // ending twice does not take a second request off
	endB(false)
	endC(false)
	if got := tracker.Snapshot(nil).ActiveRequests; len(got) != 0 {
		t.Fatalf("still active: %+v", got)
	}
}

func TestAnUnendedRequestExpiresAfterAMinute(t *testing.T) {
	tracker, now := fixed()
	tracker.Begin("openai", "gpt", "a")
	*now = now.Add(PendingTimeout - time.Second)
	if len(tracker.Snapshot(nil).ActiveRequests) != 1 {
		t.Fatal("expired too early")
	}
	*now = now.Add(2 * time.Second)
	if len(tracker.Snapshot(nil).ActiveRequests) != 0 {
		t.Fatal("a leaked request must not show as active forever")
	}
}

func TestTheLastFailingProviderIsReportedForTenSeconds(t *testing.T) {
	tracker, now := fixed()
	end := tracker.Begin("OpenAI", "gpt", "a")
	end(true)
	if got := tracker.Snapshot(nil).ErrorProvider; got != "openai" {
		t.Fatalf("error provider %q", got)
	}
	*now = now.Add(ErrorWindow + time.Second)
	if got := tracker.Snapshot(nil).ErrorProvider; got != "" {
		t.Fatalf("error provider %q after the window", got)
	}
	tracker.Begin("x", "m", "a")(false)
	if tracker.Snapshot(nil).ErrorProvider != "" {
		t.Fatal("a success is not an error")
	}
}

func TestRecentRequestsAreNewestFirstDedupedBoundedAndSkipEmptyOnes(t *testing.T) {
	tracker, now := fixed()
	tracker.Finish(Recent{Model: "m", Provider: "p", PromptTokens: 0, CompletionTokens: 0, Status: "ok"}) // moved no tokens: not listed
	for i := 0; i < RingCap+10; i++ {
		*now = now.Add(time.Minute)
		tracker.Finish(Recent{Model: "m", Provider: "p", PromptTokens: int64(i + 1), CompletionTokens: 1, Status: "ok"})
	}
	// The same model, provider, counts and minute collapse into one entry.
	*now = now.Add(time.Second)
	tracker.Finish(Recent{Model: "dup", Provider: "p", PromptTokens: 5, CompletionTokens: 5})
	tracker.Finish(Recent{Model: "dup", Provider: "p", PromptTokens: 5, CompletionTokens: 5})
	snapshot := tracker.Snapshot(nil)
	if len(snapshot.Recent) != RecentShown {
		t.Fatalf("listed %d, want %d", len(snapshot.Recent), RecentShown)
	}
	if snapshot.Recent[0].Model != "dup" || snapshot.Recent[1].Model != "m" || snapshot.Recent[1].PromptTokens != int64(RingCap+10) {
		t.Fatalf("not newest first, collapsed: %+v %+v", snapshot.Recent[0], snapshot.Recent[1])
	}
	count := 0
	for _, item := range snapshot.Recent {
		if item.Model == "dup" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%d copies of a repeated request", count)
	}
	if len(tracker.ring) != RingCap {
		t.Fatalf("ring holds %d, want %d", len(tracker.ring), RingCap)
	}
}

func TestOnChangeFiresOnBeginEndAndFinish(t *testing.T) {
	tracker, _ := fixed()
	changes := 0
	tracker.OnChange = func() { changes++ }
	end := tracker.Begin("p", "m", "a")
	tracker.Finish(Recent{Model: "m", Provider: "p", PromptTokens: 1})
	end(false)
	end(false)
	if changes != 3 {
		t.Fatalf("changes=%d, want 3 (begin, finish, one end)", changes)
	}
}
