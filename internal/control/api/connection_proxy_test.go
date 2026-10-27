package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/discovery"
	"github.com/raufimusaddiq/routeweft/internal/proxy"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
	"github.com/raufimusaddiq/routeweft/internal/transport"
)

// connectionProxyEnv is the Codex API with store writes published into the
// runtime snapshot, as the app wires it (app.go SetPublisher).
func connectionProxyEnv(t *testing.T) (*codexEnv, *runtime.Manager) {
	t.Helper()
	env := newCodexEnv(t)
	manager := env.h.opts.Runtime.(*runtime.Manager)
	env.creds.SetPublisher(manager.UpdateProviders)
	return env, manager
}

func loadSnapshot(t *testing.T, manager *runtime.Manager) *runtime.RuntimeSnapshot {
	t.Helper()
	snapshot, err := manager.Load()
	if err != nil {
		t.Fatal(err)
	}
	return snapshot
}

// A Codex account exported by LiteRouter with a per-connection proxy is
// imported with it and served through it: inference and the side paths
// resolve the same compiled policy, and its no-proxy list bypasses it
// (connectionProxy.js:39-56,151-165; codex-reset-credits/route.js:66).
func TestImportedCodexConnectionProxyIsServed(t *testing.T) {
	env, manager := connectionProxyEnv(t)
	item := `{"accessToken":"` + unsignedJWT(codexClaims("proxy@example.invalid", "acct-proxy", "plus")) + `","refreshToken":"refresh-proxy",` +
		`"providerSpecificData":{"connectionProxyEnabled":true,"connectionProxyUrl":" http://user:secret@legacy.example:3128 ","connectionNoProxy":"auth.openai.com, .internal.example"}}`
	id, status := bulkOp("codex", item)(t, env)
	if status != http.StatusOK || id == "" {
		t.Fatalf("import status=%d id=%q", status, id)
	}
	legacy, ok := loadSnapshot(t, manager).LegacyConnectionProxy(id)
	if !ok || legacy.URL != "http://user:secret@legacy.example:3128" || strings.Join(legacy.NoProxy, "|") != "auth.openai.com|.internal.example" {
		t.Fatalf("compiled connection proxy=%+v ok=%v", legacy, ok)
	}
	binder := proxy.NewBinder(func() *runtime.RuntimeSnapshot { return loadSnapshot(t, manager) })
	policy, _ := binder.Policy(id)
	for _, tc := range []struct{ host, want string }{
		{"chatgpt.com", "legacy.example:3128"},
		{"auth.openai.com", ""},
		{"api.internal.example", ""},
		{"internal.example", ""},
	} {
		got, err := policy.ProxyForHost(tc.host)
		if err != nil {
			t.Fatal(err)
		}
		if host := hostOf(got); host != tc.want {
			t.Fatalf("%s via %q, want %q", tc.host, host, tc.want)
		}
	}
	// The list shows the proxy in force, never its credentials.
	recorder := doJSON(t, env.mux, env.cookie, http.MethodGet, "/admin/v1/connections", "")
	if recorder.Code != http.StatusOK || strings.Contains(recorder.Body.String(), "secret") || !strings.Contains(recorder.Body.String(), `"connectionProxy":{"url":"http://legacy.example:3128","noProxy":"auth.openai.com,.internal.example"}`) {
		t.Fatalf("connections status=%d body=%s", recorder.Code, recorder.Body.String())
	}
}

// The Grok CLI import spreads providerSpecificData as given
// (grok-cli/bulk-import/route.js:87), so a boolean flag survives it.
func TestImportedGrokConnectionProxyIsServed(t *testing.T) {
	env, manager := connectionProxyEnv(t)
	id, status := bulkOp("grok-cli", `{"accessToken":"grok-access","email":"grok@example.invalid","providerSpecificData":{"connectionProxyEnabled":true,"connectionProxyUrl":"socks5://legacy.example:1080"}}`)(t, env)
	if status != http.StatusOK || id == "" {
		t.Fatalf("import status=%d id=%q", status, id)
	}
	if legacy, ok := loadSnapshot(t, manager).LegacyConnectionProxy(id); !ok || legacy.URL != "socks5://legacy.example:1080" {
		t.Fatalf("compiled connection proxy=%+v ok=%v", legacy, ok)
	}
}

func hostOf(u *url.URL) string {
	if u == nil {
		return ""
	}
	return u.Host
}

// The per-connection fields follow the pin's normalizeProxyConfig
// (providers/[id]/route.js:9-34,135-155): any of the three sets all three,
// only a JSON true enables, enabling needs a URL, and a body without them is
// the pool assignment it always was. Routeweft adds the pool member checks:
// a proxy scheme and an address the outbound policy allows.
func TestSetConnectionProxyLegacyFields(t *testing.T) {
	type want struct {
		status  int
		code    string
		url     string // compiled connection proxy URL, "" when none
		noProxy string
		pool    string
		stored  string // sealed connectionProxyEnabled|connectionProxyUrl|connectionNoProxy
	}
	const seeded = "true|http://seed.example:3128|"
	cases := []struct {
		name string
		body string
		want want
	}{
		{"enable", `{"connectionProxyEnabled":true,"connectionProxyUrl":" http://proxy.example:3128 ","connectionNoProxy":" localhost "}`, want{status: 200, url: "http://proxy.example:3128", noProxy: "localhost", pool: "pool-1", stored: "true|http://proxy.example:3128|localhost"}},
		{"host:port is an http proxy", `{"connectionProxyEnabled":true,"connectionProxyUrl":"proxy.example:3128"}`, want{status: 200, url: "http://proxy.example:3128", pool: "pool-1", stored: "true|proxy.example:3128|"}},
		{"disable clears all three", `{"connectionProxyEnabled":false}`, want{status: 200, pool: "pool-1", stored: "false||"}},
		{"a string true does not enable", `{"connectionProxyEnabled":"true","connectionProxyUrl":"http://proxy.example:3128"}`, want{status: 200, pool: "pool-1", stored: "false|http://proxy.example:3128|"}},
		{"url alone does not enable", `{"connectionProxyUrl":"http://proxy.example:3128"}`, want{status: 200, pool: "pool-1", stored: "false|http://proxy.example:3128|"}},
		{"enable needs a url", `{"connectionProxyEnabled":true}`, want{status: 400, code: "invalid_connection_proxy", url: "http://seed.example:3128", pool: "pool-1", stored: seeded}},
		{"enable needs a string url", `{"connectionProxyEnabled":true,"connectionProxyUrl":3128}`, want{status: 400, code: "invalid_connection_proxy", url: "http://seed.example:3128", pool: "pool-1", stored: seeded}},
		{"enable needs a proxy scheme", `{"connectionProxyEnabled":true,"connectionProxyUrl":"ftp://proxy.example:21"}`, want{status: 400, code: "invalid_connection_proxy", url: "http://seed.example:3128", pool: "pool-1", stored: seeded}},
		{"enable needs an allowed address", `{"connectionProxyEnabled":true,"connectionProxyUrl":"http://169.254.169.254:80"}`, want{status: 400, code: "proxy_host_blocked", url: "http://seed.example:3128", pool: "pool-1", stored: seeded}},
		{"pool and connection fields together", `{"proxyPoolId":"","connectionProxyEnabled":true,"connectionProxyUrl":"http://proxy.example:3128"}`, want{status: 200, url: "http://proxy.example:3128", stored: "true|http://proxy.example:3128|"}},
		{"pool alone keeps the connection proxy", `{"proxyPoolId":""}`, want{status: 200, url: "http://seed.example:3128", stored: seeded}},
		{"empty body still unbinds the pool", `{}`, want{status: 200, url: "http://seed.example:3128", stored: seeded}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			env, manager := connectionProxyEnv(t)
			id, _ := codexBulkOp("seed@example.invalid", "acct-seed", "seed")(t, env)
			pool := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/proxy-pools", `{"name":"edge","members":[{"url":"http://203.0.113.9:3128"}]}`)
			var created struct {
				ID string `json:"id"`
			}
			if pool.Code != http.StatusCreated || json.Unmarshal(pool.Body.Bytes(), &created) != nil {
				t.Fatalf("pool status=%d body=%s", pool.Code, pool.Body.String())
			}
			poolID := func(value string) string {
				if value == "pool-1" {
					return created.ID
				}
				return value
			}
			seed := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/connections/"+id+"/proxy", `{"proxyPoolId":"`+created.ID+`","connectionProxyEnabled":true,"connectionProxyUrl":"http://seed.example:3128"}`)
			if seed.Code != http.StatusOK {
				t.Fatalf("seed status=%d body=%s", seed.Code, seed.Body.String())
			}
			recorder := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/connections/"+id+"/proxy", tc.body)
			if recorder.Code != tc.want.status || tc.want.code != "" && !strings.Contains(recorder.Body.String(), `"`+tc.want.code+`"`) {
				t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
			}
			snapshot := loadSnapshot(t, manager)
			legacy, _ := snapshot.LegacyConnectionProxy(id)
			if legacy.URL != tc.want.url || strings.Join(legacy.NoProxy, ",") != tc.want.noProxy || snapshot.ConnectionPool(id) != poolID(tc.want.pool) {
				t.Fatalf("compiled connection proxy=%+v pool=%q, want %+v", legacy, snapshot.ConnectionPool(id), tc.want)
			}
			// The sealed copy agrees, so a later refresh cannot bring an old value
			// back (the refresh carries stored provider data, registry.go).
			stored, err := env.creds.GetConnection(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			data := stored.Secret.ProviderData
			if got := data["connectionProxyEnabled"] + "|" + data["connectionProxyUrl"] + "|" + data["connectionNoProxy"]; got != tc.want.stored {
				t.Fatalf("stored %q, want %q", got, tc.want.stored)
			}
		})
	}
}

// Every side path tags its outbound work with the connection, so the
// connection-routed client sends it through the connection's proxy, as the
// pin passes proxyOptions on each (usage route.js:159-185, testUtils.js:865,
// models/route.js:463, codex-reset-credits/route.js:66).
func TestSidePathsCarryTheConnection(t *testing.T) {
	for _, tc := range []struct {
		name, method, path, body string
	}{
		{"codex reset credits", http.MethodGet, "/codex-reset-credits", ""},
		{"connection check", http.MethodPost, "/test", `{"modelId":"gpt-5.5"}`},
		{"model test", http.MethodPost, "/test-models", `{"modelIds":["gpt-5.5"]}`},
		{"model discovery", http.MethodPost, "/discover", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, _ := connectionProxyEnv(t)
			id, _ := codexBulkOp("side@example.invalid", "acct-side", "side")(t, env)
			seen := &connectionRecorder{}
			env.h.opts.OAuthClient = seen
			env.h.opts.DiscoveryClient = &discovery.Client{HTTP: seen}
			path := "/admin/v1/connections/" + id + tc.path
			if tc.path == "/codex-reset-credits" {
				path = "/admin/v1/quota/" + id + tc.path
			}
			doJSON(t, env.mux, env.cookie, tc.method, path, tc.body)
			got := seen.connections()
			if len(got) == 0 {
				t.Fatal("no outbound request was made")
			}
			for _, connection := range got {
				if connection != id {
					t.Fatalf("outbound work went out for %q, want %q (all: %v)", connection, id, got)
				}
			}
		})
	}
}

type connectionRecorder struct{ seen []string }

func (r *connectionRecorder) Do(request *http.Request) (*http.Response, error) {
	r.seen = append(r.seen, transport.ConnectionFrom(request.Context()))
	return &http.Response{StatusCode: http.StatusOK, Header: http.Header{"Content-Type": {"application/json"}}, Body: io.NopCloser(strings.NewReader(`{}`)), Request: request}, nil
}

func (r *connectionRecorder) connections() []string { return append([]string(nil), r.seen...) }

// publications records every snapshot a proxy write publishes, through the
// store's publisher and the pool-binding refresh alike.
type publications struct {
	manager *runtime.Manager
	mu      sync.Mutex
	seen    []*runtime.RuntimeSnapshot
}

func (p *publications) record(before uint64) {
	snapshot, err := p.manager.Load()
	if err != nil || snapshot.ConfigRevision() == before {
		return
	}
	p.mu.Lock()
	p.seen = append(p.seen, snapshot)
	p.mu.Unlock()
}

func (p *publications) RefreshPoolBindings(ctx context.Context) (*runtime.RuntimeSnapshot, error) {
	before := p.revision()
	snapshot, err := p.manager.RefreshPoolBindings(ctx)
	if err == nil {
		p.record(before)
	}
	return snapshot, err
}

func (p *publications) revision() uint64 {
	snapshot, _ := p.manager.Load()
	return snapshot.ConfigRevision()
}

func (p *publications) reset() []*runtime.RuntimeSnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	seen := p.seen
	p.seen = nil
	return seen
}

// recordPublications installs the recorder on the store and the API.
func recordPublications(env *codexEnv, manager *runtime.Manager) *publications {
	recorder := &publications{manager: manager}
	env.creds.SetPublisher(func(ctx context.Context, change func(*credentials.Configuration) error, persist func(*sql.Tx) error) error {
		before := recorder.revision()
		err := manager.UpdateProviders(ctx, change, persist)
		if err == nil {
			recorder.record(before)
		}
		return err
	})
	env.h.opts.PoolBindings = recorder
	return recorder
}

// proxyFixture is one connection bound to the pool named by want, plus a
// pool to switch to.
func proxyFixture(t *testing.T) (env *codexEnv, manager *runtime.Manager, id, poolA, poolB string) {
	t.Helper()
	env, manager = connectionProxyEnv(t)
	id, _ = codexBulkOp("once@example.invalid", "acct-once", "once")(t, env)
	pools := make([]string, 2)
	for i := range pools {
		recorder := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/proxy-pools", `{"name":"edge-`+strconv.Itoa(i)+`","members":[{"url":"http://203.0.113.`+strconv.Itoa(10+i)+`:3128"}]}`)
		var created struct {
			ID string `json:"id"`
		}
		if recorder.Code != http.StatusCreated || json.Unmarshal(recorder.Body.Bytes(), &created) != nil {
			t.Fatalf("pool status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		pools[i] = created.ID
	}
	seed := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/connections/"+id+"/proxy", `{"proxyPoolId":"`+pools[0]+`","connectionProxyEnabled":true,"connectionProxyUrl":"http://seed.example:3128"}`)
	if seed.Code != http.StatusOK {
		t.Fatalf("seed status=%d body=%s", seed.Code, seed.Body.String())
	}
	return env, manager, id, pools[0], pools[1]
}

// One request is one published change: no snapshot shows the new binding
// with the old connection proxy or the reverse (providers/[id]/route.js:158
// is one write).
func TestSetConnectionProxyPublishesOnce(t *testing.T) {
	for _, tc := range []struct {
		name, body  string
		pool, proxy string // "" pool means unbound; "B" stands for the second pool
	}{
		{"pool and connection proxy", `{"proxyPoolId":"B","connectionProxyEnabled":true,"connectionProxyUrl":"http://next.example:3128"}`, "B", "http://next.example:3128"},
		{"unbind and disable", `{"proxyPoolId":"","connectionProxyEnabled":false}`, "", ""},
		{"connection proxy alone", `{"connectionProxyEnabled":true,"connectionProxyUrl":"http://next.example:3128"}`, "A", "http://next.example:3128"},
		{"pool alone", `{"proxyPoolId":"B"}`, "B", "http://seed.example:3128"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			env, manager, id, poolA, poolB := proxyFixture(t)
			names := map[string]string{"A": poolA, "B": poolB, "": ""}
			recorder := recordPublications(env, manager)
			body := strings.ReplaceAll(tc.body, `"proxyPoolId":"B"`, `"proxyPoolId":"`+poolB+`"`)
			response := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/connections/"+id+"/proxy", body)
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			seen := recorder.reset()
			if len(seen) != 1 {
				t.Fatalf("published %d snapshots, want 1", len(seen))
			}
			legacy, _ := seen[0].LegacyConnectionProxy(id)
			if seen[0].ConnectionPool(id) != names[tc.pool] || legacy.URL != tc.proxy {
				t.Fatalf("published pool=%q proxy=%q, want %q %q", seen[0].ConnectionPool(id), legacy.URL, names[tc.pool], tc.proxy)
			}
		})
	}
}

// A write that fails changes nothing and publishes nothing: an unknown pool
// fails the transaction's foreign key, after the connection proxy validated.
func TestSetConnectionProxyFailedWriteChangesNothing(t *testing.T) {
	for _, body := range []string{
		`{"proxyPoolId":"no-such-pool","connectionProxyEnabled":true,"connectionProxyUrl":"http://next.example:3128"}`,
		`{"proxyPoolId":"no-such-pool","connectionProxyEnabled":false}`,
		`{"proxyPoolId":"no-such-pool"}`,
	} {
		t.Run(body, func(t *testing.T) {
			env, manager, id, poolA, _ := proxyFixture(t)
			recorder := recordPublications(env, manager)
			response := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/connections/"+id+"/proxy", body)
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), `"proxy_assignment_failed"`) {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			if seen := recorder.reset(); len(seen) != 0 {
				t.Fatalf("a failed write published %d snapshots", len(seen))
			}
			snapshot := loadSnapshot(t, manager)
			legacy, _ := snapshot.LegacyConnectionProxy(id)
			stored, err := env.creds.GetConnection(context.Background(), id)
			if err != nil {
				t.Fatal(err)
			}
			if snapshot.ConnectionPool(id) != poolA || legacy.URL != "http://seed.example:3128" || stored.ProxyPoolID != poolA || stored.Secret.ProviderData["connectionProxyUrl"] != "http://seed.example:3128" || stored.Secret.ProviderData["connectionProxyEnabled"] != "true" {
				t.Fatalf("after a failed write: pool=%q proxy=%q stored pool=%q data=%v", snapshot.ConnectionPool(id), legacy.URL, stored.ProxyPoolID, stored.Secret.ProviderData)
			}
		})
	}
}

// Concurrent writes of two whole configurations never interleave: every
// published snapshot, and the stored row at the end, pairs pool A with proxy
// a and pool B with proxy b.
func TestSetConnectionProxyConcurrentWritesStayWhole(t *testing.T) {
	env, manager, id, poolA, poolB := proxyFixture(t)
	recorder := recordPublications(env, manager)
	bodies := map[string]string{
		poolA: `{"proxyPoolId":"` + poolA + `","connectionProxyEnabled":true,"connectionProxyUrl":"http://a.example:3128"}`,
		poolB: `{"proxyPoolId":"` + poolB + `","connectionProxyEnabled":true,"connectionProxyUrl":"http://b.example:3128"}`,
	}
	pairs := map[string]string{poolA: "http://a.example:3128", poolB: "http://b.example:3128"}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		pool := poolA
		if i%2 == 1 {
			pool = poolB
		}
		wg.Add(1)
		go func(body string) {
			defer wg.Done()
			r := httptest.NewRequest(http.MethodPost, "/admin/v1/connections/"+id+"/proxy", strings.NewReader(body))
			r.AddCookie(env.cookie)
			w := httptest.NewRecorder()
			env.mux.ServeHTTP(w, r)
			if w.Code != http.StatusOK {
				t.Errorf("status=%d body=%s", w.Code, w.Body.String())
			}
		}(bodies[pool])
	}
	wg.Wait()
	seen := recorder.reset()
	if len(seen) == 0 {
		t.Fatal("nothing was published")
	}
	for _, snapshot := range append(seen, loadSnapshot(t, manager)) {
		legacy, _ := snapshot.LegacyConnectionProxy(id)
		if pairs[snapshot.ConnectionPool(id)] != legacy.URL {
			t.Fatalf("snapshot %d pairs pool %q with proxy %q", snapshot.ConfigRevision(), snapshot.ConnectionPool(id), legacy.URL)
		}
	}
	stored, err := env.creds.GetConnection(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if pairs[stored.ProxyPoolID] != stored.Secret.ProviderData["connectionProxyUrl"] {
		t.Fatalf("stored pool %q with proxy %q", stored.ProxyPoolID, stored.Secret.ProviderData["connectionProxyUrl"])
	}
}

// A store failure on the write that carries the connection proxy leaves the
// binding as it was too: the two are not applied one after the other.
func TestSetConnectionProxyStoreFailureIsAllOrNothing(t *testing.T) {
	env, manager, id, poolA, poolB := proxyFixture(t)
	recorder := recordPublications(env, manager)
	env.creds.SetPublisher(func(ctx context.Context, change func(*credentials.Configuration) error, persist func(*sql.Tx) error) error {
		return manager.UpdateProviders(ctx, func(config *credentials.Configuration) error {
			if err := change(config); err != nil {
				return err
			}
			for _, connection := range config.Connections {
				if connection.ID == id && connection.Settings.ConnectionProxyURL != "http://seed.example:3128" {
					return errors.New("injected store failure")
				}
			}
			return nil
		}, persist)
	})
	response := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/connections/"+id+"/proxy", `{"proxyPoolId":"`+poolB+`","connectionProxyEnabled":true,"connectionProxyUrl":"http://next.example:3128"}`)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if seen := recorder.reset(); len(seen) != 0 {
		t.Fatalf("a failed write published %d snapshots", len(seen))
	}
	snapshot := loadSnapshot(t, manager)
	legacy, _ := snapshot.LegacyConnectionProxy(id)
	stored, err := env.creds.GetConnection(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.ConnectionPool(id) != poolA || legacy.URL != "http://seed.example:3128" || stored.ProxyPoolID != poolA || stored.Secret.ProviderData["connectionProxyUrl"] != "http://seed.example:3128" {
		t.Fatalf("partial write: pool=%q proxy=%q stored pool=%q url=%q", snapshot.ConnectionPool(id), legacy.URL, stored.ProxyPoolID, stored.Secret.ProviderData["connectionProxyUrl"])
	}
}

// Only a JSON true imported from LiteRouter turns the connection proxy on;
// a string "true" or any other value leaves it off, on both imports
// (connectionProxy.js:40-41).
func TestImportedConnectionProxyFlagKeepsItsType(t *testing.T) {
	items := map[string]func(flag string) string{
		"codex": func(flag string) string {
			return `{"accessToken":"` + unsignedJWT(codexClaims("flag@example.invalid", "acct-flag", "plus")) + `","providerSpecificData":{"connectionProxyEnabled":` + flag + `,"connectionProxyUrl":"http://proxy.example:3128"}}`
		},
		"grok-cli": func(flag string) string {
			return `{"accessToken":"grok-access","email":"flag@example.invalid","providerSpecificData":{"connectionProxyEnabled":` + flag + `,"connectionProxyUrl":"http://proxy.example:3128"}}`
		},
	}
	for provider, item := range items {
		for _, tc := range []struct {
			flag    string
			enabled bool
		}{
			{`true`, true},
			{`"true"`, false},
			{`" true"`, false},
			{`false`, false},
			{`"false"`, false},
			{`1`, false},
		} {
			t.Run(provider+"/"+tc.flag, func(t *testing.T) {
				env, manager := connectionProxyEnv(t)
				id, status := bulkOp(provider, item(tc.flag))(t, env)
				if status != http.StatusOK || id == "" {
					t.Fatalf("import status=%d id=%q", status, id)
				}
				if _, enabled := loadSnapshot(t, manager).LegacyConnectionProxy(id); enabled != tc.enabled {
					t.Fatalf("connection proxy enabled=%v, want %v", enabled, tc.enabled)
				}
				listed := doJSON(t, env.mux, env.cookie, http.MethodGet, "/admin/v1/connections", "")
				if strings.Contains(listed.Body.String(), `"connectionProxy"`) != tc.enabled {
					t.Fatalf("list shows a connection proxy=%v, want %v: %s", !tc.enabled, tc.enabled, listed.Body.String())
				}
			})
		}
	}
}
