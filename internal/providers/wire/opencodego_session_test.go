package wire

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

func ocgSession(client http.Header, body string, conn string) string {
	p := routing.ProviderRef{ProviderID: "opencode-go", ConnectionID: conn}
	return openCodeGoSession(p, client, []byte(body))[openCodeGoSessionHeader]
}

func ocgDigest(tool, session string) string {
	sum := sha256.Sum256([]byte("opencode-go\x00" + tool + "\x00" + session))
	return "ses_" + hex.EncodeToString(sum[:])[:32]
}

func TestOpenCodeGoNativeSessionIsUsedAsIs(t *testing.T) {
	h := http.Header{}
	h.Set("X-Opencode-Session", "  ses_native  ")
	h.Set("X-Session-Id", "other")
	if got := ocgSession(h, `{"prompt_cache_key":"k"}`, "c"); got != "ses_native" {
		t.Fatalf("got %q", got)
	}
	// An unusable native value falls through to the resolved, digested id.
	for _, bad := range []string{"", "   ", strings.Repeat("a", 257)} {
		h.Set("X-Opencode-Session", bad)
		if got := ocgSession(h, `{}`, "c"); got != ocgDigest("generic", "other") {
			t.Fatalf("%d-char native: got %q", len(bad), got)
		}
	}
}

func TestOpenCodeGoSessionIsNamespacedByClientAndDigested(t *testing.T) {
	h := http.Header{}
	h.Set("X-Session-Id", "abc")
	if got, want := ocgSession(h, `{}`, "c"), ocgDigest("generic", "abc"); got != want || len(got) != 36 {
		t.Fatalf("got %q want %q", got, want)
	}
	h.Set("User-Agent", "codex-tui/1.0")
	codex := ocgSession(h, `{}`, "c")
	h.Set("User-Agent", "claude-cli/2")
	claude := ocgSession(h, `{}`, "c")
	if codex != ocgDigest("codex", "abc") || claude != ocgDigest("claude", "abc") || codex == claude {
		t.Fatalf("codex %q claude %q", codex, claude)
	}
	if strings.Contains(codex, "abc") {
		t.Fatal("client session id leaks")
	}
}

func TestOpenCodeGoSessionResolutionOrder(t *testing.T) {
	hdr := func(kv ...string) http.Header {
		h := http.Header{}
		for i := 0; i < len(kv); i += 2 {
			h.Set(kv[i], kv[i+1])
		}
		return h
	}
	long := strings.Repeat("a", 60)
	for _, tc := range []struct {
		name string
		h    http.Header
		body string
		want string
	}{
		{"claude user_id suffix", hdr("x-session-id", "h"), `{"metadata":{"user_id":"user_x_session_ab12-cd"}}`, "claude:ab12-cd"},
		{"claude user_id json", nil, `{"metadata":{"user_id":"{\"session_id\":\" s1 \"}"}}`, "claude:s1"},
		{"claude header", hdr("x-claude-code-session-id", "cc1", "x-session-id", "h"), `{}`, "claude:cc1"},
		{"antigravity", hdr("x-session-id", "h"), `{"request":{"sessionId":"ag1"}}`, "antigravity:ag1"},
		{"antigravity request id", nil, `{"requestId":"agent/123e4567-e89b-12d3-a456-426614174000/1"}`, "antigravity:123e4567-e89b-12d3-a456-426614174000"},
		{"header priority", hdr("session_id", "b", "session-id", "a", "x-client-request-id", "r"), `{}`, "a"},
		{"request id", hdr("x-client-request-id", "r"), `{"prompt_cache_key":"k"}`, "r"},
		{"prompt_cache_key", nil, `{"prompt_cache_key":"k","session_id":"s"}`, "k"},
		{"session_id", nil, `{"session_id":"s","conversation_id":"c"}`, "s"},
		{"conversation_id", nil, `{"conversation_id":"c","metadata":{"user_id":"u"}}`, "c"},
		{"metadata user_id", nil, `{"metadata":{"user_id":"plain"}}`, "plain"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got, want := ocgSession(tc.h, tc.body, "c"), ocgDigest("generic", tc.want); got != want {
				t.Fatalf("got %q want %q", got, want)
			}
		})
	}
	// The same opening assistant text opens the same session; a short one does not.
	a := `{"messages":[{"role":"assistant","content":"` + long + `"},{"role":"user","content":"x"}]}`
	if ocgSession(nil, a, "c") != ocgSession(nil, a, "c") || ocgSession(nil, a, "c") == ocgSession(nil, a, "d") {
		t.Fatal("assistant-text session must be stable and scoped to the connection")
	}
	if ocgSession(nil, a, "c") == ocgSession(nil, `{"messages":[{"role":"assistant","content":"short"}]}`, "c") {
		t.Fatal("short assistant text must fall through to the connection")
	}
	b := `{"input":[{"role":"assistant","content":[{"type":"output_text","text":"` + long + `"}]}]}`
	if ocgSession(nil, a, "c") != ocgSession(nil, b, "c") {
		t.Fatal("messages and input forms of one opening must share a session")
	}
	// Connection fallback is stable; with no connection each request is fresh.
	if ocgSession(nil, `{}`, "c") != ocgSession(nil, `{"x":1}`, "c") || ocgSession(nil, `{}`, "c") == ocgSession(nil, `{}`, "d") {
		t.Fatal("connection session must be stable per connection")
	}
	if ocgSession(nil, `{}`, "") == ocgSession(nil, `{}`, "") {
		t.Fatal("no identity must not collapse onto one session")
	}
	if got := ocgSession(nil, `not json`, "c"); !strings.HasPrefix(got, "ses_") {
		t.Fatalf("unparseable body: %q", got)
	}
}

func TestOpenCodeGoSessionHeaderIsAppliedAndProbeFallsBack(t *testing.T) {
	p := routing.ProviderRef{ProviderID: "opencode-go", ConnectionID: "c"}
	req, _ := http.NewRequest(http.MethodPost, "https://example.test/", nil)
	if err := ApplyRequestHeaders(p, req, []byte(`{}`)); err != nil || !strings.HasPrefix(req.Header.Get(openCodeGoSessionHeader), "ses_") {
		t.Fatalf("probe header: %v %v", err, req.Header)
	}
	client := http.Header{"X-Opencode-Session": {"mine"}}
	ApplyClientRequestHeaders(p, req, client, []byte(`{}`))
	if req.Header.Get(openCodeGoSessionHeader) != "mine" {
		t.Fatalf("client session lost: %v", req.Header)
	}
	other, _ := http.NewRequest(http.MethodPost, "https://example.test/", nil)
	ApplyClientRequestHeaders(routing.ProviderRef{ProviderID: "opencode"}, other, client, nil)
	if len(other.Header) != 0 {
		t.Fatalf("other provider got headers: %v", other.Header)
	}
}

// restartProcess is a new process: the pin's in-memory stores start empty.
func restartProcess() {
	connectionSessions, assistantSessions = newSessionStore(connectionSessionCap), newSessionStore(assistantSessionCap)
}

func withClock(t *testing.T, start time.Time) *time.Time {
	t.Helper()
	now := start
	sessionNow = func() time.Time { return now }
	t.Cleanup(func() { sessionNow = time.Now; restartProcess() })
	return &now
}

const ocgOpening = `{"messages":[{"role":"assistant","content":"` + "this assistant opening is comfortably longer than fifty characters" + `"}]}`

// The pin's ids are random per process (sessionManager.js:15,45-66,183-198): the
// same inside one process, different after a restart, different per connection.
func TestOpenCodeGoSessionLastsForTheProcessNotForever(t *testing.T) {
	withClock(t, time.Unix(1_800_000_000, 0))
	restartProcess()
	for name, body := range map[string]string{"connection": `{}`, "assistant text": ocgOpening} {
		first := ocgSession(nil, body, "acct")
		if again := ocgSession(nil, body, "acct"); again != first {
			t.Fatalf("%s: unstable inside a process", name)
		}
		if other := ocgSession(nil, body, "other"); other == first {
			t.Fatalf("%s: shared across connections", name)
		}
		restartProcess()
		if after := ocgSession(nil, body, "acct"); after == first {
			t.Fatalf("%s: survived a restart; the pin's ids do not", name)
		}
	}
}

func TestOpenCodeGoSessionExpiresIdleAtTheSweepAfterTheTTL(t *testing.T) {
	start := time.Unix(1_800_000_000, 0)
	now := withClock(t, start)
	restartProcess()
	first := ocgSession(nil, `{}`, "acct") // lastUsed t0, first sweep t0+30m
	// Used every 59 minutes: each use refreshes lastUsed, so it never expires.
	for i := 1; i <= 4; i++ {
		*now = start.Add(time.Duration(i) * 59 * time.Minute)
		if ocgSession(nil, `{}`, "acct") != first {
			t.Fatalf("refreshed session expired at use %d", i)
		}
	}
	// Idle from lastUsed=L: the sweep at tick t removes it only when t-L > 2h, so it
	// survives to the first tick more than 2h after L, and is gone after.
	last := *now
	*now = last.Add(2 * time.Hour)
	if ocgSession(nil, `{}`, "acct") != first {
		t.Fatal("expired exactly at the TTL; the pin keeps it until a sweep sees it idle for more")
	}
	*now = (*now).Add(3 * time.Hour)
	if got := ocgSession(nil, `{}`, "acct"); got == first {
		t.Fatal("idle session outlived the TTL")
	}
}

func TestOpenCodeGoSessionStoreEvictsTheOldestAtTheCap(t *testing.T) {
	withClock(t, time.Unix(1_800_000_000, 0))
	restartProcess()
	first := ocgSession(nil, `{}`, "c0")
	for i := 1; i < connectionSessionCap; i++ {
		ocgSession(nil, `{}`, "c"+strconv.Itoa(i))
	}
	if ocgSession(nil, `{}`, "c0") != first {
		t.Fatal("evicted below the cap")
	}
	ocgSession(nil, `{}`, "overflow") // a 1001st entry evicts c0, the oldest insertion
	if ocgSession(nil, `{}`, "c0") == first {
		t.Fatal("oldest entry kept at the cap")
	}
}
