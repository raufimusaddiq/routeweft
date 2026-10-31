package wire

import (
	"strconv"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/providers/oauthheaders"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// The pinned vector. Every part is fixed, so a refactor that reorders, re-separates
// or re-encodes the canonical string changes the digest and fails here.
//
// Canonical input (open-sse/executors/iflow.js:31):
//
//	"iFlow-Cli:session-00000000-0000-4000-8000-000000000000:1700000000000"
//
// HMAC-SHA256 keyed with "test-api-key", hex (open-sse/executors/iflow.js:32-34).
const (
	iflowFixtureUserAgent = "iFlow-Cli"
	iflowFixtureSessionID = "session-00000000-0000-4000-8000-000000000000"
	iflowFixtureTimestamp = int64(1700000000000)
	iflowFixtureKey       = "test-api-key"
	iflowFixtureSignature = "194c023afa277f5414c44614f13f592fe67d768ae111422b0f5f7edf3997b1b1"
)

func TestIflowCanonicalStringIsPinned(t *testing.T) {
	want := "iFlow-Cli:session-00000000-0000-4000-8000-000000000000:1700000000000"
	if got := iflowCanonical(iflowFixtureUserAgent, iflowFixtureSessionID, iflowFixtureTimestamp); got != want {
		t.Fatalf("canonical string %q, want %q", got, want)
	}
	// The millisecond timestamp is decimal and unquoted; a seconds value would be
	// a different string and a different signature.
	if got := iflowCanonical("ua", "sid", 1700000000); got != "ua:sid:1700000000" {
		t.Fatalf("canonical string %q", got)
	}
}

func TestIflowSignatureIsPinned(t *testing.T) {
	got := iflowSignature(iflowFixtureUserAgent, iflowFixtureSessionID, iflowFixtureTimestamp, iflowFixtureKey)
	if got != iflowFixtureSignature {
		t.Fatalf("signature %q, want %q", got, iflowFixtureSignature)
	}
	if len(got) != 64 || strings.ToLower(got) != got {
		t.Fatalf("signature %q is not lowercase hex SHA-256", got)
	}
	// The key is part of the MAC, not a prefix of the payload.
	if other := iflowSignature(iflowFixtureUserAgent, iflowFixtureSessionID, iflowFixtureTimestamp, "other-key"); other == got {
		t.Fatal("signature does not depend on the API key")
	}
}

// TestIflowHeadersSignWhatTheySend proves the emitted headers are internally
// consistent: the signature verifies against the User-Agent, session id and
// timestamp the same call puts on the wire. The pin signs the User-Agent it
// actually sends (open-sse/executors/iflow.js:31,49,57-64), so signing one string
// and sending another is exactly the failure this catches.
func TestIflowHeadersSignWhatTheySend(t *testing.T) {
	provider := routing.ProviderRef{ProviderID: "iflow", Protocol: "openai-chat", APIToken: iflowFixtureKey}
	headers := iflowHeaders(provider)

	userAgent := headers["User-Agent"]
	if userAgent != oauthheaders.IflowUserAgent {
		t.Fatalf("user agent %q, want %q", userAgent, oauthheaders.IflowUserAgent)
	}
	sessionID := headers["session-id"]
	if !strings.HasPrefix(sessionID, "session-") || len(sessionID) != len("session-")+36 {
		t.Fatalf("session id %q is not session-<uuid>", sessionID)
	}
	timestamp, err := strconv.ParseInt(headers["x-iflow-timestamp"], 10, 64)
	if err != nil {
		t.Fatalf("timestamp %q: %v", headers["x-iflow-timestamp"], err)
	}
	want := iflowSignature(userAgent, sessionID, timestamp, iflowFixtureKey)
	if headers["x-iflow-signature"] != want {
		t.Fatalf("signature %q does not match the headers it was sent with (want %q)", headers["x-iflow-signature"], want)
	}

	// Two calls must not reuse a session id: the pin mints one per request
	// (open-sse/executors/iflow.js:45).
	if second := iflowHeaders(provider); second["session-id"] == sessionID {
		t.Fatal("session id reused across requests")
	}
}

// A connection with no credential gets no signature headers at all. Signing with
// an empty key would produce a signature the gateway cannot accept, which reads
// as a corrupt request rather than a missing credential; serving refuses such a
// connection before dispatch (RequireCredential).
func TestIflowHeadersRefuseToSignAnEmptyKey(t *testing.T) {
	for _, token := range []string{"", "   "} {
		headers := iflowHeaders(routing.ProviderRef{ProviderID: "iflow", Protocol: "openai-chat", APIToken: token})
		if headers != nil {
			t.Fatalf("token %q produced signature headers %v", token, headers)
		}
	}
	if !RequiresCredential("iflow") {
		t.Fatal("iflow does not require a credential; an unsigned request could be dispatched")
	}
	if RequiresCredential("openai") {
		t.Fatal("a plain bearer provider was marked as requiring a credential")
	}
}

// A streaming iFlow Chat request asks the gateway to report usage
// (open-sse/executors/iflow.js:99-104), so a streamed request is accounted
// instead of unobserved. A non-stream request and a client's own
// stream_options are left exactly as they are.
func TestIflowRequestsUsageOnAStreamOnlyWhenUnset(t *testing.T) {
	out := finalize(t, "iflow", chat, `{"model":"glm-5","stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	if got := decode(t, out)["stream_options"]; got == nil {
		t.Fatalf("no include_usage on a stream: %s", out)
	}
	options, _ := decode(t, out)["stream_options"].(map[string]any)
	if options["include_usage"] != true {
		t.Fatalf("stream_options = %v", options)
	}
	if out := finalize(t, "iflow", chat, `{"model":"glm-5","stream":false,"messages":[{"role":"user","content":"hi"}]}`); strings.Contains(out, "stream_options") {
		t.Fatalf("a non-stream request gained stream_options: %s", out)
	}
	in := `{"model":"glm-5","stream":true,"stream_options":{"include_usage":false},"messages":[{"role":"user","content":"hi"}]}`
	if out := finalize(t, "iflow", chat, in); out != in {
		t.Fatalf("a client's stream_options was rewritten: %s", out)
	}
}
