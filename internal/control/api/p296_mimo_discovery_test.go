package api

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/credentials"
	"github.com/raufimusaddiq/routeweft/internal/providers/onboarding"
)

// #296: MiMo Desktop credential discovery. The pin reads Desktop's key file and
// cookie store from the host whenever its Xiaomi modal opens and on every quota
// read without a stored passToken (auto-import/route.js:52-131,
// mimoAccount.js:36-94,181-185). Here the host must allow it and the operator
// must consent in the request; what is read is sealed into the connection and
// never answered, logged or read again behind the operator's back.

const (
	discoveredKey  = "sk-fixture-desktop-key"
	discoveredPass = "fixture-desktop-pass"
	discoveredUser = "fixture-desktop-user"
	discoveredCUID = "fixture-desktop-cuser"
)

// countingDesktop counts every read of the host's files.
type countingDesktop struct {
	onboarding.MimoDesktop
	authReads, sessionReads atomic.Int32
}

func (d *countingDesktop) ReadAuthFile(limit int64) (string, []byte, error) {
	d.authReads.Add(1)
	return d.MimoDesktop.ReadAuthFile(limit)
}

func (d *countingDesktop) ReadSession(ctx context.Context) map[string]string {
	d.sessionReads.Add(1)
	return d.MimoDesktop.ReadSession(ctx)
}

func (d *countingDesktop) reads() int32 { return d.authReads.Load() + d.sessionReads.Load() }

// writeDesktopAuthFile writes MiMoCode's auth.json in the fixture home.
func writeDesktopAuthFile(t *testing.T, desktop onboarding.MimoDesktop, contents string) {
	t.Helper()
	path := desktop.AuthFilePaths()[0]
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

// writeDesktopCookieStore writes Desktop's Chromium cookie store with an account
// session in the fixture home.
func writeDesktopCookieStore(t *testing.T, desktop onboarding.MimoDesktop) {
	t.Helper()
	path := desktop.CookieStorePath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE cookies (host_key TEXT NOT NULL, name TEXT NOT NULL, value TEXT NOT NULL, encrypted_value BLOB NOT NULL DEFAULT x'')`); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{"passToken": discoveredPass, "userId": discoveredUser, "cUserId": discoveredCUID} {
		if _, err := db.Exec(`INSERT INTO cookies (host_key, name, value) VALUES ('.account.xiaomi.com', ?, ?)`, name, value); err != nil {
			t.Fatal(err)
		}
	}
}

const desktopAuthFile = `{
  "xiaomi": {
    "type": "api",
    "key": "` + discoveredKey + `",
    "metadata": { "uid": "7", "base_url": "https://api.xiaomimimo.com/v1" }
  }
}`

type discoveryEnv struct {
	*importEnv
	handler *Handler
	desktop *countingDesktop
}

// newDiscoveryEnv is the onboarding API over a fixture home, with the host's
// consent given or not.
func newDiscoveryEnv(t *testing.T, allowed bool) *discoveryEnv {
	t.Helper()
	exchange := &routeExchange{routes: map[string]string{"/v1/models": `{"data":[{"id":"mimo-v2-flash"},{"id":"mimo-v2-pro"}]}`}}
	h, mux, creds, cookie := newOnboardingAPI(t, exchange)
	addBuiltinSpecs(h, "xiaomi-mimo")
	desktop := &countingDesktop{MimoDesktop: onboarding.MimoDesktop{Home: t.TempDir(), GOOS: "linux", TempDir: t.TempDir()}}
	h.mimoDesktop = desktop
	h.opts.HostCredentialDiscovery = allowed
	return &discoveryEnv{importEnv: &importEnv{mux: mux, creds: creds, cookie: cookie, exchange: exchange}, handler: h, desktop: desktop}
}

func (env *discoveryEnv) discover(t *testing.T, body string) (int, map[string]any, string) {
	t.Helper()
	recorder := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/oauth/xiaomi-mimo/discover", body)
	var decoded map[string]any
	_ = json.Unmarshal(recorder.Body.Bytes(), &decoded)
	return recorder.Code, decoded, recorder.Body.String()
}

func discoveryError(body map[string]any) string {
	if inner, ok := body["error"].(map[string]any); ok {
		code, _ := inner["code"].(string)
		return code
	}
	return ""
}

// Nothing on the host is read unless the host allows it and the operator
// consents in the request; nor for an anonymous caller or another provider.
func TestMimoDiscoveryNeedsBothConsents(t *testing.T) {
	disabled := newDiscoveryEnv(t, false)
	writeDesktopAuthFile(t, disabled.desktop.MimoDesktop, desktopAuthFile)
	writeDesktopCookieStore(t, disabled.desktop.MimoDesktop)
	if status, body, _ := disabled.discover(t, `{"consent":true}`); status != http.StatusForbidden || discoveryError(body) != "host_discovery_disabled" {
		t.Fatalf("host not allowed: %d %v", status, body)
	}

	env := newDiscoveryEnv(t, true)
	writeDesktopAuthFile(t, env.desktop.MimoDesktop, desktopAuthFile)
	writeDesktopCookieStore(t, env.desktop.MimoDesktop)
	for _, body := range []string{`{}`, `{"consent":false}`} {
		if status, decoded, _ := env.discover(t, body); status != http.StatusBadRequest || discoveryError(decoded) != "consent_required" {
			t.Fatalf("%s: %d %v", body, status, decoded)
		}
	}
	if recorder := doJSON(t, env.mux, nil, http.MethodPost, "/admin/v1/oauth/xiaomi-mimo/discover", `{"consent":true}`); recorder.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: %d %s", recorder.Code, recorder.Body.String())
	}
	if recorder := doJSON(t, env.mux, env.cookie, http.MethodPost, "/admin/v1/oauth/kiro/discover", `{"consent":true}`); recorder.Code != http.StatusNotFound {
		t.Fatalf("kiro: %d %s", recorder.Code, recorder.Body.String())
	}
	if reads := disabled.desktop.reads() + env.desktop.reads(); reads != 0 {
		t.Fatalf("host files read %d times without both consents", reads)
	}
	if rows := disabled.connections(t, "xiaomi-mimo"); len(rows) != 0 {
		t.Fatalf("saved without consent: %+v", rows)
	}
	if rows := env.connections(t, "xiaomi-mimo"); len(rows) != 0 {
		t.Fatalf("saved without consent: %+v", rows)
	}
}

// With both consents the key file and Desktop's session become one sealed
// connection, as the pin's auto-import and api-key import do together; the
// answer names the source but carries no credential, and nothing is logged.
func TestMimoDiscoveryImportsTheKeyAndSessionSealed(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	t.Cleanup(func() { slog.SetDefault(previous) })

	env := newDiscoveryEnv(t, true)
	writeDesktopAuthFile(t, env.desktop.MimoDesktop, desktopAuthFile)
	writeDesktopCookieStore(t, env.desktop.MimoDesktop)
	status, body, raw := env.discover(t, `{"consent":true}`)
	if status != http.StatusOK || body["identity"] != "7@xiaomi" || body["outcome"] != onboardedAdded {
		t.Fatalf("discover: %d %s", status, raw)
	}
	discovery, _ := body["discovery"].(map[string]any)
	if discovery["source"] != env.desktop.AuthFilePaths()[0] || discovery["accountSession"] != true {
		t.Fatalf("discovery = %v", discovery)
	}
	assertNoSecret(t, "discover answer", raw, discoveredKey, discoveredPass, discoveredUser, discoveredCUID)
	assertNoSecret(t, "log output", logs.String(), discoveredKey, discoveredPass, discoveredUser, discoveredCUID)

	rows := env.connections(t, "xiaomi-mimo")
	if len(rows) != 1 {
		t.Fatalf("rows = %+v", rows)
	}
	row := rows[0]
	if row.AuthKind != credentials.AuthAPIKey || row.Secret.AccessToken != discoveredKey {
		t.Fatalf("row = %+v", row)
	}
	for key, want := range map[string]string{"mimoPassToken": discoveredPass, "mimoUserId": discoveredUser, "mimoCUserId": discoveredCUID, "uid": "7", "baseUrl": "https://api.xiaomimimo.com/v1", "modelCount": "2"} {
		if got := row.Secret.ProviderData[key]; got != want {
			t.Errorf("%s = %q, want %q", key, got, want)
		}
	}
	// The key was proven as a pasted one is: the models listing with MiMoCode's
	// source header, nothing else.
	if seen := strings.Join(env.exchange.seenURLs, " "); seen != "https://api.xiaomimimo.com/v1/models" {
		t.Fatalf("upstream calls: %s", seen)
	}
	if list := doJSON(t, env.mux, env.cookie, http.MethodGet, "/admin/v1/connections", ""); list.Code != http.StatusOK {
		t.Fatalf("connections: %d", list.Code)
	} else {
		assertNoSecret(t, "connections read model", list.Body.String(), discoveredKey, discoveredPass, discoveredUser, discoveredCUID)
	}
}

// While Desktop runs its store is locked and the pin imports the key alone
// (route.js:106-121); discovering again once it can be read adds the session to
// the same connection, as the api-key route's merge does (:66-80).
func TestMimoDiscoveryWithoutASessionThenWithOne(t *testing.T) {
	env := newDiscoveryEnv(t, true)
	writeDesktopAuthFile(t, env.desktop.MimoDesktop, desktopAuthFile)
	status, body, raw := env.discover(t, `{"consent":true}`)
	discovery, _ := body["discovery"].(map[string]any)
	if status != http.StatusOK || discovery["accountSession"] != false {
		t.Fatalf("key alone: %d %s", status, raw)
	}
	if rows := env.connections(t, "xiaomi-mimo"); len(rows) != 1 || rows[0].Secret.ProviderData["mimoPassToken"] != "" {
		t.Fatalf("rows = %+v", rows)
	}
	id := body["id"]
	writeDesktopCookieStore(t, env.desktop.MimoDesktop)
	status, body, raw = env.discover(t, `{"consent":true}`)
	if status != http.StatusOK || body["outcome"] != onboardedUpdated || body["id"] != id {
		t.Fatalf("again: %d %s", status, raw)
	}
	rows := env.connections(t, "xiaomi-mimo")
	if len(rows) != 1 || rows[0].Secret.ProviderData["mimoPassToken"] != discoveredPass || rows[0].Secret.AccessToken != discoveredKey {
		t.Fatalf("rows = %+v", rows)
	}
}

// The pin's own answers for a missing or unusable key file; Desktop's cookie
// store is not read for any of them, since there is no key to attach it to.
func TestMimoDiscoveryReportsAnUnusableKeyFile(t *testing.T) {
	env := newDiscoveryEnv(t, true)
	writeDesktopCookieStore(t, env.desktop.MimoDesktop)
	status, body, raw := env.discover(t, `{"consent":true}`)
	if status != http.StatusNotFound || discoveryError(body) != "discovery_not_found" || !strings.Contains(raw, "Xiaomi MiMo Desktop auth file not found. Checked:") || !strings.Contains(raw, filepath.ToSlash(env.desktop.AuthFilePaths()[0])) {
		t.Fatalf("missing: %d %s", status, raw)
	}
	cases := map[string]string{
		`{"xiaomi":`:                     "auth.json is not valid JSON. Please sign in to Xiaomi MiMo Desktop again.",
		`{}`:                             "No Xiaomi credentials found in auth.json. Please sign in to Xiaomi MiMo Desktop.",
		`{"xiaomi":{"key":""}}`:          "No Xiaomi credentials found in auth.json. Please sign in to Xiaomi MiMo Desktop.",
		`{"xiaomi":{"key":"not-a-key"}}`: "Xiaomi key does not appear to be a valid API key (expected sk- prefix).",
		// Decoded values are held to /import's control-character rule.
		`{"xiaomi":{"key":"sk-a\u000db"}}`:                       "the credential contains control characters",
		`{"xiaomi":{"key":"sk-a","metadata":{"uid":"7\u0000"}}}`: "the credential contains control characters",
	}
	for contents, message := range cases {
		writeDesktopAuthFile(t, env.desktop.MimoDesktop, contents)
		status, body, raw := env.discover(t, `{"consent":true}`)
		if status != http.StatusBadRequest || discoveryError(body) != "import_invalid" || !strings.Contains(raw, message) {
			t.Errorf("%s: %d %s, want %q", contents, status, raw, message)
		}
	}
	if reads := env.desktop.sessionReads.Load(); reads != 0 {
		t.Fatalf("cookie store read %d times without a usable key", reads)
	}
	if rows := env.connections(t, "xiaomi-mimo"); len(rows) != 0 {
		t.Fatalf("saved: %+v", rows)
	}
	// A file without a uid names no account; the operator names it instead.
	writeDesktopAuthFile(t, env.desktop.MimoDesktop, `{"xiaomi":{"key":"`+discoveredKey+`"}}`)
	if status, body, raw := env.discover(t, `{"consent":true}`); status != http.StatusBadRequest || discoveryError(body) != "oauth_identity_unresolved" {
		t.Fatalf("no uid: %d %s", status, raw)
	}
	if status, body, raw := env.discover(t, `{"consent":true,"identity":"work@xiaomi"}`); status != http.StatusOK || body["identity"] != "work@xiaomi" {
		t.Fatalf("named: %d %s", status, raw)
	}
}

// A number uid names the account as the pin's template literal does.
func TestMimoAuthFileAccountReadsThePinsFields(t *testing.T) {
	account, problem := mimoAuthFileAccount([]byte(`{"xiaomi":{"key":" sk-a ","metadata":{"uid":1234,"base_url":null}}}`))
	if problem != "" || account["apiKey"] != "sk-a" || account["uid"] != "1234" || account["baseUrl"] != "" {
		t.Fatalf("account = %v, problem %q", account, problem)
	}
	if account, _ := mimoAuthFileAccount([]byte(`{"xiaomi":{"key":"sk-a","metadata":{"uid":0}}}`)); account["uid"] != "" {
		t.Fatalf("a zero uid is falsy in the pin: %v", account)
	}
}

// The pin's exchange attaches Desktop's session to every Xiaomi sign-in
// ([provider]/[action]/route.js:335-361); here only to one started with consent,
// on a host that allows it.
func TestMimoSignInAttachesTheDesktopSessionOnlyWithConsent(t *testing.T) {
	signIn := func(t *testing.T, h *Handler, mux *http.ServeMux, cookie *http.Cookie, start string) (int, string) {
		t.Helper()
		started := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/xiaomi-mimo/start", start)
		if started.Code != http.StatusOK {
			return started.Code, started.Body.String()
		}
		var flow struct {
			State string `json:"state"`
		}
		_ = json.Unmarshal(started.Body.Bytes(), &flow)
		payload := callbackPayloadFor(t, "xiaomi-mimo", started.Body.Bytes())
		done := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/xiaomi-mimo/callback", `{"state":"`+flow.State+`","input":`+quoteJSON(payload)+`}`)
		return done.Code, done.Body.String()
	}
	setup := func(t *testing.T, allowed bool) (*Handler, *http.ServeMux, *http.Cookie, *countingDesktop, func() []credentials.Connection) {
		h, mux, store, cookie := flowHandler(t)
		t.Cleanup(func() { store.Close() })
		desktop := &countingDesktop{MimoDesktop: onboarding.MimoDesktop{Home: t.TempDir(), GOOS: "linux", TempDir: t.TempDir()}}
		writeDesktopCookieStore(t, desktop.MimoDesktop)
		h.mimoDesktop, h.opts.HostCredentialDiscovery = desktop, allowed
		return h, mux, cookie, desktop, func() []credentials.Connection {
			rows, err := h.opts.Credentials.ListConnections(context.Background(), "xiaomi-mimo")
			if err != nil {
				t.Fatal(err)
			}
			return rows
		}
	}

	t.Run("host not allowed", func(t *testing.T) {
		h, mux, cookie, desktop, _ := setup(t, false)
		if status, body := signIn(t, h, mux, cookie, `{"desktopSession":true}`); status != http.StatusForbidden || !strings.Contains(body, "host_discovery_disabled") {
			t.Fatalf("%d %s", status, body)
		}
		if desktop.reads() != 0 {
			t.Fatal("read the host")
		}
	})
	t.Run("another provider", func(t *testing.T) {
		_, mux, cookie, _, _ := setup(t, true)
		if recorder := doJSON(t, mux, cookie, http.MethodPost, "/admin/v1/oauth/zed/start", `{"desktopSession":true}`); recorder.Code != http.StatusBadRequest || !strings.Contains(recorder.Body.String(), "desktop_session_unsupported") {
			t.Fatalf("%d %s", recorder.Code, recorder.Body.String())
		}
	})
	t.Run("no consent", func(t *testing.T) {
		h, mux, cookie, desktop, rows := setup(t, true)
		if status, body := signIn(t, h, mux, cookie, `{}`); status != http.StatusOK {
			t.Fatalf("%d %s", status, body)
		}
		if desktop.reads() != 0 {
			t.Fatal("read the host without consent")
		}
		if got := rows(); len(got) != 1 || got[0].Secret.ProviderData["mimoPassToken"] != "" {
			t.Fatalf("rows = %+v", got)
		}
	})
	t.Run("consent", func(t *testing.T) {
		h, mux, cookie, _, rows := setup(t, true)
		status, body := signIn(t, h, mux, cookie, `{"desktopSession":true}`)
		if status != http.StatusOK {
			t.Fatalf("%d %s", status, body)
		}
		assertNoSecret(t, "callback answer", body, discoveredPass, discoveredUser, discoveredCUID)
		got := rows()
		if len(got) != 1 || got[0].Secret.AccessToken != "fixture-sk" || got[0].Secret.ProviderData["uid"] != "fixture-uid" {
			t.Fatalf("rows = %+v", got)
		}
		for key, want := range map[string]string{"mimoPassToken": discoveredPass, "mimoUserId": discoveredUser, "mimoCUserId": discoveredCUID} {
			if got[0].Secret.ProviderData[key] != want {
				t.Errorf("%s = %q", key, got[0].Secret.ProviderData[key])
			}
		}
	})
}

// The dashboard offers discovery only where the endpoint would read.
func TestProviderReadModelReportsHostDiscovery(t *testing.T) {
	for _, allowed := range []bool{false, true} {
		env := newDiscoveryEnv(t, allowed)
		recorder := doJSON(t, env.mux, env.cookie, http.MethodGet, "/admin/v1/providers?limit=500", "")
		var page struct {
			Items []struct {
				ID            string `json:"id"`
				HostDiscovery bool   `json:"hostDiscovery"`
			} `json:"items"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil || recorder.Code != http.StatusOK {
			t.Fatalf("providers: %d %v", recorder.Code, err)
		}
		found := false
		for _, item := range page.Items {
			if item.ID == "xiaomi-mimo" {
				found = true
				if item.HostDiscovery != allowed {
					t.Errorf("allowed=%v: hostDiscovery=%v", allowed, item.HostDiscovery)
				}
			} else if item.HostDiscovery {
				t.Errorf("%s offers host discovery", item.ID)
			}
		}
		if !found {
			t.Fatal("xiaomi-mimo is not listed")
		}
	}
}
