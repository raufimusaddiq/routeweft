package events

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// MaxUsageSubscribers bounds concurrent live usage streams. A subscriber is
	// a goroutine, a socket and a buffer, so the list must not grow unbounded.
	MaxUsageSubscribers = 32
	usageBuffer         = 64
)

// ErrTooManySubscribers is returned by Subscribe at the concurrency cap.
var ErrTooManySubscribers = errors.New("too many live usage subscribers")

// UsageEvent is the live projection of one completed request. It is metadata
// only and a strict subset of what the usage endpoints already return: no
// request or response body, no prompt text, no credential material. New
// fields need the same property.
type UsageEvent struct {
	ID           uint64 `json:"-"`
	RequestID    string `json:"requestId"`
	ProviderID   string `json:"providerId,omitempty"`
	ModelID      string `json:"modelId,omitempty"`
	ConnectionID string `json:"connectionId,omitempty"`
	APIKeyID     string `json:"apiKeyId,omitempty"`
	Status       int    `json:"status"`
	DurationMS   int64  `json:"durationMs"`
	InputTokens  int64  `json:"inputTokens"`
	OutputTokens int64  `json:"outputTokens"`
	// Termination and UsageUnobserved qualify the counts above: an unobserved
	// request carries no measurement, and zero must not be read as one.
	Termination     string    `json:"termination,omitempty"`
	UsageUnobserved bool      `json:"usageUnobserved,omitempty"`
	CreatedAt       time.Time `json:"createdAt"`
}

// UsageFeed fans completed-request events out to live dashboard streams.
//
// Publish is called on the inference path and never waits on a consumer: it
// takes a short mutex that only Subscribe/Close contend for, does one
// non-blocking send per subscriber, and returns. A subscriber whose buffer is
// full is removed on the spot instead of being waited for or silently losing
// events, because a gap would leave the UI with stale rows. The stream ends,
// and the browser's EventSource reconnects and reloads recorded history.
type UsageFeed struct {
	mu      sync.Mutex
	nextID  uint64
	nextSub uint64
	subs    map[uint64]*UsageSubscription
	// active mirrors len(subs) so Publish with no viewers is one atomic load.
	active atomic.Int64
	// dropped counts subscribers removed for falling behind.
	dropped atomic.Uint64
}

func NewUsageFeed() *UsageFeed { return &UsageFeed{subs: make(map[uint64]*UsageSubscription)} }

// UsageSubscription is one consumer's bounded stream.
type UsageSubscription struct {
	feed   *UsageFeed
	id     uint64
	ch     chan UsageEvent
	lagged atomic.Bool
	once   sync.Once
}

// C delivers events. It is closed when the subscription ends, whether by Close
// or because the consumer fell behind (see Lagged).
func (s *UsageSubscription) C() <-chan UsageEvent { return s.ch }

// Lagged reports that the feed removed this subscriber for being too slow.
func (s *UsageSubscription) Lagged() bool { return s.lagged.Load() }

// Close releases the subscription. It is idempotent and safe after a lag drop.
func (s *UsageSubscription) Close() {
	s.once.Do(func() {
		f := s.feed
		f.mu.Lock()
		defer f.mu.Unlock()
		if _, ok := f.subs[s.id]; ok {
			delete(f.subs, s.id)
			f.active.Store(int64(len(f.subs)))
			close(s.ch)
		}
	})
}

// Subscribe registers a consumer, or fails with ErrTooManySubscribers.
func (f *UsageFeed) Subscribe() (*UsageSubscription, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.subs) >= MaxUsageSubscribers {
		return nil, ErrTooManySubscribers
	}
	f.nextSub++
	sub := &UsageSubscription{feed: f, id: f.nextSub, ch: make(chan UsageEvent, usageBuffer)}
	f.subs[sub.id] = sub
	f.active.Store(int64(len(f.subs)))
	return sub, nil
}

// Publish delivers one event. It is a no-op without subscribers and never blocks.
func (f *UsageFeed) Publish(event UsageEvent) {
	if f == nil || f.active.Load() == 0 {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.nextID++
	event.ID = f.nextID
	for id, sub := range f.subs {
		select {
		case sub.ch <- event:
		default:
			sub.lagged.Store(true)
			delete(f.subs, id)
			close(sub.ch)
			f.dropped.Add(1)
		}
	}
	f.active.Store(int64(len(f.subs)))
}

// Subscribers is the current number of live streams.
func (f *UsageFeed) Subscribers() int { return int(f.active.Load()) }

// Dropped is how many subscribers were removed for falling behind.
func (f *UsageFeed) Dropped() uint64 { return f.dropped.Load() }
