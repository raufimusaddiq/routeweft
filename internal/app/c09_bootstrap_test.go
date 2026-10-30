package app

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/raufimusaddiq/routeweft/internal/store/storetest"
)

// C09 bootstrap proof. SPEC 12.2 / RUNBOOK bootstrap: the first admin is created
// from ROUTEWEFT_BOOTSTRAP_ADMIN_* only while admin_users is empty, only at
// startup, never leaves the password anywhere but a salted hash, and a later
// boot never re-provisions or overwrites it.

const c09BootstrapPassword = "c09-bootstrap-secret-value"

func c09Login(a *App, username, password string) int {
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin/v1/auth/login", strings.NewReader(`{"username":"`+username+`","password":"`+password+`"}`)))
	return w.Code
}

// c09Boot starts an app on dir with the given bootstrap pair and closes its
// store when the test ends (or earlier through the returned func).
func c09Boot(t *testing.T, dir, username, password string, logger *slog.Logger) (*App, func()) {
	t.Helper()
	a := New(Config{DataDir: dir, AdminBootstrapUsername: username, AdminBootstrapPassword: password}, logger)
	if err := a.Initialize(context.Background()); err != nil {
		t.Fatal(err)
	}
	closed := false
	closeStore := func() {
		if !closed {
			closed = true
			_ = a.store.Close()
		}
	}
	t.Cleanup(closeStore)
	return a, closeStore
}

func TestC09BootstrapPasswordNeverLeavesTheHash(t *testing.T) {
	var logged bytes.Buffer
	dir := storetest.DataDir(t)
	a, closeStore := c09Boot(t, dir, "operator", c09BootstrapPassword, slog.New(slog.NewTextHandler(&logged, &slog.HandlerOptions{Level: slog.LevelDebug})))

	var stored string
	if err := a.store.DB().QueryRow("SELECT password_hash FROM admin_users").Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(stored, "pbkdf2-sha256$600000$") || strings.Contains(stored, c09BootstrapPassword) {
		t.Fatalf("stored credential is not a salted PBKDF2 hash: %q", stored)
	}
	if !strings.Contains(logged.String(), "provisioned initial admin account") {
		t.Fatalf("provisioning is not logged: %s", logged.String())
	}

	// Log in, then read everything an operator can read back: the console log
	// buffer API and the login response.
	w := httptest.NewRecorder()
	a.Handler().ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/admin/v1/auth/login", strings.NewReader(`{"username":"operator","password":"`+c09BootstrapPassword+`"}`)))
	if w.Code != http.StatusOK || len(w.Result().Cookies()) == 0 {
		t.Fatalf("login %d %s", w.Code, w.Body)
	}
	cookie := w.Result().Cookies()[0]
	surfaces := map[string]string{"login response": w.Body.String(), "logger output": logged.String()}
	logsRequest := httptest.NewRequest(http.MethodGet, "/admin/v1/logs?limit=500", nil)
	logsRequest.AddCookie(cookie)
	logsResponse := c03Do(a, logsRequest)
	if logsResponse.Code != http.StatusOK {
		t.Fatalf("logs %d %s", logsResponse.Code, logsResponse.Body)
	}
	surfaces["console logs API"] = logsResponse.Body.String()

	// The database files themselves (main + WAL) must not hold the plaintext.
	closeStore()
	for _, name := range []string{"routeweft.sqlite", "routeweft.sqlite-wal"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			t.Fatal(err)
		}
		surfaces[name] = string(data)
	}
	for name, content := range surfaces {
		if strings.Contains(content, c09BootstrapPassword) {
			t.Fatalf("bootstrap password found in %s", name)
		}
	}
}

func TestC09BootstrapActsOnlyWhileNoAdminExists(t *testing.T) {
	dir := storetest.DataDir(t)
	_, closeFirst := c09Boot(t, dir, "operator", c09BootstrapPassword, nil)
	closeFirst()

	// A later boot with different bootstrap values must not add, replace or
	// reset anything: the original credential still works, the new one does not.
	second, closeSecond := c09Boot(t, dir, "intruder", "c09-replacement-secret", nil)
	if code := c09Login(second, "operator", c09BootstrapPassword); code != http.StatusOK {
		t.Fatalf("original admin lost after a re-bootstrap attempt: %d", code)
	}
	if code := c09Login(second, "intruder", "c09-replacement-secret"); code == http.StatusOK {
		t.Fatal("a later boot provisioned a second admin from the environment")
	}
	if code := c09Login(second, "operator", "c09-replacement-secret"); code == http.StatusOK {
		t.Fatal("a later boot overwrote the existing admin's password")
	}
	var rows int
	if err := second.store.DB().QueryRow("SELECT COUNT(*) FROM admin_users").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("admin rows=%d err=%v", rows, err)
	}
	closeSecond()

	// And with no bootstrap environment at all, the existing admin still logs in.
	third, _ := c09Boot(t, dir, "", "", nil)
	if code := c09Login(third, "operator", c09BootstrapPassword); code != http.StatusOK {
		t.Fatalf("existing admin cannot log in without the bootstrap environment: %d", code)
	}
}

func TestC09BootstrapRefusalLeavesTheInstallLockedAndNotReady(t *testing.T) {
	// A whitespace-only username is not a username: startup fails, no admin is
	// created, and the instance never reports ready.
	a := New(Config{DataDir: storetest.DataDir(t), AdminBootstrapUsername: " \t ", AdminBootstrapPassword: c09BootstrapPassword}, nil)
	if err := a.Initialize(context.Background()); err == nil {
		_ = a.store.Close()
		t.Fatal("startup succeeded with an unusable bootstrap username")
	}
	if a.ready.Load() {
		t.Fatal("instance reported ready after a failed bootstrap")
	}

	// A surrounding-whitespace username is trimmed to the name the operator types.
	trimmed, _ := c09Boot(t, storetest.DataDir(t), "  operator  ", c09BootstrapPassword, nil)
	if code := c09Login(trimmed, "operator", c09BootstrapPassword); code != http.StatusOK {
		t.Fatalf("trimmed bootstrap username cannot log in: %d", code)
	}
	if !trimmed.ready.Load() {
		t.Fatal("instance not ready after a successful bootstrap")
	}
}

func TestC09FreshDataDirectoryIsCreatedPrivate(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "fresh", "data")
	a, closeStore := c09Boot(t, dir, "operator", c09BootstrapPassword, nil)
	defer closeStore()
	for path, want := range map[string]os.FileMode{dir: 0o700, filepath.Join(dir, "routeweft.sqlite"): 0o600} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != want {
			t.Fatalf("%s mode %o, want %o", path, got, want)
		}
	}
	if !a.ready.Load() {
		t.Fatal("instance not ready after bootstrapping a fresh data directory")
	}
}
