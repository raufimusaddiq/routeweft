package api

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

const proxySecret = "hunter2-proxy-secret"

// closedPort returns a loopback address nothing listens on.
func closedPort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	_ = listener.Close()
	return address
}

func decodeBody(t *testing.T, recorder *httptest.ResponseRecorder, target any) {
	t.Helper()
	if err := json.Unmarshal(recorder.Body.Bytes(), target); err != nil {
		t.Fatalf("decode %q: %v", recorder.Body.String(), err)
	}
}

func TestProxyPoolRoutesRequireSession(t *testing.T) {
	_, mux, store, _, _ := newProvidersAPI(t, false, nil)
	defer store.Close()
	for _, target := range []struct{ method, path string }{
		{http.MethodGet, "/admin/v1/proxy-pools/p1"},
		{http.MethodPost, "/admin/v1/proxy-pools"},
		{http.MethodPut, "/admin/v1/proxy-pools/p1"},
		{http.MethodDelete, "/admin/v1/proxy-pools/p1"},
		{http.MethodPost, "/admin/v1/proxy-pools/p1/test"},
		{http.MethodPost, "/admin/v1/settings/proxy-test"},
	} {
		if recorder := doJSON(t, mux, nil, target.method, target.path, `{}`); recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s status=%d want 401", target.method, target.path, recorder.Code)
		}
	}
}

func TestProxyPoolCRUDNeverEchoesCredentials(t *testing.T) {
	ctx := context.Background()
	handler, mux, store, _, cookie := newProvidersAPI(t, true, nil)
	defer store.Close()
	credentialed := "http://operator:" + proxySecret + "@proxy.example.net:3128?token=" + proxySecret

	created := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/proxy-pools", `{"name":"edge","strategy":"fill_first","members":[{"url":"`+credentialed+`"},{"url":"socks5://203.0.113.9:1080","enabled":false}]}`)
	if created.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", created.Code, created.Body.String())
	}
	var pool struct {
		ID       string `json:"id"`
		Strategy string `json:"strategy"`
		Revision uint64 `json:"configRevision"`
		Members  []struct {
			ID      string `json:"id"`
			URL     string `json:"url"`
			Enabled bool   `json:"enabled"`
		} `json:"members"`
	}
	decodeBody(t, created, &pool)
	if pool.ID == "" || pool.Strategy != "fill_first" || pool.Revision == 0 || len(pool.Members) != 2 || pool.Members[1].Enabled {
		t.Fatalf("unexpected pool %+v", pool)
	}
	if pool.Members[0].URL != "http://proxy.example.net:3128" {
		t.Fatalf("member url=%q want credentials and query stripped", pool.Members[0].URL)
	}

	// Update keeps the stored credentialed URL when the member is sent by id only.
	updated := doJSON(t, mux, cookie, http.MethodPut, "/admin/v1/proxy-pools/"+pool.ID, `{"name":"edge-2","members":[{"id":"`+pool.Members[0].ID+`"}]}`)
	if updated.Code != http.StatusOK {
		t.Fatalf("update status=%d body=%s", updated.Code, updated.Body.String())
	}
	stored, err := handler.proxyStore.GetPool(ctx, pool.ID)
	if err != nil || stored.Name != "edge-2" || len(stored.Members) != 1 || !strings.Contains(stored.Members[0].URL, proxySecret) {
		t.Fatalf("stored pool lost its credentials: %+v err=%v", stored, err)
	}
	snapshot, err := handler.opts.Runtime.Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, members, ok := snapshot.ProxyPool(pool.ID); !ok || len(members) != 1 {
		t.Fatalf("snapshot pool not published: ok=%v members=%v", ok, members)
	}

	responses := []*httptest.ResponseRecorder{
		created, updated,
		doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/proxy-pools/"+pool.ID, ""),
		doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/proxy-pools", ""),
		doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/proxy-pools/"+pool.ID+"/test", ""),
		doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/settings/proxy-test", `{"url":"`+credentialed+`"}`),
	}
	for _, recorder := range responses {
		if recorder.Code != http.StatusOK && recorder.Code != http.StatusCreated {
			t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
		}
		if body := recorder.Body.String(); strings.Contains(body, proxySecret) || strings.Contains(body, "operator") {
			t.Fatalf("response leaked proxy credentials: %s", body)
		}
	}

	if missing := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/proxy-pools/nope", ""); missing.Code != http.StatusNotFound {
		t.Fatalf("missing status=%d", missing.Code)
	}
	if duplicate := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/proxy-pools", `{"name":"edge-2"}`); duplicate.Code != http.StatusConflict {
		t.Fatalf("duplicate name status=%d want 409", duplicate.Code)
	}
	for _, bad := range []string{`{"name":""}`, `{"name":"x","strategy":"random"}`, `{"name":"x","members":[{"url":"ftp://203.0.113.1"}]}`, `{"name":"x","members":[{}]}`} {
		if recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/proxy-pools", bad); recorder.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d want 400", bad, recorder.Code)
		}
	}

	deleted := doJSON(t, mux, cookie, http.MethodDelete, "/admin/v1/proxy-pools/"+pool.ID, "")
	if deleted.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", deleted.Code, deleted.Body.String())
	}
	if again := doJSON(t, mux, cookie, http.MethodDelete, "/admin/v1/proxy-pools/"+pool.ID, ""); again.Code != http.StatusNotFound {
		t.Fatalf("second delete status=%d want 404", again.Code)
	}
	snapshot, _ = handler.opts.Runtime.Load()
	if _, _, ok := snapshot.ProxyPool(pool.ID); ok {
		t.Fatal("deleted pool is still in the active snapshot")
	}
}

func TestDeleteBoundProxyPoolIsRefused(t *testing.T) {
	_, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	created := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/proxy-pools", `{"name":"bound","members":[{"url":"http://203.0.113.9:3128"}]}`)
	var pool struct {
		ID string `json:"id"`
	}
	decodeBody(t, created, &pool)
	connection := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/connections", `{"providerId":"openai","name":"primary","identity":"acct","secret":{"accessToken":"sk-live"},"proxyPoolId":"`+pool.ID+`"}`)
	if connection.Code >= 300 {
		t.Fatalf("connection status=%d body=%s", connection.Code, connection.Body.String())
	}
	if recorder := doJSON(t, mux, cookie, http.MethodDelete, "/admin/v1/proxy-pools/"+pool.ID, ""); recorder.Code != http.StatusConflict || !strings.Contains(recorder.Body.String(), "proxy_pool_in_use") {
		t.Fatalf("bound delete status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	if recorder := doJSON(t, mux, cookie, http.MethodGet, "/admin/v1/proxy-pools/"+pool.ID, ""); recorder.Code != http.StatusOK {
		t.Fatalf("pool must survive refused delete, status=%d", recorder.Code)
	}
}

func TestProxyConnectivityUnreachableIsClassifiedNot500(t *testing.T) {
	_, mux, store, _, cookie := newProvidersAPI(t, true, nil)
	defer store.Close()
	dead := "http://" + closedPort(t)

	single := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/settings/proxy-test", `{"url":"`+dead+`"}`)
	var result struct {
		OK      bool   `json:"ok"`
		Class   string `json:"class"`
		Message string `json:"message"`
	}
	decodeBody(t, single, &result)
	if single.Code != http.StatusOK || result.OK || result.Class != "refused" || result.Message == "" {
		t.Fatalf("single status=%d body=%s", single.Code, single.Body.String())
	}

	created := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/proxy-pools", `{"name":"dead","members":[{"url":"`+dead+`"}]}`)
	var pool struct {
		ID string `json:"id"`
	}
	decodeBody(t, created, &pool)
	tested := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/proxy-pools/"+pool.ID+"/test", "")
	var poolResult struct {
		OK      bool `json:"ok"`
		Members []struct {
			OK    bool   `json:"ok"`
			Class string `json:"class"`
		} `json:"members"`
	}
	decodeBody(t, tested, &poolResult)
	if tested.Code != http.StatusOK || poolResult.OK || len(poolResult.Members) != 1 || poolResult.Members[0].Class != "refused" {
		t.Fatalf("pool status=%d body=%s", tested.Code, tested.Body.String())
	}
	if missing := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/proxy-pools/"+pool.ID+"/test", `{"memberId":"ghost"}`); missing.Code != http.StatusNotFound {
		t.Fatalf("unknown member status=%d want 404", missing.Code)
	}
	if none := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/settings/proxy-test", ""); none.Code != http.StatusBadRequest {
		t.Fatalf("no global proxy status=%d want 400", none.Code)
	}
}

func TestProxyConnectivitySucceedsThroughProxyWithCredentials(t *testing.T) {
	handler, mux, store, _, cookie := newProvidersAPI(t, true, nil)
	defer store.Close()
	var hits atomic.Int32
	var sawAuth atomic.Bool
	proxyServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Header.Get("Proxy-Authorization") != "" {
			sawAuth.Store(true)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer proxyServer.Close()
	// TEST-NET-3: public per the SSRF policy but never routable.
	handler.proxyTestTarget = "http://203.0.113.10/generate_204"
	address := strings.TrimPrefix(proxyServer.URL, "http://")

	recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/settings/proxy-test", `{"url":"http://operator:`+proxySecret+`@`+address+`"}`)
	var result struct {
		OK         bool `json:"ok"`
		StatusCode int  `json:"statusCode"`
	}
	decodeBody(t, recorder, &result)
	if recorder.Code != http.StatusOK || !result.OK || result.StatusCode != http.StatusNoContent || hits.Load() != 1 || !sawAuth.Load() {
		t.Fatalf("status=%d body=%s hits=%d auth=%v", recorder.Code, recorder.Body.String(), hits.Load(), sawAuth.Load())
	}
	if strings.Contains(recorder.Body.String(), proxySecret) {
		t.Fatal("success response leaked credentials")
	}
}

func TestProxyConnectivityRejectsPrivateAddressesBeforeDialing(t *testing.T) {
	_, mux, store, _, cookie := newProvidersAPI(t, false, nil)
	defer store.Close()
	var hits atomic.Int32
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1) }))
	defer local.Close()

	for _, private := range []string{local.URL, "http://10.0.0.5:3128", "http://169.254.169.254:80", "socks5://192.168.1.1:1080"} {
		recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/settings/proxy-test", `{"url":"`+private+`"}`)
		var result struct {
			OK    bool   `json:"ok"`
			Class string `json:"class"`
		}
		decodeBody(t, recorder, &result)
		if recorder.Code != http.StatusOK || result.OK || result.Class != "blocked" {
			t.Fatalf("%s: status=%d body=%s", private, recorder.Code, recorder.Body.String())
		}
		create := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/proxy-pools", `{"name":"p","members":[{"url":"`+private+`"}]}`)
		if create.Code != http.StatusBadRequest || !strings.Contains(create.Body.String(), "proxy_host_blocked") {
			t.Fatalf("%s: create status=%d body=%s", private, create.Code, create.Body.String())
		}
	}
	if hits.Load() != 0 {
		t.Fatalf("private proxy was dialed %d times", hits.Load())
	}
}
