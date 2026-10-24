package wire

import (
	"container/list"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
)

// The pin's session ids are random and live in process memory: one per
// connection (utils/sessionManager.js:15,45-66) and one per opening assistant
// text (:88,183-198). Both are stable while the process runs and the entry is
// used, and new after a restart or after sitting idle past the TTL. They are
// swept every cleanup interval (:18-26,262-270) and capped, evicting the oldest
// insertion (:56-61,192-194). Routeweft keeps the same lifetime.
const (
	sessionTTL           = 2 * time.Hour    // runtimeConfig.js:28
	sessionSweepInterval = 30 * time.Minute // runtimeConfig.js:29
	connectionSessionCap = 1000             // :58
	assistantSessionCap  = 5000             // :91
)

// sessionNow is the store clock; tests replace it.
var sessionNow = time.Now

type sessionEntry struct {
	key      string
	id       string
	lastUsed time.Time
}

// sessionStore is one of the pin's Maps. Eviction order is insertion order, and
// using an entry refreshes lastUsed without reordering it, as a JS Map does.
type sessionStore struct {
	mu        sync.Mutex
	cap       int
	entries   map[string]*list.Element
	order     *list.List
	nextSweep time.Time
}

func newSessionStore(cap int) *sessionStore {
	return &sessionStore{cap: cap, entries: map[string]*list.Element{}, order: list.New()}
}

// get returns key's session, creating it when absent. The sweep the pin runs on
// a timer is run here, on first use after each interval boundary: an entry
// removed by a sweep at tick t was idle for more than the TTL at t.
func (s *sessionStore) get(key string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := sessionNow()
	if s.nextSweep.IsZero() {
		s.nextSweep = now.Add(sessionSweepInterval)
	}
	for !now.Before(s.nextSweep) {
		tick := s.nextSweep
		for element := s.order.Front(); element != nil; {
			next := element.Next()
			if entry := element.Value.(*sessionEntry); tick.Sub(entry.lastUsed) > sessionTTL {
				s.order.Remove(element)
				delete(s.entries, entry.key)
			}
			element = next
		}
		s.nextSweep = tick.Add(sessionSweepInterval)
	}
	if element, ok := s.entries[key]; ok {
		element.Value.(*sessionEntry).lastUsed = now
		return element.Value.(*sessionEntry).id
	}
	if s.order.Len() >= s.cap {
		oldest := s.order.Front()
		s.order.Remove(oldest)
		delete(s.entries, oldest.Value.(*sessionEntry).key)
	}
	entry := &sessionEntry{key: key, id: newSessionID(now), lastUsed: now}
	s.entries[key] = s.order.PushBack(entry)
	return entry.id
}

// newSessionID is generateBinaryStyleId (:74-76): a UUID and the time in ms.
func newSessionID(now time.Time) string {
	return uuid.NewString() + strconv.FormatInt(now.UnixMilli(), 10)
}

var (
	connectionSessions = newSessionStore(connectionSessionCap)
	assistantSessions  = newSessionStore(assistantSessionCap)
)
