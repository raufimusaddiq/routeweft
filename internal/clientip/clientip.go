// Package clientip resolves the address a request is attributed to for the admin
// login throttle (SPEC 12.3, PRD-SEC-002).
//
// The socket peer is the default and the only fallback. A forwarded identity is
// honored only when the connecting peer is itself a configured trusted proxy, and
// then only from the right-hand end of X-Forwarded-For, which is the part the
// trusted proxy wrote. Anything a client put on the left of that is ignored, and
// anything that does not parse makes the whole request fall back to the peer.
package clientip

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"strings"
)

// Resolver maps a request to the address its throttle bucket is keyed on. The
// zero value trusts no proxy. A Resolver is immutable and safe for concurrent use.
type Resolver struct {
	trusted []netip.Prefix
}

// New returns a Resolver trusting the given proxy prefixes.
func New(trusted []netip.Prefix) Resolver {
	return Resolver{trusted: append([]netip.Prefix(nil), trusted...)}
}

// ParseTrusted parses the ROUTEWEFT_TRUSTED_PROXIES / --trusted-proxies value: IP
// addresses and CIDR prefixes separated by commas or whitespace. An empty value
// trusts nothing. A prefix that would trust every address (/0) is refused, as is
// any entry that is not an address or a prefix, so a typo never silently widens or
// narrows the trust list.
func ParseTrusted(value string) ([]netip.Prefix, error) {
	var out []netip.Prefix
	for _, entry := range strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\n' }) {
		prefix, err := parseEntry(entry)
		if err != nil {
			return nil, fmt.Errorf("invalid trusted proxy %q: %w", entry, err)
		}
		out = append(out, prefix)
	}
	return out, nil
}

func parseEntry(entry string) (netip.Prefix, error) {
	if strings.Contains(entry, "/") {
		prefix, err := netip.ParsePrefix(entry)
		if err != nil {
			return netip.Prefix{}, errors.New("not an IP address or CIDR prefix")
		}
		prefix = unmapPrefix(prefix)
		if prefix.Bits() == 0 {
			return netip.Prefix{}, errors.New("a /0 prefix would trust every peer; list the proxies")
		}
		return prefix.Masked(), nil
	}
	addr, err := netip.ParseAddr(entry)
	if err != nil {
		return netip.Prefix{}, errors.New("not an IP address or CIDR prefix")
	}
	addr = addr.Unmap().WithZone("")
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

// unmapPrefix turns an IPv4-mapped IPv6 prefix (::ffff:a.b.c.d/n, n>=96) into the
// IPv4 prefix, so a peer reported either way matches the same entry.
func unmapPrefix(prefix netip.Prefix) netip.Prefix {
	addr := prefix.Addr()
	if addr.Is4In6() && prefix.Bits() >= 96 {
		return netip.PrefixFrom(addr.Unmap(), prefix.Bits()-96)
	}
	return prefix
}

func (r Resolver) isTrusted(addr netip.Addr) bool {
	for _, prefix := range r.trusted {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

// Key returns the throttle key for a request: the normalised client address, or
// the raw remote address when it is not an IP:port (so an odd transport still
// gets one stable bucket rather than none).
func (r Resolver) Key(req *http.Request) string {
	host := req.RemoteAddr
	if h, _, err := net.SplitHostPort(req.RemoteAddr); err == nil {
		host = h
	}
	peer, err := netip.ParseAddr(host)
	if err != nil {
		return host
	}
	peer = normalise(peer)
	if len(r.trusted) == 0 || !r.isTrusted(peer) {
		return peer.String()
	}
	if client, ok := r.forwarded(req.Header); ok {
		return client.String()
	}
	return peer.String()
}

func normalise(addr netip.Addr) netip.Addr {
	return addr.Unmap().WithZone("")
}

// forwarded reads the client identity a trusted peer vouches for. X-Forwarded-For
// is walked from the right, skipping hops that are themselves trusted proxies; the
// first hop that is not trusted is the client. A malformed hop fails closed (the
// caller falls back to the peer). X-Real-IP is consulted only when the request
// carries no X-Forwarded-For at all, because a proxy that does not rewrite it
// passes a client-supplied value through. Forwarded (RFC 7239) is never read.
func (r Resolver) forwarded(header http.Header) (netip.Addr, bool) {
	values := header.Values("X-Forwarded-For")
	if len(values) == 0 {
		return singleAddr(header.Values("X-Real-Ip"))
	}
	var hops []string
	for _, value := range values {
		hops = append(hops, strings.Split(value, ",")...)
	}
	var leftmost netip.Addr
	for i := len(hops) - 1; i >= 0; i-- {
		hop, err := netip.ParseAddr(strings.TrimSpace(hops[i]))
		if err != nil {
			return netip.Addr{}, false
		}
		hop = normalise(hop)
		if !r.isTrusted(hop) {
			return hop, true
		}
		leftmost = hop
	}
	// Every hop is a trusted proxy: the original sender is the leftmost one.
	return leftmost, leftmost.IsValid()
}

func singleAddr(values []string) (netip.Addr, bool) {
	if len(values) != 1 {
		return netip.Addr{}, false
	}
	addr, err := netip.ParseAddr(strings.TrimSpace(values[0]))
	if err != nil {
		return netip.Addr{}, false
	}
	return normalise(addr), true
}
