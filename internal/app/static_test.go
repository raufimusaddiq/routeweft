package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func uiFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		full := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("index.html", "<!doctype html><div id=root></div>")
	write("assets/index-abc123.js", "console.log(1)")
	write("assets/index-abc123.css", "body{}")
	write("assets/font-abc.woff2", "font")
	write("favicon.svg", "<svg/>")
	return dir
}

func getUI(t *testing.T, h http.Handler, method, target string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(method, target, nil))
	return rr
}

func TestStaticUIServesAssets(t *testing.T) {
	h := New(Config{UIDir: uiFixture(t)}, nil).Handler()
	cases := []struct {
		path, ctype, cache string
	}{
		{"/", "text/html; charset=utf-8", "no-cache"},
		{"/index.html", "", ""}, // redirect handled by ServeContent
		{"/assets/index-abc123.js", "text/javascript; charset=utf-8", "public, max-age=31536000, immutable"},
		{"/assets/index-abc123.css", "text/css; charset=utf-8", "public, max-age=31536000, immutable"},
		{"/assets/font-abc.woff2", "font/woff2", "public, max-age=31536000, immutable"},
		{"/favicon.svg", "image/svg+xml", "no-cache"},
	}
	for _, c := range cases {
		rr := getUI(t, h, http.MethodGet, c.path)
		if c.ctype == "" {
			continue
		}
		if rr.Code != http.StatusOK {
			t.Fatalf("%s: got %d", c.path, rr.Code)
		}
		if got := rr.Header().Get("Content-Type"); got != c.ctype {
			t.Errorf("%s: content-type %q, want %q", c.path, got, c.ctype)
		}
		if got := rr.Header().Get("Cache-Control"); got != c.cache {
			t.Errorf("%s: cache-control %q, want %q", c.path, got, c.cache)
		}
		if rr.Header().Get("X-Content-Type-Options") != "nosniff" {
			t.Errorf("%s: missing nosniff", c.path)
		}
	}
}

func TestStaticUIHashRouteServesIndex(t *testing.T) {
	// The fragment never reaches the server: every hash route is a request for "/".
	h := New(Config{UIDir: uiFixture(t)}, nil).Handler()
	for _, method := range []string{http.MethodGet, http.MethodHead} {
		rr := getUI(t, h, method, "/?next=1")
		if rr.Code != http.StatusOK {
			t.Fatalf("%s /: %d", method, rr.Code)
		}
		if method == http.MethodGet && !strings.Contains(rr.Body.String(), `id=root`) {
			t.Fatalf("GET / did not return index.html: %q", rr.Body.String())
		}
	}
}

// Unknown paths are 404s and never index.html. A path with dot segments or
// repeated slashes is answered by net/http.ServeMux before any handler runs: since
// Go 1.22 (go.mod) it redirects to the cleaned path with 307, for every route and
// not only the UI. That is the contract asserted here: the redirect stays on this
// origin, names the cleaned path, and the cleaned path is itself a 404 (#206).
func TestStaticUIUnknownPathsAre404(t *testing.T) {
	h := New(Config{UIDir: uiFixture(t)}, nil).Handler()
	for _, c := range []struct {
		path     string
		code     int
		location string
	}{
		{path: "/assets/missing.js", code: http.StatusNotFound},
		{path: "/overview", code: http.StatusNotFound},
		{path: "/assets/", code: http.StatusNotFound},
		{path: "/assets", code: http.StatusNotFound},
		{path: "/missing.png", code: http.StatusNotFound},
		{path: "/admin/v1/nope", code: http.StatusNotFound},
		{path: "/v1/nope", code: http.StatusNotFound},
		{path: "/api/nope", code: http.StatusNotFound},
		// Encoded dot segments are not canonicalized by the mux; the handler refuses them.
		{path: "/%2e%2e/etc/passwd", code: http.StatusNotFound},
		{path: "/assets/%2e%2e/index.html", code: http.StatusNotFound},
		{path: "/assets/..%2findex.html", code: http.StatusNotFound},
		{path: "/../etc/passwd", code: http.StatusTemporaryRedirect, location: "/etc/passwd"},
		{path: "/assets/../../etc/passwd", code: http.StatusTemporaryRedirect, location: "/etc/passwd"},
		{path: "//etc/passwd", code: http.StatusTemporaryRedirect, location: "/etc/passwd"},
	} {
		rr := getUI(t, h, http.MethodGet, c.path)
		if rr.Code != c.code {
			t.Errorf("%s: got %d, want %d", c.path, rr.Code, c.code)
		}
		if got := rr.Header().Get("Location"); got != c.location {
			t.Errorf("%s: location %q, want %q", c.path, got, c.location)
		}
		if strings.Contains(rr.Body.String(), "id=root") {
			t.Errorf("%s: leaked index.html", c.path)
		}
		if c.location == "" {
			continue
		}
		next := getUI(t, h, http.MethodGet, c.location)
		if next.Code != http.StatusNotFound || strings.Contains(next.Body.String(), "id=root") {
			t.Errorf("%s -> %s: got %d, want 404", c.path, c.location, next.Code)
		}
	}
}

// A file beside the UI root is never served, whatever spelling of ".." reaches
// the server and however many same-origin redirects a client follows.
func TestStaticUITraversalNeverReadsOutsideRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "ui")
	if err := os.MkdirAll(filepath.Join(root, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte("<div id=root></div>"), 0o644); err != nil {
		t.Fatal(err)
	}
	const secret = "outside-ui-root-sentinel"
	if err := os.WriteFile(filepath.Join(parent, "secret.txt"), []byte(secret), 0o644); err != nil {
		t.Fatal(err)
	}
	h := New(Config{UIDir: root}, nil).Handler()
	for _, p := range []string{
		"/../secret.txt", "/assets/../../secret.txt", "/./../secret.txt", "//../secret.txt",
		"/%2e%2e/secret.txt", "/%2E%2E/secret.txt", "/..%2fsecret.txt", "/%2e%2e%2fsecret.txt",
		"/assets/..%2f..%2fsecret.txt", "/..%5csecret.txt", "/..\\secret.txt", "/ui/../secret.txt",
	} {
		target := p
		for hop := 0; ; hop++ {
			rr := getUI(t, h, http.MethodGet, target)
			if strings.Contains(rr.Body.String(), secret) || strings.Contains(rr.Body.String(), "id=root") {
				t.Fatalf("%s: served %q", p, rr.Body.String())
			}
			if rr.Code != http.StatusTemporaryRedirect {
				if rr.Code != http.StatusNotFound {
					t.Errorf("%s: final %d, want 404", p, rr.Code)
				}
				break
			}
			loc := rr.Header().Get("Location")
			if !strings.HasPrefix(loc, "/") || strings.HasPrefix(loc, "//") || hop == 3 {
				t.Fatalf("%s: redirect %q leaves the origin or loops", p, loc)
			}
			target = loc
		}
	}
}

// The handler refuses unclean paths itself, so the protection does not depend on
// the mux canonicalizing first (e.g. if the UI is mounted under another router).
func TestStaticUIHandlerRejectsUncleanPaths(t *testing.T) {
	ui, err := newStaticUI(uiFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{
		"/../index.html", "/assets/../index.html", "/./index.html", "//index.html",
		"/assets/", "/index.html/", "/..\\index.html", "/../../etc/passwd",
	} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r.URL.Path = p
		rr := httptest.NewRecorder()
		ui.ServeHTTP(rr, r)
		if rr.Code != http.StatusNotFound || strings.Contains(rr.Body.String(), "id=root") {
			t.Errorf("%s: got %d %q, want 404", p, rr.Code, rr.Body.String())
		}
	}
}

func TestStaticUINonGetMethodsDoNotReachUI(t *testing.T) {
	h := New(Config{UIDir: uiFixture(t)}, nil).Handler()
	rr := getUI(t, h, http.MethodPost, "/")
	if strings.Contains(rr.Body.String(), "id=root") {
		t.Fatal("POST / served index.html")
	}
}

func TestStaticUIDisabledByDefault(t *testing.T) {
	rr := getUI(t, New(Config{}, nil).Handler(), http.MethodGet, "/")
	if rr.Code != http.StatusNotFound {
		t.Fatalf("got %d, want 404 with no UI dir", rr.Code)
	}
}

func TestStaticUIRequiresIndex(t *testing.T) {
	if _, err := newStaticUI(t.TempDir()); err == nil {
		t.Fatal("expected error for dir without index.html")
	}
	// Health stays available when the UI dir is misconfigured.
	rr := getUI(t, New(Config{UIDir: t.TempDir()}, nil).Handler(), http.MethodGet, "/health/live")
	if rr.Code != http.StatusOK {
		t.Fatalf("health: %d", rr.Code)
	}
}
