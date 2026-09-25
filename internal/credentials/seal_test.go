package credentials

import (
	"bytes"
	"crypto/rand"
	"strings"
	"testing"
)

func testSealer(t *testing.T) *Sealer {
	t.Helper()
	key := make([]byte, MasterKeySize)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	sealer, err := NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	return sealer
}

func TestSealRoundTripAndNonceFreshness(t *testing.T) {
	sealer := testSealer(t)
	plaintext := []byte(`{"refreshToken":"fixture-rotating-token"}`)
	first, err := sealer.Seal(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	second, err := sealer.Seal(plaintext)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("sealing the same plaintext twice reused an envelope")
	}
	if strings.Contains(first, "fixture-rotating-token") {
		t.Fatal("sealed blob leaked plaintext")
	}
	opened, err := sealer.Open(first)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(opened, plaintext) {
		t.Fatalf("round trip mismatch: %s", opened)
	}
}

func TestSealRejectsTamperAndBadEnvelope(t *testing.T) {
	sealer := testSealer(t)
	sealed, err := sealer.Seal([]byte(`{"accessToken":"fixture"}`))
	if err != nil {
		t.Fatal(err)
	}
	tampered := sealed[:len(sealed)-2] + "AA"
	if _, err := sealer.Open(tampered); err == nil {
		t.Fatal("tampered blob authenticated")
	}
	for _, envelope := range []string{"", "not-a-blob", "rwc2.AAAA"} {
		if _, err := sealer.Open(envelope); err == nil {
			t.Fatalf("envelope %q was accepted", envelope)
		}
	}
	if _, err := sealer.Open(sealed[:len(blobPrefix)+2]); err == nil {
		t.Fatal("truncated blob was accepted")
	}
}

func TestNewSealerRejectsWrongKeyLength(t *testing.T) {
	if _, err := NewSealer(make([]byte, MasterKeySize-1)); err == nil {
		t.Fatal("short key accepted")
	}
	var nilSealer *Sealer
	if _, err := nilSealer.Seal([]byte("x")); err != ErrKeyRequired {
		t.Fatalf("nil sealer err=%v", err)
	}
}
