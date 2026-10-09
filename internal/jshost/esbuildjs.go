package jshost

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/grafana/sobek"
	"github.com/moriyoshi/oganesson/runtime/engine"
)

// The npm esbuild package's JS API (transform, build) drives a native
// binary over a pipe, which build-time JS cannot spawn. og is built on the
// same esbuild in Go: the package's lib/main.js is replaced
// (nativeSubstitute) by esbuildShim, which calls these host functions
// (Symbol.for("og.esbuild")). JS plugins in build() are not supported.

// esbuildOptions is the subset of esbuild's JS options the host maps.
type esbuildOptions struct {
	// Shared.
	Loader, Format, Platform, Charset, Sourcefile, GlobalName, LegalComments, JSX, JSXFactory, JSXFragment, JSXImportSource string
	Target                                                                                                                  any
	Minify, MinifyWhitespace, MinifyIdentifiers, MinifySyntax, KeepNames, JSXDev                                            bool
	Sourcemap                                                                                                               any
	Define                                                                                                                  map[string]string
	Pure, Drop                                                                                                              []string
	Banner, Footer                                                                                                          map[string]string
	TsconfigRaw                                                                                                             any
	// build().
	EntryPoints                                                 any
	Stdin                                                       *struct{ Contents, ResolveDir, Sourcefile, Loader string }
	Bundle, Splitting, Metafile, Write, TreeShaking             bool
	Outdir, Outfile, Outbase, AbsWorkingDir                     string
	External, MainFields, Conditions, ResolveExtensions, Inject []string
	LoaderMap                                                   map[string]string `json:"loaderMap"`
	Alias                                                       map[string]string
	EntryNames, ChunkNames, AssetNames                          string
}

var esLoaders = map[string]api.Loader{"js": api.LoaderJS, "jsx": api.LoaderJSX, "ts": api.LoaderTS, "tsx": api.LoaderTSX, "json": api.LoaderJSON, "css": api.LoaderCSS, "text": api.LoaderText, "base64": api.LoaderBase64, "dataurl": api.LoaderDataURL, "file": api.LoaderFile, "binary": api.LoaderBinary, "copy": api.LoaderCopy, "empty": api.LoaderEmpty, "default": api.LoaderDefault, "local-css": api.LoaderLocalCSS, "global-css": api.LoaderGlobalCSS}
var esFormats = map[string]api.Format{"iife": api.FormatIIFE, "cjs": api.FormatCommonJS, "esm": api.FormatESModule}
var esPlatforms = map[string]api.Platform{"browser": api.PlatformBrowser, "node": api.PlatformNode, "neutral": api.PlatformNeutral}

func (o *esbuildOptions) sourcemap() api.SourceMap {
	switch v := o.Sourcemap.(type) {
	case bool:
		if v {
			return api.SourceMapExternal
		}
	case string:
		switch v {
		case "inline":
			return api.SourceMapInline
		case "external":
			return api.SourceMapExternal
		case "both":
			return api.SourceMapInlineAndExternal
		case "linked":
			return api.SourceMapLinked
		}
	}
	return api.SourceMapNone
}

func (o *esbuildOptions) targets() ([]api.Engine, api.Target) {
	var list []string
	switch v := o.Target.(type) {
	case string:
		list = strings.Split(v, ",")
	case []any:
		for _, x := range v {
			list = append(list, fmt.Sprint(x))
		}
	}
	targets := map[string]api.Target{"es2015": api.ES2015, "es2016": api.ES2016, "es2017": api.ES2017, "es2018": api.ES2018, "es2019": api.ES2019, "es2020": api.ES2020, "es2021": api.ES2021, "es2022": api.ES2022, "es2023": api.ES2023, "es2024": api.ES2024, "esnext": api.ESNext}
	names := map[string]api.EngineName{"chrome": api.EngineChrome, "edge": api.EngineEdge, "firefox": api.EngineFirefox, "safari": api.EngineSafari, "ios": api.EngineIOS, "node": api.EngineNode, "deno": api.EngineDeno, "opera": api.EngineOpera}
	var engines []api.Engine
	target := api.DefaultTarget
	for _, t := range list {
		t = strings.ToLower(strings.TrimSpace(t))
		if es, ok := targets[t]; ok {
			target = es
			continue
		}
		for name, e := range names {
			if strings.HasPrefix(t, name) && len(t) > len(name) {
				engines = append(engines, api.Engine{Name: e, Version: t[len(name):]})
				break
			}
		}
	}
	return engines, target
}

func (o *esbuildOptions) tsconfigRaw() string {
	switch v := o.TsconfigRaw.(type) {
	case string:
		return v
	case nil:
		return ""
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func (o *esbuildOptions) jsx() api.JSX {
	switch o.JSX {
	case "automatic":
		return api.JSXAutomatic
	case "preserve":
		return api.JSXPreserve
	}
	return api.JSXTransform
}

func messages(vm *sobek.Runtime, ms []api.Message) []any {
	out := make([]any, 0, len(ms))
	for _, m := range ms {
		r := map[string]any{"id": m.ID, "pluginName": m.PluginName, "text": m.Text, "location": nil, "notes": []any{}, "detail": nil}
		if l := m.Location; l != nil {
			r["location"] = map[string]any{"file": l.File, "namespace": l.Namespace, "line": l.Line, "column": l.Column, "length": l.Length, "lineText": l.LineText, "suggestion": l.Suggestion}
		}
		out = append(out, r)
	}
	return out
}

// installEsbuild exposes esbuild's transform and build to build-time JS.
func installEsbuild(e *engine.Engine) error {
	vm := e.VM
	o := vm.NewObject()
	_ = o.Set("version", esbuildVersion())
	_ = o.Set("transform", func(code, optsJSON string) sobek.Value {
		var opts esbuildOptions
		if err := json.Unmarshal([]byte(optsJSON), &opts); err != nil {
			panic(vm.NewGoError(err))
		}
		engines, target := opts.targets()
		to := api.TransformOptions{
			Loader: esLoaders[opts.Loader], Format: esFormats[opts.Format], Platform: esPlatforms[opts.Platform], Sourcefile: opts.Sourcefile,
			GlobalName: opts.GlobalName, Target: target, Engines: engines, Define: opts.Define, Pure: opts.Pure, KeepNames: opts.KeepNames,
			MinifyWhitespace: opts.Minify || opts.MinifyWhitespace, MinifyIdentifiers: opts.Minify || opts.MinifyIdentifiers, MinifySyntax: opts.Minify || opts.MinifySyntax,
			Sourcemap: opts.sourcemap(), TsconfigRaw: opts.tsconfigRaw(), JSX: opts.jsx(), JSXFactory: opts.JSXFactory, JSXFragment: opts.JSXFragment, JSXImportSource: opts.JSXImportSource, JSXDev: opts.JSXDev,
			Banner: opts.Banner["js"], Footer: opts.Footer["js"],
		}
		if opts.Charset == "utf8" {
			to.Charset = api.CharsetUTF8
		}
		if opts.Loader == "" {
			to.Loader = api.LoaderJS
		}
		res := api.Transform(code, to)
		return vm.ToValue(map[string]any{"code": string(res.Code), "map": string(res.Map), "errors": messages(vm, res.Errors), "warnings": messages(vm, res.Warnings)})
	})
	_ = o.Set("build", func(optsJSON string) sobek.Value {
		var opts esbuildOptions
		if err := json.Unmarshal([]byte(optsJSON), &opts); err != nil {
			panic(vm.NewGoError(err))
		}
		engines, target := opts.targets()
		bo := api.BuildOptions{
			Bundle: opts.Bundle, Splitting: opts.Splitting, Metafile: opts.Metafile, Write: opts.Write, Outdir: opts.Outdir, Outfile: opts.Outfile, Outbase: opts.Outbase,
			AbsWorkingDir: opts.AbsWorkingDir, External: opts.External, MainFields: opts.MainFields, Conditions: opts.Conditions, ResolveExtensions: opts.ResolveExtensions,
			Inject: opts.Inject, Alias: opts.Alias, EntryNames: opts.EntryNames, ChunkNames: opts.ChunkNames, AssetNames: opts.AssetNames,
			Format: esFormats[opts.Format], Platform: esPlatforms[opts.Platform], GlobalName: opts.GlobalName, Target: target, Engines: engines,
			Define: opts.Define, Pure: opts.Pure, KeepNames: opts.KeepNames, Sourcemap: opts.sourcemap(), TsconfigRaw: opts.tsconfigRaw(),
			MinifyWhitespace: opts.Minify || opts.MinifyWhitespace, MinifyIdentifiers: opts.Minify || opts.MinifyIdentifiers, MinifySyntax: opts.Minify || opts.MinifySyntax,
			JSX: opts.jsx(), JSXFactory: opts.JSXFactory, JSXFragment: opts.JSXFragment, JSXImportSource: opts.JSXImportSource, JSXDev: opts.JSXDev,
			Banner: opts.Banner, Footer: opts.Footer, LogLevel: api.LogLevelSilent,
		}
		if opts.Charset == "utf8" {
			bo.Charset = api.CharsetUTF8
		}
		if len(opts.LoaderMap) > 0 {
			bo.Loader = map[string]api.Loader{}
			for ext, l := range opts.LoaderMap {
				bo.Loader[ext] = esLoaders[l]
			}
		}
		switch v := opts.EntryPoints.(type) {
		case []any:
			for _, x := range v {
				if s, ok := x.(string); ok {
					bo.EntryPoints = append(bo.EntryPoints, s)
				} else if m, ok := x.(map[string]any); ok {
					bo.EntryPointsAdvanced = append(bo.EntryPointsAdvanced, api.EntryPoint{InputPath: fmt.Sprint(m["in"]), OutputPath: fmt.Sprint(m["out"])})
				}
			}
		case map[string]any:
			for out, in := range v {
				bo.EntryPointsAdvanced = append(bo.EntryPointsAdvanced, api.EntryPoint{InputPath: fmt.Sprint(in), OutputPath: out})
			}
		}
		if s := opts.Stdin; s != nil {
			bo.Stdin = &api.StdinOptions{Contents: s.Contents, ResolveDir: s.ResolveDir, Sourcefile: s.Sourcefile, Loader: esLoaders[s.Loader]}
			if s.Loader == "" {
				bo.Stdin.Loader = api.LoaderJS
			}
		}
		res := api.Build(bo)
		files := make([]any, 0, len(res.OutputFiles))
		for _, f := range res.OutputFiles {
			files = append(files, map[string]any{"path": f.Path, "contents": vm.NewArrayBuffer(f.Contents), "hash": f.Hash})
		}
		out := map[string]any{"errors": messages(vm, res.Errors), "warnings": messages(vm, res.Warnings), "outputFiles": files, "metafile": nil}
		if res.Metafile != "" {
			out["metafile"] = res.Metafile
		}
		return vm.ToValue(out)
	})
	return defineSymbol(vm, "og.esbuild", o)
}

// esbuildShim replaces the esbuild package's lib/main.js.
const esbuildShim = `"use strict";
const host = globalThis[Symbol.for("og.esbuild")];
const pkgVersion = (() => { try { return require("../package.json").version; } catch { return host.version; } })();
function fail(result) {
  if (!result.errors.length) return;
  const e = new Error("Build failed with " + result.errors.length + " error" + (result.errors.length > 1 ? "s" : "") + ":\n" + result.errors.map((m) => (m.location ? m.location.file + ":" + m.location.line + ":" + m.location.column + ": " : "") + "ERROR: " + m.text).join("\n"));
  e.errors = result.errors;
  e.warnings = result.warnings;
  throw e;
}
function normalize(options) {
  const o = Object.assign({}, options);
  if (o.plugins && o.plugins.length) throw new Error("og: esbuild JS plugins are not supported by Oganesson's esbuild substitute");
  if (o.loader && typeof o.loader === "object") { o.loaderMap = o.loader; delete o.loader; }
  if (o.tsconfigRaw && typeof o.tsconfigRaw === "object") o.tsconfigRaw = JSON.stringify(o.tsconfigRaw);
  for (const k of ["define"]) if (o[k]) o[k] = Object.fromEntries(Object.entries(o[k]).map(([a, b]) => [a, String(b)]));
  if (o.banner && typeof o.banner === "string") o.banner = { js: o.banner };
  if (o.footer && typeof o.footer === "string") o.footer = { js: o.footer };
  if (o.write === undefined) o.write = true;
  return o;
}
function transformSync(input, options = {}) {
  if (typeof input !== "string") input = Buffer.from(input).toString();
  const r = host.transform(input, JSON.stringify(normalize(Object.assign({}, options, { write: false }))));
  fail(r);
  return { code: r.code, map: r.map, warnings: r.warnings, mangleCache: undefined, legalComments: undefined };
}
function buildSync(options = {}) {
  const r = host.build(JSON.stringify(normalize(options)));
  fail(r);
  const outputFiles = r.outputFiles.map((f) => {
    const contents = new Uint8Array(f.contents);
    return { path: f.path, contents, hash: f.hash, get text() { return new TextDecoder().decode(contents); } };
  });
  return { errors: [], warnings: r.warnings, outputFiles: options.write === false ? outputFiles : undefined, metafile: r.metafile ? JSON.parse(r.metafile) : undefined, mangleCache: undefined };
}
function formatMessagesSync(messages, options = {}) {
  return messages.map((m) => (m.location ? m.location.file + ":" + m.location.line + ":" + m.location.column + ": " : "") + (options.kind === "warning" ? "WARNING: " : "ERROR: ") + m.text + "\n");
}
const later = (fn) => (...args) => new Promise((resolve, reject) => queueMicrotask(() => { try { resolve(fn(...args)); } catch (e) { reject(e); } }));
module.exports = {
  version: pkgVersion,
  transform: later(transformSync),
  transformSync,
  build: later(buildSync),
  buildSync,
  formatMessages: later(formatMessagesSync),
  formatMessagesSync,
  analyzeMetafile: async (m) => (typeof m === "string" ? m : JSON.stringify(m, null, 2)),
  analyzeMetafileSync: (m) => (typeof m === "string" ? m : JSON.stringify(m, null, 2)),
  context: async () => { throw new Error("og: esbuild.context() is not supported by Oganesson's esbuild substitute"); },
  initialize: async () => {},
  stop: async () => {},
};
`
