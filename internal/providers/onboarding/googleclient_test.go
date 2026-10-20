package onboarding

import (
	"context"
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
