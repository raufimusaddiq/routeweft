package adminauth

import (
	"encoding/hex"
	"strings"
	"testing"
)

// TestPBKDF2MatchesKnownVector pins the KDF implementation to the RFC 6070
// PBKDF2-HMAC-SHA256 vector so a refactor cannot silently weaken it.
func TestPBKDF2MatchesKnownVector(t *testing.T) {
	// PBKDF2-HMAC-SHA256("password", "salt", 1, 32).
	want := "120fb6cffcf8b32c43e7225256c4f837a86548c92ccc35480805987cb70be17b"
	got := pbkdf2SHA256([]byte("password"), []byte("salt"), 1, 32)
	if hex.EncodeToString(got) != want {
		t.Fatalf("pbkdf2=%x want %s", got, want)
	}
}

func TestHashPasswordRoundTrip(t *testing.T) {
	hash, err := HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(hash, "correct horse") {
		t.Fatal("plaintext leaked into hash")
	}
	if !strings.HasPrefix(hash, pbkdf2Algorithm+"$") {
		t.Fatalf("hash=%s", hash)
	}
	if !VerifyPassword(hash, "correct horse battery staple") {
		t.Fatal("valid password rejected")
	}
	if VerifyPassword(hash, "wrong password") {
		t.Fatal("wrong password accepted")
	}
}

func TestHashPasswordUsesUniqueSalt(t *testing.T) {
	first, err := HashPassword("same")
	if err != nil {
		t.Fatal(err)
	}
	second, err := HashPassword("same")
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("identical hashes for the same password: salt is not unique")
	}
}

func TestVerifyPasswordFailsClosedOnBadFormat(t *testing.T) {
	for _, stored := range []string{"", "plain", "argon2id$x$y$z", "pbkdf2-sha256$notanumber$a$b", "pbkdf2-sha256$1000$!!!$@@@"} {
		if VerifyPassword(stored, "whatever") {
			t.Fatalf("accepted malformed hash %q", stored)
		}
	}
	if _, err := HashPassword(""); err == nil {
		t.Fatal("empty password accepted")
	}
}
