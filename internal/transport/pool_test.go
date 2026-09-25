package transport

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestProxyPolicyNoProxyAndPoolReuse(t *testing.T) {
	pool := NewPooledClients()
	policy := ProxyPolicy{Enabled: true, GlobalProxyURL: "http://proxy.example:3128", NoProxy: []string{".internal.example", "localhost"}}
	first, err := pool.Client(policy)
	if err != nil {
		t.Fatal(err)
	}
	second, err := pool.Client(policy)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatal("transport client was not reused")
	}
	transport := underlyingTransport(first)
	proxy, err := transport.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "service.example"}})
	if err != nil || proxy == nil || proxy.Host != "proxy.example:3128" {
		t.Fatalf("proxy %v %v", proxy, err)
	}
	proxy, err = transport.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "svc.internal.example"}})
	if err != nil || proxy != nil {
		t.Fatalf("no-proxy rule: %v %v", proxy, err)
	}
}

func TestProxyClientRejectsPrivateDestinationBeforeProxyDial(t *testing.T) {
	proxyListener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxyListener.Close()
	proxy := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) }))
	proxy.Listener.Close()
	proxy.Listener = proxyListener
	proxy.Start()
	defer proxy.Close()
	client, err := NewPooledClients().Client(ProxyPolicy{Enabled: true, GlobalProxyURL: proxy.URL})
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://127.0.0.1:12345/private", nil)
	if _, err := client.Do(request); err == nil {
		t.Fatal("proxy client reached private destination")
	}
}

func TestProxyPolicyRejectsUnsafeSchemesAndNoProxyBypasses(t *testing.T) {
	if _, err := NewPooledClients().Client(ProxyPolicy{Enabled: true, GlobalProxyURL: "file:///tmp/sock"}); err == nil {
		t.Fatal("accepted file proxy")
	}
	pool := NewPooledClients()
	policy := ProxyPolicy{Enabled: true, GlobalProxyURL: "http://proxy.example:3128", NoProxy: []string{"*"}}
	client, err := pool.Client(policy)
	if err != nil {
		t.Fatal(err)
	}
	proxy, err := underlyingTransport(client).Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "example.com"}})
	if err != nil || proxy != nil {
		t.Fatalf("wildcard no proxy: %v %v", proxy, err)
	}
}

// underlyingTransport unwraps the shared client down to the http.Transport so
// proxy policy can be asserted directly.
func underlyingTransport(client *http.Client) *http.Transport {
	if guard, ok := client.Transport.(destinationGuard); ok {
		return guard.next.(*http.Transport)
	}
	return client.Transport.(*http.Transport)
}
