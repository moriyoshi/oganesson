package next

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/moriyoshi/oganesson/runtime/app"
)

// TestBasePath: next.config's basePath (testdata/apps/next-basepath,
// "/docs"), as next start serves it: pages (app and pages routers,
// prerendered and not), /_next/ assets, url() assets in CSS, static image
// imports through the image optimizer, public files and data URLs all live
// under /docs; the same paths without it are 404s.
func TestBasePath(t *testing.T) {
	dir := appDir("next-basepath")
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "next")); err != nil {
		t.Skip("fixture not installed (go run ./cmd/og install testdata/apps/next-basepath)")
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
	get := func(p string) (int, string, http.Header) {
		t.Helper()
		resp, err := client.Get(srv.URL + p)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp.StatusCode, string(b), resp.Header
	}
	// Prerendered at build time under the basePath: the first request is a
	// cache hit.
	for _, p := range []string{"/docs", "/docs/about", "/docs/posts/first", "/docs/legacy"} {
		if code, _, hdr := get(p); code != 200 || hdr.Get("X-Nextjs-Cache") != "HIT" {
			t.Errorf("GET %s: %d, x-nextjs-cache %q, want a prerendered HIT", p, code, hdr.Get("X-Nextjs-Cache"))
		}
	}
	for p, want := range map[string]string{
		"/docs":             "basePath home",
		"/docs/about":       "basePath about",
		"/docs/posts/first": "post <!-- -->first",
		"/docs/posts/other": "post <!-- -->other",
		"/docs/legacy":      "legacy <!-- -->getStaticProps",
		"/docs/hello.txt":   "hello from public",
	} {
		if code, body, _ := get(p); code != 200 || !strings.Contains(body, want) {
			t.Errorf("GET %s: %d, want 200 with %q\n%.400s", p, code, want, body)
		}
	}
	for _, p := range []string{"/", "/about", "/legacy", "/hello.txt", "/docsabout", "/_next/data/" + out.BuildID + "/legacy.json"} {
		if code, _, _ := get(p); code != 404 {
			t.Errorf("GET %s: %d, want 404 (outside basePath)", p, code)
		}
	}
	if code, _, _ := get("/_next/data/x/y.json"); code != 404 {
		t.Errorf("GET /_next/data/x/y.json: %d, want 404", code)
	}
	if code, body, hdr := get("/docs/_next/data/" + out.BuildID + "/legacy.json"); code != 200 || !strings.Contains(body, `"from":"getStaticProps"`) {
		t.Errorf("data URL: %d %s %v", code, body, hdr)
	}

	// proxy.ts guards /guarded with a narrow matcher. Next's
	// getMiddlewareMatchers prepends the basePath to the matcher source, so the
	// regexp og builds only fires under /docs: /docs/guarded is intercepted,
	// the same path without the basePath is a plain 404 (matcher misses it).
	if !out.HasMiddleware {
		t.Fatal("next-basepath has proxy.ts but the build reports no middleware")
	}
	if code, body, _ := get("/docs/guarded"); code != 403 || body != "guarded by proxy" {
		t.Errorf("GET /docs/guarded: %d %q, want 403 from the proxy (matcher must carry the basePath)", code, body)
	}
	if code, _, _ := get("/guarded"); code != 404 {
		t.Errorf("GET /guarded (no basePath): %d, want 404 (the matcher regexp carries /docs, so the proxy does not run)", code)
	}

	_, home, _ := get("/docs")
	if !strings.Contains(home, `href="/docs/about"`) {
		t.Error("the link to /about lacks the basePath")
	}
	assets := regexp.MustCompile(`(?:src|href)="(/[^"]*_next/static/[^"]+)"`).FindAllStringSubmatch(home, -1)
	if len(assets) == 0 {
		t.Fatalf("no /_next/static assets in the page\n%.2000s", home)
	}
	var css string
	for _, a := range assets {
		u := a[1]
		if !strings.HasPrefix(u, "/docs/_next/static/") {
			t.Errorf("asset %s is not under the basePath", u)
			continue
		}
		code, b, _ := get(u)
		if code != 200 {
			t.Errorf("GET %s: %d", u, code)
		}
		if code, _, _ := get(strings.TrimPrefix(u, "/docs")); code != 404 {
			t.Errorf("GET %s without the basePath: %d, want 404", u, code)
		}
		if strings.HasSuffix(u, ".css") {
			css += b
		}
	}
	media := regexp.MustCompile(`url\(([^)]*static/media/dot[^)]*)\)`).FindStringSubmatch(css)
	if media == nil {
		t.Fatalf("the stylesheet lacks the url() asset\n%.1000s", css)
	}
	if u := strings.Trim(media[1], `"'`); !strings.HasPrefix(u, "/docs/_next/static/media/") {
		t.Errorf("url() asset %s is not under the basePath", u)
	} else if code, _, _ := get(u); code != 200 {
		t.Errorf("GET %s: %d", u, code)
	}
	img := regexp.MustCompile(`<img[^>]*id="dot"[^>]*src="([^"]+)"`).FindStringSubmatch(home)
	if img == nil {
		t.Fatalf("no image in the page")
	}
	src := strings.ReplaceAll(img[1], "&amp;", "&")
	if !strings.HasPrefix(src, "/docs/_next/image?url=%2Fdocs%2F_next%2Fstatic%2Fmedia%2Fdot.") {
		t.Errorf("image src %s", src)
	}
	if code, _, hdr := get(src); code != 200 || hdr.Get("Content-Type") != "image/png" {
		t.Errorf("GET %s: %d %s", src, code, hdr.Get("Content-Type"))
	}
}
