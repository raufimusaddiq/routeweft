package proxy

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/routing"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
	"github.com/raufimusaddiq/routeweft/internal/transport"
)

// namedProxy answers every request with its own name instead of forwarding
// it, so the body says which hop a request took.
func namedProxy(t *testing.T, name string) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !r.URL.IsAbs() {
			// A direct request reached a proxy: never what a row expects.
			_, _ = io.WriteString(w, name+"-not-proxied")
			return
		}
		_, _ = io.WriteString(w, name)
	}))
	t.Cleanup(server.Close)
	return server.URL
}

// legacySnapshot compiles a snapshot with one Codex connection carrying the
// given LiteRouter per-connection proxy settings, as import stores them.
func legacySnapshot(t *testing.T, settings map[string]string, legacy routing.ProviderSettings, bindings map[string]string, pools []runtime.ProxyPool) *runtime.RuntimeSnapshot {
	t.Helper()
	providers := credentials.Configuration{
		Nodes:       []credentials.Node{{ID: "codex-node", ProviderID: "codex", Kind: credentials.NodeBuiltin, Name: "Codex"}},
		Connections: []credentials.Connection{{ID: "conn", NodeID: "codex-node", ProviderID: "codex", Name: "Imported", AuthKind: credentials.AuthOAuth, Enabled: true, Settings: legacy}},
	}
	snapshot, err := (runtime.Compiler{}).Compile(runtime.Config{Settings: settings, PoolBindings: bindings, ProxyPools: pools, Providers: providers}, 1)
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// TestBinderLegacyConnectionProxy is the pin's resolution order and bypass
// rules, one row per case, observed on the wire: a usable pool member, else
// the connection's own proxy (connectionProxy.js:92-165), else the global
// proxy; only connectionNoProxy bypasses the connection proxy, and a bypassed
// host takes the global proxy under the global no-proxy list
// (proxyFetch.js:62-73,112-114).
func TestBinderLegacyConnectionProxy(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "direct") }))
	defer target.Close()
	port := strconv.Itoa(target.Listener.Addr().(*net.TCPAddr).Port)
	legacyURL, poolURL, globalURL := namedProxy(t, "legacy"), namedProxy(t, "pool"), namedProxy(t, "global")
	hostPort := strings.TrimPrefix(legacyURL, "http://")
	global := map[string]string{"outboundProxyEnabled": "true", "outboundProxyUrl": globalURL}
	usable := []runtime.ProxyPool{{ID: "pool", Enabled: true, Members: []runtime.ProxyMember{{URL: poolURL, Enabled: true}}}}
	disabled := []runtime.ProxyPool{{ID: "pool", Enabled: false, Members: []runtime.ProxyMember{{URL: poolURL, Enabled: true}}}}
	empty := []runtime.ProxyPool{{ID: "pool", Enabled: true, Members: []runtime.ProxyMember{{URL: poolURL, Enabled: false}}}}
	bound := map[string]string{"conn": "pool"}
	on := func(proxyURL, noProxy string) routing.ProviderSettings {
		return routing.ProviderSettings{ConnectionProxyEnabled: "true", ConnectionProxyURL: proxyURL, ConnectionNoProxy: noProxy}
	}
	cases := []struct {
		name     string
		settings map[string]string
		legacy   routing.ProviderSettings
		bindings map[string]string
		pools    []runtime.ProxyPool
		host     string
		want     string
	}{
		{"connection proxy alone", nil, on(legacyURL, ""), nil, nil, "127.0.0.1", "legacy"},
		{"connection proxy over the global proxy", global, on(legacyURL, ""), nil, nil, "127.0.0.1", "legacy"},
		{"usable pool over the connection proxy", global, on(legacyURL, ""), bound, usable, "127.0.0.1", "pool"},
		{"disabled pool falls to the connection proxy", global, on(legacyURL, ""), bound, disabled, "127.0.0.1", "legacy"},
		{"pool without a usable member falls to the connection proxy", global, on(legacyURL, ""), bound, empty, "127.0.0.1", "legacy"},
		{"missing pool falls to the connection proxy", global, on(legacyURL, ""), map[string]string{"conn": "gone"}, nil, "127.0.0.1", "legacy"},
		{"connection no-proxy bypass takes the global proxy", global, on(legacyURL, "localhost"), nil, nil, "localhost", "global"},
		{"connection no-proxy subdomain rule", global, on(legacyURL, ".localhost"), nil, nil, "localhost", "global"},
		{"connection no-proxy wildcard", global, on(legacyURL, "*"), nil, nil, "127.0.0.1", "global"},
		{"connection no-proxy list is comma separated and trimmed", global, on(legacyURL, " other.example , LOCALHOST "), nil, nil, "localhost", "global"},
		{"connection no-proxy miss keeps the connection proxy", global, on(legacyURL, "other.example"), nil, nil, "localhost", "legacy"},
		{"global no-proxy does not bypass the connection proxy", merge(global, map[string]string{"noProxy": `["localhost"]`}), on(legacyURL, ""), nil, nil, "localhost", "legacy"},
		{"bypassed host under the global no-proxy goes direct", merge(global, map[string]string{"noProxy": `["localhost"]`}), on(legacyURL, "localhost"), nil, nil, "localhost", "direct"},
		{"bypassed host without a global proxy goes direct", nil, on(legacyURL, "localhost"), nil, nil, "localhost", "direct"},
		{"global no-proxy still bypasses a pool member", merge(global, map[string]string{"noProxy": `["localhost"]`}), on(legacyURL, ""), bound, usable, "localhost", "direct"},
		{"host:port without a scheme is an http proxy", nil, on(hostPort, ""), nil, nil, "127.0.0.1", "legacy"},
		{"surrounding blanks are trimmed", nil, on("  "+legacyURL+"  ", ""), nil, nil, "127.0.0.1", "legacy"},
		{"disabled connection proxy is not used", global, routing.ProviderSettings{ConnectionProxyEnabled: "false", ConnectionProxyURL: legacyURL}, nil, nil, "127.0.0.1", "global"},
		{"enabled without a URL is not used", global, on("   ", ""), nil, nil, "127.0.0.1", "global"},
		{"nothing configured goes direct", nil, routing.ProviderSettings{}, nil, nil, "127.0.0.1", "direct"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := legacySnapshot(t, tc.settings, tc.legacy, tc.bindings, tc.pools)
			binder := NewBinder(func() *runtime.RuntimeSnapshot { return snapshot })
			binder.AllowLocal = true
			client := binder.ClientFor(context.Background(), "conn")
			if client == nil {
				client = transport.NewTrustedLocalSSRFProtectedClient()
			}
			response, err := client.Get("http://" + tc.host + ":" + port + "/v1/models")
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if string(body) != tc.want {
				t.Fatalf("went via %q, want %q", body, tc.want)
			}
		})
	}
}

// An unusable stored URL never turns into a direct request: the proxy was set
// to hide this address.
func TestBinderInvalidConnectionProxyRefuses(t *testing.T) {
	var reached bool
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { reached = true }))
	defer target.Close()
	for _, raw := range []string{"ftp://proxy.example:21", "http://", "http://[::1"} {
		t.Run(raw, func(t *testing.T) {
			snapshot := legacySnapshot(t, nil, routing.ProviderSettings{ConnectionProxyEnabled: "true", ConnectionProxyURL: raw}, nil, nil)
			binder := NewBinder(func() *runtime.RuntimeSnapshot { return snapshot })
			binder.AllowLocal = true
			client := binder.ClientFor(context.Background(), "conn")
			if client == nil {
				t.Fatal("an unusable connection proxy fell back to the default client")
			}
			if _, err := client.Get(target.URL); err == nil || reached {
				t.Fatalf("err=%v reached=%v, want a refusal", err, reached)
			}
		})
	}
}

// Side paths carry the connection on the context and leave through the same
// compiled proxy; work for no connection keeps the direct client.
func TestConnectionRouterUsesConnectionProxy(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = io.WriteString(w, "direct") }))
	defer target.Close()
	legacyURL := namedProxy(t, "legacy")
	snapshot := legacySnapshot(t, nil, routing.ProviderSettings{ConnectionProxyEnabled: "true", ConnectionProxyURL: legacyURL}, nil, nil)
	binder := NewBinder(func() *runtime.RuntimeSnapshot { return snapshot })
	binder.AllowLocal = true
	client := transport.RouteByConnection(transport.NewTrustedLocalSSRFProtectedClient(), binder.TransportFor)
	for _, tc := range []struct {
		name string
		ctx  context.Context
		want string
	}{
		{"connection with a proxy", transport.WithConnection(context.Background(), "conn"), "legacy"},
		{"connection without a proxy", transport.WithConnection(context.Background(), "other"), "direct"},
		{"no connection", context.Background(), "direct"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			request, _ := http.NewRequestWithContext(tc.ctx, http.MethodGet, target.URL, nil)
			response, err := client.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := io.ReadAll(response.Body)
			response.Body.Close()
			if string(body) != tc.want {
				t.Fatalf("went via %q, want %q", body, tc.want)
			}
		})
	}
}

func merge(maps ...map[string]string) map[string]string {
	merged := map[string]string{}
	for _, m := range maps {
		for key, value := range m {
			merged[key] = value
		}
	}
	return merged
}
