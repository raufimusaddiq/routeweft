// Package security tests the shared security-boundary primitives and proves
// that every operator-configurable outbound path resolves through one SSRF
// policy (PRD-SEC-002, PRD-SEC-003, PRD-SEC-004; SPEC §20, §28.1, §29).
package security

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/adminauth"
	controlapi "github.com/raufimusaddiq/routeweft/internal/control/api"
	"github.com/raufimusaddiq/routeweft/internal/providers/discovery"
	"github.com/raufimusaddiq/routeweft/internal/providers/generic"
	"github.com/raufimusaddiq/routeweft/internal/transforms"
	"github.com/raufimusaddiq/routeweft/internal/transforms/headroom"
	"github.com/raufimusaddiq/routeweft/internal/transforms/pxpipe"
	"github.com/raufimusaddiq/routeweft/internal/transport"
)

type fakeSettings struct{ values map[string]string }

func (f *fakeSettings) Settings() map[string]string { return f.values }
func (f *fakeSettings) SetSettings(_ context.Context, values map[string]string, remove []string) (uint64, error) {
	for key, value := range values {
		f.values[key] = value
	}
	for _, key := range remove {
		delete(f.values, key)
	}
	return 1, nil
}

// loginFixture builds a control-API handler with a real admin store so the
// trusted-proxy and header-hygiene behavior can be exercised end to end.
func loginFixture(t *testing.T) (*http.ServeMux, *adminauth.Store) {
	t.Helper()
	store := newAdminStore(t)
	handler := controlapi.New(controlapi.Options{Accounts: store, Sessions: adminauth.NewSessionManager(0), Settings: &fakeSettings{values: map[string]string{}}})
	mux := http.NewServeMux()
	handler.Attach(mux)
	return mux, store
}

// blockedLiterals is the canonical negative corpus every entry point must
// reject: loopback, RFC1918, link-local, cloud metadata, IPv6 loopback and a
// v4-mapped loopback, plus the wildcard bind address.
var blockedLiterals = []string{
	"http://127.0.0.1:8080",
	"http://10.0.0.5",
	"http://192.168.1.1",
	"http://172.16.9.9",
	"http://169.254.169.254",
	"http://[::1]",
	"http://[fe80::1]",
	"http://[::ffff:127.0.0.1]",
	"http://0.0.0.0",
}

// malformedURLs are rejected by shape before any DNS/dial work.
var malformedURLs = []string{
	"", "ftp://example.com", "file:///etc/passwd", "https://",
	"https://user:pass@example.com", "https://example.com?q=1", "https://example.com#x",
}

func TestValidatePublicURLRejectsFullNegativeCorpus(t *testing.T) {
	for _, raw := range blockedLiterals {
		if _, err := transport.ValidatePublicURL(raw); err == nil {
			t.Errorf("strict URL validation accepted %s", raw)
		}
	}
	for _, raw := range malformedURLs {
		if _, err := transport.ValidatePublicURL(raw); err == nil {
			t.Errorf("strict URL validation accepted %q", raw)
		}
	}
	for _, raw := range []string{"https://api.openai.com/v1", "http://example.com", "https://8.8.8.8"} {
		if _, err := transport.ValidatePublicURL(raw); err != nil {
			t.Errorf("strict URL validation rejected %s: %v", raw, err)
		}
	}
}

func TestTrustedLocalURLAllowsLANButStillBlocksMetadataAndUnspecified(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:8080", "http://10.0.0.5", "http://192.168.1.1", "http://[::1]"} {
		if _, err := transport.ValidateTrustedLocalURL(raw); err != nil {
			t.Errorf("trusted-local validation rejected LAN/loopback %s: %v", raw, err)
		}
	}
	for _, raw := range []string{"http://169.254.169.254", "http://100.100.100.200", "http://0.0.0.0", "http://[::]", "http://[fe80::1]", "https://user:pass@10.0.0.5"} {
		if _, err := transport.ValidateTrustedLocalURL(raw); err == nil {
			t.Errorf("trusted-local validation accepted %s", raw)
		}
	}
}

// TestProtectedClientsBlockPrivateDial exercises the runtime guards, not just
// URL parsing: a live loopback server is unreachable for the strict client and
// reachable for the explicit trusted-local client.
func TestProtectedClientsBlockPrivateDial(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	defer server.Close()
	if _, err := transport.NewSSRFProtectedClient().Get(server.URL); err == nil {
		t.Fatal("strict client reached a loopback upstream")
	}
	response, err := transport.NewTrustedLocalSSRFProtectedClient().Get(server.URL)
	if err != nil {
		t.Fatalf("trusted-local client could not reach loopback: %v", err)
	}
	_ = response.Body.Close()
}

// TestProtectedClientsDoNotFollowRedirects proves the SSRF clients do not let a
// public URL bounce a request into a metadata destination (PRD-SEC-003).
func TestProtectedClientsDoNotFollowRedirects(t *testing.T) {
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer redirector.Close()
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, redirector.URL, nil)
	response, err := transport.NewTrustedLocalSSRFProtectedClient().Do(request)
	if err != nil {
		return // a refused redirect is acceptable; what matters is that it is not followed
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusFound {
		t.Fatalf("SSRF client followed a redirect: status=%d", response.StatusCode)
	}
}

// TestGenericProviderDiscoveryUsesSharedPolicy proves generic model discovery
// honors the same strict/trusted-local split as every other outbound fetch.
func TestGenericProviderDiscoveryUsesSharedPolicy(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"model-a"}]}`))
	}))
	defer server.Close()
	node := generic.Node{BaseURL: server.URL}
	if _, err := generic.DiscoverModels(context.Background(), node, generic.Connection{}, false, nil); err == nil {
		t.Fatal("generic discovery reached a loopback node under the strict policy")
	}
	models, err := generic.DiscoverModels(context.Background(), node, generic.Connection{}, true, nil)
	if err != nil {
		t.Fatalf("generic discovery rejected a trusted-local node: %v", err)
	}
	if len(models) != 1 || models[0] != "model-a" {
		t.Fatalf("unexpected discovered models: %v", models)
	}
}

// TestDiscoveryClientUsesSharedPolicy proves declared provider discovery runs
// the shared URL-shape guard instead of trusting provider metadata blindly.
func TestDiscoveryClientUsesSharedPolicy(t *testing.T) {
	for _, raw := range []string{"ftp://discovery.example", "https://user:pass@discovery.example", "http://169.254.169.254"} {
		client := &discovery.Client{}
		if _, err := client.Models(context.Background(), discovery.Request{ProviderID: "p", BaseURL: raw}); err == nil {
			t.Errorf("discovery accepted %s under the strict policy", raw)
		}
	}
	if _, err := (&discovery.Client{}).Models(context.Background(), discovery.Request{ProviderID: "p", BaseURL: "file:///etc/passwd"}); err == nil {
		t.Fatal("discovery accepted a non-HTTP base URL")
	}
}

// TestHeadroomAndPXPIPEEndpointsUseSharedPolicy proves the token-saver/pipe
// server-side fetches reject private endpoints and malformed shapes before any
// content leaves the process.
func TestHeadroomAndPXPIPEEndpointsUseSharedPolicy(t *testing.T) {
	for _, raw := range []string{"http://127.0.0.1:8787", "http://169.254.169.254/pipe", "file:///etc/passwd", "https://user:pass@pipe.example"} {
		request := transforms.Request{System: []string{"secret system prompt"}, Messages: []transforms.Message{{Role: "user", Content: "secret"}}}
		if _, err := (headroom.Transform{URL: raw}).Apply(request); err == nil {
			t.Errorf("headroom accepted %s", raw)
		}
		if _, err := (pxpipe.Transform{Enabled: true, URL: raw, MinChars: 1}).Apply(request); err == nil {
			t.Errorf("pxpipe accepted %s", raw)
		}
	}
}

// TestTrustedProxyHeadersDoNotInfluenceThrottleBuckets proves PRD-SEC-002: a
// client cannot escape (or poison) the login throttle by spoofing forwarded-IP
// headers, because the bucket key is the direct socket peer only.
func TestTrustedProxyHeadersDoNotInfluenceThrottleBuckets(t *testing.T) {
	mux, _ := loginFixture(t)
	login := func(remote, forwarded string) *httptest.ResponseRecorder {
		request := httptest.NewRequest(http.MethodPost, "/admin/v1/auth/login", strings.NewReader(`{"username":"operator","password":"wrong"}`))
		request.RemoteAddr = remote
		if forwarded != "" {
			request.Header.Set("X-Forwarded-For", forwarded)
			request.Header.Set("X-Real-IP", forwarded)
		}
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		return recorder
	}
	blocked := false
	for i := 0; i < 12; i++ {
		// Rotating a spoofed forwarded IP must not reset the throttle bucket.
		if login("203.0.113.7:1234", fmt.Sprintf("10.0.0.%d", i)).Code == http.StatusTooManyRequests {
			blocked = true
			break
		}
	}
	if !blocked {
		t.Fatal("spoofed forwarded headers bypassed the login throttle")
	}
	// A genuinely different socket peer is unaffected.
	if code := login("198.51.100.9:5555", "203.0.113.7").Code; code != http.StatusUnauthorized {
		t.Fatalf("distinct peer status=%d want 401", code)
	}
}
