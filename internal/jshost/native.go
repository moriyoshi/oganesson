package jshost

import (
	_ "embed"
	"fmt"
	"github.com/moriyoshi/oganesson/runtime/natives"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/evanw/esbuild/pkg/api"
	"github.com/grafana/sobek"
	"github.com/moriyoshi/oganesson/third_party/goja_nodejs/require"
)

// Native addons at build time. Oganesson cannot load .node binaries, but many
// addons also ship WebAssembly builds, which run on the WebAssembly global
// (internal/wasm). nativeSubstitute swaps a package's loader for one that
// uses them.

const (
	napiRSWasiModule = "og:napi-rs-wasi" // js/napi-rs-wasi.js
	napiWasmModule   = "og:napi-wasm"    // vendored napi-wasm
)

//go:embed js/napi-rs-wasi.js
var napiRSWasiSource string

//go:embed js/vendor/napi-wasm/index.mjs
var napiWasmSource string

var embeddedModules = map[string]string{
	napiRSWasiModule: napiRSWasiSource,
	// Node's napi_create_string_utf8 replaces invalid UTF-8 (V8's
	// NewFromUtf8); napi-wasm's fatal decoder throws instead, e.g. on
	// candidates Tailwind's oxide extracts from arbitrary file bytes.
	//
	// napi-wasm numbers environments from 0, and an addon sees its env as
	// that number; emnapi's compiled-in C (threadsafe functions, which
	// napi-rs creates at registration for its custom GC) rejects a NULL
	// env, as Node never passes one. Ids start at 1.
	napiWasmModule: strings.NewReplacer(
		`new TextDecoder("utf-8", { ignoreBOM: true, fatal: true })`, `new TextDecoder("utf-8", { ignoreBOM: true })`,
		"const environments = [];", "const environments = [undefined];",
	).Replace(napiWasmSource),
}

// nativeSubstitute returns replacement source for module files that would
// load a native addon in a way Oganesson cannot.
func nativeSubstitute(path string) (string, bool) {
	// napi-rs wasm32-wasi packages: <name>.wasi.cjs instantiates
	// <name>.wasm32-wasi.wasm through emnapi, Node's WASI and workers.
	if strings.HasSuffix(path, ".wasi.cjs") {
		wasm := strings.TrimSuffix(path, ".wasi.cjs") + ".wasm32-wasi.wasm"
		if _, err := os.Stat(wasm); err == nil {
			return fmt.Sprintf("module.exports = require(%q).load(%s);", napiRSWasiModule, jsonString(wasm)), true
		}
	}
	// lightningcss loads lightningcss-<platform> (a .node binary); its
	// WebAssembly build is the separately published lightningcss-wasm
	// (installed alongside, see internal/install), with the same exports.
	if strings.HasSuffix(path, "/lightningcss/node/index.js") {
		if src, err := os.ReadFile(path); err == nil && strings.Contains(string(src), lightningcssNative) {
			return strings.Replace(string(src), lightningcssNative, `require("lightningcss-wasm")`, 1), true
		}
	}
	// Rollup 4 loads @rollup/rollup-<platform> (a .node binary) from
	// dist/native.js; its WebAssembly build @rollup/wasm-node (installed
	// alongside, see runtime/natives) has the same native.js exports.
	if strings.HasSuffix(filepath.ToSlash(path), "/rollup/dist/native.js") {
		return `module.exports = require("@rollup/wasm-node/dist/native.js");`, true
	}
	// Next's Babel loader (a project with a Babel config) loads Next's SWC
	// bindings first, to have them for its React Compiler check; og has no
	// SWC binding and no React Compiler, so there is nothing to install.
	if strings.HasSuffix(filepath.ToSlash(path), "/next/dist/build/swc/install-bindings.js") {
		return `exports.installBindings = async function installBindings() {};`, true
	}
	// The esbuild package's JS API spawns its native binary; og's own
	// esbuild (Go) answers instead (esbuildjs.go).
	if strings.HasSuffix(filepath.ToSlash(path), "/node_modules/esbuild/lib/main.js") {
		return esbuildShim, true
	}
	// satteri (Astro 7's Markdown processor, imported eagerly by Astro's
	// config schema) is an emnapi-built addon; its WebAssembly build loads
	// through napi-rs-wasi.js. Should that load fail (a future build needing
	// more of emnapi's runtime than og provides), the error is deferred to
	// the first use, so sites that render no Markdown (or use the unified()
	// processor) still build.
	if strings.HasSuffix(filepath.ToSlash(path), "/node_modules/satteri/index.js") {
		if src, err := os.ReadFile(path); err == nil && strings.Contains(string(src), satteriThrow) {
			return strings.Replace(string(src), satteriThrow, satteriDeferred, 1), true
		}
	}
	// sharp and better-sqlite3 load native addons; build-time code gets
	// og's Go-backed substitutes, as packaged apps do (runtime/natives;
	// prepare installs their Go side). vite-imagetools (Qwik City) loads
	// sharp at build time.
	// sharp 0.35 moved its entry points to dist/index.cjs and dist/index.mjs
	// (the ES module gets the substitute's ES module source).
	slash := filepath.ToSlash(path)
	// pg (node-postgres) is CommonJS with its entry at lib/index.js; the
	// substitute replaces it with og's PostgreSQL-on-pgx module.
	for _, pkg := range []string{"sharp", "better-sqlite3", "pg"} {
		if strings.HasSuffix(slash, "/node_modules/"+pkg+"/lib/index.js") || strings.HasSuffix(slash, "/node_modules/"+pkg+"/dist/index.cjs") {
			if src, ok := nativeModuleCJS(pkg); ok {
				return src, true
			}
		}
		if strings.HasSuffix(slash, "/node_modules/"+pkg+"/dist/index.mjs") {
			if src, ok := natives.ModuleSource(pkg); ok {
				return src, true
			}
		}
	}
	// postgres (postgres.js) shares pg's Go service; its entry is src/index.js
	// (the "module" field, ES) or cjs/src/index.js (CommonJS "main").
	if strings.HasSuffix(slash, "/node_modules/postgres/src/index.js") {
		if src, ok := natives.ModuleSource("postgres"); ok {
			return src, true
		}
	}
	if strings.HasSuffix(slash, "/node_modules/postgres/cjs/src/index.js") {
		if src, ok := nativeModuleCJS("postgres"); ok {
			return src, true
		}
	}
	// mysql2 has a callback entry (index.js) and a promise entry (promise.js);
	// both get og's MySQL-on-go-sql-driver modules.
	if strings.HasSuffix(slash, "/node_modules/mysql2/index.js") {
		if src, ok := nativeModuleCJS("mysql2"); ok {
			return src, true
		}
	}
	if strings.HasSuffix(slash, "/node_modules/mysql2/promise.js") {
		if src, ok := nativeModuleCJS("mysql2/promise"); ok {
			return src, true
		}
	}
	// next/font/google downloads with node:http(s) requests, which unenv
	// only stubs; the build-time fetch (Go's net/http, honoring the proxy
	// variables as get-proxy-agent does) replaces them.
	if strings.HasSuffix(path, "/@next/font/dist/google/fetch-resource.js") {
		return fetchResourceShim, true
	}
	return "", false
}

const fetchResourceShim = `"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.fetchResource = async function fetchResource(url, isDev, errorMessage) {
  const res = await fetch(url, {
    headers: {
      "User-Agent": "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/104.0.0.0 Safari/537.36",
    },
    signal: isDev ? AbortSignal.timeout(3000) : undefined,
  });
  if (res.status !== 200) {
    throw new Error(errorMessage || "Request failed: " + url + " (status: " + res.status + ")");
  }
  return Buffer.from(await res.arrayBuffer());
};
`

var nativeCJS sync.Map // package -> CommonJS source

// nativeModuleCJS is a runtime/natives substitute as CommonJS (the
// runtime require takes substitutes as they are).
func nativeModuleCJS(pkg string) (string, bool) {
	if v, ok := nativeCJS.Load(pkg); ok {
		return v.(string), true
	}
	src, ok := natives.ModuleSource(pkg)
	if !ok {
		return "", false
	}
	res := api.Transform(src, api.TransformOptions{Format: api.FormatCommonJS, Loader: api.LoaderJS, Target: api.ES2020})
	if len(res.Errors) > 0 {
		return "", false
	}
	// require("sharp") is the function itself, as the package's CommonJS
	// is (named exports stay properties of it).
	code := string(res.Code) + "\n{ const d = module.exports.default; for (const k of Object.keys(module.exports)) if (k !== \"default\") d[k] = module.exports[k]; module.exports = d; }\n"
	nativeCJS.Store(pkg, code)
	return code, true
}

// satteriThrow is the end of satteri's napi-rs loader when no binding
// loaded; satteriDeferred exports a binding that throws that error on use.
const satteriThrow = "if (!nativeBinding) {\n  if (loadErrors.length > 0) {"

const satteriDeferred = `if (!nativeBinding) {
  const ogError = new Error("og: satteri's WebAssembly binding failed to load (see the cause); Astro's unified() Markdown processor avoids it");
  ogError.cause = loadErrors[0];
  // The loader copies the binding's exports at load: each is a stub that
  // throws when called or constructed.
  const fail = function () { throw ogError; };
  nativeBinding = new Proxy({}, { get(_, k) { return k === "then" || typeof k === "symbol" ? undefined : new Proxy(fail, { construct() { throw ogError; } }); } });
}
if (!nativeBinding) {
  if (loadErrors.length > 0) {`

const lightningcssNative = "require(`lightningcss-${parts.join('-')}`)"

// embeddedLoader bundles one of embeddedModules on first require.
func (h *Host) embeddedLoader(name string) require.ModuleLoader {
	return func(vm *sobek.Runtime, module *sobek.Object) {
		code, err := h.bundle(fmt.Sprintf("import * as m from %q; globalThis.__ogEmbedded = m;", name), []api.Plugin{embeddedPlugin()})
		if err != nil {
			panic(vm.NewGoError(err))
		}
		if _, err := vm.RunScript(name, code); err != nil {
			panic(err)
		}
		_ = module.Set("exports", vm.Get("__ogEmbedded"))
		_ = vm.GlobalObject().Delete("__ogEmbedded")
	}
}

func embeddedPlugin() api.Plugin {
	return api.Plugin{Name: "og-embedded", Setup: func(b api.PluginBuild) {
		b.OnResolve(api.OnResolveOptions{Filter: `^og:`}, func(a api.OnResolveArgs) (api.OnResolveResult, error) {
			if _, ok := embeddedModules[a.Path]; !ok {
				return api.OnResolveResult{}, nil
			}
			return api.OnResolveResult{Path: a.Path, Namespace: "og-embedded"}, nil
		})
		b.OnLoad(api.OnLoadOptions{Filter: `.*`, Namespace: "og-embedded"}, func(a api.OnLoadArgs) (api.OnLoadResult, error) {
			src := embeddedModules[a.Path]
			return api.OnLoadResult{Contents: &src, Loader: api.LoaderJS, ResolveDir: "/"}, nil
		})
	}}
}
