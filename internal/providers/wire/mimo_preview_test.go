package wire

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/providers/mimoaccount"
	"github.com/raufimusaddiq/routeweft/internal/routing"
)

type mimoFixture struct {
	*httptest.Server
	mu          sync.Mutex
	routeCalls  int
	cookies     []string
	bodies      []string
	authorized  []string
	ssoCalls    int
	routeStatus int // status of the first route answer (200 default)
}

func newMimoFixture(t *testing.T) *mimoFixture {
	t.Helper()
	f := &mimoFixture{}
	f.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		base := "https://" + r.Host
		switch r.URL.Path {
		case "/api/route/chat/completions":
			raw, _ := io.ReadAll(r.Body)
			f.mu.Lock()
			f.routeCalls++
			f.cookies = append(f.cookies, r.Header.Get("Cookie"))
			f.bodies = append(f.bodies, string(raw))
			f.authorized = append(f.authorized, r.Header.Get("Authorization"))
			status := f.routeStatus
			if status == 0 && f.routeCalls == 1 {
				status = http.StatusUnauthorized
			}
			f.mu.Unlock()
			if status == http.StatusUnauthorized {
				w.WriteHeader(status)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"ok":true}`))
		case "/api/user/xiaomi/me":
			callback := base + "/api/sts"
			w.Header().Set("Location", base+"/pass/serviceLogin?sid=mimopc&callback="+url.QueryEscape(callback))
			w.WriteHeader(http.StatusFound)
		case "/pass/serviceLogin":
			w.Header().Set("Content-Type", "application/json")
			switch r.URL.Query().Get("sid") {
			case "passportapi":
				if r.URL.Query().Get("clientSign") == "" {
					w.Write([]byte(`&&&START&&&{"location":"` + base + `/pass/serviceLogin?sid=passportapi&nonce=n1","nonce":"n1","ssecurity":"sec"}`))
					return
				}
				w.Header().Add("Set-Cookie", "userId=u1; Path=/")
				w.Write([]byte(`{}`))
			case "mimopc":
				w.Write([]byte(`&&&START&&&{"location":"` + base + `/api/sts?ticket=t1"}`))
			}
		case "/api/sts":
			w.Header().Add("Set-Cookie", "serviceToken=svc; Path=/")
			w.Header().Add("Set-Cookie", "mimopc_ph=ph; Path=/")
			w.Header().Add("Set-Cookie", "mimopc_slh=slh; Path=/")
			w.Header().Add("Set-Cookie", "userId=u1; Path=/")
			w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(f.Close)
	oldBase, oldHost := mimoaccount.Base, mimoaccount.AccountHost
	mimoaccount.Base, mimoaccount.AccountHost = f.URL, strings.TrimPrefix(f.URL, "https://")
	mimoSessions.Invalidate()
	t.Cleanup(func() {
		mimoaccount.Base, mimoaccount.AccountHost = oldBase, oldHost
		mimoSessions.Invalidate()
	})
	return f
}

func (f *mimoFixture) snaps() (int, []string, []string, []string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.routeCalls, append([]string(nil), f.cookies...), append([]string(nil), f.bodies...), append([]string(nil), f.authorized...)
}

func mimoPreviewRequest(t *testing.T, endpoint string, body string) *http.Request {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer sk-fixture")
	return request.WithContext(WithAccountData(request.Context(), map[string]string{
		"mimoPassToken": "fixture-pass", "mimoUserId": "u1", "mimoCUserId": "c1",
	}))
}

// executors/xiaomi-mimo.js:39-47,51-60,76-101: the Preview model goes to the
// account route with the cookie identity and the pin's defaults, and one 401
// is retried with a fresh session.
func TestMimoPreviewDoSendsTheAccountRouteAndRetriesOnce(t *testing.T) {
	f := newMimoFixture(t)
	request := mimoPreviewRequest(t, f.URL+"/api/route/chat/completions",
		`{"model":"mimo-x-pro-preview","messages":[{"role":"user","content":"hi"}]}`)
	response, err := MimoDo(routing.ProviderRef{ProviderID: "xiaomi-mimo", UpstreamModel: "xiaomi-mimo/mimo-x-pro-preview"}, f.Client(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status %d", response.StatusCode)
	}
	calls, cookies, bodies, authorized := f.snaps()
	if calls != 2 {
		t.Fatalf("route calls = %d, want 2 (one 401 then one retry)", calls)
	}
	for i, cookie := range cookies {
		if !strings.Contains(cookie, "serviceToken=svc") {
			t.Fatalf("call %d cookie %q", i+1, cookie)
		}
	}
	if authorized[0] != "" || authorized[1] != "" {
		t.Fatalf("Preview sent the sk- bearer: %v", authorized)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(bodies[1]), &body); err != nil {
		t.Fatal(err)
	}
	if body["thinking"] == nil || body["temperature"] != 1.0 || body["top_p"] != 0.95 || body["max_tokens"] != 4096.0 {
		t.Fatalf("defaults not applied: %s", bodies[1])
	}
}

func TestMimoPreviewASecond401FailsWithoutAThirdCall(t *testing.T) {
	f := newMimoFixture(t)
	f.routeStatus = http.StatusUnauthorized
	request := mimoPreviewRequest(t, f.URL+"/api/route/chat/completions",
		`{"model":"mimo-x-flash-preview","messages":[{"role":"user","content":"hi"}]}`)
	response, err := MimoDo(routing.ProviderRef{ProviderID: "xiaomi-mimo", UpstreamModel: "mimo-x-flash-preview"}, f.Client(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status %d", response.StatusCode)
	}
	if calls, _, _, _ := f.snaps(); calls != 2 {
		t.Fatalf("route calls = %d, want exactly two", calls)
	}
}

func TestMimoPreviewWithoutAPassTokenSendsNothing(t *testing.T) {
	f := newMimoFixture(t)
	request, _ := http.NewRequest(http.MethodPost, f.URL+"/api/route/chat/completions", strings.NewReader(`{"model":"mimo-x-pro-preview"}`))
	request = request.WithContext(WithAccountData(request.Context(), map[string]string{}))
	response, err := MimoDo(routing.ProviderRef{ProviderID: "xiaomi-mimo", UpstreamModel: "mimo-x-pro-preview"}, f.Client(), request)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	body, _ := io.ReadAll(response.Body)
	if response.StatusCode != http.StatusBadGateway || !strings.Contains(string(body), "account session unavailable") {
		t.Fatalf("%d %s", response.StatusCode, body)
	}
	if calls, _, _, _ := f.snaps(); calls != 0 {
		t.Fatalf("route calls = %d", calls)
	}
}

func TestMimoDoLeavesCloudModelsAlone(t *testing.T) {
	var seen string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = r.Header.Get("Authorization")
		w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	request, _ := http.NewRequest(http.MethodPost, server.URL+"/v1/chat/completions", strings.NewReader(`{"model":"mimo-v2.5"}`))
	request.Header.Set("Authorization", "Bearer sk-fixture")
	response, err := MimoDo(routing.ProviderRef{ProviderID: "xiaomi-mimo", UpstreamModel: "mimo-v2.5"}, server.Client(), request)
	if err != nil {
		t.Fatal(err)
	}
	response.Body.Close()
	if seen != "Bearer sk-fixture" {
		t.Fatalf("cloud bearer = %q", seen)
	}
}

func TestMimoPreviewHookDeclaresItsNeeds(t *testing.T) {
	hook := For("xiaomi-mimo")
	if hook.Do == nil || hook.Target == nil || hook.NeedsAccountData == nil || !NeedsAccountData(routing.ProviderRef{ProviderID: "xiaomi-mimo", UpstreamModel: "mimo-x-pro-preview"}) || NeedsAccountData(routing.ProviderRef{ProviderID: "xiaomi-mimo", UpstreamModel: "mimo-v2.5"}) {
		t.Fatalf("hook %+v", hook)
	}
}

func TestMimoTargetOnlyMovesPreviewModels(t *testing.T) {
	if target, err := mimoTarget(routing.ProviderRef{UpstreamModel: "xiaomi-mimo/mimo-x-pro-preview"}); err != nil || target.Endpoint != mimoPreviewPath || target.BaseURL != mimoaccount.Base {
		t.Fatalf("preview target %+v %v", target, err)
	}
	if target, err := mimoTarget(routing.ProviderRef{UpstreamModel: "mimo-v2.5"}); err != nil || target.Endpoint != "" || target.BaseURL != "" {
		t.Fatalf("cloud target %+v %v", target, err)
	}
}
