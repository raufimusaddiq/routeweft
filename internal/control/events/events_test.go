package events

import (
	"testing"
	"time"
)

func TestBoundedFanoutAndRedaction(t *testing.T) {
	bus := New()
	stream, unsubscribe := bus.Subscribe()
	defer unsubscribe()
	bus.Publish("request.completed", map[string]any{"requestId": "r1", "Authorization": "secret"})
	select {
	case event := <-stream:
		if event.Type != "request.completed" || event.ID != 1 || event.CreatedAt.IsZero() {
			t.Fatalf("unexpected event: %+v", event)
		}
		data, ok := event.Data.(map[string]any)
		if !ok || data["Authorization"] != "[redacted]" {
			t.Fatalf("event data was not redacted: %#v", event.Data)
		}
	case <-time.After(time.Second):
		t.Fatal("event was not delivered")
	}
	for i := 0; i < subscriberBuffer+8; i++ {
		bus.Publish("traffic", map[string]int{"n": i})
	}
	if got := len(stream); got != subscriberBuffer {
		t.Fatalf("subscriber buffer=%d want %d", got, subscriberBuffer)
	}
}
