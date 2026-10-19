// Package events owns bounded operator-facing live event delivery.
package events

import (
	"sync"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/telemetry"
)

const subscriberBuffer = 16

// Event is a non-durable UI invalidation/event notification.
type Event struct {
	ID        uint64    `json:"id"`
	Type      string    `json:"type"`
	CreatedAt time.Time `json:"createdAt"`
	Data      any       `json:"data"`
}

// Bus fans out bounded best-effort events. A slow browser loses notifications,
// then refreshes its query state; publishers never wait for consumers.
type Bus struct {
	mu          sync.Mutex
	nextID      uint64
	nextSubID   uint64
	subscribers map[uint64]chan Event
}

func New() *Bus { return &Bus{subscribers: make(map[uint64]chan Event)} }

// Publish sends one redacted event to current subscribers. Full subscriber
// queues shed the event instead of blocking inference or configuration writes.
func (b *Bus) Publish(eventType string, data any) {
	if b == nil || eventType == "" {
		return
	}
	clean := telemetry.Redact(data)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.nextID++
	event := Event{ID: b.nextID, Type: eventType, CreatedAt: time.Now().UTC(), Data: clean}
	for _, subscriber := range b.subscribers {
		select {
		case subscriber <- event:
		default:
		}
	}
}

// Subscribe returns a bounded stream and an idempotent unsubscribe function.
func (b *Bus) Subscribe() (<-chan Event, func()) {
	if b == nil {
		closed := make(chan Event)
		close(closed)
		return closed, func() {}
	}
	b.mu.Lock()
	b.nextSubID++
	id := b.nextSubID
	ch := make(chan Event, subscriberBuffer)
	b.subscribers[id] = ch
	b.mu.Unlock()
	var once sync.Once
	return ch, func() {
		once.Do(func() {
			b.mu.Lock()
			delete(b.subscribers, id)
			close(ch)
			b.mu.Unlock()
		})
	}
}
