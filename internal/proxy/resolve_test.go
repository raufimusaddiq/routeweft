package proxy

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

type pinFile struct {
	Resolved []struct {
		ProviderSpecificData map[string]any `json:"providerSpecificData"`
		Result               map[string]any `json:"result"`
	} `json:"resolved"`
	Rotation []struct {
		IDs      []string  `json:"ids"`
		Strategy *string   `json:"strategy"`
		Provider string    `json:"provider"`
		Random   []float64 `json:"random"`
		Picked   *string   `json:"picked"`
	} `json:"rotation"`
}

func loadPin(t *testing.T) pinFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/proxy_pools_pin.json")
	if err != nil {
		t.Fatal(err)
	}
	var pin pinFile
	if err := json.Unmarshal(raw, &pin); err != nil {
		t.Fatal(err)
	}
	return pin
}

// The pools the generator's connectionProxy.js run looked up, as Routeweft sees them.
var pinPools = map[string]PoolInfo{
	"active":         {Found: true, Enabled: true, Kind: KindHTTP, URL: "http://p:8080", NoProxy: "a.com"},
	"strict":         {Found: true, Enabled: true, Kind: KindHTTP, Strict: true, URL: "http://s:1"},
	"inactive":       {Found: true, Kind: KindHTTP, URL: "http://i:1"},
	"inactiveStrict": {Found: true, Kind: KindHTTP, Strict: true, URL: "http://i:1"},
	"empty":          {Found: true, Enabled: true, Kind: KindHTTP},
	"emptyStrict":    {Found: true, Enabled: true, Kind: KindHTTP, Strict: true},
	"vercel":         {Found: true, Enabled: true, Kind: KindVercel, URL: "https://v.example/relay", NoProxy: "x.com"},
	"cloudflare":     {Found: true, Enabled: true, Kind: KindCloudflare, Strict: true, URL: "https://c.example/"},
	"deno":           {Found: true, Enabled: true, Kind: KindDeno, URL: "https://d.example/"},
	"netlify":        {Found: true, Enabled: true, Kind: KindNetlify, URL: "https://n.netlify.app/.netlify/functions/relay"},
	"inactiveRelay":  {Found: true, Kind: KindNetlify, Strict: true, URL: "https://n.example/"},
}

func str(m map[string]any, key string) string {
	value, _ := m[key].(string)
	return value
}

func flag(m map[string]any, key string) bool {
	value, _ := m[key].(bool)
	return value
}

// TestResolveReplaysTheOraclesConnectionProxy replays the pin's 26 resolutions of
// connectionProxy.js: which pool wins, the legacy fallback, relay kinds, and the
// strict guarantee for an unusable or missing pool.
func TestResolveReplaysTheOraclesConnectionProxy(t *testing.T) {
	pin := loadPin(t)
	replayed := 0
	for _, c := range pin.Resolved {
		if _, throws := c.Result["throws"]; throws {
			continue // a failed database lookup; Routeweft reads pools from the snapshot, which cannot fail
		}
		data := c.ProviderSpecificData
		poolID := str(data, "proxyPoolId")
		in := ConnectionInput{PoolID: poolID, LegacyEnabled: flag(data, "connectionProxyEnabled"), LegacyURL: str(data, "connectionProxyUrl"), LegacyNoProxy: str(data, "connectionNoProxy")}
		in.Pool = pinPools[strings.TrimSpace(poolID)]
		got := Resolve(in)
		name := poolID
		if want := str(c.Result, "source"); got.Source != want {
			t.Errorf("%v: source %q, oracle %q", data, got.Source, want)
		}
		if want := str(c.Result, "proxyPoolId"); got.PoolID != want {
			t.Errorf("%v: pool %q, oracle %q", data, got.PoolID, want)
		}
		if want := flag(c.Result, "connectionProxyEnabled"); got.ProxyEnabled != want {
			t.Errorf("%v: enabled %v, oracle %v", data, got.ProxyEnabled, want)
		}
		if want := str(c.Result, "connectionProxyUrl"); got.ProxyURL != want {
			t.Errorf("%v: url %q, oracle %q", data, got.ProxyURL, want)
		}
		if want := str(c.Result, "connectionNoProxy"); got.NoProxy != want {
			t.Errorf("%v: noProxy %q, oracle %q", data, got.NoProxy, want)
		}
		if want := flag(c.Result, "strictProxy"); got.Strict != want {
			t.Errorf("%v: strict %v, oracle %v", data, got.Strict, want)
		}
		if want := str(c.Result, "vercelRelayUrl"); got.RelayURL != want {
			t.Errorf("%v: relay %q, oracle %q", data, got.RelayURL, want)
		}
		_ = name
		replayed++
	}
	if replayed < 20 {
		t.Fatalf("only %d resolutions replayed", replayed)
	}
}

func TestRotationReplaysTheOraclesPick(t *testing.T) {
	pin := loadPin(t)
	var rotation Rotation
	for _, c := range pin.Rotation {
		strategy := ""
		if c.Strategy != nil {
			strategy = *c.Strategy
		}
		random := func(n int) int {
			if len(c.Random) == 0 {
				return 0
			}
			return int(c.Random[0] * float64(n))
		}
		got := rotation.Pick(c.IDs, strategy, c.Provider, random)
		want := ""
		if c.Picked != nil {
			want = *c.Picked
		}
		if got != want {
			t.Errorf("%v %q %q: picked %q, oracle %q", c.IDs, strategy, c.Provider, got, want)
		}
	}
}
