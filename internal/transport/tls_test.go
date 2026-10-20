package transport

import (
	"context"
	"crypto/x509"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestSystemTLSRoots(t *testing.T) {
	roots, err := x509.SystemCertPool()
	if err != nil {
		t.Fatalf("load system TLS roots: %v", err)
	}
	if roots == nil || len(roots.Subjects()) == 0 {
		t.Fatal("system TLS trust store is empty")
	}
	client, err := NewPooledClients().Client(ProxyPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	config := underlyingTransport(client).TLSClientConfig
	if config != nil && (config.RootCAs != nil || config.InsecureSkipVerify) {
		t.Fatal("pooled transport must verify certificates using system roots")
	}
	t.Logf("loaded %d system TLS root subjects", len(roots.Subjects()))
}

func TestPublicHTTPSReachability(t *testing.T) {
	if os.Getenv("ROUTEWEFT_TLS_SMOKE") != "1" {
		t.Skip("set ROUTEWEFT_TLS_SMOKE=1 to check https://example.com using the direct pooled transport")
	}
	client, err := NewPooledClients().Client(ProxyPolicy{})
	if err != nil {
		t.Fatal(err)
	}
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, "https://example.com", nil)
	if err != nil {
		t.Fatal(err)
	}
	response, err := client.Do(req)
	if err != nil {
		t.Fatalf("public HTTPS reachability: %v", err)
	}
	defer response.Body.Close()
	if response.TLS == nil || len(response.TLS.VerifiedChains) == 0 {
		t.Fatal("HTTPS completed without a verified TLS chain")
	}
	t.Logf("verified example.com TLS chain; HTTP status %d", response.StatusCode)
}
