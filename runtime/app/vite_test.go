package app

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"testing/fstest"
)

// TestViteApp: a Vite client build is served as vite preview serves it:
// files, then Vite's HTML fallback (x.html, x/index.html, the SPA's
// index.html) for HTML requests, then an empty 404; the base is stripped.
func TestViteApp(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	payload := func(appType, base string) fstest.MapFS {
		return fstest.MapFS{
			MetaFile:                       {Data: []byte(fmt.Sprintf(`{"format":%d,"framework":"vite","appType":%q,"basePath":%q}`, FormatVersion, appType, base))},
			ClientDir + "/index.html":      {Data: []byte("<p>index</p>")},
			ClientDir + "/about.html":      {Data: []byte("<p>about</p>")},
			ClientDir + "/docs/index.html": {Data: []byte("<p>docs</p>")},
			ClientDir + "/blog/index.html": {Data: []byte("<p>blog</p>")},
			ClientDir + "/assets/a-1.js":   {Data: []byte("export{}")},
			ClientDir + "/robots.txt":      {Data: []byte("ok")},
		}
	}
	type req struct {
		path, accept string
		status       int
		body, cc     string
	}
	for _, c := range []struct {
		appType, base string
		reqs          []req
	}{
		{"spa", "", []req{
			{"/", "text/html", 200, "<p>index</p>", "no-cache"},
			{"/about", "text/html", 200, "<p>about</p>", "no-cache"},
			{"/docs/", "text/html", 200, "<p>docs</p>", "no-cache"},
			{"/some/route", "text/html", 200, "<p>index</p>", "no-cache"},
			{"/some/route", "", 200, "<p>index</p>", "no-cache"},
			{"/some/route", "application/json", 404, "", ""},
			{"/assets/a-1.js", "*/*", 200, "export{}", "public, max-age=31536000, immutable"},
			{"/robots.txt", "", 200, "ok", ""},
			{"/favicon.ico", "text/html", 404, "", ""},
		}},
		{"mpa", "", []req{
			{"/about", "text/html", 200, "<p>about</p>", "no-cache"},
			// a directory-format build (Astro's /blog is blog/index.html)
			{"/blog", "text/html", 200, "<p>blog</p>", "no-cache"},
			{"/some/route", "text/html", 404, "", ""},
		}},
		{"spa", "/app", []req{
			{"/app/", "text/html", 200, "<p>index</p>", "no-cache"},
			{"/app/assets/a-1.js", "", 200, "export{}", "public, max-age=31536000, immutable"},
			{"/app/deep", "text/html", 200, "<p>index</p>", "no-cache"},
			{"/assets/a-1.js", "", 404, "404 page not found\n", ""},
		}},
	} {
		h, meta, err := Handler(payload(c.appType, c.base), logger)
		if err != nil {
			t.Fatal(err)
		}
		if meta.Framework != "vite" {
			t.Fatalf("framework %q", meta.Framework)
		}
		for _, r := range c.reqs {
			rq := httptest.NewRequest(http.MethodGet, r.path, nil)
			if r.accept != "" {
				rq.Header.Set("Accept", r.accept)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, rq)
			if rec.Code != r.status || rec.Body.String() != r.body || rec.Header().Get("Cache-Control") != r.cc {
				t.Errorf("%s base %q: GET %s (%s): %d %q cc %q, want %d %q cc %q", c.appType, c.base, r.path, r.accept, rec.Code, rec.Body.String(), rec.Header().Get("Cache-Control"), r.status, r.body, r.cc)
			}
		}
	}
}

// TestViteAppWithServer: with a server (TanStack Start's), the client
// files and the pages the build prerendered (p/index.html, p.html) are
// served as static files; everything else goes to the server.
func TestViteAppWithServer(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, _, err := Handler(fstest.MapFS{
		MetaFile:                         {Data: []byte(fmt.Sprintf(`{"format":%d,"framework":"vite","appType":"custom"}`, FormatVersion))},
		ServerFile:                       {Data: []byte(`globalThis.__ogHandler = (req) => new Response("server " + req.method + " " + new URL(req.url).pathname);`)},
		ClientDir + "/static/index.html": {Data: []byte("<p>static</p>")},
		ClientDir + "/flat.html":         {Data: []byte("<p>flat</p>")},
		ClientDir + "/assets/a-1.js":     {Data: []byte("export{}")},
	}, logger)
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range []struct{ method, path, body string }{
		{"GET", "/static", "<p>static</p>"},
		{"GET", "/static/", "<p>static</p>"},
		{"GET", "/flat", "<p>flat</p>"},
		{"GET", "/assets/a-1.js", "export{}"},
		{"GET", "/", "server GET /"},
		{"GET", "/other", "server GET /other"},
		{"POST", "/static", "server POST /static"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(r.method, r.path, nil))
		if rec.Code != 200 || rec.Body.String() != r.body {
			t.Errorf("%s %s: %d %q, want %q", r.method, r.path, rec.Code, rec.Body.String(), r.body)
		}
	}
}
