package next

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/moriyoshi/oganesson/runtime/app"
)

// TestTrailingSlash: next.config trailingSlash (testdata/apps/next-trailingslash,
// trailingSlash: true), as next start enforces it (verified against a real
// next start of the fixture): a non-root page path without a trailing slash
// 308-redirects to the slash form (query preserved); the slash form serves the
// page; the root and data/asset URLs are untouched.
func TestTrailingSlash(t *testing.T) {
	dir := appDir("next-trailingslash")
	if _, err := os.Stat(filepath.Join(dir, "node_modules/next")); err != nil {
		t.Skip("fixture not installed (go run ./cmd/og install testdata/apps/next-trailingslash)")
	}
	out, err := Build(context.Background(), dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	fsys, err := packageFixture(out)
	if err != nil {
		t.Fatal(err)
	}
	h, _, err := app.Handler(fsys, slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(p string) (*http.Response, string) {
		resp, err := client.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	// 308 to the slash form (next start oracle), query preserved.
	for _, c := range []struct{ path, loc string }{
		{"/about", "/about/"},
		{"/about?x=1", "/about/?x=1"},
	} {
		resp, _ := get(c.path)
		if resp.StatusCode != 308 || resp.Header.Get("Location") != c.loc {
			t.Errorf("GET %s: %d Location %q, want 308 %q", c.path, resp.StatusCode, resp.Header.Get("Location"), c.loc)
		}
	}
	// The canonical forms serve the page; the root and data URLs are untouched.
	if resp, body := get("/about/"); resp.StatusCode != 200 || !strings.Contains(body, `id="where">about`) {
		t.Errorf("GET /about/: %d, want 200 with the page\n%.300s", resp.StatusCode, body)
	}
	if resp, _ := get("/"); resp.StatusCode != 200 {
		t.Errorf("GET /: %d, want 200 (root is not redirected)", resp.StatusCode)
	}
	if resp, _ := get("/_next/data/" + out.BuildID + "/about.json"); resp.StatusCode != 200 {
		t.Errorf("GET data URL: %d, want 200 (data URLs are not slash-normalized)", resp.StatusCode)
	}
	// A dynamic SSG route (getStaticPaths) is prerendered under its canonical
	// slash form, so /posts/1 redirects and /posts/1/ is a build HIT (not a
	// request-time render).
	if resp, _ := get("/posts/1"); resp.StatusCode != 308 || resp.Header.Get("Location") != "/posts/1/" {
		t.Errorf("GET /posts/1: %d Location %q, want 308 /posts/1/", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, body := get("/posts/1/"); resp.StatusCode != 200 || resp.Header.Get("x-nextjs-cache") != "HIT" || !strings.Contains(body, `id="post">post <!-- -->1`) {
		t.Errorf("GET /posts/1/: %d cache=%s, want 200 HIT with the page\n%.300s", resp.StatusCode, resp.Header.Get("x-nextjs-cache"), body)
	}
	// An App Router dynamic route: its generateStaticParams paths (filled by
	// prerender.go, not pre-canonicalized) are prerendered under the slash
	// form too.
	if resp, _ := get("/items/1"); resp.StatusCode != 308 || resp.Header.Get("Location") != "/items/1/" {
		t.Errorf("GET /items/1: %d Location %q, want 308 /items/1/", resp.StatusCode, resp.Header.Get("Location"))
	}
	if resp, body := get("/items/1/"); resp.StatusCode != 200 || resp.Header.Get("x-nextjs-cache") != "HIT" || !strings.Contains(body, `id="item">item <!-- -->1`) {
		t.Errorf("GET /items/1/: %d cache=%s, want 200 HIT with the page\n%.300s", resp.StatusCode, resp.Header.Get("x-nextjs-cache"), body)
	}
}

// TestTrailingSlashBasePath: basePath "/docs" + trailingSlash true
// (testdata/apps/next-ts-basepath), verified against a real next start. The
// basePath root and pages 308-redirect to their slash form and HIT there; the
// slash normalization applies to the raw pathname, so a path outside the
// basePath still redirects (then 404s); the true root 404s.
func TestTrailingSlashBasePath(t *testing.T) {
	dir := appDir("next-ts-basepath")
	if _, err := os.Stat(filepath.Join(dir, "node_modules/next")); err != nil {
		t.Skip("fixture not installed (go run ./cmd/og install testdata/apps/next-ts-basepath)")
	}
	out, err := Build(context.Background(), dir, Options{})
	if err != nil {
		t.Fatal(err)
	}
	fsys, err := packageFixture(out)
	if err != nil {
		t.Fatal(err)
	}
	h, _, err := app.Handler(fsys, slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	get := func(p string) (*http.Response, string) {
		resp, err := client.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	// The basePath root and a page redirect to their slash form and HIT there.
	for _, c := range []struct{ path, loc string }{
		{"/docs", "/docs/"},
		{"/docs/about", "/docs/about/"},
		{"/docs/about?x=1", "/docs/about/?x=1"},
	} {
		if resp, _ := get(c.path); resp.StatusCode != 308 || resp.Header.Get("Location") != c.loc {
			t.Errorf("GET %s: %d Location %q, want 308 %q", c.path, resp.StatusCode, resp.Header.Get("Location"), c.loc)
		}
	}
	if resp, _ := get("/docs/"); resp.StatusCode != 200 || resp.Header.Get("X-Nextjs-Cache") != "HIT" {
		t.Errorf("GET /docs/: %d cache=%s, want 200 HIT (home prerendered under the basePath slash form)", resp.StatusCode, resp.Header.Get("X-Nextjs-Cache"))
	}
	if resp, body := get("/docs/about/"); resp.StatusCode != 200 || resp.Header.Get("X-Nextjs-Cache") != "HIT" || !strings.Contains(body, `id="where">about`) {
		t.Errorf("GET /docs/about/: %d cache=%s, want 200 HIT with the page", resp.StatusCode, resp.Header.Get("X-Nextjs-Cache"))
	}
	// The slash normalization is on the raw pathname: /about redirects (then
	// 404s outside the basePath); the true root 404s.
	if resp, _ := get("/about"); resp.StatusCode != 308 || resp.Header.Get("Location") != "/about/" {
		t.Errorf("GET /about: %d Location %q, want 308 /about/", resp.StatusCode, resp.Header.Get("Location"))
	}
	for _, p := range []string{"/about/", "/"} {
		if resp, _ := get(p); resp.StatusCode != 404 {
			t.Errorf("GET %s: %d, want 404 (outside the basePath)", p, resp.StatusCode)
		}
	}
}

// TestTrailingSlashDefault: with trailingSlash unset (the default), a page
// path WITH a trailing slash 308-redirects to the no-slash form, as next
// start does. next-hello (serveFixture) sets no trailingSlash.
func TestTrailingSlashDefault(t *testing.T) {
	srv := serveFixture(t)
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.Get(srv.URL + "/blog/hello/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 308 || resp.Header.Get("Location") != "/blog/hello" {
		t.Errorf("GET /blog/hello/: %d Location %q, want 308 /blog/hello", resp.StatusCode, resp.Header.Get("Location"))
	}
	// The no-slash form still serves (prerendered HIT), unredirected.
	resp2, err := client.Get(srv.URL + "/blog/hello")
	if err != nil {
		t.Fatal(err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Errorf("GET /blog/hello: %d, want 200", resp2.StatusCode)
	}
}
