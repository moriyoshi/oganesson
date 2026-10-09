package next

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/moriyoshi/oganesson/internal/jshost"
	"github.com/moriyoshi/oganesson/runtime/kv"
)

// The browser bundle: Next's own app bootstrap (app-next) plus every client
// component referenced from the server, behind a webpack-compatible runtime
// shim so React's Flight client and Next's router run unmodified.
//
// TODO: code splitting per route (chunks in clientModules).

// clientFallbacks mirrors webpack-config's resolve.fallback for the client:
// Node built-ins map to browser polyfills shipped in next/dist/compiled.
var clientFallbacks = map[string]string{
	"assert":         "next/dist/compiled/assert",
	"buffer":         "next/dist/compiled/buffer",
	"constants":      "next/dist/compiled/constants-browserify",
	"crypto":         "next/dist/compiled/crypto-browserify",
	"domain":         "next/dist/compiled/domain-browser",
	"http":           "next/dist/compiled/stream-http",
	"https":          "next/dist/compiled/https-browserify",
	"os":             "next/dist/compiled/os-browserify",
	"path":           "next/dist/compiled/path-browserify",
	"punycode":       "next/dist/compiled/punycode",
	"process":        "next/dist/build/polyfills/process",
	"querystring":    "next/dist/compiled/querystring-es3",
	"stream":         "next/dist/compiled/stream-browserify",
	"string_decoder": "next/dist/compiled/string_decoder",
	"sys":            "next/dist/compiled/util",
	"timers":         "next/dist/compiled/timers-browserify",
	"tty":            "next/dist/compiled/tty-browserify",
	"util":           "next/dist/compiled/util",
	"vm":             "next/dist/compiled/vm-browserify",
	"zlib":           "next/dist/compiled/browserify-zlib",
	"events":         "next/dist/compiled/events",
	"setImmediate":   "next/dist/compiled/setimmediate",
}

// webpackRuntime stands in for webpack's browser runtime (with
// __OG_PUBLIC_PATH__ replaced by output.publicPath, assetPrefix + "/_next/"). Client modules
// register themselves by id; chunk loading is a no-op because everything
// ships in one bundle.
const webpackRuntime = `
const modules = Object.create(null);
function __webpack_require__(id) {
  const m = modules[id];
  if (!m) throw new Error("og: unknown client module " + id);
  return m;
}
__webpack_require__.u = (chunkId) => chunkId;
__webpack_require__.k = (chunkId) => chunkId;
__webpack_require__.miniCssF = (chunkId) => chunkId;
__webpack_require__.p = __OG_PUBLIC_PATH__;
__webpack_require__.ogModules = modules;
globalThis.__webpack_require__ = __webpack_require__;
globalThis.__webpack_chunk_load__ = () => Promise.resolve();
globalThis.__webpack_public_path__ = __OG_PUBLIC_PATH__;
if (typeof globalThis.process === "undefined") globalThis.process = { env: {} };
`

// buildClient bundles the browser entry. It returns the bundle and its
// path relative to /_next/ (content-hashed).
func (b *builder) buildClient() (file string, code []byte, err error) {
	opts := api.BuildOptions{
		EntryPoints:       []string{"og:client-entry"},
		Bundle:            true,
		Write:             false,
		AbsWorkingDir:     b.dir,
		LogLevel:          api.LogLevelSilent,
		Define:            b.clientDefines,
		JSX:               api.JSXAutomatic,
		Format:            api.FormatIIFE,
		Platform:          api.PlatformBrowser,
		Target:            api.ES2020,
		MinifyWhitespace:  b.minify,
		MinifySyntax:      b.minify,
		MinifyIdentifiers: b.minify,
		LegalComments:     api.LegalCommentsNone,
		Metafile:          true,
		ResolveExtensions: nextResolveExtensions,
	}
	b.clientChain = &jshost.LoadChain{}
	user, err := b.userPlugins([]string{"all", "client", "webpack-client"}, "client", nil, b.clientChain)
	if err != nil {
		return "", nil, err
	}
	opts.Plugins = append(user, b.fontPlugin(), b.clientPlugin(LayerBrowser))
	res := b.buildTolerant(opts)
	if len(res.Errors) > 0 {
		return "", nil, buildError("client", res.Errors)
	}
	code = res.OutputFiles[0].Contents
	if b.browserMeta, err = parseMetafile(res.Metafile, b.dir); err != nil {
		return "", nil, err
	}
	if err := b.licenses.AddMetafile(res.Metafile, b.dir); err != nil {
		return "", nil, err
	}
	sum := sha256.Sum256(code)
	return fmt.Sprintf("static/chunks/main-app-%s.js", hex.EncodeToString(sum[:8])), code, nil
}

// buildPagesClient bundles the pages browser entry (pagesClientEntrySource)
// in the pages-browser layer.
func (b *builder) buildPagesClient() (file string, code []byte, err error) {
	opts := api.BuildOptions{
		EntryPoints:       []string{"og:pages-client-entry"},
		Bundle:            true,
		Write:             false,
		AbsWorkingDir:     b.dir,
		LogLevel:          api.LogLevelSilent,
		Define:            b.clientDefines,
		JSX:               api.JSXAutomatic,
		Format:            api.FormatIIFE,
		Platform:          api.PlatformBrowser,
		Target:            api.ES2020,
		MinifyWhitespace:  b.minify,
		MinifySyntax:      b.minify,
		MinifyIdentifiers: b.minify,
		LegalComments:     api.LegalCommentsNone,
		Metafile:          true,
		ResolveExtensions: nextResolveExtensions,
	}
	chain := &jshost.LoadChain{}
	user, err := b.userPlugins([]string{"all", "client", "webpack-client"}, "client", nil, chain)
	if err != nil {
		return "", nil, err
	}
	saved := b.clientChain
	b.clientChain = chain
	defer func() { b.clientChain = saved }()
	opts.Plugins = append(user, b.fontPlugin(), b.clientPlugin(LayerPagesBrowser))
	res := b.buildTolerant(opts)
	if len(res.Errors) > 0 {
		return "", nil, buildError("pages client", res.Errors)
	}
	if err := b.licenses.AddMetafile(res.Metafile, b.dir); err != nil {
		return "", nil, err
	}
	code = res.OutputFiles[0].Contents
	sum := sha256.Sum256(code)
	return fmt.Sprintf("static/chunks/pages-%s.js", hex.EncodeToString(sum[:8])), code, nil
}

func (b *builder) clientPlugin(layer Layer) api.Plugin {
	return api.Plugin{
		Name: "og-next-client",
		Setup: func(pb api.PluginBuild) {
			pb.OnResolve(api.OnResolveOptions{Filter: `.*`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
				if a.PluginData == skipMarker {
					return api.OnResolveResult{}, nil
				}
				if a.Path == kv.ModuleName {
					return api.OnResolveResult{}, fmt.Errorf("%s is server-only and cannot be imported from client code (imported by %s)", kv.ModuleName, a.Importer)
				}
				switch a.Path {
				case "og:client-entry", "og:pages-client-entry", "og:webpack-runtime", "og:client-registry", "private-next-instrumentation-client":
					return api.OnResolveResult{Path: a.Path, Namespace: "og-client"}, nil
				}
				return b.resolveBrowser(pb, layer, a)
			})
			pb.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: emptyNamespace}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
				empty := "module.exports = {};"
				return api.OnLoadResult{Contents: &empty, Loader: api.LoaderJS}, nil
			})
			pb.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: missingNamespace}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
				code := fmt.Sprintf("throw Object.assign(new Error(%q), { code: \"MODULE_NOT_FOUND\" });", "Cannot find module '"+a.Path+"'")
				return api.OnLoadResult{Contents: &code, Loader: api.LoaderJS}, nil
			})
			pb.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "og-client"}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
				var code string
				switch a.Path {
				case "og:client-entry":
					code = "import \"og:webpack-runtime\";\nimport \"og:client-registry\";\nimport \"next/dist/client/app-next\";\n"
				case "og:pages-client-entry":
					code = b.pagesClientEntrySource()
				case "og:webpack-runtime":
					code = strings.ReplaceAll(webpackRuntime, "__OG_PUBLIC_PATH__", jsonStr(b.assetPrefix()+"/_next/"))
				case "og:client-registry":
					code = b.browserRegistrySource()
				case "private-next-instrumentation-client":
					code = b.instrumentationClientSource()
				}
				return api.OnLoadResult{Contents: &code, Loader: api.LoaderJS, ResolveDir: b.dir}, nil
			})
			pb.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "file"}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
				if b.isCSS(a.Path) {
					code, err := b.cssModuleSource(a.Path)
					return api.OnLoadResult{Contents: &code, Loader: api.LoaderJS}, err
				}
				transformed, handled, err := b.clientChain.Load(a)
				if err != nil || len(transformed.Errors) > 0 {
					return transformed, err
				}
				if handled {
					loader := transformed.Loader
					if _, js := jsLoaders[loader]; !js {
						return transformed, nil
					}
					src := []byte(*transformed.Contents)
					code, err := b.transformFonts(a.Path, b.rewriteMissingImports(a.Path, typeofWindow(a.Path, stripTurbopackAttributes(string(src)), "object")))
					if err == nil {
						code, err = b.browserPageTransform(layer, a.Path, code)
					}
					if err != nil {
						return api.OnLoadResult{}, err
					}
					if useServerRe.Match(src) {
						m, err := b.addActions(a.Path)
						if err != nil {
							return api.OnLoadResult{}, err
						}
						code, loader = clientActionSource(m), api.LoaderJS
					}
					return api.OnLoadResult{Contents: &code, Loader: loader, ResolveDir: filepath.Dir(a.Path)}, nil
				}
				if loader, ok := loaderFor(a.Path); ok {
					src, err := os.ReadFile(a.Path)
					if err != nil {
						return api.OnLoadResult{}, err
					}
					code := stripTurbopackAttributes(string(src))
					if loader != api.LoaderJSON {
						code = b.rewriteMissingImports(a.Path, typeofWindow(a.Path, code, "object"))
						if code, err = b.transformFonts(a.Path, code); err != nil {
							return api.OnLoadResult{}, err
						}
						if code, err = b.browserPageTransform(layer, a.Path, code); err != nil {
							return api.OnLoadResult{}, err
						}
					}
					if loader != api.LoaderJSON && useServerRe.Match(src) {
						m, err := b.addActions(a.Path)
						if err != nil {
							return api.OnLoadResult{}, err
						}
						code, loader = clientActionSource(m), api.LoaderJS
					}
					return api.OnLoadResult{Contents: &code, Loader: loader}, nil
				}
				if code, ok, err := b.staticImageSource(a.Path); ok || err != nil {
					return api.OnLoadResult{Contents: &code, Loader: api.LoaderJS}, err
				}
				empty := "export {};"
				return api.OnLoadResult{Contents: &empty, Loader: api.LoaderJS}, nil
			})
		},
	}
}

func (b *builder) resolveBrowser(pb api.PluginBuild, layer Layer, a api.OnResolveArgs) (api.OnResolveResult, error) {
	// next.config webpack()'s resolve.alias (webpackhook.go).
	if a.PluginData != aliasedMarker {
		if r, ok, err := b.resolveHookAlias(pb, "client", a, false); ok || err != nil {
			return r, err
		}
	}
	spec := aliasSpecifier(layer, a.Path)
	res := pb.Resolve(spec, api.ResolveOptions{ResolveDir: a.ResolveDir, Kind: a.Kind, Importer: a.Importer, PluginData: skipMarker})
	if len(res.Errors) > 0 {
		// Then its resolve.fallback, for what does not resolve.
		if r, ok, err := b.resolveHookAlias(pb, "client", a, true); ok || err != nil {
			return r, err
		}
		name := strings.TrimPrefix(spec, "node:")
		fb, ok := clientFallbacks[name]
		if !ok {
			// A require() of a package that is not installed throws when it
			// runs, as webpack's missing optional modules do (framer-motion
			// requires @emotion/is-prop-valid inside a try).
			if a.Kind == api.ResolveJSRequireCall && isBare(spec) {
				return api.OnResolveResult{Path: spec, Namespace: missingNamespace}, nil
			}
			return api.OnResolveResult{}, fmt.Errorf("%s (from %s)", res.Errors[0].Text, a.Importer)
		}
		res = pb.Resolve(fb, api.ResolveOptions{ResolveDir: b.dir, Kind: a.Kind, PluginData: skipMarker})
		if len(res.Errors) > 0 {
			return api.OnResolveResult{}, fmt.Errorf("%s (fallback for %s)", res.Errors[0].Text, a.Path)
		}
	}
	if !res.External && (res.Namespace == "" || res.Namespace == "file") && !filepath.IsAbs(res.Path) {
		// A module the package's "browser" field disables ("buffer": false
		// in bn.js): an empty module, as in webpack.
		return api.OnResolveResult{Path: res.Path, Namespace: emptyNamespace}, nil
	}
	if target, ok := b.browserVariant(res.Path); ok {
		r := pb.Resolve(target, api.ResolveOptions{ResolveDir: b.dir, Kind: a.Kind, PluginData: skipMarker})
		if len(r.Errors) > 0 {
			return api.OnResolveResult{}, fmt.Errorf("%s (browser variant of %s)", r.Errors[0].Text, a.Path)
		}
		res = r
	}
	if target, ok := aliasResolved(layer, b.nextDir, res.Path); ok {
		r := pb.Resolve(aliasSpecifier(layer, target), api.ResolveOptions{ResolveDir: b.dir, Kind: a.Kind, PluginData: skipMarker})
		if len(r.Errors) > 0 {
			return api.OnResolveResult{}, fmt.Errorf("%s (alias of %s)", r.Errors[0].Text, a.Path)
		}
		res = r
	}
	se := api.SideEffectsTrue
	if !res.SideEffects {
		se = api.SideEffectsFalse
	}
	return api.OnResolveResult{Path: res.Path, Namespace: res.Namespace, External: res.External, SideEffects: se}, nil
}

// browserRegistrySource registers each client module under the id the
// server's Flight payload uses.
func (b *builder) browserRegistrySource() string {
	var sb strings.Builder
	clients := b.sortedClients()
	for i, m := range clients {
		fmt.Fprintf(&sb, "import * as c%d from %s;\n", i, jsonStr(cjsTwin(m.Path)))
	}
	sb.WriteString("const r = globalThis.__webpack_require__.ogModules;\n")
	for i, m := range clients {
		fmt.Fprintf(&sb, "r[%s] = c%d;\n", jsonStr(m.ID), i)
	}
	return sb.String()
}

// instrumentationClientSource resolves private-next-instrumentation-client
// to the user's instrumentation-client file, or nothing.
func (b *builder) instrumentationClientSource() string {
	for _, dir := range []string{b.dir, filepath.Join(b.dir, "src")} {
		for _, ext := range b.exts {
			p := filepath.Join(dir, "instrumentation-client."+ext)
			if _, err := os.Stat(p); err == nil {
				return fmt.Sprintf("module.exports = require(%s);\n", jsonStr(p))
			}
		}
	}
	return "module.exports = {};\n"
}

func buildError(what string, errs []api.Message) error {
	var sb strings.Builder
	for i, e := range errs {
		if i == 20 {
			fmt.Fprintf(&sb, "... and %d more\n", len(errs)-20)
			break
		}
		if e.Location != nil {
			fmt.Fprintf(&sb, "%s:%d:%d: ", e.Location.File, e.Location.Line, e.Location.Column)
		}
		sb.WriteString(e.Text + "\n")
	}
	return fmt.Errorf("next: %s build failed:\n%s", what, sb.String())
}

var browserVariantRe = regexp.MustCompile(`'([^']+)'`)

// browserVariant swaps next/dist modules that have a ".browser" sibling, as
// create-compiler-aliases does for the client compiler. The module list is
// read from the installed Next (dist/build/browser-variant-modules.js).
func (b *builder) browserVariant(resolved string) (string, bool) {
	b.variantsOnce.Do(func() {
		b.variants = map[string]string{
			// Production builds swap the instant-navigation testing lock for an inert shim.
			filepath.Join(b.nextDir, "dist", "client", "components", "segment-cache", "navigation-testing-lock.js"): "next/dist/client/components/segment-cache/navigation-testing-lock.disabled",
		}
		src, err := os.ReadFile(filepath.Join(b.nextDir, "dist", "build", "browser-variant-modules.js"))
		if err != nil {
			return
		}
		if i := strings.Index(string(src), "browserVariantModules = ["); i >= 0 {
			list := string(src[i:])
			list = list[:strings.Index(list, "]")]
			for _, m := range browserVariantRe.FindAllStringSubmatch(list, -1) {
				b.variants[filepath.Join(b.nextDir, "dist", filepath.FromSlash(m[1])+".js")] = "next/dist/" + m[1] + ".browser"
			}
		}
	})
	t, ok := b.variants[resolved]
	return t, ok
}

// emptyNamespace holds modules a package's "browser" field disables.
const emptyNamespace = "og-empty"
