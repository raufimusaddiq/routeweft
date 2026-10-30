package clientip

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func resolver(t *testing.T, trusted string) Resolver {
	t.Helper()
	prefixes, err := ParseTrusted(trusted)
	if err != nil {
		t.Fatal(err)
	}
	return New(prefixes)
}

func key(r Resolver, peer string, headers map[string][]string) string {
	req := httptest.NewRequest(http.MethodPost, "/admin/v1/auth/login", nil)
	req.RemoteAddr = peer
	for name, values := range headers {
		req.Header[http.CanonicalHeaderKey(name)] = values
	}
	return r.Key(req)
}

// A peer that is not a trusted proxy is keyed on its own address no matter what
// forwarded headers it sends (PRD-SEC-002). This is the spoofing guarantee.
func TestUntrustedPeerForwardedHeadersAreIgnored(t *testing.T) {
	for name, r := range map[string]Resolver{"no proxies configured": {}, "other proxy configured": resolver(t, "10.0.0.0/8")} {
		t.Run(name, func(t *testing.T) {
			headers := map[string][]string{
				"X-Forwarded-For": {"192.0.2.1, 198.51.100.2"},
				"X-Real-Ip":       {"192.0.2.3"},
				"Forwarded":       {"for=192.0.2.4"},
			}
			if got := key(r, "203.0.113.7:4444", headers); got != "203.0.113.7" {
				t.Fatalf("key=%q, want the socket host", got)
			}
		})
	}
}

func TestTrustedPeerHonorsTheRightmostUntrustedHop(t *testing.T) {
	r := resolver(t, "10.0.0.0/8, 192.168.1.5")
	for _, tc := range []struct {
		name    string
		peer    string
		headers map[string][]string
		want    string
	}{
		{"one proxy, one client", "10.0.0.2:1", map[string][]string{"X-Forwarded-For": {"198.51.100.9"}}, "198.51.100.9"},
		// The client forged the left; the proxy appended the real peer on the right.
		{"forged left of the real client", "10.0.0.2:1", map[string][]string{"X-Forwarded-For": {"1.1.1.1, 198.51.100.9"}}, "198.51.100.9"},
		{"two proxies in the chain", "10.0.0.2:1", map[string][]string{"X-Forwarded-For": {"198.51.100.9, 10.0.0.3"}}, "198.51.100.9"},
		{"chain split over two header lines", "10.0.0.2:1", map[string][]string{"X-Forwarded-For": {"1.1.1.1", "198.51.100.9, 192.168.1.5"}}, "198.51.100.9"},
		{"junk left of the real client is never read", "10.0.0.2:1", map[string][]string{"X-Forwarded-For": {"not-an-ip, 198.51.100.9"}}, "198.51.100.9"},
		{"ipv6 client", "10.0.0.2:1", map[string][]string{"X-Forwarded-For": {"2001:db8::5"}}, "2001:db8::5"},
		{"ipv4-mapped client is normalised", "10.0.0.2:1", map[string][]string{"X-Forwarded-For": {"::ffff:198.51.100.9"}}, "198.51.100.9"},
		{"every hop trusted: the original sender", "10.0.0.2:1", map[string][]string{"X-Forwarded-For": {"10.1.1.1, 10.0.0.3"}}, "10.1.1.1"},
		{"real ip when there is no chain", "10.0.0.2:1", map[string][]string{"X-Real-Ip": {"198.51.100.9"}}, "198.51.100.9"},
		{"forwarded chain wins over real ip", "10.0.0.2:1", map[string][]string{"X-Forwarded-For": {"198.51.100.9"}, "X-Real-Ip": {"192.0.2.77"}}, "198.51.100.9"},
		{"no headers: the proxy itself", "10.0.0.2:1", nil, "10.0.0.2"},
		{"RFC 7239 Forwarded is never read", "10.0.0.2:1", map[string][]string{"Forwarded": {"for=198.51.100.9"}}, "10.0.0.2"},
		{"single trusted address entry", "192.168.1.5:9", map[string][]string{"X-Forwarded-For": {"198.51.100.9"}}, "198.51.100.9"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := key(r, tc.peer, tc.headers); got != tc.want {
				t.Fatalf("key=%q, want %q", got, tc.want)
			}
		})
	}
}

// A hop that is not an address, or a header that is empty or doubled where one
// value is required, fails closed to the proxy's own address: the attacker cannot
// pick a bucket by sending garbage.
func TestMalformedForwardedIdentityFailsClosedToThePeer(t *testing.T) {
	r := resolver(t, "10.0.0.0/8")
	for name, headers := range map[string]map[string][]string{
		"garbage hop":            {"X-Forwarded-For": {"198.51.100.9, not-an-ip"}},
		"empty value":            {"X-Forwarded-For": {""}},
		"trailing comma":         {"X-Forwarded-For": {"198.51.100.9,"}},
		"hostname":               {"X-Forwarded-For": {"evil.example"}},
		"with port":              {"X-Forwarded-For": {"198.51.100.9:443"}},
		"two real-ip lines":      {"X-Real-Ip": {"198.51.100.9", "192.0.2.1"}},
		"real ip not an address": {"X-Real-Ip": {"nope"}},
	} {
		t.Run(name, func(t *testing.T) {
			if got := key(r, "10.0.0.2:1", headers); got != "10.0.0.2" {
				t.Fatalf("key=%q, want the proxy's own address", got)
			}
		})
	}
}

func TestPeerNormalisation(t *testing.T) {
	r := resolver(t, "10.0.0.0/8")
	for peer, want := range map[string]string{
		"[2001:db8::1]:4444":      "2001:db8::1",
		"[::ffff:203.0.113.7]:80": "203.0.113.7",
		"203.0.113.7:80":          "203.0.113.7",
		"[fe80::1%eth0]:80":       "fe80::1",
		"unix-socket":             "unix-socket",
	} {
		if got := key(r, peer, nil); got != want {
			t.Errorf("peer %q key=%q, want %q", peer, got, want)
		}
	}
	// A mapped-form peer still matches an IPv4 trust entry.
	if got := key(r, "[::ffff:10.0.0.2]:80", map[string][]string{"X-Forwarded-For": {"198.51.100.9"}}); got != "198.51.100.9" {
		t.Fatalf("mapped trusted peer key=%q", got)
	}
}

func TestParseTrusted(t *testing.T) {
	prefixes, err := ParseTrusted(" 127.0.0.1 ,10.0.0.0/8\n::1  fd00::/8,,")
	if err != nil || len(prefixes) != 4 {
		t.Fatalf("prefixes=%v err=%v", prefixes, err)
	}
	if prefixes, err := ParseTrusted(""); err != nil || len(prefixes) != 0 {
		t.Fatalf("empty value: %v %v", prefixes, err)
	}
	for _, bad := range []string{"0.0.0.0/0", "::/0", "::ffff:0.0.0.0/96", "not-an-ip", "10.0.0.0/33", "10.0.0.1:80", "*", "true", "10.0.0.1, evil"} {
		if _, err := ParseTrusted(bad); err == nil {
			t.Errorf("%q was accepted", bad)
		} else if !strings.Contains(err.Error(), "invalid trusted proxy") {
			t.Errorf("%q: error %q does not name the entry", bad, err)
		}
	}
	// A host-bits prefix is masked, so 10.1.2.3/8 trusts 10.0.0.0/8.
	masked, _ := ParseTrusted("10.1.2.3/8")
	if got := New(masked).Key(func() *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.9.9.9:1"
		req.Header.Set("X-Forwarded-For", "198.51.100.9")
		return req
	}()); got != "198.51.100.9" {
		t.Fatalf("masked prefix key=%q", got)
	}
}

func TestResolverCopiesItsPrefixes(t *testing.T) {
	prefixes, _ := ParseTrusted("10.0.0.0/8")
	r := New(prefixes)
	prefixes[0], _ = parseEntry("203.0.113.0/24")
	if got := key(r, "203.0.113.7:1", map[string][]string{"X-Forwarded-For": {"198.51.100.9"}}); got != "203.0.113.7" {
		t.Fatalf("mutating the caller's slice changed the trust list: key=%q", got)
	}
}
