package app

import (
	"bytes"
	"io/fs"
	"net/http"
	"path"
	"strings"
	"time"
)

// Directories inside a built application.
const (
	NextDir   = "_next"  // /_next/static/... build assets
	PublicDir = "public" // files served from the site root
)

// withStatic serves build assets and public files before falling back to
// the JS handler. With a basePath, both are under it (as every URL of the
// app is); the JS handler answers other paths with 404s.
func withStatic(fsys fs.FS, basePath string, next http.Handler) http.Handler {
	return staticHandler{fsys: fsys, base: strings.TrimSuffix(basePath, "/"), next: next}
}

type staticHandler struct {
	fsys fs.FS
	base string
	next http.Handler
}

func (s staticHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	fsys, next := s.fsys, s.next
	func() {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		p := path.Clean("/" + r.URL.Path)
		if s.base != "" {
			rest, ok := strings.CutPrefix(p, s.base)
			if !ok || !strings.HasPrefix(rest, "/") {
				next.ServeHTTP(w, r)
				return
			}
			p = rest
		}
		if strings.HasPrefix(p, "/_next/static/") {
			if serveFile(w, r, fsys, NextDir+strings.TrimPrefix(p, "/_next")) {
				return
			}
			http.NotFound(w, r)
			return
		}
		if p != "/" && serveFile(w, r, fsys, PublicDir+p) {
			return
		}
		next.ServeHTTP(w, r)
	}()
}

func serveFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, name string) bool {
	st, err := fs.Stat(fsys, name)
	if err != nil || st.IsDir() {
		return false
	}
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return false
	}
	if strings.HasPrefix(name, NextDir+"/static/") {
		// Build assets are content-addressed (hashed names).
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "public, max-age=0")
	}
	http.ServeContent(w, r, name, modTime(st), bytes.NewReader(b))
	return true
}

func modTime(st fs.FileInfo) time.Time {
	if t := st.ModTime(); !t.IsZero() {
		return t
	}
	return time.Unix(0, 0)
}
