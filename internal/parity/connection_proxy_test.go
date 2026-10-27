package parity

import (
	"strings"
	"testing"
)

// TestConnectionProxyCallersAreCovered is the drift gate for LiteRouter's
// per-connection proxy (#249). Every pinned caller of
// resolveConnectionProxyConfig sends that connection's outbound work through
// its proxy; each must name the Routeweft path that does the same, so a caller
// the pin adds fails here until it is covered.
func TestConnectionProxyCallersAreCovered(t *testing.T) {
	o := pinnedOracle(t)
	covered := map[string]string{
		"src/sse/services/auth.js":                                      "inference: proxy.Binder.ClientFor via ingress Options.ClientFor",
		"src/app/api/usage/[connectionId]/route.js":                     "usage reads: quota.Observer.Observe",
		"src/app/api/usage/[connectionId]/codex-reset-credits/route.js": "control/api handleCodexResetCredits",
		"src/shared/services/quotaAutoPing.js":                          "quota.SendPing",
		"src/sse/services/antigravityQuota.js":                          "usage reads: quota.Observer.Observe (antigravity reader)",
		"src/app/api/providers/[id]/test/testUtils.js":                  "control/api validateConnection and testProviderModel",
		"src/app/api/providers/[id]/models/route.js":                    "control/api fetchModelEntries",
		"src/app/api/v1/models/route.js":                                "not applicable: Routeweft serves /v1/models from the snapshot catalog, with no per-connection fetch",
	}
	found := map[string]string{}
	out := o.git(t, "grep", "-l", "resolveConnectionProxyConfig", o.pin, "--", "src", "open-sse")
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		path := strings.TrimPrefix(line, o.pin+":")
		if path == "" || path == "src/lib/network/connectionProxy.js" {
			continue
		}
		found[path] = path
	}
	ratchet(t, "resolveConnectionProxyConfig callers", found, covered)
}

// TestConnectionProxySemanticsMatchPin pins the source lines Routeweft's
// resolution mirrors (internal/runtime compileConnectionProxies,
// internal/proxy Binder, internal/transport ProxyPolicy).
func TestConnectionProxySemanticsMatchPin(t *testing.T) {
	o := pinnedOracle(t)
	const resolver = "src/lib/network/connectionProxy.js"
	const fetcher = "open-sse/utils/proxyFetch.js"
	sources := map[string]string{resolver: o.file(t, resolver), fetcher: o.file(t, fetcher)}
	for _, tc := range []struct{ path, fragment, why string }{
		{resolver, `providerSpecificData?.connectionProxyEnabled === true`, "only a JSON true enables"},
		{resolver, `providerSpecificData?.connectionProxyUrl`, "the URL key"},
		{resolver, `providerSpecificData?.connectionNoProxy`, "the no-proxy key"},
		{resolver, `proxyPool.isActive === true &&`, "an inactive pool is unusable"},
		{resolver, `legacy.connectionProxyEnabled &&`, "the connection proxy is the fallback after the pool"},
		{fetcher, `const patterns = noProxy.split(",").map((p) => p.trim().toLowerCase()).filter(Boolean);`, "comma list, trimmed, case-insensitive"},
		{fetcher, `if (pattern === "*") return true;`, "wildcard"},
		{fetcher, `if (pattern.startsWith(".")) return hostname.endsWith(pattern) || hostname === pattern.slice(1);`, "leading dot matches subdomains and apex"},
		{fetcher, "return hostname === pattern || hostname.endsWith(`.${pattern}`);", "bare name matches itself and subdomains"},
		{fetcher, `if (noProxy && shouldBypassByNoProxy(targetUrl, noProxy)) return null;`, "only the connection list bypasses the connection proxy"},
		{fetcher, `const envProxyUrl = connectionProxyUrl ? null : normalizeProxyUrl(getEnvProxyUrl(targetUrl));`, "a bypassed host takes the global proxy"},
		{fetcher, "return `http://${normalizedInput}`;", "a value that is not a URL is an http proxy"},
	} {
		if !strings.Contains(sources[tc.path], tc.fragment) {
			t.Errorf("%s no longer has %q (%s); review internal/runtime, internal/proxy and internal/transport", tc.path, tc.fragment, tc.why)
		}
	}
}
