package transport

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestValidatePublicURL(t *testing.T) {
	valid := []string{"https://api.openai.com/v1", "http://example.com", "https://8.8.8.8"}
	for _, raw := range valid {
		if _, err := ValidatePublicURL(raw); err != nil {
			t.Errorf("rejected %s: %v", raw, err)
		}
	}
	invalid := []string{
		"", "ftp://example.com", "https://", "https://user:pass@example.com", "https://example.com?q=1",
		"https://example.com#x", "http://127.0.0.1:8080", "http://10.0.0.5", "http://192.168.1.1",
		"http://169.254.169.254", "http://[::1]", "http://[fe80::1]", "http://[::ffff:127.0.0.1]", "http://0.0.0.0",
	}
	for _, raw := range invalid {
		if _, err := ValidatePublicURL(raw); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
}

func TestDialGuardBlocksPrivateTargets(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	server.Listener.Close()
	server.Listener = listener
	server.Start()
	defer server.Close()
	client := NewSSRFProtectedClient()
	if _, err := client.Get(server.URL); err == nil {
		t.Fatal("SSRF client reached a loopback upstream")
	}
	dialer := &net.Dialer{}
	if _, err := dialGuard(dialer.DialContext, false)(context.Background(), "tcp", "127.0.0.1:80"); err == nil {
		t.Fatal("dial guard allowed loopback")
	}
	if _, err := ValidateTrustedLocalURL("http://127.0.0.1:8080"); err != nil {
		t.Fatalf("trusted-local URL rejected: %v", err)
	}
	for _, raw := range []string{"http://169.254.169.254", "http://[fe80::1]", "http://0.0.0.0"} {
		if _, err := ValidateTrustedLocalURL(raw); err == nil {
			t.Errorf("trusted-local accepted %s", raw)
		}
	}
}
