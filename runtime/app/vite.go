package app

import (
	"io/fs"
	"net/http"
	"path"
	"strings"
)

// ClientDir holds a Vite app's client build (its build.outDir).
const ClientDir = "client"

// viteHandler serves a Vite client build as `vite preview` does: the
// files of the build (after stripping the base), then, for HTML requests,
// Vite's htmlFallbackMiddleware (x.html, x/index.html, x -> x.html, and
// /index.html for an SPA), then an empty 404. With a server (next, the
// server environment's fetch handler), requests the client files do not
// answer go to it instead, after the pages the build prerendered.
func viteHandler(fsys fs.FS, meta *Meta, next http.Handler) http.Handler {
	base := strings.TrimSuffix(meta.BasePath, "/")
	spa := meta.AppType == "" || meta.AppType == "spa"
	html := spa || meta.AppType == "mpa"
	clientDir := ClientDir
	if meta.ClientDir != "" {
		clientDir = meta.ClientDir
	}
	exists := func(p string) bool {
		st, err := fs.Stat(fsys, clientDir+p)
		return err == nil && !st.IsDir()
	}
	serve := func(w http.ResponseWriter, r *http.Request, p string) bool {
		if !exists(p) {
			return false
		}
		name := clientDir + p
		if strings.HasSuffix(p, ".html") {
			w.Header().Set("Cache-Control", "no-cache")
		}
		return serveClientFile(w, r, fsys, clientDir, name)
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := r.URL.Path
		if base != "" {
			if p != base && !strings.HasPrefix(p, base+"/") {
				http.NotFound(w, r)
				return
			}
			p = strings.TrimPrefix(p, base)
			if p == "" {
				p = "/"
			}
		}
		p = path.Clean("/" + p)
		if r.URL.Path != "/" && strings.HasSuffix(r.URL.Path, "/") && p != "/" {
			p += "/"
		}
		get := r.Method == http.MethodGet || r.Method == http.MethodHead
		if get && !strings.HasSuffix(p, "/") && serve(w, r, p) {
			return
		}
		if next != nil {
			// Pages the build prerendered (TanStack Start writes /p as
			// p/index.html, or p.html without autoSubfolderIndex) are
			// static files, served as a static host serves them.
			if get && p != "/favicon.ico" {
				if strings.HasSuffix(p, "/") {
					if serve(w, r, p+"index.html") {
						return
					}
				} else if serve(w, r, p+"/index.html") || serve(w, r, p+".html") {
					return
				}
			}
			next.ServeHTTP(w, r)
			return
		}
		accept := r.Header.Get("Accept")
		wantsHTML := accept == "" || strings.Contains(accept, "text/html") || strings.Contains(accept, "*/*")
		if html && get && p != "/favicon.ico" && wantsHTML {
			switch {
			case strings.HasSuffix(p, ".html"):
				if serve(w, r, p) {
					return
				}
			case strings.HasSuffix(p, "/"):
				if serve(w, r, p+"index.html") {
					return
				}
			default:
				// x.html, then x/index.html (a directory-format multi-page
				// build: Astro's /about is about/index.html).
				if serve(w, r, p+".html") || serve(w, r, p+"/index.html") {
					return
				}
			}
			if spa && serve(w, r, "/index.html") {
				return
			}
		}
		w.WriteHeader(http.StatusNotFound)
	})
}

// serveClientFile serves a client build file; files under assets/ have
// content hashes in their names and are cached for good.
func serveClientFile(w http.ResponseWriter, r *http.Request, fsys fs.FS, clientDir, name string) bool {
	if strings.HasPrefix(name, clientDir+"/assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	}
	st, err := fs.Stat(fsys, name)
	if err != nil {
		return false
	}
	b, err := fs.ReadFile(fsys, name)
	if err != nil {
		return false
	}
	http.ServeContent(w, r, name, modTime(st), strings.NewReader(string(b)))
	return true
}
