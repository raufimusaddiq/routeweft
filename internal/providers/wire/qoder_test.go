package wire

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// Fixed inputs for the pinned canonical string (open-sse/shared/qoder/cosy.js:147).
const (
	qoderFixturePayloadB64 = "UEFZTE9BRA=="
	qoderFixtureCosyKey    = "COSYKEY"
	qoderFixtureTimestamp  = "1700000000"
	qoderFixtureSigPath    = "/api/v2/service/pro/sse/agent_chat_generation"
)

func TestCosySigInputIsPinned(t *testing.T) {
	// Body carries a non-ASCII byte pair (UTF-8 "é") and an embedded newline:
	// the pin signs the bytes verbatim via latin1, never re-encoded.
	body := []byte("{\"q\":\"caf\xc3\xa9\"}\n")
	want := "UEFZTE9BRA==\nCOSYKEY\n1700000000\n{\"q\":\"caf\xc3\xa9\"}\n\n/api/v2/service/pro/sse/agent_chat_generation"
	got := string(cosySigInput(qoderFixturePayloadB64, qoderFixtureCosyKey, qoderFixtureTimestamp, body, qoderFixtureSigPath))
	if got != want {
		t.Fatalf("canonical %q, want %q", got, want)
	}
	// Empty body (GET) keeps both separators.
	empty := string(cosySigInput("a", "b", "c", nil, "d"))
	if empty != "a\nb\nc\n\nd" {
		t.Fatalf("empty-body canonical %q", empty)
	}
}

func TestCosySignatureIsPinned(t *testing.T) {
	body := []byte("{\"q\":\"caf\xc3\xa9\"}\n")
	got := cosySignature(qoderFixturePayloadB64, qoderFixtureCosyKey, qoderFixtureTimestamp, body, qoderFixtureSigPath)
	const want = "8ff902bca9f2db8ad6cbb4c229de5dc5"
	if got != want {
		t.Fatalf("signature %s, want %s", got, want)
	}
}

func TestCosySigPath(t *testing.T) {
	cases := map[string]string{
		"https://api3.qoder.sh/algo/api/v2/service/pro/sse/agent_chat_generation?FetchKeys=x&Encode=1": "/api/v2/service/pro/sse/agent_chat_generation",
		"https://api3.qoder.sh/algo/api/v2/model/list":                                                 "/api/v2/model/list",
		"https://api3.qoder.sh/other":                                                                  "/other",
		"https://api3.qoder.sh":                                                                        "/",
		"::not a url":                                                                                  "",
	}
	for in, want := range cases {
		if got := cosySigPath(in); got != want {
			t.Errorf("cosySigPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func fixedEntropy() cosyEntropy {
	e := cosyLive
	e.aesKey = func() string { return "0123456789abcdef" }
	e.requestID = func() string { return "req-1" }
	e.headerID = func() string { return "hdr-1" }
	e.now = func() time.Time { return time.Unix(1700000000, 0) }
	return e
}

func fixtureCreds() CosyCredentials {
	return CosyCredentials{UserID: "u-1", AuthToken: "dt-token", Name: "N", Email: "e@x.test", MachineID: "machine-1"}
}

func TestBuildCosyHeadersFullSetAndSelfConsistent(t *testing.T) {
	body := []byte(`{"a":1}`)
	url := "https://api3.qoder.sh/algo/api/v2/service/pro/sse/agent_chat_generation?x=1"
	h, err := buildCosyHeaders(body, url, fixtureCreds(), fixedEntropy())
	if err != nil {
		t.Fatal(err)
	}
	if len(h) != 19 {
		t.Fatalf("header count %d, want the pin's 19", len(h))
	}
	exact := map[string]string{
		"Cosy-User": "u-1", "Cosy-Date": "1700000000", "Cosy-Version": "1.0.0",
		"Cosy-Machineid": "machine-1", "Cosy-Machinetoken": "machine-1", "Cosy-Machinetype": "5",
		"Cosy-Machineos": "x86_64_windows", "Cosy-Clienttype": "5", "Cosy-Clientip": "127.0.0.1",
		"Cosy-Bodyhash": "bb6cb5c68df4652941caf652a366f2d8", "Cosy-Bodylength": "7",
		"Cosy-Sigpath": "/api/v2/service/pro/sse/agent_chat_generation", "Cosy-Data-Policy": "disagree",
		"Cosy-Organization-Id": "", "Cosy-Organization-Tags": "", "Login-Version": "v2", "X-Request-Id": "hdr-1",
	}
	for k, want := range exact {
		if got, ok := h[k]; !ok || got != want {
			t.Errorf("%s = %q (present %v), want %q", k, got, ok, want)
		}
	}
	parts := strings.Split(strings.TrimPrefix(h["Authorization"], "Bearer COSY."), ".")
	if len(parts) != 2 || h["Cosy-Key"] == "" {
		t.Fatalf("authorization %q", h["Authorization"])
	}
	// Recompute the signature from the emitted pieces.
	want := cosySignature(parts[0], h["Cosy-Key"], h["Cosy-Date"], body, h["Cosy-Sigpath"])
	if parts[1] != want {
		t.Fatalf("signature %s, want %s", parts[1], want)
	}
}

func TestBuildCosyHeadersFailsClosed(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*CosyCredentials)
		want   error
	}{
		{"user", func(c *CosyCredentials) { c.UserID = "" }, ErrCosyUserID},
		{"token", func(c *CosyCredentials) { c.AuthToken = "" }, ErrCosyToken},
		{"machine id", func(c *CosyCredentials) { c.MachineID = "" }, ErrCosyMachineID},
	}
	for _, c := range cases {
		creds := fixtureCreds()
		c.mutate(&creds)
		h, err := buildCosyHeaders([]byte("{}"), "https://api3.qoder.sh/algo/x", creds, fixedEntropy())
		if !errors.Is(err, c.want) || h != nil {
			t.Errorf("%s: headers %v err %v, want nil and %v", c.name, h, err, c.want)
		}
	}
	// A key that is not 16 bytes is refused, not padded (cosy.js:46-48).
	e := fixedEntropy()
	e.aesKey = func() string { return "short" }
	if h, err := buildCosyHeaders(nil, "https://api3.qoder.sh/algo/x", fixtureCreds(), e); err == nil || h != nil {
		t.Fatalf("short aes key accepted: %v %v", h, err)
	}
}

// The builder takes the machine id as given: two calls with the same stored
// value carry the same id, and it is never replaced.
func TestBuildCosyHeadersReusesSuppliedMachineID(t *testing.T) {
	creds := fixtureCreds()
	a, _ := buildCosyHeaders(nil, "https://api3.qoder.sh/algo/x", creds, fixedEntropy())
	b, _ := buildCosyHeaders(nil, "https://api3.qoder.sh/algo/x", creds, fixedEntropy())
	if a["Cosy-Machineid"] != "machine-1" || b["Cosy-Machineid"] != a["Cosy-Machineid"] || b["Cosy-Machinetoken"] != "machine-1" {
		t.Fatalf("machine id drifted: %q %q", a["Cosy-Machineid"], b["Cosy-Machineid"])
	}
}

func TestBuildCosyHeadersLiveEntropyDiffersPerRequest(t *testing.T) {
	a, err := BuildCosyHeaders([]byte("{}"), "https://api3.qoder.sh/algo/x", fixtureCreds())
	if err != nil {
		t.Fatal(err)
	}
	b, _ := BuildCosyHeaders([]byte("{}"), "https://api3.qoder.sh/algo/x", fixtureCreds())
	if a["Cosy-Key"] == b["Cosy-Key"] || a["X-Request-Id"] == b["X-Request-Id"] {
		t.Fatal("per-request key / id was reused")
	}
}

// AES-128-CBC (key reused as IV, PKCS#7) over the user info JSON, produced by
// the pin's own routine (cosy.js:44-55, 65-71) with the key "0123456789abcdef".
// The RSA-wrapped key is randomised (PKCS#1 v1.5) so it cannot be pinned.
func TestCosyUserInfoCiphertextIsPinned(t *testing.T) {
	const want = "+DGTa5lPw8l0y/64Vpmyj+B0DDaUVR0UYbc7ASAqfO+ao9Vbo2QMyCvDvx4A5JbgJcAfmjLeuceH9euvs+k87ouY5mBn4AMW66eU0qCoiFMQ3FtCxzOBbiY3+BVumkrS"
	_, got, err := cosyEncryptUserInfo(cosyUserInfo{UID: "u-1", Token: "dt-token", Name: "N", Email: "e@x.test"}, "0123456789abcdef", cosyLive)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("info %s, want %s", got, want)
	}
}

func BenchmarkCosyHeaders(b *testing.B) {
	body := []byte(`{"model":"fixture","messages":[{"role":"user","content":"hi"}]}`)
	creds := CosyCredentials{UserID: "fixture-user", AuthToken: "fixture-token", MachineID: "stored-machine"}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := BuildCosyHeaders(body, "https://example.invalid/algo/api/chat", creds); err != nil {
			b.Fatal(err)
		}
	}
}
