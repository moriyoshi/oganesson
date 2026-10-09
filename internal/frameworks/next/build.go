// Package next builds a Next.js App Router project into a single server
// bundle for Oganesson's Goja runtime, reusing Next's own edge templates and
// runtime modules. Routes are compiled as Next's edge runtime would compile
// them; esbuild replaces webpack, and Go code replaces Next's loaders.
package next

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"github.com/microsoft/TypeScript/tsc/ogapi"
	"github.com/moriyoshi/oganesson/internal/nativegen"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/moriyoshi/oganesson/internal/bundle/rsc"
	"github.com/moriyoshi/oganesson/internal/gojatarget"
	"github.com/moriyoshi/oganesson/internal/jshost"
	"github.com/moriyoshi/oganesson/internal/licenses"
	"github.com/moriyoshi/oganesson/runtime/app"
	"github.com/moriyoshi/oganesson/runtime/isr"
	"github.com/moriyoshi/oganesson/runtime/kv"
	"github.com/moriyoshi/oganesson/runtime/natives"
	"github.com/moriyoshi/oganesson/runtime/nodeshim"
)

// FilesDir is the payload directory of the traced project files.
const FilesDir = "files"

// draftCookie is Next's draft mode cookie; its valid value is the build's
// preview mode id (prerender-manifest preview.previewModeId).
const (
	draftCookie   = "__prerender_bypass"
	draftValueEnv = "__NEXT_PREVIEW_MODE_ID"
)

// Meta is out's meta.json (app.MetaFile).
func (out *Output) Meta() (app.Meta, error) {
	meta := app.Meta{
		Format: app.FormatVersion, Framework: "next", BuildID: out.BuildID, Env: out.Env,
		HasMiddleware: out.HasMiddleware || out.LocaleDetection, BasePath: out.BasePath,
		TrailingSlash: out.TrailingSlash,
		CacheBypass:   &app.CacheBypass{Cookie: draftCookie, ValueEnv: draftValueEnv},
	}
	if out.Images != nil {
		if err := json.Unmarshal(out.Images, &meta.Images); err != nil {
			return meta, fmt.Errorf("images config: %w", err)
		}
	}
	for _, r := range out.Routes {
		meta.Routes = append(meta.Routes, r.Pathname)
	}
	if len(out.Files) > 0 {
		meta.Files = FilesDir
	}
	meta.PrismaMigrate = out.PrismaMigrate
	return meta, nil
}

// Output is a built server.
type Output struct {
	ServerJS string            // Goja-ready script defining globalThis.__ogHandler
	Env      map[string]string // process.env values the server needs at runtime
	Routes   []Route
	BuildID  string
	// Static holds files served under /_next/ (e.g. "static/chunks/x.js").
	Static map[string][]byte
	// Public holds the project's public/ directory, served from the root.
	Public map[string][]byte
	// Files are the project files the server reads at run time (file
	// tracing, trace.go), by path relative to the project; the runtime
	// mounts them at its working directory.
	Files map[string][]byte
	// HasMiddleware reports a proxy.ts/middleware.ts; it then runs in front
	// of the page cache on every request.
	HasMiddleware bool
	// Notices are the licenses of the npm packages bundled into the server
	// and browser code (notices.AppFile), nil when none.
	Notices []byte
	// Images is next.config's images (the image optimizer's settings) and
	// BasePath its basePath.
	Images   json.RawMessage
	BasePath string
	// Locales and DefaultLocale are next.config's i18n (Pages Router);
	// LocaleDetection: the root locale redirect runs ahead of the page cache.
	Locales         []string
	DefaultLocale   string
	LocaleDetection bool
	// TrailingSlash is next.config's trailingSlash: pages are prerendered and
	// served under the slash form, and the other form 308-redirects to it.
	TrailingSlash bool
	// PrismaMigrate, when set, is the app-startup `migrate deploy` (its
	// packaged files are already merged into Files).
	PrismaMigrate *app.PrismaMigrate
}

// PrerenderRoutes are the routes og build prerenders: every route except
// the internal not-found and error entries, static pages once per locale
// ("/fr/about"; the default locale unprefixed), as next build generates
// each locale's pages. Dynamic routes list their paths themselves.
func (out *Output) PrerenderRoutes() []app.PrerenderRoute {
	var routes []app.PrerenderRoute
	for _, r := range out.Routes {
		if r.Pathname == "/_not-found" || r.Page == "/_error" {
			continue
		}
		pathname := r.Pathname
		// trailingSlash: prerender the canonical slash form, as next build
		// does, so the request-time redirect lands on a cache HIT. Dynamic
		// patterns (filled per path) and the status pages keep their shape.
		if !strings.Contains(pathname, "[") && pathname != "/404" && pathname != "/500" {
			pathname = out.canonicalSlash(pathname)
		}
		routes = append(routes, app.PrerenderRoute{Pathname: pathname, Page: r.Page})
		if !r.Pages || r.IsRoute || strings.Contains(r.Pathname, "[") || r.Pathname == "/404" || r.Pathname == "/500" {
			continue
		}
		for _, l := range out.Locales {
			if l == out.DefaultLocale {
				continue
			}
			p := "/" + l
			if r.Pathname != "/" {
				p += r.Pathname
			}
			routes = append(routes, app.PrerenderRoute{Pathname: out.canonicalSlash(p), Page: r.Page})
		}
	}
	return routes
}

// canonicalSlash is p in the form next build prerenders under trailingSlash:
// a non-root path gains a trailing slash when trailingSlash is set.
func (out *Output) canonicalSlash(p string) string {
	if out.TrailingSlash && p != "/" && !strings.HasSuffix(p, "/") {
		return p + "/"
	}
	return p
}

// Options configure a build.
type Options struct {
	Log func(format string, args ...any)
	// Readable leaves the browser bundle and stylesheets unminified (the
	// server bundle never is), for inspecting the output.
	Readable bool
}

type builder struct {
	dir, appDir, nextDir string
	templatesDir         string
	config               map[string]any
	defines              map[string]string
	clientDefines        map[string]string
	clientMain           string // browser entry, relative to /_next/
	log                  func(format string, args ...any)
	minify               bool
	middleware           *middleware
	host                 *jshost.Host       // runs Next's build-time helpers
	plugins              *jshost.PluginHost // og.config.* esbuild plugins, if any
	// hooks are next.config's webpack() hook results, by target ("server",
	// "client"; webpackhook.go).
	hooks map[string]*jshost.WebpackHook
	// missing are imports of names their modules lack, read as undefined
	// (missingexports.go).
	missing           missingImports
	serverChain       *jshost.LoadChain // plugin onLoad transforms for server modules
	clientChain       *jshost.LoadChain // … and for browser modules
	routes            []Route
	pages             *pagesInfo               // the pages directory, if any
	pagesClient       string                   // the pages browser bundle, relative to /_next/
	pagesFiles        map[string][]string      // __BUILD_MANIFEST.pages
	traceBuild        bool                     // esbuild: Node's built-ins external (trace.go)
	decoratorMetadata string                   // the tsconfig when it sets experimentalDecorators and emitDecoratorMetadata
	nodeFiles         sync.Map                 // package files resolved with Node's conditions (resolve)
	nativePlugin      api.Plugin               // the project's og:<name> modules
	metadataRoutes    map[string]metadataRoute // metadata file routes by page
	trees             map[string]*TreeResult
	exts              []string
	buildID           string
	env               map[string]string

	mu      sync.Mutex
	clients map[string]*clientModule // by absolute path
	actions map[string]*actionModule // "use server" modules by absolute path

	variantsOnce sync.Once
	variants     map[string]string // browser-variant swaps by resolved path

	cssOnce                 sync.Once
	css                     *cssState
	serverMeta, browserMeta metaGraph
	entryCSSFiles           map[string]any
	media                   map[string][]byte // static image imports and fonts, by static/ path
	fonts                   map[string]*fontModule
	fontManifest            map[string][]string // next/font manifest "app": entry -> preloaded files
	appUsingSizeAdjust      bool
	licenses                *licenses.Collector // npm packages the shipped bundles read
	// prismaPush, when set, is a `prisma db push` in the build script: og pushes
	// the schema at app startup (schemaPush) rather than applying migrations.
	// Set by codegen, read by prismaMigrate in finish.
	prismaPush *prismaPushConfig
}

// prismaPushConfig records a build-script `prisma db push`.
type prismaPushConfig struct {
	acceptDataLoss bool // --accept-data-loss: schemaPush force (apply destructive changes)
}

type clientModule struct {
	Path    string
	ID      string
	Exports []string
}

// Build compiles the Next.js project in dir.
func Build(ctx context.Context, dir string, opts Options) (*Output, error) {
	if opts.Log == nil {
		opts.Log = func(string, ...any) {}
	}
	dir, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	b := &builder{dir: dir, log: opts.Log, minify: !opts.Readable, trees: map[string]*TreeResult{}, clients: map[string]*clientModule{}, actions: map[string]*actionModule{}, licenses: &licenses.Collector{}}
	if b.nextDir, err = findPackage(dir, "next"); err != nil {
		return nil, err
	}
	b.templatesDir = filepath.Join(b.nextDir, "dist", "esm", "build", "templates")
	for _, d := range []string{"app", "src/app"} {
		if st, err := os.Stat(filepath.Join(dir, d)); err == nil && st.IsDir() {
			b.appDir = filepath.Join(dir, d)
			break
		}
	}

	// The project's native Go modules (og/<name>).
	if b.nativePlugin, err = nativegen.Plugin(dir); err != nil {
		return nil, err
	}
	// Code generators the scripts run before next build (prisma generate).
	if err := b.codegen(ctx, opts.Log); err != nil {
		return nil, err
	}
	// tsconfig.json's legacy decorators with metadata (load).
	if tsconfig := filepath.Join(dir, "tsconfig.json"); fileExists(tsconfig) {
		if cfg, _ := ogapi.LoadConfig(tsconfig, dir); cfg != nil && cfg.ExperimentalDecorators && cfg.EmitDecoratorMetadata {
			b.decoratorMetadata = tsconfig
		}
	}
	// next.config loads under the bundler choice next build would make
	// (bundler.go).
	host := &jshost.Host{Dir: dir, Env: bundlerEnv(dir)}
	b.host = host
	setup, err := host.LoadNext(ctx)
	if err != nil {
		return nil, err
	}
	b.config = setup.Config
	// The build's JS host: og.config.* plugins (if any) and PostCSS.
	experimental, _ := b.config["experimental"].(map[string]any)
	cfg := jshost.FindConfig(dir)
	if b.plugins, err = host.StartPlugins(cfg, jshost.HostOptions{PostCSS: &jshost.PostCSSOptions{
		NextDir:          dir,
		Root:             dir,
		DisablePresetEnv: experimental["disablePostcssPresetEnv"] == true,
		UseLightningcss:  experimental["useLightningcss"] == true,
	}, SassOptions: sassOptions(b.config), NextConfigFile: jshost.FindNextConfig(dir), LightningCSSFeatures: mapOf(experimental["lightningCssFeatures"])}); err != nil {
		return nil, err
	}
	defer b.plugins.Close()
	if cfg != "" {
		opts.Log("using %s", filepath.Base(cfg))
	}
	b.exts = DefaultPageExtensions
	if pe, ok := b.config["pageExtensions"].([]any); ok && len(pe) > 0 {
		b.exts = nil
		for _, e := range pe {
			b.exts = append(b.exts, fmt.Sprint(e))
		}
	}
	b.defines = setup.EdgeDefines
	b.clientDefines = setup.ClientDefines
	b.defines["global"] = "globalThis"
	// Webpack's web/edge targets mock these (ncc-compiled deps use them).
	b.defines["__dirname"] = `"/"`
	b.defines["__filename"] = `"/index.js"`

	if err := b.runWebpackHooks(opts.Log); err != nil {
		return nil, err
	}

	if err := b.scanApp(); err != nil {
		return nil, err
	}
	if b.pages, err = scanPages(dir, b.exts, b.probeExports); err != nil {
		return nil, err
	}
	if b.pages != nil {
		b.routes = append(b.routes, b.pages.routes()...)
	}
	if b.appDir == "" && b.pages == nil {
		return nil, fmt.Errorf("next: no app or pages directory in %s", dir)
	}
	if err := b.checkAppPageConflicts(dir); err != nil {
		return nil, err
	}

	if b.middleware, err = b.findMiddleware(ctx, host); err != nil {
		return nil, err
	}
	return b.finish(ctx, dir, setup, opts)
}

// scanApp collects the app directory's routes, metadata routes and loader
// trees (nothing without an app directory).
func (b *builder) scanApp() error {
	if b.appDir == "" {
		return nil
	}
	appPaths, err := ScanAppPaths(b.appDir, b.exts)
	if err != nil {
		return err
	}
	b.routes = append(GroupRoutes(appPaths), Route{Page: notFoundRouteEntry, Pathname: notFoundRoute, AppPaths: []string{notFoundRouteEntry}})
	// Metadata files (favicon.ico, sitemap.ts, …) are served as route handlers.
	mrs, err := scanMetadataRoutes(b.appDir, b.exts)
	if err != nil {
		return err
	}
	b.metadataRoutes = map[string]metadataRoute{}
	for _, m := range mrs {
		b.metadataRoutes[m.Page] = m
		b.routes = append(b.routes, Route{Page: m.Page, Pathname: NormalizeAppPath(m.Page), AppPaths: []string{m.Page}, IsRoute: true})
	}
	all := append(appPaths, notFoundRouteEntry)
	exp, _ := b.config["experimental"].(map[string]any)
	globalNotFound := exp["globalNotFound"] == true
	for _, r := range b.routes {
		if r.IsRoute {
			continue // route handlers have no loader tree
		}
		t, err := BuildTree(b.appDir, r.Page, r.AppPaths, all, b.exts, b.basePath(), globalNotFound)
		if err != nil {
			return err
		}
		b.trees[r.Page] = t
	}
	return nil
}

// finish runs the builds: server pass 1, the browser bundles, server pass 2.
func (b *builder) finish(ctx context.Context, dir string, setup *jshost.NextSetup, opts Options) (*Output, error) {
	var err error

	if b.buildID == "" {
		b.buildID = randomHex(12)
	}
	b.env = map[string]string{
		"__NEXT_BUILD_ID":                    b.buildID,
		"NEXT_SERVER_ACTIONS_ENCRYPTION_KEY": randomB64(32),
		"__NEXT_PREVIEW_MODE_ID":             randomHex(16),
		"__NEXT_PREVIEW_MODE_SIGNING_KEY":    randomHex(32),
		"__NEXT_PREVIEW_MODE_ENCRYPTION_KEY": randomHex(32),
		"NEXT_RUNTIME":                       "edge",
	}
	// Variables from .env* files are part of the server's environment.
	for k, v := range setup.Env {
		if _, ok := b.env[k]; !ok {
			b.env[k] = v
		}
	}

	// Pass 1 discovers "use client" modules; pass 2 emits the SSR registry
	// and manifests that reference them.
	_, meta, err := b.esbuild()
	if err != nil {
		return nil, err
	}
	if b.serverMeta, err = parseMetafile(meta, b.dir); err != nil {
		return nil, err
	}
	static := map[string][]byte{}
	if b.appDir != "" {
		opts.Log("found %d client component module(s), %d server action module(s)", len(b.clients), len(b.actions))
		clientFile, clientCode, err := b.buildClient()
		if err != nil {
			return nil, err
		}
		b.clientMain = clientFile
		static[clientFile] = clientCode
		opts.Log("browser bundle %s: %d KB", clientFile, len(clientCode)/1024)
		if b.entryCSSFiles, err = b.entryCSS(b.serverMeta, b.browserMeta); err != nil {
			return nil, err
		}
	}
	if b.pages != nil {
		file, code, err := b.buildPagesClient()
		if err != nil {
			return nil, err
		}
		b.pagesClient = file
		static[file] = code
		opts.Log("pages browser bundle %s: %d KB", file, len(code)/1024)
		if b.pagesFiles, err = b.pagesManifest(); err != nil {
			return nil, err
		}
		manifests, err := b.clientBuildManifests(ctx, b.pagesFiles)
		if err != nil {
			return nil, err
		}
		for k, v := range manifests {
			static[k] = v
		}
	}
	code, metafile, err := b.esbuild()
	if err != nil {
		return nil, err
	}
	traceStart := time.Now()
	b.traceBuild = true
	_, traceMeta, err := b.esbuild()
	b.traceBuild = false
	if err != nil {
		return nil, fmt.Errorf("file tracing: %w", err)
	}
	bundled := time.Since(traceStart)
	traced, err := b.traceFiles(ctx, traceMeta, metafile)
	if err == nil {
		opts.Log("traced %d project file(s) the server reads (bundle %s, trace %s)", len(traced), bundled.Round(time.Millisecond), (time.Since(traceStart) - bundled).Round(time.Millisecond))
	}
	if err != nil {
		return nil, err
	}
	public, err := readTree(filepath.Join(dir, "public"))
	if err != nil {
		return nil, err
	}
	// Attribute npm packages whose CSS or font bytes a JS-host plugin inlined
	// into the output (Tailwind's preflight @import "tailwindcss", a package's
	// own next/font/local files), which esbuild never saw as bundle inputs,
	// and note fonts downloaded from Google Fonts at build time.
	if b.plugins != nil {
		for _, f := range b.plugins.AssetReads() {
			b.licenses.AddFile(f)
		}
	}
	b.noteGoogleFonts()
	notices, err := b.licenses.Text()
	if err != nil {
		return nil, err
	}
	out := &Output{
		ServerJS: code, Env: b.env, Routes: b.routes, BuildID: b.buildID,
		Notices:       notices,
		Static:        b.staticFiles(static),
		Public:        public,
		Files:         traced,
		HasMiddleware: b.middleware != nil,
		Images:        mustRaw(b.config["images"]),
		BasePath:      b.basePath(),
		TrailingSlash: b.config["trailingSlash"] == true,
	}
	if i18n, ok := b.config["i18n"].(map[string]any); ok && b.pages != nil {
		for _, l := range anySlice(i18n["locales"]) {
			if s, ok := l.(string); ok {
				out.Locales = append(out.Locales, s)
			}
		}
		out.DefaultLocale, _ = i18n["defaultLocale"].(string)
		out.LocaleDetection = i18n["localeDetection"] != false
	}
	// A Prisma project with migrations: package `migrate deploy` to run at
	// app startup (the migrator program, the migrations, and the WASM engine).
	pm, pmFiles, err := b.prismaMigrate(ctx, opts.Log)
	if err != nil {
		return nil, err
	}
	if pm != nil {
		out.PrismaMigrate = pm
		if out.Files == nil {
			out.Files = map[string][]byte{}
		}
		for k, v := range pmFiles {
			out.Files[k] = v
		}
	}
	return out, nil
}

func mapOf(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func sassOptions(config map[string]any) map[string]any {
	m, _ := config["sassOptions"].(map[string]any)
	return m
}

const skipMarker = "og-next-skip"

func (b *builder) esbuild() (code, metafile string, err error) {
	opts := api.BuildOptions{
		EntryPoints:   []string{"og:entry"},
		Bundle:        true,
		Write:         false,
		AbsWorkingDir: b.dir,
		LogLevel:      api.LogLevelSilent,
		Define:        b.defines,
		JSX:           api.JSXAutomatic,
		Metafile:      true,
		// Next's webpack resolve.extensions (esbuild's default has no .mjs:
		// Panda CSS generates styled-system/css/index.mjs).
		ResolveExtensions: nextResolveExtensions,
	}
	b.serverChain = &jshost.LoadChain{}
	user, err := b.userPlugins([]string{"all", "server", "webpack-server"}, "server", []string{string(LayerRSC), string(LayerSSR), string(LayerShared), string(LayerPages)}, b.serverChain)
	if err != nil {
		return "", "", err
	}
	if b.traceBuild {
		// The bundle file tracing reads (trace.go): Node's built-ins stay
		// imports, as in Next's compiled server code, so the tracer knows
		// path.join(process.cwd(), ...) for what it is.
		user = append([]api.Plugin{{Name: "og-trace-builtins", Setup: func(pb api.PluginBuild) {
			pb.OnResolve(api.OnResolveOptions{Filter: `^(node:)?[a-z_/]+$`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
				if name := strings.TrimPrefix(a.Path, "node:"); nodeshim.Builtins[name] {
					return api.OnResolveResult{Path: "node:" + name, External: true}, nil
				}
				return api.OnResolveResult{}, nil
			})
		}}}, user...)
	}
	opts.Plugins = append(user,
		b.fontPlugin(),
		b.wasmPlugin(),
		// Native packages (sharp, better-sqlite3) as Oganesson built-ins.
		natives.Plugin(),
		// Next's edge runtime provides buffer/events/async_hooks/assert/util;
		// webpack supplies Buffer from Next's compiled copy.
		// unenv's stream classes and StringDecoder are stubs; Next's own
		// polyfills (what its browser bundles get) work, and Pages API routes
		// need them: the req/res mocks are streams, and the body parser
		// (raw-body, iconv-lite) decodes with StringDecoder.
		nodeshim.Plugin(nodeshim.Options{Overrides: nextNodeOverrides, ResolveDir: b.dir}),
		// The project's Go functions (og:<name>, internal/nativegen).
		b.nativePlugin,
		kv.Plugin(),
		isr.Plugin(),
		b.plugin(),
	)
	gojatarget.Apply(&opts)
	if b.traceBuild {
		opts.Format = api.FormatESModule // imports of the built-ins stay imports
	}
	opts.Platform = api.PlatformBrowser
	opts.MainFields = []string{"edge-light", "browser", "module", "main"}
	opts.Conditions = []string{"edge-light", "worker", "browser"}
	res := b.buildTolerant(opts)
	if len(res.Errors) > 0 {
		return "", "", buildError("server", res.Errors)
	}
	if err := b.licenses.AddMetafile(res.Metafile, b.dir); err != nil {
		return "", "", err
	}
	for _, f := range res.OutputFiles {
		if !strings.HasSuffix(f.Path, ".css") { // the JS output ("<stdout>")
			return gojatarget.Finish(string(f.Contents)), res.Metafile, nil
		}
	}
	return "", "", fmt.Errorf("next: server build produced no JS")
}

func (b *builder) plugin() api.Plugin {
	return api.Plugin{
		Name: "og-next",
		Setup: func(pb api.PluginBuild) {
			pb.OnResolve(api.OnResolveOptions{Filter: `^og:entry$`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
				return api.OnResolveResult{Path: "og:entry", Namespace: string(LayerSSR)}, nil
			})
			pb.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: emptyNamespace}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
				empty := "module.exports = {};"
				return api.OnLoadResult{Contents: &empty, Loader: api.LoaderJS}, nil
			})
			pb.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: missingNamespace}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
				code := fmt.Sprintf("throw Object.assign(new Error(%q), { code: \"MODULE_NOT_FOUND\" });", "Cannot find module '"+a.Path+"'")
				if spec, ok := strings.CutPrefix(a.Path, missingImportPrefix); ok {
					code = fmt.Sprintf("throw Object.assign(new Error(%q), { code: \"ERR_MODULE_NOT_FOUND\" });", "Cannot find package '"+spec+"'")
				}
				return api.OnLoadResult{Contents: &code, Loader: api.LoaderJS}, nil
			})
			// Patched files reached outside the layers (the buffer override
			// requires Next's compiled buffer from the nodeshim namespace):
			// the same patches (patches.go).
			pb.OnLoad(api.OnLoadOptions{Filter: sourcePatchFilter(), Namespace: "file"}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
				src, err := os.ReadFile(a.Path)
				if err != nil {
					return api.OnLoadResult{}, err
				}
				code, err := patchSource(a.Path, string(src))
				return api.OnLoadResult{Contents: &code, Loader: api.LoaderJS, ResolveDir: filepath.Dir(a.Path)}, err
			})
			for _, layer := range []Layer{LayerSSR, LayerRSC, LayerShared, LayerPages} {
				layer := layer
				pb.OnResolve(api.OnResolveOptions{Filter: `.*`, Namespace: string(layer)}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
					return b.resolve(pb, layer, a)
				})
				pb.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: string(layer)}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
					return b.load(layer, a)
				})
			}
		},
	}
}

func (b *builder) resolve(pb api.PluginBuild, layer Layer, a api.OnResolveArgs) (api.OnResolveResult, error) {
	if a.PluginData == skipMarker {
		return api.OnResolveResult{}, nil
	}
	spec := a.Path
	if strings.HasPrefix(spec, nodeDefaultPrefix) {
		// A Node-mode default import (nodeinterop.go): a CommonJS target's
		// module.exports, else the module as imported.
		// Resolved through every plugin, as the plain specifier would be
		// (og's substitutes for better-sqlite3 and the like come first).
		r := pb.Resolve(strings.TrimPrefix(spec, nodeDefaultPrefix), api.ResolveOptions{Kind: a.Kind, Importer: a.Importer, Namespace: a.Namespace, ResolveDir: a.ResolveDir, With: a.With})
		if len(r.Errors) > 0 {
			return api.OnResolveResult{}, fmt.Errorf("%s (from %s)", r.Errors[0].Text, a.Importer)
		}
		if r.External || !filepath.IsAbs(r.Path) || !isCommonJSFile(r.Path) {
			out := api.OnResolveResult{Path: r.Path, Namespace: r.Namespace, External: r.External, Suffix: r.Suffix, PluginData: r.PluginData}
			if !r.SideEffects {
				out.SideEffects = api.SideEffectsFalse
			}
			return out, nil
		}
		return api.OnResolveResult{Path: nodeDefaultPrefix + r.Path, Namespace: r.Namespace}, nil
	}
	if spec == nodeshim.GlobalsModule || spec == kv.ModuleName || spec == isr.CacheHandlerModule {
		return api.OnResolveResult{}, nil // handled by the nodeshim plugin
	}
	// next.config webpack()'s resolve.alias (webpackhook.go).
	if a.PluginData != aliasedMarker {
		if r, ok, err := b.resolveHookAlias(pb, "server", a, false); ok || err != nil {
			return r, err
		}
	}
	if strings.HasPrefix(spec, "og:") {
		ns := LayerSSR
		if strings.HasPrefix(spec, "og:app-page:") || strings.HasPrefix(spec, "og:edge-route:") || strings.HasPrefix(spec, "og:app-route:") ||
			strings.HasPrefix(spec, "og:metadata-") || spec == actionsModuleID {
			ns = LayerRSC // route handlers live entirely in the RSC layer
		} else if strings.HasPrefix(spec, pagesRouteModule) || strings.HasPrefix(spec, pagesAPIRouteModule) ||
			strings.HasPrefix(spec, pagesAPIBridgeModule) || strings.HasPrefix(spec, pagesAPINodeModule) {
			ns = LayerPages
		}
		return api.OnResolveResult{Path: spec, Namespace: string(ns)}, nil
	}
	// bcrypt -> bcryptjs and the like (runtime/natives).
	if s, ok := natives.Alias(spec); ok {
		spec = s
	}
	spec = aliasSpecifier(layer, spec)

	var resolved string
	// Package "sideEffects": false lets esbuild drop unused imports; modules
	// resolved by Oganesson itself keep their side effects.
	sideEffects := api.SideEffectsTrue
	if layer == LayerRSC && isBare(spec) {
		if p, ok := resolveReactServerExport(spec, a.ResolveDir); ok {
			resolved = p
		}
	}
	// Packages imported by the app's code or by other packages resolve as
	// on next start's Node runtime (Node's conditions: TypeORM's browser
	// build disables its drivers); Next's and React's own server code keeps
	// the edge conditions og's templates are built on.
	// A module resolved this way resolves its relative imports the same
	// way: esbuild would apply the package's "browser" field to them
	// (TypeORM's maps ./index.js to its browser build).
	nodeImporter := b.nodeResolved(a.Importer)
	if resolved == "" && (isBare(spec) && !edgePackage(spec) && !inEdgePackage(a.Importer) || nodeImporter && (strings.HasPrefix(spec, ".") || filepath.IsAbs(spec))) {
		esm := a.Kind == api.ResolveJSImportStatement || a.Kind == api.ResolveJSDynamicImport
		if p, err := jshost.ResolveNode(spec, a.ResolveDir, esm); err == nil && filepath.IsAbs(p) {
			resolved = p
			if strings.Contains(p, "/node_modules/") {
				b.nodeFiles.Store(p, true)
			}
		}
	}
	res := api.ResolveResult{}
	if resolved == "" {
		res = pb.Resolve(spec, api.ResolveOptions{ResolveDir: a.ResolveDir, Kind: a.Kind, Importer: a.Importer, PluginData: skipMarker})
		if len(res.Errors) > 0 {
			// A require() or import() of a package that is not installed
			// fails when it runs, as in Node, where code catches it: pg's
			// optional pg-native and pg-cloudflare (Next leaves pg external,
			// so Node loads it from node_modules), cosmiconfig's
			// import("typescript"). Static imports still must resolve.
			if a.Kind == api.ResolveJSRequireCall && isBare(spec) {
				return api.OnResolveResult{Path: spec, Namespace: missingNamespace}, nil
			}
			if a.Kind == api.ResolveJSDynamicImport && isBare(spec) {
				return api.OnResolveResult{Path: missingImportPrefix + spec, Namespace: missingNamespace}, nil
			}
			return api.OnResolveResult{}, fmt.Errorf("%s (from %s)", res.Errors[0].Text, a.Importer)
		}
		if res.External || res.Namespace != "file" {
			return api.OnResolveResult{Path: res.Path, Namespace: res.Namespace, External: res.External}, nil
		}
		resolved = res.Path
		if !res.SideEffects {
			sideEffects = api.SideEffectsFalse
		}
	}
	if target, ok := aliasResolved(layer, b.nextDir, resolved); ok {
		r := pb.Resolve(aliasSpecifier(layer, target), api.ResolveOptions{ResolveDir: b.dir, Kind: a.Kind, PluginData: skipMarker})
		if len(r.Errors) > 0 {
			return api.OnResolveResult{}, fmt.Errorf("%s (alias of %s)", r.Errors[0].Text, a.Path)
		}
		resolved = r.Path
		sideEffects = api.SideEffectsTrue
		if !r.SideEffects {
			sideEffects = api.SideEffectsFalse
		}
	}
	return api.OnResolveResult{
		Path:        resolved,
		Namespace:   string(layerFor(layer, resolved)),
		SideEffects: sideEffects,
	}, nil
}

// missingNamespace holds the modules of packages a require() names but
// that are not installed: each throws Node's MODULE_NOT_FOUND when required.
const missingNamespace = "og-missing"

// missingImportPrefix marks a missing module named by import(): Node's
// error for it is ERR_MODULE_NOT_FOUND's.
const missingImportPrefix = "import:"

// nodeResolved reports an importer that was resolved as Node resolves
// (resolve), from its esbuild path ("rsc:/abs/path" or "/abs/path").
func (b *builder) nodeResolved(importer string) bool {
	if i := strings.Index(importer, ":"); i > 0 && !strings.Contains(importer[:i], "/") {
		importer = importer[i+1:]
	}
	_, ok := b.nodeFiles.Load(importer)
	return ok
}

// edgePackages are Next, React and the packages their server code is
// built from: resolved with the edge conditions og's server bundle targets
// (ARCHITECTURE: the bundle runs Next's edge templates).
var edgePackages = map[string]bool{
	"next": true, "react": true, "react-dom": true, "scheduler": true, "styled-jsx": true,
	"server-only": true, "client-only": true, "react-server-dom-webpack": true,
}

func packageName(spec string) string {
	parts := strings.SplitN(spec, "/", 3)
	if strings.HasPrefix(spec, "@") && len(parts) >= 2 {
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
}

// edgePackage reports a specifier naming one of edgePackages (or @next/*).
func edgePackage(spec string) bool {
	name := packageName(spec)
	return edgePackages[name] || strings.HasPrefix(name, "@next/")
}

// inEdgePackage reports an importer inside one of edgePackages.
func inEdgePackage(importer string) bool {
	if i := strings.Index(importer, ":"); i > 0 && !strings.Contains(importer[:i], "/") {
		importer = importer[i+1:] // "rsc:/abs/path"
	}
	i := strings.LastIndex(importer, "/node_modules/")
	if i < 0 {
		return false
	}
	return edgePackage(importer[i+len("/node_modules/"):])
}

func isBare(spec string) bool {
	return spec != "" && spec[0] != '.' && spec[0] != '/' && !strings.Contains(spec, ":")
}

func (b *builder) load(layer Layer, a api.OnLoadArgs) (api.OnLoadResult, error) {
	if target, ok := strings.CutPrefix(a.Path, nodeDefaultPrefix); ok {
		code := "export default require(" + jsonStr(target) + ");\n"
		return api.OnLoadResult{Contents: &code, Loader: api.LoaderJS, ResolveDir: filepath.Dir(target)}, nil
	}
	if strings.HasPrefix(a.Path, "og:") {
		code, err := b.virtual(a.Path)
		if err != nil {
			return api.OnLoadResult{}, err
		}
		dir := b.templatesDir
		if strings.HasPrefix(a.Path, "og:metadata-") {
			dir = b.dir // generated next-metadata-*-loader output
		}
		return api.OnLoadResult{Contents: &code, Loader: api.LoaderJS, ResolveDir: dir}, nil
	}
	if b.isCSS(a.Path) {
		code, err := b.cssModuleSource(a.Path)
		return api.OnLoadResult{Contents: &code, Loader: api.LoaderJS}, err
	}
	// Plugin transforms (og.config.*) run first; Oganesson processes their output.
	transformed, handled, err := b.serverChain.Load(api.OnLoadArgs{Path: a.Path, Namespace: "file", PluginData: a.PluginData})
	if err != nil || len(transformed.Errors) > 0 {
		return transformed, err
	}
	loader, ok := loaderFor(a.Path)
	var src []byte
	if handled {
		src = []byte(*transformed.Contents)
		if transformed.Loader != api.LoaderNone && transformed.Loader != api.LoaderDefault {
			loader, ok = transformed.Loader, true
		}
		if _, js := jsLoaders[loader]; !js {
			return transformed, nil // not JS (e.g. text, json): nothing for Oganesson to do
		}
	} else {
		if !ok {
			if code, ok, err := b.staticImageSource(a.Path); ok || err != nil {
				return api.OnLoadResult{Contents: &code, Loader: api.LoaderJS}, err
			}
			// Other assets have no server-side meaning.
			empty := "export {};"
			return api.OnLoadResult{Contents: &empty, Loader: api.LoaderJS}, nil
		}
		if src, err = os.ReadFile(a.Path); err != nil {
			return api.OnLoadResult{}, err
		}
	}
	code := stripTurbopackAttributes(string(src))
	if _, js := jsLoaders[loader]; js && !strings.Contains(a.Path, "/node_modules/") {
		code = typeofWindow(a.Path, expandComputedImports(a.Path, code), "undefined")
	}
	if (loader == api.LoaderJS || loader == api.LoaderJSX) && isNodeModeESM(a.Path) {
		code = rewriteNodeDefaultImports(a.Path, code)
	}
	if _, js := jsLoaders[loader]; js {
		code = b.rewriteMissingImports(a.Path, code)
	}
	if code, err = patchSource(a.Path, code); err != nil {
		return api.OnLoadResult{}, err
	}
	// import.meta.url / dirname / filename: the module file's, as Next's
	// bundlers write them into server code (Prisma's generated client sets
	// __dirname from import.meta.url).
	if _, js := jsLoaders[loader]; js {
		code = jshost.ReplaceImportMeta(a.Path, code, a.Path)
	}
	if loader != api.LoaderJSON {
		if code, err = b.transformFonts(a.Path, code); err != nil {
			return api.OnLoadResult{}, err
		}
	}
	if loader != api.LoaderJSON && useServerRe.Match(src) {
		if layer == LayerRSC && inlineDirectiveRe.Match(src) {
			// Inline "use cache" functions of a "use server" module (its
			// top-level "use server" functions stay module actions).
			if code, err = b.transformInlineActions(a.Path, code, "", true); err != nil {
				return api.OnLoadResult{}, err
			}
		}
		m, err := b.addActions(a.Path)
		if err != nil {
			return api.OnLoadResult{}, err
		}
		if layer == LayerRSC {
			code = serverActionSource(code, m)
		} else {
			code, loader = clientActionSource(m), api.LoaderJS
		}
	} else if layer == LayerRSC && loader != api.LoaderJSON && rsc.HasUseClient(src) {
		m, err := b.addClient(a.Path)
		if err != nil {
			return api.OnLoadResult{}, err
		}
		code, loader = clientProxy(m), api.LoaderJS
	} else if loader != api.LoaderJSON && !strings.Contains(a.Path, "/node_modules/") && (inlineDirectiveRe.Match(src) || useCacheFileRe.Match(src)) {
		if layer != LayerRSC {
			if inlineUseServerRe.Match(src) {
				return api.OnLoadResult{}, fmt.Errorf("next: %s: It is not allowed to define inline \"use server\" annotated Server Actions in Client Components.", a.Path)
			}
			return api.OnLoadResult{}, fmt.Errorf("next: %s: \"use cache\" functions run on the server; a Client Component cannot define them.", a.Path)
		}
		fileCache := ""
		if m := useCacheFileRe.FindSubmatch(src); m != nil {
			fileCache = string(m[1])
		}
		if code, err = b.transformInlineActions(a.Path, code, fileCache, false); err != nil {
			return api.OnLoadResult{}, err
		}
	}
	if (loader == api.LoaderTS || loader == api.LoaderTSX) && b.decoratorMetadata != "" && !strings.Contains(a.Path, "/node_modules/") && decoratorRe.MatchString(code) {
		// Legacy decorators with design:* metadata (tsconfig's
		// emitDecoratorMetadata, which Next's SWC honors and esbuild does
		// not emit; TypeORM reads column types from it): TypeScript-Go
		// transpiles the file as tsc would.
		js, diags := ogapi.TranspileDecorators(context.Background(), b.decoratorMetadata, b.dir, a.Path, code)
		if len(diags) > 0 && js == "" {
			return api.OnLoadResult{}, fmt.Errorf("next: %s: %s", a.Path, diags[0].Message)
		}
		code = js
		if loader == api.LoaderTSX {
			loader = api.LoaderJSX
		} else {
			loader = api.LoaderJS
		}
	}
	return api.OnLoadResult{Contents: &code, Loader: loader, ResolveDir: filepath.Dir(a.Path)}, nil
}

// decoratorRe finds what may be a decorator: "@name" starting a line, or a
// parameter's.
var decoratorRe = regexp.MustCompile(`(?m)^[ \t]*@[A-Za-z_$]|[(,][ \t]*@[A-Za-z_$]`)

var jsLoaders = map[api.Loader]bool{api.LoaderJS: true, api.LoaderJSX: true, api.LoaderTS: true, api.LoaderTSX: true}

var loaders = map[string]api.Loader{
	".js": api.LoaderJS, ".mjs": api.LoaderJS, ".cjs": api.LoaderJS, ".jsx": api.LoaderJSX,
	".ts": api.LoaderTS, ".mts": api.LoaderTS, ".cts": api.LoaderTS, ".tsx": api.LoaderTSX,
	".json": api.LoaderJSON,
}

// nextResolveExtensions are next build's webpack resolve.extensions, in
// its order (.wasm aside: og's wasm plugin resolves those imports).
var nextResolveExtensions = []string{".js", ".mjs", ".tsx", ".ts", ".jsx", ".json"}

// loaderFor is the esbuild loader of a source file, as Next's SWC parses
// it: the project's own JavaScript may contain JSX (getParserOptions in
// next/dist/build/swc/options.js enables jsx for every non-TypeScript
// file), packages' may not.
func loaderFor(path string) (api.Loader, bool) {
	l, ok := loaders[filepath.Ext(path)]
	if l == api.LoaderJS && !strings.Contains(path, "/node_modules/") {
		l = api.LoaderJSX
	}
	return l, ok
}

func (b *builder) addClient(path string) (*clientModule, error) {
	b.mu.Lock()
	if m, ok := b.clients[path]; ok {
		b.mu.Unlock()
		return m, nil
	}
	b.mu.Unlock()
	exports, err := b.probeExports(path)
	if err != nil {
		return nil, err
	}
	if src, err := os.ReadFile(path); err == nil && loaders[filepath.Ext(path)] != api.LoaderNone && !hasModuleSyntax(path, string(src)) {
		// CommonJS (esbuild reports only "default"): also the names Node's
		// cjs-module-lexer would find, so require("./ga").GoogleAnalytics is
		// a reference too (esbuild copies a module's own properties; it
		// cannot ask a whole-module proxy).
		seen := map[string]bool{}
		for _, n := range exports {
			seen[n] = true
		}
		for _, n := range commonJSExports(path, 0) {
			if !seen[n] {
				exports = append(exports, n)
			}
		}
	}
	id, _ := filepath.Rel(b.dir, path)
	m := &clientModule{Path: path, ID: filepath.ToSlash(id), Exports: exports}
	b.mu.Lock()
	b.clients[path] = m
	b.mu.Unlock()
	return m, nil
}

// clientProxy mirrors next-flight-loader: references keyed by the module's
// absolute path, one per export.
func clientProxy(m *clientModule) string {
	var sb strings.Builder
	sb.WriteString(`import { registerClientReference, createClientModuleProxy } from "react-server-dom-webpack/server";` + "\n")
	if len(m.Exports) == 0 { // CommonJS: proxy the whole module
		fmt.Fprintf(&sb, "export default createClientModuleProxy(%s);\n", jsonStr(m.Path))
		return sb.String()
	}
	for _, name := range m.Exports {
		ref := fmt.Sprintf("registerClientReference(function () { throw new Error(%s); }, %s, %s)",
			jsonStr(fmt.Sprintf("Attempted to call %s() from the server but %s is on the client. It's not possible to invoke a client function from the server, it can only be rendered as a Component or passed to props of a Client Component.", name, name)),
			jsonStr(m.Path), jsonStr(name))
		if name == "default" {
			fmt.Fprintf(&sb, "export default %s;\n", ref)
		} else {
			fmt.Fprintf(&sb, "export const %s = %s;\n", name, ref)
		}
	}
	return sb.String()
}

func (b *builder) sortedClients() []*clientModule {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*clientModule, 0, len(b.clients))
	for _, m := range b.clients {
		out = append(out, m)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func findPackage(from, name string) (string, error) {
	for dir := from; ; dir = filepath.Dir(dir) {
		p := filepath.Join(dir, "node_modules", name)
		if st, err := os.Stat(filepath.Join(p, "package.json")); err == nil && !st.IsDir() {
			return p, nil
		}
		if filepath.Dir(dir) == dir {
			return "", fmt.Errorf("next: package %q is not installed (run og install)", name)
		}
	}
}

func mustRaw(v any) json.RawMessage {
	if v == nil {
		return nil
	}
	return json.RawMessage(mustJSON(v))
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func randomB64(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return base64.StdEncoding.EncodeToString(b)
}

func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return string(b)
}

// readTree reads every regular file under dir, keyed by slash path.
func readTree(dir string) (map[string][]byte, error) {
	out := map[string][]byte{}
	err := filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) && p == dir {
				return filepath.SkipDir
			}
			return err
		}
		if d.Type().IsRegular() {
			b, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(dir, p)
			out[filepath.ToSlash(rel)] = b
		}
		return nil
	})
	return out, err
}

// staticFiles are the files served under /_next/: the browser bundles plus
// compiled CSS and its assets.
func (b *builder) staticFiles(bundles map[string][]byte) map[string][]byte {
	out := map[string][]byte{}
	for k, v := range bundles {
		out[k] = v
	}
	st := b.cssState()
	st.mu.Lock()
	defer st.mu.Unlock()
	for k, v := range st.outputs {
		out[k] = v
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for k, v := range b.media {
		out[k] = v
	}
	return out
}

// userPlugins adapts og.config.* esbuild plugins for one of Oganesson's builds.
// Registrations for the "file" namespace also cover namespaces (Oganesson keeps
// server modules in layer namespaces); modules a plugin resolves inside a
// layer stay in that layer. CSS imports in JS builds stay with Oganesson.
func (b *builder) userPlugins(kinds []string, target string, namespaces []string, chain *jshost.LoadChain) ([]api.Plugin, error) {
	if b.plugins == nil {
		return nil, nil
	}
	layers := map[string]bool{}
	for _, n := range namespaces {
		layers[n] = true
	}
	return b.plugins.Plugins(jshost.BridgeOptions{
		Kinds: kinds,
		InitialOptions: map[string]any{
			"absWorkingDir": b.dir,
			"bundle":        true,
			"platform":      "browser",
			"og":            map[string]any{"target": target},
		},
		FileNamespaces: namespaces,
		MapResolve: func(importerNS string, r *api.OnResolveResult) {
			if layers[importerNS] && (r.Namespace == "" || r.Namespace == "file") && filepath.IsAbs(r.Path) {
				r.Namespace = string(layerFor(Layer(importerNS), r.Path))
			}
		},
		SkipLoad:  func(path string) bool { return target != "css" && b.isCSS(path) },
		LoadChain: chain,
	})
}

func anySlice(v any) []any {
	l, _ := v.([]any)
	return l
}
