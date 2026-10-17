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

func TestStaticUIUnknownPathsAre404(t *testing.T) {
	h := New(Config{UIDir: uiFixture(t)}, nil).Handler()
	for _, p := range []string{
		"/assets/missing.js", "/overview", "/assets/", "/assets", "/missing.png",
		"/../etc/passwd", "/assets/../../etc/passwd", "/%2e%2e/etc/passwd", "/assets/%2e%2e/index.html",
		"/admin/v1/nope", "/v1/nope", "/api/nope",
	} {
		rr := getUI(t, h, http.MethodGet, p)
		if rr.Code != http.StatusNotFound && rr.Code != http.StatusMovedPermanently && rr.Code != http.StatusBadRequest {
			t.Errorf("%s: got %d, want 404", p, rr.Code)
		}
		if strings.Contains(rr.Body.String(), "id=root") {
			t.Errorf("%s: leaked index.html", p)
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
