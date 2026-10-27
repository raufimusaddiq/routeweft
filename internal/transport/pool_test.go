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

func TestPooledClientsReleaseDropsOnlyThatPolicy(t *testing.T) {
	pool := NewPooledClients()
	kept := ProxyPolicy{Enabled: true, ConnectionProxyURL: "http://kept.example:3128"}
	released := ProxyPolicy{Enabled: true, ConnectionProxyURL: "http://released.example:3128"}
	keptClient, _ := pool.Client(kept)
	releasedClient, _ := pool.Client(released)
	pool.Release(released)
	if again, _ := pool.Client(kept); again != keptClient {
		t.Fatal("release evicted an unrelated client")
	}
	if again, _ := pool.Client(released); again == releasedClient {
		t.Fatal("released policy kept its cached client")
	}
}

// TestNoProxyMatchesPinSemantics pins the bypass rules of
// open-sse/utils/proxyFetch.js:11-24 (#78/C04): case-insensitive, a bare entry
// covers its subdomains, a leading-dot entry also covers the apex.
func TestNoProxyMatchesPinSemantics(t *testing.T) {
	cases := []struct {
		entry, host string
		want        bool
	}{
		{"example.com", "example.com", true},
		{"example.com", "api.example.com", true}, // pin: endsWith("."+pattern); old Routeweft dialed via the proxy
		{"example.com", "badexample.com", false},
		{".example.com", "example.com", true}, // pin: hostname === pattern.slice(1)
		{".example.com", "api.example.com", true},
		{".example.com", "badexample.com", false},
		{"EXAMPLE.com", "Api.Example.COM", true},
		{" localhost ", "localhost", true},
		{"*", "anything.test", true},
		{"", "example.com", false},
		{"*", "", false},
	}
	for _, tc := range cases {
		if got := noProxyMatches(tc.entry, tc.host); got != tc.want {
			t.Errorf("noProxyMatches(%q,%q)=%v want %v", tc.entry, tc.host, got, tc.want)
		}
	}
	policy := ProxyPolicy{Enabled: true, GlobalProxyURL: "http://proxy.example:3128", NoProxy: []string{"example.com"}}
	if proxy, err := policy.ProxyForHost("api.example.com"); err != nil || proxy != nil {
		t.Fatalf("subdomain of a bare no-proxy entry must dial directly: %v %v", proxy, err)
	}
	if proxy, err := policy.ProxyForHost("other.test"); err != nil || proxy == nil {
		t.Fatalf("unrelated host must use the proxy: %v %v", proxy, err)
	}
}

// A pool member and a per-connection proxy at the same URL differ in which
// no-proxy list applies, so they never share a cached client.
func TestConnectionOwnedNoProxyIsMaterial(t *testing.T) {
	pool := NewPooledClients()
	member := ProxyPolicy{Enabled: true, ConnectionProxyURL: "http://proxy.example:3128", NoProxy: []string{"localhost"}}
	owned := member
	owned.ConnectionOwnsNoProxy = true
	scoped := owned
	scoped.ConnectionNoProxy = []string{"internal.example"}
	clients := map[*http.Client]string{}
	for name, policy := range map[string]ProxyPolicy{"member": member, "owned": owned, "scoped": scoped} {
		client, err := pool.Client(policy)
		if err != nil {
			t.Fatal(err)
		}
		if other, ok := clients[client]; ok {
			t.Fatalf("%s shares a client with %s", name, other)
		}
		clients[client] = name
	}
	if got, _ := member.ProxyForHost("localhost"); got != nil {
		t.Fatalf("global no-proxy did not bypass a pool member: %v", got)
	}
	if got, _ := owned.ProxyForHost("localhost"); got == nil {
		t.Fatal("global no-proxy bypassed a per-connection proxy")
	}
	if got, _ := scoped.ProxyForHost("api.internal.example"); got != nil {
		t.Fatalf("connection no-proxy did not bypass, and no global proxy is set: %v", got)
	}
}
