package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

const netlifyToken = "nfp_synthetic_token_value"

// fakeNetlify answers the digest-deploy sequence like the real API and records what
// it was sent.
type fakeNetlify struct {
	mu       sync.Mutex
	calls    []string
	tokens   map[string]bool
	siteName string
	taken    bool
	state    string
}

func newFakeNetlify(t *testing.T) (*fakeNetlify, *httptest.Server) {
	t.Helper()
	fake := &fakeNetlify{tokens: map[string]bool{}, state: "ready"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		fake.mu.Lock()
		fake.calls = append(fake.calls, r.Method+" "+r.URL.RequestURI())
		fake.tokens[r.Header.Get("Authorization")] = true
		fake.mu.Unlock()
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/sites":
			var request struct{ Name string }
			_ = json.Unmarshal(body, &request)
			fake.siteName = request.Name
			if fake.taken {
				w.WriteHeader(http.StatusUnprocessableEntity)
				fmt.Fprint(w, `{"message":"Subdomain not available"}`)
				return
			}
			w.WriteHeader(http.StatusCreated)
			fmt.Fprintf(w, `{"id":"site-1","ssl_url":"https://%s.netlify.app"}`, request.Name)
		case r.Method == http.MethodPost && r.URL.Path == "/sites/site-1/deploys":
			var request struct {
				Files     map[string]string
				Functions map[string]string
			}
			_ = json.Unmarshal(body, &request)
			fmt.Fprintf(w, `{"id":"deploy-1","required":[%q],"required_functions":[%q]}`, request.Files["/index.html"], request.Functions["relay"])
		case r.Method == http.MethodPut:
			w.WriteHeader(http.StatusOK)
			fmt.Fprint(w, `{}`)
		case r.Method == http.MethodGet && r.URL.Path == "/deploys/deploy-1":
			fmt.Fprintf(w, `{"state":%q}`, fake.state)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return fake, server
}

func TestNetlifyDeployCreatesARelayPoolAndNeverKeepsTheToken(t *testing.T) {
	handler, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	fake, server := newFakeNetlify(t)
	handler.opts.NetlifyAPI, handler.opts.NetlifyHTTP = server.URL, server.Client()

	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/proxy-pools/netlify-deploy", `{"netlifyToken":"  `+netlifyToken+` ","projectName":"My Edge_Relay"}`)
	if recorder.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body)
	}
	var answer struct {
		DeployURL string `json:"deployUrl"`
		Pool      struct {
			ID, Name, Type string
			StrictProxy    bool
			Enabled        bool
			Members        []struct{ URL string }
		} `json:"proxyPool"`
	}
	decodeBody(t, recorder, &answer)
	wantURL := "https://my-edge-relay.netlify.app/.netlify/functions/relay"
	if answer.DeployURL != wantURL || answer.Pool.Name != "my-edge-relay" || answer.Pool.Type != "netlify" || !answer.Pool.Enabled || answer.Pool.StrictProxy || len(answer.Pool.Members) != 1 || answer.Pool.Members[0].URL != wantURL {
		t.Fatalf("answer=%+v", answer)
	}
	if len(fake.calls) != 5 || fake.calls[0] != "POST /sites" || fake.calls[4] != "GET /deploys/deploy-1" {
		t.Fatalf("calls=%v", fake.calls)
	}
	if len(fake.tokens) != 1 || !fake.tokens["Bearer "+netlifyToken] {
		t.Fatalf("Netlify saw %v: the trimmed token, as a bearer credential, on every call", fake.tokens)
	}
	// The token is in no answer and no stored row.
	for _, path := range []string{"/admin/v1/proxy-pools", "/admin/v1/proxy-pools/" + answer.Pool.ID} {
		if body := doJSON(t, mux, cookie, http.MethodGet, path, "").Body.String(); strings.Contains(body, netlifyToken) {
			t.Fatalf("%s leaks the token", path)
		}
	}
	if strings.Contains(recorder.Body.String(), netlifyToken) {
		t.Fatal("the deploy answer leaks the token")
	}
	var stored int
	if err := store.DB().QueryRow(`SELECT (SELECT COUNT(*) FROM proxy_pools WHERE name LIKE ?) + (SELECT COUNT(*) FROM proxy_pool_members WHERE proxy_url LIKE ?)`, "%"+netlifyToken+"%", "%"+netlifyToken+"%").Scan(&stored); err != nil || stored != 0 {
		t.Fatalf("token stored in pool rows: %d %v", stored, err)
	}
}

func TestNetlifyDeployRefusals(t *testing.T) {
	handler, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	fake, server := newFakeNetlify(t)
	handler.opts.NetlifyAPI, handler.opts.NetlifyHTTP = server.URL, server.Client()

	if recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/proxy-pools/netlify-deploy", `{"projectName":"x"}`); recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "Netlify API token is required") {
		t.Fatalf("no token: %d %s", recorder.Code, recorder.Body)
	}
	if len(fake.calls) != 0 {
		t.Fatalf("Netlify was called without a token: %v", fake.calls)
	}
	if recorder := doJSON(t, mux, nil, http.MethodPost, "/admin/v1/proxy-pools/netlify-deploy", `{"netlifyToken":"`+netlifyToken+`"}`); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("a deploy needs an operator session, got %d", recorder.Code)
	}
	fake.taken = true
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/proxy-pools/netlify-deploy", `{"netlifyToken":"`+netlifyToken+`","projectName":"taken"}`)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), `Site name \"taken\" is taken`) {
		t.Fatalf("taken: %d %s", recorder.Code, recorder.Body)
	}
	fake.taken, fake.state = false, "error"
	if recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/proxy-pools/netlify-deploy", `{"netlifyToken":"`+netlifyToken+`","projectName":"broken"}`); recorder.Code != http.StatusInternalServerError {
		t.Fatalf("failed deploy: %d %s", recorder.Code, recorder.Body)
	}
	var pools int
	if err := store.DB().QueryRow(`SELECT COUNT(*) FROM proxy_pools`).Scan(&pools); err != nil || pools != 0 {
		t.Fatalf("a failed deploy left %d pools", pools)
	}
	// A pool of that name already exists: the relay is live, so the answer says where.
	fake.state = "ready"
	doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/proxy-pools", `{"name":"dup","members":[{"url":"http://203.0.113.9:3128"}]}`)
	recorder = doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/proxy-pools/netlify-deploy", `{"netlifyToken":"`+netlifyToken+`","projectName":"dup"}`)
	if recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "https://dup.netlify.app/.netlify/functions/relay") {
		t.Fatalf("name conflict: %d %s", recorder.Code, recorder.Body)
	}
}

func TestPoolTypeStrictNoProxyAreReadLooselyLikeTheOracle(t *testing.T) {
	_, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	type pool struct {
		ID          string `json:"id"`
		Type        string `json:"type"`
		StrictProxy bool   `json:"strictProxy"`
		NoProxy     string `json:"noProxy"`
	}
	create := func(name, extra string) pool {
		t.Helper()
		recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/proxy-pools", `{"name":"`+name+`","members":[{"url":"https://relay.example.net/fn"}]`+extra+`}`)
		if recorder.Code != http.StatusCreated {
			t.Fatalf("%s: %d %s", name, recorder.Code, recorder.Body)
		}
		var out pool
		decodeBody(t, recorder, &out)
		return out
	}
	for kind, want := range map[string]string{"vercel": "vercel", "cloudflare": "cloudflare", "deno": "deno", "netlify": "netlify", "http": "http", "socks": "http", "NETLIFY": "http", "7": "http"} {
		if got := create("kind-"+kind, `,"type":`+quoteIfWord(kind)).Type; got != want {
			t.Errorf("type %s stored as %s, want %s", kind, got, want)
		}
	}
	if got := create("default", ""); got.Type != "http" || got.StrictProxy || got.NoProxy != "" {
		t.Errorf("defaults: %+v", got)
	}
	if got := create("strict", `,"strictProxy":true,"noProxy":" a.com,b.com "`); !got.StrictProxy || got.NoProxy != "a.com,b.com" {
		t.Errorf("strict: %+v", got)
	}
	if got := create("stringly", `,"strictProxy":"true","noProxy":5`); got.StrictProxy || got.NoProxy != "" {
		t.Errorf("only the boolean true makes a pool strict, and a non-string bypass list is empty: %+v", got)
	}
	// An update changes only what it names.
	strict := create("update-me", `,"type":"vercel","strictProxy":true,"noProxy":"x.com"`)
	updated := doJSON(t, mux, cookie, http.MethodPut, "/admin/v1/proxy-pools/"+strict.ID, `{"name":"update-me","noProxy":"y.com"}`)
	var after pool
	decodeBody(t, updated, &after)
	if updated.Code != http.StatusOK || after.Type != "vercel" || !after.StrictProxy || after.NoProxy != "y.com" {
		t.Errorf("update: %d %+v", updated.Code, after)
	}
	// A relay is an http(s) function, not a proxy.
	bad := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/proxy-pools", `{"name":"bad-relay","type":"netlify","members":[{"url":"socks5://203.0.113.9:1080"}]}`)
	if bad.Code != http.StatusBadRequest {
		t.Errorf("a relay behind socks5 was accepted: %d %s", bad.Code, bad.Body)
	}
}

func quoteIfWord(text string) string {
	if text == "7" {
		return text
	}
	return `"` + text + `"`
}

func TestPoolListFiltersByActiveAndCountsBoundConnections(t *testing.T) {
	_, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	var ids = map[string]string{}
	for name, enabled := range map[string]bool{"on": true, "off": false} {
		recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/proxy-pools", fmt.Sprintf(`{"name":%q,"enabled":%v,"members":[{"url":"http://203.0.113.9:3128"}]}`, name, enabled))
		var pool struct{ ID string }
		decodeBody(t, recorder, &pool)
		ids[name] = pool.ID
	}
	for i := 0; i < 2; i++ {
		if r := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections", fmt.Sprintf(`{"providerId":"openai","name":"c%d","identity":"a%d","secret":{"accessToken":"sk-%d"},"proxyPoolId":%q}`, i, i, i, ids["on"])); r.Code >= 300 {
			t.Fatalf("connection: %d %s", r.Code, r.Body)
		}
	}
	list := func(query string) (names []string, counts map[string]*int, total int64) {
		recorder := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/proxy-pools"+query, "")
		var page struct {
			Items []struct {
				Name                 string
				BoundConnectionCount *int
			}
			Total int64
		}
		decodeBody(t, recorder, &page)
		counts = map[string]*int{}
		for _, item := range page.Items {
			names = append(names, item.Name)
			counts[item.Name] = item.BoundConnectionCount
		}
		return names, counts, page.Total
	}
	for query, want := range map[string]string{"": "off,on", "?isActive=true": "on", "?isActive=false": "off", "?isActive=yes": "off,on", "?isActive=TRUE": "off,on"} {
		if names, _, _ := list(query); strings.Join(names, ",") != want {
			t.Errorf("%q listed %v, want %s", query, names, want)
		}
	}
	if _, counts, _ := list(""); counts["on"] != nil || counts["off"] != nil {
		t.Errorf("usage must be absent unless asked for: %v", counts)
	}
	if _, counts, _ := list("?includeUsage=1"); counts["on"] != nil {
		t.Error("only includeUsage=true asks for usage")
	}
	_, counts, total := list("?includeUsage=true")
	if counts["on"] == nil || *counts["on"] != 2 || counts["off"] == nil || *counts["off"] != 0 || total != 2 {
		t.Errorf("usage: on=%v off=%v total=%d", counts["on"], counts["off"], total)
	}
	if names, counts, total := list("?isActive=true&includeUsage=true"); strings.Join(names, ",") != "on" || *counts["on"] != 2 || total != 1 {
		t.Errorf("filter and usage together: %v %v %d", names, counts["on"], total)
	}
}

func TestDeleteIsRefusedWhileAStrategyNamesThePool(t *testing.T) {
	_, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/proxy-pools", `{"name":"rotating","members":[{"url":"http://203.0.113.9:3128"}]}`)
	var pool struct{ ID string }
	decodeBody(t, recorder, &pool)
	strategies := fmt.Sprintf(`{"set":{"providerStrategies":%q}}`, `{"opencode":{"proxyPoolId":"`+pool.ID+`"}}`)
	if r := doJSON(t, mux, cookie, http.MethodPatch, "/admin/v1/settings", strategies); r.Code != http.StatusOK {
		t.Fatalf("settings: %d %s", r.Code, r.Body)
	}
	refused := doJSON(t, mux, cookie, http.MethodDelete, "/admin/v1/proxy-pools/"+pool.ID, "")
	if refused.Code != http.StatusConflict || !strings.Contains(refused.Body.String(), "boundConnectionCount") {
		t.Fatalf("delete: %d %s", refused.Code, refused.Body)
	}
	if r := doJSON(t, mux, cookie, http.MethodPatch, "/admin/v1/settings", `{"remove":["providerStrategies"]}`); r.Code != http.StatusOK {
		t.Fatalf("settings: %d %s", r.Code, r.Body)
	}
	if r := doJSON(t, mux, cookie, http.MethodDelete, "/admin/v1/proxy-pools/"+pool.ID, ""); r.Code != http.StatusOK {
		t.Fatalf("delete after unbinding: %d %s", r.Code, r.Body)
	}
}

func TestTestingARelayPoolRecordsTheResultAndSwitchesThePool(t *testing.T) {
	handler, mux, store, _, cookie := newProvidersAPI(t, true, nil)
	defer store.Close()
	var healthy = true
	var target string
	relay := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		target = r.Header.Get("X-Relay-Target") + r.Header.Get("X-Relay-Path")
		if !healthy {
			http.Error(w, "down", http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer relay.Close()
	handler.proxyTestTarget = "https://probe.example.net/generate_204"
	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/proxy-pools", `{"name":"fn","type":"vercel","members":[{"url":"`+relay.URL+`/api/relay"}]}`)
	var pool struct{ ID string }
	decodeBody(t, recorder, &pool)

	run := func() (result struct {
		OK         bool
		Enabled    bool
		TestStatus string
	}) {
		answer := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/proxy-pools/"+pool.ID+"/test", "")
		if answer.Code != http.StatusOK {
			t.Fatalf("test: %d %s", answer.Code, answer.Body)
		}
		decodeBody(t, answer, &result)
		return result
	}
	if got := run(); !got.OK || !got.Enabled || got.TestStatus != "active" || target != "https://probe.example.net/generate_204" {
		t.Fatalf("passing test: %+v target=%q", got, target)
	}
	healthy = false
	if got := run(); got.OK || got.Enabled || got.TestStatus != "error" {
		t.Fatalf("failing test: %+v", got)
	}
	var read struct {
		Enabled      bool
		TestStatus   string
		LastTestedAt string
		LastError    string
	}
	decodeBody(t, doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/proxy-pools/"+pool.ID, ""), &read)
	if read.Enabled || read.TestStatus != "error" || read.LastTestedAt == "" || read.LastError == "" || strings.Contains(read.LastError, relay.URL) {
		t.Fatalf("recorded: %+v", read)
	}
	healthy = true
	if got := run(); !got.Enabled || got.TestStatus != "active" {
		t.Fatalf("a pool that passes again is switched back on: %+v", got)
	}
}
