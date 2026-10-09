// Package nodeshim provides Node.js built-in modules to bundles that run in
// Oganesson's Goja runtime, as an unenv preset: unenv (vendored) supplies every
// built-in with Node's complete API surface, and Oganesson overrides a few with
// real implementations (Go-backed fs, crypto and zlib, the engine's
// propagating AsyncLocalStorage, the build-time module loader).
package nodeshim

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
)

//go:embed js
var overrideFiles embed.FS

// unenv 2.x runtime and its pathe dependency, readable-stream and
// string_decoder, and Node's own lib files; see scripts/vendor-js.sh.
//
//go:embed all:vendor/unenv/dist/runtime all:vendor/pathe/dist all:vendor/readable-stream/lib all:vendor/string_decoder/lib all:vendor/node/lib all:vendor/minimatch/index.js all:vendor/http-parser-js/http-parser.js
var vendorFiles embed.FS

// vendored are built-ins provided by a vendored package instead of unenv:
// node:stream is readable-stream (Node's own implementation),
// node:string_decoder its string_decoder (unenv's are stubs), and
// node:path Node's lib/path.js (unenv's is pathe, which treats a backslash
// as a separator on posix and has no real win32).
var vendored = map[string]string{
	"stream":          "/readable-stream/lib/stream.js",
	"stream/promises": "/readable-stream/lib/stream/promises.js",
	"string_decoder":  "/string_decoder/lib/string_decoder.js",
	"path":            "/node/lib/path.js",
	// unenv's TracingChannel throws "not implemented" (ioredis traces its
	// commands through one).
	"diagnostics_channel": "/node/lib/diagnostics_channel.js",
	"path/posix":          "/og-path/posix",
	"path/win32":          "/og-path/win32",
	// Node's internal HTTP module (msw's interceptors import its
	// HTTPParser); unenv has none: js/http_common.js over http-parser-js.
	"_http_common": "/og-http-common",
}

// nodeInternals are the internal modules vendored Node lib files require:
// js/node_internals.js (with primordials) and js/node_stream_internals.js
// provide most, readable-stream (Node's streams) the stream utilities.
var nodeInternals = map[string]string{
	"internal/constants":                    "/og-node-internals",
	"internal/validators":                   "/og-node-internals",
	"internal/util":                         "/og-node-internals",
	"internal/fs/glob":                      "/og-node-internals",
	"internal/errors":                       "/og-node-stream-internals",
	"internal/encoding":                     "/og-node-stream-internals",
	"internal/util/types":                   "/og-node-stream-internals",
	"internal/streams/end-of-stream":        "/og-node-stream-internals",
	"internal/webstreams/readablestream":    "/og-node-stream-internals",
	"internal/webstreams/writablestream":    "/og-node-stream-internals",
	"internal/webstreams/queuingstrategies": "/og-node-stream-internals",
	"internal/fs/promises":                  "/og-node-stream-internals",
	"internal/fs/utils":                     "/og-node-stream-internals",
	"internal/url":                          "/og-node-stream-internals",
	"internal/deps/minimatch/index":         "/minimatch/index.js",
	"internal/streams/utils":                "/readable-stream/lib/internal/streams/utils.js",
	"internal/streams/destroy":              "/readable-stream/lib/internal/streams/destroy.js",
}

// internalFiles are the files behind the og-node-*internals paths.
var internalFiles = map[string]string{
	"/og-node-internals":        "js/node_internals.js",
	"/og-node-stream-internals": "js/node_stream_internals.js",
	"/og-http-common":           "js/http_common.js",
}

// overrides are Oganesson's implementations, which extend unenv's modules
// (imported from inside them as "unenv:node/<name>").
var overrides = map[string]string{
	"fs":             "fs.js",
	"fs/promises":    "fs_promises.js",
	"crypto":         "crypto.js",
	"zlib":           "zlib.js",
	"async_hooks":    "async_hooks.js",
	"events":         "events.js",
	"util":           "util.js",
	"module":         "module.js",
	"process":        "process.js",
	"http":           "http.js",
	"assert":         "assert.js",
	"assert/strict":  "assert_strict.js",
	"worker_threads": "worker_threads.js",
	"net":            "net.js",
	"tls":            "tls.js",
	"vm":             "vm.js",
}

// Builtins are the module names Node resolves without node_modules: every
// module unenv provides.
var Builtins = func() map[string]bool {
	m := map[string]bool{}
	_ = fs.WalkDir(vendorFiles, "vendor/unenv/dist/runtime/node", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".mjs") || strings.Contains(p, "/internal/") {
			return nil
		}
		name := strings.TrimSuffix(strings.TrimPrefix(p, "vendor/unenv/dist/runtime/node/"), ".mjs")
		m[name] = true
		return nil
	})
	m["_http_common"] = true
	return m
}()

// GlobalsModule is an import specifier whose module installs Node-style
// globals (global, Buffer). Import it before anything else.
const GlobalsModule = "og:node-globals"

// Options customize the plugin.
type Options struct {
	// Overrides maps a builtin name to an import specifier that provides it,
	// resolved from ResolveDir (e.g. "buffer" -> "next/dist/compiled/buffer").
	Overrides  map[string]string
	ResolveDir string
}

const (
	nsOverride = "og-node"  // Oganesson's override modules
	nsUnenv    = "og-unenv" // vendored unenv/pathe files (virtual paths)
	unenvRoot  = "/unenv/dist/runtime"
)

// Plugin resolves Node built-ins (with or without the "node:" prefix).
func Plugin(opts Options) api.Plugin {
	return api.Plugin{
		Name: "og-nodeshim",
		Setup: func(b api.PluginBuild) {
			builtin := func(spec string) (api.OnResolveResult, bool) {
				name := strings.TrimPrefix(spec, "node:")
				if !Builtins[name] {
					return api.OnResolveResult{}, false
				}
				if _, ok := opts.Overrides[name]; ok {
					return api.OnResolveResult{Path: name, Namespace: nsOverride}, true
				}
				if _, ok := overrides[name]; ok {
					return api.OnResolveResult{Path: name, Namespace: nsOverride}, true
				}
				if p, ok := vendored[name]; ok {
					return api.OnResolveResult{Path: p, Namespace: nsUnenv}, true
				}
				return api.OnResolveResult{Path: unenvRoot + "/node/" + name + ".mjs", Namespace: nsUnenv}, true
			}
			b.OnResolve(api.OnResolveOptions{Filter: `^(node:)?[a-z0-9_/]+$`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
				// unenv's fs imports node:fs/promises. Our fs/promises is our fs's
				// promises, and our fs extends unenv's fs: a cycle that leaves
				// whichever loads first half-built. unenv's own import gets
				// unenv's module; ours is composed on top.
				if name := strings.TrimPrefix(a.Path, "node:"); a.Namespace == nsUnenv && unenvOwn[name] {
					return api.OnResolveResult{Path: unenvRoot + "/node/" + name + ".mjs", Namespace: nsUnenv}, nil
				}
				r, _ := builtin(a.Path)
				return r, nil
			})
			// Overrides import vendored Node lib files this way
			// ("og-node-lib:internal/fs/streams").
			b.OnResolve(api.OnResolveOptions{Filter: `^og-node-lib:`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
				return api.OnResolveResult{Path: "/node/lib/" + strings.TrimPrefix(a.Path, "og-node-lib:") + ".js", Namespace: nsUnenv}, nil
			})
			// Overrides import the original unenv module this way.
			b.OnResolve(api.OnResolveOptions{Filter: `^unenv:node/`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
				return api.OnResolveResult{Path: unenvRoot + "/node/" + strings.TrimPrefix(a.Path, "unenv:node/") + ".mjs", Namespace: nsUnenv}, nil
			})
			// Imports inside vendored files: relative paths and pathe.
			b.OnResolve(api.OnResolveOptions{Filter: `.*`, Namespace: nsUnenv}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
				switch {
				case strings.HasPrefix(a.Path, "."):
					// One path per file: CommonJS requires omit the extension.
					p := path.Join(path.Dir(a.Importer), a.Path)
					for _, c := range []string{p, p + ".js", p + "/index.js"} {
						if st, err := fs.Stat(vendorFiles, "vendor"+c); err == nil && !st.IsDir() {
							p = c
							break
						}
					}
					return api.OnResolveResult{Path: p, Namespace: nsUnenv}, nil
				case a.Path == "pathe":
					return api.OnResolveResult{Path: "/pathe/dist/index.mjs", Namespace: nsUnenv}, nil
				case strings.HasPrefix(a.Path, "pathe/"):
					return api.OnResolveResult{Path: "/pathe/dist/" + strings.TrimPrefix(a.Path, "pathe/") + ".mjs", Namespace: nsUnenv}, nil
				// readable-stream's dependencies (the npm copies of built-ins).
				case a.Path == "process/" || a.Path == "string_decoder/":
					r, _ := builtin(strings.TrimSuffix(a.Path, "/"))
					return r, nil
				case a.Path == "safe-buffer":
					r, _ := builtin("buffer")
					return r, nil
				case a.Path == "abort-controller":
					return api.OnResolveResult{Path: "/abort-controller", Namespace: nsUnenv}, nil
				case nodeInternals[a.Path] != "":
					return api.OnResolveResult{Path: nodeInternals[a.Path], Namespace: nsUnenv}, nil
				// Vendored files by their virtual path (the shims and rewrites).
				case strings.HasPrefix(a.Path, "/readable-stream/") || strings.HasPrefix(a.Path, "/node/lib/") || strings.HasPrefix(a.Path, "/http-parser-js/"):
					return api.OnResolveResult{Path: a.Path, Namespace: nsUnenv}, nil
				}
				if r, ok := builtin(a.Path); ok {
					return r, nil
				}
				return api.OnResolveResult{}, nil
			})
			b.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: nsUnenv}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
				if a.Path == "/abort-controller" {
					s := "module.exports = { AbortController: globalThis.AbortController, AbortSignal: globalThis.AbortSignal };"
					return api.OnLoadResult{Contents: &s, Loader: api.LoaderJS}, nil
				}
				if f, ok := internalFiles[a.Path]; ok {
					src, err := overrideFiles.ReadFile(f)
					s := string(src)
					return api.OnLoadResult{Contents: &s, Loader: api.LoaderJS, ResolveDir: "/"}, err
				}
				// require("path/posix") is require("path").posix (and win32).
				if side, ok := strings.CutPrefix(a.Path, "/og-path/"); ok {
					s := fmt.Sprintf("module.exports = require(\"path\").%s;", side)
					return api.OnLoadResult{Contents: &s, Loader: api.LoaderJS, ResolveDir: "/"}, nil
				}
				src, err := vendorFiles.ReadFile("vendor" + a.Path)
				if err != nil {
					return api.OnLoadResult{}, fmt.Errorf("nodeshim: %s is not vendored", a.Path)
				}
				s := string(src)
				for _, r := range vendorRewrites {
					if r.file == "" || strings.HasSuffix(a.Path, r.file) {
						s = strings.ReplaceAll(s, r.from, r.to)
					}
				}
				return api.OnLoadResult{Contents: &s, Loader: api.LoaderJS, ResolveDir: "/"}, nil
			})
			b.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: nsOverride}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
				src, dir, err := overrideSource(a.Path, opts)
				if err != nil {
					return api.OnLoadResult{}, err
				}
				return api.OnLoadResult{Contents: &src, Loader: api.LoaderJS, ResolveDir: dir}, nil
			})
			b.OnResolve(api.OnResolveOptions{Filter: `^og:node-globals$`}, func(api.OnResolveArgs) (api.OnResolveResult, error) {
				return api.OnResolveResult{Path: "globals", Namespace: "og-node-globals"}, nil
			})
			b.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "og-node-globals"}, func(api.OnLoadArgs) (api.OnLoadResult, error) {
				src, err := overrideFiles.ReadFile("js/globals.js")
				s := string(src)
				return api.OnLoadResult{Contents: &s, Loader: api.LoaderJS}, err
			})
			b.OnResolve(api.OnResolveOptions{Filter: `^buffer$`, Namespace: "og-node-globals"}, func(api.OnResolveArgs) (api.OnResolveResult, error) {
				r, _ := builtin("buffer")
				return r, nil
			})
		},
	}
}

// unenvOwn are the built-ins unenv's own files import as unenv's modules
// rather than Oganesson's overrides (see the nsUnenv resolver).
var unenvOwn = map[string]bool{"fs/promises": true}

// vendorRewrites patch vendored unenv sources at load time (file "" means
// every file), so the vendored tree stays an unmodified copy.
var vendorRewrites = []struct{ file, from, to string }{
	// base64url (Node 15+), which unenv's Buffer lacks. Its base64 decoder
	// already reads the URL-safe alphabet and missing padding; the encoding
	// name is accepted and toString drops +/ and padding.
	{"/node/internal/buffer/buffer.mjs", "\t\tcase \"base64\":\n\t\tcase \"ucs2\":", "\t\tcase \"base64\":\n\t\tcase \"base64url\":\n\t\tcase \"ucs2\":"},
	{"/node/internal/buffer/buffer.mjs", `case "base64": return base64ToBytes(string).length;`, `case "base64url": case "base64": return base64ToBytes(string).length;`},
	{"/node/internal/buffer/buffer.mjs", `case "base64": return base64Slice(this, start, end);`, `case "base64": return base64Slice(this, start, end);
			case "base64url": return base64Slice(this, start, end).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");`},
	{"/node/internal/buffer/buffer.mjs", "\t\t\tcase \"base64\":\n // Warning", "\t\t\tcase \"base64url\":\n\t\t\tcase \"base64\":\n // Warning"},
	// Goja has no async generators, so esbuild lowers them to plain
	// functions and this idiom (used by unenv's events) dereferences
	// undefined. The equivalent object: an async iterator prototype.
	{"", "Object.getPrototypeOf(Object.getPrototypeOf(async function* () {}).prototype)", "({ [Symbol.asyncIterator]() { return this; } })"},
	// Node's util.debuglog(section) logs only when NODE_DEBUG names the
	// section (comma or space separated, * wildcards, any case); unenv's
	// always logs, which floods the console from readable-stream.
	{"/internal/util/log.mjs", "fn.enabled = true;", `fn.enabled = (globalThis.process?.env?.NODE_DEBUG || "").split(/[\s,]+/).some((p) => p && new RegExp("^" + p.replace(/[|\\{}()[\]^$+?.]/g, "\\$&").replace(/\*/g, ".*") + "$", "i").test(section));`},
	// unenv's AssertionError drops its message (super() without it); Node's
	// carries the given message, or one generated from the comparison.
	{"/node/assert.mjs", "\t\tsuper();\n\t\tthis.actual = options.actual;", "\t\tsuper(options.message != null ? String(options.message) : `${String(options.actual)} ${options.operator || \"==\"} ${String(options.expected)}`);\n\t\tthis.name = \"AssertionError\";\n\t\tthis.actual = options.actual;"},
	// Node's os.tmpdir() reads TMPDIR, TMP, TEMP (trailing slash trimmed),
	// else /tmp; unenv's always returns /tmp.
	{"/node/os.mjs", `export const tmpdir = () => "/tmp";`, `export const tmpdir = () => { const env = globalThis.process?.env || {}; const d = env.TMPDIR || env.TMP || env.TEMP || "/tmp"; return d.length > 1 && d.endsWith("/") ? d.slice(0, -1) : d; };`},
	// Node's lib files destructure primordials and call internalBinding,
	// parameters of Node's internal module wrapper.
	{"/node/lib/path.js", "} = primordials;", "} = require('internal/constants').primordials;"},
	{"/node/lib/internal/webstreams/adapters.js", "} = primordials;", "} = require('internal/constants').primordials;"},
	{"/node/lib/internal/webstreams/adapters.js", "internalBinding(", "require('internal/errors').internalBinding("},
	{"/node/lib/internal/fs/streams.js", "} = primordials;", "} = require('internal/constants').primordials;"},
	{"/node/lib/diagnostics_channel.js", "} = primordials;", "} = require('internal/constants').primordials;"},
	{"/node/lib/diagnostics_channel.js", "internalBinding(", "require('internal/errors').internalBinding("},
	// readable-stream leaves out Node's web stream adapters (Readable.toWeb,
	// Writable.fromWeb, Duplex.toWeb, ...); these are Node's own.
	{"/readable-stream/lib/internal/streams/readable.js", "webStreamsAdapters = {}", "webStreamsAdapters = require('/node/lib/internal/webstreams/adapters.js')"},
	{"/readable-stream/lib/internal/streams/writable.js", "webStreamsAdapters = {}", "webStreamsAdapters = require('/node/lib/internal/webstreams/adapters.js')"},
	{"/readable-stream/lib/internal/streams/duplex.js", "webStreamsAdapters = {}", "webStreamsAdapters = require('/node/lib/internal/webstreams/adapters.js')"},
	// Node only emits DEP0169 (url.parse) under --pending-deprecation;
	// unenv always warns, which every Tailwind build would print.
	{"/node/url.mjs", "if (!urlParseWarned) {", `if (!urlParseWarned && globalThis.process?.env?.NODE_PENDING_DEPRECATION === "1") {`},
}

func overrideSource(name string, opts Options) (src, resolveDir string, err error) {
	if spec, ok := opts.Overrides[name]; ok {
		return fmt.Sprintf("module.exports = require(%q);", spec), opts.ResolveDir, nil
	}
	f := overrides[name]
	b, err := overrideFiles.ReadFile("js/" + f)
	src = string(b)
	if f == "module.js" {
		names := make([]string, 0, len(Builtins))
		for n := range Builtins {
			names = append(names, n)
		}
		sort.Strings(names)
		list, _ := json.Marshal(names)
		src = strings.Replace(src, "__OG_BUILTINS__", string(list), 1)
	}
	return src + noInheritedDefault, "", err
}

// noInheritedDefault ends each override module: one that spreads unenv's
// module (an ES module's namespace) inherits its default export, a
// separate object of unenv's stubs, which interop helpers prefer
// (`"default" in m ? m.default : m`, as mlly reads fs). Node's built-ins
// have no default property.
const noInheritedDefault = `
if (module.exports != null && Object.prototype.hasOwnProperty.call(module.exports, "default")) {
  if (!Reflect.deleteProperty(module.exports, "default")) {
    const { default: _, ...rest } = module.exports; // a module namespace: copy it
    module.exports = rest;
  }
}
`
