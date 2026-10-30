package app

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/proxy"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

// fakeRelay is the relay function the reference deploys (netlifyRelay.js:29-99),
// standing in for Netlify: it forwards a request to x-relay-target + x-relay-path
// with those headers removed. The target is mapped to a local server because the
// names a relay is asked for are public ones.
type fakeRelay struct {
	mu      sync.Mutex
	targets []string
	paths   []string
}

func (f *fakeRelay) serve(forward map[string]string) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target, path := r.Header.Get("X-Relay-Target"), r.Header.Get("X-Relay-Path")
		f.mu.Lock()
		f.targets, f.paths = append(f.targets, target), append(f.paths, path)
		f.mu.Unlock()
		real, ok := forward[target]
		if !ok {
			http.Error(w, `{"error":"unknown relay target"}`, http.StatusBadGateway)
			return
		}
		outbound, _ := http.NewRequestWithContext(r.Context(), r.Method, real+path, r.Body)
		for name, values := range r.Header {
			switch strings.ToLower(name) {
			case "x-relay-target", "x-relay-path", "host", "accept-encoding":
				continue
			}
			outbound.Header[name] = values
		}
		response, err := http.DefaultTransport.RoundTrip(outbound)
		if err != nil {
			http.Error(w, fmt.Sprintf(`{"error":%q}`, err.Error()), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		for name, values := range response.Header {
			w.Header()[name] = values
		}
		w.WriteHeader(response.StatusCode)
		io.Copy(w, response.Body)
	}))
}

// provider is a recording upstream.
type provider struct {
	hits       atomic.Int32
	authHeader atomic.Value
	relayLeak  atomic.Bool
}

func (p *provider) serve() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p.hits.Add(1)
		p.authHeader.Store(r.Header.Get("Authorization"))
		if r.Header.Get("X-Relay-Target") != "" {
			p.relayLeak.Store(true)
		}
		io.Copy(io.Discard, r.Body)
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	}))
}

func newPoolStore(t *testing.T, a *App) *proxy.Store {
	t.Helper()
	store, err := proxy.NewStore(a.store.DB())
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func bindPool(t *testing.T, a *App, connection, pool string) {
	t.Helper()
	if _, err := a.store.DB().Exec(`UPDATE provider_connections SET proxy_pool_id=? WHERE id=?`, pool, connection); err != nil {
		t.Fatal(err)
	}
	if _, err := a.runtime.RefreshPoolBindings(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func chat(a *App, key string) *httptest.ResponseRecorder {
	return post(a, key, fmt.Sprintf(textRequest, "fast"))
}

// A connection bound to a Netlify pool reaches its provider through the relay: the
// request goes to the relay function, carrying the destination in x-relay-target
// and x-relay-path, with the credential the provider needs; the provider never
// sees a relay header.
func TestRelayPoolCarriesTheProviderRequestThroughTheRelay(t *testing.T) {
	upstream := &provider{}
	providerServer := upstream.serve()
	defer providerServer.Close()
	relay := &fakeRelay{}
	relayServer := relay.serve(map[string]string{"https://api.relay-test.example": providerServer.URL})
	defer relayServer.Close()

	a, key := servingApp(t, "https://api.relay-test.example/v1", 1)
	pools := newPoolStore(t, a)
	pool, err := pools.PutPool(context.Background(), proxy.Pool{Name: "edge", Enabled: true, Kind: proxy.KindNetlify, Members: []proxy.Member{{URL: relayServer.URL + "/.netlify/functions/relay", Enabled: true}}})
	if err != nil {
		t.Fatal(err)
	}
	bindPool(t, a, "account-00", pool.ID)

	response := chat(a, key)
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"content":"ok"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body)
	}
	if len(relay.targets) != 1 || relay.targets[0] != "https://api.relay-test.example" || !strings.HasSuffix(relay.paths[0], "/chat/completions") {
		t.Fatalf("the relay was asked for %v %v", relay.targets, relay.paths)
	}
	if upstream.hits.Load() != 1 || upstream.authHeader.Load() != "Bearer synthetic-0" {
		t.Fatalf("provider hits=%d auth=%v", upstream.hits.Load(), upstream.authHeader.Load())
	}
	if upstream.relayLeak.Load() {
		t.Fatal("the provider saw a relay header")
	}
}

// A strict pool that cannot supply a proxy keeps the connection off the direct
// address; a pool that is not strict lets it through, as the reference does.
func TestStrictPoolFailsClosedAndALoosePoolFallsBackToDirect(t *testing.T) {
	for _, tc := range []struct {
		name       string
		strict     bool
		wantStatus int
		wantHits   int32
	}{{"loose pool that is switched off", false, http.StatusOK, 1}, {"strict pool that is switched off", true, http.StatusBadGateway, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			upstream := &provider{}
			providerServer := upstream.serve()
			defer providerServer.Close()
			a, key := servingApp(t, providerServer.URL+"/v1", 1)
			pools := newPoolStore(t, a)
			pool, err := pools.PutPool(context.Background(), proxy.Pool{Name: "off", Enabled: false, Strict: tc.strict, Members: []proxy.Member{{URL: "http://203.0.113.9:3128", Enabled: true}}})
			if err != nil {
				t.Fatal(err)
			}
			bindPool(t, a, "account-00", pool.ID)
			response := chat(a, key)
			if upstream.hits.Load() != tc.wantHits || (tc.wantStatus == http.StatusOK) != (response.Code == http.StatusOK) {
				t.Fatalf("status=%d provider hits=%d body=%s", response.Code, upstream.hits.Load(), response.Body)
			}
		})
	}
}

// forwardProxy is a plain HTTP forward proxy that counts what passes through.
func forwardProxy(hits *atomic.Int32) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		outbound, _ := http.NewRequestWithContext(r.Context(), r.Method, r.URL.String(), r.Body)
		outbound.Header = r.Header.Clone()
		response, err := http.DefaultTransport.RoundTrip(outbound)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer response.Body.Close()
		for name, values := range response.Header {
			w.Header()[name] = values
		}
		w.WriteHeader(response.StatusCode)
		io.Copy(w, response.Body)
	}))
}

func noAuthApp(t *testing.T, base string) (*App, string) {
	t.Helper()
	a := New(Config{DataDir: t.TempDir(), CredentialKey: []byte("01234567890123456789012345678901"), AllowPrivateUpstreams: true}, nil)
	ctx := context.Background()
	if err := a.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.store.Close() })
	_, key, err := a.runtime.CreateAPIKey(ctx, "client")
	if err != nil {
		t.Fatal(err)
	}
	node, err := a.creds.PutNode(ctx, credentials.Node{ID: "node", Kind: credentials.NodeGeneric, ProviderID: "free", Name: "free", Prefix: "free", BaseURL: base, Transports: []string{"openai-chat"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := a.creds.PutConnection(ctx, credentials.Connection{ID: "public", NodeID: node.ID, Name: "Public", Identity: "public", AuthKind: credentials.AuthNone, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := a.runtime.PutModel(ctx, runtime.Model{ProviderID: "free", ID: "vendor/model"}); err != nil {
		t.Fatal(err)
	}
	if err := a.runtime.PutAlias(ctx, "fast", runtime.ModelRef{ProviderID: "free", ModelID: "vendor/model"}); err != nil {
		t.Fatal(err)
	}
	if err := a.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	return a, key
}

// A no-auth provider's providerStrategies entry chooses its proxy: one fixed pool,
// or a rotation over every active pool; with a rotation and no active pool the
// request goes direct (src/sse/services/auth.js:78-104).
func TestNoAuthProviderProxyChoiceAndRotation(t *testing.T) {
	upstream := &provider{}
	providerServer := upstream.serve()
	defer providerServer.Close()
	a, key := noAuthApp(t, providerServer.URL+"/v1")
	ctx := context.Background()
	pools := newPoolStore(t, a)
	var hits [3]atomic.Int32
	var ids [3]string
	for i := range hits {
		proxyServer := forwardProxy(&hits[i])
		defer proxyServer.Close()
		pool, err := pools.PutPool(ctx, proxy.Pool{Name: fmt.Sprintf("p%d", i), Enabled: i != 2, Members: []proxy.Member{{URL: proxyServer.URL, Enabled: true}}})
		if err != nil {
			t.Fatal(err)
		}
		ids[i] = pool.ID
	}
	set := func(strategy string) {
		t.Helper()
		if _, err := a.runtime.SetSettings(ctx, map[string]string{"providerStrategies": strategy}, nil); err != nil {
			t.Fatal(err)
		}
		if _, err := a.runtime.RefreshPoolBindings(ctx); err != nil {
			t.Fatal(err)
		}
	}
	send := func(n int) {
		t.Helper()
		for i := 0; i < n; i++ {
			if response := chat(a, key); response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body)
			}
		}
	}
	counts := func() [3]int32 { return [3]int32{hits[0].Load(), hits[1].Load(), hits[2].Load()} }

	// No entry: direct.
	send(1)
	if counts() != [3]int32{} || upstream.hits.Load() != 1 {
		t.Fatalf("direct: proxies=%v provider=%d", counts(), upstream.hits.Load())
	}
	// A fixed pool.
	set(fmt.Sprintf(`{"free":{"proxyPoolId":%q}}`, ids[1]))
	send(3)
	if counts() != [3]int32{0, 3, 0} {
		t.Fatalf("fixed pool: %v", counts())
	}
	// "none" is no rotation, the fixed pool stands.
	set(fmt.Sprintf(`{"free":{"proxyPoolId":%q,"rotateStrategy":"none"}}`, ids[0]))
	send(2)
	if counts() != [3]int32{2, 3, 0} {
		t.Fatalf("rotateStrategy none: %v", counts())
	}
	// Round-robin over the active pools alone (the third is switched off), taking
	// turns, and a rotation overrides the fixed pool.
	set(fmt.Sprintf(`{"free":{"proxyPoolId":%q,"rotateStrategy":"round-robin"}}`, ids[0]))
	before := counts()
	send(4)
	got := counts()
	if got[0]-before[0] != 2 || got[1]-before[1] != 2 || got[2] != 0 {
		t.Fatalf("round-robin: before=%v after=%v", before, got)
	}
	// Random stays inside the active pools.
	set(`{"free":{"rotateStrategy":"random"}}`)
	before = counts()
	send(6)
	got = counts()
	if (got[0]-before[0])+(got[1]-before[1]) != 6 || got[2] != 0 {
		t.Fatalf("random: before=%v after=%v", before, got)
	}
	// A rotation with no active pool goes direct.
	for _, id := range ids[:2] {
		if _, err := a.store.DB().Exec(`UPDATE proxy_pools SET enabled=0 WHERE id=?`, id); err != nil {
			t.Fatal(err)
		}
	}
	set(`{"free":{"rotateStrategy":"round-robin"}}`)
	before, direct := counts(), upstream.hits.Load()
	send(2)
	if counts() != before || upstream.hits.Load() != direct+2 {
		t.Fatalf("no active pool: proxies=%v provider=%d", counts(), upstream.hits.Load())
	}
}
