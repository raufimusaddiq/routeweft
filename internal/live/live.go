// Package live tracks what the gateway is doing right now: the requests in flight
// per account and model, and the few most recent ones. It is the reference's
// pending-request counter and recent ring (usageRepo.js:14-15,172-259), kept in
// memory, bounded, and never persisted.
package live

import (
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	// PendingTimeout drops an in-flight count nobody ended, so a leaked request
	// cannot show as active forever (usageRepo.js:14,193-201).
	PendingTimeout = time.Minute
	// RingCap is how many finished requests are remembered (usageRepo.js:15).
	RingCap = 50
	// RecentShown is how many of them a snapshot lists (usageRepo.js:255).
	RecentShown = 20
	// ErrorWindow is how long the last failing provider is reported (usageRepo.js:257).
	ErrorWindow = 10 * time.Second
)

// Recent is one finished request in the ring.
type Recent struct {
	Time             time.Time `json:"timestamp"`
	Model            string    `json:"model"`
	Provider         string    `json:"provider"`
	PromptTokens     int64     `json:"promptTokens"`
	CompletionTokens int64     `json:"completionTokens"`
	Status           string    `json:"status"`
}

// Active is requests in flight on one account for one model.
type Active struct {
	Model    string `json:"model"`
	Provider string `json:"provider"`
	Account  string `json:"account"`
	Count    int    `json:"count"`
}

// Snapshot is what the live view reads.
type Snapshot struct {
	ActiveRequests []Active `json:"activeRequests"`
	Recent         []Recent `json:"recentRequests"`
	ErrorProvider  string   `json:"errorProvider"`
}

type flight struct {
	provider, model, account string
	started                  time.Time
}

// Tracker counts requests in flight and remembers the latest finished ones. It is
// safe for concurrent use; every method is O(in-flight) at worst.
type Tracker struct {
	mu       sync.Mutex
	now      func() time.Time
	next     uint64
	inflight map[uint64]flight
	ring     []Recent
	errProv  string
	errAt    time.Time
	// OnChange is called after the counts change, outside the lock. It must not
	// block; the app coalesces it into one live event.
	OnChange func()
}

// New builds a tracker.
func New() *Tracker { return &Tracker{now: time.Now, inflight: map[uint64]flight{}} }

// SetClock replaces the clock, for tests.
func (t *Tracker) SetClock(now func() time.Time) { t.now = now }

// Begin notes a request sent to provider/model on account and returns the function
// that ends it. End is idempotent. failed marks the provider as the latest to fail.
func (t *Tracker) Begin(provider, model, account string) (end func(failed bool)) {
	t.mu.Lock()
	t.expireLocked()
	t.next++
	id := t.next
	t.inflight[id] = flight{provider: provider, model: model, account: account, started: t.now()}
	t.mu.Unlock()
	t.changed()
	return func(failed bool) {
		t.mu.Lock()
		_, live := t.inflight[id]
		delete(t.inflight, id)
		if live && failed && provider != "" {
			t.errProv, t.errAt = strings.ToLower(provider), t.now()
		}
		t.mu.Unlock()
		if live {
			t.changed()
		}
	}
}

// Finish remembers a finished request in the ring. A request that moved no tokens
// is not listed (usageRepo.js:248).
func (t *Tracker) Finish(recent Recent) {
	if recent.PromptTokens == 0 && recent.CompletionTokens == 0 {
		return
	}
	t.mu.Lock()
	if recent.Time.IsZero() {
		recent.Time = t.now()
	}
	t.ring = append(t.ring, recent)
	if len(t.ring) > RingCap {
		t.ring = append([]Recent(nil), t.ring[len(t.ring)-RingCap:]...)
	}
	t.mu.Unlock()
	t.changed()
}

func (t *Tracker) changed() {
	if t.OnChange != nil {
		t.OnChange()
	}
}

// expireLocked drops flights older than PendingTimeout.
func (t *Tracker) expireLocked() {
	cutoff := t.now().Add(-PendingTimeout)
	for id, f := range t.inflight {
		if f.started.Before(cutoff) {
			delete(t.inflight, id)
		}
	}
}

// Snapshot lists what is in flight, grouped by account and model, and the most
// recent finished requests, newest first, repeats within a minute collapsed
// (usageRepo.js:235-255). name turns an account id into its label.
func (t *Tracker) Snapshot(name func(account string) string) Snapshot {
	t.mu.Lock()
	t.expireLocked()
	type key struct{ account, provider, model string }
	counts := map[key]int{}
	for _, f := range t.inflight {
		counts[key{f.account, f.provider, f.model}]++
	}
	ring := append([]Recent(nil), t.ring...)
	errProv := ""
	if t.now().Sub(t.errAt) < ErrorWindow {
		errProv = t.errProv
	}
	t.mu.Unlock()

	active := make([]Active, 0, len(counts))
	for k, count := range counts {
		label := k.account
		if name != nil {
			label = name(k.account)
		}
		active = append(active, Active{Model: k.model, Provider: k.provider, Account: label, Count: count})
	}
	sort.Slice(active, func(i, j int) bool {
		a, b := active[i], active[j]
		if a.Provider != b.Provider {
			return a.Provider < b.Provider
		}
		if a.Model != b.Model {
			return a.Model < b.Model
		}
		return a.Account < b.Account
	})
	sort.SliceStable(ring, func(i, j int) bool { return ring[i].Time.After(ring[j].Time) })
	seen := map[string]bool{}
	recent := make([]Recent, 0, RecentShown)
	for _, item := range ring {
		minute := item.Time.UTC().Format("2006-01-02T15:04")
		id := item.Model + "|" + item.Provider + "|" + minute + "|" + itoa(item.PromptTokens) + "|" + itoa(item.CompletionTokens)
		if seen[id] {
			continue
		}
		seen[id] = true
		recent = append(recent, item)
		if len(recent) == RecentShown {
			break
		}
	}
	return Snapshot{ActiveRequests: active, Recent: recent, ErrorProvider: errProv}
}

func itoa(value int64) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits [20]byte
	i := len(digits)
	for value > 0 {
		i--
		digits[i] = byte('0' + value%10)
		value /= 10
	}
	if negative {
		i--
		digits[i] = '-'
	}
	return string(digits[i:])
}
