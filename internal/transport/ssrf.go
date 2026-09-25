package transport

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

// NewSSRFProtectedClient allows public HTTP(S) destinations only. Every DNS
// result is checked at dial time, redirects are disabled, and proxy env vars
// are ignored to prevent bypassing destination validation.
func NewSSRFProtectedClient() *http.Client {
	return newSSRFProtectedClient(false)
}

// NewTrustedLocalSSRFProtectedClient is the explicit trusted-local operator
// policy: public and loopback/LAN destinations are allowed, but metadata
// addresses, redirects, proxy env vars, and bad schemes remain blocked.
func NewTrustedLocalSSRFProtectedClient() *http.Client {
	return newSSRFProtectedClient(true)
}

func newSSRFProtectedClient(allowPrivate bool) *http.Client {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		Proxy:                 nil,
		DialContext:           dialGuard(dialer.DialContext, allowPrivate),
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   10,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
	}
	return &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
}

func dialGuard(dial func(context.Context, string, string) (net.Conn, error), allowPrivate bool) func(context.Context, string, string) (net.Conn, error) {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, errors.New("invalid outbound address")
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil || len(ips) == 0 {
			return nil, errors.New("outbound host resolution failed")
		}
		for _, result := range ips {
			if !allowedIP(result.IP, allowPrivate) {
				return nil, errors.New("outbound host resolves to a non-public address")
			}
		}
		var lastErr error
		for _, result := range ips {
			conn, dialErr := dial(ctx, network, net.JoinHostPort(result.IP.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		return nil, lastErr
	}
}

func allowedIP(ip net.IP, allowPrivate bool) bool {
	if !allowPrivate {
		return isPublicIP(ip)
	}
	address, err := netip.ParseAddr(ip.String())
	if err != nil {
		return false
	}
	address = address.Unmap()
	return address.IsValid() && !address.IsMulticast() && !address.IsUnspecified() && !address.IsLinkLocalUnicast() && !address.IsLinkLocalMulticast() && !isMetadataIP(address)
}

func isPublicIP(ip net.IP) bool {
	address, err := netip.ParseAddr(ip.String())
	if err != nil {
		return false
	}
	address = address.Unmap()
	return address.IsValid() && address.IsGlobalUnicast() && !address.IsPrivate() && !address.IsLoopback() && !address.IsLinkLocalUnicast() && !address.IsLinkLocalMulticast() && !address.IsMulticast() && !address.IsUnspecified() && !isMetadataIP(address)
}

func isMetadataIP(address netip.Addr) bool {
	return address == netip.MustParseAddr("169.254.169.254") || address == netip.MustParseAddr("100.100.100.200")
}

// ValidatePublicURL rejects malformed or non-HTTP(S) outbound URLs and
// obvious private literals before a request reaches the dial-time DNS guard.
func ValidatePublicURL(raw string) (*url.URL, error) {
	return validateURL(raw, false)
}

// ValidateTrustedLocalURL is the explicit trusted-local variant used by
// operator flows that may target LAN nodes.
func ValidateTrustedLocalURL(raw string) (*url.URL, error) {
	return validateURL(raw, true)
}

func validateURL(raw string, allowPrivate bool) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || strings.ContainsAny(u.Host, "\r\n") {
		return nil, errors.New("outbound URL must be absolute HTTP(S) without userinfo, query, or fragment")
	}
	if ip, parseErr := netip.ParseAddr(u.Hostname()); parseErr == nil && !allowedIP(net.IP(ip.AsSlice()), allowPrivate) {
		return nil, errors.New("outbound URL must not target a non-public address")
	}
	return u, nil
}
