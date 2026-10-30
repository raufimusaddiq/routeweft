package transport

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
)

type relayPin struct {
	Fetches []struct {
		URL          string         `json:"url"`
		Options      map[string]any `json:"options"`
		ProxyOptions map[string]any `json:"proxyOptions"`
		Sent         []struct {
			Target  string            `json:"target"`
			Method  *string           `json:"method"`
			Headers map[string]string `json:"headers"`
			Body    *string           `json:"body"`
		} `json:"sent"`
		Throws string `json:"throws"`
	} `json:"fetches"`
}

// TestRelayCarriesTheRequestLikeTheOraclesProxyAwareFetch replays the pin's relay
// fetches: the call goes to the relay with the method, body and headers it had plus
// x-relay-target (scheme and host) and x-relay-path (path and query).
func TestRelayCarriesTheRequestLikeTheOraclesProxyAwareFetch(t *testing.T) {
	raw, err := os.ReadFile("../proxy/testdata/proxy_pools_pin.json")
	if err != nil {
		t.Fatal(err)
	}
	var pin relayPin
	if err := json.Unmarshal(raw, &pin); err != nil {
		t.Fatal(err)
	}
	relayed := 0
	for _, c := range pin.Fetches {
		relayURL, _ := c.ProxyOptions["vercelRelayUrl"].(string)
		relayURL = strings.TrimSpace(relayURL)
		if relayURL == "" || len(c.Sent) != 1 {
			continue
		}
		want := c.Sent[0]
		var got struct {
			method, body string
			header       http.Header
			host         string
		}
		relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, _ := io.ReadAll(r.Body)
			got.method, got.body, got.header, got.host = r.Method, string(body), r.Header.Clone(), r.Host
			w.Write([]byte("relayed"))
		}))
		client, err := NewPooledClients().Client(ProxyPolicy{RelayURL: relay.URL, AllowPrivate: true})
		if err != nil {
			t.Fatal(err)
		}
		method, _ := c.Options["method"].(string)
		if method == "" {
			method = http.MethodGet
		}
		body, _ := c.Options["body"].(string)
		request, _ := http.NewRequest(method, c.URL, strings.NewReader(body))
		if headers, ok := c.Options["headers"].(map[string]any); ok {
			for k, v := range headers {
				request.Header.Set(k, v.(string))
			}
		}
		response, err := client.Do(request)
		if err != nil {
			t.Fatalf("%s: %v", c.URL, err)
		}
		response.Body.Close()
		relay.Close()
		if want.Method != nil && got.method != *want.Method {
			t.Errorf("%s: relayed as %s, oracle %s", c.URL, got.method, *want.Method)
		}
		if want.Body != nil && got.body != *want.Body {
			t.Errorf("%s: body %q, oracle %q", c.URL, got.body, *want.Body)
		}
		for name, value := range want.Headers {
			if actual := got.header.Get(name); actual != value {
				t.Errorf("%s: header %s = %q, oracle %q", c.URL, name, actual, value)
			}
		}
		if got.host == "" || strings.Contains(got.host, "api.openai.com") {
			t.Errorf("%s: the relay was addressed as %q", c.URL, got.host)
		}
		relayed++
	}
	if relayed < 3 {
		t.Fatalf("only %d relay fetches replayed", relayed)
	}
}

func TestRelayRefusesDestinationsNoRelayCanReach(t *testing.T) {
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("reached the relay") }))
	defer relay.Close()
	client, err := NewPooledClients().Client(ProxyPolicy{RelayURL: relay.URL, AllowPrivate: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range []string{"http://127.0.0.1:9/x", "http://localhost/x", "https://169.254.169.254/latest", "ftp://example.com/x"} {
		request, _ := http.NewRequest(http.MethodGet, target, nil)
		if response, err := client.Do(request); err == nil {
			response.Body.Close()
			t.Errorf("%s was relayed", target)
		}
	}
}

func TestRelayHostAndPathSpelling(t *testing.T) {
	cases := map[string][2]string{
		"https://api.anthropic.com:8443/v1/messages?beta=true": {"api.anthropic.com:8443", "/v1/messages?beta=true"},
		"https://api.openai.com:443/v1/x":                      {"api.openai.com", "/v1/x"},
		"http://upstream.example:80":                           {"upstream.example", "/"},
		"https://h.example/a%20b/c?x=1&y=%2F":                  {"h.example", "/a%20b/c?x=1&y=%2F"},
	}
	for raw, want := range cases {
		u := mustParse(t, raw)
		if host, path := jsHost(u), relayPath(u); host != want[0] || path != want[1] {
			t.Errorf("%s: %q %q, want %q %q", raw, host, path, want[0], want[1])
		}
	}
}

func TestStrictPolicyRefusesWhatNoProxyApplies(t *testing.T) {
	// The destination is reachable directly, and the pool's bypass list excludes it:
	// without strict it goes direct, with strict it must not.
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("direct")) }))
	defer direct.Close()
	host := mustParse(t, direct.URL).Hostname()
	policy := ProxyPolicy{Enabled: true, ConnectionProxyURL: "http://127.0.0.1:1", PoolNoProxy: []string{host}, AllowPrivate: true, Intended: true}
	get := func(policy ProxyPolicy) (string, error) {
		client, err := NewPooledClients().Client(policy)
		if err != nil {
			t.Fatal(err)
		}
		response, err := client.Get(direct.URL)
		if err != nil {
			return "", err
		}
		defer response.Body.Close()
		body, _ := io.ReadAll(response.Body)
		return string(body), nil
	}
	if body, err := get(policy); err != nil || body != "direct" {
		t.Fatalf("a bypassed host goes direct when the pool is not strict: %q %v", body, err)
	}
	strict := policy
	strict.Strict = true
	if body, err := get(strict); err == nil {
		t.Fatalf("a strict pool let a bypassed host go direct: %q", body)
	}
	// A pool that does not mention the host still uses its member (here a dead
	// proxy), so the request fails for the proxy's sake, never by going direct.
	through := policy
	through.PoolNoProxy = []string{"elsewhere.example"}
	if body, err := get(through); err == nil {
		t.Fatalf("the member must be used for a host it does not bypass: %q", body)
	}
	if proxy, err := policy.ProxyForHost(host); err != nil || proxy != nil {
		t.Fatalf("a bypassed host takes the global proxy, which is none: %v %v", proxy, err)
	}
	if proxy, err := through.ProxyForHost(host); err != nil || proxy == nil {
		t.Fatalf("a host the bypass list does not name uses the member: %v %v", proxy, err)
	}
}

func mustParse(t *testing.T, raw string) *url.URL {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	return u
}
