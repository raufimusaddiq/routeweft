// Package auth owns Routeweft inference client credential handling.
package auth

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"sort"
	"strings"
)

// Hash normalizes and hashes a client API key. Only the hash is ever stored or compared.
func Hash(key string) string {
	sum := sha256.Sum256([]byte(normalize(key)))
	return hex.EncodeToString(sum[:])
}

func normalize(key string) string { return strings.TrimSpace(key) }

// KeyIndex is the immutable hash-to-ID index published in RuntimeSnapshot.
// It never stores recoverable key material.
type KeyIndex struct {
	byHash map[string]Entry
}

// Entry is one active client API key as seen by the request path.
type Entry struct {
	ID       string
	Name     string
	Prefix   string
	Hash     string
	Paused   bool
	Disabled bool
	// Restricted limits the key to Allow. Allow lists the exact names a client
	// may request: a Combo name, a model alias, or "provider/model". A request
	// is also permitted when it resolves to an allowed "provider/model".
	// A restricted key with an empty Allow list can request nothing.
	Restricted bool
	Allow      []string
	// allowSet is built by NewKeyIndex so Permits is O(1) per identity; it is
	// always rebuilt from Allow, never trusted from a copied Entry.
	allowSet map[string]struct{}
}

// Limits on a stored allowlist keep the compiled key index small and bounded.
const (
	MaxAllowEntries = 1000
	MaxAllowLength  = 256
)

// NormalizeAllow trims, de-duplicates and validates an allowlist.
func NormalizeAllow(allow []string) ([]string, error) {
	out := make([]string, 0, len(allow))
	seen := make(map[string]struct{}, len(allow))
	for _, name := range allow {
		name = strings.TrimSpace(name)
		if name == "" {
			return nil, errors.New("allowlist entries must not be empty")
		}
		if len(name) > MaxAllowLength {
			return nil, errors.New("allowlist entry is too long")
		}
		if _, dup := seen[name]; dup {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	if len(out) > MaxAllowEntries {
		return nil, errors.New("allowlist has too many entries")
	}
	sort.Strings(out)
	return out, nil
}

// Permits reports whether the key may use a request that names any of the
// given identities (the requested name plus its resolved "provider/model").
func (e Entry) Permits(identities ...string) bool {
	if !e.Restricted {
		return true
	}
	if e.allowSet == nil {
		for _, identity := range identities {
			for _, allowed := range e.Allow {
				if allowed == identity {
					return true
				}
			}
		}
		return false
	}
	for _, identity := range identities {
		if _, ok := e.allowSet[identity]; ok {
			return true
		}
	}
	return false
}

// NewKeyIndex builds the immutable index.
func NewKeyIndex(entries []Entry) *KeyIndex {
	byHash := make(map[string]Entry, len(entries))
	index := &KeyIndex{byHash: byHash}
	for _, entry := range entries {
		if entry.Hash != "" {
			entry.allowSet = make(map[string]struct{}, len(entry.Allow))
			for _, allowed := range entry.Allow {
				entry.allowSet[allowed] = struct{}{}
			}
			byHash[entry.Hash] = entry
		}
	}
	return index
}

// Lookup hashes a presented key and returns its active entry without retaining plaintext.
func (i *KeyIndex) Lookup(presented string) (Entry, bool) {
	if i == nil || presented == "" {
		return Entry{}, false
	}
	entry, ok := i.byHash[Hash(presented)]
	if !ok || entry.Paused || entry.Disabled {
		return Entry{}, false
	}
	return entry, true
}

// Records returns copy-safe records for runtime mutation persistence. Hashes
// are one-way digests, never original API keys.
func (i *KeyIndex) Records() []Entry {
	if i == nil {
		return nil
	}
	entries := make([]Entry, 0, len(i.byHash))
	for _, entry := range i.byHash {
		entries = append(entries, entry)
	}
	return entries
}

// FromAuthorization parses an Authorization header containing a Routeweft bearer key.
func FromAuthorization(header string) (string, bool) {
	fields := strings.Fields(header)
	if len(fields) != 2 || !strings.EqualFold(fields[0], "Bearer") {
		return "", false
	}
	return fields[1], true
}

// FromAnthropicKey returns the Anthropic-style x-api-key client key when present.
func FromAnthropicKey(values []string) (string, bool) {
	for _, value := range values {
		if trimmed := normalize(value); trimmed != "" {
			return trimmed, true
		}
	}
	return "", false
}

// FromGeminiKey returns the Gemini-compatible key forms required by compatibility ingress.
func FromGeminiKey(header string, query []string) (string, bool) {
	if trimmed := normalize(header); trimmed != "" {
		return trimmed, true
	}
	for _, value := range query {
		if trimmed := normalize(value); trimmed != "" {
			return trimmed, true
		}
	}
	return "", false
}
