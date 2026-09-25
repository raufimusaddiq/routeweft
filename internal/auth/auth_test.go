package auth

import (
	"fmt"
	"testing"
)

func TestHashIsStableAndPrefixIsNotRecoverable(t *testing.T) {
	key := "rw_live_abcdefghijklmnopqrstuvwxyz012345"
	if Hash(key) != Hash("  "+key+"  ") {
		t.Fatal("hash must normalize surrounding whitespace")
	}
	if len(Hash(key)) != 64 {
		t.Fatalf("hash length %d, want 64 hex chars", len(Hash(key)))
	}
	if Hash(key) == Hash(key+"x") {
		t.Fatal("distinct keys hashed identically")
	}
}

func TestKeyIndexLookupHonorsPausedAndMissingKeys(t *testing.T) {
	active := Entry{ID: "k1", Name: "active", Hash: Hash("rw_active_key_000000000000000000")}
	paused := Entry{ID: "k2", Name: "paused", Hash: Hash("rw_paused_key_000000000000000000"), Paused: true}
	index := NewKeyIndex([]Entry{active, paused})
	if entry, ok := index.Lookup("rw_active_key_000000000000000000"); !ok || entry.ID != "k1" {
		t.Fatalf("active lookup = %v %v", entry, ok)
	}
	if _, ok := index.Lookup("rw_paused_key_000000000000000000"); ok {
		t.Fatal("paused key must not validate")
	}
	if _, ok := index.Lookup("unknown"); ok {
		t.Fatal("unknown key must not validate")
	}
	if _, ok := index.Lookup(""); ok {
		t.Fatal("empty key must not validate")
	}
}

func TestClientKeyForms(t *testing.T) {
	if key, ok := FromAuthorization("Bearer rw_token_1"); !ok || key != "rw_token_1" {
		t.Fatalf("bearer parse = %q %v", key, ok)
	}
	if _, ok := FromAuthorization("Basic abc"); ok {
		t.Fatal("non-bearer must not parse")
	}
	if key, ok := FromAnthropicKey([]string{"", " rw_anthropic "}); !ok || key != "rw_anthropic" {
		t.Fatalf("anthropic parse = %q %v", key, ok)
	}
	if key, ok := FromGeminiKey("", []string{"rw_gemini"}); !ok || key != "rw_gemini" {
		t.Fatalf("gemini query parse = %q %v", key, ok)
	}
	if key, ok := FromGeminiKey("rw_header", nil); !ok || key != "rw_header" {
		t.Fatalf("gemini header parse = %q %v", key, ok)
	}
}

func BenchmarkAPIKeyLookup(b *testing.B) {
	entries := make([]Entry, 1000)
	for i := range entries {
		key := fmt.Sprintf("rw_bench_key_%032d", i)
		entries[i] = Entry{ID: fmt.Sprint(i), Name: "bench", Hash: Hash(key)}
	}
	index := NewKeyIndex(entries)
	key := fmt.Sprintf("rw_bench_key_%032d", 999)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, ok := index.Lookup(key); !ok {
			b.Fatal("benchmark key lookup failed")
		}
	}
}
