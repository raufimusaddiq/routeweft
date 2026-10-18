package api

import (
	"sync"
	"time"
)

// loginThrottle bounds failed admin login attempts per client so an exposed
// control plane cannot be brute-forced at unbounded rate (PRD-SEC-001). The
// window/limit are fixed policy, not operator knobs.
const (
	loginMaxFailures = 8
	loginWindow      = 5 * time.Minute
)

type attemptWindow struct {
	failures int
	resetAt  time.Time
}

// throttler tracks failed login attempts per key.
type throttler struct {
	mu      sync.Mutex
	windows map[string]attemptWindow
	now     func() time.Time
}

func newThrottler() *throttler {
	return &throttler{windows: make(map[string]attemptWindow), now: time.Now}
}

func (t *throttler) retryAfter(key string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	window, ok := t.windows[key]
	now := t.now()
	if !ok {
		return 0
	}
	if !now.Before(window.resetAt) {
		delete(t.windows, key)
		return 0
	}
	if window.failures < loginMaxFailures {
		return 0
	}
	return int((window.resetAt.Sub(now) + time.Second - 1) / time.Second)
}

// fail records one failed attempt for key.
func (t *throttler) fail(key string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	window, ok := t.windows[key]
	if !ok || !now.Before(window.resetAt) {
		window = attemptWindow{resetAt: now.Add(loginWindow)}
	}
	window.failures++
	t.windows[key] = window
}

// succeed clears the failure window for key.
func (t *throttler) succeed(key string) {
	t.mu.Lock()
	delete(t.windows, key)
	t.mu.Unlock()
}
