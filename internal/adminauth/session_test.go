package adminauth

import (
	"testing"
	"time"
)

func TestSessionLifecycle(t *testing.T) {
	manager := NewSessionManager(time.Minute)
	session, err := manager.Create(Account{ID: "a1", Username: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	if session.ID == "" || session.Username != "operator" {
		t.Fatalf("session=%+v", session)
	}
	if got, ok := manager.Lookup(session.ID); !ok || got.AdminID != "a1" {
		t.Fatalf("lookup=%+v ok=%v", got, ok)
	}
	manager.Revoke(session.ID)
	if _, ok := manager.Lookup(session.ID); ok {
		t.Fatal("revoked session still valid")
	}
}

func TestSessionExpires(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	manager := NewSessionManager(time.Minute)
	manager.now = func() time.Time { return now }
	session, err := manager.Create(Account{ID: "a1", Username: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	manager.now = func() time.Time { return now.Add(2 * time.Minute) }
	if _, ok := manager.Lookup(session.ID); ok {
		t.Fatal("expired session still valid")
	}
	if manager.Count() != 0 {
		t.Fatalf("expired session not pruned: %d", manager.Count())
	}
}

func TestRevokeAdminInvalidatesAllSessions(t *testing.T) {
	manager := NewSessionManager(time.Hour)
	for i := 0; i < 3; i++ {
		if _, err := manager.Create(Account{ID: "a1", Username: "operator"}); err != nil {
			t.Fatal(err)
		}
	}
	other, err := manager.Create(Account{ID: "a2", Username: "other"})
	if err != nil {
		t.Fatal(err)
	}
	manager.RevokeAdmin("a1")
	if manager.Count() != 1 {
		t.Fatalf("expected only the other admin's session to survive, count=%d", manager.Count())
	}
	if _, ok := manager.Lookup(other.ID); !ok {
		t.Fatal("other admin session was revoked")
	}
}
