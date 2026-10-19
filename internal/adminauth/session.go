package adminauth

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// DefaultSessionTTL bounds dashboard session lifetime.
const DefaultSessionTTL = 12 * time.Hour

// Session is one authenticated dashboard session.
type Session struct {
	ID        string
	AdminID   string
	Username  string
	ExpiresAt time.Time
}

// SessionManager owns process-local admin sessions. Sessions are ephemeral
// (a restart re-authenticates), so they are not persisted; passwords are.
type SessionManager struct {
	mu       sync.Mutex
	sessions map[string]Session
	ttl      time.Duration
	now      func() time.Time
}

// NewSessionManager builds a session manager with the given TTL (zero uses the
// default).
func NewSessionManager(ttl time.Duration) *SessionManager {
	if ttl <= 0 {
		ttl = DefaultSessionTTL
	}
	return &SessionManager{sessions: make(map[string]Session), ttl: ttl, now: time.Now}
}

// Create issues a new session token for an authenticated account.
func (m *SessionManager) Create(account Account) (Session, error) {
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		return Session{}, err
	}
	session := Session{ID: base64.RawURLEncoding.EncodeToString(token), AdminID: account.ID, Username: account.Username, ExpiresAt: m.now().Add(m.ttl)}
	m.mu.Lock()
	m.sessions[session.ID] = session
	m.mu.Unlock()
	return session, nil
}

// Lookup returns the live session for a token, pruning it if expired.
func (m *SessionManager) Lookup(token string) (Session, bool) {
	if token == "" {
		return Session{}, false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	session, ok := m.sessions[token]
	if !ok {
		return Session{}, false
	}
	if !m.now().Before(session.ExpiresAt) {
		delete(m.sessions, token)
		return Session{}, false
	}
	return session, true
}

// Revoke invalidates one session token.
func (m *SessionManager) Revoke(token string) {
	m.mu.Lock()
	delete(m.sessions, token)
	m.mu.Unlock()
}

// RevokeAdmin invalidates every session for one admin, used after a password
// change so an old credential cannot keep an active session (RUNBOOK §rotate).
func (m *SessionManager) RevokeAdmin(adminID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for token, session := range m.sessions {
		if session.AdminID == adminID {
			delete(m.sessions, token)
		}
	}
}

// Count reports the number of live sessions (test/observability helper).
func (m *SessionManager) Count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.sessions)
}
