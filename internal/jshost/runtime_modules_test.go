package jshost

import (
	"bytes"
	"context"
	"image"
	pngenc "image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRuntimeModules covers modules loaded at run time (computed import()
// and require, as tools load configs and plugins): ES modules declaring
// the CommonJS wrapper's names, package "imports", default imports
// between converted ES modules, modules the bundle shares, and Patch.
func TestRuntimeModules(t *testing.T) {
	dir := t.TempDir()
	write := func(name, src string) {
		p := filepath.Join(dir, name)
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// const __dirname at the top of an ES module (a SyntaxError once the
	// module is a CommonJS wrapper's body, unless shadowed).
	write("lib/dirname.mjs", `import path from "node:path"; import { fileURLToPath } from "node:url";
const __dirname = path.dirname(fileURLToPath(import.meta.url));
export const base = path.basename(__dirname);`)
	// "#name" from the package's "imports", with conditions, through
	// createRequire (require.resolve's resolver) and a CommonJS module's
	// own require (goja_nodejs's lookup).
	write("node_modules/imp/package.json", `{"name":"imp","imports":{"#flag":{"module-sync":"./yes.mjs","default":"./no.cjs"},"#sub/*":"./sub/*.js"},"exports":{".":"./index.mjs","./cjs":"./cjs.cjs"}}`)
	write("node_modules/imp/cjs.cjs", `module.exports = require("#sub/x") + "/cjs";`)
	write("node_modules/imp/no.cjs", `exports.default = false;`)
	write("node_modules/imp/yes.mjs", `export default true;`)
	write("node_modules/imp/sub/x.js", `module.exports = "sub-x";`)
	write("node_modules/imp/index.mjs", `import { createRequire } from "node:module";
const require = createRequire(import.meta.url);
export const flag = require("#flag").default;
export const sub = require("#sub/x");`)
	// An ES module's default import of another ES module (converted to
	// CommonJS, imported in Node mode from an .mjs file).
	write("lib/plugin.mjs", `export default function plugin() { return "plugin-default"; }`)
	write("lib/config.mjs", `import plugin from "./plugin.mjs"; export default plugin();`)
	// A "type": "module" package's .js file default-imports a CommonJS
	// module that sets __esModule but has no default: Node gives
	// module.exports (caniuse-api from postcss-reduce-initial).
	write("node_modules/esm-pkg/package.json", `{"name":"esm-pkg","type":"module","exports":"./index.js"}`)
	write("node_modules/esm-pkg/index.js", `import api from "babel-cjs"; export const supported = api.isSupported();`)
	write("node_modules/babel-cjs/package.json", `{"name":"babel-cjs","main":"index.js"}`)
	write("node_modules/babel-cjs/index.js", `Object.defineProperty(exports, "__esModule", { value: true }); exports.isSupported = () => "yes";`)
	// An ES module importing a dual package gets its import build.
	write("node_modules/dual/package.json", `{"name":"dual","exports":{"./plugin":{"import":"./plugin.mjs","require":"./plugin.cjs"}}}`)
	write("node_modules/dual/plugin.mjs", `export default function plugin() { return "esm-plugin"; }`)
	write("node_modules/dual/plugin.cjs", `Object.defineProperty(exports, "__esModule", { value: true }); exports.default = function plugin() { return "cjs-plugin"; };`)
	write("node_modules/esm-pkg/uses-dual.js", `import plugin from "dual/plugin"; export const which = typeof plugin === "function" ? plugin() : typeof plugin;`)
	// Patch.
	write("lib/patched.js", `module.exports = "unpatched";`)
	// A package the bundle contains and shares.
	write("node_modules/shared/package.json", `{"name":"shared","main":"index.js"}`)
	write("node_modules/shared/index.js", `exports.id = Math.random();`)
	// A shared ES package, default-imported by a module loaded at run time
	// (SvelteKit's reloaded config imports its adapter this way).
	write("node_modules/shared-esm/package.json", `{"name":"shared-esm","type":"module","exports":"./index.js"}`)
	write("node_modules/shared-esm/index.js", `export default function adapter() { return "adapter"; } export const named = 1; export const token = {};`)
	write("lib/uses-shared.mjs", `import adapter, { named, token } from "shared-esm"; export const out = adapter() + named; export { token };`)

	h := &Host{Dir: dir, Out: io.Discard, Shared: []string{"shared-esm"}, Patch: func(path, src string) (string, error) {
		if strings.HasSuffix(path, "/lib/patched.js") {
			return strings.Replace(src, "unpatched", "patched", 1), nil
		}
		return src, nil
	}}
	code, err := h.Bundle("", `
		import * as shared from "shared";
		import * as sharedEsm from "shared-esm";
		globalThis[Symbol.for("og.sharedModules")].shared = shared;
		const load = (id) => require([id][0]);
		globalThis.result = [
			load(__dirname + "/lib/dirname.mjs").base,
			load("imp").flag,
			load("imp").sub,
			load("imp/cjs"),
			load(__dirname + "/lib/config.mjs").default,
			load(__dirname + "/lib/patched.js"),
			// the bundle's own instance, by name and by its file's URL
			load("shared") === shared,
			load("file://" + __dirname + "/node_modules/shared/index.js") === shared,
			load(__dirname + "/lib/uses-shared.mjs").out,
			load("esm-pkg").supported,
			load(__dirname + "/node_modules/esm-pkg/uses-dual.js").which,
			// the bundle's instance, through a default import
			load(__dirname + "/lib/uses-shared.mjs").token === sharedEsm.token,
		].join("|");
	`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "result")
	if err != nil {
		t.Fatal(err)
	}
	if want := "lib|false|sub-x|sub-x/cjs|plugin-default|patched|true|true|adapter1|yes|esm-plugin|true"; v != want {
		t.Fatalf("got  %v\nwant %s", v, want)
	}
}

// TestRequireFailuresAreCatchable: a native addon throws Node's
// ERR_DLOPEN_FAILED (Qwik's optimizer catches it and falls back to its
// WebAssembly build), and a module that does not parse throws a
// SyntaxError, both catchable, as in Node (Node 24 gives
// "ERR_DLOPEN_FAILED:Error|SyntaxError:true"); before, both were Go panics
// out of the engine.
func TestRequireFailuresAreCatchable(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "addon.node"), []byte("\x7fELF binary"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad.js"), []byte("module.exports = {;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Host{Dir: dir}
	code, err := h.Bundle("", `
const out = [];
const req = eval("require");
try { req(`+jsonString(filepath.Join(dir, "addon.node"))+`); } catch (e) { out.push(e.code + ":" + e.constructor.name); }
try { req(`+jsonString(filepath.Join(dir, "bad.js"))+`); } catch (e) { out.push(e.name + ":" + (e instanceof SyntaxError)); }
globalThis.result = out.join("|");
`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "result")
	if err != nil {
		t.Fatal(err)
	}
	if want := "ERR_DLOPEN_FAILED:Error|SyntaxError:true"; v != want {
		t.Errorf("got %v, want %s", v, want)
	}
}

// TestComputedImportFromFile: a computed import(x) / require(x) in a
// bundled package module resolves a relative x from that module's file, as
// in Node (Qwik's optimizer imports "../bindings/qwik.wasm.mjs", Qwik
// City's static generation requires "./node.cjs"); bundled, the calls had
// no file of their own. Node 24 gives {"value":"data"},data.
func TestComputedImportFromFile(t *testing.T) {
	dir := t.TempDir()
	lib := filepath.Join(dir, "node_modules", "pkg", "lib")
	if err := os.MkdirAll(lib, 0o755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"node_modules/pkg/package.json":  `{"name":"pkg","version":"1.0.0","main":"lib/index.cjs","exports":{".":{"import":"./lib/index.mjs","require":"./lib/index.cjs"}}}`,
		"node_modules/pkg/lib/index.mjs": `export const viaImport = (p) => import(p).then((m) => m.default ?? m.value);`,
		"node_modules/pkg/lib/index.cjs": `module.exports.viaRequire = (p) => require(p).value;`,
		"node_modules/pkg/lib/data.cjs":  `module.exports = { value: "data" };`,
	}
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, filepath.FromSlash(name)), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := &Host{Dir: dir}
	code, err := h.Bundle("", `
import { viaImport } from "pkg";
const cjs = require("pkg");
const name = "./data.cjs";
globalThis.__ogResult = (async () => [JSON.stringify(await viaImport(name)), cjs.viaRequire(name)].join(","))();
`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "__ogResult")
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"value":"data"},data`; v != want {
		t.Errorf("got %v, want %s", v, want)
	}
}

// TestBuildTimeSharp: build-time code requiring sharp gets og's Go-backed
// sharp (runtime/natives), as packaged apps do: the package itself loads a
// native addon (vite-imagetools, which Qwik City runs, loads it). Its
// metadata has sharp 0.34's fields (imagetools reads autoOrient), and
// require("sharp") is the function, as the package's CommonJS is.
func TestBuildTimeSharp(t *testing.T) {
	// sharp 0.34's layout (lib/index.js) and 0.35's (dist/index.cjs and
	// dist/index.mjs behind "exports"): every real entry throws.
	for _, layout := range []struct {
		version string
		files   map[string]string
	}{
		{"0.34.5", map[string]string{
			"package.json": `{"name":"sharp","version":"0.34.5","main":"lib/index.js"}`,
			"lib/index.js": `throw new Error("the native sharp loaded");`,
		}},
		{"0.35.5", map[string]string{
			"package.json":   `{"name":"sharp","version":"0.35.5","main":"./dist/index.cjs","exports":{".":{"import":"./dist/index.mjs","require":"./dist/index.cjs"}}}`,
			"dist/index.cjs": `throw new Error("the native sharp loaded");`,
			"dist/index.mjs": `throw new Error("the native sharp loaded");`,
			"dist/sharp.cjs": `require("../src/build/Release/sharp-" + process.platform + ".node");`,
		}},
	} {
		t.Run(layout.version, func(t *testing.T) { buildTimeSharp(t, layout.files) })
	}
}

func buildTimeSharp(t *testing.T, files map[string]string) {
	dir := t.TempDir()
	for name, src := range files {
		p := filepath.Join(dir, "node_modules", "sharp", filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	img := image.NewNRGBA(image.Rect(0, 0, 4, 2))
	for i := range img.Pix {
		img.Pix[i] = 200
	}
	var png bytes.Buffer
	if err := pngenc.Encode(&png, img); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "a.png"), png.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Host{Dir: dir}
	code, err := h.Bundle("", `
import sharp from "sharp";
import { readFileSync } from "node:fs";
const req = eval("require");
globalThis.__ogResult = (async () => {
  const input = readFileSync(`+jsonString(filepath.Join(dir, "a.png"))+`);
  const m = await sharp(input).metadata();
  const webp = await sharp(input).resize(2).webp().toBuffer();
  const runtime = req("sharp");
  return [typeof runtime, runtime === sharp || typeof runtime.default, m.format, m.width, m.height, m.channels, m.depth, m.hasAlpha, JSON.stringify(m.autoOrient), m.isPalette, webp.subarray(0, 4).toString(), webp.subarray(8, 12).toString()].join(",");
})();
`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "__ogResult")
	if err != nil {
		t.Fatal(err)
	}
	if want := `function,undefined,png,4,2,4,uchar,true,{"width":4,"height":2},false,RIFF,WEBP`; v != want {
		t.Errorf("got  %v\nwant %s", v, want)
	}
}

// TestNodeLoadingRules: build-time code (next.config and what it requires)
// loads as Node loads it, though og bundles it: a package's "main", not its
// bundler-only "module" build (next-translate-plugin's breaks under Node);
// require.resolve("./x") from the requiring file (@next/mdx's loaders); a
// require() of a file an installed package lacks failing only when it
// runs (nextra reaches Next's compiled webpack, whose optional requires of
// its own missing files are guarded); a UMD build's relative requires
// through its factory's require parameter (jsonc-parser, which Astro
// loads); and process.report (detect-libc reads it). The expected output is
// Node 24's for the same files.
func TestNodeLoadingRules(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"node_modules/pkg/package.json":  `{"name":"pkg","version":"1.0.0","main":"cjs/index.js","module":"esm/index.js"}`,
		"node_modules/pkg/cjs/index.js":  "exports.build = \"main\";\nexports.loader = require.resolve(\"./loader\");\nexports.optional = () => require(\"pkg/cjs/not-a-file\");\n",
		"node_modules/pkg/cjs/loader.js": "module.exports = 1;\n",
		"node_modules/pkg/esm/index.js":  "module.exports = { build: \"module\" };\n",
		"node_modules/umd/package.json":  `{"name":"umd","version":"1.0.0","main":"./lib/main.js","module":"./lib/esm.js"}`,
		"node_modules/umd/lib/main.js":   "(function (factory) {\n  if (typeof module === \"object\" && typeof module.exports === \"object\") {\n    var v = factory(require, exports);\n    if (v !== undefined) module.exports = v;\n  }\n})(function (require, exports) {\n  exports.impl = require(\"./impl/x\").name;\n});\n",
		"node_modules/umd/lib/impl/x.js": "exports.name = \"umd impl\";\n",
		"node_modules/umd/lib/esm.js":    "export const impl = \"module build\";\n",
	}
	for name, src := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	real, err := filepath.EvalSymlinks(dir) // require.resolve gives real paths, as Node's
	if err != nil {
		t.Fatal(err)
	}
	h := &Host{Dir: dir}
	code, err := h.Bundle("", `
const pkg = require("pkg");
const path = require("path");
let optional;
try { pkg.optional(); } catch (e) { optional = e.code; }
const report = process.report.getReport();
globalThis.__ogResult = [pkg.build, path.relative(`+jsonString(real)+`, pkg.loader), optional, typeof report.header.nodejsVersion, Array.isArray(report.sharedObjects), require("umd").impl].join(",");
`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "__ogResult")
	if err != nil {
		t.Fatal(err)
	}
	if want := "main,node_modules/pkg/cjs/loader.js,MODULE_NOT_FOUND,string,true,umd impl"; v != want {
		t.Errorf("got %v, want %s", v, want)
	}
}

// TestModuleCompile: module._compile runs CommonJS source as a module of a
// file (require-from-string, which preval.macro uses under
// babel-plugin-macros). The expected output is Node 24's.
func TestModuleCompile(t *testing.T) {
	h := &Host{Dir: t.TempDir()}
	code, err := h.Bundle("", `
const Module = require("module");
const out = [];
const parent = { children: [] };
const m = new Module("/tmp/virtual.js", parent);
m.filename = "/tmp/virtual.js";
m.paths = Module._nodeModulePaths("/tmp");
m._compile("#!/usr/bin/env node\nmodule.exports = { name: __filename, dir: __dirname, self: this === exports, path: require(\"path\").basename(__filename) };", "/tmp/virtual.js");
out.push(JSON.stringify(m.exports), m.loaded, m.id, parent.children.includes(m));
globalThis.__ogResult = out.join(",");
`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "__ogResult")
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"name":"/tmp/virtual.js","dir":"/tmp","self":true,"path":"virtual.js"},false,/tmp/virtual.js,true`; v != want {
		t.Errorf("got %v, want %s", v, want)
	}
}

// TestModuleResolveFilename: Module._resolveFilename resolves as the
// parent's require does, or from options.paths (resolve-from, through
// import-fresh: Lingui's config loading). The expected output is Node 24's.
func TestModuleResolveFilename(t *testing.T) {
	dir := t.TempDir()
	for name, src := range map[string]string{
		"sub/x.js":                          "module.exports = 1;",
		"sub/node_modules/dep/package.json": `{"name":"dep","main":"main.js"}`,
		"sub/node_modules/dep/main.js":      "module.exports = 2;",
	} {
		p := filepath.Join(dir, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(src), 0o644)
	}
	real, _ := filepath.EvalSymlinks(dir)
	h := &Host{Dir: dir}
	code, err := h.Bundle("", `
const Module = require("module");
const path = require("path");
const root = `+jsonString(real)+`;
const from = path.join(root, "sub", "index.js");
const parent = { id: from, filename: from, paths: Module._nodeModulePaths(path.dirname(from)) };
const rel = (p) => path.relative(root, p);
globalThis.__ogResult = [rel(Module._resolveFilename("./x", parent)), rel(Module._resolveFilename("dep", parent)), rel(Module._resolveFilename("dep", null, false, { paths: [path.join(root, "sub")] }))].join(",");
`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "__ogResult")
	if err != nil {
		t.Fatal(err)
	}
	if want := "sub/x.js,sub/node_modules/dep/main.js,sub/node_modules/dep/main.js"; v != want {
		t.Errorf("got %v, want %s", v, want)
	}
}

// TestRunProgramTopLevelAwait: a program that is an ES module awaiting at
// its top level (Prisma 8's CLI) runs as Node runs it: its hashbang, argv,
// its own import.meta.url, its exit code. Node 24 writes "hello a b dist" and
// exits with 3.
func TestRunProgramTopLevelAwait(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"node_modules/tlacli/package.json": `{"name":"tlacli","version":"1.0.0","type":"module","bin":"dist/cli.js"}`,
		"node_modules/tlacli/dist/cli.js": `#!/usr/bin/env node
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
const config = await Promise.resolve({ greeting: "hello" });
const here = path.dirname(fileURLToPath(import.meta.url));
fs.writeFileSync(path.join(process.cwd(), "out.txt"), ` + "`${config.greeting} ${process.argv.slice(2).join(\" \")} ${path.basename(here)}`" + `);
process.exitCode = 3;
`,
	}
	for name, src := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, []byte(src), 0o644)
	}
	h := &Host{Dir: dir}
	code, err := h.RunProgram(context.Background(), filepath.Join(dir, "node_modules/tlacli/dist/cli.js"), []string{"a", "b"})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := os.ReadFile(filepath.Join(dir, "out.txt"))
	if code != 3 || string(out) != "hello a b dist" {
		t.Errorf("exit %d, wrote %q; want 3, %q", code, out, "hello a b dist")
	}
}

// TestKeepNames: modules required at run time keep their function names,
// as in Node: esbuild's transform renames a function expression named like
// its variable (Stylus's nodes: var Root = module.exports = function Root()
// {}; its compiler dispatches on constructor.name). Code rebuilt from a
// function's text in the same realm finds esbuild's __name helper.
func TestKeepNames(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"node_modules/names/package.json": `{"name":"names","version":"1.0.0"}`,
		"node_modules/names/dyn.js":       "var Root = module.exports = function Root() {};\nmodule.exports.outer = function outer() { function inner() {} return inner; };\n",
	}
	for name, src := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h := &Host{Dir: dir}
	code, err := h.Bundle("", `
const dynPath = `+jsonString(filepath.Join(dir, "node_modules/names/dyn.js"))+`;
const Dyn = globalThis.require(dynPath);
const rebuilt = new Function("return (" + Dyn.outer.toString() + ")")()().name;
globalThis.__ogResult = [Dyn.name, new Dyn().constructor.name, rebuilt].join(",");
`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "__ogResult")
	if err != nil {
		t.Fatal(err)
	}
	if want := "Root,Root,inner"; v != want { // Node 24
		t.Errorf("names: %v, want %s", v, want)
	}
}

// TestModuleObject: a required module's module object has what Node's has
// besides exports: module.require (relative to the module; GraphQL Mesh's
// CLI calls it), id / filename, path, paths, loaded and children. The
// expected output is Node 24's for the same files.
func TestModuleObject(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "node_modules", "m", "index.js")
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	src := `exports.r = [typeof module.require, module.require("path").sep, module.id === module.filename, require("path").basename(module.filename), require("path").basename(module.path), Array.isArray(module.paths), module.loaded, Array.isArray(module.children)];` + "\n"
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Host{Dir: dir}
	code, err := h.Bundle("", `globalThis.__ogResult = JSON.stringify(globalThis.require(`+jsonString(p)+`).r);`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "__ogResult")
	if err != nil {
		t.Fatal(err)
	}
	if want := `["function","/",true,"index.js","m",true,false,true]`; v != want {
		t.Errorf("got  %v\nwant %s", v, want)
	}
}

// TestModulePreload: Module._preloadModules requires each request from the
// working directory, as node --require does (GraphQL Mesh's CLI); anything
// but an array preloads nothing. The expected output is Node 24's.
func TestModulePreload(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "p.js"), []byte("globalThis.__preloaded = (globalThis.__preloaded || 0) + 1;\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	h := &Host{Dir: dir}
	code, err := h.Bundle("", `
const M = require("module");
M._preloadModules(["./p.js"]);
M._preloadModules([]);
let err = "";
try { M._preloadModules(undefined); } catch (e) { err = e.code; }
globalThis.__ogResult = [globalThis.__preloaded, err, typeof M._preloadModules].join("|");
`, "")
	if err != nil {
		t.Fatal(err)
	}
	v, err := h.Eval(context.Background(), code, "__ogResult")
	if err != nil {
		t.Fatal(err)
	}
	if want := "1||function"; v != want {
		t.Errorf("got %v, want %s", v, want)
	}
}
