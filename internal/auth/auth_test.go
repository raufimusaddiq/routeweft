package auth

import (
	"fmt"
	"strings"
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

func TestEntryPermitsAndNormalizeAllow(t *testing.T) {
	if !(Entry{}).Permits("anything") {
		t.Fatal("unrestricted key must permit everything")
	}
	scoped := Entry{Restricted: true, Allow: []string{"fast", "openai/gpt-4o"}}
	if !scoped.Permits("fast") || !scoped.Permits("other", "openai/gpt-4o") {
		t.Fatal("restricted key must permit listed identities")
	}
	if scoped.Permits("openai/gpt-4") || scoped.Permits() {
		t.Fatal("restricted key must reject unlisted identities")
	}
	if (Entry{Restricted: true}).Permits("fast") {
		t.Fatal("restricted key with an empty allowlist permits nothing")
	}
	got, err := NormalizeAllow([]string{" b ", "a", "b"})
	if err != nil || len(got) != 2 || got[0] != "a" || got[1] != "b" {
		t.Fatalf("NormalizeAllow = %v, %v", got, err)
	}
	for _, bad := range [][]string{{""}, {"  "}, {strings.Repeat("x", MaxAllowLength+1)}} {
		if _, err := NormalizeAllow(bad); err == nil {
			t.Fatalf("NormalizeAllow(%q) should fail", bad)
		}
	}
}
