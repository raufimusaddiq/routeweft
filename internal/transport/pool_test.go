package transport

import (
	"net/http"
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
	transport := first.Transport.(*http.Transport)
	proxy, err := transport.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "service.example"}})
	if err != nil || proxy == nil || proxy.Host != "proxy.example:3128" {
		t.Fatalf("proxy %v %v", proxy, err)
	}
	proxy, err = transport.Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "svc.internal.example"}})
	if err != nil || proxy != nil {
		t.Fatalf("no-proxy rule: %v %v", proxy, err)
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
	proxy, err := client.Transport.(*http.Transport).Proxy(&http.Request{URL: &url.URL{Scheme: "https", Host: "example.com"}})
	if err != nil || proxy != nil {
		t.Fatalf("wildcard no proxy: %v %v", proxy, err)
	}
}
