package wire

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/routing"
)

// executors/opencode.js:492-510: the free-tier desk identity and the client
// values it forwards when they are well formed.
func TestOpenCodeIdentityHeadersMatchThePin(t *testing.T) {
	provider := routing.ProviderRef{ProviderID: "opencode", ConnectionID: "conn-a"}
	headers := opencodeIdentity(provider, http.Header{}, nil)
	for name, want := range map[string]string{
		"User-Agent":         "opencode/1.18.31",
		"x-opencode-client":  "desktop",
		"x-opencode-project": "global",
	} {
		if headers[name] != want {
			t.Fatalf("%s = %q, want %q", name, headers[name], want)
		}
	}
	if !opencodeRequest.MatchString(headers["x-opencode-request"]) {
		t.Fatalf("request id %q is not msg_<12 hex><14 base62>", headers["x-opencode-request"])
	}
	if again := opencodeIdentity(provider, http.Header{}, nil)["x-opencode-session"]; again != headers["x-opencode-session"] {
		t.Fatalf("session changed between requests: %q then %q", headers["x-opencode-session"], again)
	}
	if other := opencodeIdentity(routing.ProviderRef{ProviderID: "opencode", ConnectionID: "conn-b"}, http.Header{}, nil)["x-opencode-session"]; other == headers["x-opencode-session"] {
		t.Fatal("two connections share a session id")
	}
}

func TestOpenCodeIdentityForwardsOnlyAcceptedClientValues(t *testing.T) {
	provider := routing.ProviderRef{ProviderID: "opencode", ConnectionID: "conn-a"}
	client := http.Header{
		"User-Agent":         {"opencode/1.18.31 (darwin; arm64)"},
		"X-Opencode-Client":  {"cli"},
		"X-Opencode-Project": {"project-7"},
		"X-Opencode-Session": {"ses_abcdef1234567890abcdef"},
		"X-Opencode-Request": {"msg_0123456789abABCDEFGHIJKLMN"},
	}
	headers := opencodeIdentity(provider, client, nil)
	for name, want := range map[string]string{
		"User-Agent":         "opencode/1.18.31 (darwin; arm64)",
		"x-opencode-client":  "cli",
		"x-opencode-project": "project-7",
		"x-opencode-session": "ses_abcdef1234567890abcdef",
		"x-opencode-request": "msg_0123456789abABCDEFGHIJKLMN",
	} {
		if headers[name] != want {
			t.Fatalf("%s = %q, want the client's %q", name, headers[name], want)
		}
	}
	// An old opencode version is not the fingerprint the gate wants; an
	// over-long session and a malformed request id fall back.
	old := http.Header{"User-Agent": {"opencode/1.2.3"}}
	if got := opencodeIdentity(provider, old, nil)["User-Agent"]; got != opencodeClientVersion {
		t.Fatalf("old user agent %q was forwarded", got)
	}
	long := http.Header{"X-Opencode-Session": {strings.Repeat("s", opencodeMaxIdentity+1)}, "X-Opencode-Request": {"msg_bad"}}
	fallback := opencodeIdentity(provider, long, nil)
	if fallback["x-opencode-session"] == strings.Repeat("s", opencodeMaxIdentity+1) || !opencodeRequest.MatchString(fallback["x-opencode-request"]) {
		t.Fatalf("over-long session or bad request id was forwarded: %#v", fallback)
	}
}

func TestOpenCodeRequestIDShape(t *testing.T) {
	id := opencodeNewRequestID(time.UnixMilli(1_800_000_000_000))
	if !opencodeRequest.MatchString(id) || !strings.HasPrefix(id, "msg_") {
		t.Fatalf("id %q does not match the pin's shape", id)
	}
}

// The hook is applied through the same seam the dispatcher uses.
func TestOpenCodeIdentityReachesTheRequest(t *testing.T) {
	request, _ := http.NewRequest(http.MethodPost, "https://opencode.ai/zen/v1/chat/completions", nil)
	provider := routing.ProviderRef{ProviderID: "opencode", ConnectionID: "conn-a"}
	ApplyClientRequestHeaders(provider, request, http.Header{}, nil)
	for _, name := range []string{"User-Agent", "x-opencode-client", "x-opencode-project", "x-opencode-session", "x-opencode-request"} {
		if request.Header.Get(name) == "" {
			t.Fatalf("%s was not applied", name)
		}
	}
}
