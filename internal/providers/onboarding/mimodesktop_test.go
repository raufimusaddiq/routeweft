package onboarding

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// #296: MiMo Desktop's files are found where the pin looks for them and read as
// it reads them, from a fixture home; nothing here touches the real one.

// writeMimoCookieStore writes a Chromium-shaped cookie database at path. Each row
// is host_key, name, value; encrypted_value is set as modern Chromium does when
// value is empty.
func writeMimoCookieStore(t *testing.T, path string, rows [][3]string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(`CREATE TABLE cookies (creation_utc INTEGER NOT NULL DEFAULT 0, host_key TEXT NOT NULL, name TEXT NOT NULL, value TEXT NOT NULL, encrypted_value BLOB NOT NULL DEFAULT x'', path TEXT NOT NULL DEFAULT '/')`); err != nil {
		t.Fatal(err)
	}
	for _, row := range rows {
		encrypted := []byte{}
		if row[2] == "" {
			encrypted = []byte("v10-fixture-encrypted")
		}
		if _, err := db.Exec(`INSERT INTO cookies (host_key, name, value, encrypted_value) VALUES (?, ?, ?, ?)`, row[0], row[1], row[2], encrypted); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMimoDesktopPathsAreThePins(t *testing.T) {
	home := filepath.FromSlash("/home/fixture")
	cases := []struct {
		desktop MimoDesktop
		auth    []string
		cookies string
	}{
		{MimoDesktop{Home: home, GOOS: "linux"},
			[]string{filepath.Join(home, ".local/share/mimocode/auth.json")},
			filepath.Join(home, ".config/Xiaomi MiMo/Partitions/xiaomi-account/Network/Cookies")},
		{MimoDesktop{Home: home, GOOS: "darwin"},
			[]string{filepath.Join(home, ".local/share/mimocode/auth.json"), filepath.Join(home, "Library/Application Support/mimocode/auth.json")},
			filepath.Join(home, "Library/Application Support/Xiaomi MiMo/Partitions/xiaomi-account/Network/Cookies")},
		{MimoDesktop{Home: home, GOOS: "windows"},
			[]string{filepath.Join(home, ".local/share/mimocode/auth.json"), filepath.Join(home, "AppData/Roaming/Xiaomi MiMo/auth.json")},
			filepath.Join(home, "AppData/Roaming/Xiaomi MiMo/Partitions/xiaomi-account/Network/Cookies")},
		// %APPDATA% moves the key file only; the pin builds the cookie path from
		// the home directory (mimoAccount.js:38-40).
		{MimoDesktop{Home: home, GOOS: "windows", AppData: filepath.FromSlash("/roaming")},
			[]string{filepath.Join(home, ".local/share/mimocode/auth.json"), filepath.FromSlash("/roaming/Xiaomi MiMo/auth.json")},
			filepath.Join(home, "AppData/Roaming/Xiaomi MiMo/Partitions/xiaomi-account/Network/Cookies")},
		{MimoDesktop{GOOS: "linux"}, nil, ""},
	}
	for _, c := range cases {
		if got := c.desktop.AuthFilePaths(); !reflect.DeepEqual(got, c.auth) {
			t.Errorf("%+v auth paths = %q, want %q", c.desktop, got, c.auth)
		}
		if got := c.desktop.CookieStorePath(); got != c.cookies {
			t.Errorf("%+v cookie store = %q, want %q", c.desktop, got, c.cookies)
		}
	}
}

func TestMimoDesktopReadsTheFirstReadableAuthFile(t *testing.T) {
	home := t.TempDir()
	desktop := MimoDesktop{Home: home, GOOS: "darwin"}
	if _, _, err := desktop.ReadAuthFile(1 << 10); !errors.Is(err, ErrMimoAuthFileNotFound) {
		t.Fatalf("empty home: %v", err)
	}
	mac := filepath.Join(home, "Library/Application Support/mimocode/auth.json")
	if err := os.MkdirAll(filepath.Dir(mac), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(mac, []byte(`{"xiaomi":{"key":"sk-mac"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	path, raw, err := desktop.ReadAuthFile(1 << 10)
	if err != nil || path != mac || string(raw) != `{"xiaomi":{"key":"sk-mac"}}` {
		t.Fatalf("mac only: %q %q %v", path, raw, err)
	}
	// The shared XDG path comes first, as in the pin's list.
	xdg := filepath.Join(home, ".local/share/mimocode/auth.json")
	if err := os.MkdirAll(filepath.Dir(xdg), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(xdg, []byte(`{"xiaomi":{"key":"sk-xdg"}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if path, _, err := desktop.ReadAuthFile(1 << 10); err != nil || path != xdg {
		t.Fatalf("both: %q %v", path, err)
	}
	if _, raw, err := desktop.ReadAuthFile(8); !errors.Is(err, ErrMimoAuthFileTooLarge) || raw != nil {
		t.Fatalf("past the bound: %q %v", raw, err)
	}
}

func TestMimoDesktopReadsTheAccountSessionFromACopy(t *testing.T) {
	home, temp := t.TempDir(), t.TempDir()
	desktop := MimoDesktop{Home: home, GOOS: "linux", TempDir: temp}
	if session := desktop.ReadSession(context.Background()); session != nil {
		t.Fatalf("no store: %v", session)
	}
	store := desktop.CookieStorePath()
	writeMimoCookieStore(t, store, [][3]string{
		{".account.xiaomi.com", "passToken", "fixture-pass"},
		{".account.xiaomi.com", "userId", "fixture-user"},
		{".account.xiaomi.com", "cUserId", "fixture-cuser"},
		{".account.xiaomi.com", "deviceId", "fixture-device"},
		{"account.xiaomi.com", "passToken", "host-only-pass"},
		{".xiaomi.com", "passToken", "other-host-pass"},
	})
	before, err := os.ReadFile(store)
	if err != nil {
		t.Fatal(err)
	}
	session := desktop.ReadSession(context.Background())
	want := map[string]string{"mimoPassToken": "fixture-pass", "mimoUserId": "fixture-user", "mimoCUserId": "fixture-cuser"}
	if !reflect.DeepEqual(session, want) {
		t.Fatalf("session = %v, want %v", session, want)
	}
	after, err := os.ReadFile(store)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatalf("the store changed: %v", err)
	}
	if left, _ := os.ReadDir(temp); len(left) != 0 {
		t.Fatalf("the copy was left behind: %v", left)
	}
}

// The pin returns null for each of these and the caller imports the key alone.
func TestMimoDesktopSessionIsAbsentWhenItCannotBeUsed(t *testing.T) {
	cases := map[string][][3]string{
		"no passToken":  {{".account.xiaomi.com", "userId", "fixture-user"}},
		"empty value":   {{".account.xiaomi.com", "passToken", ""}},
		"cookie syntax": {{".account.xiaomi.com", "passToken", "fixture;pass"}},
		"bad userId":    {{".account.xiaomi.com", "passToken", "fixture-pass"}, {".account.xiaomi.com", "userId", "a b"}},
		"another host":  {{".mi.com", "passToken", "fixture-pass"}},
	}
	for name, rows := range cases {
		t.Run(name, func(t *testing.T) {
			desktop := MimoDesktop{Home: t.TempDir(), GOOS: "linux", TempDir: t.TempDir()}
			writeMimoCookieStore(t, desktop.CookieStorePath(), rows)
			if session := desktop.ReadSession(context.Background()); session != nil {
				t.Fatalf("session = %v, want none", session)
			}
		})
	}
	t.Run("not a database", func(t *testing.T) {
		desktop := MimoDesktop{Home: t.TempDir(), GOOS: "linux", TempDir: t.TempDir()}
		if err := os.MkdirAll(filepath.Dir(desktop.CookieStorePath()), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(desktop.CookieStorePath(), []byte("not sqlite"), 0o600); err != nil {
			t.Fatal(err)
		}
		if session := desktop.ReadSession(context.Background()); session != nil {
			t.Fatalf("session = %v", session)
		}
	})
	t.Run("no temporary directory", func(t *testing.T) {
		desktop := MimoDesktop{Home: t.TempDir(), GOOS: "linux", TempDir: filepath.Join(t.TempDir(), "missing")}
		writeMimoCookieStore(t, desktop.CookieStorePath(), [][3]string{{".account.xiaomi.com", "passToken", "fixture-pass"}})
		if session := desktop.ReadSession(context.Background()); session != nil {
			t.Fatalf("session = %v", session)
		}
	})
}
