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

// TestWebpackHook: next.config's webpack() hook (testdata/apps/next-webpack)
// as next build applies it: @next/mdx's loader for an .mdx page and an
// imported .mdx module, with mdx-components.tsx (its array alias with
// private-next-root-dir); @svgr/webpack set up as Next's docs do (finding
// and editing Next's image rule), which renders as svgr does under Node; a
// resolve.alias; DefinePlugin values per target; the browser's
// resolve.fallback (an empty module, not a missing one); and a loader of
// the project's own as rule.loader with rule.options, reading the module's
// buildInfo; a loader resolving with getResolve's preferRelative; and
// experiments.asyncWebAssembly. The fixture builds with `next build
// --webpack`.
func TestWebpackHook(t *testing.T) {
	dir := appDir("next-webpack")
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "@next", "mdx")); err != nil {
		t.Skip("fixture not installed (go run ./cmd/og install testdata/apps/next-webpack)")
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
	get := func(path string) string {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("GET %s: %d\n%.500s", path, resp.StatusCode, b)
		}
		return string(b)
	}
	home := get("/")
	for _, want := range []string{
		// svgr under Node gives <svg xmlns=… width={24} height={24} {...props}><circle cx={12} cy={12} r={10} /></svg>.
		`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24" id="logo"><circle cx="12" cy="12" r="10"></circle></svg>`,
		`<h1 class="mdx-heading">Imported MDX</h1>`,
		`<strong>@next/mdx</strong>`,
		`<p id="greeting">hello from an alias</p>`,
		`<p id="txt">hello txt! (object)</p>`,
		`<p id="server-built-for">server built for <!-- -->server</p>`,
		// lib/links-loader.cjs: getResolve's preferRelative, and a package
		// that is not installed rejects.
		`<p id="links">{&quot;sibling&quot;:&quot;app/sibling.js&quot;,&quot;og-fixture-not-installed&quot;:null}</p>`,
	} {
		if !strings.Contains(home, want) {
			t.Errorf("/ lacks %s", want)
			if i := strings.Index(home, `<p id="links">`); i >= 0 && strings.HasPrefix(want, `<p id="links">`) {
				t.Logf("found %.200s", home[i:])
			}
		}
	}
	notes := get("/notes")
	for _, want := range []string{`<title>Notes</title>`, `<h1 class="mdx-heading">Notes page</h1>`, `<p>An MDX page in the App Router.</p>`} {
		if !strings.Contains(notes, want) {
			t.Errorf("/notes lacks %s", want)
		}
	}
	// experiments.asyncWebAssembly: app/wasm/calc.wasm's exports, its import
	// from ./base.js wired (Node: 42 101).
	if wasm := get("/wasm"); !strings.Contains(wasm, `<p id="wasm">42 101</p>`) {
		t.Errorf("/wasm lacks the wasm module's results\n%.500s", wasm)
	}
	var browser string
	for name, b := range out.Static {
		if strings.HasPrefix(name, "static/chunks/main-app-") {
			browser = string(b)
		}
	}
	if !regexp.MustCompile(`"built-for:"\s*\+\s*"client"|"built-for:client"`).MatchString(browser) {
		t.Error("the browser bundle lacks the client's DefinePlugin value")
	}
	// The browser build's loader run resolves alike (og's stand-in for a
	// package that is not installed is no file).
	if !regexp.MustCompile(`sibling"?:\s*"app/sibling\.js",\s*"og-fixture-not-installed":\s*null`).MatchString(browser) {
		i := strings.Index(browser, "og-fixture-not-installed")
		t.Errorf("the browser build's links-loader output differs: %.200s", browser[max(0, i-120):max(0, i+80)])
	}
	if strings.Contains(browser, "Cannot find module 'og-fixture-node-only'") {
		t.Error("the browser's resolve.fallback was not applied (the module is missing, not empty)")
	}
}

// TestBabelConfig: a project with a .babelrc (testdata/apps/next-babel) and
// no next.config compiles its own files with Next's Babel loader and the
// config's plugins, as next build does ("Disabled SWC as replacement for
// Babel"): a project plugin replaces __BABEL_MARK__ in server and client
// components alike.
func TestBabelConfig(t *testing.T) {
	dir := appDir("next-babel")
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "next")); err != nil {
		t.Skip("fixture not installed (go run ./cmd/og install testdata/apps/next-babel)")
	}
	_ = os.Remove(filepath.Join(dir, "lib", "build-info.generated.js")) // the build regenerates it
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
	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	// The prebuild script's generated module (codegen.go): its arguments and
	// environment; its `tsc --noEmit` is skipped.
	if want := `<main><h1>og babel</h1><p id="mark">compiled by babel: page.tsx</p><p id="client-mark">compiled by babel: client.tsx</p><p id="build-info">generated<!-- --> <!-- -->prod</p><p id="mjs">an .mjs file</p></main>`; !strings.Contains(string(b), want) {
		t.Errorf("/ lacks %s\n%.800s", want, b)
	}
	var browser string
	for name, code := range out.Static {
		if strings.HasPrefix(name, "static/chunks/main-app-") {
			browser = string(code)
		}
	}
	if !strings.Contains(browser, "compiled by babel: client.tsx") {
		t.Error("the browser bundle was not compiled with Babel")
	}
}

// TestTurbopack: a project built with Turbopack, next build's default
// (testdata/apps/next-turbopack): next.config's turbopack section applies
// (a rule with JavaScript output and a condition, one with CSS output and
// options, a resolveAlias) and its webpack() does not; @next/mdx, loaded
// with TURBOPACK set, compiles an .mdx page through its Turbopack rule,
// with the project's mdx-components (Turbopack's mdx-import-source).
func TestTurbopack(t *testing.T) {
	dir := appDir("next-turbopack")
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "@next", "mdx")); err != nil {
		t.Skip("fixture not installed (go run ./cmd/og install testdata/apps/next-turbopack)")
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
	get := func(path string) string {
		t.Helper()
		resp, err := http.Get(srv.URL + path)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("GET %s: %d\n%.500s", path, resp.StatusCode, b)
		}
		return string(b)
	}
	// next.config's turbopack section (app/turbo): a rule with JavaScript
	// output (with a condition), one with CSS output (with options), and a
	// resolveAlias.
	turbo := get("/turbo")
	for _, want := range []string{`<p id="turbo-banner">MADE BY A TURBOPACK RULE</p>`, `<p id="turbo-alias">hello from a turbopack alias</p>`, `<p id="bundler">turbopack</p>`} {
		if !strings.Contains(turbo, want) {
			t.Errorf("/turbo lacks %s", want)
		}
	}
	var turboCSS string
	for _, m := range regexp.MustCompile(`href="(/_next/static/css/[^"]+\.css)"`).FindAllStringSubmatch(turbo, -1) {
		turboCSS += get(m[1])
	}
	if !regexp.MustCompile(`#turbo-banner\s*\{[^}]*color:\s*(#010203|rgb\(1, ?2, ?3\))`).MatchString(turboCSS) {
		t.Errorf("/turbo's stylesheets lack theme.tcss's rule:\n%.800s", turboCSS)
	}
	notes := get("/notes")
	for _, want := range []string{`<h1 class="turbo-mdx-heading">Turbopack notes</h1>`, `<strong>@next/mdx</strong>`} {
		if !strings.Contains(notes, want) {
			t.Errorf("/notes lacks %s\n%.600s", want, notes)
		}
	}
}
