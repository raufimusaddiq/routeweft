package app

import (
	"context"
	"sync"
	"time"

	controlevents "github.com/raufimusaddiq/routeweft/internal/control/events"
	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/live"
)

// liveView feeds the live view of what the gateway is doing now: it names accounts
// for the snapshot and tells the event channel when the counts moved, at most once
// per liveEventDelay (the reference coalesces the same way, usageRepo.js:58-66).
type liveView struct {
	tracker *live.Tracker
	events  *controlevents.Bus
	creds   func() *credentials.Store

	mu      sync.Mutex
	names   map[string]string
	namesAt time.Time
	pending bool
}

const (
	liveEventDelay = 150 * time.Millisecond
	// accountNameTTL is how long a connection's name is remembered (usageRepo.js:16).
	accountNameTTL = 30 * time.Second
)

func newLiveView(events *controlevents.Bus, creds func() *credentials.Store) *liveView {
	view := &liveView{tracker: live.New(), events: events, creds: creds}
	view.tracker.OnChange = view.changed
	return view
}

// changed schedules one "usage.pending" event; further changes inside the delay ride on it.
func (v *liveView) changed() {
	v.mu.Lock()
	if v.pending {
		v.mu.Unlock()
		return
	}
	v.pending = true
	v.mu.Unlock()
	time.AfterFunc(liveEventDelay, func() {
		v.mu.Lock()
		v.pending = false
		v.mu.Unlock()
		if v.events != nil {
			v.events.Publish("usage.pending", v.snapshot())
		}
	})
}

// account is the label of a connection: its name, else its id's first characters
// (usageRepo.js:223).
func (v *liveView) account(id string) string {
	v.mu.Lock()
	defer v.mu.Unlock()
	if time.Since(v.namesAt) > accountNameTTL || v.names == nil {
		names := map[string]string{}
		if store := v.creds(); store != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			if connections, err := store.ListConnections(ctx, ""); err == nil {
				for _, connection := range connections {
					names[connection.ID] = connection.Name
				}
			}
			cancel()
		}
		v.names, v.namesAt = names, time.Now()
	}
	if name := v.names[id]; name != "" {
		return name
	}
	if len(id) > 8 {
		id = id[:8]
	}
	return "Account " + id + "..."
}

func (v *liveView) snapshot() live.Snapshot { return v.tracker.Snapshot(v.account) }
