package wire

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/transport"
)

func cloudRef(id, base string) routing.ProviderRef {
	return routing.ProviderRef{ProviderID: id, Protocol: id, BaseURL: base, APIToken: "ya29.fixture",
		Connection: routing.ConnectionMetadata{Settings: routing.ProviderSettings{ProjectID: "fixture-project"}}}
}

// executors/gemini-cli.js:10-13 and antigravity.js:118-125: the action is joined
// to v1internal with a colon, a stream asks for SSE, and the base is the host
// (Antigravity) or the host with v1internal (Gemini CLI).
func TestCloudCodeURLs(t *testing.T) {
	for _, c := range []struct{ id, base string }{
		{"gemini-cli", "https://cloudcode-pa.googleapis.com/v1internal"},
		{"gemini-cli", "https://cloudcode-pa.googleapis.com/v1internal/"},
		{"antigravity", "https://daily-cloudcode-pa.googleapis.com"},
	} {
		host := strings.TrimSuffix(strings.TrimRight(c.base, "/"), "/v1internal")
		for action, query := range map[string]string{"generateContent": "", "streamGenerateContent": "alt=sse"} {
			target, err := ResolveTarget(cloudRef(c.id, c.base), "v1internal:"+action)
			if err != nil {
				t.Fatal(err)
			}
			u, err := target.URL(transport.ValidatePublicURL)
			want := host + "/v1internal:" + action
			if query != "" {
				want += "?" + query
			}
			if err != nil || u.String() != want {
				t.Errorf("%s %s: %v %v, want %s", c.id, action, u, err, want)
			}
		}
	}
}

func TestCloudCodeRefusesWhatItCannotBind(t *testing.T) {
	for _, id := range []string{"gemini-cli", "antigravity"} {
		noProject := cloudRef(id, "https://x.example")
		noProject.Connection.Settings.ProjectID = " "
		for name, ref := range map[string]routing.ProviderRef{"no project": noProject, "bad endpoint": cloudRef(id, "https://x.example")} {
			endpoint := "v1internal:generateContent"
			if name == "bad endpoint" {
				endpoint = "chat/completions"
			}
			var refused *TargetError
			if _, err := ResolveTarget(ref, endpoint); !errors.As(err, &refused) {
				t.Errorf("%s %s: %v", id, name, err)
			}
		}
	}
}

// executors/gemini-cli.js:15-23 and antigravity.js:129-135.
func TestCloudCodeHeaders(t *testing.T) {
	body := []byte(`{"project":"p","model":"gemini-2.5-pro","request":{}}`)
	request, _ := http.NewRequest(http.MethodPost, "https://cloudcode-pa.googleapis.com/v1internal:streamGenerateContent?alt=sse", nil)
	request.Header = OpenAIHeaders(cloudRef("gemini-cli", "https://cloudcode-pa.googleapis.com/v1internal"))
	if err := ApplyRequestHeaders(cloudRef("gemini-cli", ""), request, body); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{
		"Authorization": "Bearer ya29.fixture", "Accept": "text/event-stream", "X-Goog-Api-Client": geminiCLIAPIClient,
		"User-Agent": "GeminiCLI/0.34.0/gemini-2.5-pro (linux; x64; terminal)",
	} {
		if got := request.Header.Get(name); got != want {
			t.Errorf("gemini-cli %s = %q, want %q", name, got, want)
		}
	}
	request, _ = http.NewRequest(http.MethodPost, "https://daily-cloudcode-pa.googleapis.com/v1internal:generateContent", nil)
	request.Header = OpenAIHeaders(cloudRef("antigravity", ""))
	if err := ApplyRequestHeaders(cloudRef("antigravity", ""), request, body); err != nil {
		t.Fatal(err)
	}
	if request.Header.Get("Authorization") != "Bearer ya29.fixture" || request.Header.Get("User-Agent") != "antigravity/ide/2.11.0 darwin/arm64" ||
		request.Header.Get("X-Goog-Api-Client") != "" {
		t.Errorf("antigravity headers %v", request.Header)
	}
}
