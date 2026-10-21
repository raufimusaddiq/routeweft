package wire

import (
	"container/list"
	"crypto/sha256"
	"sync"
	"time"
)

// The turn index Grok CLI is sent never goes backwards for a session within this
// process (executors/grok-cli.js:63-112). A client that sends the whole history
// is counted from its user turns; one that sends only the newest turn would
// otherwise be sent 1 every time, so a later request in the same session is at
// least one more than the last.
//
// Differences from the pin:
//   - the pin remembers a request by the identity of its body object, so a retry
//     of the same request is not counted twice (:93-95). A Go request has no such
//     object by the time its headers are built, so a request is the session and
//     the exact bytes sent. Two different requests with byte-identical bodies in
//     one session are therefore one request here and two there.
const (
	// grokCLITurnStoreMax is GROK_CLI_TURN_STORE_MAX (:55).
	grokCLITurnStoreMax = 5000
	// grokCLITurnTTL is MEMORY_CONFIG.sessionTtlMs (config/runtimeConfig.js:28).
	grokCLITurnTTL = 2 * time.Hour
)

// grokCLINow is the clock, replaced by tests.
var grokCLINow = time.Now

type turnEntry struct {
	key  string
	turn int
	at   time.Time
}

// turnTable is a bounded map that evicts its oldest key first. Writing a key
// moves it to the newest end, as the pin's delete-then-set does (:102,:109).
type turnTable struct {
	entries map[string]*list.Element
	order   *list.List
}

func newTurnTable() *turnTable {
	return &turnTable{entries: map[string]*list.Element{}, order: list.New()}
}

func (t *turnTable) get(key string) (turnEntry, bool) {
	element, ok := t.entries[key]
	if !ok {
		return turnEntry{}, false
	}
	return element.Value.(turnEntry), true
}

func (t *turnTable) put(key string, turn int, at time.Time) {
	if element, ok := t.entries[key]; ok {
		t.order.Remove(element)
		delete(t.entries, key)
	}
	for t.order.Len() >= grokCLITurnStoreMax {
		oldest := t.order.Front()
		t.order.Remove(oldest)
		delete(t.entries, oldest.Value.(turnEntry).key)
	}
	t.entries[key] = t.order.PushBack(turnEntry{key: key, turn: turn, at: at})
}

type grokCLITurnMemory struct {
	mu       sync.Mutex
	sessions *turnTable
	requests *turnTable
}

var grokCLITurnStore = &grokCLITurnMemory{sessions: newTurnTable(), requests: newTurnTable()}

// reset forgets every session, for tests.
func (s *grokCLITurnMemory) reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions, s.requests = newTurnTable(), newTurnTable()
}

// resolve is resolveGrokCliTurnIdx (:89-112): the user turns the input carries,
// but never fewer than one more than the last index this session was sent. A
// request already seen is answered with the index it was given.
func (s *grokCLITurnMemory) resolve(session string, fromInput int, body []byte) int {
	if session == "" {
		return fromInput
	}
	digest := sha256.Sum256(append([]byte(session+"\x00"), body...))
	requestKey := string(digest[:])
	now := grokCLINow()

	s.mu.Lock()
	defer s.mu.Unlock()
	if seen, ok := s.requests.get(requestKey); ok && now.Sub(seen.at) <= grokCLITurnTTL {
		return seen.turn
	}
	previous := 0
	if existing, ok := s.sessions.get(session); ok && now.Sub(existing.at) <= grokCLITurnTTL {
		previous = existing.turn
	}
	turn := fromInput
	if previous > 0 && previous+1 > turn {
		turn = previous + 1
	}
	s.sessions.put(session, turn, now)
	s.requests.put(requestKey, turn, now)
	return turn
}
