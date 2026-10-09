package vite

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"html"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"github.com/andybalholm/brotli"
	"github.com/moriyoshi/oganesson/runtime/app"
	"github.com/moriyoshi/oganesson/runtime/notices"
)

func fixture(t *testing.T, name string) string {
	t.Helper()
	dir, _ := filepath.Abs(filepath.Join("../../../testdata/apps", name))
	if _, err := os.Stat(filepath.Join(dir, "node_modules/vite")); err != nil {
		t.Skip("fixture not installed (go run ./cmd/og install testdata/apps/" + name + ")")
	}
	return dir
}

var (
	reactOnce sync.Once
	reactOut  *Output
	reactErr  error
)

func buildReact(t *testing.T) *Output {
	t.Helper()
	dir := fixture(t, "vite-react")
	reactOnce.Do(func() { reactOut, reactErr = Build(context.Background(), dir, Options{Log: t.Logf}) })
	if reactErr != nil {
		t.Fatal(reactErr)
	}
	return reactOut
}

// file returns the one output file matching re.
func file(t *testing.T, out *Output, re string) (string, string) {
	t.Helper()
	var name string
	for n := range out.Client {
		if regexp.MustCompile(re).MatchString(n) {
			if name != "" {
				t.Fatalf("two files match %s: %s, %s", re, name, n)
			}
			name = n
		}
	}
	if name == "" {
		t.Fatalf("no file matches %s", re)
	}
	return name, string(out.Client[name])
}

// TestBuildReactSPA: vite build of a React SPA through Vite's own code on
// the Rolldown facade: Vite's HTML plugin, CSS per chunk, the preload
// helper for dynamic imports, env and define, JSON, import.meta.glob and
// resolve.alias (Vite's JS plugins), and public/.
func TestBuildReactSPA(t *testing.T) {
	checkReactSPA(t, buildReact(t), true)
}

// TestBuildVite7SPA: the same app on Vite 7, which bundles with Rollup
// (its WebAssembly build) and the esbuild npm package (og's esbuild), not
// Rolldown: og's Rolldown facade and shared rolldown/* stay out of it.
// Node's build of it has the same files but for minifier naming.
func TestBuildVite7SPA(t *testing.T) {
	checkReactSPA(t, buildFixture(t, "vite7-react"), false)
}

// checkReactSPA checks the vite-react app's output; vite8 adds what only
// Vite 8 writes (a modulepreload link, the preload helper's
// import.meta.url), which Node's Vite 7 build has not either.
func checkReactSPA(t *testing.T, out *Output, vite8 bool) {
	t.Helper()
	if out.AppType != "spa" || out.Base != "/" {
		t.Errorf("appType %q base %q", out.AppType, out.Base)
	}
	entry, entryJS := file(t, out, `^assets/index-[^/]+\.js$`)
	cssName, css := file(t, out, `^assets/index-[^/]+\.css$`)
	lazyCSSName, lazyCSS := file(t, out, `^assets/Lazy-[^/]+\.css$`)
	lazyJS, _ := file(t, out, `^assets/Lazy-[^/]+\.js$`)
	html := string(out.Client["index.html"])
	for _, w := range []string{
		`<script type="module" crossorigin src="/` + entry + `"></script>`,
		`<link rel="stylesheet" crossorigin href="/` + cssName + `">`,
		`<link rel="icon" href="/favicon.svg" />`,
		`<link rel="modulepreload" crossorigin href="/assets/`,
	} {
		if !vite8 && strings.Contains(w, "modulepreload") {
			continue
		}
		if !strings.Contains(html, w) {
			t.Errorf("index.html lacks %s\n%s", w, html)
		}
	}
	if strings.Contains(html, "/src/main.tsx") {
		t.Errorf("index.html still references the source entry")
	}
	if out.Client["favicon.svg"] == nil {
		t.Error("public/favicon.svg not copied")
	}
	for _, w := range []string{
		`Hello from Vite`, // @/data.json: resolve.alias + JSON
		`"hi"`, `"og"`,    // .env VITE_GREETING, define
		`"hello"`, `"bonjour"`, `./locales/en.ts`, // import.meta.glob
		`__vite__mapDeps(`, // the preload helper around import("./Lazy")
		`"` + lazyJS + `"`, `"` + lazyCSSName + `"`,
		`import.meta.url`, // the helper's own source, not the host's rewrite
	} {
		if !vite8 && w == `import.meta.url` {
			continue
		}
		if !strings.Contains(entryJS, w) {
			t.Errorf("%s lacks %s", entry, w)
		}
	}
	for _, bad := range []string{"import_meta", "file://", "__VITE_PRELOAD__", "__VITE_IS_MODERN__", "import.meta.glob", "import.meta.env"} {
		if strings.Contains(entryJS, bad) {
			t.Errorf("%s contains %s", entry, bad)
		}
	}
	if !strings.Contains(css, "color:#639") || !strings.Contains(lazyCSS, "color:teal") {
		t.Errorf("css: %q / %q", css, lazyCSS)
	}
}

// TestBuildViteFeatures: Vite build features through the facade: ?raw /
// ?url / ?inline, new URL(…, import.meta.url), both worker forms (their
// URLs are Rolldown file-URL references, resolved by Vite's resolveFileUrl),
// build.manifest, source maps, a function assetFileNames, tsconfig
// decorators, and a plugin calling this.parse.
func TestBuildViteFeatures(t *testing.T) {
	dir := fixture(t, "vite-features")
	out, err := Build(context.Background(), dir, Options{Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	entry, js := file(t, out, `^assets/index-[^/]+\.js$`)
	worker, _ := file(t, out, `^assets/worker-[^/]+\.js$`)
	square, _ := file(t, out, `^assets/square\.worker-[^/]+\.js$`)
	logo, _ := file(t, out, `^assets/logo-[^/]+\.png$`)
	asset, _ := file(t, out, `^text/asset-[^/]+\.txt$`) // assetFileNames(info)
	for _, w := range []string{
		"raw text from note.txt",
		`"/` + logo + `"`,
		"data:image/svg+xml,",
		`new URL("/` + asset + `",""+import.meta.url)`,
		`new Worker(new URL("/` + worker + `",""+import.meta.url)`,
		`new Worker("/` + square + `"`,
		`{main:7}`, // this.parse counted main.ts's imports
		"//# sourceMappingURL=" + path.Base(entry) + ".map",
	} {
		if !strings.Contains(js, w) {
			t.Errorf("%s lacks %s", entry, w)
		}
	}
	for _, bad := range []string{"ROLLDOWN_FILE_URL", "ROLLUP_FILE_URL", "file://"} {
		if strings.Contains(js, bad) {
			t.Errorf("%s contains %s", entry, bad)
		}
	}
	// tsconfig.json: legacy decorators (a decorate-class call, not
	// standard decorators' initializers) and useDefineForClassFields
	// false (an assignment in the constructor, no "name" class field).
	if !regexp.MustCompile(`=\w+\(\[\w+\("decorated"\)\],\w+\)`).MatchString(js) || !strings.Contains(js, `this.name="thing"`) || strings.Contains(js, "{name;") {
		t.Errorf("%s: decorators or class fields do not follow tsconfig.json", entry)
	}

	var manifest map[string]struct {
		File    string `json:"file"`
		Src     string `json:"src"`
		IsEntry bool   `json:"isEntry"`
	}
	if err := json.Unmarshal(out.Client[".vite/manifest.json"], &manifest); err != nil {
		t.Fatalf("manifest: %v\n%s", err, out.Client[".vite/manifest.json"])
	}
	if m := manifest["index.html"]; m.File != entry || !m.IsEntry {
		t.Errorf("manifest index.html: %+v", m)
	}
	if m := manifest["src/logo.png"]; m.File != logo {
		t.Errorf("manifest src/logo.png: %+v", m)
	}

	var sm struct {
		Sources        []string `json:"sources"`
		SourcesContent []string `json:"sourcesContent"`
	}
	if err := json.Unmarshal(out.Client[entry+".map"], &sm); err != nil {
		t.Fatalf("%s.map: %v", entry, err)
	}
	joined := strings.Join(sm.Sources, " ")
	if !strings.Contains(joined, "../../src/main.ts") || !strings.Contains(joined, "../../src/note.txt?raw") || strings.Contains(joined, "og-vite:") || strings.Contains(joined, dir) {
		t.Errorf("source map sources %q", sm.Sources)
	}
	// Plugin transform maps are chained: the map leads to the original
	// TypeScript, not the transformed JavaScript esbuild bundled.
	for i, src := range sm.Sources {
		if src == "../../src/decorated.ts" && (i >= len(sm.SourcesContent) || !strings.Contains(sm.SourcesContent[i], "name!: string;")) {
			t.Errorf("decorated.ts maps to transformed code, not the original TypeScript")
		}
	}
}

// buildFixture builds a fixture and returns its client output.
func buildFixture(t *testing.T, name string) *Output {
	t.Helper()
	out, err := Build(context.Background(), fixture(t, name), Options{Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestBuildVue: @vitejs/plugin-vue. The SFC's <script setup lang="ts">
// goes through Vite's transformWithOxc (rolldown/utils on esbuild), and
// its scoped style is extracted although the style module renders no code.
func TestBuildVue(t *testing.T) {
	out := buildFixture(t, "vite-vue")
	_, js := file(t, out, `^assets/index-[^/]+\.js$`)
	cssName, css := file(t, out, `^assets/index-[^/]+\.css$`)
	scope := regexp.MustCompile(`data-v-[0-9a-f]{8}\b`).FindString(js)
	if scope == "" || !strings.Contains(js, "Hello from Vue") {
		t.Errorf("no compiled SFC with a scope id")
	}
	if !strings.Contains(css, ".title["+scope+"]{color:#0a141e}") {
		t.Errorf("%s: %q (scope %s)", cssName, css, scope)
	}
	if html := string(out.Client["index.html"]); !strings.Contains(html, `href="/`+cssName+`"`) {
		t.Errorf("index.html does not link %s:\n%s", cssName, html)
	}
}

// TestBuildSvelte: @sveltejs/vite-plugin-svelte (Svelte 5 runes, TS in the
// component, this.getCombinedSourcemap in its transform).
func TestBuildSvelte(t *testing.T) {
	out := buildFixture(t, "vite-svelte")
	_, js := file(t, out, `^assets/index-[^/]+\.js$`)
	_, css := file(t, out, `^assets/index-[^/]+\.css$`)
	if !strings.Contains(js, "Hello from Svelte") {
		t.Error("no compiled component")
	}
	if !regexp.MustCompile(`\.title\.svelte-[a-z0-9]+\{color:#28323c\}`).MatchString(css) {
		t.Errorf("css: %q", css)
	}
}

// TestBuildTailwind: @tailwindcss/vite, whose scanner (oxide) is a napi-rs
// addon loaded from its wasm32-wasi build. The CSS has the utilities the
// sources use (index.html and main.ts) and not others.
func TestBuildTailwind(t *testing.T) {
	out := buildFixture(t, "vite-tailwind")
	_, css := file(t, out, `^assets/index-[^/]+\.css$`)
	for _, w := range []string{".text-3xl", ".font-bold", ".text-sky-600", ".underline", ".bg-amber-100", ".text-brand", "--color-brand"} {
		if !strings.Contains(css, w) {
			t.Errorf("css lacks %s", w)
		}
	}
	if strings.Contains(css, ".text-red-500") {
		t.Error("css has an unused utility")
	}
	// Tailwind's preflight/theme CSS is inlined by @tailwindcss/vite, so the
	// build host reads node_modules/tailwindcss/*.css (addAssetLicenses); the
	// package is attributed although esbuild never saw it as a bundle input.
	notices := string(out.Notices)
	if !strings.Contains(notices, "\ntailwindcss ") {
		t.Errorf("notices lack tailwindcss (its preflight CSS ships):\n%s", notices)
	}
	// The build-time tailwind tooling and Vite do not ship, so stay unattributed.
	for _, bad := range []string{"\n@tailwindcss/vite ", "\n@tailwindcss/node ", "\n@tailwindcss/oxide ", "\nvite "} {
		if strings.Contains(notices, bad) {
			t.Errorf("notices list the build-time package %q", strings.TrimSpace(bad))
		}
	}
}

// TestBuildPreact: @preact/preset-vite (its Babel JSX transform; a
// `new Function("specifier", "return import(specifier)")` in the preset
// is rewritten for the build host).
func TestBuildPreact(t *testing.T) {
	out := buildFixture(t, "vite-preact")
	_, js := file(t, out, `^assets/index-[^/]+\.js$`)
	_, css := file(t, out, `^assets/index-[^/]+\.css$`)
	if !strings.Contains(js, "Hello from Preact") || strings.Contains(js, "react/jsx-runtime") {
		t.Error("the component is not compiled for Preact")
	}
	if !strings.Contains(css, ".title{color:#0b1621}") {
		t.Errorf("css: %q", css)
	}
}

// TestBuildSolid: vite-plugin-solid (Babel with dom-expressions: JSX into
// template cloning and fine-grained effects).
func TestBuildSolid(t *testing.T) {
	out := buildFixture(t, "vite-solid")
	_, js := file(t, out, `^assets/index-[^/]+\.js$`)
	if !strings.Contains(js, "Hello from Solid") || !strings.Contains(js, "<main><h1 class=title>") {
		t.Errorf("the component is not compiled to Solid templates")
	}
}

// TestBuildUnoCSS: UnoCSS's Vite plugin generates the utilities its
// extraction pipeline finds in index.html and the .tsx module (not plain
// .ts, as upstream) into virtual:uno.css.
func TestBuildUnoCSS(t *testing.T) {
	out := buildFixture(t, "vite-unocss")
	_, css := file(t, out, `^assets/index-[^/]+\.css$`)
	for _, w := range []string{".text-3xl{", ".font-bold{", ".text-red-600{", ".underline{", ".mt-4{", ".p-2{"} {
		if !strings.Contains(css, w) {
			t.Errorf("css lacks %s", w)
		}
	}
	if strings.Contains(css, ".text-blue-600") {
		t.Error("css has an unused utility")
	}
}

// TestBuildPWA: vite-plugin-pwa's generateSW. workbox-build bundles the
// service worker with Rollup 4 (its WebAssembly build, @rollup/wasm-node),
// Babel and @rollup/plugin-terser (whose worker pool runs on build-time
// worker_threads), writing its temporary files under the project's TMPDIR.
func TestBuildPWA(t *testing.T) {
	out := buildFixture(t, "vite-pwa")
	entry, _ := file(t, out, `^assets/index-[^/]+\.js$`)
	sw := string(out.Client["sw.js"])
	for _, w := range []string{`url:"index.html"`, `url:"` + entry + `"`, `url:"manifest.webmanifest"`, "precacheAndRoute"} {
		if !strings.Contains(sw, w) {
			t.Errorf("sw.js lacks %s:\n%.800s", w, sw)
		}
	}
	if strings.Contains(sw, "process.env.NODE_ENV") || strings.Count(sw, "\n") > 5 {
		t.Error("sw.js is not built for production (replaced and minified)")
	}
	if _, wb := file(t, out, `^workbox-[0-9a-f]+\.js$`); !strings.Contains(wb, "workbox") {
		t.Error("no workbox runtime chunk")
	}
	if html := string(out.Client["index.html"]); !strings.Contains(html, `rel="manifest" href="/manifest.webmanifest"`) {
		t.Errorf("index.html does not link the manifest:\n%s", html)
	}
}

// TestBuildTanStackRouter: TanStack Router's Vite plugin (route tree
// generation and automatic code splitting, both Babel-based) on the facade.
// The route tree is generated by the build (it is deleted first), and each
// route's component lands in its own chunk.
func TestBuildTanStackRouter(t *testing.T) {
	dir := fixture(t, "tanstack-router")
	gen := filepath.Join(dir, "src", "routeTree.gen.ts")
	if err := os.Remove(gen); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	out, err := Build(context.Background(), dir, Options{Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	tree, err := os.ReadFile(gen)
	if err != nil {
		t.Fatalf("route tree not generated: %v", err)
	}
	for _, w := range []string{"'/about'", "'/posts/$postId'", "rootRouteImport"} {
		if !strings.Contains(string(tree), w) {
			t.Errorf("routeTree.gen.ts lacks %s", w)
		}
	}
	_, entryJS := file(t, out, `^assets/index-[^/]+\.js$`)
	_, aboutJS := file(t, out, `^assets/about-[^/]+\.js$`)
	_, postJS := file(t, out, `^assets/posts\.\$postId-[^/]+\.js$`)
	if strings.Contains(entryJS, "About (code-split)") || !strings.Contains(aboutJS, "About (code-split)") {
		t.Errorf("the about component was not split out of the entry")
	}
	if !strings.Contains(postJS, "useLoaderData") {
		t.Errorf("posts.$postId chunk lacks its component")
	}
	if !strings.Contains(string(out.Client["index.html"]), `<script type="module" crossorigin src="/assets/index-`) {
		t.Errorf("index.html: %s", out.Client["index.html"])
	}
}

// TestServeTanStackStart: TanStack Start's client and SSR environments
// (its own buildApp) through the facade; the SSR entry bundled for Goja and
// served per request through runtime/app, like the packaged binary.
// startBuilds caches fixture builds by fixture name and SPA mode: tests
// sharing one save a build (minutes under -race).
var (
	startMu     sync.Mutex
	startBuilds = map[string]*Output{}
)

// serveStart builds a TanStack Start fixture and serves its payload as
// the packaged binary would.
func serveStart(t *testing.T, name string) (*Output, *httptest.Server) {
	t.Helper()
	dir := fixture(t, name)
	key := name + "|" + os.Getenv("OG_FIXTURE_SPA")
	startMu.Lock()
	out := startBuilds[key]
	if out == nil {
		var err error
		if out, err = Build(context.Background(), dir, Options{Log: t.Logf}); err != nil {
			startMu.Unlock()
			t.Fatal(err)
		}
		startBuilds[key] = out
	}
	startMu.Unlock()
	if out.ServerJS == "" {
		t.Fatal("no server entry")
	}
	fsys := fstest.MapFS{}
	for name, b := range out.Payload() {
		fsys[name] = &fstest.MapFile{Data: b}
	}
	h, meta, err := app.Handler(fsys, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if meta.Framework != "vite" || meta.AppType != "custom" {
		t.Errorf("meta %+v", meta)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return out, srv
}

// browserUA makes Start stream (for a bot, isbot, it waits for all data).
const browserUA = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0.0.0 Safari/537.36"

func fetch(t *testing.T, srv *httptest.Server, method, p, body string) (*http.Response, string) {
	t.Helper()
	req, err := http.NewRequest(method, srv.URL+p, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("User-Agent", browserUA)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	return resp, string(b)
}

func TestServeTanStackStart(t *testing.T) {
	out, srv := serveStart(t, "tanstack-start")
	if strings.Contains(out.ServerJS, "Dynamic require of") && strings.Contains(out.ServerJS, `__require("`) {
		t.Error("server bundle still calls esbuild's require shim")
	}
	if len(out.Env) != 0 {
		t.Errorf("env %v without SPA mode", out.Env)
	}
	get := func(p string) (*http.Response, string) { return fetch(t, srv, "GET", p, "") }
	resp, body := get("/")
	for _, w := range []string{`<title>TanStack Start on Oganesson</title>`, `<h1 id="message">Hello from the server</h1>`, `<p id="runtime">server</p>`, `<script type="module" async="" src="/assets/index-`} {
		if !strings.Contains(body, w) {
			t.Errorf("GET / lacks %s\n%.1500s", w, body)
		}
	}
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") {
		t.Errorf("GET /: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	asset := regexp.MustCompile(`/assets/index-[^"]+\.js`).FindString(body)
	if resp, js := get(asset); resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Content-Type"), "javascript") || len(js) == 0 {
		t.Errorf("GET %s: %d %s", asset, resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp, _ := get("/no/such/page"); resp.StatusCode != 404 {
		t.Errorf("GET /no/such/page: %d", resp.StatusCode)
	}

	// Request middleware (src/start.ts) tags every response; function
	// middleware gives the about page's server function its context.
	resp, body = get("/about")
	for _, w := range []string{`<h1 id="about">About TanStack Start</h1>`, `<p id="user">og-user</p>`} {
		if !strings.Contains(body, w) {
			t.Errorf("GET /about lacks %s\n%.1500s", w, body)
		}
	}
	if h := resp.Header.Get("X-Og-Middleware"); h != "router:/about" {
		t.Errorf("GET /about: x-og-middleware %q", h)
	}

	// Server routes.
	resp, body = get("/api/hello?name=og")
	if resp.StatusCode != 200 || strings.TrimSpace(body) != `{"hello":"og"}` || resp.Header.Get("X-Og-Middleware") != "router:/api/hello" {
		t.Errorf("GET /api/hello: %d %q %v", resp.StatusCode, body, resp.Header)
	}
	resp, body = fetch(t, srv, "POST", "/api/hello", `{"a":1}`)
	if resp.StatusCode != 201 || strings.TrimSpace(body) != `{"received":{"a":1}}` {
		t.Errorf("POST /api/hello: %d %q", resp.StatusCode, body)
	}

	// createIsomorphicFn: the server implementation in the loader.
	if _, body := get("/iso"); !strings.Contains(body, `<p id="iso-loader">server</p>`) {
		t.Errorf("GET /iso:\n%.1500s", body)
	}
	// Selective SSR: ssr: false renders and loads nothing on the server;
	// "data-only" loads on the server and renders nothing.
	_, body = get("/client-only")
	if strings.Contains(body, `id="client-only"`) || strings.Contains(body, `at:"server"`) || !strings.Contains(body, `id="to-client-only"`) {
		t.Errorf("GET /client-only rendered or loaded on the server:\n%.2500s", body)
	}
	_, body = get("/data-only")
	if strings.Contains(body, `id="data-only"`) || !strings.Contains(body, `at:"server"`) {
		t.Errorf("GET /data-only: the loader did not run on the server, or the page rendered:\n%.2500s", body)
	}

	// The sitemap lists the routes, under sitemap.host.
	sitemap := string(out.Client["sitemap.xml"])
	for _, w := range []string{"<loc>https://og.example/static</loc>", "<loc>https://og.example/about</loc>"} {
		if !strings.Contains(sitemap, w) {
			t.Errorf("sitemap.xml lacks %s:\n%s", w, sitemap)
		}
	}
	if resp, body := get("/sitemap.xml"); resp.StatusCode != 200 || body != sitemap || !strings.Contains(resp.Header.Get("Content-Type"), "xml") {
		t.Errorf("GET /sitemap.xml: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}

	// Prerendering: /static was rendered at build time (through og's own
	// preview server) and is served as that file, stamp and all.
	page, ok := out.Client["static/index.html"]
	if !ok {
		t.Fatalf("no prerendered static/index.html in %v", keys(out.Client))
	}
	stamp := regexp.MustCompile(`<p id="stamp">([^<]+)</p>`).FindStringSubmatch(string(page))
	if stamp == nil {
		t.Fatalf("prerendered page lacks the stamp:\n%.1500s", page)
	}
	if _, ok := out.Client["index.html"]; ok {
		t.Error("/ was prerendered; the filter allows only /static")
	}
	for i := 0; i < 2; i++ {
		resp, body = get("/static")
		if resp.StatusCode != 200 || body != string(page) {
			t.Errorf("GET /static is not the prerendered page: %d\n%.1500s", resp.StatusCode, body)
		}
	}
}

// TestStreamTanStackStartDeferred: a loader's unawaited promise streams;
// the fallback arrives first and the value when the promise settles.
func TestStreamTanStackStartDeferred(t *testing.T) {
	_, srv := serveStart(t, "tanstack-start")
	req, _ := http.NewRequest("GET", srv.URL+"/deferred", nil)
	req.Header.Set("User-Agent", browserUA)
	start := time.Now()
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var got strings.Builder
	buf := make([]byte, 64<<10)
	var firstAt time.Duration
	for {
		n, err := resp.Body.Read(buf)
		got.Write(buf[:n])
		if firstAt == 0 && strings.Contains(got.String(), `id="slow-fallback"`) {
			firstAt = time.Since(start)
			if strings.Contains(got.String(), "slow data") {
				t.Error("the fallback arrived together with the resolved value")
			}
		}
		if err != nil {
			break
		}
	}
	total := time.Since(start)
	body := got.String()
	if !strings.Contains(body, `<p id="fast">fast data</p>`) || !strings.Contains(body, "slow data") {
		t.Fatalf("GET /deferred:\n%.3000s", body)
	}
	if firstAt == 0 || firstAt > time.Second || total < 1200*time.Millisecond {
		t.Errorf("not streamed: fallback after %v, done after %v", firstAt, total)
	}
}

// TestServeTanStackStartSPA: in SPA mode (OG_FIXTURE_SPA, vite.config.ts)
// the build prerenders the shell (_shell.html, for static hosts), and the
// server renders pages as Start's own server does: Start sets TSS_SHELL
// only on the shell's prerender request, so `vite preview` on Node 24
// renders /about in full (its loader's "og-user" included). og used to set
// TSS_SHELL for every request. Server routes and prerendered pages are
// unaffected.
func TestServeTanStackStartSPA(t *testing.T) {
	t.Setenv("OG_FIXTURE_SPA", "1")
	out, srv := serveStart(t, "tanstack-start")
	if _, ok := out.Env["TSS_SHELL"]; ok {
		t.Errorf("env %v", out.Env)
	}
	shell, ok := out.Client["_shell.html"]
	if !ok || !strings.Contains(string(shell), `id="to-home"`) || strings.Contains(string(shell), `id="about"`) {
		t.Errorf("no prerendered shell in %v", keys(out.Client))
	}
	if resp, body := fetch(t, srv, "GET", "/_shell.html", ""); resp.StatusCode != 200 || !strings.Contains(body, `id="to-home"`) {
		t.Errorf("GET /_shell.html: %d\n%.800s", resp.StatusCode, body)
	}
	resp, body := fetch(t, srv, "GET", "/about", "")
	if resp.StatusCode != 200 || !strings.Contains(body, `<h1 id="about">About TanStack Start</h1>`) || !strings.Contains(body, `<p id="user">og-user</p>`) {
		t.Errorf("GET /about (want the rendered page, as Start's server gives): %d\n%.1500s", resp.StatusCode, body)
	}
	if resp, body := fetch(t, srv, "GET", "/api/hello", ""); resp.StatusCode != 200 || strings.TrimSpace(body) != `{"hello":"world"}` {
		t.Errorf("GET /api/hello: %d %q", resp.StatusCode, body)
	}
	if _, body := fetch(t, srv, "GET", "/static", ""); !strings.Contains(body, `<p id="stamp">`) {
		t.Errorf("GET /static:\n%.1500s", body)
	}
}

// TestServeTanStackStartRSC: the React Server Components variant. The
// server component renders in the rsc environment, its client component
// (Counter) reaches SSR as a client reference.
func TestServeTanStackStartRSC(t *testing.T) {
	out, srv := serveStart(t, "tanstack-start-rsc")
	resp, body := fetch(t, srv, "GET", "/", "")
	for _, w := range []string{`<h1 id="hello">Hello, <!-- -->og<!-- -->, from a server component</h1>`, `<p id="env">rendered in <!-- -->rsc<!-- --> (<!-- -->server<!-- -->)</p>`, `<button id="counter">count <!-- -->5</button>`} {
		if !strings.Contains(body, w) {
			t.Errorf("GET / lacks %s\n%.2500s", w, body)
		}
	}
	if resp.StatusCode != 200 || strings.Contains(body, "<!--$!-->") {
		t.Errorf("GET /: %d (a boundary errored?)\n%.2500s", resp.StatusCode, body)
	}
	// CSS a server component imports: the stylesheet is linked, and is
	// a client asset.
	css := regexp.MustCompile(`<link rel="stylesheet"[^>]*href="/(assets/[^"]+\.css)"`).FindStringSubmatch(body)
	if css == nil || !strings.Contains(string(out.Client[css[1]]), "#010203") {
		t.Errorf("no stylesheet for greeting.css linked (%v):\n%.2500s", css, body)
	}

	// A mutation: the POST server function bumps a count in og:kv, and the
	// server component renders it.
	id := regexp.MustCompile(`"?([0-9a-f]{64})"?:\s*\{\s*functionName:\s*["']bumpVisits_createServerFn_handler`).FindStringSubmatch(out.ServerJS)
	if id == nil {
		t.Fatal("bumpVisits is not in the server function manifest")
	}
	for i := 1; i <= 2; i++ {
		req, _ := http.NewRequest("POST", srv.URL+"/_serverFn/"+id[1], nil)
		req.Header.Set("x-tsr-serverFn", "true")
		req.Header.Set("Origin", srv.URL) // Start's CSRF middleware wants same-origin
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("bumpVisits: %d %s", resp.StatusCode, b)
		}
	}
	if _, body := fetch(t, srv, "GET", "/", ""); !strings.Contains(body, `<p id="visits">visits <!-- -->2</p>`) {
		t.Errorf("GET / after two bumps:\n%.2500s", body)
	}

	// A Composite Component: the server's card with both client slots.
	_, body = fetch(t, srv, "GET", "/composite", "")
	for _, w := range []string{`<h2 id="card-title">Server card</h2>`, `<div id="actions"><button id="act">act <!-- -->42</button></div>`, `<div id="kids"><p id="slot-child">client child</p></div>`} {
		if !strings.Contains(body, w) {
			t.Errorf("GET /composite lacks %s\n%.2500s", w, body)
		}
	}

	// The browser gets Counter's code (a client reference module).
	found := false
	for name, b := range out.Client {
		if strings.HasSuffix(name, ".js") && strings.Contains(string(b), "count ") {
			found = true
		}
	}
	if !found {
		t.Errorf("no client chunk has Counter in %v", keys(out.Client))
	}
}

func keys(m map[string][]byte) []string {
	var ks []string
	for k := range m {
		ks = append(ks, k)
	}
	return ks
}

// TestServeViteSSRServers: create-vite-extra's Express SSR template, and
// the same server.js on Fastify, Koa, Hono's node adapter and h3 (srvx). og follows
// the scripts (build: the client and --ssr builds; preview: `cross-env
// NODE_ENV=production node server`) and runs server.js per request: the
// framework renders through dist/server/entry-server.js, serves dist/client
// from the packaged files and compresses (gzip; brotli where the framework
// prefers it, as under Node).
func TestServeViteSSRServers(t *testing.T) {
	for _, c := range []struct {
		name string
		// indexIsTemplate: /index.html is the client template file (served
		// by the framework's static middleware), not a rendered page.
		indexIsTemplate bool
		brotli          bool // chosen for "br, gzip"
		header          string
		compress        bool // the server compresses responses
		publicFiles     bool // the server serves dist/client's public files (favicon.svg)
	}{
		{"vite-ssr-express", true, true, "X-Powered-By", true, true},
		{"vite-ssr-fastify", true, true, "", true, true},
		{"vite-ssr-koa", true, true, "", true, true},
		{"vite-ssr-hono", false, false, "", true, true},
		{"vite-ssr-h3", false, false, "", false, false}, // h3 v2: srvx's serve
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := fixture(t, c.name)
			out, err := Build(context.Background(), dir, Options{Log: t.Logf})
			if err != nil {
				t.Fatal(err)
			}
			if out.Env["NODE_ENV"] != "production" || out.ClientDir != "dist/client" {
				t.Errorf("env %v, client dir %q", out.Env, out.ClientDir)
			}
			// Vite externalizes dependencies for SSR: the output imports them.
			if entry := string(out.Files["dist/server/entry-server.js"]); !strings.Contains(entry, `from "react-dom/server"`) {
				t.Errorf("dist/server/entry-server.js does not import react-dom/server:\n%.600s", entry)
			}
			fsys := fstest.MapFS{}
			for name, b := range out.Payload() {
				fsys[name] = &fstest.MapFile{Data: b}
			}
			h, _, err := app.Handler(fsys, slog.New(slog.NewTextHandler(io.Discard, nil)))
			if err != nil {
				t.Fatal(err)
			}
			srv := httptest.NewServer(h)
			defer srv.Close()
			client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
			get := func(p, encoding string) (*http.Response, string) {
				t.Helper()
				req, _ := http.NewRequest("GET", srv.URL+p, nil)
				if encoding != "" {
					req.Header.Set("Accept-Encoding", encoding)
				}
				resp, err := client.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				defer resp.Body.Close()
				var r io.Reader = resp.Body
				switch resp.Header.Get("Content-Encoding") {
				case "gzip":
					if r, err = gzip.NewReader(resp.Body); err != nil {
						t.Fatal(err)
					}
				case "br":
					r = brotli.NewReader(resp.Body)
				}
				b, err := io.ReadAll(r)
				if err != nil {
					t.Fatalf("GET %s (%s): %v", p, encoding, err)
				}
				return resp, string(b)
			}
			resp, body := get("/", "")
			for _, w := range []string{`<div id="root"><link rel="preload" as="image" href="/assets/hero-`, `Count is <!-- -->0`, `<link rel="stylesheet" crossorigin href="/assets/index-`} {
				if !strings.Contains(body, w) {
					t.Errorf("GET / lacks %s\n%.1500s", w, body)
				}
			}
			if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") || strings.Contains(body, "<!--app-html-->") {
				t.Errorf("GET /: %d %v", resp.StatusCode, resp.Header)
			}
			if c.header != "" && resp.Header.Get(c.header) == "" {
				t.Errorf("GET / lacks %s", c.header)
			}
			want := map[bool]string{true: "gzip"}[c.compress]
			if resp, gz := get("/", "gzip"); resp.Header.Get("Content-Encoding") != want || gz != body {
				t.Errorf("GET / with gzip: %q (want %q), body equal %v", resp.Header.Get("Content-Encoding"), want, gz == body)
			}
			if c.brotli {
				want = "br"
			}
			if resp, br := get("/", "br, gzip"); resp.Header.Get("Content-Encoding") != want || br != body {
				t.Errorf("GET / with br, gzip: %q (want %s), body equal %v", resp.Header.Get("Content-Encoding"), want, br == body)
			}
			if resp, svg := get("/favicon.svg", ""); c.publicFiles && (resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "image/svg+xml") || svg != string(out.Files["dist/client/favicon.svg"])) {
				t.Errorf("GET /favicon.svg: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
			}
			css := regexp.MustCompile(`/assets/index-[^"]+\.css`).FindString(body)
			if resp, got := get(css, ""); resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/css") || got != string(out.Files["dist/client"+css]) {
				t.Errorf("GET %s: %d %s", css, resp.StatusCode, resp.Header.Get("Content-Type"))
			}
			if _, html := get("/index.html", ""); strings.Contains(html, "<!--app-html-->") != c.indexIsTemplate {
				t.Errorf("GET /index.html: template %v, want %v:\n%.600s", !c.indexIsTemplate, c.indexIsTemplate, html)
			}
			if resp, other := get("/some/route", ""); resp.StatusCode != 200 || other != body {
				t.Errorf("GET /some/route: %d", resp.StatusCode)
			}
		})
	}
}

// TestBuildAstroStatic: Astro 7 (output "static"): og runs Astro's build()
// with the project's astro.config passed inline. The pages are files: the
// component's scoped style inlined in <head>, its <script> bundled and
// inlined; /about serves about/index.html.
func TestBuildAstroStatic(t *testing.T) {
	out := buildFixture(t, "astro-static")
	if out.AppType != "mpa" || out.ServerJS != "" {
		t.Errorf("appType %q, server %v", out.AppType, out.ServerJS != "")
	}
	html := string(out.Client["index.html"])
	for _, w := range []string{`<h1 id="hello">Hello from Astro</h1>`, `<li class="item">ALPHA</li>`, `]{color:#016302}`, `<script type="module">`, `count ${++`} {
		if !strings.Contains(html, w) {
			t.Errorf("index.html lacks %s\n%s", w, html)
		}
	}
	fsys := fstest.MapFS{}
	for name, b := range out.Payload() {
		fsys[name] = &fstest.MapFile{Data: b}
	}
	h, _, err := app.Handler(fsys, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	defer srv.Close()
	for _, p := range []string{"/about", "/about/"} {
		resp, body := fetch(t, srv, "GET", p, "")
		if resp.StatusCode != 200 || !strings.Contains(body, `<h1 id="about">About this site</h1>`) {
			t.Errorf("GET %s: %d\n%.400s", p, resp.StatusCode, body)
		}
	}
	// The inlined page script carries no define banner (it uses none).
	if strings.Contains(html, "__og_define_") {
		t.Errorf("index.html has define banner statements\n%s", html)
	}
	// A Markdown page: satteri (emnapi WebAssembly) with its frontmatter
	// layout, GFM and Shiki highlighting (inlined Oniguruma WebAssembly).
	resp, body := fetch(t, srv, "GET", "/notes/", "")
	for _, w := range []string{"<title>Notes</title>", `<h1 id="notes-in-markdown">Notes in <em>Markdown</em></h1>`, "<td>og</td>",
		`<input type="checkbox" checked disabled>`, `<span style="color:#F97583">const</span>`, `data-footnote-ref aria-describedby="footnote-label">1</a>`} {
		if resp.StatusCode != 200 || !strings.Contains(body, w) {
			t.Errorf("GET /notes/: %d lacks %s\n%.1500s", resp.StatusCode, w, body)
		}
	}
}

// TestServeAstroNode: Astro with @astrojs/node (standalone): its
// dist/server/entry.mjs is the server program. Per-request pages, a
// prerendered page (served by the adapter from dist/client), an API route
// (GET and POST, whose body the adapter streams from the IncomingMessage)
// and Astro's 404 page.
func TestServeAstroNode(t *testing.T) {
	out, err := Build(context.Background(), fixture(t, "astro-node"), Options{Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if out.Files["dist/server/entry.mjs"] == nil || out.ClientDir != "dist/client" {
		t.Fatalf("no server entry / client dir %q", out.ClientDir)
	}
	srv := serveWithPort(t, out)
	resp, body := fetch(t, srv, "GET", "/?q=og", "")
	if resp.StatusCode != 200 || !regexp.MustCompile(`<p id="q"[^>]*>q=og</p>`).MatchString(body) || !strings.Contains(body, "]{color:#030405}") {
		t.Errorf("GET /?q=og: %d\n%.1500s", resp.StatusCode, body)
	}
	if resp, body := fetch(t, srv, "GET", "/static/", ""); resp.StatusCode != 200 || !strings.Contains(body, "Prerendered 1970-01-01T00:00:00.000Z") {
		t.Errorf("GET /static/: %d\n%.400s", resp.StatusCode, body)
	}
	if resp, body := fetch(t, srv, "GET", "/api/hello?name=og", ""); resp.StatusCode != 200 || strings.TrimSpace(body) != `{"hello":"og"}` {
		t.Errorf("GET /api/hello: %d %q", resp.StatusCode, body)
	}
	// Astro's origin check refuses a 127.0.0.1 origin under Node too (403);
	// the browser's view is localhost.
	req, _ := http.NewRequest("POST", srv.URL+"/api/hello", strings.NewReader(`{"a":1}`))
	req.Host = strings.Replace(req.URL.Host, "127.0.0.1", "localhost", 1)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "http://"+req.Host)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 201 || strings.TrimSpace(string(b)) != `{"received":{"a":1}}` {
		t.Errorf("POST /api/hello: %d %q", r.StatusCode, b)
	}
	if resp, body := fetch(t, srv, "GET", "/nope", ""); resp.StatusCode != 404 || !strings.Contains(body, "404: Not Found") {
		t.Errorf("GET /nope: %d", resp.StatusCode)
	}
}

// serveWithPort serves out with PORT set to the server's port before the
// handler is made, as og's binary sets it (server programs read it at
// startup: @astrojs/node builds request URLs with it, react-router-serve
// would otherwise probe for a free port).
func serveWithPort(t *testing.T, out *Output, env ...string) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(nil)
	port := strconv.Itoa(srv.Listener.Addr().(*net.TCPAddr).Port)
	t.Setenv("PORT", port)
	// More environment, NAME=value pairs; {url} is the server's URL.
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		t.Setenv(k, strings.ReplaceAll(v, "{url}", "http://127.0.0.1:"+port))
	}
	fsys := fstest.MapFS{}
	for name, b := range out.Payload() {
		fsys[name] = &fstest.MapFile{Data: b}
	}
	h, _, err := app.Handler(fsys, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	srv.Config.Handler = h
	srv.Start()
	t.Cleanup(srv.Close)
	return srv
}

// React Router 8 (framework mode): `react-router build` (Vite's builder
// with React Router's plugin, which loads the Vite config itself and
// compiles routes with a Vite dev server), prerendering through the start
// script's program, and that program (react-router-serve, Express) as
// the server.
func TestServeReactRouter(t *testing.T) {
	out, err := Build(context.Background(), fixture(t, "react-router"), Options{Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if out.ServerJS == "" || out.Files["build/client/about.data"] == nil || out.Files["build/client/about/index.html"] == nil {
		t.Fatalf("no server program or prerendered /about (%d files)", len(out.Files))
	}
	srv := serveWithPort(t, out)
	resp, body := fetch(t, srv, "GET", "/?q=og", "")
	if resp.StatusCode != 200 || !strings.Contains(body, "<title>og react-router</title>") || !strings.Contains(body, ">hello og</h1>") || !strings.Contains(body, `<p id="runtime">server</p>`) {
		t.Errorf("GET /?q=og: %d\n%.1500s", resp.StatusCode, body)
	}
	css := regexp.MustCompile(`href="(/assets/root-[^"]+\.css)"`).FindStringSubmatch(body)
	if css == nil {
		t.Fatalf("no stylesheet link")
	}
	// Assets: express.static with react-router-serve's immutable caching.
	if resp, body := fetch(t, srv, "GET", css[1], ""); resp.StatusCode != 200 || !strings.Contains(body, "color:#016302") || resp.Header.Get("Cache-Control") != "public, max-age=31536000, immutable" {
		t.Errorf("GET %s: %d %q %.200s", css[1], resp.StatusCode, resp.Header.Get("Cache-Control"), body)
	}
	if resp, body := fetch(t, srv, "GET", "/about/", ""); resp.StatusCode != 200 || !strings.Contains(body, `<p id="built">prerendered</p>`) {
		t.Errorf("GET /about/: %d\n%.600s", resp.StatusCode, body)
	}
	if resp, body := fetch(t, srv, "GET", "/about.data", ""); resp.StatusCode != 200 || !strings.Contains(body, `"prerendered"`) {
		t.Errorf("GET /about.data: %d %q", resp.StatusCode, body)
	}
	if resp, body := fetch(t, srv, "GET", "/api/hello?name=og", ""); resp.StatusCode != 200 || strings.TrimSpace(body) != `{"hello":"og"}` {
		t.Errorf("GET /api/hello: %d %q", resp.StatusCode, body)
	}
	if resp, body := fetch(t, srv, "POST", "/api/hello", `{"a":1}`); resp.StatusCode != 200 || strings.TrimSpace(body) != `{"echoed":{"a":1}}` {
		t.Errorf("POST /api/hello: %d %q", resp.StatusCode, body)
	}
	// A form action, as the browser posts it without JavaScript.
	r, err := http.Post(srv.URL+"/todos", "application/x-www-form-urlencoded", strings.NewReader("title=milk"))
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 200 || !strings.Contains(string(b), `<p id="added">added <!-- -->MILK</p>`) {
		t.Errorf("POST /todos: %d\n%.2000s", r.StatusCode, b)
	}
	if resp, _ := fetch(t, srv, "GET", "/nope", ""); resp.StatusCode != 404 {
		t.Errorf("GET /nope: %d", resp.StatusCode)
	}
}

// SvelteKit 3 with @sveltejs/adapter-node: the build (its route analysis,
// prerendering and fallback in worker threads, which reload the Vite
// config), and `node build` as the server program: SSR with a server
// load, __data.json, a prerendered page, an endpoint, a form action.
func TestServeSvelteKit(t *testing.T) {
	out, err := Build(context.Background(), fixture(t, "sveltekit-node"), Options{Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if out.Files["build/index.js"] == nil || out.Files["build/prerendered/about.html"] == nil {
		t.Fatalf("no adapter output or prerendered /about (%d files)", len(out.Files))
	}
	// adapter-node assumes https unless a header names the protocol, as a
	// proxy in front of it would (x-forwarded-proto: http below).
	srv := serveWithPort(t, out, "PROTOCOL_HEADER=x-forwarded-proto")
	resp, body := fetch(t, srv, "GET", "/?q=og", "")
	if resp.StatusCode != 200 || !regexp.MustCompile(`<h1 class="brand svelte-[a-z0-9]+">hello og</h1>`).MatchString(body) || !strings.Contains(body, "<title>og sveltekit</title>") {
		t.Errorf("GET /?q=og: %d\n%.2000s", resp.StatusCode, body)
	}
	if resp, body := fetch(t, srv, "GET", "/__data.json?q=og", ""); resp.StatusCode != 200 || !strings.Contains(body, `"hello og"`) {
		t.Errorf("GET /__data.json: %d %s", resp.StatusCode, body)
	}
	if resp, body := fetch(t, srv, "GET", "/about", ""); resp.StatusCode != 200 || !strings.Contains(body, `<p id="built">prerendered</p>`) {
		t.Errorf("GET /about: %d\n%.600s", resp.StatusCode, body)
	}
	if resp, body := fetch(t, srv, "GET", "/api/hello?name=og", ""); resp.StatusCode != 200 || !strings.Contains(body, `"hello":"og"`) {
		t.Errorf("GET /api/hello: %d %s", resp.StatusCode, body)
	}
	if resp, body := fetch(t, srv, "POST", "/api/hello", `{"a":1}`); resp.StatusCode != 201 || strings.TrimSpace(body) != `{"echoed":{"a":1}}` {
		t.Errorf("POST /api/hello: %d %s", resp.StatusCode, body)
	}
	// A form action without JavaScript.
	req, _ := http.NewRequest("POST", srv.URL+"/?/visit", strings.NewReader("by=2"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", srv.URL)
	req.Header.Set("X-Forwarded-Proto", "http")
	req.Header.Set("Accept", "text/html")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 200 || !strings.Contains(r.Header.Get("Set-Cookie"), "visits=2") || !strings.Contains(string(b), `<p id="result">now 2</p>`) {
		t.Errorf("POST /?/visit: %d %q\n%.1500s", r.StatusCode, r.Header.Get("Set-Cookie"), b)
	}
	// Unknown paths render Kit's error page, whose entry shares Svelte's
	// component context with the rest (split server build).
	req, _ = http.NewRequest("GET", srv.URL+"/nope", nil)
	req.Header.Set("Accept", "text/html")
	r, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ = io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 404 || !strings.Contains(string(b), "<h1>404</h1>") {
		t.Errorf("GET /nope: %d\n%.800s", r.StatusCode, b)
	}
}

// Nuxt 4: what `nuxi build` runs (@nuxt/kit), Vite for the app, Nitro
// (Rollup's WebAssembly build) for the server and prerendering, and Nitro's
// node server (.output/server/index.mjs) as the server program.
//
// A previous build's client chunks are cleared first, as nuxi clears the
// build dir: Vite does not empty the client outDir, and Nitro copies all
// of it to .output/public.
func TestServeNuxt(t *testing.T) {
	dir := fixture(t, "nuxt")
	stale := filepath.Join(dir, "node_modules/.cache/nuxt/.nuxt/dist/client/_nuxt/og-stale.js")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("export {};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(stale) })
	out, err := Build(context.Background(), dir, Options{Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if out.Files[".output/public/_nuxt/og-stale.js"] != nil {
		t.Error("a previous build's chunk is in .output/public")
	}
	if out.Files[".output/server/index.mjs"] == nil || out.Files[".output/public/about/index.html"] == nil {
		t.Fatalf("no Nitro server or prerendered /about (%d files)", len(out.Files))
	}
	srv := serveWithPort(t, out)
	resp, body := fetch(t, srv, "GET", "/?q=og", "")
	if resp.StatusCode != 200 || !strings.Contains(body, "<title>og nuxt</title>") || !regexp.MustCompile(`<h1 class="brand" data-v-[0-9a-f]+>hello og</h1>`).MatchString(body) ||
		!regexp.MustCompile(`\.brand\[data-v-[0-9a-f]+\]\{color:#016304\}`).MatchString(body) {
		t.Errorf("GET /?q=og: %d\n%.2500s", resp.StatusCode, body)
	}
	if resp, body := fetch(t, srv, "GET", "/about", ""); resp.StatusCode != 200 || !strings.Contains(body, `<p id="built">prerendered</p>`) {
		t.Errorf("GET /about: %d\n%.800s", resp.StatusCode, body)
	}
	if resp, body := fetch(t, srv, "GET", "/about/_payload.json", ""); resp.StatusCode != 200 || !strings.Contains(body, "prerenderedAt") {
		t.Errorf("GET /about/_payload.json: %d %s", resp.StatusCode, body)
	}
	if resp, body := fetch(t, srv, "GET", "/api/hello?name=og", ""); resp.StatusCode != 200 || strings.TrimSpace(body) != `{"greeting":"hello og","runtime":"server"}` {
		t.Errorf("GET /api/hello: %d %s", resp.StatusCode, body)
	}
	if resp, body := fetch(t, srv, "POST", "/api/hello", `{"a":1}`); resp.StatusCode != 200 || strings.TrimSpace(body) != `{"echoed":{"a":1}}` {
		t.Errorf("POST /api/hello: %d %s", resp.StatusCode, body)
	}
	js := regexp.MustCompile(`"#entry":"(/_nuxt/[^"]+\.js)"`).FindStringSubmatch(body)
	if js == nil {
		t.Fatal("no entry script in the import map")
	}
	if resp, _ := fetch(t, srv, "GET", js[1], ""); resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/javascript") {
		t.Errorf("GET %s: %d %q", js[1], resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp, _ := fetch(t, srv, "GET", "/nope", ""); resp.StatusCode != 404 {
		t.Errorf("GET /nope: %d", resp.StatusCode)
	}
}

// SolidStart 2: Vite with SolidStart's plugins and Nitro 3 (nitro/vite),
// whose node server (.output/server/index.mjs) is the server program.
// "use server" functions are compiled to RPC stubs in the client bundle
// (SolidStart's filter resolves its "src/**" globs against the project
// path, which only matched once node:path was Node's), and a form action
// works without JavaScript (srvx reads the body through Readable.toWeb).
func TestServeSolidStart(t *testing.T) {
	out, err := Build(context.Background(), fixture(t, "solid-start"), Options{Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if out.Files[".output/server/index.mjs"] == nil {
		t.Fatalf("no Nitro server (%d files)", len(out.Files))
	}
	for name, f := range out.Files {
		if strings.HasPrefix(name, ".output/public/") && bytes.Contains(f, []byte("hello ${")) {
			t.Errorf("%s has the body of a \"use server\" function", name)
		}
	}
	srv := serveWithPort(t, out)
	resp, body := fetch(t, srv, "GET", "/?q=og", "")
	if resp.StatusCode != 200 || !strings.Contains(body, ">og solid-start</title>") || !strings.Contains(body, `<h1 class="brand">hello og</h1>`) ||
		!strings.Contains(body, `<p id="runtime">server</p>`) {
		t.Errorf("GET /?q=og: %d\n%.2500s", resp.StatusCode, body)
	}
	css := regexp.MustCompile(`<link href="(/_build/assets/[^"]+\.css)"`).FindAllStringSubmatch(body, -1)
	var styles string
	for _, m := range css {
		_, b := fetch(t, srv, "GET", m[1], "")
		styles += b
	}
	if !strings.Contains(styles, ".brand{color:#016305}") {
		t.Errorf("no .brand rule in %d stylesheet(s)", len(css))
	}
	if resp, body := fetch(t, srv, "GET", "/api/hello", ""); resp.StatusCode != 200 || strings.TrimSpace(body) != `{"hello":"world"}` {
		t.Errorf("GET /api/hello: %d %s", resp.StatusCode, body)
	}
	if resp, _ := fetch(t, srv, "GET", "/nope", ""); resp.StatusCode != 404 {
		t.Errorf("GET /nope: %d", resp.StatusCode)
	}
	_, about := fetch(t, srv, "GET", "/about", "")
	action := regexp.MustCompile(`action="(/_server\?id=[^"]+)"`).FindStringSubmatch(about)
	if action == nil {
		t.Fatalf("no form action on /about\n%.1500s", about)
	}
	req, _ := http.NewRequest("POST", srv.URL+html.UnescapeString(action[1]), strings.NewReader("text=plain"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Referer", srv.URL+"/about")
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if r.StatusCode != 302 || r.Header.Get("Location") != srv.URL+"/about" || !strings.Contains(r.Header.Get("Set-Cookie"), "%22result%22%3A%22PLAIN%22") {
		t.Errorf("POST %s: %d %q %q", action[1], r.StatusCode, r.Header.Get("Location"), r.Header.Get("Set-Cookie"))
	}
}

// Qwik City 1 on Vite 7 (Rollup's WebAssembly build): `qwik build` runs
// build.client and build.server (the node-server adapter); Qwik's optimizer
// falls back from its native binding to its wasm-bindgen build (externref
// tables), the static generation step runs in workers, and
// server/entry.node-server.js is the server program. Node's server gives
// the same pages but for hashes.
func TestServeQwikCity(t *testing.T) {
	out, err := Build(context.Background(), fixture(t, "qwik-city"), Options{Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	if out.Files["server/entry.node-server.js"] == nil {
		t.Fatalf("no node-server entry (%d files)", len(out.Files))
	}
	srv := serveWithPort(t, out)
	resp, body := fetch(t, srv, "GET", "/?q=og", "")
	for _, w := range []string{"<title q:head>og qwik</title>", ">hello og<", `<p id="runtime"`, ">server<", "{ color: #016306; }", `q:container="paused"`} {
		if resp.StatusCode != 200 || !strings.Contains(body, w) {
			t.Errorf("GET /?q=og: %d lacks %s\n%.1500s", resp.StatusCode, w, body)
		}
	}
	// The image import: vite-imagetools made its variant at build time
	// with og's sharp (the same <img> as Node's build).
	img := regexp.MustCompile(`<img [^>]*id="logo"[^>]*srcSet="(/assets/[^" ]+-logo\.webp) 120w" width="120" height="60"`).FindStringSubmatch(body)
	if img == nil {
		t.Errorf("no optimized logo <img> in /?q=og")
	} else if resp, b := fetch(t, srv, "GET", img[1], ""); resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "image/webp" || !strings.HasPrefix(b, "RIFF") {
		t.Errorf("GET %s: %d %q", img[1], resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp, body := fetch(t, srv, "GET", "/api/hello?name=og", ""); resp.StatusCode != 200 || strings.TrimSpace(body) != `{"hello":"og"}` {
		t.Errorf("GET /api/hello: %d %s", resp.StatusCode, body)
	}
	if resp, _ := fetch(t, srv, "GET", "/nope", ""); resp.StatusCode != 404 {
		t.Errorf("GET /nope: %d", resp.StatusCode)
	}
	_, about := fetch(t, srv, "GET", "/about/", "")
	action := regexp.MustCompile(`<form action="(\?qaction=[^"]+)"`).FindStringSubmatch(about)
	if action == nil {
		t.Fatalf("no form action on /about/\n%.1500s", about)
	}
	req, _ := http.NewRequest("POST", srv.URL+"/about/"+action[1], strings.NewReader("text=plain"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Origin", srv.URL)
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	if r.StatusCode != 200 || !regexp.MustCompile(`<p id="result"><!--t=\d+-->PLAIN<`).Match(b) {
		t.Errorf("POST %s: %d\n%.1500s", action[1], r.StatusCode, b)
	}
}

// The client bundle's npm packages, and only those, are credited; the
// payload carries them as notices.AppFile.
func TestBuildReactNotices(t *testing.T) {
	out := buildReact(t)
	var titles []string
	for _, s := range strings.Split(string(out.Notices), strings.Repeat("=", 80)+"\n")[1:] {
		titles = append(titles, strings.SplitN(s, "\n", 2)[0])
	}
	if got, want := strings.Join(titles, ", "), "react 19.3.0 (MIT), react-dom 19.3.0 (MIT), scheduler 0.28.0 (MIT)"; got != want {
		t.Errorf("notices list %s, want %s", got, want)
	}
	if !strings.Contains(string(out.Notices), "--- LICENSE\n\nMIT License\n\nCopyright (c) Meta Platforms, Inc. and affiliates.") {
		t.Error("notices lack React's license text")
	}
	if !bytes.Equal(out.Payload()[notices.AppFile], out.Notices) {
		t.Errorf("payload %s is not the notices", notices.AppFile)
	}
}

// TestServerProgramStartupOnce: a server program runs in every request's
// engine, but its startup output (Express's "Server started at …", printed
// from the listen callback) shows once per process, as under Node.
func TestServerProgramStartupOnce(t *testing.T) {
	out, err := Build(context.Background(), fixture(t, "vite-ssr-express"), Options{Log: t.Logf})
	if err != nil {
		t.Fatal(err)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout := os.Stdout
	os.Stdout = w
	var captured bytes.Buffer
	copied := make(chan struct{})
	go func() { _, _ = io.Copy(&captured, r); close(copied) }()
	srv := serveWithPort(t, out)
	for i := 0; i < 3; i++ {
		if resp, _ := fetch(t, srv, "GET", "/", ""); resp.StatusCode != 200 {
			t.Errorf("GET /: %d", resp.StatusCode)
		}
	}
	os.Stdout = stdout
	w.Close()
	<-copied
	if n := strings.Count(captured.String(), "Server started at"); n != 1 {
		t.Errorf("startup banner printed %d times:\n%s", n, captured.String())
	}
}
