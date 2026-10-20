package gemini

import (
	"container/list"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"
)

// PlaceholderSignature stands in for a thought signature the gateway does not
// hold. Gemini 3 answers a request whose first function call in a turn carries no
// signature with a 400, and the signature the model returned is state this
// gateway can only replay when it still has it. The value is the dummy Google
// documents for exactly this case. That Google accepts it is NOT verified here:
// there is no live call in this repository (PARITY 6.7 G2). The pin ships three
// captured vendor blobs instead (config/defaultThinkingSignature.js); those are
// deliberately not copied.
const PlaceholderSignature = "skip_thought_signature_validator"

const (
	// MaxSignatureBytes bounds one cached signature; a longer one is not stored.
	MaxSignatureBytes = 16 << 10
	// DefaultSignatureEntries and DefaultSignatureTTL mirror the pin's in-memory
	// tier (services/thoughtSignatureStore.js:3-6): 2000 entries, one hour. The
	// pin's persisted tier (SQLite, seven days) is not built: a signature is
	// opaque model state and is never written to disk, and a read of it on the
	// request path would be a synchronous store read (AGENTS 3).
	DefaultSignatureEntries = 2000
	DefaultSignatureTTL     = time.Hour
)

// Signatures is a bounded in-memory TTL cache of the thought signatures a Gemini
// response attached to its function calls, so the next request of the same
// conversation can send them back.
//
// Every key carries the scope: the authenticated client and the provider
// connection. A signature is the model's encrypted reasoning state for one
// client's conversation; served to another client it would be that state
// crossing a tenant boundary. The pin keys by session and tool-call id only
// (:94-98), and a generated id such as call_msg0_tc0_lookup repeats across
// clients, so it has no such separation. An empty scope disables the cache for
// that call: nothing is stored and nothing is found, and the placeholder is used.
//
// The key also carries the call's name and arguments, so a repeated id inside one
// client's session finds the signature of the call it was minted for.
type Signatures struct {
	mu      sync.Mutex
	max     int
	ttl     time.Duration
	now     func() time.Time
	order   *list.List // most recently used first
	entries map[string]*list.Element
}

type signatureEntry struct {
	key       string
	signature string
	expires   time.Time
}

// NewSignatures builds a cache holding at most max entries for ttl each.
func NewSignatures(max int, ttl time.Duration) *Signatures {
	return &Signatures{max: max, ttl: ttl, now: time.Now, order: list.New(), entries: map[string]*list.Element{}}
}

func signatureKey(scope, id, name, arguments string) string {
	sum := sha256.New()
	for _, part := range []string{scope, id, name, arguments} {
		// Length-prefixed, so ("ab","c") and ("a","bc") are different keys.
		_, _ = sum.Write([]byte{byte(len(part) >> 24), byte(len(part) >> 16), byte(len(part) >> 8), byte(len(part))})
		_, _ = sum.Write([]byte(part))
	}
	return hex.EncodeToString(sum.Sum(nil))
}

// Put remembers the signature of the call (id, name, arguments) under scope.
func (s *Signatures) Put(scope, id, name, arguments, signature string) {
	if s == nil || scope == "" || id == "" || signature == "" || len(signature) > MaxSignatureBytes || s.max <= 0 {
		return
	}
	key := signatureKey(scope, id, name, arguments)
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if element, ok := s.entries[key]; ok {
		entry := element.Value.(*signatureEntry)
		entry.signature, entry.expires = signature, now.Add(s.ttl)
		s.order.MoveToFront(element)
		return
	}
	s.entries[key] = s.order.PushFront(&signatureEntry{key: key, signature: signature, expires: now.Add(s.ttl)})
	for s.order.Len() > s.max {
		s.drop(s.order.Back())
	}
}

// Get returns the signature remembered for the call, if it has not expired.
func (s *Signatures) Get(scope, id, name, arguments string) (string, bool) {
	if s == nil || scope == "" || id == "" {
		return "", false
	}
	key := signatureKey(scope, id, name, arguments)
	s.mu.Lock()
	defer s.mu.Unlock()
	element, ok := s.entries[key]
	if !ok {
		return "", false
	}
	entry := element.Value.(*signatureEntry)
	if !s.now().Before(entry.expires) {
		s.drop(element)
		return "", false
	}
	s.order.MoveToFront(element)
	return entry.signature, true
}

// Len is the number of entries held, expired ones included until they are met.
func (s *Signatures) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.order.Len()
}

func (s *Signatures) drop(element *list.Element) {
	delete(s.entries, element.Value.(*signatureEntry).key)
	s.order.Remove(element)
}
