// Package jshost runs build-time JavaScript (next.config, PostCSS plugins,
// prerendering) in Goja, with the same Web APIs as the production runtime
// plus a few Node built-ins.
package jshost

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/moriyoshi/oganesson/runtime/natives"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/grafana/sobek"
	"github.com/microsoft/TypeScript/tsc/ogapi"
	"github.com/moriyoshi/oganesson/internal/gojatarget"
	"github.com/moriyoshi/oganesson/internal/wasm"
	"github.com/moriyoshi/oganesson/runtime/engine"
	"github.com/moriyoshi/oganesson/runtime/nodeshim"
	"github.com/moriyoshi/oganesson/runtime/webapi"
)

// Host is a build-time JS environment rooted at a project directory.
type Host struct {
	natives *natives.Services // created by prepare (nativesMu)
	reads   *fileReads        // node_modules asset reads, set by prepare

	Dir string
	Env map[string]string
	Out io.Writer
	// Alias maps import specifiers to replacements (esbuild "alias").
	Alias map[string]string
	// Plugins run ahead of the built-in ones when bundling host code.
	Plugins []api.Plugin
	// Patch, if set, adjusts the source of each module loaded at run time
	// (require, computed import()), as Plugins can for bundled ones.
	Patch func(path, src string) (string, error)
	// TopLevelAwait makes Bundle (without a global name) bundle ES modules
	// with top-level await: ESM output run inside an async function, which
	// EvalWith awaits before reading its result (SvelteKit awaits optional
	// imports at module level).
	TopLevelAwait bool
	// Shared lists packages each bundle (with stdin contents) includes and
	// registers under SharedModules, so modules loaded at run time, in the
	// bundle's engine, get the bundle's instance (Vite, whose plugins import
	// it by computed specifier or file URL).
	Shared []string
	// WorkerShared, if set, replaces Shared in worker bundles: workers
	// that reload a config (SvelteKit's) need its packages shared, which
	// the host's own bundle cannot import whole (an `import *` defeats
	// tree shaking and evaluates every module of the package).
	WorkerShared []string

	// setup is EvalWith's engine setup, which worker engines get too.
	setup func(*engine.Engine) error
}

// nonCodeLoaders are the loaders of files that are not modules (empty).
var nonCodeLoaders = map[string]api.Loader{
	".html": api.LoaderEmpty, ".htm": api.LoaderEmpty, ".md": api.LoaderEmpty, ".markdown": api.LoaderEmpty,
	".txt": api.LoaderEmpty, ".yml": api.LoaderEmpty, ".yaml": api.LoaderEmpty, ".map": api.LoaderEmpty,
	".gyp": api.LoaderEmpty, ".gypi": api.LoaderEmpty, ".cc": api.LoaderEmpty, ".h": api.LoaderEmpty,
}

// bundleDone is the global symbol (Symbol.for) holding the promise of a
// TopLevelAwait bundle's evaluation.
const bundleDone = "og.bundleDone"

// nativesMu guards Host.natives (worker engines prepare concurrently).
var nativesMu sync.Mutex

// BuiltinsPlugin resolves Node built-ins to Oganesson's shims.
func BuiltinsPlugin() api.Plugin { return nodeshim.Plugin(nodeshim.Options{}) }

// declaresDirname matches files that define their own __dirname/__filename
// (common in ESM: const __dirname = path.dirname(fileURLToPath(import.meta.url))).
var declaresDirname = regexp.MustCompile(`(?:var|let|const|function)\s+(?:__dirname|__filename)\b`)

// replaceImportMeta substitutes import.meta.url, import.meta.dirname and
// import.meta.filename expressions (found by parsing), and not the same
// text in strings, template text or comments: Vite's define key
// "import.meta.url", and the browser code Vite generates in templates
// (`new URL(${…}, import.meta.url)`). file is the module's path as it sees
// itself (absolute at build time, "/<rel>" in a packaged program).
func replaceImportMeta(path, code, file string) string {
	if !strings.Contains(code, "import.meta.") {
		return code
	}
	values := map[string]string{
		"url":      jsonString("file://" + file),
		"dirname":  jsonString(filepath.ToSlash(filepath.Dir(file))),
		"filename": jsonString(file),
	}
	var sb strings.Builder
	last := 0
	for _, e := range ogapi.ImportMetaURLs(path, code) {
		sb.WriteString(code[last:e.Start])
		sb.WriteString(values[e.Text[strings.LastIndexByte(e.Text, '.')+1:]])
		last = e.End
	}
	sb.WriteString(code[last:])
	return sb.String()
}

// functionImport matches new Function("x", "return import(x)"): packages
// build a dynamic import that CommonJS compilation cannot turn into
// require() (e.g. @preact/preset-vite). Goja cannot compile import() in a
// Function body; the equivalent arrow function is a dynamic import the
// host performs.
var functionImport = regexp.MustCompile(`new Function\(\s*(["'])(\w+)["']\s*,\s*["']return import\(\s*(\w+)\s*\)["']\s*\)`)

// ReplaceImportMeta replaces code's import.meta.url, import.meta.dirname
// and import.meta.filename (code is the file at path, which picks the
// parser's language) with the values of the module file (its path as the
// module sees itself).
func ReplaceImportMeta(path, code, file string) string {
	return replaceImportMeta(path, code, file)
}

// LocalizeFile gives code (the file at path) its own import.meta.url,
// __dirname and __filename, as Node would, for bundling into a Goja host.
func LocalizeFile(path, code string) string {
	// A hashbang must stay the first line, where the declarations below
	// would push it (a CLI bundled as a program: Prisma 8's prisma.js).
	if strings.HasPrefix(code, "#!") {
		if i := strings.IndexByte(code, '\n'); i >= 0 {
			code = "//" + code[2:i] + code[i:]
		} else {
			code = ""
		}
	}
	code = localizeImports(path, code)
	code = localizeRequireResolve(path, code)
	code = localizeRelativeRequires(path, code)
	code = replaceImportMeta(path, code, path)
	if !declaresDirname.MatchString(code) {
		code = fmt.Sprintf("var __dirname = %s, __filename = %s;\n", jsonString(filepath.Dir(path)), jsonString(path)) + code
	}
	return code
}

var requireNames = map[string]bool{"require": true, "__require": true}

// requireResolveRe matches require.resolve("literal") (no options).
var requireResolveRe = regexp.MustCompile(`\brequire\.resolve\(\s*("[^"\n]+"|'[^'\n]+')\s*\)`)

// umdFactory matches a UMD wrapper passing require to its factory.
var umdFactory = regexp.MustCompile(`\(\s*require\s*,\s*exports\b`)

// relativeRequireRe matches require("./x") / require("../x").
var relativeRequireRe = regexp.MustCompile(`\brequire\(\s*("\.\.?/[^"\n]*"|'\.\.?/[^'\n]*')\s*\)`)

// localizeRelativeRequires resolves a literal relative require from the
// file, into its absolute path: where require is the module's, esbuild
// bundles either the same; where it is a parameter (a UMD build's
// factory(require, exports), jsonc-parser's "main", which Node loads), the
// call is left to run time, which would resolve it from the project.
func localizeRelativeRequires(path, code string) string {
	if !strings.Contains(code, "require(") {
		return code
	}
	return relativeRequireRe.ReplaceAllStringFunc(code, func(m string) string {
		lit := relativeRequireRe.FindStringSubmatch(m)[1]
		p, err := ResolveNode(lit[1:len(lit)-1], filepath.Dir(path), false)
		if err != nil || !filepath.IsAbs(p) {
			return m
		}
		return "require(" + jsonString(p) + ")"
	})
}

// localizeRequireResolve resolves require.resolve("x") as Node would from
// the file, into the path: bundled, the call has no file of its own and
// would resolve from the project (@next/mdx's require.resolve
// ("./mdx-rs-loader")). A specifier that does not resolve stays, to fail
// when it runs, as in Node.
func localizeRequireResolve(path, code string) string {
	if !strings.Contains(code, "require.resolve(") {
		return code
	}
	return requireResolveRe.ReplaceAllStringFunc(code, func(m string) string {
		lit := requireResolveRe.FindStringSubmatch(m)[1]
		spec := lit[1 : len(lit)-1]
		if strings.ContainsAny(spec, "\\") {
			return m
		}
		p, err := ResolveNode(spec, filepath.Dir(path), false)
		if err != nil || !filepath.IsAbs(p) {
			return m
		}
		return jsonString(p)
	})
}

// computedImport and computedRequire find calls whose argument does not
// start as a string literal (the only ones localizeImports changes), so
// most files are not parsed.
var (
	computedImport  = regexp.MustCompile("\\bimport\\(\\s*[^\\s\"'`)]")
	computedRequire = regexp.MustCompile("\\b(?:__)?require\\(\\s*[^\\s\"'`)]")
)

// localizeImports makes a computed import(x) or require(x) resolve a
// relative x from the file, as Node does (both resolve against the calling
// module): bundled, the call has no file of its own. The argument becomes
// globalThis[Symbol.for("og.importFrom")](x, dir) (require.go).
// Qwik's optimizer loads its WebAssembly build with import(path) of
// "../bindings/qwik.wasm.mjs".
func localizeImports(path, code string) string {
	var ims []ogapi.DynamicImport
	if computedImport.MatchString(code) {
		ims = ogapi.DynamicImports(path, code)
	}
	// A computed require(x) (or esbuild's __require(x) helper, which calls
	// require) likewise: Qwik City's static generation requires
	// "./node.cjs" so when require is a global, as it is here.
	if computedRequire.MatchString(code) {
		ims = append(ims, ogapi.RequireCalls(path, code, requireNames)...)
		sort.Slice(ims, func(i, j int) bool { return ims[i].ArgStart < ims[j].ArgStart })
	}
	if len(ims) == 0 {
		return code
	}
	var sb strings.Builder
	last := 0
	for _, im := range ims {
		if im.ArgStart < last {
			continue // inside an argument already rewritten
		}
		arg := strings.TrimSpace(code[im.ArgStart:im.ArgEnd])
		if arg == "" || ((arg[0] == '"' || arg[0] == '\'') && arg[len(arg)-1] == arg[0]) || (arg[0] == '`' && !strings.Contains(arg, "${")) {
			continue // a literal: esbuild resolves it from the file
		}
		sb.WriteString(code[last:im.ArgStart])
		sb.WriteString(`globalThis[Symbol.for("og.importFrom")](` + code[im.ArgStart:im.ArgEnd] + `, ` + jsonString(filepath.Dir(path)) + `)`)
		last = im.ArgEnd
	}
	if last == 0 {
		return code
	}
	sb.WriteString(code[last:])
	return sb.String()
}

// fileLocalsPlugin gives each source file its own __dirname, __filename and
// import.meta.url, as Node would.
func (h *Host) fileLocalsPlugin() api.Plugin {
	return api.Plugin{
		Name: "og-file-locals",
		Setup: func(b api.PluginBuild) {
			// The napi-rs WASI runtime a .wasi.cjs substitute requires is
			// loaded by the runtime require (require.go), not bundled.
			b.OnResolve(api.OnResolveOptions{Filter: "^" + regexp.QuoteMeta(napiRSWasiModule) + "$"}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
				return api.OnResolveResult{Path: a.Path, External: true}, nil
			})
			b.OnLoad(api.OnLoadOptions{Filter: `\.[cm]?[jt]sx?$`}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
				// The same native-addon substitutes as the runtime require
				// (native.go).
				if sub, ok := nativeSubstitute(a.Path); ok {
					sub = LocalizeFile(a.Path, sub)
					return api.OnLoadResult{Contents: &sub, Loader: loaderFor(a.Path), ResolveDir: filepath.Dir(a.Path)}, nil
				}
				src, err := os.ReadFile(a.Path)
				if err != nil {
					return api.OnLoadResult{}, err
				}
				code := string(src)
				if !strings.Contains(code, "import.meta.") && !strings.Contains(code, "__dirname") && !strings.Contains(code, "__filename") && !strings.Contains(code, "return import(") && !strings.Contains(code, "require.resolve(") && !umdFactory.MatchString(code) && !computedImport.MatchString(code) && !computedRequire.MatchString(code) {
					return api.OnLoadResult{}, nil // nothing to localize: esbuild's default loading
				}
				code = functionImport.ReplaceAllString(LocalizeFile(a.Path, code), "(($2) => import($2))")
				return api.OnLoadResult{Contents: &code, Loader: loaderFor(a.Path), ResolveDir: filepath.Dir(a.Path)}, nil
			})
		},
	}
}

func loaderFor(path string) api.Loader {
	switch filepath.Ext(path) {
	case ".ts", ".mts", ".cts":
		return api.LoaderTS
	case ".tsx":
		return api.LoaderTSX
	case ".jsx":
		return api.LoaderJSX
	}
	return api.LoaderJS
}

// Bundle bundles entry (a file, or stdin contents resolved from Dir) into a
// Goja-ready IIFE that assigns its exports to globalThis[globalName].
func (h *Host) Bundle(entry, stdin, globalName string) (string, error) {
	opts := h.bundleOptions(nil)
	opts.GlobalName = globalName
	if stdin != "" {
		// A shared namespace is marked as an ES module's (markESM), so a
		// default import of it gets its default export.
		var shared strings.Builder
		for i, name := range h.Shared {
			fmt.Fprintf(&shared, "import * as __og_shared%d from %q;\n", i, name)
		}
		for i, name := range h.Shared {
			fmt.Fprintf(&shared, "(globalThis[Symbol.for(%q)] ??= {})[%q] = __og_esm(__og_shared%d);\n", SharedModules, name, i)
		}
		if len(h.Shared) > 0 {
			shared.WriteString(`function __og_esm(m) {
  if (Object.isExtensible(m)) {
    if (!m.__esModule) Object.defineProperty(m, "__esModule", { value: true });
    Object.defineProperty(m, Symbol.for("og.esm"), { value: true });
  }
  return m;
}
`)
		}
		stdin = fmt.Sprintf("import %q;\n", nodeshim.GlobalsModule) + shared.String() + stdin
		opts.Stdin = &api.StdinOptions{Contents: stdin, ResolveDir: h.Dir, Loader: api.LoaderTS, Sourcefile: "og-entry.ts"}
	} else {
		opts.EntryPoints = []string{entry}
	}
	if entry == "" {
		entry = "the build host's entry"
	}
	if !h.TopLevelAwait || globalName != "" {
		return h.build(opts, entry)
	}
	// The script form unless the code awaits at its top level: ESM output
	// hoists every external import to the top, lazily evaluated modules'
	// included.
	if code, err := h.build(opts, entry); err == nil || !strings.Contains(err.Error(), "Top-level await is not available") {
		return code, err
	}
	opts.Format = api.FormatESModule
	if opts.Supported == nil {
		opts.Supported = map[string]bool{}
	}
	opts.Supported["top-level-await"] = true
	code, err := h.build(opts, entry)
	if err != nil {
		return "", err
	}
	return "globalThis[Symbol.for(\"" + bundleDone + "\")] = (async () => {\n" + requireExternals(code) + "\n})();\n", nil
}

// requireExternals turns the import declarations esbuild leaves in ESM
// output for external modules (packages left to run time) into require()
// calls, which a function body can hold, as the IIFE format writes them:
// a default import is the module's default export when it is an ES module
// (markESM), module.exports otherwise; a namespace import the module with
// that default.
func requireExternals(code string) string {
	m := ogapi.ParseModule("/og-bundle.js", code)
	if len(m.Imports) == 0 {
		return code
	}
	var sb strings.Builder
	// A module that fails to load (an optional package, imported by a
	// module the bundle may never evaluate) fails where it is used.
	sb.WriteString(`var __og_esm_of = (m) => !!m && (m[Symbol.for("og.esm")] || m.__esModule);
var __og_default = (m) => (__og_esm_of(m) ? m.default : m);
var __og_namespace = (m) => (__og_esm_of(m) ? m : Object.assign(Object.create(null), m, { default: m }));
var __og_require = (id) => {
  try {
    return require(id);
  } catch (err) {
    const fail = () => { throw err; };
    const missing = new Proxy(function () {}, { get: (t, k) => (k === Symbol.toPrimitive ? fail : missing), apply: fail, construct: fail });
    return missing;
  }
};
`)
	last := 0
	for i, im := range m.Imports {
		sb.WriteString(code[last:im.Start])
		last = im.End
		mod := fmt.Sprintf("__og_ext%d", i)
		fmt.Fprintf(&sb, "var %s = __og_require(%s);", mod, jsonString(im.Specifier))
		for _, b := range im.Bindings {
			switch b.Imported {
			case "*":
				fmt.Fprintf(&sb, " var %s = __og_namespace(%s);", b.Local, mod)
			case "default":
				fmt.Fprintf(&sb, " var %s = __og_default(%s);", b.Local, mod)
			default:
				fmt.Fprintf(&sb, " var %s = %s[%s];", b.Local, mod, jsonString(b.Imported))
			}
		}
	}
	sb.WriteString(code[last:])
	return sb.String()
}

// lenientMarker marks lenientRequirePlugin's own resolutions.
var lenientMarker = &struct{ _ byte }{}

// lenientRequirePlugin leaves a require() or import() that cannot be
// resolved to run time, where it fails only if it runs: Node loads the
// code the build host runs (next.config, tool configs and their packages)
// without bundling it, and packages guard optional requires at run time
// (Next's compiled webpack requires webpack/lib/* only with
// NEXT_PRIVATE_LOCAL_WEBPACK; nextra's config reaches it). A static import
// still fails the bundle, as it fails to link in Node.
func lenientRequirePlugin() api.Plugin {
	return api.Plugin{Name: "og-lenient-require", Setup: func(b api.PluginBuild) {
		// Resolutions in progress: another plugin resolving the same
		// request with its own marker reaches this one again, which must
		// then step aside (or the two would recurse forever).
		var inFlight sync.Map
		b.OnResolve(api.OnResolveOptions{Filter: `.*`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			if a.Kind != api.ResolveJSRequireCall && a.Kind != api.ResolveJSDynamicImport || a.PluginData == lenientMarker {
				return api.OnResolveResult{}, nil
			}
			key := a.Namespace + "\x00" + a.Importer + "\x00" + a.ResolveDir + "\x00" + a.Path
			if _, busy := inFlight.LoadOrStore(key, true); busy {
				return api.OnResolveResult{}, nil
			}
			defer inFlight.Delete(key)
			res := b.Resolve(a.Path, api.ResolveOptions{Kind: a.Kind, Importer: a.Importer, ResolveDir: a.ResolveDir, Namespace: a.Namespace, PluginData: lenientMarker, With: a.With})
			if len(res.Errors) > 0 {
				return api.OnResolveResult{Path: a.Path, External: true}, nil
			}
			out := api.OnResolveResult{Path: res.Path, External: res.External, Namespace: res.Namespace, Suffix: res.Suffix, PluginData: res.PluginData, Warnings: res.Warnings}
			if !res.SideEffects {
				out.SideEffects = api.SideEffectsFalse
			}
			return out, nil
		})
	}}
}

// bundle bundles stdin contents with extra plugins ahead of the defaults.
func (h *Host) bundle(stdin string, extra []api.Plugin) (string, error) {
	opts := h.bundleOptions(extra)
	stdin = fmt.Sprintf("import %q;\n", nodeshim.GlobalsModule) + stdin
	opts.Stdin = &api.StdinOptions{Contents: stdin, ResolveDir: h.Dir, Loader: api.LoaderTS, Sourcefile: "og-entry.ts"}
	return h.build(opts, "og-entry.ts")
}

func (h *Host) bundleOptions(extra []api.Plugin) api.BuildOptions {
	opts := api.BuildOptions{
		Bundle:        true,
		Write:         false,
		Format:        api.FormatIIFE,
		AbsWorkingDir: h.Dir,
		LogLevel:      api.LogLevelSilent,
		Plugins:       append(append(append([]api.Plugin{lenientRequirePlugin()}, extra...), h.Plugins...), BuiltinsPlugin(), h.fileLocalsPlugin()),
		// Node reads "main" only: a package's "module" build is for bundlers
		// (next-translate-plugin's references module.exports).
		MainFields: []string{"main"},
		// esbuild adds "import"/"require" per import kind, as Node does.
		Conditions: []string{"node"},
		Alias:      h.Alias,
		// Files a computed require() may name but tooling never loads as
		// code: esbuild bundles every match of require("./dir/" + x)
		// (node-pre-gyp, under Nitro's file tracer, has an index.html).
		Loader: nonCodeLoaders,
	}
	gojatarget.Apply(&opts)
	return opts
}

func (h *Host) build(opts api.BuildOptions, what string) (string, error) {
	res := api.Build(opts)
	if len(res.Errors) > 0 {
		var sb strings.Builder
		for _, e := range res.Errors {
			if e.Location != nil {
				fmt.Fprintf(&sb, "%s:%d:%d: ", e.Location.File, e.Location.Line, e.Location.Column)
			}
			sb.WriteString(e.Text + "\n")
		}
		return "", fmt.Errorf("jshost: bundling %s:\n%s", what, sb.String())
	}
	out := HonorESM(gojatarget.Finish(string(res.OutputFiles[0].Contents)))
	// Debugging aids: OG_DUMP_BUNDLE=/path writes the last bundle out,
	// OG_DUMP_BUNDLE_DIR=/dir every bundle (named by size).
	if p := os.Getenv("OG_DUMP_BUNDLE"); p != "" {
		_ = os.WriteFile(p, []byte(out), 0o644)
	}
	if d := os.Getenv("OG_DUMP_BUNDLE_DIR"); d != "" {
		_ = os.WriteFile(filepath.Join(d, fmt.Sprintf("bundle-%09d.js", len(out))), []byte(out), 0o644)
	}
	return out, nil
}

// prepare installs the build-time environment into an engine: Web APIs,
// the Node-like fs (writable inside the project), require stub and globals.
func (h *Host) prepare(e *engine.Engine) error {
	env := map[string]string{"NODE_ENV": "production"}
	for _, kv := range os.Environ() {
		if k, v, ok := strings.Cut(kv, "="); ok {
			env[k] = v
		}
	}
	for k, v := range h.Env {
		env[k] = v
	}
	// Build-time JS writes only inside the project, so its temporary
	// directory (os.tmpdir(), from TMPDIR as in Node) is the project's tool
	// cache rather than the system's.
	if _, set := h.Env["TMPDIR"]; !set && h.Dir != "" {
		tmp := filepath.Join(h.Dir, "node_modules", ".cache", "og-tmp")
		if err := os.MkdirAll(tmp, 0o755); err == nil {
			env["TMPDIR"] = tmp
		}
	}
	out := h.Out
	if out == nil {
		out = os.Stderr
	}
	if _, err := webapi.Install(e, webapi.Options{Env: env, Stdout: out, Stderr: out}); err != nil {
		return err
	}
	h.reads = newFileReads()
	if err := installFS(e.VM, h.Dir, h.reads); err != nil {
		return err
	}
	if err := h.installWorkers(e); err != nil {
		return err
	}
	if err := installEsbuild(e); err != nil {
		return err
	}
	if err := nodeshim.InstallZlib(e); err != nil {
		return err
	}
	if err := nodeshim.InstallNet(e, buildSockets); err != nil {
		return err
	}
	// The Go side of og's sharp and better-sqlite3 (native.go substitutes
	// the packages); one set of services per host.
	nativesMu.Lock()
	if h.natives == nil {
		h.natives = natives.NewServices()
	}
	svc := h.natives
	nativesMu.Unlock()
	if err := natives.Install(e, svc); err != nil {
		return err
	}
	// WebAssembly (wazero), for tools that ship wasm builds of native addons.
	w, err := wasm.Install(e.VM, wasm.Options{Env: env, Stderr: out})
	if err != nil {
		return err
	}
	e.OnClose(w.Close)
	// Bundled code only reaches the global require for dynamic ids; those
	// go through a real CommonJS loader (see require.go).
	if err := h.installRequire(e.VM); err != nil {
		return err
	}
	// Build tooling expects more of Node's process than edge code does.
	// process.stdout / stderr write to the host's output directly, as
	// Node's write to their file descriptors (not through console, which
	// loggers such as consola wrap and route back to stdout).
	write := func(s string) {
		_, _ = io.WriteString(out, s)
	}
	if err := defineSymbol(e.VM, "og.writeOut", e.VM.ToValue(write)); err != nil {
		return err
	}
	if _, err := e.VM.RunString(processExtras); err != nil {
		return err
	}
	// Dependencies that use __dirname see the project root.
	_, err = e.VM.RunString(fmt.Sprintf("process.cwd = () => %[1]s; globalThis.global = globalThis; globalThis.__dirname = %[1]s; globalThis.__filename = %[1]s + '/index.js';", jsonString(h.Dir)))
	return err
}

// AssetReads returns the node_modules asset files (CSS, fonts) that build-time
// JS read through og.fs during this host's most recent engine run. A plugin
// inlined their bytes into the output (Tailwind's preflight, a package's
// next/font/local files) without esbuild seeing them, so the license collector
// uses them to attribute the packages they belong to.
func (h *Host) AssetReads() []string { return h.reads.assets() }

// Eval runs code in a fresh engine and returns the awaited result of
// globalThis[resultExpr] converted to JSON-compatible Go values.
func (h *Host) Eval(ctx context.Context, code, resultExpr string) (any, error) {
	return h.EvalWith(ctx, code, resultExpr, nil)
}

// EvalWith is Eval with setup run on the engine first (after the host
// environment is installed), e.g. to install Go services the code calls.
func (h *Host) EvalWith(ctx context.Context, code, resultExpr string, setup func(*engine.Engine) error) (any, error) {
	e := engine.New(engine.Options{})
	defer e.Close()
	if err := h.prepare(e); err != nil {
		return nil, err
	}
	h.setup = setup
	if setup != nil {
		if err := setup(e); err != nil {
			return nil, err
		}
	}
	var result, done sobek.Value
	err := e.Run(ctx, func(vm *sobek.Runtime) error {
		if _, err := vm.RunScript("og-build.js", code); err != nil {
			return err
		}
		v, err := vm.RunString(`globalThis[Symbol.for("` + bundleDone + `")]`)
		done = v
		return err
	})
	if err != nil {
		return nil, err
	}
	// A TopLevelAwait bundle finishes evaluating first.
	if done != nil && !sobek.IsUndefined(done) {
		if _, err := e.Await(ctx, done); err != nil {
			return nil, err
		}
	}
	err = e.Run(ctx, func(vm *sobek.Runtime) error {
		v, err := vm.RunString(resultExpr)
		result = v
		return err
	})
	if err != nil {
		return nil, err
	}
	v, err := e.Await(ctx, result)
	if err != nil {
		return nil, err
	}
	// Round-trip through JSON in JS so the result is plain data.
	stringify, _ := sobek.AssertFunction(e.VM.Get("JSON").ToObject(e.VM).Get("stringify"))
	s, err := stringify(sobek.Undefined(), v)
	if err != nil {
		return nil, err
	}
	if sobek.IsUndefined(s) {
		return nil, nil
	}
	var out2 any
	if err := json.Unmarshal([]byte(s.String()), &out2); err != nil {
		return nil, err
	}
	return out2, nil
}

const processExtras = `
(function () {
  const p = globalThis.process;
  const writeOut = globalThis[Symbol.for("og.writeOut")];
  const stream = (fd) => ({
    fd, isTTY: false, columns: 80, rows: 24, writable: true,
    write(chunk, enc, cb) { writeOut(typeof chunk === "string" ? chunk : new TextDecoder().decode(chunk)); if (typeof enc === "function") enc(); else if (cb) cb(); return true; },
    on() { return this; }, once() { return this; }, off() { return this; }, end() {},
    getColorDepth() { return 1; }, hasColors() { return false; },
  });
  p.stdout ??= stream(1);
  p.stderr ??= stream(2);
  p.versions = { node: "22.0.0", ...p.versions };
  p.version = "v22.0.0";
  p.release ??= { name: "node" };
  p.arch ??= "x64";
  p.pid ??= 1;
  p.execArgv ??= [];
  p.exitCode ??= undefined;
  p.exit ??= (code) => { throw new Error("process.exit(" + (code ?? 0) + ") called by build-time JavaScript"); };
  p.hrtime ??= Object.assign((prev) => {
    const ns = BigInt(Math.round(performance.now() * 1e6));
    const t = [Number(ns / 1000000000n), Number(ns % 1000000000n)];
    if (!prev) return t;
    let s = t[0] - prev[0], n = t[1] - prev[1];
    if (n < 0) { s--; n += 1e9; }
    return [s, n];
  }, { bigint: () => BigInt(Math.round(performance.now() * 1e6)) });
  p.memoryUsage ??= Object.assign(() => ({ rss: 0, heapTotal: 0, heapUsed: 0, external: 0, arrayBuffers: 0 }), { rss: () => 0 });
  p.uptime ??= () => performance.now() / 1000;
  p.umask ??= () => 0o22;
  p.getuid ??= () => 0;
})();
`

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// buildSockets serves node:net / node:tls on the build host (a build script
// or prerendering may reach a database).
var buildSockets = nodeshim.NewSockets()
