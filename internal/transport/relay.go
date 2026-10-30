package transport

import (
	"errors"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// Relay request headers: a relay (a Vercel, Cloudflare, Deno or Netlify function)
// receives the request addressed to itself and forwards it to the destination
// named here (open-sse/utils/proxyFetch.js:97-110; the relay function itself is
// internal/proxy/netlify).
const (
	RelayTargetHeader = "X-Relay-Target"
	RelayPathHeader   = "X-Relay-Path"
)

var errRelayTarget = errors.New("a relay cannot carry a request to a non-public destination")

// relayTransport sends each request to the relay URL instead of its destination,
// with the destination in the relay headers. Method, body and every other header
// are kept: the relay is the operator's own function and speaks for the request.
type relayTransport struct {
	relay *url.URL
	next  http.RoundTripper
}

func (t relayTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request == nil || request.URL == nil || request.URL.Hostname() == "" {
		return nil, errors.New("outbound request URL is required")
	}
	if request.URL.Scheme != "http" && request.URL.Scheme != "https" {
		return nil, errors.New("a relay carries http and https requests only")
	}
	// The destination is reached by the relay, not from here, so the dial-time
	// guard cannot vouch for it: refuse what can never be a public host.
	if host := request.URL.Hostname(); strings.EqualFold(host, "localhost") || (net.ParseIP(host) != nil && !allowedIP(net.ParseIP(host), false)) {
		closeBody(request)
		return nil, errRelayTarget
	}
	relayed := request.Clone(request.Context())
	relayed.URL = t.relay
	relayed.Host = t.relay.Host
	relayed.RequestURI = ""
	relayed.Header = request.Header.Clone()
	if relayed.Header == nil {
		relayed.Header = http.Header{}
	}
	relayed.Header.Set(RelayTargetHeader, request.URL.Scheme+"://"+jsHost(request.URL))
	relayed.Header.Set(RelayPathHeader, relayPath(request.URL))
	return t.next.RoundTrip(relayed)
}

func (t relayTransport) CloseIdleConnections() {
	if closer, ok := t.next.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

func closeBody(request *http.Request) {
	if request != nil && request.Body != nil {
		_ = request.Body.Close()
	}
}

// jsHost is URL.host as JavaScript spells it: the port is left out when it is the
// scheme's default.
func jsHost(u *url.URL) string {
	host := u.Host
	if port := u.Port(); (u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80") {
		host = strings.TrimSuffix(host, ":"+port)
	}
	return host
}

// relayPath is URL.pathname + URL.search as JavaScript spells it.
func relayPath(u *url.URL) string {
	path := u.EscapedPath()
	if path == "" {
		path = "/"
	}
	if u.RawQuery != "" {
		path += "?" + u.RawQuery
	}
	return path
}
