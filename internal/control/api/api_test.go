package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/raufimusaddiq/routeweft/internal/adminauth"
	"github.com/raufimusaddiq/routeweft/internal/store/migrations"
	"github.com/raufimusaddiq/routeweft/internal/store/sqlite"
)

type fakeSettings struct {
	values   map[string]string
	setErr   error
	revision uint64
}

func (f *fakeSettings) Settings() map[string]string { return f.values }

func (f *fakeSettings) SetSettings(_ context.Context, values map[string]string, remove []string) (uint64, error) {
	if f.setErr != nil {
		return 0, f.setErr
	}
	for key := range values {
		if key == "notARealSetting" {
			return 0, errors.New("setting is not writable through this route")
		}
	}
	f.revision++
	for key, value := range values {
		f.values[key] = value
	}
	for _, key := range remove {
		delete(f.values, key)
	}
	return f.revision, nil
}

func newAPI(t *testing.T, settings SettingsStore) (*Handler, *adminauth.Store, *sqlite.Store) {
	t.Helper()
	ctx := context.Background()
	store, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "routeweft.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	if err := migrations.NewRunner(store.DB()).Apply(ctx); err != nil {
		t.Fatal(err)
	}
	accounts := adminauth.NewStore(store.DB())
	if _, err := accounts.Bootstrap(ctx, "a1", "operator", "s3cret"); err != nil {
		t.Fatal(err)
	}
	handler := New(Options{Accounts: accounts, Sessions: adminauth.NewSessionManager(time.Hour), Settings: settings})
	return handler, accounts, store
}

func TestAdminRoutesRequireSession(t *testing.T) {
	handler, _, store := newAPI(t, &fakeSettings{values: map[string]string{}})
	defer store.Close()
	mux := http.NewServeMux()
	handler.Attach(mux)
	for _, target := range []string{"/admin/v1/settings"} {
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusUnauthorized {
			t.Fatalf("%s status=%d want 401", target, recorder.Code)
		}
	}
	password := httptest.NewRecorder()
	mux.ServeHTTP(password, httptest.NewRequest(http.MethodPost, "/admin/v1/auth/password", strings.NewReader(`{"currentPassword":"x","newPassword":"y"}`)))
	if password.Code != http.StatusUnauthorized {
		t.Fatalf("password route status=%d want 401", password.Code)
	}
}

func TestChangePasswordRequiresCurrentPasswordAndRevokesSessions(t *testing.T) {
	handler, accounts, store := newAPI(t, &fakeSettings{values: map[string]string{}})
	defer store.Close()
	session, err := handler.opts.Sessions.Create(adminauth.Account{ID: "a1", Username: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	otherSession, err := handler.opts.Sessions.Create(adminauth.Account{ID: "a1", Username: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Attach(mux)
	change := func(current, next string) *httptest.ResponseRecorder {
		t.Helper()
		body := `{"currentPassword":"` + current + `","newPassword":"` + next + `"}`
		request := httptest.NewRequest(http.MethodPost, "/admin/v1/auth/password", strings.NewReader(body))
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		return recorder
	}
	if response := change("wrong", "new-secret"); response.Code != http.StatusUnauthorized {
		t.Fatalf("wrong current password status=%d want 401", response.Code)
	}
	if _, ok := handler.opts.Sessions.Lookup(session.ID); !ok {
		t.Fatal("failed password change revoked the current session")
	}
	if _, valid, err := accounts.Authenticate(context.Background(), "operator", "s3cret"); err != nil || !valid {
		t.Fatalf("failed password change changed stored password: valid=%v err=%v", valid, err)
	}
	if response := change("s3cret", "new-secret"); response.Code != http.StatusNoContent {
		t.Fatalf("password change status=%d body=%s", response.Code, response.Body.String())
	} else {
		cookies := response.Result().Cookies()
		if len(cookies) != 1 || cookies[0].MaxAge >= 0 || !cookies[0].Secure || !cookies[0].HttpOnly {
			t.Fatalf("session cookie not securely expired: %+v", cookies)
		}
	}
	for _, token := range []string{session.ID, otherSession.ID} {
		if _, ok := handler.opts.Sessions.Lookup(token); ok {
			t.Fatal("password change did not revoke every admin session")
		}
	}
	if _, valid, err := accounts.Authenticate(context.Background(), "operator", "new-secret"); err != nil || !valid {
		t.Fatalf("new password invalid: valid=%v err=%v", valid, err)
	}
	if _, valid, err := accounts.Authenticate(context.Background(), "operator", "s3cret"); err != nil || valid {
		t.Fatalf("old password still accepted after rotation: valid=%v err=%v", valid, err)
	}
}

func TestLoginIssuesSessionCookieAndGatesSettings(t *testing.T) {
	settings := &fakeSettings{values: map[string]string{"rtkEnabled": "true"}}
	handler, _, store := newAPI(t, settings)
	defer store.Close()
	mux := http.NewServeMux()
	handler.Attach(mux)

	// Wrong password is rejected.
	bad := httptest.NewRecorder()
	mux.ServeHTTP(bad, httptest.NewRequest(http.MethodPost, "/admin/v1/auth/login", strings.NewReader(`{"username":"operator","password":"nope"}`)))
	if bad.Code != http.StatusUnauthorized {
		t.Fatalf("bad login status=%d", bad.Code)
	}

	server := httptest.NewServer(mux)
	defer server.Close()
	client := &http.Client{}
	loginBody := strings.NewReader(`{"username":"operator","password":"s3cret"}`)
	response, err := client.Post(server.URL+"/admin/v1/auth/login", "application/json", loginBody)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("login status=%d", response.StatusCode)
	}
	cookies := response.Cookies()
	if len(cookies) == 0 || !cookies[0].HttpOnly {
		t.Fatalf("session cookie not set or not HttpOnly: %+v", cookies)
	}

	// Authenticated settings read succeeds.
	request, _ := http.NewRequest(http.MethodGet, server.URL+"/admin/v1/settings", nil)
	request.AddCookie(cookies[0])
	authResponse, err := client.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	defer authResponse.Body.Close()
	if authResponse.StatusCode != http.StatusOK {
		t.Fatalf("settings status=%d", authResponse.StatusCode)
	}
	var payload struct {
		Settings map[string]string `json:"settings"`
		Writable []string          `json:"writable"`
	}
	if err := json.NewDecoder(authResponse.Body).Decode(&payload); err != nil {
		t.Fatal(err)
	}
	if payload.Settings["rtkEnabled"] != "true" || len(payload.Writable) == 0 {
		t.Fatalf("payload=%+v", payload)
	}

	// Patch settings through the same session.
	patch, _ := http.NewRequest(http.MethodPatch, server.URL+"/admin/v1/settings", strings.NewReader(`{"set":{"cavemanEnabled":"true"}}`))
	patch.AddCookie(cookies[0])
	patchResponse, err := client.Do(patch)
	if err != nil {
		t.Fatal(err)
	}
	defer patchResponse.Body.Close()
	if patchResponse.StatusCode != http.StatusOK {
		t.Fatalf("patch status=%d", patchResponse.StatusCode)
	}
	if settings.values["cavemanEnabled"] != "true" {
		t.Fatalf("setting not applied: %+v", settings.values)
	}

	// Logout invalidates the session.
	logout, _ := http.NewRequest(http.MethodPost, server.URL+"/admin/v1/auth/logout", nil)
	logout.AddCookie(cookies[0])
	logoutResponse, err := client.Do(logout)
	if err != nil {
		t.Fatal(err)
	}
	defer logoutResponse.Body.Close()
	after, _ := http.NewRequest(http.MethodGet, server.URL+"/admin/v1/settings", nil)
	after.AddCookie(cookies[0])
	afterResponse, err := client.Do(after)
	if err != nil {
		t.Fatal(err)
	}
	defer afterResponse.Body.Close()
	if afterResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("logged-out session status=%d want 401", afterResponse.StatusCode)
	}
}

func TestPatchSettingsRejectsEmptyAndInvalid(t *testing.T) {
	settings := &fakeSettings{values: map[string]string{}}
	handler, _, store := newAPI(t, settings)
	defer store.Close()
	session, err := handler.opts.Sessions.Create(adminauth.Account{ID: "a1", Username: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Attach(mux)
	for _, body := range []string{`{}`, `{"set":{"notARealSetting":"1"}}`, `not json`} {
		request := httptest.NewRequest(http.MethodPatch, "/admin/v1/settings", strings.NewReader(body))
		request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		if recorder.Code != http.StatusBadRequest {
			t.Fatalf("body=%s status=%d want 400", body, recorder.Code)
		}
	}
}

func TestPatchSettingsAppliesSSRFPolicyToGlobalProxyURL(t *testing.T) {
	settings := &fakeSettings{values: map[string]string{}}
	handler, _, store := newAPI(t, settings)
	defer store.Close()
	session, err := handler.opts.Sessions.Create(adminauth.Account{ID: "a1", Username: "operator"})
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	handler.Attach(mux)
	request := httptest.NewRequest(http.MethodPatch, "/admin/v1/settings", strings.NewReader(`{"set":{"outboundProxyUrl":"http://127.0.0.1:3128"}}`))
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusBadRequest || settings.values["outboundProxyUrl"] != "" {
		t.Fatalf("private proxy status=%d settings=%v", recorder.Code, settings.values)
	}
	handler.opts.AllowPrivateUpstreams = true
	request = httptest.NewRequest(http.MethodPatch, "/admin/v1/settings", strings.NewReader(`{"set":{"outboundProxyUrl":"http://127.0.0.1:3128"}}`))
	request.AddCookie(&http.Cookie{Name: sessionCookieName, Value: session.ID})
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK || settings.values["outboundProxyUrl"] != "http://127.0.0.1:3128" {
		t.Fatalf("trusted-local proxy status=%d settings=%v body=%s", recorder.Code, settings.values, recorder.Body.String())
	}
}

func TestSafeSettingsRedactsURLCredentialsAndSecretValues(t *testing.T) {
	got := safeSettings(map[string]string{
		"outboundProxyUrl": "http://operator:pass@proxy.example:8080/path?token=abc&mode=fast#frag",
		"apiKey":           "raw-key",
		"rtkEnabled":       "true",
	})
	if got["outboundProxyUrl"] != "http://proxy.example:8080/path" {
		t.Fatalf("proxy URL was not sanitized: %q", got["outboundProxyUrl"])
	}
	if got["apiKey"] != "[redacted]" || got["rtkEnabled"] != "true" {
		t.Fatalf("settings=%v", got)
	}
}

func TestLoginThrottleBlocksBruteForce(t *testing.T) {
	handler, _, store := newAPI(t, &fakeSettings{values: map[string]string{}})
	defer store.Close()
	mux := http.NewServeMux()
	handler.Attach(mux)
	blocked := false
	for i := 0; i < loginMaxFailures+2; i++ {
		request := httptest.NewRequest(http.MethodPost, "/admin/v1/auth/login", strings.NewReader(`{"username":"operator","password":"wrong"}`))
		request.RemoteAddr = "203.0.113.7:1234"
		recorder := httptest.NewRecorder()
		mux.ServeHTTP(recorder, request)
		if recorder.Code == http.StatusTooManyRequests {
			blocked = true
		}
	}
	if !blocked {
		t.Fatal("repeated failed logins were never throttled")
	}
	// A different client address is unaffected.
	other := httptest.NewRequest(http.MethodPost, "/admin/v1/auth/login", strings.NewReader(`{"username":"operator","password":"s3cret"}`))
	other.RemoteAddr = "198.51.100.9:5555"
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, other)
	if recorder.Code != http.StatusOK {
		t.Fatalf("other client status=%d want 200", recorder.Code)
	}
}

func TestCrossSiteMutationsRejected(t *testing.T) {
	handler, _, store := newAPI(t, &fakeSettings{values: map[string]string{}})
	defer store.Close()
	mux := http.NewServeMux()
	handler.Attach(mux)
	// Cross-site login is rejected before any credential check.
	request := httptest.NewRequest(http.MethodPost, "/admin/v1/auth/login", strings.NewReader(`{"username":"operator","password":"s3cret"}`))
	request.Header.Set("Sec-Fetch-Site", "cross-site")
	recorder := httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("cross-site login status=%d want 403", recorder.Code)
	}
	// Mismatched Origin host is also rejected.
	request = httptest.NewRequest(http.MethodPost, "/admin/v1/auth/login", strings.NewReader(`{"username":"operator","password":"s3cret"}`))
	request.Header.Set("Origin", "https://evil.example")
	recorder = httptest.NewRecorder()
	mux.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusForbidden {
		t.Fatalf("mismatched-origin status=%d want 403", recorder.Code)
	}
}
