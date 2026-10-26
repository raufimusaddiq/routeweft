package onboarding

import (
	"context"
	"encoding/json"
	"net/http"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// TestGoogleClientComesFromTheEnvironment proves the client has no built-in
// fallback: unset means a clear error, not a silent default.
func TestGoogleClientComesFromTheEnvironment(t *testing.T) {
	t.Setenv(GoogleClientIDEnv, "")
	t.Setenv(GoogleClientSecretEnv, "")
	if _, _, err := googleClient(); err == nil {
		t.Fatal("google client resolved without configuration")
	}
	t.Setenv(GoogleClientIDEnv, "fixture-client-id")
	if _, _, err := googleClient(); err == nil {
		t.Fatal("google client resolved with a partial configuration")
	}
	t.Setenv(GoogleClientSecretEnv, "fixture-client-secret")
	clientID, clientSecret, err := googleClient()
	if err != nil || clientID != "fixture-client-id" || clientSecret != "fixture-client-secret" {
		t.Fatalf("client=%q/%q err=%v", clientID, clientSecret, err)
	}
}

// TestGoogleFlowsRefuseToStartWithoutClient proves a Google-family sign-in fails
// with the configuration error rather than sending an empty client id upstream.
func TestGoogleFlowsRefuseToStartWithoutClient(t *testing.T) {
	t.Setenv(GoogleClientIDEnv, "")
	t.Setenv(GoogleClientSecretEnv, "")
	for _, provider := range []string{"gemini-cli", "antigravity"} {
		stub := &stubClient{}
		if _, err := flowFor(t, provider).Authorize(context.Background(), Params{Client: stub}); err == nil {
			t.Errorf("%s started a sign-in with no OAuth client", provider)
		}
		if len(stub.requests) != 0 {
			t.Errorf("%s sent a request with no OAuth client", provider)
		}
	}
}

// TestGoogleSignInLoadCodeAssistMatchesReference pins the loadCodeAssist call
// each Google sign-in makes (#240): both carry the host platform enum
// (src/lib/oauth/constants/oauth.js:12-19,54-56); gemini-cli adds mode 1
// (gemini-cli.js:51-63); antigravity sends metadata alone with the IDE user
// agent and x-request-source (antigravity.js:61-67,82-86).
func TestGoogleSignInLoadCodeAssistMatchesReference(t *testing.T) {
	t.Setenv(GoogleClientIDEnv, "fixture-client-id")
	t.Setenv(GoogleClientSecretEnv, "fixture-client-secret")
	metadata := map[string]any{"ideType": 9.0, "platform": float64(cloudCodePlatform()), "pluginType": 2.0}
	for _, tc := range []struct {
		provider  string
		body      map[string]any
		userAgent string
		source    string
	}{
		{"gemini-cli", map[string]any{"metadata": metadata, "mode": 1.0}, "", ""},
		{"antigravity", map[string]any{"metadata": metadata}, "antigravity/ide/2.11.0 darwin/arm64", "local"},
	} {
		stub := &stubClient{handler: func(request *http.Request) (int, string) {
			switch {
			case strings.HasSuffix(request.URL.Path, "/token"):
				return http.StatusOK, `{"access_token":"fixture-access","refresh_token":"fixture-refresh","expires_in":3600}`
			case strings.HasSuffix(request.URL.Path, ":loadCodeAssist"):
				return http.StatusOK, `{"cloudaicompanionProject":{"id":"fixture-project"}}`
			}
			return http.StatusOK, `{"email":"fixture@example.test"}`
		}}
		result, err := flowFor(t, tc.provider).Exchange(context.Background(), Params{
			Client: stub, Input: "fixture-code", RedirectURI: "http://localhost:8080/callback",
		})
		if err != nil {
			t.Fatalf("%s: %v", tc.provider, err)
		}
		if result.Secret.ProviderData["projectId"] != "fixture-project" {
			t.Fatalf("%s: projectId=%q", tc.provider, result.Secret.ProviderData["projectId"])
		}
		index := -1
		for i, request := range stub.requests {
			if strings.HasSuffix(request.URL.Path, ":loadCodeAssist") {
				index = i
			}
		}
		if index < 0 {
			t.Fatalf("%s: no loadCodeAssist request", tc.provider)
		}
		request := stub.requests[index]
		if request.URL.String() != "https://cloudcode-pa.googleapis.com/v1internal:loadCodeAssist" {
			t.Fatalf("%s: endpoint=%s", tc.provider, request.URL)
		}
		var body map[string]any
		if err := json.Unmarshal([]byte(stub.bodies[index]), &body); err != nil {
			t.Fatalf("%s: body %q: %v", tc.provider, stub.bodies[index], err)
		}
		if !reflect.DeepEqual(body, tc.body) {
			t.Fatalf("%s: body=%v want %v", tc.provider, body, tc.body)
		}
		if got := request.Header.Get("Authorization"); got != "Bearer fixture-access" {
			t.Fatalf("%s: authorization=%q", tc.provider, got)
		}
		if got := request.Header.Get("User-Agent"); got != tc.userAgent {
			t.Fatalf("%s: user-agent=%q want %q", tc.provider, got, tc.userAgent)
		}
		if got := request.Header.Get("x-request-source"); got != tc.source {
			t.Fatalf("%s: x-request-source=%q want %q", tc.provider, got, tc.source)
		}
	}
}

// cloudCodePlatform restates the reference's platform enum independently of the
// code under test (oauth.js:12-19).
func cloudCodePlatform() int {
	switch runtime.GOOS {
	case "darwin":
		if runtime.GOARCH == "arm64" {
			return 2
		}
		return 1
	case "linux":
		if runtime.GOARCH == "arm64" {
			return 4
		}
		return 3
	case "windows":
		return 5
	}
	return 0
}
