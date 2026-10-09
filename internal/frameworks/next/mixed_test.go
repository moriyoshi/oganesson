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
	"sync"
	"testing"
	"testing/fstest"

	"github.com/moriyoshi/oganesson/runtime/app"
)

var (
	mixedOnce sync.Once
	mixedOut  *Output
	mixedFS   fstest.MapFS
	mixedErr  error
)

func mixedFixtureDir(t *testing.T) string {
	t.Helper()
	dir := appDir("next-mixed")
	if _, err := os.Stat(filepath.Join(dir, "node_modules/next")); err != nil {
		t.Skip("fixture not installed (go run ./cmd/og install testdata/apps/next-mixed)")
	}
	return dir
}

// TestMixedRouters: an app directory and a pages directory in one project.
// Each router serves its routes; 404s (unmatched URLs and a page's
// notFound) render the app's not-found, as next start's
// renderErrorToResponse does when an app directory exists; pages/404
// visited directly answers 404.
func TestMixedRouters(t *testing.T) {
	dir := mixedFixtureDir(t)
	mixedOnce.Do(func() {
		mixedOut, mixedErr = Build(context.Background(), dir, Options{})
		if mixedErr == nil {
			mixedFS, mixedErr = packageFixture(mixedOut)
		}
	})
	if mixedErr != nil {
		t.Fatal(mixedErr)
	}
	h, _, err := app.Handler(mixedFS, slog.New(slog.NewTextHandler(testWriter{t}, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	get := func(p string, hdr map[string]string) (*http.Response, string) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+p, nil)
		for k, v := range hdr {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return resp, string(b)
	}
	data := "/_next/data/" + mixedOut.BuildID
	image := map[string]string{"Sec-Fetch-Dest": "image"}
	for _, c := range []struct {
		path   string
		hdr    map[string]string
		status int
		want   []string
	}{
		{"/", nil, 200, []string{`id="app-home"`, `data-router="app"`}},
		{"/dashboard", nil, 200, []string{`id="dashboard"`}},
		{"/about", nil, 200, []string{`id="about"`, `"page":"/about"`}},
		{"/blog/hello", nil, 200, []string{`Pages post <!-- -->hello`}},
		{data + "/about.json", nil, 200, []string{`{"pageProps":{"builtAt":`}},
		{data + "/blog/hello.json", nil, 200, []string{`{"pageProps":{"slug":"hello"},"__N_SSP":true}`}},
		{"/api/hello", nil, 200, []string{`{"router":"pages"}`}},
		// 404s render the app's not-found.
		{"/nope", nil, 404, []string{`id="app-nf"`}},
		{"/blog/missing", nil, 404, []string{`id="app-nf"`}},
		{"/_error", nil, 404, []string{`id="app-nf"`}},
		{data + "/dashboard.json", nil, 404, nil},
		// pages/404 visited directly.
		{"/404", nil, 404, []string{`id="pages-nf"`}},
		// Subresource requests get a plain 404.
		{"/nope.png", image, 404, []string{`Not Found`}},
		{"/404", image, 404, []string{`Not Found`}},
	} {
		resp, body := get(c.path, c.hdr)
		if resp.StatusCode != c.status {
			t.Errorf("GET %s %v: %d, want %d\n%.300s", c.path, c.hdr, resp.StatusCode, c.status, body)
			continue
		}
		for _, w := range c.want {
			if !strings.Contains(body, w) {
				t.Errorf("GET %s %v lacks %s\n%.400s", c.path, c.hdr, w, body)
			}
		}
		if c.hdr != nil && strings.Contains(body, "<html") {
			t.Errorf("GET %s %v: rendered HTML for a subresource request", c.path, c.hdr)
		}
	}
}

// TestAppPageConflict: next build's "Conflicting app and page file" error.
func TestAppPageConflict(t *testing.T) {
	src := mixedFixtureDir(t)
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(src, "node_modules"), filepath.Join(dir, "node_modules")); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"package.json":       `{"name":"conflict","private":true}`,
		"app/layout.tsx":     `export default function L({ children }: { children: React.ReactNode }) { return <html><body>{children}</body></html>; }`,
		"app/about/page.tsx": `export default function P() { return <p>app</p>; }`,
		"app/(g)/x/page.tsx": `export default function P() { return <p>app x</p>; }`,
		"app/api/h/route.ts": `export function GET() { return Response.json({}); }`,
		"app/ok/page.tsx":    `export default function P() { return <p>ok</p>; }`,
		"pages/about.tsx":    `export default function P() { return <p>pages</p>; }`,
		"pages/x/index.tsx":  `export default function P() { return <p>pages x</p>; }`,
		"pages/api/h.ts":     `export default function h(_req: any, res: any) { res.json({}); }`,
		"pages/fine.tsx":     `export default function P() { return <p>fine</p>; }`,
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	_, err := Build(context.Background(), dir, Options{})
	if err == nil {
		t.Fatal("build succeeded")
	}
	want := "Conflicting app and page files were found, please remove the conflicting files to continue:\n" +
		`  "pages/about.tsx" - "app/about/page.tsx"` + "\n" +
		`  "pages/api/h.ts" - "app/api/h/route.ts"` + "\n" +
		`  "pages/x/index.tsx" - "app/(g)/x/page.tsx"`
	if err.Error() != want {
		t.Fatalf("error:\n%s\nwant:\n%s", err, want)
	}
}
