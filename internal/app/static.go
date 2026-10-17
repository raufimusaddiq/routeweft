package app

import (
	"errors"
	"io/fs"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
)

// staticTypes covers extensions the Go builtin MIME table lacks. The runtime
// image is FROM scratch, so there is no /etc/mime.types to fall back on.
var staticTypes = map[string]string{
	".css":         "text/css; charset=utf-8",
	".html":        "text/html; charset=utf-8",
	".ico":         "image/x-icon",
	".js":          "text/javascript; charset=utf-8",
	".json":        "application/json",
	".map":         "application/json",
	".mjs":         "text/javascript; charset=utf-8",
	".png":         "image/png",
	".svg":         "image/svg+xml",
	".txt":         "text/plain; charset=utf-8",
	".webmanifest": "application/manifest+json",
	".woff":        "font/woff",
	".woff2":       "font/woff2",
}

// staticUI serves the built Vite UI (BDR-003). The UI uses hash routes, so the
// browser only ever requests "/" for a page; every other path must be a real
// built file and anything else is a 404 rather than an index.html fallback.
type staticUI struct {
	root string
}

// newStaticUI returns nil when dir is empty (API-only deployment, e.g. Caddy
// serves the assets) and an error when dir has no index.html, so a mis-pointed
// ROUTEWEFT_UI_DIR fails at startup rather than serving 404s.
func newStaticUI(dir string) (*staticUI, error) {
	if dir == "" {
		return nil, nil
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(filepath.Join(abs, "index.html"))
	if err != nil {
		return nil, errors.New("ui dir has no index.html: " + err.Error())
	}
	if info.IsDir() {
		return nil, errors.New("ui dir index.html is a directory")
	}
	return &staticUI{root: abs}, nil
}

func (s *staticUI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	name := "index.html"
	if r.URL.Path != "/" {
		clean := path.Clean("/" + strings.TrimPrefix(r.URL.Path, "/"))
		if clean != r.URL.Path || strings.Contains(r.URL.Path, "\\") {
			http.NotFound(w, r)
			return
		}
		name = strings.TrimPrefix(clean, "/")
	}
	if !fs.ValidPath(name) {
		http.NotFound(w, r)
		return
	}
	file, err := os.DirFS(s.root).Open(name)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() {
		http.NotFound(w, r)
		return
	}
	seeker, ok := file.(interface {
		Read([]byte) (int, error)
		Seek(int64, int) (int64, error)
	})
	if !ok {
		http.NotFound(w, r)
		return
	}
	ext := strings.ToLower(path.Ext(name))
	ctype := staticTypes[ext]
	if ctype == "" {
		ctype = mime.TypeByExtension(ext)
	}
	if ctype == "" {
		ctype = "application/octet-stream"
	}
	h := w.Header()
	h.Set("Content-Type", ctype)
	h.Set("X-Content-Type-Options", "nosniff")
	if name == "index.html" {
		// The shell references hashed bundles; it must revalidate to pick up deploys.
		h.Set("Cache-Control", "no-cache")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "same-origin")
	} else if strings.HasPrefix(name, "assets/") {
		h.Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		h.Set("Cache-Control", "no-cache")
	}
	http.ServeContent(w, r, name, info.ModTime(), seeker)
}
