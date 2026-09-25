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

// allow reports whether another attempt may proceed for key.
func (t *throttler) allow(key string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	window, ok := t.windows[key]
	if !ok {
		return true
	}
	if !t.now().Before(window.resetAt) {
		delete(t.windows, key)
		return true
	}
	return window.failures < loginMaxFailures
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
