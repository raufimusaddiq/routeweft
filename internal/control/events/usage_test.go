package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"testing"
	"time"
)

func TestUsageFeedSubscribeUnsubscribe(t *testing.T) {
	feed := NewUsageFeed()
	sub, err := feed.Subscribe()
	if err != nil {
		t.Fatal(err)
	}
	if feed.Subscribers() != 1 {
		t.Fatalf("subscribers=%d", feed.Subscribers())
	}
	feed.Publish(UsageEvent{RequestID: "r1", Status: 200})
	got := <-sub.C()
	if got.RequestID != "r1" || got.ID != 1 {
		t.Fatalf("event=%+v", got)
	}
	sub.Close()
	sub.Close() // idempotent
	if _, open := <-sub.C(); open {
		t.Fatal("channel not closed")
	}
	if feed.Subscribers() != 0 {
		t.Fatalf("subscriber leaked: %d", feed.Subscribers())
	}
	feed.Publish(UsageEvent{RequestID: "r2"}) // no subscribers: no-op, no panic
}

func TestUsageFeedCap(t *testing.T) {
	feed := NewUsageFeed()
	var subs []*UsageSubscription
	for i := 0; i < MaxUsageSubscribers; i++ {
		sub, err := feed.Subscribe()
		if err != nil {
			t.Fatalf("rejected below cap at %d: %v", i, err)
		}
		subs = append(subs, sub)
	}
	if _, err := feed.Subscribe(); !errors.Is(err, ErrTooManySubscribers) {
		t.Fatalf("over cap err=%v", err)
	}
	subs[0].Close()
	sub, err := feed.Subscribe()
	if err != nil {
		t.Fatalf("slot not released: %v", err)
	}
	sub.Close()
	for _, s := range subs {
		s.Close()
	}
	if feed.Subscribers() != 0 {
		t.Fatal("subscribers leaked")
	}
}

// A subscriber that never reads must be cut off without delaying Publish or
// starving a healthy subscriber.
func TestUsageFeedSlowClientIsIsolated(t *testing.T) {
	feed := NewUsageFeed()
	slow, _ := feed.Subscribe()
	fast, _ := feed.Subscribe()
	defer fast.Close()

	received := make(chan int)
	go func() {
		n := 0
		for range fast.C() {
			n++
			if n == usageBuffer*3 {
				break
			}
		}
		received <- n
	}()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < usageBuffer*3; i++ {
			feed.Publish(UsageEvent{RequestID: "r"})
			for len(fast.ch) > usageBuffer/2 { // the healthy reader keeps pace
				runtime.Gosched()
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publisher blocked behind a slow subscriber")
	}
	if !slow.Lagged() {
		t.Fatal("slow subscriber not marked lagged")
	}
	if feed.Dropped() != 1 {
		t.Fatalf("dropped=%d want 1", feed.Dropped())
	}
	if feed.Subscribers() != 1 {
		t.Fatalf("subscribers=%d want 1", feed.Subscribers())
	}
	// The slow channel is closed after draining what it buffered.
	n := 0
	for range slow.C() {
		n++
	}
	if n != usageBuffer {
		t.Fatalf("slow buffered %d want %d", n, usageBuffer)
	}
	if got := <-received; got != usageBuffer*3 {
		t.Fatalf("healthy subscriber saw %d of %d", got, usageBuffer*3)
	}
	slow.Close() // safe after a lag drop
}

func TestUsageFeedConcurrentPublishSubscribe(t *testing.T) {
	feed := NewUsageFeed()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				feed.Publish(UsageEvent{RequestID: "r"})
			}
		}()
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				sub, err := feed.Subscribe()
				if err != nil {
					continue
				}
				select {
				case <-sub.C():
				default:
				}
				sub.Close()
			}
		}()
	}
	wg.Wait()
	if feed.Subscribers() != 0 {
		t.Fatalf("subscribers=%d", feed.Subscribers())
	}
}

// The wire shape is the contract with the "metadata only" requirement: only
// these keys may ever appear. Adding a field must be a deliberate edit here.
func TestUsageEventWireShapeIsMetadataOnly(t *testing.T) {
	raw, err := json.Marshal(UsageEvent{ID: 9, RequestID: "r", ProviderID: "p", ModelID: "m", ConnectionID: "c", APIKeyID: "k", Status: 200, DurationMS: 5, InputTokens: 1, OutputTokens: 2, CreatedAt: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]any
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	allowed := map[string]bool{"requestId": true, "providerId": true, "modelId": true, "connectionId": true, "apiKeyId": true, "status": true, "durationMs": true, "inputTokens": true, "outputTokens": true, "createdAt": true}
	for key := range keys {
		if !allowed[key] {
			t.Fatalf("unexpected key %q in %s", key, raw)
		}
	}
	if len(keys) != len(allowed) {
		t.Fatalf("keys=%v", keys)
	}
}

// BenchmarkUsageFeedPublish measures Publish plus delivery to n healthy
// subscribers; each is drained in the loop so none is dropped as slow.
func BenchmarkUsageFeedPublish(b *testing.B) {
	for _, n := range []int{0, 1, MaxUsageSubscribers} {
		b.Run(fmt.Sprintf("%d-subscribers", n), func(b *testing.B) {
			feed := NewUsageFeed()
			subs := make([]*UsageSubscription, n)
			for i := range subs {
				subs[i], _ = feed.Subscribe()
			}
			event := UsageEvent{RequestID: "r", ProviderID: "p", ModelID: "m", Status: 200, InputTokens: 100}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				feed.Publish(event)
				for _, sub := range subs {
					<-sub.C()
				}
			}
			b.StopTimer()
			if feed.Subscribers() != n {
				b.Fatalf("subscribers dropped during benchmark: %d of %d", feed.Subscribers(), n)
			}
		})
	}
}
