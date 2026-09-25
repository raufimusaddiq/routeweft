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
}

// proxyFor returns the effective proxy URL for a destination host, or nil when
// the host is in the no-proxy list or proxying is disabled.
func (p ProxyPolicy) proxyFor(host string) (*url.URL, error) {
	if !p.Enabled {
		return nil, nil
	}
	for _, entry := range p.NoProxy {
		entry = strings.TrimSpace(entry)
		if entry == "*" || entry == host || (strings.HasPrefix(entry, ".") && strings.HasSuffix(host, entry)) {
			return nil, nil
		}
	}
	raw := p.ConnectionProxyURL
	if raw == "" {
		raw = p.GlobalProxyURL
	}
	if raw == "" {
		return nil, nil
	}
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

func policyCacheKey(policy ProxyPolicy) (string, error) {
	key := fmt.Sprintf("enabled=%t|global=%s|connection=%s|private=%t|", policy.Enabled, policy.GlobalProxyURL, policy.ConnectionProxyURL, policy.AllowPrivate)
	noProxy := append([]string(nil), policy.NoProxy...)
	sort.Strings(noProxy)
	key += strings.Join(noProxy, ",")
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
