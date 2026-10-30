package app

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/store/storetest"
)

// #78 C03: every assertion here backs a statement in docs/SPEC.md 12.2 and the
// PARITY auth rows. The negative cases (a guarded route must refuse, a closed
// bypass must stay closed) are the point; the positive cases only prove the gate
// is not closed by accident.

const (
	c03Password   = "synthetic-password"
	c03CookieName = "routeweft_admin_session"
)

func c03App(t *testing.T, bootstrap bool) *App {
	t.Helper()
	cfg := Config{DataDir: storetest.DataDir(t)}
	if bootstrap {
		// The admin pre-exists (low-iteration fixture hash), so the bootstrap pair is
		// ignored as on any later boot; the bootstrap tests below do not use this.
		cfg.DataDir = storetest.DataDirWithAdmin(t, "operator", c03Password)
		cfg.AdminBootstrapUsername, cfg.AdminBootstrapPassword = "operator", c03Password
	}
	a := New(cfg, nil)
	if err := a.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = a.store.Close() })
	return a
}

func c03Login(t *testing.T, a *App) *http.Cookie {
	t.Helper()
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin/v1/auth/login", strings.NewReader(`{"username":"operator","password":"`+c03Password+`"}`)))
	if w.Code != http.StatusOK || len(w.Result().Cookies()) == 0 {
		t.Fatalf("login %d %s", w.Code, w.Body)
	}
	return w.Result().Cookies()[0]
}

func c03Do(a *App, r *http.Request) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, r)
	return w
}

// c03AdminRoutes extracts every admin pattern the control API registers, so a
// route added later without requireSession fails this suite instead of slipping by.
func c03AdminRoutes(t *testing.T) [][2]string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join("..", "control", "api", "*.go"))
	if err != nil || len(files) == 0 {
		t.Fatalf("control api sources not found: %v", err)
	}
	pattern := regexp.MustCompile(`"(GET|POST|PUT|PATCH|DELETE) (/admin/v1/[^"]*)"`)
	param := regexp.MustCompile(`\{[^}]*\}`)
	seen := map[[2]string]bool{}
	var routes [][2]string
	add := func(method, path string) {
		if strings.HasSuffix(path, "/") { // the loop prefix, not a route
			return
		}
		route := [2]string{method, param.ReplaceAllString(path, "x")}
		if !seen[route] {
			seen[route] = true
			routes = append(routes, route)
		}
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		source, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		for _, match := range pattern.FindAllStringSubmatch(string(source), -1) {
			add(match[1], match[2])
		}
	}
	// Registered in a loop in Attach as "GET /admin/v1/"+path.
	for _, name := range []string{"overview", "providers", "provider-nodes", "connections", "models", "aliases", "pricing", "combos", "proxy-pools", "keys", "usage", "requests", "quota", "token-saver", "systemone"} {
		add(http.MethodGet, "/admin/v1/"+name)
	}
	if len(routes) < 60 {
		t.Fatalf("only %d admin routes extracted; the scan is broken", len(routes))
	}
	return routes
}

// c03PublicAdmin is the complete set of /admin/v1 routes that answer without a
// session. Each does its own credential check.
var c03PublicAdmin = map[[2]string]bool{
	{http.MethodPost, "/admin/v1/auth/login"}:  true,
	{http.MethodPost, "/admin/v1/auth/logout"}: true,
	{http.MethodGet, "/admin/v1/auth/session"}: true,
}

func TestC03EveryAdminRouteRefusesWithoutSession(t *testing.T) {
	a := c03App(t, true)
	extracted := 0
	for _, route := range c03AdminRoutes(t) {
		if c03PublicAdmin[route] {
			extracted++
			continue
		}
		w := c03Do(a, httptest.NewRequest(route[0], route[1], strings.NewReader("{}")))
		if w.Code != http.StatusUnauthorized {
			t.Errorf("%s %s without session = %d, want 401", route[0], route[1], w.Code)
		}
	}
	if extracted != len(c03PublicAdmin) {
		t.Fatalf("found %d of %d public admin routes; the route scan or the allow-list drifted", extracted, len(c03PublicAdmin))
	}
}

func TestC03PublicAdminRoutesGrantNothingWithoutCredentials(t *testing.T) {
	a := c03App(t, true)
	if w := c03Do(a, httptest.NewRequest(http.MethodGet, "/admin/v1/auth/session", nil)); w.Code != http.StatusUnauthorized {
		t.Fatalf("session probe without cookie = %d, want 401", w.Code)
	}
	w := c03Do(a, httptest.NewRequest(http.MethodPost, "/admin/v1/auth/login", strings.NewReader(`{"username":"operator","password":"wrong"}`)))
	if w.Code != http.StatusUnauthorized || len(w.Result().Cookies()) != 0 {
		t.Fatalf("wrong password = %d with %d cookies, want 401 and none", w.Code, len(w.Result().Cookies()))
	}
	// Logout without a session is a harmless no-op, not an error oracle.
	if w := c03Do(a, httptest.NewRequest(http.MethodPost, "/admin/v1/auth/logout", nil)); w.Code != http.StatusNoContent {
		t.Fatalf("logout without session = %d, want 204", w.Code)
	}
}

// TestC03NoLocalOrHeaderBypassOpensAdmin proves the v1 bypasses stay closed:
// loopback peer, localhost Host/Origin, the CLI token header, forwarded-IP
// headers, v1's cookie, and an inference API key.
func TestC03NoLocalOrHeaderBypassOpensAdmin(t *testing.T) {
	a := c03App(t, true)
	_, key, err := a.runtime.CreateAPIKey(context.Background(), "client")
	if err != nil {
		t.Fatal(err)
	}
	variants := map[string]func(*http.Request){
		"loopback peer":      func(r *http.Request) { r.RemoteAddr = "127.0.0.1:50000" },
		"ipv6 loopback peer": func(r *http.Request) { r.RemoteAddr = "[::1]:50000" },
		"localhost host":     func(r *http.Request) { r.Host = "localhost:21128" },
		"localhost origin":   func(r *http.Request) { r.Host = "localhost:21128"; r.Header.Set("Origin", "http://localhost:21128") },
		"v1 cli token":       func(r *http.Request) { r.Header.Set("X-9r-Cli-Token", "anything") },
		"v1 peer headers":    func(r *http.Request) { r.Header.Set("X-9r-Real-Ip", "127.0.0.1"); r.Header.Set("X-9r-Peer-Token", "x") },
		"forwarded for": func(r *http.Request) {
			r.Header.Set("X-Forwarded-For", "127.0.0.1")
			r.Header.Set("X-Real-Ip", "127.0.0.1")
		},
		"inference bearer":    func(r *http.Request) { r.Header.Set("Authorization", "Bearer "+key) },
		"inference x-api-key": func(r *http.Request) { r.Header.Set("X-Api-Key", key) },
		"query key":           func(r *http.Request) { r.URL.RawQuery = "key=" + key },
		"forged session":      func(r *http.Request) { r.AddCookie(&http.Cookie{Name: c03CookieName, Value: "forged"}) },
		"v1 auth_token":       func(r *http.Request) { r.AddCookie(&http.Cookie{Name: "auth_token", Value: "forged"}) },
	}
	routes := [][2]string{{http.MethodGet, "/admin/v1/settings"}, {http.MethodPatch, "/admin/v1/settings"}, {http.MethodGet, "/admin/v1/backup"}, {http.MethodPost, "/admin/v1/auth/password"}, {http.MethodPost, "/admin/v1/keys"}}
	for name, mutate := range variants {
		for _, route := range routes {
			r := httptest.NewRequest(route[0], route[1], strings.NewReader(`{"set":{"requireApiKey":"false"}}`))
			mutate(r)
			if w := c03Do(a, r); w.Code != http.StatusUnauthorized {
				t.Errorf("%s: %s %s = %d, want 401", name, route[0], route[1], w.Code)
			}
		}
	}
	if a.runtime.Settings()["requireApiKey"] != "true" {
		t.Fatal("an unauthenticated request changed settings")
	}
}

func TestC03NoResetStatusOrRequireLoginRoutes(t *testing.T) {
	a := c03App(t, true)
	for _, route := range [][2]string{
		{http.MethodPost, "/api/auth/reset-password"},
		{http.MethodPost, "/admin/v1/auth/reset-password"},
		{http.MethodPost, "/admin/v1/auth/reset"},
		{http.MethodPost, "/admin/v1/auth/bootstrap"},
		{http.MethodPost, "/admin/v1/auth/setup"},
		{http.MethodGet, "/api/auth/status"},
		{http.MethodGet, "/api/settings/require-login"},
		{http.MethodGet, "/admin/v1/settings/require-login"},
		{http.MethodPut, "/admin/v1/settings/require-login"},
	} {
		r := httptest.NewRequest(route[0], route[1], strings.NewReader(`{"password":"attacker"}`))
		r.RemoteAddr = "127.0.0.1:50000"
		if w := c03Do(a, r); w.Code >= 200 && w.Code < 300 {
			t.Errorf("%s %s = %d; no recovery, status or require-login route may exist", route[0], route[1], w.Code)
		}
	}
	// The operator password still works, so nothing above reset it.
	c03Login(t, a)
}

func TestC03RequireLoginIsNotWritable(t *testing.T) {
	a := c03App(t, true)
	cookie := c03Login(t, a)
	patch := func(body string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPatch, "/admin/v1/settings", strings.NewReader(body))
		r.AddCookie(cookie)
		return c03Do(a, r)
	}
	if w := patch(`{"set":{"requireLogin":"false"}}`); w.Code != http.StatusBadRequest {
		t.Fatalf("set requireLogin=false = %d %s, want 400", w.Code, w.Body)
	}
	if w := patch(`{"remove":["requireLogin"]}`); w.Code != http.StatusBadRequest {
		t.Fatalf("remove requireLogin = %d %s, want 400", w.Code, w.Body)
	}
	r := httptest.NewRequest(http.MethodGet, "/admin/v1/settings", nil)
	r.AddCookie(cookie)
	w := c03Do(a, r)
	var got struct {
		Settings map[string]string `json:"settings"`
		Writable []string          `json:"writable"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Settings["requireLogin"] != "true" {
		t.Fatalf("requireLogin=%q want true", got.Settings["requireLogin"])
	}
	for _, name := range got.Writable {
		if name == "requireLogin" {
			t.Fatal("requireLogin is listed as writable")
		}
	}
}

// TestC03StoredRequireLoginFalseDoesNotOpenAdmin restarts on a database whose
// settings table holds requireLogin=false (hand edit or restored backup).
func TestC03StoredRequireLoginFalseDoesNotOpenAdmin(t *testing.T) {
	first := c03App(t, true)
	dir := first.cfg.DataDir
	if _, err := first.store.DB().ExecContext(context.Background(), "INSERT INTO settings (key,value) VALUES ('requireLogin','false')"); err != nil {
		t.Fatal(err)
	}
	_ = first.store.Close()
	second := New(Config{DataDir: dir}, nil)
	if err := second.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer second.store.Close()
	if w := c03Do(second, httptest.NewRequest(http.MethodGet, "/admin/v1/settings", nil)); w.Code != http.StatusUnauthorized {
		t.Fatalf("settings with stored requireLogin=false = %d, want 401", w.Code)
	}
	if got := second.runtime.Settings()["requireLogin"]; got != "true" {
		t.Fatalf("compiled requireLogin=%q want true", got)
	}
}

func TestC03FreshInstallWithoutBootstrapCredentialIsLockedNotOpen(t *testing.T) {
	a := c03App(t, false)
	if has, err := a.admin.HasAdmin(context.Background()); err != nil || has {
		t.Fatalf("HasAdmin=%v err=%v, want no admin and no error", has, err)
	}
	for _, route := range c03AdminRoutes(t) {
		if c03PublicAdmin[route] {
			continue
		}
		r := httptest.NewRequest(route[0], route[1], strings.NewReader("{}"))
		r.RemoteAddr = "127.0.0.1:50000"
		if w := c03Do(a, r); w.Code != http.StatusUnauthorized {
			t.Errorf("no-admin install: %s %s = %d, want 401", route[0], route[1], w.Code)
		}
	}
	login := c03Do(a, httptest.NewRequest(http.MethodPost, "/admin/v1/auth/login", strings.NewReader(`{"username":"operator","password":""}`)))
	if login.Code != http.StatusConflict || len(login.Result().Cookies()) != 0 {
		t.Fatalf("login with no admin = %d, want 409 and no session", login.Code)
	}
	// No default password: the guesses v1 would have accepted fail.
	for _, guess := range []string{"123456", "admin", "routeweft", "changeme"} {
		w := c03Do(a, httptest.NewRequest(http.MethodPost, "/admin/v1/auth/login", strings.NewReader(`{"username":"admin","password":"`+guess+`"}`)))
		if w.Code == http.StatusOK || len(w.Result().Cookies()) != 0 {
			t.Fatalf("default guess %q produced a session", guess)
		}
	}
}

func TestC03BootstrapIsAllOrNothing(t *testing.T) {
	ctx := context.Background()
	// A username without a password (and the reverse) refuses to start rather
	// than provisioning a half credential.
	for _, cfg := range []Config{{AdminBootstrapUsername: "operator"}, {AdminBootstrapPassword: "pw"}} {
		cfg.DataDir = t.TempDir()
		if err := New(cfg, nil).Initialize(ctx); err == nil {
			t.Fatalf("half bootstrap credential %+v started", cfg)
		}
	}
}

func TestC03AdminSessionIsIndependentOfRequireAPIKey(t *testing.T) {
	a := c03App(t, true)
	_, key, err := a.runtime.CreateAPIKey(context.Background(), "client")
	if err != nil {
		t.Fatal(err)
	}
	models := func(withKey bool) int {
		r := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		r.RemoteAddr = "127.0.0.1:50000" // no loopback exemption on the inference side either
		r.Host = "localhost:21128"
		if withKey {
			r.Header.Set("Authorization", "Bearer "+key)
		}
		return c03Do(a, r).Code
	}
	if got := models(false); got != http.StatusUnauthorized {
		t.Fatalf("loopback inference without key = %d, want 401", got)
	}
	if got := models(true); got != http.StatusOK {
		t.Fatalf("inference with key = %d, want 200", got)
	}
	r := httptest.NewRequest(http.MethodPatch, "/admin/v1/settings", strings.NewReader(`{"set":{"requireApiKey":"false"}}`))
	r.AddCookie(c03Login(t, a))
	if w := c03Do(a, r); w.Code != http.StatusOK {
		t.Fatalf("operator could not set requireApiKey=false: %d %s", w.Code, w.Body)
	}
	if got := models(false); got != http.StatusOK {
		t.Fatalf("requireApiKey=false inference = %d, want 200 (the documented inference-only open mode)", got)
	}
	// Opening inference never opens the control plane.
	adminReq := httptest.NewRequest(http.MethodGet, "/admin/v1/settings", nil)
	adminReq.RemoteAddr = "127.0.0.1:50000"
	if w := c03Do(a, adminReq); w.Code != http.StatusUnauthorized {
		t.Fatalf("admin with requireApiKey=false = %d, want 401", w.Code)
	}
}

// TestC03UnauthenticatedSurfaceIsHealthOnly pins the paths that answer without
// any credential when no UI is configured: liveness and readiness, nothing else.
// There is no metrics, pprof or debug endpoint.
func TestC03UnauthenticatedSurfaceIsHealthOnly(t *testing.T) {
	a := c03App(t, true)
	for _, path := range []string{"/health/live", "/health/ready"} {
		w := c03Do(a, httptest.NewRequest(http.MethodGet, path, nil))
		if w.Code != http.StatusOK || w.Body.String() != "ok\n" {
			t.Errorf("%s = %d %q, want 200 \"ok\\n\" and no other data", path, w.Code, w.Body)
		}
	}
	for _, path := range []string{"/metrics", "/debug/pprof/", "/debug/vars", "/health", "/health/", "/status", "/admin/", "/admin/v1/", "/api/health"} {
		r := httptest.NewRequest(http.MethodGet, path, nil)
		r.RemoteAddr = "127.0.0.1:50000"
		if w := c03Do(a, r); w.Code >= 200 && w.Code < 300 {
			t.Errorf("GET %s = %d, want no unauthenticated endpoint", path, w.Code)
		}
	}
}

// TestC03RecoveryRequiresHostWriteAccessToTheDatabase documents the only
// lost-password path: with the process stopped, an operator who can write the
// SQLite file empties admin_users and restarts with bootstrap credentials.
// Nothing reachable over HTTP does this, and a running process never re-runs it.
func TestC03RecoveryRequiresHostWriteAccessToTheDatabase(t *testing.T) {
	ctx := context.Background()
	first := c03App(t, true)
	dir := first.cfg.DataDir
	if _, err := first.store.DB().ExecContext(ctx, "DELETE FROM admin_users"); err != nil {
		t.Fatal(err)
	}
	_ = first.store.Close()
	second := New(Config{DataDir: dir, AdminBootstrapUsername: "operator", AdminBootstrapPassword: "recovered-password"}, nil)
	if err := second.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	defer second.store.Close()
	login := func(password string) int {
		body := `{"username":"operator","password":"` + password + `"}`
		return c03Do(second, httptest.NewRequest(http.MethodPost, "/admin/v1/auth/login", strings.NewReader(body))).Code
	}
	if got := login(c03Password); got != http.StatusUnauthorized {
		t.Fatalf("old password after recovery = %d, want 401", got)
	}
	if got := login("recovered-password"); got != http.StatusOK {
		t.Fatalf("recovered password = %d, want 200", got)
	}
}

// TestC03LoginThrottleHoldsAcrossSourcePorts sends wrong-password logins from
// one host with a new source port each time, as a real client opening a fresh
// connection per attempt. Keyed on the full socket address this never throttles.
func TestC03LoginThrottleHoldsAcrossSourcePorts(t *testing.T) {
	a := c03App(t, true)
	login := func(host string, port int, password string) int {
		r := httptest.NewRequest(http.MethodPost, "/admin/v1/auth/login", strings.NewReader(`{"username":"operator","password":"`+password+`"}`))
		r.RemoteAddr = net.JoinHostPort(host, strconv.Itoa(port))
		return c03Do(a, r).Code
	}
	limited := 0
	for i := 0; i < 30; i++ {
		if login("203.0.113.7", 40000+i, "wrong") == http.StatusTooManyRequests {
			limited++
		}
	}
	if limited == 0 {
		t.Fatal("30 wrong-password logins from one host on distinct ports were never throttled")
	}
	// Once limited, the right password from that host is refused too.
	if got := login("203.0.113.7", 50000, c03Password); got != http.StatusTooManyRequests {
		t.Fatalf("correct password while limited = %d, want 429", got)
	}
	// Another host (and an IPv6 peer) is unaffected.
	if got := login("198.51.100.9", 40000, c03Password); got != http.StatusOK {
		t.Fatalf("other host = %d, want 200", got)
	}
	if got := login("2001:db8::1", 40000, "wrong"); got != http.StatusUnauthorized {
		t.Fatalf("ipv6 peer first failure = %d, want 401", got)
	}
}
