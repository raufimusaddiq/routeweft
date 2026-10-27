package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"
)

// ProxyPolicy is the compiled outbound proxy configuration for one connection:
// a global proxy, an optional per-connection proxy, a no-proxy list, and the
// SSRF policy (PRD-ROUTE-005, SPEC §20).
type ProxyPolicy struct {
	Enabled            bool
	GlobalProxyURL     string
	ConnectionProxyURL string
	NoProxy            []string
	AllowPrivate       bool
	// ConnectionOwnsNoProxy marks ConnectionProxyURL as a LiteRouter
	// per-connection proxy (providerSpecificData.connectionProxyUrl). Only
	// ConnectionNoProxy bypasses it, and a bypassed host takes the global proxy
	// under NoProxy (open-sse/utils/proxyFetch.js:62-73,112-114). A pool member
	// leaves it false, so NoProxy bypasses the member as well.
	ConnectionOwnsNoProxy bool
	ConnectionNoProxy     []string
}

// ProxyForHost exposes the compiled precedence for tests and callers that need
// to inspect the effective proxy: a connection-level proxy wins over the global
// one, and a no-proxy match wins over both (PRD-ROUTE-005, SPEC §20), except
// that a per-connection proxy answers only to its own no-proxy list. A nil
// result means the request dials directly.
func (p ProxyPolicy) ProxyForHost(host string) (*url.URL, error) {
	return p.proxyFor(host)
}

// proxyFor returns the effective proxy URL for a destination host, or nil when
// the host is in the no-proxy list or proxying is disabled.
func (p ProxyPolicy) proxyFor(host string) (*url.URL, error) {
	if !p.Enabled {
		return nil, nil
	}
	raw := p.ConnectionProxyURL
	if p.ConnectionOwnsNoProxy && raw != "" {
		if !noProxyListMatches(p.ConnectionNoProxy, host) {
			return parseProxyURL(raw)
		}
		raw = ""
	}
	if noProxyListMatches(p.NoProxy, host) {
		return nil, nil
	}
	if raw == "" {
		raw = p.GlobalProxyURL
	}
	if raw == "" {
		return nil, nil
	}
	return parseProxyURL(raw)
}

func parseProxyURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Host == "" {
		return nil, errors.New("invalid proxy URL")
	}
	switch parsed.Scheme {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, errors.New("unsupported proxy scheme")
	}
	return parsed, nil
}

func noProxyListMatches(entries []string, host string) bool {
	for _, entry := range entries {
		if noProxyMatches(entry, host) {
			return true
		}
	}
	return false
}

// noProxyMatches applies the pin's bypass rules
// (open-sse/utils/proxyFetch.js:11-24): case-insensitive hostname match where
// "*" matches everything, a bare "example.com" matches itself and every
// subdomain, and ".example.com" matches every subdomain and the apex itself.
func noProxyMatches(entry, host string) bool {
	entry = strings.ToLower(strings.TrimSpace(entry))
	host = strings.ToLower(host)
	switch {
	case entry == "" || host == "":
		return false
	case entry == "*":
		return true
	case strings.HasPrefix(entry, "."):
		return strings.HasSuffix(host, entry) || host == entry[1:]
	default:
		return host == entry || strings.HasSuffix(host, "."+entry)
	}
}

// PooledClients caches one http.Client per material transport configuration so
// requests never build a transport per call (SPEC §20).
type PooledClients struct {
	mu      sync.Mutex
	clients map[string]*http.Client
}

// NewPooledClients builds an empty client pool.
func NewPooledClients() *PooledClients { return &PooledClients{clients: make(map[string]*http.Client)} }

// Client returns a shared SSRF-protected client for the policy.
func (p *PooledClients) Client(policy ProxyPolicy) (*http.Client, error) {
	key, err := policyCacheKey(policy)
	if err != nil {
		return nil, err
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if client, ok := p.clients[key]; ok {
		return client, nil
	}
	client, err := newProxiedClient(policy)
	if err != nil {
		return nil, err
	}
	p.clients[key] = client
	return client, nil
}

// Release drops and closes the cached client for a policy. Operator-triggered
// connectivity tests use it for one-off proxy URLs so an unbounded set of
// untrusted inputs cannot accumulate transports.
func (p *PooledClients) Release(policy ProxyPolicy) {
	key, err := policyCacheKey(policy)
	if err != nil {
		return
	}
	p.mu.Lock()
	client := p.clients[key]
	delete(p.clients, key)
	p.mu.Unlock()
	if client != nil {
		client.CloseIdleConnections()
	}
}

func policyCacheKey(policy ProxyPolicy) (string, error) {
	key := fmt.Sprintf("enabled=%t|global=%s|connection=%s|private=%t|owns=%t|", policy.Enabled, policy.GlobalProxyURL, policy.ConnectionProxyURL, policy.AllowPrivate, policy.ConnectionOwnsNoProxy)
	noProxy := append([]string(nil), policy.NoProxy...)
	sort.Strings(noProxy)
	key += strings.Join(noProxy, ",")
	if policy.ConnectionOwnsNoProxy {
		connectionNoProxy := append([]string(nil), policy.ConnectionNoProxy...)
		sort.Strings(connectionNoProxy)
		key += "|" + strings.Join(connectionNoProxy, ",")
	}
	if _, err := policy.proxyFor(""); err != nil {
		return "", err
	}
	if policy.AllowPrivate {
		key += "|trusted-local"
	}
	return key, nil
}

func newProxiedClient(policy ProxyPolicy) (*http.Client, error) {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	httpTransport := &http.Transport{
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		DialContext:           dialGuard(dialer.DialContext, policy.AllowPrivate),
	}
	if policy.Enabled {
		httpTransport.Proxy = func(request *http.Request) (*url.URL, error) {
			return policy.proxyFor(request.URL.Hostname())
		}
	}
	return &http.Client{Transport: destinationGuard{next: httpTransport, allowPrivate: policy.AllowPrivate}, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

// destinationGuard validates the destination itself before handing a request
// to an outbound proxy. DialContext alone only sees the proxy address when a
// proxy is configured, so without this check the proxy could reach private or
// metadata targets on Routeweft's behalf.
type destinationGuard struct {
	next         http.RoundTripper
	allowPrivate bool
}

// CloseIdleConnections lets http.Client.CloseIdleConnections reach the wrapped
// transport.
func (g destinationGuard) CloseIdleConnections() {
	if closer, ok := g.next.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func (g destinationGuard) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil {
		return nil, errors.New("outbound request URL is required")
	}
	validate := ValidatePublicURL
	if g.allowPrivate {
		validate = ValidateTrustedLocalURL
	}
	if _, err := validate(request.URL.String()); err != nil {
		return nil, err
	}
	if err := validateDestinationHost(request.Context(), request.URL.Hostname(), g.allowPrivate); err != nil {
		return nil, err
	}
	return g.next.RoundTrip(request)
}

func validateDestinationHost(ctx context.Context, host string, allowPrivate bool) error {
	if ip := net.ParseIP(host); ip != nil {
		if !allowedIP(ip, allowPrivate) {
			return errors.New("outbound URL must not target a non-public address")
		}
		return nil
	}
	addresses, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil || len(addresses) == 0 {
		return errors.New("outbound host resolution failed")
	}
	for _, address := range addresses {
		if !allowedIP(address.IP, allowPrivate) {
			return errors.New("outbound host resolves to a non-public address")
		}
	}
	return nil
}
