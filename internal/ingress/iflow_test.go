package ingress

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/providers/oauthheaders"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// iflowRig serves one iflow connection. credential is what the store hands back
// for it; an empty value is a connection with no usable secret.
func iflowRig(t *testing.T, credential string, upstream http.HandlerFunc) (*httptest.ResponseRecorder, *http.Request) {
	t.Helper()
	var seen *http.Request
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Clone(context.Background())
		upstream(w, r)
	}))
	t.Cleanup(server.Close)

	manager := newManager(t)
	_, key, err := manager.CreateAPIKey(context.Background(), "test")
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	New(manager, Options{
		AllowPrivateUpstreams: true,
		Candidates: func(*runtime.RuntimeSnapshot, string) ([]routing.Account, bool) {
			return []routing.Account{{ID: "acct", Enabled: true}}, true
		},
		AccountProvider: func(_ *runtime.RuntimeSnapshot, _, account, _ string) (routing.ProviderRef, bool) {
			return routing.ProviderRef{ProviderID: "iflow", ConnectionID: account, BaseURL: server.URL, Protocol: "openai-chat", UpstreamModel: "qwen3-coder-plus"}, true
		},
		CredentialFor: func(context.Context, string) (string, error) { return credential, nil },
	}).Attach(mux)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"qwen3-coder-plus","messages":[{"role":"user","content":"hi"}]}`))
	request.Header.Set("Authorization", "Bearer "+key)
	mux.ServeHTTP(recorder, request)
	return recorder, seen
}

// TestIflowRequestIsSigned proves a served iflow request carries the signature
// headers, and that the signature is the one the pin computes over the values
// actually sent (open-sse/executors/iflow.js:31-34,57-64).
func TestIflowRequestIsSigned(t *testing.T) {
	recorder, seen := iflowRig(t, "account-key", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"x","object":"chat.completion","choices":[]}`))
	})
	if recorder.Code != http.StatusOK {
		t.Fatalf("status %d body %s", recorder.Code, recorder.Body.String())
	}
	if seen == nil {
		t.Fatal("upstream saw no request")
	}
	userAgent := seen.Header.Get("User-Agent")
	if userAgent != oauthheaders.IflowUserAgent {
		t.Fatalf("user agent %q, want %q", userAgent, oauthheaders.IflowUserAgent)
	}
	sessionID := seen.Header.Get("session-id")
	timestamp := seen.Header.Get("x-iflow-timestamp")
	signature := seen.Header.Get("x-iflow-signature")
	if !strings.HasPrefix(sessionID, "session-") || timestamp == "" || signature == "" {
		t.Fatalf("signature headers missing: session-id=%q timestamp=%q signature=%q", sessionID, timestamp, signature)
	}
	if seen.Header.Get("Authorization") != "Bearer account-key" {
		t.Fatalf("credential header %q", seen.Header.Get("Authorization"))
	}
	mac := hmac.New(sha256.New, []byte("account-key"))
	mac.Write([]byte(userAgent + ":" + sessionID + ":" + timestamp))
	if want := hex.EncodeToString(mac.Sum(nil)); signature != want {
		t.Fatalf("signature %q, want %q over %q", signature, want, userAgent+":"+sessionID+":"+timestamp)
	}
}

// TestIflowWithoutCredentialFailsClosed proves a connection with no secret is
// never sent unsigned: the request is refused before any upstream call.
func TestIflowWithoutCredentialFailsClosed(t *testing.T) {
	recorder, seen := iflowRig(t, "", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	if seen != nil {
		t.Fatal("an unsigned iflow request reached the upstream")
	}
	if recorder.Code != http.StatusServiceUnavailable || !strings.Contains(recorder.Body.String(), "credentials_unavailable") {
		t.Fatalf("status %d body %s", recorder.Code, recorder.Body.String())
	}
}
