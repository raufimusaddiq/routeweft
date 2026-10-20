package app

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/registry"
	"github.com/raufimusaddiq/routeweft/internal/runtime"
)

func servingApp(t testing.TB, upstream string, count int) (*App, string) {
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
	node, err := a.creds.PutNode(ctx, credentials.Node{ID: "node", Kind: credentials.NodeGeneric, ProviderID: "test", Name: "test", Prefix: "test", BaseURL: upstream, Transports: []string{"openai-chat", "openai-responses", "anthropic-messages"}})
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < count; i++ {
		_, err := a.creds.PutConnection(ctx, credentials.Connection{ID: fmt.Sprintf("account-%02d", i), NodeID: node.ID, Name: fmt.Sprintf("Account %02d", i), Identity: fmt.Sprintf("identity-%d", i), AuthKind: credentials.AuthAPIKey, Secret: credentials.Secret{AccessToken: fmt.Sprintf("synthetic-%d", i)}, Enabled: true, Priority: i})
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := a.runtime.PutModel(ctx, runtime.Model{ProviderID: "test", ID: "vendor/model"}); err != nil {
		t.Fatal(err)
	}
	if err := a.runtime.PutAlias(ctx, "fast", runtime.ModelRef{ProviderID: "test", ModelID: "vendor/model"}); err != nil {
		t.Fatal(err)
	}
	// Rebuild the real app from durable configuration; no injected resolver or
	// test-only credential seeding can make this request pass.
	if err := a.store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := a.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	return a, key
}

func serveRequest(handler http.Handler, key, path, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+key)
	r.Header.Set("Cookie", "client=synthetic")
	r.Header.Set("X-Secret", "synthetic-client-secret")
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, r)
	return w
}

func TestConfiguredServingNativeEndpointsWithoutDatabase(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"model":"vendor/model"`) || !strings.Contains(string(body), `"future":{"keep":true}`) {
			t.Errorf("outbound body %s", body)
		}
		if r.Header.Get("Authorization") != "Bearer synthetic-0" && r.Header.Get("X-Api-Key") != "synthetic-0" {
			t.Error("provider credential not selected")
		}
		if r.Header.Get("Cookie") != "" || r.Header.Get("X-Secret") != "" {
			t.Error("client secret forwarded")
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprintf(w, `{"path":%q}`, r.URL.Path)
	}))
	defer upstream.Close()
	a, key := servingApp(t, upstream.URL+"/native", 1)
	// Also proves authentication, account selection and credentials are DB-free.
	if err := a.store.Close(); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, body, want string }{
		{"/v1/chat/completions", `{"model":"fast","messages":[{"role":"user","content":"hi"}],"future":{"keep":true}}`, "/native/chat/completions"},
		{"/v1/responses", `{"model":"test/vendor/model","input":"hi","future":{"keep":true}}`, "/native/responses"},
		{"/v1/responses/compact", `{"model":"test/vendor/model","input":"hi","future":{"keep":true}}`, "/native/responses/compact"},
		{"/v1/messages", `{"model":"fast","messages":[{"role":"user","content":"hi"}],"max_tokens":1,"future":{"keep":true}}`, "/native/messages"},
	} {
		w := serveRequest(a.Handler(), key, tc.path, tc.body)
		if w.Code != 200 || !strings.Contains(w.Body.String(), tc.want) {
			t.Fatalf("%s status=%d body=%s", tc.path, w.Code, w.Body)
		}
	}
	if calls.Load() != 4 {
		t.Fatalf("calls %d", calls.Load())
	}
	w := serveRequest(a.Handler(), "wrong", "/v1/chat/completions", `{"model":"fast","messages":[]}`)
	if w.Code != 401 || calls.Load() != 4 {
		t.Fatalf("invalid key status=%d calls=%d", w.Code, calls.Load())
	}
}

func TestConfiguredServingControlMutations(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.URL.Path+" "+r.Header.Get("Authorization"))
	}))
	defer upstream.Close()
	a := New(Config{DataDir: t.TempDir(), CredentialKey: []byte("01234567890123456789012345678901"), AllowPrivateUpstreams: true, AdminBootstrapUsername: "operator", AdminBootstrapPassword: "synthetic-password"}, nil)
	if err := a.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer a.store.Close()
	_, key, err := a.runtime.CreateAPIKey(context.Background(), "client")
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	a.Handler().ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/admin/v1/auth/login", strings.NewReader(`{"username":"operator","password":"synthetic-password"}`)))
	if login.Code != 200 || len(login.Result().Cookies()) == 0 {
		t.Fatalf("login %d %s", login.Code, login.Body)
	}
	admin := func(method, path, body string) string {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(login.Result().Cookies()[0])
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
		}
		var result struct {
			ID string `json:"id"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		return result.ID
	}
	node := admin(http.MethodPost, "/admin/v1/provider-nodes", fmt.Sprintf(`{"kind":"generic","name":"test","prefix":"test","baseUrl":%q,"transports":["openai-chat"]}`, upstream.URL+"/first"))
	connection := admin(http.MethodPost, "/admin/v1/connections", fmt.Sprintf(`{"nodeId":%q,"name":"primary","identity":"synthetic","secret":{"accessToken":"synthetic-first"}}`, node))
	admin(http.MethodPost, "/admin/v1/models", `{"providerId":"test","id":"model"}`)
	admin(http.MethodPost, "/admin/v1/aliases", `{"alias":"fast","providerId":"test","modelId":"model"}`)
	assertResponse := func(status int, want string) {
		t.Helper()
		w := serveRequest(a.Handler(), key, "/v1/chat/completions", `{"model":"fast","messages":[{"role":"user","content":"hi"}]}`)
		if w.Code != status || !strings.Contains(w.Body.String(), want) {
			t.Fatalf("status=%d body=%s want=%d %q", w.Code, w.Body, status, want)
		}
	}
	assertResponse(200, "/first/chat/completions Bearer synthetic-first")
	admin(http.MethodPatch, "/admin/v1/connections/"+connection, `{"enabled":false}`)
	assertResponse(503, "no_eligible_accounts")
	admin(http.MethodPatch, "/admin/v1/connections/"+connection, `{"enabled":true,"secret":{"accessToken":"synthetic-second"}}`)
	admin(http.MethodPatch, "/admin/v1/provider-nodes/"+node, fmt.Sprintf(`{"baseUrl":%q}`, upstream.URL+"/second"))
	assertResponse(200, "/second/chat/completions Bearer synthetic-second")
	admin(http.MethodDelete, "/admin/v1/connections/"+connection, "")
	assertResponse(503, "no_eligible_accounts")
}

func TestConfiguredServingBuiltinEndpoints(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, r.URL.Path) }))
	defer upstream.Close()
	a, key := servingApp(t, upstream.URL, 1)
	ctx := context.Background()
	if _, err := a.creds.PutNode(ctx, credentials.Node{ID: "node", Kind: credentials.NodeBuiltin, ProviderID: "kenari", Name: "kenari", BaseURL: upstream.URL}); err != nil {
		t.Fatal(err)
	}
	if err := a.runtime.PutModel(ctx, runtime.Model{ProviderID: "kenari", ID: "model"}); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		w := serveRequest(a.Handler(), key, path, `{"model":"kenari/model","input":"hi","messages":[{"role":"user","content":"hi"}],"max_tokens":1}`)
		if w.Code != 200 || w.Body.String() != path {
			t.Fatalf("%s status=%d body=%s", path, w.Code, w.Body)
		}
	}
}

func TestConfiguredServingStrategiesAndEligibility(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, r.Header.Get("Authorization")) }))
	defer upstream.Close()
	for _, tc := range []struct {
		name, strategy, overrides string
		want                      []string
	}{
		{"fill", "fill-first", "{}", []string{"0", "0", "0", "0"}},
		{"round-robin", "round-robin", "{}", []string{"0", "1", "0", "1"}},
		{"sticky", "sticky-round-robin", "{}", []string{"0", "0", "1", "1"}},
		{"override", "fill-first", `{"test":"round-robin"}`, []string{"0", "1", "0", "1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, key := servingApp(t, upstream.URL, 2)
			if _, err := a.runtime.SetSettings(context.Background(), map[string]string{"providerStrategy": tc.strategy, "providerStrategies": tc.overrides, "stickyRoundRobinLimit": "2"}, nil); err != nil {
				t.Fatal(err)
			}
			for _, want := range tc.want {
				w := serveRequest(a.Handler(), key, "/v1/chat/completions", `{"model":"fast","messages":[{"role":"user","content":"hi"}]}`)
				if w.Code != 200 || w.Body.String() != "Bearer synthetic-"+want {
					t.Fatalf("status=%d body=%s want=%s", w.Code, w.Body, want)
				}
			}
			if err := a.creds.SetConnectionEnabled(context.Background(), "account-00", false); err != nil {
				t.Fatal(err)
			}
			w := serveRequest(a.Handler(), key, "/v1/chat/completions", `{"model":"fast","messages":[{"role":"user","content":"hi"}]}`)
			if w.Code != 200 || w.Body.String() != "Bearer synthetic-1" {
				t.Fatalf("disabled account selected: %d %s", w.Code, w.Body)
			}
			zero := 0.0
			a.runtime.State().ObserveQuota("account-01", runtime.QuotaObservation{Remaining: &zero, ResetAt: time.Now().Add(time.Hour)})
			w = serveRequest(a.Handler(), key, "/v1/chat/completions", `{"model":"fast","messages":[{"role":"user","content":"hi"}]}`)
			if w.Code != 503 {
				t.Fatalf("exhausted status=%d %s", w.Code, w.Body)
			}
			a.runtime.State().ObserveQuota("account-01", runtime.QuotaObservation{Err: "temporarily unknown"})
			w = serveRequest(a.Handler(), key, "/v1/chat/completions", `{"model":"fast","messages":[{"role":"user","content":"hi"}]}`)
			if w.Code != 200 {
				t.Fatalf("unknown quota treated exhausted: %d", w.Code)
			}
		})
	}
}

func TestConfiguredServingFallbackAndTerminalErrors(t *testing.T) {
	for _, status := range []int{401, 403, 429, 503, 400, 413} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var first, second atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") == "Bearer synthetic-0" {
					first.Add(1)
					w.Header().Set("Retry-After", "120")
					w.Header().Set("X-Failed-Only", "secret-free")
					w.WriteHeader(status)
					fmt.Fprint(w, `{"error":{"message":"synthetic failure"}}`)
					return
				}
				second.Add(1)
				fmt.Fprint(w, `{"ok":true}`)
			}))
			defer upstream.Close()
			a, key := servingApp(t, upstream.URL, 2)
			w := serveRequest(a.Handler(), key, "/v1/chat/completions", `{"model":"fast","messages":[{"role":"user","content":"hi"}]}`)
			if status == 400 || status == 413 {
				if w.Code != status || second.Load() != 0 || !strings.Contains(w.Body.String(), "synthetic failure") {
					t.Fatalf("terminal %d: %d %s calls=%d", status, w.Code, w.Body, second.Load())
				}
			} else {
				if w.Code != 200 || second.Load() != 1 || w.Header().Get("X-Failed-Only") != "" {
					t.Fatalf("fallback %d: %d %s calls=%d", status, w.Code, w.Body, second.Load())
				}
				until, ok := a.runtime.State().CooldownUntil("account-00", time.Now())
				if !ok || time.Until(until) < 115*time.Second {
					t.Fatal("Retry-After not honored")
				}
				serveRequest(a.Handler(), key, "/v1/chat/completions", `{"model":"fast","messages":[{"role":"user","content":"hi"}]}`)
				if first.Load() != 1 {
					t.Fatal("cooldown account retried")
				}
			}
		})
	}
}

func TestConfiguredServingSkipsTransportMismatchBeforeRotationAndCap(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" {
			token = r.Header.Get("X-Api-Key")
		}
		fmt.Fprint(w, token)
	}))
	defer upstream.Close()
	a, key := servingApp(t, upstream.URL, 12)
	ctx := context.Background()
	node, err := a.creds.GetNode(ctx, "node")
	if err != nil {
		t.Fatal(err)
	}
	// Gemini has no conversion leg yet (#119), so neither Responses nor Messages
	// can reach it; a Chat-only node would now be reachable from Messages.
	node.Transports = []string{"gemini"}
	if _, err := a.creds.PutNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	node.ID, node.Prefix, node.Transports = "native", "", []string{"openai-responses", "anthropic-messages"}
	if _, err := a.creds.PutNode(ctx, node); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"account-10", "account-11"} {
		connection, err := a.creds.GetConnection(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		connection.NodeID = node.ID
		if _, err := a.creds.PutConnection(ctx, connection); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := a.runtime.SetSettings(ctx, map[string]string{"providerStrategy": "round-robin"}, nil); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/v1/responses", "/v1/messages"} {
		for _, want := range []string{"10", "11", "10", "11"} {
			w := serveRequest(a.Handler(), key, path, `{"model":"fast","input":"hi","messages":[{"role":"user","content":"hi"}],"max_tokens":1}`)
			if w.Code != 200 || w.Body.String() != "synthetic-"+want {
				t.Fatalf("%s status=%d body=%s want=%s", path, w.Code, w.Body, want)
			}
		}
	}
	if err := a.creds.SetConnectionEnabled(ctx, "account-10", false); err != nil {
		t.Fatal(err)
	}
	if err := a.creds.SetConnectionEnabled(ctx, "account-11", false); err != nil {
		t.Fatal(err)
	}
	w := serveRequest(a.Handler(), key, "/v1/messages", `{"model":"fast","messages":[{"role":"user","content":"hi"}],"max_tokens":1}`)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "no_eligible_accounts") {
		t.Fatalf("unsupported transports status=%d body=%s", w.Code, w.Body)
	}
}

func TestConfiguredServingCorruptCredentialStartup(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, r.Header.Get("Authorization"))
	}))
	defer upstream.Close()
	a, key := servingApp(t, upstream.URL, 1)
	ctx := context.Background()
	if _, err := a.creds.PutNode(ctx, credentials.Node{ID: "broken", Kind: credentials.NodeGeneric, ProviderID: "broken", Name: "broken", BaseURL: upstream.URL, Transports: []string{"openai-responses"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.creds.PutConnection(ctx, credentials.Connection{ID: "broken-account", NodeID: "broken", Name: "broken account", Identity: "broken-identity", AuthKind: credentials.AuthAPIKey, Secret: credentials.Secret{AccessToken: "synthetic-broken-token"}, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if err := a.runtime.PutModel(ctx, runtime.Model{ProviderID: "broken", ID: "model"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.store.DB().ExecContext(ctx, "UPDATE provider_connections SET secret_blob=? WHERE id=?", "synthetic-corrupt-blob", "broken-account"); err != nil {
		t.Fatal(err)
	}
	if err := a.store.Close(); err != nil {
		t.Fatal(err)
	}
	var logs bytes.Buffer
	a.log = slog.New(slog.NewTextHandler(&logs, nil))
	if err := a.Initialize(ctx); err != nil {
		t.Fatalf("corrupt provider blocked startup: %v", err)
	}
	if !strings.Contains(logs.String(), "provider=broken") || !strings.Contains(logs.String(), "level=WARN") || strings.Contains(logs.String(), "synthetic-") {
		t.Fatalf("missing warning or exposed credential material: %s", &logs)
	}
	if err := a.store.Close(); err != nil {
		t.Fatal(err)
	}
	w := serveRequest(a.Handler(), key, "/v1/responses", `{"model":"broken/model","input":"hi"}`)
	if w.Code != 503 || !strings.Contains(w.Body.String(), "credentials_unavailable") || calls.Load() != 0 {
		t.Fatalf("corrupt credential status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body)
	}
	w = serveRequest(a.Handler(), key, "/v1/responses", `{"model":"fast","input":"hi"}`)
	if w.Code != 200 || w.Body.String() != "Bearer synthetic-0" || calls.Load() != 1 {
		t.Fatalf("healthy provider status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body)
	}
}

func TestConfiguredServingAuthCooldownOncePerRequest(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/responses/compact", "/v1/messages"} {
		t.Run(path, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(http.StatusUnauthorized)
				fmt.Fprint(w, `{"error":"request scope rejected"}`)
			}))
			defer upstream.Close()
			a, key := servingApp(t, upstream.URL, 3)
			for request := 0; request < 2; request++ {
				w := serveRequest(a.Handler(), key, path, `{"model":"fast","input":"hi","messages":[{"role":"user","content":"hi"}],"max_tokens":1}`)
				wantCalls := int32(3)
				if request == 1 {
					wantCalls = 5
				}
				if w.Code != 401 || !strings.Contains(w.Body.String(), "request scope rejected") || calls.Load() != wantCalls {
					t.Fatalf("request=%d status=%d calls=%d body=%s", request, w.Code, calls.Load(), w.Body)
				}
				for account := 0; account < 3; account++ {
					until, cooling := a.runtime.State().CooldownUntil(fmt.Sprintf("account-%02d", account), time.Now())
					if cooling != (account <= request) || (cooling && time.Until(until) < 110*time.Second) {
						t.Fatalf("request=%d account=%d cooling=%v until=%s", request, account, cooling, until)
					}
				}
			}
		})
	}
}

func TestConfiguredServingCredentialFailures(t *testing.T) {
	for _, mode := range []string{"missing-key", "missing-account", "all-missing"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				fmt.Fprint(w, r.Header.Get("Authorization"))
			}))
			defer upstream.Close()
			a, key := servingApp(t, upstream.URL, 2)
			if mode == "missing-key" {
				if err := a.store.Close(); err != nil {
					t.Fatal(err)
				}
				a = New(Config{DataDir: a.cfg.DataDir, AllowPrivateUpstreams: true}, nil)
				if err := a.Initialize(context.Background()); err != nil {
					t.Fatal(err)
				}
				defer a.store.Close()
			} else {
				a.credReg.Forget("account-00")
				if mode == "all-missing" {
					a.credReg.Forget("account-01")
				}
			}
			w := serveRequest(a.Handler(), key, "/v1/responses", `{"model":"fast","input":"hi"}`)
			if mode == "missing-account" {
				if w.Code != 200 || calls.Load() != 1 || w.Body.String() != "Bearer synthetic-1" {
					t.Fatalf("account fallback status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body)
				}
			} else if w.Code != 503 || calls.Load() != 0 || !strings.Contains(w.Body.String(), "credentials_unavailable") {
				t.Fatalf("credential status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body)
			}
			if _, cooling := a.runtime.State().CooldownUntil("account-00", time.Now()); !cooling {
				t.Fatal("credential failure did not cool account")
			}
			if _, cooling := a.runtime.State().CooldownUntil("account-01", time.Now()); cooling != (mode == "all-missing") {
				t.Fatal("configuration-wide failure did not stop the chain or account cooldown missing")
			}
		})
	}
}

func TestConfiguredServingSameAccountRetry(t *testing.T) {
	for _, status := range []int{408, 409} {
		for _, persistent := range []bool{false, true} {
			t.Run(fmt.Sprintf("%d/persistent=%v", status, persistent), func(t *testing.T) {
				var first, second atomic.Int32
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Header.Get("Authorization") == "Bearer synthetic-0" {
						if n := first.Add(1); n == 1 || persistent {
							w.WriteHeader(status)
							fmt.Fprint(w, `{"error":"retry"}`)
							return
						}
					} else {
						second.Add(1)
					}
					fmt.Fprint(w, r.Header.Get("Authorization"))
				}))
				defer upstream.Close()
				a, key := servingApp(t, upstream.URL, 2)
				start := time.Now()
				w := serveRequest(a.Handler(), key, "/v1/responses", `{"model":"fast","input":"hi"}`)
				want, wantSecond := "Bearer synthetic-0", int32(0)
				if persistent {
					want, wantSecond = "Bearer synthetic-1", 1
				}
				if w.Code != 200 || w.Body.String() != want || first.Load() != 2 || second.Load() != wantSecond || time.Since(start) < 900*time.Millisecond {
					t.Fatalf("retry status=%d first=%d second=%d elapsed=%s body=%s", w.Code, first.Load(), second.Load(), time.Since(start), w.Body)
				}
			})
		}
	}
}

func TestConfiguredServingRetryDelayCancellation(t *testing.T) {
	seen := make(chan struct{}, 1)
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Retry-After", "120")
		w.WriteHeader(408)
		fmt.Fprint(w, `{"error":"retry"}`)
		select {
		case seen <- struct{}{}:
		default:
		}
	}))
	defer upstream.Close()
	a, key := servingApp(t, upstream.URL, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	r := httptest.NewRequest(http.MethodPost, "/v1/responses", strings.NewReader(`{"model":"fast","input":"hi"}`)).WithContext(ctx)
	r.Header.Set("Authorization", "Bearer "+key)
	done := make(chan struct{})
	go func() {
		a.Handler().ServeHTTP(httptest.NewRecorder(), r)
		close(done)
	}()
	select {
	case <-seen:
	case <-time.After(3 * time.Second):
		t.Fatal("request did not reach upstream")
	}
	// Wait until dispatch has classified the response and entered its delay.
	deadline := time.Now().Add(3 * time.Second)
	for {
		if until, ok := a.runtime.State().CooldownUntil("account-00", time.Now()); ok {
			if time.Until(until) < 115*time.Second {
				t.Fatal("retry delay ignored Retry-After")
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("retry delay not reached")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("retry delay ignored cancellation")
	}
	if calls.Load() != 1 {
		t.Fatalf("cancelled retry made %d requests", calls.Load())
	}
}

func TestConfiguredServingRetryDelayCapped(t *testing.T) {
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "86400")
			w.WriteHeader(408)
			fmt.Fprint(w, `{"error":"retry"}`)
			return
		}
		fmt.Fprint(w, r.Header.Get("Authorization"))
	}))
	defer upstream.Close()
	a, key := servingApp(t, upstream.URL, 1)
	start := time.Now()
	w := serveRequest(a.Handler(), key, "/v1/responses", `{"model":"fast","input":"hi"}`)
	if w.Code != 200 || w.Body.String() != "Bearer synthetic-0" || calls.Load() != 2 || time.Since(start) > 15*time.Second {
		t.Fatalf("capped retry status=%d calls=%d elapsed=%s body=%s", w.Code, calls.Load(), time.Since(start), w.Body)
	}
	if _, cooling := a.runtime.State().CooldownUntil("account-00", time.Now()); !cooling {
		t.Fatal("capped retry did not preserve full Retry-After cooldown")
	}
}

func TestConfiguredServingSharesProviderRotationAcrossModelsAndAliases(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, r.Header.Get("Authorization")) }))
	defer upstream.Close()
	for _, tc := range []struct {
		strategy string
		want     []string
	}{
		{"round-robin", []string{"0", "1", "0", "1"}},
		{"sticky-round-robin", []string{"0", "0", "1", "1"}},
	} {
		t.Run(tc.strategy, func(t *testing.T) {
			a, key := servingApp(t, upstream.URL, 2)
			ctx := context.Background()
			if err := a.runtime.PutModel(ctx, runtime.Model{ProviderID: "test", ID: "other"}); err != nil {
				t.Fatal(err)
			}
			if err := a.runtime.PutAlias(ctx, "other-alias", runtime.ModelRef{ProviderID: "test", ModelID: "other"}); err != nil {
				t.Fatal(err)
			}
			if _, err := a.runtime.SetSettings(ctx, map[string]string{"providerStrategy": tc.strategy, "stickyRoundRobinLimit": "2"}, nil); err != nil {
				t.Fatal(err)
			}
			for i, model := range []string{"fast", "test/other", "test/vendor/model", "other-alias"} {
				w := serveRequest(a.Handler(), key, "/v1/chat/completions", fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hi"}]}`, model))
				if w.Code != 200 || w.Body.String() != "Bearer synthetic-"+tc.want[i] {
					t.Fatalf("model=%s status=%d body=%s want=%s", model, w.Code, w.Body, tc.want[i])
				}
			}
		})
	}
}

func TestConfiguredServingAttemptCap(t *testing.T) {
	for _, status := range []int{503, 408} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.WriteHeader(status)
				fmt.Fprint(w, `{"error":"last failure"}`)
			}))
			defer upstream.Close()
			a, key := servingApp(t, upstream.URL, 12)
			w := serveRequest(a.Handler(), key, "/v1/responses", `{"model":"fast","input":"hi"}`)
			if w.Code != status || calls.Load() != 10 || !strings.Contains(w.Body.String(), "last failure") {
				t.Fatalf("status=%d calls=%d body=%s", w.Code, calls.Load(), w.Body)
			}
		})
	}
}

func TestConfiguredServingNetworkFallback(t *testing.T) {
	failed := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	failed.Close()
	var calls atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, "second account")
	}))
	defer upstream.Close()
	a, key := servingApp(t, failed.URL, 2)
	ctx := context.Background()
	if _, err := a.creds.PutNode(ctx, credentials.Node{ID: "second", Kind: credentials.NodeGeneric, ProviderID: "test", Name: "second", BaseURL: upstream.URL, Transports: []string{"openai-chat"}}); err != nil {
		t.Fatal(err)
	}
	connection, err := a.creds.GetConnection(ctx, "account-01")
	if err != nil {
		t.Fatal(err)
	}
	connection.NodeID = "second"
	if _, err := a.creds.PutConnection(ctx, connection); err != nil {
		t.Fatal(err)
	}
	w := serveRequest(a.Handler(), key, "/v1/chat/completions", `{"model":"fast","messages":[{"role":"user","content":"hi"}]}`)
	if w.Code != 200 || w.Body.String() != "second account" || calls.Load() != 1 {
		t.Fatalf("status=%d body=%s calls=%d", w.Code, w.Body, calls.Load())
	}
	if _, cooling := a.runtime.State().CooldownUntil("account-00", time.Now()); !cooling {
		t.Fatal("network failure did not cool account")
	}
}

func TestConfiguredServingStreamsCompleteOnce(t *testing.T) {
	for _, tc := range []struct{ path, stream, terminal string }{
		{"/v1/chat/completions", "data: {\"choices\":[]}\n\ndata: [DONE]\n\n", "data: [DONE]"},
		{"/v1/responses", "event: response.completed\ndata: {\"type\":\"response.completed\"}\n\n", "event: response.completed"},
		{"/v1/messages", "event: message_stop\ndata: {\"type\":\"message_stop\"}\n\n", "event: message_stop"},
	} {
		t.Run(tc.path, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, tc.stream)
			}))
			defer upstream.Close()
			a, key := servingApp(t, upstream.URL, 1)
			w := serveRequest(a.Handler(), key, tc.path, `{"model":"fast","input":"hi","messages":[{"role":"user","content":"hi"}],"max_tokens":1,"stream":true}`)
			if w.Code != 200 || strings.Count(w.Body.String(), tc.terminal) != 1 {
				t.Fatalf("status=%d stream=%s", w.Code, w.Body)
			}
		})
	}
}

func TestConfiguredServingConcurrentRoundRobin(t *testing.T) {
	var counts [2]atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		index := 0
		if r.Header.Get("Authorization") == "Bearer synthetic-1" {
			index = 1
		}
		counts[index].Add(1)
		fmt.Fprint(w, `{}`)
	}))
	defer upstream.Close()
	a, key := servingApp(t, upstream.URL, 2)
	if _, err := a.runtime.SetSettings(context.Background(), map[string]string{"providerStrategy": "round-robin"}, nil); err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for i := 0; i < 40; i++ {
		group.Add(1)
		go func() {
			defer group.Done()
			w := serveRequest(a.Handler(), key, "/v1/responses", `{"model":"fast","input":"hi"}`)
			if w.Code != 200 {
				t.Errorf("status=%d body=%s", w.Code, w.Body)
			}
		}()
	}
	group.Wait()
	if counts[0].Load() != 20 || counts[1].Load() != 20 {
		t.Fatalf("RR counts %d %d", counts[0].Load(), counts[1].Load())
	}
}

func TestConfiguredServingStreamCancellation(t *testing.T) {
	for _, path := range []string{"/v1/chat/completions", "/v1/responses", "/v1/messages"} {
		t.Run(path, func(t *testing.T) {
			cancelled := make(chan struct{})
			var calls atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "text/event-stream")
				fmt.Fprint(w, "data: {\"delta\":\"first\"}\n\n")
				w.(http.Flusher).Flush()
				<-r.Context().Done()
				close(cancelled)
			}))
			defer upstream.Close()
			a, key := servingApp(t, upstream.URL, 2)
			server := httptest.NewServer(a.Handler())
			defer server.Close()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			body := `{"model":"fast","input":"hi","messages":[{"role":"user","content":"hi"}],"max_tokens":1,"stream":true}`
			r, _ := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+path, strings.NewReader(body))
			r.Header.Set("Authorization", "Bearer "+key)
			response, err := server.Client().Do(r)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			buffer := make([]byte, 1)
			if _, err := response.Body.Read(buffer); err != nil {
				t.Fatal(err)
			}
			cancel()
			select {
			case <-cancelled:
			case <-time.After(3 * time.Second):
				t.Fatal("upstream not cancelled")
			}
			if calls.Load() != 1 {
				t.Fatal("stream replayed after cancellation")
			}
		})
	}
}

func BenchmarkConfiguredNativeServing(b *testing.B) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		fmt.Fprint(w, `{"usage":{"prompt_tokens":2,"completion_tokens":1}}`)
	}))
	defer upstream.Close()
	a, key := servingApp(b, upstream.URL, 1)
	handler := a.Handler()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		w := serveRequest(handler, key, "/v1/chat/completions", `{"model":"fast","messages":[{"role":"user","content":"hi"}]}`)
		if w.Code != 200 {
			b.Fatal(w.Code)
		}
	}
}

// TestFreshInstallRoutesSeededModel proves the #116 done condition: a fresh
// install routes a model for a provider it has never discovered, using only the
// generated seeds from the reference registry.
func TestFreshInstallRoutesSeededModel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.URL.Path)
	}))
	defer upstream.Close()
	a := New(Config{DataDir: t.TempDir(), CredentialKey: []byte("01234567890123456789012345678901"), AllowPrivateUpstreams: true, AdminBootstrapUsername: "operator", AdminBootstrapPassword: "synthetic-password"}, nil)
	if err := a.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer a.store.Close()
	_, key, err := a.runtime.CreateAPIKey(context.Background(), "client")
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRecorder()
	a.Handler().ServeHTTP(login, httptest.NewRequest(http.MethodPost, "/admin/v1/auth/login", strings.NewReader(`{"username":"operator","password":"synthetic-password"}`)))
	if login.Code != 200 || len(login.Result().Cookies()) == 0 {
		t.Fatalf("login %d %s", login.Code, login.Body)
	}
	cookie := login.Result().Cookies()[0]
	admin := func(method, path, body string) {
		t.Helper()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.AddCookie(cookie)
		w := httptest.NewRecorder()
		a.Handler().ServeHTTP(w, r)
		if w.Code != 200 {
			t.Fatalf("%s %s: %d %s", method, path, w.Code, w.Body)
		}
	}
	// muse is chosen because it is the newest catalog entry: its models exist only
	// as generated seeds, never as operator-created rows.
	admin(http.MethodPost, "/admin/v1/provider-nodes", fmt.Sprintf(`{"kind":"builtin","providerId":"muse","name":"muse","baseUrl":%q}`, upstream.URL))
	admin(http.MethodPost, "/admin/v1/connections", `{"providerId":"muse","name":"acct","identity":"acct","authKind":"api-key","secret":{"accessToken":"synthetic"},"enabled":true}`)

	for _, model := range []string{"muse-spark-1.2-contributor", "muse-spark-1.3-contributor"} {
		w := serveRequest(a.Handler(), key, "/v1/responses", fmt.Sprintf(`{"model":%q,"input":"hi"}`, model))
		if w.Code != 200 || !strings.Contains(w.Body.String(), "responses") {
			t.Fatalf("%s status=%d body=%s", model, w.Code, w.Body)
		}
	}
	// Seeds must not turn the catalog into a passthrough.
	w := serveRequest(a.Handler(), key, "/v1/responses", `{"model":"not-a-seed","input":"hi"}`)
	if w.Code != 404 {
		t.Fatalf("unknown model status=%d body=%s", w.Code, w.Body)
	}
}

// TestEverySeededProviderServesItsFirstModel is the drift guard for the seed
// catalog: every built-in provider that ships seeds must be able to serve one
// without discovery. A provider added to the catalog without seeds fails here
// rather than at an operator's first request.
func TestEverySeededProviderServesItsFirstModel(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, r.URL.Path)
	}))
	defer upstream.Close()
	a := New(Config{DataDir: t.TempDir(), CredentialKey: []byte("01234567890123456789012345678901"), AllowPrivateUpstreams: true}, nil)
	if err := a.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer a.store.Close()
	ctx := context.Background()
	for _, spec := range registry.Builtins() {
		if len(spec.SeedModels) == 0 {
			continue
		}
		node, err := a.creds.PutNode(ctx, credentials.Node{Kind: credentials.NodeBuiltin, ProviderID: spec.ID, Name: spec.ID, BaseURL: upstream.URL})
		if err != nil {
			t.Fatalf("%s node: %v", spec.ID, err)
		}
		_, err = a.creds.PutConnection(ctx, credentials.Connection{
			NodeID: node.ID, Name: spec.ID, Identity: spec.ID + "-account",
			AuthKind: credentials.AuthAPIKey, Secret: credentials.Secret{AccessToken: "synthetic"}, Enabled: true,
		})
		if err != nil {
			t.Fatalf("%s connection: %v", spec.ID, err)
		}
	}
	snapshot, err := a.runtime.Load()
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range registry.Builtins() {
		if len(spec.SeedModels) == 0 {
			continue
		}
		seed := spec.SeedModels[0]
		if _, ok := snapshot.ServingModel(spec.ID + "/" + seed.ID); !ok {
			t.Errorf("%s seed %q is not served", spec.ID, seed.ID)
		}
	}
}
