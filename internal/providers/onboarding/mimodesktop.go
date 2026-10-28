package onboarding

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"

	"github.com/raufimusaddiq/routeweft/internal/credentials"

	_ "modernc.org/sqlite"
)

// MimoDesktop reads the Xiaomi MiMo credentials MiMo Desktop and MiMoCode keep in
// the server user's home directory: the key file the pin's auto-import reads
// (src/app/api/oauth/xiaomi-mimo/auto-import/route.js:25-47,52-104) and the
// account-session cookies in Desktop's Chromium cookie store
// (open-sse/shared/mimoAccount.js:36-94). These are host secrets, so nothing calls
// it on its own: the control API reads them only when the host was started with
// host credential discovery allowed and the operator consents to that read in the
// request that makes it (#296).
type MimoDesktop struct {
	// Home is the server user's home directory; empty finds nothing.
	Home string
	// GOOS picks the platform's paths, as process.platform does in the pin.
	GOOS string
	// AppData is %APPDATA% on Windows; empty is Home/AppData/Roaming, the pin's
	// fallback (auto-import/route.js:34).
	AppData string
	// TempDir receives the cookie store's private copy; empty is os.TempDir().
	TempDir string
}

// ErrMimoAuthFileNotFound means no candidate key file is readable.
var ErrMimoAuthFileNotFound = errors.New("xiaomi-mimo auth file not found")

// ErrMimoAuthFileTooLarge means the key file is past the import bound.
var ErrMimoAuthFileTooLarge = errors.New("xiaomi-mimo auth file is too large")

// mimoAccountCookieHost is the cookie host the pin reads (mimoAccount.js:25,65).
const mimoAccountCookieHost = ".account.xiaomi.com"

// mimoCookieStoreLimit bounds the cookie store copy. The pin copies it whole; a
// bound keeps a stray file from filling the temporary directory.
const mimoCookieStoreLimit = 64 << 20

// AuthFilePaths is the pin's candidate list, in its order
// (auto-import/route.js:25-47).
func (d MimoDesktop) AuthFilePaths() []string {
	if d.Home == "" {
		return nil
	}
	paths := []string{filepath.Join(d.Home, ".local", "share", "mimocode", "auth.json")}
	switch d.GOOS {
	case "windows":
		appData := d.AppData
		if appData == "" {
			appData = filepath.Join(d.Home, "AppData", "Roaming")
		}
		paths = append(paths, filepath.Join(appData, "Xiaomi MiMo", "auth.json"))
	case "darwin":
		paths = append(paths, filepath.Join(d.Home, "Library", "Application Support", "mimocode", "auth.json"))
	}
	return paths
}

// CookieStorePath is Desktop's account cookie database (mimoAccount.js:36-45).
func (d MimoDesktop) CookieStorePath() string {
	if d.Home == "" {
		return ""
	}
	tail := []string{"Xiaomi MiMo", "Partitions", "xiaomi-account", "Network", "Cookies"}
	switch d.GOOS {
	case "windows":
		return filepath.Join(append([]string{d.Home, "AppData", "Roaming"}, tail...)...)
	case "darwin":
		return filepath.Join(append([]string{d.Home, "Library", "Application Support"}, tail...)...)
	}
	return filepath.Join(append([]string{d.Home, ".config"}, tail...)...)
}

// ReadAuthFile returns the first readable candidate and its contents, at most
// limit bytes. The pin takes the first path it may read and reads only that one
// (auto-import/route.js:56-74).
func (d MimoDesktop) ReadAuthFile(limit int64) (string, []byte, error) {
	for _, path := range d.AuthFilePaths() {
		file, err := os.Open(path)
		if err != nil {
			continue
		}
		defer file.Close()
		raw, err := io.ReadAll(io.LimitReader(file, limit+1))
		if err != nil {
			return path, nil, fmt.Errorf("xiaomi-mimo auth file could not be read")
		}
		if int64(len(raw)) > limit {
			return path, nil, ErrMimoAuthFileTooLarge
		}
		return path, raw, nil
	}
	return "", nil, ErrMimoAuthFileNotFound
}

// ReadSession returns the account-session fields as the import names them
// (mimoPassToken, mimoUserId, mimoCUserId), or nil when Desktop's store is
// missing, locked, unreadable or holds no passToken. Like the pin it never fails
// the caller (mimoAccount.js:53-94): the store is copied first, because a running
// Desktop holds it locked, and a value that could not travel in a Cookie header
// drops the session rather than being stored.
func (d MimoDesktop) ReadSession(ctx context.Context) map[string]string {
	jar := d.readAccountCookies(ctx)
	if jar["passToken"] == "" {
		return nil
	}
	session := map[string]string{"mimoPassToken": jar["passToken"]}
	for name, key := range map[string]string{"userId": "mimoUserId", "cUserId": "mimoCUserId"} {
		if jar[name] != "" {
			session[key] = jar[name]
		}
	}
	for _, value := range session {
		if !credentials.ValidMimoCookieValue(value) {
			return nil
		}
	}
	return session
}

// readAccountCookies is the pin's readDesktopAccountCookies: a private copy of
// the store, read only, removed afterwards; the later of two same-named rows
// wins, as Object.fromEntries does.
func (d MimoDesktop) readAccountCookies(ctx context.Context) map[string]string {
	source := d.CookieStorePath()
	if source == "" {
		return nil
	}
	in, err := os.Open(source)
	if err != nil {
		return nil
	}
	defer in.Close()
	tempDir := d.TempDir
	if tempDir == "" {
		tempDir = os.TempDir()
	}
	// CreateTemp makes the copy readable by this user alone.
	copyFile, err := os.CreateTemp(tempDir, "routeweft-mimo-cookies-*.db")
	if err != nil {
		return nil
	}
	copyPath := copyFile.Name()
	defer os.Remove(copyPath)
	written, err := io.Copy(copyFile, io.LimitReader(in, mimoCookieStoreLimit+1))
	if closeErr := copyFile.Close(); err != nil || closeErr != nil || written > mimoCookieStoreLimit {
		return nil
	}
	db, err := sql.Open("sqlite", (&url.URL{Scheme: "file", Path: copyPath, RawQuery: "mode=ro"}).String())
	if err != nil {
		return nil
	}
	defer db.Close()
	rows, err := db.QueryContext(ctx, "SELECT name, value FROM cookies WHERE host_key = ?", mimoAccountCookieHost)
	if err != nil {
		return nil
	}
	defer rows.Close()
	jar := map[string]string{}
	for rows.Next() {
		var name, value sql.NullString
		if rows.Scan(&name, &value) != nil {
			return nil
		}
		jar[name.String] = value.String
	}
	if rows.Err() != nil {
		return nil
	}
	return jar
}
